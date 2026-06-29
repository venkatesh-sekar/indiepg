package server

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/alert"
	"github.com/venkatesh-sekar/indiepg/internal/backup"
	"github.com/venkatesh-sekar/indiepg/internal/config"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/scheduler"
)

// Scheduler job names.
const (
	// telemetrySampleJob is the combined telemetry sampling + alert evaluation loop.
	telemetrySampleJob = "telemetry-sample"
	// fullBackupJob / incrementalBackupJob are the scheduled pgBackRest backups.
	// Without these registered, the panel only ever backs up when an operator
	// clicks "Run backup" — a box left alone makes zero backups, the single
	// biggest data-loss gap for the north star ("never lose data").
	fullBackupJob        = "full-backup"
	incrementalBackupJob = "incremental-backup"
	// restoreTestJob is the scheduled restore verification. Without it registered,
	// `pgbackrest verify` only ever ran when an operator clicked "Test a restore",
	// so a left-alone box's "have my backups been proven recoverable?" status could
	// sit at "never" forever while the repo silently rotted (a bit-flip, a
	// truncated WAL). Scheduling it closes the loop on "backups proven restorable".
	restoreTestJob = "restore-test"
	// dropoffSweepJob deletes the S3 objects of expired drop-off sessions (a full
	// database at rest must not linger past its TTL) and marks them expired.
	dropoffSweepJob = "dropoff-sweep"
)

// dropoffSweepSchedule is the (fixed) cadence of the drop-off expiry sweep. It is
// not operator-tunable: a full database dump sitting in S3 past its short TTL is a
// data-at-rest concern, so it is swept frequently regardless of config.
const dropoffSweepSchedule = "@every 30m"

// seedTimeout bounds the one-shot default-rule seed at startup so a slow or hung
// store can never delay the server from accepting its first connection. Mirrors
// the bounded connect context used in New.
const seedTimeout = 30 * time.Second

// startBackgroundJobs seeds the default alert rules (idempotently) and starts the
// telemetry sampling + alert evaluation loop on the configured cadence. It is the
// production seam that finally makes the alert subsystem live: without it the
// engine, collector, and rules exist but never run, so no alert can ever fire.
//
// It is best-effort and never fatal: a seeding error or a bad cron spec only logs
// (and disables the loop) so the panel still serves. ctx governs the loop's
// lifetime — the scheduler stops when ctx is cancelled.
func (s *Server) startBackgroundJobs(ctx context.Context) {
	// Bound the one-shot seed: it runs before the serve goroutine, so a hung store
	// here would otherwise stall the listener from answering its first request.
	seedCtx, cancel := context.WithTimeout(ctx, seedTimeout)
	if err := s.seedDefaultAlertRules(seedCtx); err != nil {
		s.log.Warn("could not seed default alert rules; some alerts may be missing until fixed", "err", err)
	}
	cancel()

	s.sched = scheduler.New(s.log)
	spec := s.cfg.Schedules.TelemetrySample
	if err := s.sched.Register(telemetrySampleJob, spec, s.runTelemetryCycle); err != nil {
		// A malformed spec is the operator's misconfiguration; the loop simply does
		// not run rather than taking the panel down. An empty spec disables the job
		// (Register is a no-op) — in that case alerting is off by the operator's own
		// choice, which we surface so it is not a silent gap.
		s.log.Error("could not schedule telemetry/alert loop; alerts will not fire", "spec", spec, "err", err)
	}
	if spec == "" {
		s.log.Warn("telemetry sampling schedule is empty; the alert loop is disabled and no alerts will fire")
	}

	// Scheduled backups: a left-alone box must protect its own data. Full and
	// incremental run on the configured cadence (defaults: weekly full, daily
	// incremental). Their schedules never overlap, and the Manager's single-flight
	// guard turns any accidental overlap (e.g. a long full or a manual run) into a
	// benign skip rather than a false-failure alert.
	s.registerBackupJob(fullBackupJob, s.cfg.Schedules.FullBackup, backup.TypeFull)
	s.registerBackupJob(incrementalBackupJob, s.cfg.Schedules.IncrementalBackup, backup.TypeIncr)

	// Scheduled restore verification: prove the repo is still recoverable on a
	// cadence (default 05:00 Sundays, after the weekly full). A read-only
	// `pgbackrest verify` — it never touches the live cluster — so unlike a backup
	// it cannot conflict and needs no single-flight guard.
	s.registerJob(restoreTestJob, s.cfg.Schedules.RestoreTest, s.scheduledRestoreTest(),
		"restore verification is disabled (empty schedule); backups will not be automatically proven recoverable")

	// Drop-off expiry sweep on a fixed cadence: delete the full database dumps of
	// timed-out drop-off sessions from S3 and mark them expired. Always on (a bad
	// hardcoded spec only disables the loop, never takes the panel down).
	s.registerJob(dropoffSweepJob, dropoffSweepSchedule, s.scheduledDropoffSweep(),
		"drop-off expiry sweep is disabled; expired drop-off dumps may linger in S3")

	s.sched.Start(ctx)
}

