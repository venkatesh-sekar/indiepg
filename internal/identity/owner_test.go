package identity

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

const (
	myID    = "11111111-1111-1111-1111-111111111111"
	otherID = "22222222-2222-2222-2222-222222222222"
)

// ownerAt builds an Owner whose clock is pinned to now.
func ownerAt(os ObjectStore, now time.Time) *Owner {
	id := &Identity{
		InstanceID: myID,
		Label:      "web-db-01",
		Hostname:   "host-a",
		PGSystemID: "7300000000000000001",
	}
	o := NewOwner(id, os, core.Discard())
	o.now = func() time.Time { return now }
	return o
}

// seedForeign writes a foreign-owned marker under repo with the given lastSeen.
func seedForeign(t *testing.T, os *fakeObjectStore, repo string, lastSeen time.Time) {
	t.Helper()
	m := OwnershipMarker{
		InstanceID: otherID,
		Hostname:   "host-b",
		PGSystemID: "7300000000000000002",
		ClaimedAt:  lastSeen.Add(-time.Hour),
		LastSeen:   lastSeen,
	}
	data, err := marshalMarker(m)
	require.NoError(t, err)
	os.set(markerKey(repo), data)
}

func TestClaimUnclaimed(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	o := ownerAt(os, now)

	m, err := o.Claim(ctx, "panel/"+myID)
	require.NoError(t, err)
	require.Equal(t, myID, m.InstanceID)
	require.Equal(t, "host-a", m.Hostname)
	require.Equal(t, "7300000000000000001", m.PGSystemID)
	require.Equal(t, now, m.ClaimedAt)
	require.Equal(t, now, m.LastSeen)

	// Marker actually written.
	_, ok := os.raw(markerKey("panel/" + myID))
	require.True(t, ok)
}

func TestClaimAlreadyMineRefreshesHeartbeat(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID

	claimedAt := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	o1 := ownerAt(os, claimedAt)
	first, err := o1.Claim(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, claimedAt, first.ClaimedAt)

	// Re-claim later: same instance, claimed_at preserved, last_seen advanced.
	later := claimedAt.Add(30 * time.Minute)
	o2 := ownerAt(os, later)
	second, err := o2.Claim(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, claimedAt, second.ClaimedAt, "claimed_at preserved on re-claim")
	require.Equal(t, later, second.LastSeen, "last_seen advanced on re-claim")
}

func TestClaimForeignActiveHardStop(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/shared"
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Foreign owner heartbeated 4 minutes ago → active, not adoptable.
	seedForeign(t, os, repo, now.Add(-4*time.Minute))

	o := ownerAt(os, now)
	beforePuts := os.puts
	_, err := o.Claim(ctx, repo)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	var oe *core.OwnershipError
	require.ErrorAs(t, err, &oe)
	require.Equal(t, otherID, oe.OwnerID)
	require.Equal(t, "host-b", oe.OwnerHost)
	require.False(t, oe.Adoptable, "active owner is not adoptable")

	// HARD STOP: nothing written.
	require.Equal(t, beforePuts, os.puts, "claim must not overwrite a foreign marker")
}

func TestClaimForeignStaleStillRefuses(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/shared"
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Stale (abandoned) foreign owner.
	seedForeign(t, os, repo, now.Add(-StaleAfter-time.Minute))

	o := ownerAt(os, now)
	beforePuts := os.puts
	_, err := o.Claim(ctx, repo)
	require.Error(t, err)

	var oe *core.OwnershipError
	require.ErrorAs(t, err, &oe)
	require.True(t, oe.Adoptable, "stale owner is flagged adoptable")
	require.Equal(t, beforePuts, os.puts, "claim must not adopt; that is a separate action")
}

