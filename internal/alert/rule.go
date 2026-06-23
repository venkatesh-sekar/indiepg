// Package alert holds the panel's alerting subsystem: a rule model with smart
// out-of-the-box defaults, a stateful evaluation Engine that honors sustained
// duration (For), re-notification Cooldown, and automatic recovery notices, and
// Notifier implementations for Pushover and a generic webhook.
//
// Rules evaluate scalar metrics derived from a telemetry.Snapshot (see
// metrics.go). The engine persists each rule's evaluation state in the store so
// it survives restarts. Nothing here panics: every failure path returns a typed
// *core.Error.
package alert

import (
	"encoding/json"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// Severity ranks an alert from informational to critical.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Valid reports whether s is a known severity.
func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	default:
		return false
	}
}

// Op is the comparison operator a threshold rule applies to a metric value.
type Op string

const (
	OpGT  Op = ">"
	OpLT  Op = "<"
	OpGTE Op = ">="
	OpLTE Op = "<="
)

// Valid reports whether o is a known operator.
func (o Op) Valid() bool {
	switch o {
	case OpGT, OpLT, OpGTE, OpLTE:
		return true
	default:
		return false
	}
}

// compare reports whether value satisfies the operator against threshold, i.e.
// whether the rule's breach condition is met.
func (o Op) compare(value, threshold float64) bool {
	switch o {
	case OpGT:
		return value > threshold
	case OpLT:
		return value < threshold
	case OpGTE:
		return value >= threshold
	case OpLTE:
		return value <= threshold
	default:
		return false
	}
}

// Rule defines a threshold condition over a single metric, with anti-spam
// controls: For requires the breach to be sustained before firing, and Cooldown
// throttles re-notifications while a rule stays firing.
type Rule struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Metric    string        `json:"metric"`    // a metric key from metrics.go
	Op        Op            `json:"op"`        // comparison operator
	Threshold float64       `json:"threshold"` // value to compare against
	Severity  Severity      `json:"severity"`
	For       time.Duration `json:"for"`      // sustained breach before firing
	Cooldown  time.Duration `json:"cooldown"` // min interval between re-notifications
	Enabled   bool          `json:"enabled"`
}

// Validate checks a rule is well formed before it is persisted or evaluated.
func (r Rule) Validate() error {
	if r.ID == "" {
		return core.ValidationError("alert rule id is required")
	}
	if r.Name == "" {
		return core.ValidationError("alert rule %q: name is required", r.ID)
	}
	if r.Metric == "" {
		return core.ValidationError("alert rule %q: metric is required", r.ID)
	}
	if !r.Op.Valid() {
		return core.ValidationError("alert rule %q: invalid operator %q", r.ID, r.Op)
	}
	if !r.Severity.Valid() {
		return core.ValidationError("alert rule %q: invalid severity %q", r.ID, r.Severity)
	}
	if r.For < 0 {
		return core.ValidationError("alert rule %q: for must not be negative", r.ID)
	}
	if r.Cooldown < 0 {
		return core.ValidationError("alert rule %q: cooldown must not be negative", r.ID)
	}
	return nil
}

// ruleDefinition is the JSON body stored in AlertRecord.Definition. The id,
// name, enabled and severity live in dedicated AlertRecord columns, so the
// definition carries only the threshold logic and anti-spam timings. Durations
// are encoded as seconds for human-readable, language-neutral storage.
type ruleDefinition struct {
	Metric       string  `json:"metric"`
	Op           Op      `json:"op"`
	Threshold    float64 `json:"threshold"`
	ForSecs      float64 `json:"for_seconds"`
	CooldownSecs float64 `json:"cooldown_seconds"`
}

// ToRecord serializes the rule into a store.AlertRecord. The returned record
// carries no evaluation state (State/LastFiredAt/LastEvalAt are left zero); the
// Engine merges fresh definitions with persisted state on save.
func (r Rule) ToRecord() (store.AlertRecord, error) {
	if err := r.Validate(); err != nil {
		return store.AlertRecord{}, err
	}
	def := ruleDefinition{
		Metric:       r.Metric,
		Op:           r.Op,
		Threshold:    r.Threshold,
		ForSecs:      r.For.Seconds(),
		CooldownSecs: r.Cooldown.Seconds(),
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return store.AlertRecord{}, core.InternalError("marshal alert rule %q", r.ID).Wrap(err)
	}
	return store.AlertRecord{
		ID:         r.ID,
		Name:       r.Name,
		Enabled:    r.Enabled,
		Definition: string(raw),
		Severity:   string(r.Severity),
		State:      string(StateOK),
	}, nil
}

