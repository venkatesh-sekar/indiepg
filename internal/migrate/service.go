package migrate

import (
	"context"
	"encoding/json"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/exec"
)

// DefaultTTL is the fallback session lifetime when CreateSession is given a
// non-positive ttl. Sessions are short-lived by design: the shared S3 prefix is
// only safe because it expires.
const DefaultTTL = 2 * time.Hour

// sessionFile is the object name (under the per-code prefix) of the coordinating
// session document.
const sessionFile = "session.json"

// ObjectStore is the minimal S3 surface the session coordination needs. It
// mirrors identity.ObjectStore semantics: GetObject returns a *core.Error with
// CodeNotFound when the key is absent. minio-go is adapted to this in
// internal/backup; tests use a fake.
type ObjectStore interface {
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, data []byte) error
	DeleteObject(ctx context.Context, key string) error
}

// Service drives migration sessions over an S3 ObjectStore. It owns the key
// layout, JSON (de)serialization, expiry enforcement on read, and the state
// transitions persisted back to S3. Heavy data movement (pg_dump/pg_restore)
// is shelled out through the Runner; the pure coordination logic lives here.
type Service struct {
	os     ObjectStore
	runner exec.Runner
	log    *core.Logger
	// now is overridable in tests; defaults to time.Now.
	now func() time.Time
}

// NewService builds a Service over the given object store and command runner.
func NewService(os ObjectStore, runner exec.Runner, log *core.Logger) *Service {
	if log == nil {
		log = core.Discard()
	}
	return &Service{os: os, runner: runner, log: log, now: time.Now}
}

// SessionKey returns the S3 key of the coordinating document for a code:
// pg-migrations/sessions/<code>/session.json.
func SessionKey(code string) string {
	return SessionPrefix + "/" + code + "/" + sessionFile
}

// DumpKey returns the S3 key under which a session's dump is stored:
// pg-migrations/sessions/<code>/dump.bin.
func DumpKey(code string) string {
	return SessionPrefix + "/" + code + "/dump.bin"
}

// sessionDir returns the per-code prefix (with trailing slash) for cleanup.
func sessionDir(code string) string {
	return SessionPrefix + "/" + code + "/"
}

// CreateSession (TARGET role) mints a new code, writes the initial waiting
// document to S3, and returns it. A non-positive ttl uses DefaultTTL.
func (s *Service) CreateSession(ctx context.Context, database string, ttl time.Duration) (*MigrationSession, error) {
	if database == "" {
		return nil, core.ValidationError("database is required to create a migration session")
	}
	if err := core.ValidateIdentifier(database, "database"); err != nil {
		return nil, err
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	now := s.now().UTC()
	sess := &MigrationSession{
		Code:      GenerateCode(),
		Database:  database,
		Status:    StatusWaiting,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	data, err := marshalSession(sess)
	if err != nil {
		return nil, err
	}
	if err := s.os.PutObject(ctx, SessionKey(sess.Code), data); err != nil {
		return nil, core.InternalError("failed to write migration session %s", sess.Code).Wrap(err)
	}
	s.log.InfoCtx(ctx, "migration session created", "code", sess.Code, "database", database, "expires_at", sess.ExpiresAt)
	return sess, nil
}

// GetSession reads and parses a session document by code. If the document is
// absent it returns a *core.Error CodeNotFound. If the wall clock has passed
// the session's expiry and the session is not already terminal, the returned
// session has Status StatusExpired (the stored object is left untouched; callers
// that wish to persist the expiry should UpdateSession).
func (s *Service) GetSession(ctx context.Context, code string) (*MigrationSession, error) {
	if code == "" {
		return nil, core.ValidationError("session code is required")
	}
	data, err := s.os.GetObject(ctx, SessionKey(code))
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return nil, core.NotFoundError("migration session %s not found", code).Wrap(err)
		}
		return nil, core.InternalError("failed to read migration session %s", code).Wrap(err)
	}
	sess, err := unmarshalSession(data)
	if err != nil {
		return nil, err
	}
	// Surface expiry on read so live states do not look importable past the
	// deadline. Terminal states are reported as-is.
	if !sess.Status.IsTerminal() && sess.IsExpired(s.now().UTC()) {
		sess.Status = StatusExpired
	}
	return sess, nil
}

// UpdateSession persists a session document back to S3. It validates the code
// is present; callers are responsible for having applied a legal CanTransition
// before calling. The session is normalized to UTC timestamps on write.
func (s *Service) UpdateSession(ctx context.Context, sess *MigrationSession) error {
	if sess == nil {
		return core.ValidationError("session is nil")
	}
	if sess.Code == "" {
		return core.ValidationError("session has no code")
	}
	data, err := marshalSession(sess)
	if err != nil {
		return err
	}
	if err := s.os.PutObject(ctx, SessionKey(sess.Code), data); err != nil {
		return core.InternalError("failed to update migration session %s", sess.Code).Wrap(err)
	}
	return nil
}

// Transition applies a legal state-machine edge to the session and persists it.
// It returns a *core.Error CodeConflict if from->to is not a legal edge. This is
// the safe way to advance a session: it refuses illegal jumps before writing.
func (s *Service) Transition(ctx context.Context, sess *MigrationSession, to Status) error {
	if sess == nil {
		return core.ValidationError("session is nil")
	}
	if !CanTransition(sess.Status, to) {
		return core.ConflictError("illegal session transition %q -> %q", sess.Status, to).
			WithHint("the session is not in a state that allows this action")
	}
	sess.Status = to
	return s.UpdateSession(ctx, sess)
}

// CleanupSession deletes a session's coordinating document and its dump. It is
// best-effort and idempotent: a missing object is not an error. Errors other
// than not-found are returned (wrapped) for the first failing delete.
func (s *Service) CleanupSession(ctx context.Context, code string) error {
	if code == "" {
		return core.ValidationError("session code is required")
	}
	keys := []string{DumpKey(code), SessionKey(code)}
	for _, key := range keys {
		if err := s.os.DeleteObject(ctx, key); err != nil {
			if core.CodeOf(err) == core.CodeNotFound {
				continue
			}
			return core.InternalError("failed to delete %s for session %s", key, code).Wrap(err)
		}
	}
	s.log.InfoCtx(ctx, "migration session cleaned up", "code", code, "prefix", sessionDir(code))
	return nil
}

// marshalSession serializes a session, normalizing timestamps to UTC so the
// stored document always uses RFC3339Nano UTC (Go's time.Time JSON encoding is
// RFC3339Nano; UTC keeps it unambiguous across panels).
func marshalSession(sess *MigrationSession) ([]byte, error) {
	cp := *sess
	cp.CreatedAt = cp.CreatedAt.UTC()
	cp.ExpiresAt = cp.ExpiresAt.UTC()
	data, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return nil, core.InternalError("failed to encode migration session %s", sess.Code).Wrap(err)
	}
	return data, nil
}

// unmarshalSession parses a session document, returning a *core.Error on
// malformed JSON.
func unmarshalSession(data []byte) (*MigrationSession, error) {
	var sess MigrationSession
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, core.InternalError("malformed migration session document").Wrap(err)
	}
	return &sess, nil
}