// TestClaimCASRaceLostToForeign exercises the conditional-create (CAS) path: the
// owner reads the repo as absent, but a foreign panel claims it before our
// conditional write lands. PutObjectIfAbsent then fails the precondition, we
// re-read, and the now-present foreign marker is a HARD STOP — proving two
// panels can never both claim a fresh repo.
func TestClaimCASRaceLostToForeign(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/shared"
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Model the race: the first GetObject (the initial read) sees the repo as
	// absent, but by the time we conditionally create, a foreign owner exists.
	// We seed that foreign marker into the store, and force only the first read
	// to report not-found so the fresh-claim path is taken.
	seedForeign(t, os, repo, now.Add(-time.Minute)) // active foreign owner present
	firstRead := true
	os.getErr = func(string) error {
		if firstRead {
			firstRead = false
			return core.NotFoundError("not found (raced)")
		}
		return nil // subsequent reads see the real (foreign) marker
	}

	o := ownerAt(os, now)
	_, err := o.Claim(ctx, repo)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	var oe *core.OwnershipError
	require.ErrorAs(t, err, &oe)
	require.Equal(t, otherID, oe.OwnerID, "lost the race to the foreign owner")

	// The conditional create must have been attempted (and refused), and the
	// foreign marker must be untouched.
	require.Equal(t, 1, os.putsIf, "fresh claim must go through the conditional create")
	require.Equal(t, 0, os.puts, "losing the race must not overwrite the foreign marker")
}

// TestClaimCASRaceLostToMine covers losing the create race to a marker that turns
// out to be mine (e.g. a concurrent claim by the same panel): after the
// precondition failure we re-read, see our own marker, and proceed by refreshing
// the heartbeat rather than erroring.
func TestClaimCASRaceLostToMine(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// Seed a marker already owned by me (as if a sibling goroutine just claimed).
	mine := OwnershipMarker{
		InstanceID: myID,
		Hostname:   "host-a",
		PGSystemID: "7300000000000000001",
		ClaimedAt:  now.Add(-time.Hour),
		LastSeen:   now.Add(-time.Hour),
	}
	data, err := marshalMarker(mine)
	require.NoError(t, err)
	os.set(markerKey(repo), data)

	firstRead := true
	os.getErr = func(string) error {
		if firstRead {
			firstRead = false
			return core.NotFoundError("not found (raced)")
		}
		return nil
	}

	o := ownerAt(os, now)
	m, err := o.Claim(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, myID, m.InstanceID)
	require.Equal(t, now, m.LastSeen, "heartbeat refreshed after losing the race to myself")
	require.Equal(t, now.Add(-time.Hour), m.ClaimedAt, "claimed_at preserved")
	require.Equal(t, 1, os.putsIf, "conditional create attempted")
	require.Equal(t, 1, os.puts, "re-claim refreshes the heartbeat via a normal write")
}

// TestClaimFreshUsesConditionalCreate asserts the happy fresh-claim path goes
// through PutObjectIfAbsent (not the unconditional PutObject), so the race is
// closed by construction.
func TestClaimFreshUsesConditionalCreate(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	o := ownerAt(os, now)

	_, err := o.Claim(ctx, "panel/"+myID)
	require.NoError(t, err)
	require.Equal(t, 1, os.putsIf, "fresh claim must use the conditional create primitive")
	require.Equal(t, 0, os.puts, "fresh claim must not use the unconditional put")
}

// TestClaimSystemIDMismatchHardStop covers the "different cluster, same repo"
// guard: the marker is mine by instance id, but its stored Postgres system id
// differs from ours. Claim must HARD STOP and never overwrite the stored id.
func TestClaimSystemIDMismatchHardStop(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	// A marker written by my instance id but a DIFFERENT cluster.
	stored := OwnershipMarker{
		InstanceID: myID,
		Hostname:   "host-a",
		PGSystemID: "9999999999999999999", // ours (ownerAt) is 7300000000000000001
		ClaimedAt:  now.Add(-time.Hour),
		LastSeen:   now.Add(-time.Minute),
	}
	data, err := marshalMarker(stored)
	require.NoError(t, err)
	os.set(markerKey(repo), data)

	o := ownerAt(os, now)
	beforePuts := os.puts
	_, err = o.Claim(ctx, repo)
	require.Error(t, err)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	var oe *core.OwnershipError
	require.ErrorAs(t, err, &oe)
	require.False(t, oe.Adoptable)
	require.Contains(t, oe.Error(), "9999999999999999999", "error names the stored system id")
	require.Contains(t, oe.Error(), "7300000000000000001", "error names our system id")

	// HARD STOP: the stored marker (and its system id) must be untouched.
	require.Equal(t, beforePuts, os.puts, "must not overwrite a mismatched system id")
	raw, ok := os.raw(markerKey(repo))
	require.True(t, ok)
	require.Contains(t, string(raw), "9999999999999999999")
}

