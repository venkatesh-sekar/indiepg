package identity

import (
	"context"
	"time"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// Owner enforces single-writer ownership of a repo prefix in an ObjectStore.
// Its methods read and write the .panel-owner.json marker under a repo prefix.
//
// The cardinal rule: a non-stale foreign owner is a HARD STOP. Claim and Verify
// return a *core.OwnershipError that callers (backup manager) must never proceed
// past, because two writers would corrupt the pgBackRest repository.
type Owner struct {
	id  *Identity
	os  ObjectStore
	log *core.Logger
	// now is injectable so tests can control staleness; defaults to time.Now.
	now func() time.Time
}

// NewOwner builds an Owner for the given identity over an ObjectStore.
func NewOwner(id *Identity, os ObjectStore, log *core.Logger) *Owner {
	if log == nil {
		log = core.Discard()
	}
	return &Owner{
		id:  id,
		os:  os,
		log: log,
		now: func() time.Time { return time.Now().UTC() },
	}
}

// isMine reports whether the marker was written by this panel.
func (o *Owner) isMine(m OwnershipMarker) bool {
	return m.InstanceID == o.id.InstanceID
}

// foreignError builds the loud, actionable ownership error for a foreign marker.
// adoptable mirrors whether the foreign owner looks abandoned (stale). The full
// remediation hint is folded into the message so the error is self-contained
// regardless of how callers render it.
func (o *Owner) foreignError(repoPrefix string, m OwnershipMarker, now time.Time) *core.OwnershipError {
	adoptable := m.IsStale(now)
	lastSeen := m.LastSeen.UTC().Format(time.RFC3339Nano)
	resource := markerKey(repoPrefix)

	var msg string
	if adoptable {
		msg = "s3 repo %q is owned by panel %q (host %q), last active %s — it looks abandoned; two panels must never share a backup repository, so use a different prefix or run Adopt with confirm=%s if that server is truly gone"
		return core.NewOwnershipError(
			resource, m.InstanceID, m.Hostname, lastSeen, adoptable,
			msg, resource, m.InstanceID, m.Hostname, lastSeen, m.InstanceID,
		)
	}
	msg = "s3 repo %q is owned by panel %q (host %q), last active %s — two panels must never share a backup repository or both will be corrupted; use a different bucket/prefix (an active owner cannot be adopted)"
	return core.NewOwnershipError(
		resource, m.InstanceID, m.Hostname, lastSeen, adoptable,
		msg, resource, m.InstanceID, m.Hostname, lastSeen,
	)
}

// readMarker fetches and decodes the marker, distinguishing absent (nil,
// nil-marker) from a decode/transport error.
func (o *Owner) readMarker(ctx context.Context, repoPrefix string) (*OwnershipMarker, error) {
	data, err := o.os.GetObject(ctx, markerKey(repoPrefix))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, core.InternalError("read ownership marker for %q", repoPrefix).Wrap(err)
	}
	m, err := unmarshalMarker(data)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// writeMarker serializes and stores a marker.
func (o *Owner) writeMarker(ctx context.Context, repoPrefix string, m OwnershipMarker) error {
	data, err := marshalMarker(m)
	if err != nil {
		return err
	}
	if err := o.os.PutObject(ctx, markerKey(repoPrefix), data); err != nil {
		return core.InternalError("write ownership marker for %q", repoPrefix).Wrap(err)
	}
	return nil
}

// writeMarkerIfAbsent conditionally creates the marker, returning
// ErrPreconditionFailed (unwrapped, so callers can errors.Is it) when another
// writer already created one. Any other transport error is wrapped as internal.
func (o *Owner) writeMarkerIfAbsent(ctx context.Context, repoPrefix string, m OwnershipMarker) error {
	data, err := marshalMarker(m)
	if err != nil {
		return err
	}
	err = o.os.PutObjectIfAbsent(ctx, markerKey(repoPrefix), data)
	if err == nil {
		return nil
	}
	if isPreconditionFailed(err) {
		return err
	}
	return core.InternalError("write ownership marker for %q", repoPrefix).Wrap(err)
}

// systemIDMismatchError builds the HARD STOP for a "different cluster, same
// repo" collision: the marker is mine by instance id, but the stored Postgres
// system identifier differs from ours. Both ids are named so the operator can
// tell which cluster wrote the repo. It is reported as a CodeOwnership error so
// callers treat it like any other single-writer conflict and never proceed.
func (o *Owner) systemIDMismatchError(repoPrefix string, stored OwnershipMarker, now time.Time) *core.OwnershipError {
	resource := markerKey(repoPrefix)
	lastSeen := stored.LastSeen.UTC().Format(time.RFC3339Nano)
	msg := "s3 repo %q is marked as owned by this panel %q but its stored postgres system id %q does not match this cluster's %q — a different Postgres cluster wrote this repository; two clusters must never share a backup repository or both will be corrupted; use a different bucket/prefix"
	return core.NewOwnershipError(
		resource, stored.InstanceID, stored.Hostname, lastSeen, false,
		msg, resource, stored.InstanceID, stored.PGSystemID, o.id.PGSystemID,
	)
}

// systemIDConflicts reports whether the stored PGSystemID and ours are both
// non-empty and differ. An empty stored id (e.g. an older marker written before
// the system id was known) is not a conflict; it is filled in on the next write.
func (o *Owner) systemIDConflicts(stored OwnershipMarker) bool {
	return stored.PGSystemID != "" && o.id.PGSystemID != "" && stored.PGSystemID != o.id.PGSystemID
}

// Claim writes the marker if the repo is unclaimed or already mine, and returns
// the resulting marker. If a non-stale foreign owner holds the repo it returns a
// *core.OwnershipError (HARD STOP) and writes nothing. A stale foreign owner is
// also refused here — adoption is a separate, explicitly-confirmed action.
//
// The fresh-claim path is a conditional create (PutObjectIfAbsent), so two
// panels racing to claim the same unclaimed repo cannot both win: at most one
// If-None-Match:* create succeeds, and the loser re-reads the now-present marker
// and treats it as a foreign owner (HARD STOP) or as mine.
func (o *Owner) Claim(ctx context.Context, repoPrefix string) (*OwnershipMarker, error) {
	now := o.now()
	existing, err := o.readMarker(ctx, repoPrefix)
	if err != nil {
		return nil, err
	}

	// Unclaimed → claim it fresh via a conditional create that closes the race.
	if existing == nil {
		marker := OwnershipMarker{
			InstanceID: o.id.InstanceID,
			Hostname:   o.id.Hostname,
			PGSystemID: o.id.PGSystemID,
			ClaimedAt:  now,
			LastSeen:   now,
		}
		err := o.writeMarkerIfAbsent(ctx, repoPrefix, marker)
		if err == nil {
			o.log.InfoCtx(ctx, "claimed s3 repo ownership",
				"repo_prefix", repoPrefix, "instance_id", o.id.InstanceID)
			return &marker, nil
		}
		if !isPreconditionFailed(err) {
			return nil, err
		}
		// Lost the create race: another writer claimed it between our read and
		// our conditional write. Re-read and resolve against the winner.
		o.log.Warn("lost s3 repo claim race; re-reading marker",
			"repo_prefix", repoPrefix, "instance_id", o.id.InstanceID)
		existing, err = o.readMarker(ctx, repoPrefix)
		if err != nil {
			return nil, err
		}
		if existing == nil {
			// Vanished again (raced delete); surface as a transient conflict
			// rather than silently re-creating into a possible corruption.
			return nil, core.ConflictError("s3 repo %q changed under a concurrent claim; retry", markerKey(repoPrefix))
		}
		// Fall through to the mine/foreign resolution below.
	}

	// Already mine → confirm the cluster matches, then refresh the heartbeat.
	if o.isMine(*existing) {
		// "Different cluster, same repo" guard: if the stored Postgres system id
		// differs from ours, HARD STOP and do NOT overwrite the stored id.
		if o.systemIDConflicts(*existing) {
			oe := o.systemIDMismatchError(repoPrefix, *existing, now)
			o.log.Warn("refused to claim repo with mismatched postgres system id",
				"repo_prefix", repoPrefix,
				"stored_pg_system_id", existing.PGSystemID,
				"our_pg_system_id", o.id.PGSystemID)
			return nil, oe
		}
		marker := *existing
		marker.Hostname = o.id.Hostname
		// Preserve a previously-stored system id; only fill it when blank so we
		// never silently overwrite the cluster identity recorded in the repo.
		if marker.PGSystemID == "" {
			marker.PGSystemID = o.id.PGSystemID
		}
		marker.LastSeen = now
		if existing.ClaimedAt.IsZero() {
			marker.ClaimedAt = now
		}
		if err := o.writeMarker(ctx, repoPrefix, marker); err != nil {
			return nil, err
		}
		return &marker, nil
	}

	// Foreign owner → HARD STOP. Adoption (if stale) is a separate action.
	oe := o.foreignError(repoPrefix, *existing, now)
	o.log.Warn("refused to claim foreign-owned s3 repo",
		"repo_prefix", repoPrefix,
		"owner_instance_id", existing.InstanceID,
		"owner_host", existing.Hostname,
		"adoptable", oe.Adoptable)
	return nil, oe
}

// Verify reads the marker and confirms the repo is mine. It returns the marker
// on success, a *core.NotFoundError if the repo is unclaimed, or a
// *core.OwnershipError if a foreign owner holds it. Verify never writes.
func (o *Owner) Verify(ctx context.Context, repoPrefix string) (*OwnershipMarker, error) {
	now := o.now()
	existing, err := o.readMarker(ctx, repoPrefix)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, core.NotFoundError("s3 repo %q is unclaimed", markerKey(repoPrefix)).
			WithHint("run Claim before using this repository")
	}
	if !o.isMine(*existing) {
		return nil, o.foreignError(repoPrefix, *existing, now)
	}
	// Mine by instance id, but a mismatched stored cluster identity is still a
	// HARD STOP — a different Postgres cluster wrote this repo under our id.
	if o.systemIDConflicts(*existing) {
		return nil, o.systemIDMismatchError(repoPrefix, *existing, now)
	}
	return existing, nil
}

