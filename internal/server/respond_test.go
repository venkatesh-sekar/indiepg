package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

func TestStatusForCode(t *testing.T) {
	tests := []struct {
		code core.Code
		want int
	}{
		{core.CodeValidation, http.StatusBadRequest},
		{core.CodeAuth, http.StatusUnauthorized},
		{core.CodeLocked, http.StatusTooManyRequests},
		{core.CodeSafety, http.StatusConflict},
		{core.CodeOwnership, http.StatusConflict},
		{core.CodeNotFound, http.StatusNotFound},
		{core.CodeConflict, http.StatusConflict},
		{core.CodeExec, http.StatusInternalServerError},
		{core.CodeInternal, http.StatusInternalServerError},
		{core.Code("weird"), http.StatusInternalServerError},
		{"", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			require.Equal(t, tc.want, statusForCode(tc.code))
		})
	}
}

func TestToAPIError(t *testing.T) {
	t.Run("validation error", func(t *testing.T) {
		err := core.ValidationError("bad %s", "input").WithHint("fix it").WithDetail("field", "name")
		ae, status := toAPIError(err)
		require.Equal(t, http.StatusBadRequest, status)
		require.Equal(t, core.CodeValidation, ae.Code)
		require.Equal(t, "bad input", ae.Message)
		require.Equal(t, "fix it", ae.Hint)
		require.Equal(t, "name", ae.Details["field"])
	})

	t.Run("safety error exposes operation and flags", func(t *testing.T) {
		err := core.RequireConfirmation("drop database orders", "orders", "wrong")
		require.NotNil(t, err)
		ae, status := toAPIError(err)
		require.Equal(t, http.StatusConflict, status)
		require.Equal(t, core.CodeSafety, ae.Code)
		require.Equal(t, "drop database orders", ae.Operation)
		require.Equal(t, []string{"confirm=orders"}, ae.RequiredFlags)
	})

	t.Run("ownership error exposes owner detail", func(t *testing.T) {
		err := core.NewOwnershipError(
			"s3://bucket/panel/x", "web-db-02", "10.0.0.5", "2026-06-21T00:00:00Z", true,
			"already owned by %s", "web-db-02")
		ae, status := toAPIError(err)
		require.Equal(t, http.StatusConflict, status)
		require.Equal(t, core.CodeOwnership, ae.Code)
		require.NotNil(t, ae.Owner)
		require.Equal(t, "web-db-02", ae.Owner.OwnerID)
		require.Equal(t, "10.0.0.5", ae.Owner.OwnerHost)
		require.True(t, ae.Owner.Adoptable)
	})

	t.Run("plain error normalizes to internal", func(t *testing.T) {
		ae, status := toAPIError(errors.New("boom"))
		require.Equal(t, http.StatusInternalServerError, status)
		require.Equal(t, core.CodeInternal, ae.Code)
	})

	t.Run("nil error", func(t *testing.T) {
		ae, status := toAPIError(nil)
		require.Equal(t, http.StatusInternalServerError, status)
		require.Equal(t, core.CodeInternal, ae.Code)
	})
}

func TestWriteErrorRendersJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, core.NotFoundError("missing thing"))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Header().Get("Content-Type"), "application/json")

	var got apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, core.CodeNotFound, got.Code)
	require.Equal(t, "missing thing", got.Message)
}

func TestWriteData(t *testing.T) {
	rec := httptest.NewRecorder()
	writeData(rec, http.StatusOK, map[string]any{"k": "v"})

	require.Equal(t, http.StatusOK, rec.Code)
	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	require.Equal(t, "v", env.Data["k"])
}

func TestDecodeJSON(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		body := bytes.NewBufferString(`{"password":"hunter2"}`)
		r := httptest.NewRequest(http.MethodPost, "/", body)
		var req loginRequest
		require.NoError(t, decodeJSON(r, &req, maxBodyBytes))
		require.Equal(t, "hunter2", req.Password)
	})

	t.Run("unknown field rejected", func(t *testing.T) {
		body := bytes.NewBufferString(`{"password":"x","extra":1}`)
		r := httptest.NewRequest(http.MethodPost, "/", body)
		var req loginRequest
		err := decodeJSON(r, &req, maxBodyBytes)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})

	t.Run("malformed json", func(t *testing.T) {
		body := bytes.NewBufferString(`{not json`)
		r := httptest.NewRequest(http.MethodPost, "/", body)
		var req loginRequest
		err := decodeJSON(r, &req, maxBodyBytes)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})

	t.Run("nil body", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Body = nil
		var req loginRequest
		err := decodeJSON(r, &req, maxBodyBytes)
		require.Error(t, err)
		require.Equal(t, core.CodeValidation, core.CodeOf(err))
	})

	t.Run("oversize body rejected", func(t *testing.T) {
		big := strings.Repeat("a", 100)
		body := bytes.NewBufferString(`{"password":"` + big + `"}`)
		r := httptest.NewRequest(http.MethodPost, "/", body)
		var req loginRequest
		err := decodeJSON(r, &req, 10) // tiny cap
		require.Error(t, err)
	})
}