// TestClaimEmptyStoredSystemIDFilledNotConflict ensures an older marker with a
// blank stored system id is not treated as a conflict; the heartbeat fills it in.
func TestClaimEmptyStoredSystemIDFilledNotConflict(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	stored := OwnershipMarker{
		InstanceID: myID,
		Hostname:   "host-a",
		PGSystemID: "", // unknown when the marker was first written
		ClaimedAt:  now.Add(-time.Hour),
		LastSeen:   now.Add(-time.Minute),
	}
	data, err := marshalMarker(stored)
	require.NoError(t, err)
	os.set(markerKey(repo), data)

	o := ownerAt(os, now)
	m, err := o.Claim(ctx, repo)
	require.NoError(t, err)
	require.Equal(t, "7300000000000000001", m.PGSystemID, "blank stored id is filled with ours")
}

// TestVerifySystemIDMismatchHardStop covers the read-side guard: Verify must HARD
// STOP (never write) when the stored cluster id differs from ours.
func TestVerifySystemIDMismatchHardStop(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	stored := OwnershipMarker{
		InstanceID: myID,
		Hostname:   "host-a",
		PGSystemID: "9999999999999999999",
		ClaimedAt:  now.Add(-time.Hour),
		LastSeen:   now.Add(-time.Minute),
	}
	data, err := marshalMarker(stored)
	require.NoError(t, err)
	os.set(markerKey(repo), data)

	o := ownerAt(os, now)
	beforePuts := os.puts
	_, err = o.Verify(ctx, repo)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	require.Equal(t, beforePuts, os.puts, "verify never writes")
}

// TestHeartbeatSystemIDMismatchHardStop covers the heartbeat guard: a mismatched
// stored cluster id is a HARD STOP and must not keep the wrong-cluster claim
// alive.
func TestHeartbeatSystemIDMismatchHardStop(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "panel/" + myID
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	stored := OwnershipMarker{
		InstanceID: myID,
		Hostname:   "host-a",
		PGSystemID: "9999999999999999999",
		ClaimedAt:  now.Add(-time.Hour),
		LastSeen:   now.Add(-time.Minute),
	}
	data, err := marshalMarker(stored)
	require.NoError(t, err)
	os.set(markerKey(repo), data)

	o := ownerAt(os, now)
	beforePuts := os.puts
	err = o.Heartbeat(ctx, repo)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	require.Equal(t, beforePuts, os.puts, "must not refresh a mismatched-cluster marker")
}

func TestVerify(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	t.Run("unclaimed is not found", func(t *testing.T) {
		os := newFakeObjectStore()
		o := ownerAt(os, now)
		_, err := o.Verify(ctx, "panel/x")
		require.Equal(t, core.CodeNotFound, core.CodeOf(err))
	})

	t.Run("mine verifies and does not write", func(t *testing.T) {
		os := newFakeObjectStore()
		o := ownerAt(os, now)
		_, err := o.Claim(ctx, "panel/"+myID)
		require.NoError(t, err)
		putsAfterClaim := os.puts

		m, err := o.Verify(ctx, "panel/"+myID)
		require.NoError(t, err)
		require.Equal(t, myID, m.InstanceID)
		require.Equal(t, putsAfterClaim, os.puts, "verify must not write")
	})

	t.Run("foreign is ownership error", func(t *testing.T) {
		os := newFakeObjectStore()
		seedForeign(t, os, "panel/shared", now.Add(-time.Minute))
		o := ownerAt(os, now)
		_, err := o.Verify(ctx, "panel/shared")
		require.Equal(t, core.CodeOwnership, core.CodeOf(err))
	})
}