// Heartbeat updates LastSeen on my marker so an active repo never looks
// abandoned. Call it before each backup. It refuses (HARD STOP) if the repo has
// since been taken by a foreign owner, and reports CodeNotFound if the marker
// vanished. On a foreign owner Heartbeat never overwrites the marker.
func (o *Owner) Heartbeat(ctx context.Context, repoPrefix string) error {
	now := o.now()
	existing, err := o.readMarker(ctx, repoPrefix)
	if err != nil {
		return err
	}
	if existing == nil {
		return core.NotFoundError("s3 repo %q is unclaimed; cannot heartbeat", markerKey(repoPrefix)).
			WithHint("run Claim before heartbeating this repository")
	}
	if !o.isMine(*existing) {
		return o.foreignError(repoPrefix, *existing, now)
	}
	// Mine by instance id, but a mismatched stored cluster identity is a HARD
	// STOP: heartbeating would keep a corrupted "wrong cluster" claim alive.
	if o.systemIDConflicts(*existing) {
		return o.systemIDMismatchError(repoPrefix, *existing, now)
	}

	marker := *existing
	marker.LastSeen = now
	if err := o.writeMarker(ctx, repoPrefix, marker); err != nil {
		return err
	}
	o.log.DebugCtx(ctx, "heartbeat ownership marker",
		"repo_prefix", repoPrefix, "instance_id", o.id.InstanceID)
	return nil
}

