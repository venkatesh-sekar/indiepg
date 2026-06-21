package backup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// sampleInfoJSON is a representative `pgbackrest info --output=json` document
// with two backups (a full then an incr) in pgBackRest's natural oldest-first
// order.
const sampleInfoJSON = `[
  {
    "name": "main",
    "status": {"code": 0, "message": "ok"},
    "backup": [
      {
        "label": "20260101-030000F",
        "type": "full",
        "timestamp": {"start": 1767236400, "stop": 1767236460},
        "info": {
          "size": 1048576,
          "delta": 1048576,
          "repository": {"size": 524288, "delta": 524288}
        },
        "archive": {"start": "000000010000000000000002", "stop": "000000010000000000000003"}
      },
      {
        "label": "20260102-030000F_20260102-040000I",
        "type": "incr",
        "timestamp": {"start": 1767322800, "stop": 1767322830},
        "info": {
          "size": 2097152,
          "delta": 131072,
          "repository": {"size": 600000, "delta": 65536}
        },
        "archive": {"start": "000000010000000000000005", "stop": "000000010000000000000006"}
      }
    ]
  }
]`

func TestParseInfoJSON(t *testing.T) {
	got, err := ParseInfoJSON([]byte(sampleInfoJSON), "main")
	require.NoError(t, err)
	require.Len(t, got, 2)

	// Newest first: the incr backup (later start) leads.
	require.Equal(t, "20260102-030000F_20260102-040000I", got[0].Label)
	require.Equal(t, TypeIncr, got[0].Type)
	require.Equal(t, int64(131072), got[0].Size, "Size is the per-backup delta")
	require.Equal(t, int64(2097152), got[0].DatabaseSize, "DatabaseSize is info.size")
	require.Equal(t, int64(65536), got[0].RepoSize, "RepoSize is repository.delta")
	require.Equal(t, "000000010000000000000005", got[0].WALStart)
	require.Equal(t, "000000010000000000000006", got[0].WALStop)
	require.Equal(t, 30*time.Second, got[0].Duration())

	require.Equal(t, "20260101-030000F", got[1].Label)
	require.Equal(t, TypeFull, got[1].Type)
	require.Equal(t, int64(1048576), got[1].Size)
	require.Equal(t, int64(524288), got[1].RepoSize)
	require.Equal(t, time.Unix(1767236400, 0).UTC(), got[1].StartTime)
	require.Equal(t, time.Minute, got[1].Duration())
}

func TestParseInfoJSON_EmptyStanzaMatchesFirst(t *testing.T) {
	got, err := ParseInfoJSON([]byte(sampleInfoJSON), "")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestParseInfoJSON_UnknownStanza(t *testing.T) {
	_, err := ParseInfoJSON([]byte(sampleInfoJSON), "nope")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestParseInfoJSON_NoBackups(t *testing.T) {
	doc := `[{"name":"main","backup":[]}]`
	got, err := ParseInfoJSON([]byte(doc), "main")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestParseInfoJSON_Malformed(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"not json", "this is not json"},
		{"truncated", `[{"name":"main","backup":`},
		{"empty", ""},
		{"object not array", `{"name":"main"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseInfoJSON([]byte(tc.data), "main")
			require.Error(t, err)
			require.Equal(t, core.CodeValidation, core.CodeOf(err))
		})
	}
}

func TestParseInfoJSON_EmptyArray(t *testing.T) {
	// A valid but empty document (no stanzas) -> stanza not found.
	_, err := ParseInfoJSON([]byte(`[]`), "main")
	require.Error(t, err)
	require.Equal(t, core.CodeNotFound, core.CodeOf(err))
}

func TestParseInfoJSON_SortsByStartNewestFirst(t *testing.T) {
	// Emit in scrambled order; parser must sort newest-first deterministically.
	doc := `[{"name":"main","backup":[
		{"label":"b","type":"diff","timestamp":{"start":200,"stop":210},"info":{"size":1,"delta":1,"repository":{"size":1,"delta":1}},"archive":{"start":"","stop":""}},
		{"label":"a","type":"full","timestamp":{"start":100,"stop":110},"info":{"size":1,"delta":1,"repository":{"size":1,"delta":1}},"archive":{"start":"","stop":""}},
		{"label":"c","type":"incr","timestamp":{"start":300,"stop":305},"info":{"size":1,"delta":1,"repository":{"size":1,"delta":1}},"archive":{"start":"","stop":""}}
	]}]`
	got, err := ParseInfoJSON([]byte(doc), "main")
	require.NoError(t, err)
	require.Equal(t, []string{"c", "b", "a"}, []string{got[0].Label, got[1].Label, got[2].Label})
}

func TestParseInfoJSON_UnknownTypePreserved(t *testing.T) {
	doc := `[{"name":"main","backup":[
		{"label":"x","type":"weird","timestamp":{"start":100,"stop":110},"info":{"size":1,"delta":1,"repository":{"size":1,"delta":1}},"archive":{"start":"","stop":""}}
	]}]`
	got, err := ParseInfoJSON([]byte(doc), "main")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, Type("weird"), got[0].Type)
}

func TestBackupInfoDuration(t *testing.T) {
	cases := []struct {
		name  string
		start time.Time
		stop  time.Time
		want  time.Duration
	}{
		{"normal", time.Unix(100, 0), time.Unix(160, 0), time.Minute},
		{"zero start", time.Time{}, time.Unix(160, 0), 0},
		{"zero stop", time.Unix(100, 0), time.Time{}, 0},
		{"negative clamped", time.Unix(200, 0), time.Unix(100, 0), 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := BackupInfo{StartTime: tc.start, StopTime: tc.stop}
			require.Equal(t, tc.want, b.Duration())
		})
	}
}

func TestUnixOrZero(t *testing.T) {
	require.True(t, unixOrZero(0).IsZero())
	require.True(t, unixOrZero(-5).IsZero())
	require.Equal(t, time.Unix(1767236400, 0).UTC(), unixOrZero(1767236400))
}