func TestHeartbeat(t *testing.T) {
	ctx := context.Background()

	t.Run("advances last_seen on my marker", func(t *testing.T) {
		os := newFakeObjectStore()
		t0 := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
		o0 := ownerAt(os, t0)
		_, err := o0.Claim(ctx, "panel/"+myID)
		require.NoError(t, err)

		t1 := t0.Add(time.Hour)
		o1 := ownerAt(os, t1)
		require.NoError(t, o1.Heartbeat(ctx, "panel/"+myID))

		m, err := o1.Verify(ctx, "panel/"+myID)
		require.NoError(t, err)
		require.Equal(t, t1, m.LastSeen)
		require.Equal(t, t0, m.ClaimedAt, "heartbeat keeps claimed_at")
	})

	t.Run("unclaimed repo cannot heartbeat", func(t *testing.T) {
		os := newFakeObjectStore()
		o := ownerAt(os, time.Now().UTC())
		err := o.Heartbeat(ctx, "panel/missing")
		require.Equal(t, core.CodeNotFound, core.CodeOf(err))
	})

	t.Run("foreign owner blocks heartbeat without overwriting", func(t *testing.T) {
		os := newFakeObjectStore()
		now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
		seedForeign(t, os, "panel/shared", now.Add(-time.Minute))
		o := ownerAt(os, now)
		beforePuts := os.puts
		err := o.Heartbeat(ctx, "panel/shared")
		require.Equal(t, core.CodeOwnership, core.CodeOf(err))
		require.Equal(t, beforePuts, os.puts)
	})
}

func TestAdopt(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)

	t.Run("stale owner adopted with correct confirm", func(t *testing.T) {
		os := newFakeObjectStore()
		repo := "panel/shared"
		seedForeign(t, os, repo, now.Add(-StaleAfter-time.Minute))
		o := ownerAt(os, now)

		m, err := o.Adopt(ctx, repo, otherID)
		require.NoError(t, err)
		require.Equal(t, myID, m.InstanceID)
		require.Equal(t, "host-a", m.Hostname)
		require.Equal(t, now, m.ClaimedAt)
		require.Equal(t, now, m.LastSeen)

		// Now it verifies as mine.
		got, err := o.Verify(ctx, repo)
		require.NoError(t, err)
		require.Equal(t, myID, got.InstanceID)
	})

	t.Run("wrong confirm is a safety error and writes nothing", func(t *testing.T) {
		os := newFakeObjectStore()
		repo := "panel/shared"
		seedForeign(t, os, repo, now.Add(-StaleAfter-time.Minute))
		o := ownerAt(os, now)
		beforePuts := os.puts

		_, err := o.Adopt(ctx, repo, "wrong-id")
		require.Equal(t, core.CodeSafety, core.CodeOf(err))
		var se *core.SafetyError
		require.ErrorAs(t, err, &se)
		require.Equal(t, beforePuts, os.puts, "failed confirm must not overwrite")
	})

	t.Run("blank confirm is rejected", func(t *testing.T) {
		os := newFakeObjectStore()
		repo := "panel/shared"
		seedForeign(t, os, repo, now.Add(-StaleAfter-time.Minute))
		o := ownerAt(os, now)
		_, err := o.Adopt(ctx, repo, "")
		require.Equal(t, core.CodeSafety, core.CodeOf(err))
	})

	t.Run("active owner can never be adopted", func(t *testing.T) {
		os := newFakeObjectStore()
		repo := "panel/shared"
		seedForeign(t, os, repo, now.Add(-time.Minute)) // active
		o := ownerAt(os, now)
		beforePuts := os.puts

		// Even with the correct confirm, an active owner is refused.
		_, err := o.Adopt(ctx, repo, otherID)
		require.Equal(t, core.CodeOwnership, core.CodeOf(err))
		var oe *core.OwnershipError
		require.ErrorAs(t, err, &oe)
		require.False(t, oe.Adoptable)
		require.Equal(t, beforePuts, os.puts)
	})

	t.Run("unclaimed repo has nothing to adopt", func(t *testing.T) {
		os := newFakeObjectStore()
		o := ownerAt(os, now)
		_, err := o.Adopt(ctx, "panel/empty", otherID)
		require.Equal(t, core.CodeNotFound, core.CodeOf(err))
	})

	t.Run("already mine is a conflict", func(t *testing.T) {
		os := newFakeObjectStore()
		o := ownerAt(os, now)
		_, err := o.Claim(ctx, "panel/"+myID)
		require.NoError(t, err)
		_, err = o.Adopt(ctx, "panel/"+myID, myID)
		require.Equal(t, core.CodeConflict, core.CodeOf(err))
	})
}

