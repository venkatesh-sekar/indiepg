package alert

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/venkatesh-sekar/pgpanel/internal/core"
)

// fakeDoer captures the request and returns a canned response/error.
type fakeDoer struct {
	lastReq  *http.Request
	lastBody string
	status   int
	respBody string
	err      error
	calls    int
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	f.lastReq = req
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.lastBody = string(b)
	}
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(f.respBody)),
		Header:     make(http.Header),
	}, nil
}

func sampleEvent() Event {
	return Event{
		Rule: Rule{
			ID:        "cpu-high",
			Name:      "CPU high",
			Metric:    MetricCPUPercent,
			Op:        OpGT,
			Threshold: 80,
			Severity:  SeverityCritical,
		},
		State:   StateFiring,
		Value:   95,
		FiredAt: time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC),
		Message: "CPU high: host.cpu_percent > 95 (threshold 80)",
	}
}

func TestPushoverSend(t *testing.T) {
	fd := &fakeDoer{}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL, token: "tok", user: "usr"}
	require.Equal(t, "pushover", n.Name())

	require.NoError(t, n.Send(context.Background(), sampleEvent()))
	require.Equal(t, 1, fd.calls)
	require.Equal(t, http.MethodPost, fd.lastReq.Method)
	require.Equal(t, "application/x-www-form-urlencoded", fd.lastReq.Header.Get("Content-Type"))

	form, err := url.ParseQuery(fd.lastBody)
	require.NoError(t, err)
	require.Equal(t, "tok", form.Get("token"))
	require.Equal(t, "usr", form.Get("user"))
	require.Contains(t, form.Get("title"), "CRITICAL")
	require.Contains(t, form.Get("title"), "CPU high")
	require.Equal(t, "1", form.Get("priority")) // critical -> high priority
	require.NotEmpty(t, form.Get("message"))
}

func TestPushoverResolvedTitle(t *testing.T) {
	fd := &fakeDoer{}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL, token: "tok", user: "usr"}
	ev := sampleEvent()
	ev.State = StateResolved
	require.NoError(t, n.Send(context.Background(), ev))
	form, _ := url.ParseQuery(fd.lastBody)
	require.Contains(t, form.Get("title"), "RESOLVED")
}

func TestPushoverSendTest(t *testing.T) {
	fd := &fakeDoer{}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL, token: "tok", user: "usr"}
	require.NoError(t, n.SendTest(context.Background()))
	form, _ := url.ParseQuery(fd.lastBody)
	require.Equal(t, "0", form.Get("priority"))
	require.NotEmpty(t, form.Get("message"))
}

func TestPushoverMissingCredentials(t *testing.T) {
	fd := &fakeDoer{}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL}
	err := n.SendTest(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Equal(t, 0, fd.calls, "must not hit the network without credentials")
}

func TestPushoverNon2xx(t *testing.T) {
	fd := &fakeDoer{status: http.StatusBadRequest, respBody: `{"errors":["bad token"]}`}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL, token: "tok", user: "usr"}
	err := n.Send(context.Background(), sampleEvent())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestPushoverTransportError(t *testing.T) {
	fd := &fakeDoer{err: errors.New("dial tcp: timeout")}
	n := &PushoverNotifier{client: fd, apiURL: pushoverAPIURL, token: "tok", user: "usr"}
	err := n.Send(context.Background(), sampleEvent())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestPushoverPriorityMapping(t *testing.T) {
	require.Equal(t, -1, pushoverPriority(SeverityInfo))
	require.Equal(t, 0, pushoverPriority(SeverityWarning))
	require.Equal(t, 1, pushoverPriority(SeverityCritical))
	require.Equal(t, 0, pushoverPriority(Severity("unknown")))
}

func TestWebhookSend(t *testing.T) {
	fd := &fakeDoer{}
	n := &WebhookNotifier{client: fd, url: "https://hooks.example.com/x"}
	require.Equal(t, "webhook", n.Name())

	require.NoError(t, n.Send(context.Background(), sampleEvent()))
	require.Equal(t, 1, fd.calls)
	require.Equal(t, http.MethodPost, fd.lastReq.Method)
	require.Equal(t, "application/json", fd.lastReq.Header.Get("Content-Type"))

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(fd.lastBody), &payload))
	require.Equal(t, "firing", payload.Event)
	require.Equal(t, "cpu-high", payload.Rule)
	require.Equal(t, "CPU high", payload.Name)
	require.Equal(t, string(SeverityCritical), payload.Severity)
	require.Equal(t, MetricCPUPercent, payload.Metric)
	require.Equal(t, 95.0, payload.Value)
	require.Equal(t, 80.0, payload.Threshold)
	require.NotEmpty(t, payload.Message)
}

func TestWebhookResolvedEvent(t *testing.T) {
	fd := &fakeDoer{}
	n := &WebhookNotifier{client: fd, url: "https://hooks.example.com/x"}
	ev := sampleEvent()
	ev.State = StateResolved
	require.NoError(t, n.Send(context.Background(), ev))

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(fd.lastBody), &payload))
	require.Equal(t, "resolved", payload.Event)
}

func TestWebhookSendTest(t *testing.T) {
	fd := &fakeDoer{}
	n := &WebhookNotifier{client: fd, url: "https://hooks.example.com/x"}
	require.NoError(t, n.SendTest(context.Background()))

	var payload webhookPayload
	require.NoError(t, json.Unmarshal([]byte(fd.lastBody), &payload))
	require.Equal(t, "test", payload.Event)
	require.NotEmpty(t, payload.Message)
}

func TestWebhookMissingURL(t *testing.T) {
	fd := &fakeDoer{}
	n := &WebhookNotifier{client: fd}
	err := n.SendTest(context.Background())
	require.Error(t, err)
	require.Equal(t, core.CodeValidation, core.CodeOf(err))
	require.Equal(t, 0, fd.calls)
}

func TestWebhookNon2xx(t *testing.T) {
	fd := &fakeDoer{status: http.StatusInternalServerError, respBody: "boom"}
	n := &WebhookNotifier{client: fd, url: "https://hooks.example.com/x"}
	err := n.Send(context.Background(), sampleEvent())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestWebhookTransportError(t *testing.T) {
	fd := &fakeDoer{err: errors.New("connection refused")}
	n := &WebhookNotifier{client: fd, url: "https://hooks.example.com/x"}
	err := n.Send(context.Background(), sampleEvent())
	require.Error(t, err)
	require.Equal(t, core.CodeExec, core.CodeOf(err))
}

func TestConstructorsUseDefaultClient(t *testing.T) {
	// The public constructors must not panic and must produce usable notifiers
	// even with a nil client (a default client with a timeout is substituted).
	p := NewPushover(nil, "tok", "usr")
	require.NotNil(t, p)
	require.Equal(t, "pushover", p.Name())

	w := NewWebhook(nil, "https://hooks.example.com/x")
	require.NotNil(t, w)
	require.Equal(t, "webhook", w.Name())
}

// compile-time assertions that both notifiers satisfy the Notifier interface.
var (
	_ Notifier = (*PushoverNotifier)(nil)
	_ Notifier = (*WebhookNotifier)(nil)
)
