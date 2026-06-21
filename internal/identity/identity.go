// Package identity provides the panel's stable instance identity and the
// single-writer ownership markers that make a shared S3 backup repository safe.
//
// Two panels accidentally pointed at the same pgBackRest repository would
// silently corrupt both. This package prevents that with two layers of defense
// described in the design (§6):
//
//  1. No collisions by construction — DefaultPrefix namespaces the repo path by
//     the panel's instance id (panel/<instance_id>), so two panels on the same
//     bucket land in different prefixes automatically.
//
//  2. Ownership marker + fail-fast — before writing a repo, a panel claims it by
//     writing a .panel-owner.json marker. Claim/Verify HARD STOP with a
//     *core.OwnershipError if a non-stale foreign owner already holds the repo.
//     The marker carries a heartbeat (LastSeen) so an abandoned repo (no
//     heartbeat for StaleAfter) can be Adopted with a typed-name confirmation,
//     while an actively-owned one never can.
//
// All IO goes through the ObjectStore interface; no networking lives here.
package identity

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
	"github.com/venkatesh-sekar/pgpanel/internal/store"
)

// MarkerObjectName is the S3 key (relative to a repo prefix) of the owner
// marker document.
const MarkerObjectName = ".panel-owner.json"

// StaleAfter is how long since LastSeen before an owner is considered abandoned
// and thus adoptable.
const StaleAfter = 15 * time.Minute

// OwnershipMarker is the JSON document written to a shared S3 repo to enforce
// single-writer ownership. The stored PGSystemID also catches the "different
// cluster, same repo" mistake precisely, before pgBackRest fails cryptically.
type OwnershipMarker struct {
	InstanceID string    `json:"instance_id"`
	Hostname   string    `json:"hostname"`
	PGSystemID string    `json:"pg_system_id"`
	ClaimedAt  time.Time `json:"claimed_at"`
	LastSeen   time.Time `json:"last_seen"`
}

// IsStale reports whether the owner's last heartbeat is older than StaleAfter
// relative to now, meaning the repo looks abandoned and may be adopted.
func (m OwnershipMarker) IsStale(now time.Time) bool {
	return now.Sub(m.LastSeen) > StaleAfter
}

// ObjectStore is the minimal S3 surface the marker logic needs. The real
// implementation in internal/backup adapts minio-go; tests use a fake.
type ObjectStore interface {
	// GetObject returns the object bytes, or a *core.Error with CodeNotFound
	// when the key is absent.
	GetObject(ctx context.Context, key string) ([]byte, error)
	PutObject(ctx context.Context, key string, data []byte) error
	DeleteObject(ctx context.Context, key string) error
}

// Identity wraps the panel's stable identity loaded from the local store.
type Identity struct {
	InstanceID string
	Label      string
	Hostname   string
	PGSystemID string
}

// Load reads the instance row from the store into an Identity. It returns the
// store's *core.Error (CodeNotFound) if the panel has not been installed yet.
func Load(ctx context.Context, st *store.Store) (*Identity, error) {
	inst, err := st.GetInstance(ctx)
	if err != nil {
		return nil, err
	}
	return &Identity{
		InstanceID: inst.InstanceID,
		Label:      inst.Label,
		Hostname:   inst.Hostname,
		PGSystemID: inst.PGSystemID,
	}, nil
}

// Generate creates a new identity (a fresh UUIDv4 plus the host's name) and
// persists it to the store. It is used by the install flow. If label is empty
// the hostname is used as the human label.
func Generate(ctx context.Context, st *store.Store, label, panelVersion string) (*Identity, error) {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		// A missing hostname is not fatal to identity generation; fall back to a
		// stable placeholder so the panel still gets a unique instance id.
		hostname = "unknown-host"
	}
	if strings.TrimSpace(label) == "" {
		label = hostname
	}

	id := &Identity{
		InstanceID: uuid.NewString(),
		Label:      label,
		Hostname:   hostname,
	}

	inst := store.Instance{
		InstanceID:   id.InstanceID,
		Label:        id.Label,
		Hostname:     id.Hostname,
		PGSystemID:   "",
		PanelVersion: panelVersion,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.SaveInstance(ctx, inst); err != nil {
		return nil, err
	}
	return id, nil
}

// DefaultPrefix namespaces a base prefix by instance id: "panel/<instance_id>".
// The base is an optional caller-chosen prefix (e.g. a bucket sub-path); when
// non-empty it is prepended, slash-joined, so two panels on the same bucket can
// never collide by construction.
func DefaultPrefix(base, instanceID string) string {
	ns := "panel/" + instanceID
	base = strings.Trim(strings.TrimSpace(base), "/")
	if base == "" {
		return ns
	}
	return base + "/" + ns
}

// DefaultPrefix namespaces a base prefix by this identity's instance id.
func (id *Identity) DefaultPrefix(base string) string {
	return DefaultPrefix(base, id.InstanceID)
}

// markerKey joins a repo prefix with the marker object name, tolerating a
// trailing slash on the prefix and an empty prefix (bucket root).
func markerKey(repoPrefix string) string {
	repoPrefix = strings.Trim(strings.TrimSpace(repoPrefix), "/")
	if repoPrefix == "" {
		return MarkerObjectName
	}
	return repoPrefix + "/" + MarkerObjectName
}

// isNotFound reports whether err is (or wraps) a CodeNotFound panel error.
func isNotFound(err error) bool {
	return err != nil && core.CodeOf(err) == core.CodeNotFound
}

// marshalMarker serializes a marker to indented JSON with a trailing newline,
// matching how the rest of the panel writes JSON documents to S3.
func marshalMarker(m OwnershipMarker) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, core.InternalError("encode ownership marker").Wrap(err)
	}
	return append(data, '\n'), nil
}

// unmarshalMarker parses a marker document, returning a CodeInternal error on
// malformed JSON so a corrupt marker surfaces clearly rather than as a silent
// foreign owner.
func unmarshalMarker(data []byte) (OwnershipMarker, error) {
	var m OwnershipMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return OwnershipMarker{}, core.InternalError("decode ownership marker").Wrap(err)
	}
	return m, nil
}
