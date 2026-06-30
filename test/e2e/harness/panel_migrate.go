//go:build e2e

package harness

import (
	"regexp"
	"testing"
	"time"
)

// This file adds the typed Panel client surface the three migration scenarios
// drive. It is additive (a new file the scenario author owns) and does not touch
// the frozen harness core. Every method is a thin wrapper over Panel.Do/GET/POST.

// MigrationRecord mirrors the panel's migrationResponse wire shape (the fields the
// migration scenarios assert on). Status reaches "completed" or "failed"; the
// row-count maps are populated on success.
type MigrationRecord struct {
	ID             int64            `json:"id"`
	Mode           string           `json:"mode"`
	Role           string           `json:"role"`
	Status         string           `json:"status"`
	Phase          string           `json:"phase"`
	SourceSummary  string           `json:"source_summary"`
	TargetDatabase string           `json:"target_database"`
	Code           string           `json:"code"`
	Error          string           `json:"error"`
	RowCountsSrc   map[string]int64 `json:"row_counts_src"`
	RowCountsTgt   map[string]int64 `json:"row_counts_tgt"`
}

// migrateStarted is the 202 {id,status} acknowledgement of an async migrate job.
type migrateStarted struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// MigrateSingleDB starts a DIRECT single-database pull (POST /migrate/single-db)
// and returns the local record id to poll.
func (p *Panel) MigrateSingleDB(source SourceConn, targetDB string) (int64, error) {
	body := map[string]any{"source": source, "target_database": targetDB}
	var out migrateStarted
	err := p.POST("/api/migrate/single-db", body, &out)
	return out.ID, err
}

// MigrateCluster starts a DIRECT whole-cluster pull (POST /migrate/cluster).
func (p *Panel) MigrateCluster(source SourceConn) (int64, error) {
	body := map[string]any{"source": source}
	var out migrateStarted
	err := p.POST("/api/migrate/cluster", body, &out)
	return out.ID, err
}

// GetMigration fetches one migration record (GET /migrate/{id}).
func (p *Panel) GetMigration(id int64) (MigrationRecord, error) {
	var out MigrationRecord
	err := p.GET("/api/migrate/"+itoa(id), &out)
	return out, err
}

// AwaitMigration polls a migration record until it reaches a terminal state
// (completed/failed), then returns it. A "failed" outcome is returned (not a
// fatal) so the scenario can assert on it with the recorded error text.
func (p *Panel) AwaitMigration(t *testing.T, id int64, timeout time.Duration) MigrationRecord {
	t.Helper()
	var final MigrationRecord
	err := Poll(timeout, 2*time.Second, func() (bool, error) {
		rec, err := p.GetMigration(id)
		if err != nil {
			return false, err
		}
		switch rec.Status {
		case "completed", "failed":
			final = rec
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("await migration %d: %v", id, err)
	}
	return final
}

// ---- ssh-less (S3) sessions ----

// Session mirrors the migrate.MigrationSession document (the cross-panel channel)
// fields the session scenario reads.
type Session struct {
	Code         string `json:"code"`
	Database     string `json:"database"`
	Status       string `json:"status"`
	DumpKey      string `json:"dump_key"`
	DumpChecksum string `json:"dump_checksum"`
	Error        string `json:"error"`
}

// CreateMigrationSession creates the ssh-less TARGET session (POST /migrate/sessions)
// for a database, returning the session document (with its code).
func (p *Panel) CreateMigrationSession(database string) (Session, error) {
	var out Session
	err := p.POST("/api/migrate/sessions", map[string]string{"database": database}, &out)
	return out, err
}

// GetMigrationSession reads the session document (GET /migrate/sessions/{code}).
func (p *Panel) GetMigrationSession(code string) (Session, error) {
	var out Session
	err := p.GET("/api/migrate/sessions/"+code, &out)
	return out, err
}

// ExportMigrationSession joins the session as the SOURCE and starts the export
// (POST /migrate/sessions/{code}/export), returning the source-side record id.
func (p *Panel) ExportMigrationSession(code string, source SourceConn, database string) (int64, error) {
	body := map[string]any{"source": source, "database": database}
	var out migrateStarted
	err := p.POST("/api/migrate/sessions/"+code+"/export", body, &out)
	return out.ID, err
}

// AwaitSession polls the session document until its status is terminal
// (completed/failed/expired), then returns it.
func (p *Panel) AwaitSession(t *testing.T, code string, timeout time.Duration) Session {
	t.Helper()
	var final Session
	err := Poll(timeout, 2*time.Second, func() (bool, error) {
		sess, err := p.GetMigrationSession(code)
		if err != nil {
			return false, err
		}
		switch sess.Status {
		case "completed", "failed", "expired":
			final = sess
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		t.Fatalf("await session %s: %v", code, err)
	}
	return final
}

// ---- drop-off link ----

// Dropoff is the once-only mint response (POST /migrate/drops): the code, the
// expiry, and the two paste-able push commands carrying the presigned PUT URLs.
type Dropoff struct {
	Code           string    `json:"code"`
	TargetDatabase string    `json:"target_database"`
	Overwrite      bool      `json:"overwrite"`
	ExpiresAt      time.Time `json:"expires_at"`
	CommandDocker  string    `json:"command_docker"`
	CommandNative  string    `json:"command_native"`
}

var (
	dumpURLRe = regexp.MustCompile(`INDIEPG_DUMP_URL='([^']*)'`)
	metaURLRe = regexp.MustCompile(`INDIEPG_META_URL='([^']*)'`)
)

// DumpURL extracts the presigned dump PUT URL from the minted push command (the
// panel passes it as an INDIEPG_DUMP_URL env assignment so it stays out of argv).
func (d Dropoff) DumpURL() string { return firstSubmatch(dumpURLRe, d.CommandDocker) }

// MetaURL extracts the presigned meta.json PUT URL from the minted push command.
func (d Dropoff) MetaURL() string { return firstSubmatch(metaURLRe, d.CommandDocker) }

// DropoffStatus is the safe, re-servable status view (GET /migrate/drops/{code}).
type DropoffStatus struct {
	Code           string `json:"code"`
	Status         string `json:"status"`
	TargetDatabase string `json:"target_database"`
	ByteSize       int64  `json:"byte_size"`
	MigrationID    *int64 `json:"migration_id"`
	Error          string `json:"error"`
}

// CreateDropoff mints a drop-off session for a target database (POST /migrate/drops).
func (p *Panel) CreateDropoff(targetDB string) (Dropoff, error) {
	var out Dropoff
	err := p.POST("/api/migrate/drops", map[string]any{"target_database": targetDB}, &out)
	return out, err
}

// GetDropoff reads the drop-off status (GET /migrate/drops/{code}).
func (p *Panel) GetDropoff(code string) (DropoffStatus, error) {
	var out DropoffStatus
	err := p.GET("/api/migrate/drops/"+code, &out)
	return out, err
}

// StartDropoff begins the import once the source has pushed (POST
// /migrate/drops/{code}/start), returning the migration record id to poll.
func (p *Panel) StartDropoff(code string) (int64, error) {
	var out migrateStarted
	err := p.POST("/api/migrate/drops/"+code+"/start", map[string]string{}, &out)
	return out.ID, err
}

func firstSubmatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// itoa renders an int64 without pulling strconv into every call site.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
