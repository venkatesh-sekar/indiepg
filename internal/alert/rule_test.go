package alert

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpCompare(t *testing.T) {
	tests := []struct {
		name      string
		op        Op
		value     float64
		threshold float64
		want      bool
	}{
		{"gt true", OpGT, 10, 5, true},
		{"gt false equal", OpGT, 5, 5, false},
		{"gt false less", OpGT, 4, 5, false},
		{"lt true", OpLT, 4, 5, true},
		{"lt false equal", OpLT, 5, 5, false},
		{"gte true equal", OpGTE, 5, 5, true},
		{"gte true greater", OpGTE, 6, 5, true},
		{"gte false", OpGTE, 4, 5, false},
		{"lte true equal", OpLTE, 5, 5, true},
		{"lte true less", OpLTE, 4, 5, true},
		{"lte false", OpLTE, 6, 5, false},
		{"unknown op never breaches", Op("=="), 5, 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.op.compare(tt.value, tt.threshold))
		})
	}
}

func TestOpAndSeverityValid(t *testing.T) {
	for _, o := range []Op{OpGT, OpLT, OpGTE, OpLTE} {
		require.True(t, o.Valid(), "op %q should be valid", o)
	}
	require.False(t, Op("").Valid())
	require.False(t, Op("=").Valid())

	for _, s := range []Severity{SeverityInfo, SeverityWarning, SeverityCritical} {
		require.True(t, s.Valid(), "severity %q should be valid", s)
	}
	require.False(t, Severity("").Valid())
	require.False(t, Severity("fatal").Valid())
}

func TestRuleValidate(t *testing.T) {
	base := Rule{
		ID:        "r1",
		Name:      "rule one",
		Metric:    MetricCPUPercent,
		Op:        OpGT,
		Threshold: 80,
		Severity:  SeverityWarning,
		Enabled:   true,
	}
	require.NoError(t, base.Validate())

	tests := []struct {
		name   string
		mutate func(*Rule)
	}{
		{"missing id", func(r *Rule) { r.ID = "" }},
		{"missing name", func(r *Rule) { r.Name = "" }},
		{"missing metric", func(r *Rule) { r.Metric = "" }},
		{"bad op", func(r *Rule) { r.Op = "!=" }},
		{"bad severity", func(r *Rule) { r.Severity = "nope" }},
		{"negative for", func(r *Rule) { r.For = -time.Second }},
		{"negative cooldown", func(r *Rule) { r.Cooldown = -time.Second }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.mutate(&r)
			err := r.Validate()
			require.Error(t, err)
		})
	}
}

func TestRuleRecordRoundTrip(t *testing.T) {
	orig := Rule{
		ID:        "disk-almost-full",
		Name:      "Disk almost full",
		Metric:    MetricDiskPercent,
		Op:        OpGTE,
		Threshold: 90.5,
		Severity:  SeverityCritical,
		For:       2 * time.Minute,
		Cooldown:  30 * time.Minute,
		Enabled:   true,
	}

	rec, err := orig.ToRecord()
	require.NoError(t, err)
	require.Equal(t, orig.ID, rec.ID)
	require.Equal(t, orig.Name, rec.Name)
	require.Equal(t, string(orig.Severity), rec.Severity)
	require.True(t, rec.Enabled)
	require.Equal(t, string(StateOK), rec.State)
	require.NotEmpty(t, rec.Definition)

	back, err := RuleFromRecord(rec)
	require.NoError(t, err)
	require.Equal(t, orig, back)
}

func TestToRecordRejectsInvalidRule(t *testing.T) {
	_, err := Rule{ID: "", Name: "x"}.ToRecord()
	require.Error(t, err)
}

func TestRuleFromRecordErrors(t *testing.T) {
	t.Run("empty id", func(t *testing.T) {
		_, err := RuleFromRecord(store_AlertRecord("", `{}`))
		require.Error(t, err)
	})
	t.Run("malformed json", func(t *testing.T) {
		_, err := RuleFromRecord(store_AlertRecord("r1", `{not json`))
		require.Error(t, err)
	})
	t.Run("invalid op after decode", func(t *testing.T) {
		_, err := RuleFromRecord(store_AlertRecord("r1", `{"metric":"x","op":"!!","threshold":1}`))
		require.Error(t, err)
	})
}

func TestDefaultRules(t *testing.T) {
	rules := DefaultRules()
	require.NotEmpty(t, rules)

	ids := map[string]bool{}
	for _, r := range rules {
		require.NoError(t, r.Validate(), "default rule %q must be valid", r.ID)
		require.True(t, r.Enabled, "default rule %q should be enabled", r.ID)
		require.False(t, ids[r.ID], "duplicate default rule id %q", r.ID)
		ids[r.ID] = true

		// Every default rule must reference a known, extractable metric.
		_, ok := metricValue(fullSnapshot(), r.Metric)
		require.True(t, ok, "default rule %q references unknown metric %q", r.ID, r.Metric)
	}

	// The smart defaults the design calls out must be present.
	for _, want := range []string{"pg-down", "disk-headroom-low", "disk-almost-full", "backup-stale", "backup-failed", "connections-near-max", "replication-lag-high"} {
		require.True(t, ids[want], "missing smart default rule %q", want)
	}

	// Default rules must survive a record round-trip.
	for _, r := range rules {
		rec, err := r.ToRecord()
		require.NoError(t, err)
		back, err := RuleFromRecord(rec)
		require.NoError(t, err)
		require.Equal(t, r, back)
	}
}
