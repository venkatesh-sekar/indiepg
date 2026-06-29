package pg

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// This file owns the durable state for the version-upgrade feature. A major
// upgrade is a two-phase operation (run, then finalize-or-rollback) with a
// window in between during which the old cluster lingers as a rollback point.
// That "pending finalization" state — and the status of the in-flight operation
// — MUST survive a panel restart (a binary update mid-window must not strand the
// operator with two clusters and no UI to resolve them), so it is persisted in
// the panel's local store rather than held in memory.
//
// The whole state is one small JSON document kept under a single key in the
// existing key/value config table (StateStore), alongside the rest of the
// panel's persisted config. There is only ever zero or one upgrade in flight
// (enforced by the server's single global lock), so a single document is the
// natural fit and needs no schema migration.

// upgradeStateKey is the config-table key the UpgradeState JSON document lives
// under.
const upgradeStateKey = "pg_upgrade_state"

// Operation kinds recorded in OperationState.Kind.
const (
	OpMinor    = "minor"
	OpMajor    = "major"
	OpFinalize = "finalize"
	OpRollback = "rollback"
)

// Operation statuses recorded in OperationState.Status.
const (
	OpStatusRunning = "running"
	OpStatusSuccess = "success"
	OpStatusFailed  = "failed"
)

// PendingFinalization is the durable record of a completed major upgrade that is
// now live on the new major but still keeping the old cluster (stopped, on a
// moved port) as a rollback point. It is non-nil exactly while the operator must
// choose finalize (drop the old cluster, reclaim disk) or rollback (return to
// the old cluster).
//
// Its JSON shape is the API contract surfaced by GET /api/pg/version and
// /api/pg/upgrade/status (§7): from_major, to_major, reclaimable_bytes,
// upgraded_at.
type PendingFinalization struct {
	FromMajor        int       `json:"from_major"`
	ToMajor          int       `json:"to_major"`
	ReclaimableBytes int64     `json:"reclaimable_bytes"`
	UpgradedAt       time.Time `json:"upgraded_at"`
}

// OperationState is the status of the current (or most recent) upgrade
// operation, polled by the SPA via GET /api/pg/upgrade/status so the UI can show
// progress and resume after a reload. Log carries the human-readable step trail
// (commands/phases) for the operation drawer.
type OperationState struct {
	Kind        string     `json:"kind"`
	Status      string     `json:"status"`
	Phase       string     `json:"phase"`
	Message     string     `json:"message"`
	Error       string     `json:"error,omitempty"`
	FromMajor   int        `json:"from_major,omitempty"`
	TargetMajor int        `json:"target_major,omitempty"`
	Log         []string   `json:"log,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// PreflightMemo records the outcome of the most recent major-upgrade preflight
// so the start endpoint can enforce the "no major start without a clean (no-
// fail) preflight" guard (§10). It is internal state, not part of any API shape.
type PreflightMemo struct {
	TargetMajor int       `json:"target_major"`
	HasFail     bool      `json:"has_fail"`
	At          time.Time `json:"at"`
}

// UpgradeState is the single persisted document for the feature: the current/
// last operation, the pending-finalization record (if any), the rollback
// metadata needed to move the old cluster back onto the live port, and the last
// preflight memo. Only Operation and Pending are surfaced on the wire; the rest
// is internal bookkeeping.
type UpgradeState struct {
	Operation *OperationState      `json:"operation,omitempty"`
	Pending   *PendingFinalization `json:"pending,omitempty"`

	// OldClusterPort / OldDataDir are captured at the end of a major upgrade so a
	// rollback can swap the old cluster back onto the live port and a finalize can
	// size/drop the old data directory. They are meaningful only while Pending is
	// non-nil.
	OldClusterPort string `json:"old_cluster_port,omitempty"`
	OldDataDir     string `json:"old_data_dir,omitempty"`

	// LastPreflight is the most recent major-upgrade preflight outcome, consulted
	// by the start guard.
	LastPreflight *PreflightMemo `json:"last_preflight,omitempty"`
}

// StateStore is the minimal key/value persistence the upgrade state needs. The
// panel's *store.Store satisfies it directly (GetConfig/SetConfig/DeleteConfig);
// a missing key MUST come back as a *core.Error with CodeNotFound.
type StateStore interface {
	GetConfig(ctx context.Context, key string) (string, error)
	SetConfig(ctx context.Context, key, value string) error
	DeleteConfig(ctx context.Context, key string) error
}

// UpgradeStore reads and writes the durable UpgradeState document. Its mutex
// serializes the load-modify-write cycle so the async upgrade worker and the
// status handlers never clobber one another (the underlying SQLite store is
// single-writer, but the read-modify-write here still needs guarding).
type UpgradeStore struct {
	store StateStore
	mu    sync.Mutex
}

// NewUpgradeStore builds an UpgradeStore over a key/value store.
func NewUpgradeStore(store StateStore) *UpgradeStore {
	return &UpgradeStore{store: store}
}

// Load returns the current UpgradeState, or a zero-value (non-nil) state when no
// document has been written yet. A missing key is not an error.
func (u *UpgradeStore) Load(ctx context.Context) (*UpgradeState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.loadLocked(ctx)
}

func (u *UpgradeStore) loadLocked(ctx context.Context) (*UpgradeState, error) {
	if u.store == nil {
		return &UpgradeState{}, nil
	}
	raw, err := u.store.GetConfig(ctx, upgradeStateKey)
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return &UpgradeState{}, nil
		}
		return nil, err
	}
	if raw == "" {
		return &UpgradeState{}, nil
	}
	var st UpgradeState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil, core.InternalError("pg: decoding persisted upgrade state").Wrap(err)
	}
	return &st, nil
}

// saveLocked persists st. The caller must hold u.mu.
func (u *UpgradeStore) saveLocked(ctx context.Context, st *UpgradeState) error {
	if u.store == nil {
		return core.InternalError("pg: upgrade state has no backing store")
	}
	data, err := json.Marshal(st)
	if err != nil {
		return core.InternalError("pg: encoding upgrade state").Wrap(err)
	}
	return u.store.SetConfig(ctx, upgradeStateKey, string(data))
}

// Save persists the given state document.
func (u *UpgradeStore) Save(ctx context.Context, st *UpgradeState) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.saveLocked(ctx, st)
}

// Mutate loads the current state, applies fn, and persists the result
// atomically under the store lock, returning the updated state. It is the safe
// way to advance the document from the async worker.
func (u *UpgradeStore) Mutate(ctx context.Context, fn func(*UpgradeState)) (*UpgradeState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, err := u.loadLocked(ctx)
	if err != nil {
		return nil, err
	}
	fn(st)
	if err := u.saveLocked(ctx, st); err != nil {
		return nil, err
	}
	return st, nil
}
