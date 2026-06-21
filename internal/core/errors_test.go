package core

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestErrorCodeAndWrap(t *testing.T) {
	base := NotFoundError("database %q not found", "orders")
	require.Equal(t, CodeNotFound, base.Code)
	require.Contains(t, base.Error(), "orders")

	wrapped := fmt.Errorf("loading: %w", base)
	require.Equal(t, CodeNotFound, CodeOf(wrapped))

	got, ok := AsError(wrapped)
	require.True(t, ok)
	require.Equal(t, CodeNotFound, got.Code)
}

func TestErrorIsByCode(t *testing.T) {
	err := ConflictError("already exists")
	require.True(t, errors.Is(err, &Error{Code: CodeConflict}))
	require.False(t, errors.Is(err, &Error{Code: CodeNotFound}))
}

func TestErrorWrapCause(t *testing.T) {
	cause := errors.New("disk full")
	err := InternalError("write failed").Wrap(cause)
	require.ErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "disk full")
}

func TestSafetyError(t *testing.T) {
	err := NewSafetyError("drop database orders", []string{"confirm=orders"}, "needs confirm")
	require.Equal(t, CodeSafety, CodeOf(err))
	require.Equal(t, "drop database orders", err.Operation)
	require.Equal(t, []string{"confirm=orders"}, err.RequiredFlags)
}

func TestOwnershipError(t *testing.T) {
	err := NewOwnershipError("s3://b/p", "id-2", "10.0.0.5", "2026-06-21T00:00:00Z", false, "owned by another panel")
	require.Equal(t, CodeOwnership, CodeOf(err))
	require.Equal(t, "id-2", err.OwnerID)
	require.False(t, err.Adoptable)
}

func TestRequireConfirmation(t *testing.T) {
	require.Nil(t, RequireConfirmation("drop database orders", "orders", "orders"))

	err := RequireConfirmation("drop database orders", "orders", "wrong")
	require.NotNil(t, err)
	require.Equal(t, CodeSafety, CodeOf(err))

	// Empty expected never confirms.
	require.NotNil(t, RequireConfirmation("op", "", ""))
}
