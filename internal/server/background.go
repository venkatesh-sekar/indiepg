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
)

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

	s.sched.Start(ctx)
}

// registerBackupJob registers a scheduled backup of the given type, logging an
// error on a bad spec (the job simply will not run) and a loud warning on an
// empty spec (the operator has turned automatic backups of this type OFF — never
// a silent gap, since it is a data-loss risk).
func (s *Server) registerBackupJob(name, spec string, t backup.Type) {
	if err := s.sched.Register(name, spec, s.scheduledBackup(t)); err != nil {
		s.log.Error("could not schedule backup; it will not run automatically", "job", name, "spec", spec, "err", err)
		return
	}
	if spec == "" {
		s.log.Warn("scheduled backup is disabled (empty schedule); no automatic backups of this type will run", "job", name)
	}
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
// (Postgres down, disk full, backup failed/stale, connections near max,
// replication lag). It is idempotent and never clobbers an operator's edits: a
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
// configured notification channels. A sampling or evaluation error aborts this
// cycle and is returned so the scheduler logs it; the next tick retries.
func (s *Server) runTelemetryCycle(ctx context.Context) error {
	snap, err := s.collector.SampleOnce(ctx)
	if err != nil {
		return err
	}

	events, err := s.engine.Evaluate(ctx, snap, time.Now())
	if err != nil {
		return err
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