// RuleFromRecord deserializes a stored record back into a Rule.
func RuleFromRecord(rec store.AlertRecord) (Rule, error) {
	if rec.ID == "" {
		return Rule{}, core.ValidationError("alert record has empty id")
	}
	var def ruleDefinition
	if err := json.Unmarshal([]byte(rec.Definition), &def); err != nil {
		return Rule{}, core.ValidationError("alert rule %q: malformed definition", rec.ID).Wrap(err)
	}
	r := Rule{
		ID:        rec.ID,
		Name:      rec.Name,
		Metric:    def.Metric,
		Op:        def.Op,
		Threshold: def.Threshold,
		Severity:  Severity(rec.Severity),
		For:       time.Duration(def.ForSecs * float64(time.Second)),
		Cooldown:  time.Duration(def.CooldownSecs * float64(time.Second)),
		Enabled:   rec.Enabled,
	}
	if err := r.Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// DefaultRules returns the smart out-of-the-box rules every panel ships with:
// Postgres down, disk headroom low (early warning) escalating to disk almost
// full, no recent backup, connections near max (warning) escalating to
// connections critically high, and replication lag high. These match the
// design's "smart defaults" (§5.8).
func DefaultRules() []Rule {
	return []Rule{
		{
			ID:        "pg-down",
			Name:      "Postgres is down",
			Metric:    MetricPGUp,
			Op:        OpLT,
			Threshold: 1,
			Severity:  SeverityCritical,
			For:       1 * time.Minute,
			Cooldown:  15 * time.Minute,
			Enabled:   true,
		},
		{
			// Early warning: give the operator runway to act (prune WAL, grow the
			// volume, archive data) WELL BEFORE the disk-almost-full critical below
			// turns a slow fill into an emergency that can stop Postgres. Lower
			// threshold + calmer cadence than the critical tier: a 5-minute For
			// window ignores a transient bump (e.g. a deep restore-test's scratch
			// copy on the same volume), and the 1h cooldown re-reminds without a
			// firehose. The warning fires at 80%; above 90% both tiers are
			// independently active — intended escalation, mirroring the
			// backup-stale + backup-failed pair.
			ID:        "disk-headroom-low",
			Name:      "Disk headroom low",
			Metric:    MetricDiskPercent,
			Op:        OpGTE,
			Threshold: 80,
			Severity:  SeverityWarning,
			For:       5 * time.Minute,
			Cooldown:  1 * time.Hour,
			Enabled:   true,
		},
		{
			ID:        "disk-almost-full",
			Name:      "Disk almost full",
			Metric:    MetricDiskPercent,
			Op:        OpGTE,
			Threshold: 90,
			Severity:  SeverityCritical,
			For:       2 * time.Minute,
			Cooldown:  30 * time.Minute,
			Enabled:   true,
		},
		{
			ID:        "backup-stale",
			Name:      "No successful backup in 26h",
			Metric:    MetricLastBackupAgeSecs,
			Op:        OpGT,
			Threshold: (26 * time.Hour).Seconds(),
			Severity:  SeverityWarning,
			For:       0,
			Cooldown:  6 * time.Hour,
			Enabled:   true,
		},
		{
			// A failed backup is a durability emergency: fire immediately (no For
			// window) and louder than mere staleness — you can lose data before the
			// 26h "stale" window above ever trips. Cooldown matches backup-stale so a
			// box that keeps failing every cycle is not a notification firehose.
			ID:        "backup-failed",
			Name:      "Most recent backup failed",
			Metric:    MetricLastBackupFailed,
			Op:        OpGTE,
			Threshold: 1,
			Severity:  SeverityCritical,
			For:       0,
			Cooldown:  6 * time.Hour,
			Enabled:   true,
		},
		{
			ID:        "connections-near-max",
			Name:      "Connections near max",
			Metric:    MetricConnectionsPercent,
			Op:        OpGTE,
			Threshold: 85,
			Severity:  SeverityWarning,
			For:       2 * time.Minute,
			Cooldown:  30 * time.Minute,
			Enabled:   true,
		},
		{
			// Critical escalation of connections-near-max. At ~max_connections
			// Postgres REFUSES new clients ("too many clients already") — an
			// outage: apps can no longer connect, and the panel itself can be
			// locked out once only superuser_reserved_connections remain. So once
			// saturation is near-total, page LOUDER and SOONER than the 85% warning:
			// a higher threshold but a shorter 1m For and a 15m cooldown matching the
			// other outage-class rules (pg-down). 95% < 100% leaves a sliver of
			// runway to kill runaway sessions before exhaustion. Mirrors the
			// disk-headroom-low → disk-almost-full two-tier escalation.
			ID:        "connections-critical",
			Name:      "Connections critically high",
			Metric:    MetricConnectionsPercent,
			Op:        OpGTE,
			Threshold: 95,
			Severity:  SeverityCritical,
			For:       1 * time.Minute,
			Cooldown:  15 * time.Minute,
			Enabled:   true,
		},
		{
			ID:        "replication-lag-high",
			Name:      "Replication lag high",
			Metric:    MetricReplicationLagSecs,
			Op:        OpGT,
			Threshold: 300,
			Severity:  SeverityWarning,
			For:       5 * time.Minute,
			Cooldown:  30 * time.Minute,
			Enabled:   true,
		},
	}
}