// registerJob registers a named scheduled job, logging an error on a bad cron
// spec (the job simply will not run rather than taking the panel down) and a
// loud warning on an empty spec (the operator has turned this job OFF — never a
// silent gap). emptyWarn is the message logged for that opt-out case.
func (s *Server) registerJob(name, spec string, fn scheduler.JobFunc, emptyWarn string) {
	if err := s.sched.Register(name, spec, fn); err != nil {
		s.log.Error("could not schedule job; it will not run automatically", "job", name, "spec", spec, "err", err)
		return
	}
	if spec == "" {
		s.log.Warn(emptyWarn, "job", name)
	}
}

// registerBackupJob registers a scheduled backup of the given type. An empty
// spec is the operator's explicit opt-out and is surfaced loudly, since no
// automatic backups is the single biggest data-loss risk.
func (s *Server) registerBackupJob(name, spec string, t backup.Type) {
	s.registerJob(name, spec, s.scheduledBackup(t),
		"scheduled backup is disabled (empty schedule); no automatic backups of this type will run")
}

// scheduledBackup builds the JobFunc the scheduler runs for a backup of type t.
// It self-heals the pgBackRest config first (a panel that started before Postgres
// was reachable, or before a backup target was saved, may never have written it),
// then runs the backup. An overlap with another in-flight backup returns
// CodeConflict from the Manager's single-flight guard; that is a benign skip, not
// a failure, so it is logged and swallowed — returning it would make the scheduler
// log a spurious error and must never be mistaken for a backup failure.
func (s *Server) scheduledBackup(t backup.Type) scheduler.JobFunc {
	return func(ctx context.Context) error {
		cfg, err := config.Load(ctx, s.store)
		if err != nil {
			return err
		}
		if _, err := s.ensureBackupConfigured(ctx, cfg); err != nil {
			return err
		}
		res, err := s.backups.Backup(ctx, t)
		if err != nil {
			if core.CodeOf(err) == core.CodeConflict {
				s.log.Info("scheduled backup skipped; another backup is already running", "type", string(t))
				return nil
			}
			return err
		}
		s.log.Info("scheduled backup completed", "type", string(t), "message", res.Message)
		return nil
	}
}

// scheduledRestoreTest builds the JobFunc the scheduler runs to periodically
// prove the backup repository is still recoverable. It runs the read-only
// `pgbackrest verify` via the Manager, which records a pass/fail restore_tests
// row regardless of outcome, so the durability surfacing reflects it.
//
// Unlike the backup jobs, it deliberately does NOT self-heal the pgBackRest
// config: ensureBackupConfigured runs stanza-create (which takes an exclusive
// stanza lock) and may restart Postgres to enable archiving — either would
// collide with a backup that is still running (the default weekly full starts at
// 03:00 with a 6h timeout and can still be in flight when this fires at 05:00).
// Verify is a pure reader that only needs the config the backup jobs already
// wrote, so it calls RestoreTest directly. If backups were never configured,
// verify fails and is recorded as such — an honest "no proven-recoverable
// backup" signal, not a crash.
//
// A verify failure is a genuine durability problem, not a benign skip: the
// Manager has already recorded the fail row, and returning the error lets the
// scheduler log the failure too. Unlike a backup, verify never writes the repo,
// so it cannot trip the single-flight CodeConflict path.
func (s *Server) scheduledRestoreTest() scheduler.JobFunc {
	return func(ctx context.Context) error {
		res, err := s.backups.RestoreTest(ctx)
		if err != nil {
			return err
		}
		s.log.Info("scheduled restore test completed", "message", res.Message)
		return nil
	}
}

// scheduledDropoffSweep builds the JobFunc that sweeps expired drop-off sessions:
// it deletes their S3 dump+meta objects and marks them expired. A sweep error is
// returned so the scheduler logs it; the next tick retries.
func (s *Server) scheduledDropoffSweep() scheduler.JobFunc {
	return func(ctx context.Context) error {
		return s.sweepExpiredDropoffs(ctx)
	}
}

// stopBackgroundJobs halts the background loop, blocking until any in-flight cycle
// finishes. Safe to call when the scheduler was never started (idempotent).
//
// This does not need its own deadline: jobs run under the server context, and an
// in-flight dispatch's notifier requests are built with that context
// (http.NewRequestWithContext). On the shutdown path the context is already
// cancelled before this runs, so any outstanding Pushover/webhook call aborts at
// once and Stop returns promptly rather than waiting out each notifier's timeout.
func (s *Server) stopBackgroundJobs() {
	if s.sched != nil {
		s.sched.Stop()
	}
}

