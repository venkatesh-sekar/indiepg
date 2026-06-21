package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/venkatesh-sekar/indiepg/internal/core"
)

// Notifier delivers an Event to a notification channel. Implementations must be
// safe to call concurrently and must honor ctx cancellation. SendTest delivers
// a synthetic "this is a test" notification so the operator can confirm a
// channel is wired correctly from the panel's "send test" button.
type Notifier interface {
	Name() string
	Send(ctx context.Context, ev Event) error
	SendTest(ctx context.Context) error
}

// httpDo is the minimal HTTP surface the notifiers need. *http.Client satisfies
// it; tests inject a fake to assert request shape without real network IO.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// defaultClient returns the provided client or a sane default with a timeout so
// a hung collector never blocks the eval/notify loop forever.
func defaultClient(client *http.Client) httpDoer {
	if client != nil {
		return client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// pushoverPriority maps a severity to a Pushover priority value.
//
//	info     -> -1 (low / no sound)
//	warning  ->  0 (normal)
//	critical ->  1 (high)
func pushoverPriority(sev Severity) int {
	switch sev {
	case SeverityInfo:
		return -1
	case SeverityCritical:
		return 1
	default:
		return 0
	}
}

const pushoverAPIURL = "https://api.pushover.net/1/messages.json"

// PushoverNotifier delivers events to Pushover (https://pushover.net) using an
// application token and a user/group key.
type PushoverNotifier struct {
	client httpDoer
	apiURL string
	token  string
	user   string
}

// NewPushover constructs a Pushover notifier. A nil client uses a default
// http.Client with a timeout.
func NewPushover(client *http.Client, token, user string) *PushoverNotifier {
	return &PushoverNotifier{
		client: defaultClient(client),
		apiURL: pushoverAPIURL,
		token:  token,
		user:   user,
	}
}

// Name identifies the channel.
func (n *PushoverNotifier) Name() string { return "pushover" }

// Send delivers an event to Pushover.
func (n *PushoverNotifier) Send(ctx context.Context, ev Event) error {
	title := fmt.Sprintf("[%s] %s", strings.ToUpper(string(ev.Rule.Severity)), ev.Rule.Name)
	if ev.State == StateResolved {
		title = fmt.Sprintf("[RESOLVED] %s", ev.Rule.Name)
	}
	return n.post(ctx, title, ev.Message, pushoverPriority(ev.Rule.Severity))
}

// SendTest delivers a synthetic test notification.
func (n *PushoverNotifier) SendTest(ctx context.Context) error {
	return n.post(ctx, "indiepg test alert",
		"This is a test notification from indiepg. If you can read this, Pushover is configured correctly.", 0)
}

func (n *PushoverNotifier) post(ctx context.Context, title, message string, priority int) error {
	if n.token == "" || n.user == "" {
		return core.ValidationError("pushover notifier missing token or user")
	}
	form := url.Values{}
	form.Set("token", n.token)
	form.Set("user", n.user)
	form.Set("title", title)
	form.Set("message", message)
	form.Set("priority", strconv.Itoa(priority))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.apiURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return core.InternalError("build pushover request").Wrap(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := n.client.Do(req)
	if err != nil {
		return core.ExecError("pushover request failed").Wrap(err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return core.ExecError("pushover returned status %d", resp.StatusCode).
			WithDetail("body", strings.TrimSpace(string(body)))
	}
	return nil
}

// WebhookNotifier POSTs a JSON payload to an arbitrary URL (Slack, Discord,
// n8n, or a custom endpoint). The payload is a stable JSON document describing
// the event.
type WebhookNotifier struct {
	client httpDoer
	url    string
}

// NewWebhook constructs a generic webhook notifier targeting url.
func NewWebhook(client *http.Client, url string) *WebhookNotifier {
	return &WebhookNotifier{
		client: defaultClient(client),
		url:    url,
	}
}

// Name identifies the channel.
func (n *WebhookNotifier) Name() string { return "webhook" }

// webhookPayload is the JSON body POSTed to the webhook URL.
type webhookPayload struct {
	Event     string    `json:"event"`          // "firing" | "resolved" | "test"
	Rule      string    `json:"rule,omitempty"` // rule id
	Name      string    `json:"name,omitempty"` // rule name
	Severity  string    `json:"severity,omitempty"`
	Metric    string    `json:"metric,omitempty"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold,omitempty"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// Send delivers an event to the webhook.
func (n *WebhookNotifier) Send(ctx context.Context, ev Event) error {
	event := "firing"
	if ev.State == StateResolved {
		event = "resolved"
	}
	payload := webhookPayload{
		Event:     event,
		Rule:      ev.Rule.ID,
		Name:      ev.Rule.Name,
		Severity:  string(ev.Rule.Severity),
		Metric:    ev.Rule.Metric,
		Value:     ev.Value,
		Threshold: ev.Rule.Threshold,
		Message:   ev.Message,
		Timestamp: ev.FiredAt.UTC(),
	}
	return n.post(ctx, payload)
}

// SendTest delivers a synthetic test payload.
func (n *WebhookNotifier) SendTest(ctx context.Context) error {
	payload := webhookPayload{
		Event:     "test",
		Message:   "This is a test notification from indiepg.",
		Timestamp: time.Now().UTC(),
	}
	return n.post(ctx, payload)
}

func (n *WebhookNotifier) post(ctx context.Context, payload webhookPayload) error {
	if n.url == "" {
		return core.ValidationError("webhook notifier missing url")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return core.InternalError("marshal webhook payload").Wrap(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(body))
	if err != nil {
		return core.ValidationError("invalid webhook url %q", n.url).Wrap(err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return core.ExecError("webhook request failed").Wrap(err)
	}
	defer drainClose(resp.Body)

	if resp.StatusCode/100 != 2 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return core.ExecError("webhook returned status %d", resp.StatusCode).
			WithDetail("body", strings.TrimSpace(string(rb)))
	}
	return nil
}

// drainClose drains and closes a response body so the underlying connection can
// be reused, ignoring errors.
func drainClose(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(rc, 4096))
	_ = rc.Close()
}
