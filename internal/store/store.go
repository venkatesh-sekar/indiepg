// Package store is the panel's local SQLite state, kept separate from the
// managed Postgres so the panel still works when PG is down. It uses
// modernc.org/sqlite (pure Go) via database/sql, so CGO_ENABLED=0 builds work.
//
// The schema is applied idempotently on Open via IF NOT EXISTS DDL; there is no
// external migration tooling. Typed accessors are grouped by table in the
// other files of this package.
package store

import (
	"context"
	"database/sql"
	"time"

	// Pure-Go SQLite driver, registered as "sqlite".
	_ "modernc.org/sqlite"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// Store is the handle to the panel's SQLite database. It is safe for concurrent
// use by multiple goroutines (database/sql pools connections).
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the SQLite database at path, applies the
// connection pragmas and the idempotent schema, and returns a ready Store. Use
// path ":memory:" for an ephemeral in-memory store in tests.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, core.InternalError("open sqlite at %q", path).Wrap(err)
	}
	// SQLite is single-writer; serialize to avoid SQLITE_BUSY under load.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.applyPragmas(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// DB exposes the underlying *sql.DB for advanced callers and tests.
func (s *Store) DB() *sql.DB { return s.db }

// Close closes the database.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Ping verifies connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return core.InternalError("ping store").Wrap(err)
	}
	return nil
}

func (s *Store) applyPragmas() error {
	for _, p := range connectionPragmas {
		if _, err := s.db.Exec(p); err != nil {
			return core.InternalError("apply pragma %q", p).Wrap(err)
		}
	}
	return nil
}

// migrate applies the schema statements idempotently inside a transaction.
func (s *Store) migrate() error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return core.InternalError("begin migration").Wrap(err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range schemaStatements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return core.InternalError("apply schema statement").WithDetail("sql", stmt).Wrap(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.InternalError("commit migration").Wrap(err)
	}
	return nil
}

// nowRFC3339 returns the current UTC time formatted as RFC3339Nano, the
// canonical timestamp string used throughout the store.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// parseTime parses an RFC3339 timestamp string. An empty string yields the zero
// time and no error.
func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, core.InternalError("parse time %q", s).Wrap(err)
	}
	return t, nil
}
