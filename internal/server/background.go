package server

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/alert"
	"github.com/venkatesh-sekar/indiepg/internal/scheduler"
)

// telemetrySampleJob is the scheduler job name for the combined telemetry
// sampling + alert evaluation loop.
const telemetrySampleJob = "telemetry-sample"

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

	s.sched.Start(ctx)
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
