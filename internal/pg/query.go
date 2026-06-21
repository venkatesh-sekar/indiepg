package pg

import (
	"context"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// QueryColumn describes one result column: its name and Postgres type name.
type QueryColumn struct {
	Name     string
	DataType string
}

// QueryRows is the generic result of a read query: column metadata plus rows of
// JSON-friendly scalar values (string/number/bool/null).
type QueryRows struct {
	Columns  []QueryColumn
	Rows     [][]any
	RowCount int
}

// ExecuteRead runs an already-classified read statement on the read-only pool
// and serializes the result generically (any column set). The caller is
// responsible for classifying/limiting the SQL via the guard first; this method
// only executes and serializes. The read-only role plus the pool's
// statement_timeout bound what this can do and how long it can run.
func (m *Manager) ExecuteRead(ctx context.Context, sql string) (*QueryRows, error) {
	pool := m.ReadPool()
	if pool == nil {
		return nil, core.InternalError("pg: not connected").
			WithHint("Postgres is not reachable from the panel")
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, core.InternalError("pg: acquiring read connection").Wrap(err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		// A failed query is the operator's SQL error, not an internal fault:
		// surface it as a CodeExec so the SPA shows the message verbatim.
		return nil, core.ExecError("query failed: %s", firstLine(err.Error()))
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	tm := conn.Conn().TypeMap()
	columns := make([]QueryColumn, len(fds))
	for i, fd := range fds {
		typeName := fmt.Sprintf("oid:%d", fd.DataTypeOID)
		if t, ok := tm.TypeForOID(fd.DataTypeOID); ok && t.Name != "" {
			typeName = t.Name
		}
		columns[i] = QueryColumn{Name: fd.Name, DataType: typeName}
	}

	out := &QueryRows{Columns: columns, Rows: [][]any{}}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, core.ExecError("query failed reading row: %s", firstLine(err.Error()))
		}
		row := make([]any, len(vals))
		for i, v := range vals {
			row[i] = normalizeValue(v)
		}
		out.Rows = append(out.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, core.ExecError("query failed: %s", firstLine(err.Error()))
	}
	out.RowCount = len(out.Rows)
	return out, nil
}

// normalizeValue coerces a pgx-decoded value into a JSON-friendly scalar the SPA
// can render: nil, bool, a numeric, or a string. Bytes become a string,
// timestamps become RFC3339, and anything exotic (e.g. pgtype.Numeric, arrays,
// json) is rendered via its driver value or a string fallback.
func normalizeValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return x
	case []byte:
		return string(x)
	case [16]byte:
		// pgx decodes a uuid column to a bare [16]byte; render canonical UUID text.
		return formatUUID(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case map[string]any, []any:
		// json/jsonb (and array) columns decode to Go composites; emit JSON text
		// rather than Go's map/slice formatting.
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	default:
		if valuer, ok := v.(driver.Valuer); ok {
			if dv, err := valuer.Value(); err == nil {
				return normalizeDriverValue(dv)
			}
		}
		return fmt.Sprintf("%v", v)
	}
}

// formatUUID renders a 16-byte UUID as canonical 8-4-4-4-12 hex text.
func formatUUID(b [16]byte) string {
	var buf [36]byte
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf[:])
}

// normalizeDriverValue normalizes a database/sql/driver.Value (the small set of
// types a driver.Valuer may return) into a JSON-friendly scalar.
func normalizeDriverValue(v driver.Value) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case bool, int64, float64, string:
		return x
	default:
		return fmt.Sprintf("%v", v)
	}
}

// firstLine returns the first line of s, trimming a trailing newline. Postgres
// errors can be multi-line (with context/hints); the first line is the message.
func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