// seedDefaultAlertRules inserts any of the shipped default rules that are not
// already present in the store, so a fresh install boots with smart alerting
// (Postgres down, disk full, backup failed/stale, connections near max
// escalating to critically high, replication lag). It is idempotent and never
// clobbers an operator's edits: a
// rule whose id already exists is left exactly as the operator saved it
// (including a disabled or re-thresholded one), so seeding can run safely on
// every startup.
func (s *Server) seedDefaultAlertRules(ctx context.Context) error {
	existing, err := s.store.ListAlerts(ctx)
	if err != nil {
		return err
	}
	have := make(map[string]bool, len(existing))
	for _, rec := range existing {
		have[rec.ID] = true
	}

	for _, rule := range alert.DefaultRules() {
		if have[rule.ID] {
			continue // operator-owned once it exists; never overwrite.
		}
		rec, err := rule.ToRecord()
		if err != nil {
			return err
		}
		if err := s.store.UpsertAlert(ctx, rec); err != nil {
			return err
		}
		s.log.Info("seeded default alert rule", "id", rule.ID, "name", rule.Name)
	}
	return nil
}

// runTelemetryCycle is the scheduled unit of work: take one telemetry sample
// (which also folds in backup health from the store), evaluate every persisted
// rule against it, and dispatch the resulting firing/recovery events to the
// configured notification channels. A sampling error aborts this cycle and is
// returned so the scheduler logs it; the next tick retries. An evaluation error
// is a non-fatal per-rule persistence failure — the events that were computed
// are still dispatched (a real firing must never be lost to one rule's bad store
// write), and the next tick re-persists the unsaved state.
func (s *Server) runTelemetryCycle(ctx context.Context) error {
	snap, err := s.collector.SampleOnce(ctx)
	if err != nil {
		return err
	}

	events, err := s.engine.Evaluate(ctx, snap, time.Now())
	if err != nil {
		// Evaluate's error is now a non-fatal, per-rule persistence failure: the
		// events it could compute are still returned and MUST be dispatched, or a
		// real firing would be lost to an unrelated rule's store-write error. The
		// rule detail is already logged inside Evaluate; note it and press on.
		s.log.Warn("alert evaluation had a persistence error; dispatching computed events anyway", "err", err)
	}
	if len(events) == 0 {
		return nil
	}

	s.dispatchAlertEvents(ctx, events)
	return nil
}

// dispatchAlertEvents sends each event to every enabled, configured notification
// channel. A per-channel send failure is logged and does not stop the other
// channels or events — the rule's firing state is already persisted by the
// engine, so the operator can still see it in the panel even if delivery fails.
func (s *Server) dispatchAlertEvents(ctx context.Context, events []alert.Event) {
	notifiers := s.activeNotifiers(ctx)
	if len(notifiers) == 0 {
		// The rule fired and its state is persisted (visible in the UI), but there
		// is nowhere to push it. Make that loud in the log so a blank channel list
		// during an incident is not a silent black hole.
		s.log.Warn("alert events fired but no notification channels are configured", "events", len(events))
		return
	}

	for _, ev := range events {
		for _, n := range notifiers {
			if err := n.Send(ctx, ev); err != nil {
				s.log.Error("alert notification failed",
					"channel", n.Name(), "rule", ev.Rule.ID, "state", string(ev.State), "err", err)
			}
		}
	}
}

// activeNotifiers builds a Notifier for each enabled, configured channel. A nil
// HTTP client lets each notifier use its own timeout-bounded default so a hung
// endpoint cannot stall the loop. A channel-load error is logged and yields no
// notifiers (the cycle proceeds; state is still persisted).
func (s *Server) activeNotifiers(ctx context.Context) []alert.Notifier {
	channels, err := s.loadAlertChannelsCtx(ctx)
	if err != nil {
		s.log.Error("could not load alert channels for dispatch", "err", err)
		return nil
	}

	var out []alert.Notifier
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		switch ch.Kind {
		case "pushover":
			out = append(out, alert.NewPushover(nil, ch.PushoverToken, ch.PushoverUser))
		case "webhook":
			out = append(out, alert.NewWebhook(nil, ch.WebhookURL))
		default:
			// A kind that passed handleSaveAlertChannel's validation but is unknown
			// here would otherwise be silently dropped during an incident. Surface it.
			s.log.Warn("unknown alert channel kind; skipping during dispatch", "kind", ch.Kind)
		}
	}
	return out
}