func TestClaimDecodeErrorOnCorruptMarker(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	os.set(markerKey("panel/x"), []byte("{garbage"))
	o := ownerAt(os, time.Now().UTC())

	_, err := o.Claim(ctx, "panel/x")
	require.Error(t, err)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestNewOwnerNilLoggerDoesNotPanic(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	id := &Identity{InstanceID: myID, Hostname: "h"}
	o := NewOwner(id, os, nil)
	_, err := o.Claim(ctx, "panel/"+myID)
	require.NoError(t, err)
}

func TestClaimWrapsTransportErrors(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	t.Run("get transport error is wrapped internal", func(t *testing.T) {
		os := newFakeObjectStore()
		os.getErr = func(string) error { return core.ExecError("s3 unreachable") }
		o := ownerAt(os, now)
		_, err := o.Claim(ctx, "panel/x")
		require.Error(t, err)
		require.Equal(t, core.CodeInternal, core.CodeOf(err))
	})

	t.Run("put error on fresh claim is wrapped", func(t *testing.T) {
		os := newFakeObjectStore()
		// The fresh-claim path is a conditional create (PutObjectIfAbsent), so
		// inject the transport error on that primitive, not the plain PutObject.
		os.putIfErr = core.ExecError("s3 write denied")
		o := ownerAt(os, now)
		_, err := o.Claim(ctx, "panel/x")
		require.Error(t, err)
		require.Equal(t, core.CodeInternal, core.CodeOf(err))
	})
}

func TestHeartbeatWrapsPutError(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	t0 := time.Date(2026, 6, 21, 10, 0, 0, 0, time.UTC)
	o := ownerAt(os, t0)
	_, err := o.Claim(ctx, "panel/"+myID)
	require.NoError(t, err)

	// Now fail the next write.
	os.putErr = core.ExecError("s3 write denied")
	err = o.Heartbeat(ctx, "panel/"+myID)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestVerifyWrapsTransportError(t *testing.T) {
	ctx := context.Background()
	os := newFakeObjectStore()
	os.getErr = func(string) error { return core.ExecError("s3 unreachable") }
	o := ownerAt(os, time.Now().UTC())
	_, err := o.Verify(ctx, "panel/x")
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestAdoptWritesNewMarkerErrorWrapped(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	os := newFakeObjectStore()
	repo := "panel/shared"
	seedForeign(t, os, repo, now.Add(-StaleAfter-time.Minute))
	o := ownerAt(os, now)

	os.putErr = core.ExecError("s3 write denied")
	_, err := o.Adopt(ctx, repo, otherID)
	require.Equal(t, core.CodeInternal, core.CodeOf(err))
}

func TestObjectStoreDeleteIsAvailable(t *testing.T) {
	// The ObjectStore interface requires DeleteObject; ensure the fake satisfies
	// the full surface used by callers (and exercise the delete accounting).
	ctx := context.Background()
	os := newFakeObjectStore()
	require.NoError(t, os.PutObject(ctx, "k", []byte("v")))
	require.NoError(t, os.DeleteObject(ctx, "k"))
	_, ok := os.raw("k")
	require.False(t, ok)
	require.Equal(t, 1, os.deletes)

	var _ ObjectStore = os
}

func TestClaimAndVerifyEndToEnd(t *testing.T) {
	// A full lifecycle: claim, heartbeat keeps it active, a second panel is
	// hard-stopped, then after the owner abandons it the second panel adopts.
	ctx := context.Background()
	os := newFakeObjectStore()
	repo := "backups/panel/" + myID

	t0 := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	mine := ownerAt(os, t0)
	_, err := mine.Claim(ctx, repo)
	require.NoError(t, err)

	// A different panel tries to claim the same repo while mine is active.
	otherIdent := &Identity{InstanceID: otherID, Hostname: "host-b"}
	other := NewOwner(otherIdent, os, core.Discard())
	other.now = func() time.Time { return t0.Add(time.Minute) }
	_, err = other.Claim(ctx, repo)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))

	// My panel goes silent; long after StaleAfter, the other panel adopts.
	adoptTime := t0.Add(StaleAfter + time.Hour)
	other.now = func() time.Time { return adoptTime }
	m, err := other.Adopt(ctx, repo, myID)
	require.NoError(t, err)
	require.Equal(t, otherID, m.InstanceID)

	// Now the original owner is locked out.
	mine.now = func() time.Time { return adoptTime.Add(time.Minute) }
	err = mine.Heartbeat(ctx, repo)
	require.Equal(t, core.CodeOwnership, core.CodeOf(err))
}
