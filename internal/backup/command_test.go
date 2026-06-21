package backup

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/indiepg/internal/core"
)

func TestParseType(t *testing.T) {
	cases := []struct {
		in      string
		want    Type
		wantErr bool
	}{
		{"full", TypeFull, false},
		{"diff", TypeDiff, false},
		{"differential", TypeDiff, false},
		{"incr", TypeIncr, false},
		{"incremental", TypeIncr, false},
		{"", "", true},
		{"FULL", "", true},
		{"bogus", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseType(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, core.CodeValidation, core.CodeOf(err))
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTypeString(t *testing.T) {
	require.Equal(t, "full", TypeFull.String())
	require.Equal(t, "incr", TypeIncr.String())
}

func TestInfoCmd(t *testing.T) {
	spec := InfoCmd("main")
	require.Equal(t, "pgbackrest", spec.Name)
	require.Equal(t, "postgres", spec.AsUser)
	require.Contains(t, spec.Args, "--stanza=main")
	require.Contains(t, spec.Args, "--output=json")
	require.Contains(t, spec.Args, "info")
	require.Greater(t, spec.Timeout, time.Duration(0))
}

func TestBackupCmd(t *testing.T) {
	cases := []Type{TypeFull, TypeDiff, TypeIncr}
	for _, tt := range cases {
		t.Run(string(tt), func(t *testing.T) {
			spec := BackupCmd("main", tt)
			require.Equal(t, "pgbackrest", spec.Name)
			require.Equal(t, "postgres", spec.AsUser)
			require.Contains(t, spec.Args, "--stanza=main")
			require.Contains(t, spec.Args, "--type="+string(tt))
			require.Equal(t, "backup", spec.Args[len(spec.Args)-1])
		})
	}
}

func TestRestoreCmd_Plain(t *testing.T) {
	spec, err := RestoreCmd("main", nil, false)
	require.NoError(t, err)
	require.Equal(t, "pgbackrest", spec.Name)
	require.Equal(t, "postgres", spec.AsUser)
	require.Contains(t, spec.Args, "--stanza=main")
	require.NotContains(t, spec.Args, "--delta")
	require.Equal(t, "restore", spec.Args[len(spec.Args)-1])
}

func TestRestoreCmd_Delta(t *testing.T) {
	spec, err := RestoreCmd("main", nil, true)
	require.NoError(t, err)
	require.Contains(t, spec.Args, "--delta")
}

func TestRestoreCmd_PITRTime(t *testing.T) {
	tm := time.Date(2026, 6, 21, 12, 30, 0, 0, time.UTC)
	spec, err := RestoreCmd("main", &RecoveryTarget{Time: &tm, Action: "promote"}, false)
	require.NoError(t, err)
	joined := strings.Join(spec.Args, " ")
	require.Contains(t, joined, "--type=time")
	require.Contains(t, joined, "--target=2026-06-21 12:30:00+00")
	require.Contains(t, joined, "--target-action=promote")
}

func TestRestoreCmd_PITRVariants(t *testing.T) {
	cases := []struct {
		name      string
		target    RecoveryTarget
		wantType  string
		wantValue string
	}{
		{"xid", RecoveryTarget{XID: "12345"}, "--type=xid", "--target=12345"},
		{"lsn", RecoveryTarget{LSN: "0/16B6A50"}, "--type=lsn", "--target=0/16B6A50"},
		{"name", RecoveryTarget{Name: "before_migration"}, "--type=name", "--target=before_migration"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := RestoreCmd("main", &tc.target, false)
			require.NoError(t, err)
			joined := strings.Join(spec.Args, " ")
			require.Contains(t, joined, tc.wantType)
			require.Contains(t, joined, tc.wantValue)
		})
	}
}

func TestRestoreCmd_InvalidStanza(t *testing.T) {
	_, err := RestoreCmd("Bad Stanza!", nil, false)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestRestoreCmd_ConflictingTarget(t *testing.T) {
	tm := time.Now()
	_, err := RestoreCmd("main", &RecoveryTarget{Time: &tm, XID: "1"}, false)
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
}

func TestRecoveryTargetValidate(t *testing.T) {
	tm := time.Now()
	cases := []struct {
		name    string
		target  RecoveryTarget
		wantErr bool
	}{
		{"empty ok", RecoveryTarget{}, false},
		{"only time", RecoveryTarget{Time: &tm}, false},
		{"only xid", RecoveryTarget{XID: "7"}, false},
		{"only name", RecoveryTarget{Name: "rp"}, false},
		{"action pause", RecoveryTarget{Action: "pause"}, false},
		{"action shutdown", RecoveryTarget{Action: "shutdown"}, false},
		{"two targets", RecoveryTarget{Time: &tm, LSN: "0/1"}, true},
		{"three targets", RecoveryTarget{XID: "1", LSN: "0/1", Name: "n"}, true},
		{"bad action", RecoveryTarget{Action: "explode"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.target.Validate()
			if tc.wantErr {
				require.Error(t, err)
				require.Equal(t, core.CodeValidation, core.CodeOf(err))
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestRecoveryTargetIsZero(t *testing.T) {
	require.True(t, RecoveryTarget{}.IsZero())
	require.True(t, RecoveryTarget{Action: "promote"}.IsZero(), "action alone is not a target selector")
	tm := time.Now()
	require.False(t, RecoveryTarget{Time: &tm}.IsZero())
	require.False(t, RecoveryTarget{XID: "1"}.IsZero())
}

func TestValidateStanza(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"main", false},
		{"prod-db-01", false},
		{"a1", false},
		{"", true},
		{"Main", true},
		{"has space", true},
		{"semi;colon", true},
		{"under_score", true},
		{strings.Repeat("a", 129), true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			err := validateStanza(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}
