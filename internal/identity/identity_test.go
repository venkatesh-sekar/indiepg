package identity

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

func TestMarkerIsStale(t *testing.T) {
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		lastSeen time.Time
		want     bool
	}{
		{"fresh just now", now, false},
		{"within window", now.Add(-StaleAfter + time.Second), false},
		{"exactly at window", now.Add(-StaleAfter), false},
		{"just past window", now.Add(-StaleAfter - time.Second), true},
		{"long abandoned", now.Add(-24 * time.Hour), true},
		{"future heartbeat (clock skew)", now.Add(time.Hour), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := OwnershipMarker{LastSeen: tt.lastSeen}
			require.Equal(t, tt.want, m.IsStale(now))
		})
	}
}

func TestDefaultPrefix(t *testing.T) {
	tests := []struct {
		name string
		base string
		id   string
		want string
	}{
		{"empty base", "", "abc-123", "panel/abc-123"},
		{"plain base", "backups", "abc-123", "backups/panel/abc-123"},
		{"base with leading slash", "/backups", "abc-123", "backups/panel/abc-123"},
		{"base with trailing slash", "backups/", "abc-123", "backups/panel/abc-123"},
		{"base with both slashes", "/backups/", "abc-123", "backups/panel/abc-123"},
		{"nested base", "team/prod", "id9", "team/prod/panel/id9"},
		{"whitespace base", "   ", "id9", "panel/id9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, DefaultPrefix(tt.base, tt.id))
			id := &Identity{InstanceID: tt.id}
			require.Equal(t, tt.want, id.DefaultPrefix(tt.base))
		})
	}
}

func TestMarkerKey(t *testing.T) {
	tests := []struct {
		prefix string
		want   string
	}{
		{"", MarkerObjectName},
		{"   ", MarkerObjectName},
		{"panel/abc", "panel/abc/" + MarkerObjectName},
		{"panel/abc/", "panel/abc/" + MarkerObjectName},
		{"/panel/abc/", "panel/abc/" + MarkerObjectName},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, markerKey(tt.prefix))
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	m := OwnershipMarker{
		InstanceID: "id-1",
		Hostname:   "host-a",
		PGSystemID: "7300000000000000000",
		ClaimedAt:  time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC),
		LastSeen:   time.Date(2026, 6, 21, 11, 0, 0, 0, time.UTC),
	}
	data, err := marshalMarker(m)
	require.NoError(t, err)
	require.Equal(t, byte('\n'), data[len(data)-1], "marker should end with a newline")

	got, err := unmarshalMarker(data)
	require.NoError(t, err)
	require.Equal(t, m, got)

	// JSON keys must match the contract field names.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))
	for _, k := range []string{"instance_id", "hostname", "pg_system_id", "claimed_at", "last_seen"} {
		require.Contains(t, raw, k)
	}
}

func TestUnmarshalMarkerMalformed(t *testing.T) {
	_, err := unmarshalMarker([]byte("{not json"))
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestGenerateAndLoad(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	// Not installed yet → Load surfaces the store's NotFound.
	_, err = Load(ctx, st)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))

	id, err := Generate(ctx, st, "web-db-01", "1.2.3")
	require.NoError(t, err)
	require.NotEmpty(t, id.InstanceID)
	require.Equal(t, "web-db-01", id.Label)
	require.NotEmpty(t, id.Hostname)

	// Two generations produce distinct instance ids.
	id2, err := Generate(ctx, st, "web-db-01", "1.2.3")
	require.NoError(t, err)
	require.NotEqual(t, id.InstanceID, id2.InstanceID)

	loaded, err := Load(ctx, st)
	require.NoError(t, err)
	require.Equal(t, id2.InstanceID, loaded.InstanceID)
	require.Equal(t, "web-db-01", loaded.Label)
}

func TestGenerateEmptyLabelUsesHostname(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := Generate(ctx, st, "  ", "1.0.0")
	require.NoError(t, err)
	require.NotEmpty(t, id.Label)
	require.Equal(t, id.Hostname, id.Label)
}

func TestGeneratePersistsPanelVersion(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	id, err := Generate(ctx, st, "lbl", "9.9.9")
	require.NoError(t, err)

	inst, err := st.GetInstance(ctx)
	require.NoError(t, err)
	require.Equal(t, "9.9.9", inst.PanelVersion)
	require.Equal(t, id.InstanceID, inst.InstanceID)
	require.False(t, inst.CreatedAt.IsZero())
}
