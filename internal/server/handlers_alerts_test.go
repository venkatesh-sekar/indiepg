package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// writeAlertChannels persists the given channel list under the exact config key
// handleTestAlertChannel reads, mirroring how the SPA's save endpoint records
// them, so the tests drive the real load → select → dispatch path.
func writeAlertChannels(t *testing.T, st *store.Store, channels ...alertChannelConfig) {
	t.Helper()
	raw, err := json.Marshal(channels)
	require.NoError(t, err)
	require.NoError(t, st.SetConfig(context.Background(), alertChannelsConfigKey, string(raw)))
}

// TestTestAlertChannel_RejectsUnknownKind pins the input-validation gate
// (handlers_alerts.go:300): a request kind outside {pushover, webhook} is
// rejected with CodeValidation *before* any channel is loaded or any notifier
// built. This is the guard that keeps req.Kind ∈ {pushover,webhook}, which in
// turn keeps the credential switch (:327) from ever leaving `notifier` nil and
// panicking on SendTest. Drop or weaken the validation and this request instead
// falls through to a 404 (no channel of that kind) — a distinct code — so the
// mutation reds here.
func TestTestAlertChannel_RejectsUnknownKind(t *testing.T) {
	srv, _ := newTestServer(t)
	token := login(t, srv, testPassword)

	rec := authedRequest(t, srv, http.MethodPost, "/api/alerts/channels/test", token,
		map[string]any{"kind": "slack"})

	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeValidation, ae.Code)
	// The message must name both accepted kinds so the operator knows what to fix.
	require.Contains(t, ae.Message, "pushover")
	require.Contains(t, ae.Message, "webhook")
}

// TestTestAlertChannel_RequiresChannelOfExactRequestedKind pins the selection
// gate (handlers_alerts.go:311-323): the channel is chosen only when its Kind
// equals the requested kind — not "the first configured channel". A webhook
// channel is configured, but a *pushover* test is requested, so no matching
// channel exists and the handler must return CodeNotFound WITHOUT dispatching.
// If selection were loosened (== → !=, or "pick the first channel"), the webhook
// channel would be selected and its notifier would attempt a POST to the
// unreachable URL, yielding a 500 ExecError instead of the 404 asserted here.
func TestTestAlertChannel_RequiresChannelOfExactRequestedKind(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	// A real, valid channel exists — but of a DIFFERENT kind than requested. The
	// URL is deliberately unreachable so a wrongly-selected webhook fails loudly
	// (ExecError) rather than silently succeeding.
	writeAlertChannels(t, st, alertChannelConfig{
		Kind:       "webhook",
		Enabled:    true,
		WebhookURL: "http://127.0.0.1:1/should-never-be-called",
	})

	rec := authedRequest(t, srv, http.MethodPost, "/api/alerts/channels/test", token,
		map[string]any{"kind": "pushover"})

	require.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
	var ae apiError
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &ae))
	require.Equal(t, core.CodeNotFound, ae.Code)
	require.Contains(t, ae.Message, "pushover")
}

// TestTestAlertChannel_DispatchesToMatchingChannel pins the happy path: when a
// channel of the requested kind is configured, it IS selected and its notifier's
// SendTest IS invoked. A local webhook endpoint records the delivery; the
// handler must POST exactly one synthetic "test" event and return 200. This is
// the positive complement to the selection test — together they prove exact-kind
// matching from both sides — and it also catches a mutation that stops actually
// dispatching (hit count would drop to zero).
func TestTestAlertChannel_DispatchesToMatchingChannel(t *testing.T) {
	srv, st := newTestServer(t)
	token := login(t, srv, testPassword)

	var hits atomic.Int32
	var gotEvent atomic.Value // string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		var p struct {
			Event string `json:"event"`
		}
		_ = json.Unmarshal(body, &p)
		gotEvent.Store(p.Event)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	writeAlertChannels(t, st, alertChannelConfig{
		Kind:       "webhook",
		Enabled:    true,
		WebhookURL: ts.URL,
	})

	rec := authedRequest(t, srv, http.MethodPost, "/api/alerts/channels/test", token,
		map[string]any{"kind": "webhook"})

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, int32(1), hits.Load(), "the matching channel's notifier must be invoked exactly once")
	require.Equal(t, "test", gotEvent.Load(), "SendTest must POST a synthetic test event")
}