// Adopt forcibly takes over a STALE foreign marker, rewriting it as mine. It
// requires a typed-name confirmation equal to the current owner's instance id
// (via core.RequireConfirmation) and refuses outright if:
//
//   - the repo is unclaimed (use Claim),
//   - the repo is already mine (no adoption needed),
//   - the foreign owner is still active (not stale) — an active owner can never
//     be adopted, period.
//
// On a non-stale owner it returns a *core.OwnershipError; on a bad/blank confirm
// it returns the *core.SafetyError from RequireConfirmation.
func (o *Owner) Adopt(ctx context.Context, repoPrefix, confirmTyped string) (*OwnershipMarker, error) {
	now := o.now()
	existing, err := o.readMarker(ctx, repoPrefix)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return nil, core.NotFoundError("s3 repo %q is unclaimed; nothing to adopt", markerKey(repoPrefix)).
			WithHint("run Claim to take an unclaimed repository")
	}
	if o.isMine(*existing) {
		return nil, core.ConflictError("s3 repo %q is already owned by this panel", markerKey(repoPrefix))
	}

	// An actively-owned (non-stale) repo can never be adopted. foreignError with a
	// non-stale marker already explains that an active owner cannot be adopted.
	if !existing.IsStale(now) {
		return nil, o.foreignError(repoPrefix, *existing, now)
	}

	// Stale owner → require typed-name confirmation of the current owner id.
	op := "adopt s3 repo " + markerKey(repoPrefix)
	if serr := core.RequireConfirmation(op, existing.InstanceID, confirmTyped); serr != nil {
		return nil, serr
	}

	marker := OwnershipMarker{
		InstanceID: o.id.InstanceID,
		Hostname:   o.id.Hostname,
		PGSystemID: o.id.PGSystemID,
		ClaimedAt:  now,
		LastSeen:   now,
	}
	if err := o.writeMarker(ctx, repoPrefix, marker); err != nil {
		return nil, err
	}
	o.log.Warn("adopted abandoned s3 repo",
		"repo_prefix", repoPrefix,
		"previous_owner", existing.InstanceID,
		"previous_owner_host", existing.Hostname,
		"new_owner", o.id.InstanceID)
	return &marker, nil
}
