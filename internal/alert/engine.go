package alert

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
	"github.com/venkatesh-sekar/pgpanel/internal/telemetry"
)

// State is the persisted evaluation state of a rule.
type State string

const (
	// StateOK means the rule is not currently breached (or not yet sustained).
	StateOK State = "ok"
	// StateFiring means the breach has been sustained past For and is active.
	StateFiring State = "firing"
	// StateResolved is a transient marker emitted on recovery; the engine writes
	// it on the eval cycle a rule recovers, then it returns to StateOK.
	StateResolved State = "resolved"
)

// Event is produced by Evaluate when a rule fires or recovers. State is either
// StateFiring (a new or re-notified breach) or StateResolved (recovery).
type Event struct {
	Rule    Rule
	State   State
	Value   float64
	FiredAt time.Time
	Message string
}

// Engine evaluates the persisted rules against telemetry snapshots, honoring
// each rule's For (sustained-breach), Cooldown (re-notification throttle), and
// emitting automatic recovery events. It persists rule state to the store so
// firing status and cooldown survive restarts.
//
// The engine keeps a small in-memory map of "breaching since" timestamps to
// implement the For window; this is rebuilt naturally as snapshots arrive and a
// cold start simply treats an ongoing breach as starting now.
type Engine struct {
	st  *store.Store
	log *core.Logger

	mu          sync.Mutex
	breachSince map[string]time.Time // ruleID -> first time the breach was observed
}

// NewEngine constructs an Engine over the store.
func NewEngine(st *store.Store, log *core.Logger) *Engine {
	if log == nil {
		log = core.Discard()
	}
	return &Engine{
		st:          st,
		log:         log,
		breachSince: make(map[string]time.Time),
	}
}

// Evaluate checks every enabled, persisted rule against the snapshot at time
// now and returns the events that should be notified: new firings, cooldown-
// elapsed re-notifications, and recoveries. Events suppressed by an active
// cooldown are omitted. Rule state is persisted as a side effect.
//
// Disabled rules and rules whose metric cannot be computed from the snapshot
// are skipped (and not transitioned), so a transient missing metric never
// produces a spurious recovery.
func (e *Engine) Evaluate(ctx context.Context, snap telemetry.Snapshot, now time.Time) ([]Event, error) {
	now = now.UTC()

	records, err := e.st.ListAlerts(ctx)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	var events []Event
	for _, rec := range records {
		if !rec.Enabled {
			// Clear any lingering breach timer for a disabled rule.
			delete(e.breachSince, rec.ID)
			continue
		}
		rule, err := RuleFromRecord(rec)
		if err != nil {
			// A malformed rule should not abort the whole cycle; log and skip.
			e.log.Warn("skipping malformed alert rule", "id", rec.ID, "err", err.Error())
			continue
		}

		value, ok := metricValue(snap, rule.Metric)
		if !ok {
			// Metric not present in this snapshot: leave state untouched.
			continue
		}

		ev, changed := e.evaluateRule(rec, rule, value, now)
		if changed != nil {
			if err := e.persist(ctx, rec, changed); err != nil {
				return nil, err
			}
		}
		if ev != nil {
			events = append(events, *ev)
		}
	}
	return events, nil
}

// stateUpdate captures the columns to write back for a rule after an eval cycle.
type stateUpdate struct {
	state       State
	lastFiredAt *time.Time
	lastEvalAt  time.Time
}

// evaluateRule applies the state machine for one rule. It returns the Event to
// notify (or nil) and the stateUpdate to persist (or nil when nothing changed
// other than the always-updated last-eval timestamp, which is folded in).
func (e *Engine) evaluateRule(rec store.AlertRecord, rule Rule, value float64, now time.Time) (*Event, *stateUpdate) {
	breaching := rule.Op.compare(value, rule.Threshold)
	current := State(rec.State)
	if current != StateFiring {
		// Treat anything that is not actively firing (ok/resolved/empty) as ok
		// for transition purposes.
		current = StateOK
	}

	upd := &stateUpdate{state: current, lastFiredAt: rec.LastFiredAt, lastEvalAt: now}

	if !breaching {
		// No breach. Clear the sustained-breach timer.
		delete(e.breachSince, rule.ID)
		if current == StateFiring {
			// Recovery: emit a resolved event and drop back to ok.
			upd.state = StateOK
			ev := &Event{
				Rule:    rule,
				State:   StateResolved,
				Value:   value,
				FiredAt: now,
				Message: recoveryMessage(rule, value),
			}
			return ev, upd
		}
		// Stayed ok. Only the eval timestamp changed.
		return nil, upd
	}

	// Breaching. Record when the breach first began.
	since, seen := e.breachSince[rule.ID]
	if !seen {
		since = now
		e.breachSince[rule.ID] = since
	}

	// Has the breach been sustained long enough to fire?
	if now.Sub(since) < rule.For {
		// Still within the For window: not firing yet.
		return nil, upd
	}

	if current != StateFiring {
		// New firing.
		fired := now
		upd.state = StateFiring
		upd.lastFiredAt = &fired
		ev := &Event{
			Rule:    rule,
			State:   StateFiring,
			Value:   value,
			FiredAt: now,
			Message: firingMessage(rule, value),
		}
		return ev, upd
	}

	// Already firing: re-notify only if the cooldown has elapsed.
	if rec.LastFiredAt != nil && rule.Cooldown > 0 && now.Sub(*rec.LastFiredAt) < rule.Cooldown {
		// Cooldown still active: suppress the re-notification.
		return nil, upd
	}

	// Cooldown elapsed (or none configured): re-notify.
	fired := now
	upd.state = StateFiring
	upd.lastFiredAt = &fired
	ev := &Event{
		Rule:    rule,
		State:   StateFiring,
		Value:   value,
		FiredAt: now,
		Message: firingMessage(rule, value),
	}
	return ev, upd
}

// persist writes the updated state back to the store, preserving the rule's
// definition and identity columns.
func (e *Engine) persist(ctx context.Context, rec store.AlertRecord, upd *stateUpdate) error {
	rec.State = string(upd.state)
	rec.LastFiredAt = upd.lastFiredAt
	lastEval := upd.lastEvalAt
	rec.LastEvalAt = &lastEval
	if err := e.st.UpsertAlert(ctx, rec); err != nil {
		return err
	}
	return nil
}

// firingMessage builds a human-readable firing line for a rule breach.
func firingMessage(rule Rule, value float64) string {
	return fmt.Sprintf("%s: %s %s %s (threshold %s)",
		rule.Name, rule.Metric, string(rule.Op), formatValue(value), formatValue(rule.Threshold))
}

// recoveryMessage builds a human-readable recovery line.
func recoveryMessage(rule Rule, value float64) string {
	return fmt.Sprintf("RESOLVED %s: %s is now %s (threshold %s)",
		rule.Name, rule.Metric, formatValue(value), formatValue(rule.Threshold))
}

// formatValue renders a float compactly: integers without a fractional part,
// otherwise two decimals.
func formatValue(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}
