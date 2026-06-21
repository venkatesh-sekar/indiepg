package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/venkatesh-sekar/indiepg/internal/alert"
	"github.com/venkatesh-sekar/indiepg/internal/core"
	"github.com/venkatesh-sekar/indiepg/internal/store"
)

// alertChannelsConfigKey is the fixed config KV key under which the list of
// notification channels is persisted as a JSON-encoded []alertChannelConfig.
// There is no typed channel model in the store, so the handler owns the schema.
const alertChannelsConfigKey = "alerts.channels"

// alertTestClientTimeout bounds the synthetic test-notification request so a
// hung collector cannot block the handler indefinitely.
const alertTestClientTimeout = 10 * time.Second

// alertChannelConfig is one notification channel as stored and exposed to the
// SPA. Tokens are kept as-is: this is single-admin local state. The kind
// discriminates which credential fields are meaningful.
type alertChannelConfig struct {
	Kind          string `json:"kind"` // "pushover" | "webhook"
	Enabled       bool   `json:"enabled"`
	PushoverToken string `json:"pushover_token,omitempty"`
	PushoverUser  string `json:"pushover_user,omitempty"`
	WebhookURL    string `json:"webhook_url,omitempty"`
}

// alertRuleResponse is the wire shape for a single rule. Durations are exposed
// in whole seconds (matching the stored definition), and the evaluation state
// columns from the persisted record are surfaced read-only for the UI.
type alertRuleResponse struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Metric          string  `json:"metric"`
	Op              string  `json:"op"`
	Threshold       float64 `json:"threshold"`
	Severity        string  `json:"severity"`
	ForSeconds      int64   `json:"for_seconds"`
	CooldownSeconds int64   `json:"cooldown_seconds"`
	Enabled         bool    `json:"enabled"`
	State           string  `json:"state,omitempty"`
	LastFiredAt     *string `json:"last_fired_at,omitempty"`
	LastEvalAt      *string `json:"last_eval_at,omitempty"`
}

// alertsConfigResponse is the aggregate payload for GET /alerts.
type alertsConfigResponse struct {
	Channels []alertChannelConfig `json:"channels"`
	Rules    []alertRuleResponse  `json:"rules"`
}

// alertRuleRequest is the inbound shape for PUT /alerts/rules. Durations arrive
// as whole seconds, which the handler converts to the time.Duration fields on
// alert.Rule.
type alertRuleRequest struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Metric          string  `json:"metric"`
	Op              string  `json:"op"`
	Threshold       float64 `json:"threshold"`
	Severity        string  `json:"severity"`
	ForSeconds      int64   `json:"for_seconds"`
	CooldownSeconds int64   `json:"cooldown_seconds"`
	Enabled         bool    `json:"enabled"`
}

// alertTestChannelRequest is the inbound shape for POST /alerts/channels/test.
type alertTestChannelRequest struct {
	Kind string `json:"kind"` // "pushover" | "webhook"
}

// handleGetAlerts returns the full alerting configuration: the configured
// notification channels and every persisted rule with its evaluation state.
// Read-only, so it does not audit.
func (s *Server) handleGetAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	channels, err := s.loadAlertChannels(r)
	if err != nil {
		writeError(w, err)
		return
	}

	records, err := s.store.ListAlerts(ctx)
	if err != nil {
		writeError(w, err)
		return
	}

	rules := make([]alertRuleResponse, 0, len(records))
	for _, rec := range records {
		rule, err := alert.RuleFromRecord(rec)
		if err != nil {
			writeError(w, err)
			return
		}
		rules = append(rules, alertRuleToResponse(rule, rec))
	}

	writeData(w, http.StatusOK, alertsConfigResponse{
		Channels: maskAlertChannels(channels),
		Rules:    rules,
	})
}

// maskAlertChannels strips credential values (Pushover token, webhook URL) from
// the channel list before it is sent to the client. The fields are write-only:
// the panel keeps them in the store but never echoes them back, so a saved
// secret cannot leak via GET /alerts. Enabled + kind + pushover_user remain so
// the UI can still show that a channel is configured.
func maskAlertChannels(in []alertChannelConfig) []alertChannelConfig {
	out := make([]alertChannelConfig, len(in))
	for i, c := range in {
		c.PushoverToken = ""
		c.WebhookURL = ""
		out[i] = c
	}
	return out
}

// handleSaveAlertRule upserts a single alert rule. The seconds-based durations
// from the request are mapped onto the rule's time.Duration fields, validated,
// serialized to a record, and persisted.
func (s *Server) handleSaveAlertRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req alertRuleRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	rule := alert.Rule{
		ID:        req.ID,
		Name:      req.Name,
		Metric:    req.Metric,
		Op:        alert.Op(req.Op),
		Threshold: req.Threshold,
		Severity:  alert.Severity(req.Severity),
		For:       time.Duration(req.ForSeconds) * time.Second,
		Cooldown:  time.Duration(req.CooldownSeconds) * time.Second,
		Enabled:   req.Enabled,
	}

	if err := rule.Validate(); err != nil {
		writeError(w, err)
		return
	}

	rec, err := rule.ToRecord()
	if err != nil {
		writeError(w, err)
		return
	}

	if err := s.store.UpsertAlert(ctx, rec); err != nil {
		s.audit(ctx, "save_alert_rule", rule.ID, "failure", "save alert rule "+rule.Name, core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "save_alert_rule", rule.ID, "success", "saved alert rule "+rule.Name, "")
	writeData(w, http.StatusOK, core.Ok("alert rule saved"))
}

// handleDeleteAlertRule removes an alert rule by id. Deleting a missing rule is
// not an error (the store treats it idempotently).
func (s *Server) handleDeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, core.ValidationError("alert rule id is required"))
		return
	}

	if err := s.store.DeleteAlert(ctx, id); err != nil {
		s.audit(ctx, "delete_alert_rule", id, "failure", "delete alert rule "+id, core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "delete_alert_rule", id, "success", "deleted alert rule "+id, "")
	writeData(w, http.StatusOK, core.Ok("alert rule deleted"))
}

// handleSaveAlertChannel upserts one notification channel into the persisted
// channel list, keyed by kind. Existing channels of the same kind are replaced.
func (s *Server) handleSaveAlertChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req alertChannelConfig
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	if req.Kind != "pushover" && req.Kind != "webhook" {
		writeError(w, core.ValidationError("alert channel kind must be \"pushover\" or \"webhook\""))
		return
	}

	channels, err := s.loadAlertChannels(r)
	if err != nil {
		writeError(w, err)
		return
	}

	replaced := false
	for i := range channels {
		if channels[i].Kind == req.Kind {
			channels[i] = req
			replaced = true
			break
		}
	}
	if !replaced {
		channels = append(channels, req)
	}

	raw, err := json.Marshal(channels)
	if err != nil {
		writeError(w, core.InternalError("marshal alert channels").Wrap(err))
		return
	}

	if err := s.store.SetConfig(ctx, alertChannelsConfigKey, string(raw)); err != nil {
		s.audit(ctx, "save_alert_channel", req.Kind, "failure", "save alert channel "+req.Kind, core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "save_alert_channel", req.Kind, "success", "saved alert channel "+req.Kind, "")
	writeData(w, http.StatusOK, core.Ok("alert channel saved"))
}

// handleTestAlertChannel sends a synthetic test notification through the saved
// channel of the requested kind, so the operator can confirm it is wired up.
func (s *Server) handleTestAlertChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req alertTestChannelRequest
	if err := decodeJSON(r, &req, maxBodyBytes); err != nil {
		writeError(w, err)
		return
	}

	if req.Kind != "pushover" && req.Kind != "webhook" {
		writeError(w, core.ValidationError("alert channel kind must be \"pushover\" or \"webhook\""))
		return
	}

	channels, err := s.loadAlertChannels(r)
	if err != nil {
		writeError(w, err)
		return
	}

	var ch *alertChannelConfig
	for i := range channels {
		if channels[i].Kind == req.Kind {
			ch = &channels[i]
			break
		}
	}
	if ch == nil {
		err := core.NotFoundError("no %q alert channel is configured", req.Kind)
		s.audit(ctx, "test_alert_channel", req.Kind, "failure", "test alert channel "+req.Kind, core.CodeOf(err))
		writeError(w, err)
		return
	}

	client := &http.Client{Timeout: alertTestClientTimeout}
	var notifier alert.Notifier
	switch ch.Kind {
	case "pushover":
		notifier = alert.NewPushover(client, ch.PushoverToken, ch.PushoverUser)
	case "webhook":
		notifier = alert.NewWebhook(client, ch.WebhookURL)
	}

	if err := notifier.SendTest(ctx); err != nil {
		s.audit(ctx, "test_alert_channel", req.Kind, "failure", "test alert channel "+req.Kind, core.CodeOf(err))
		writeError(w, err)
		return
	}

	s.audit(ctx, "test_alert_channel", req.Kind, "success", "tested alert channel "+req.Kind, "")
	writeData(w, http.StatusOK, core.Ok("test notification sent"))
}

// loadAlertChannels reads and decodes the persisted channel list. A missing
// config key is normal (no channels configured yet) and yields a non-nil empty
// slice so the JSON response is [] rather than null.
func (s *Server) loadAlertChannels(r *http.Request) ([]alertChannelConfig, error) {
	raw, err := s.store.GetConfig(r.Context(), alertChannelsConfigKey)
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return []alertChannelConfig{}, nil
		}
		return nil, err
	}
	var channels []alertChannelConfig
	if err := json.Unmarshal([]byte(raw), &channels); err != nil {
		return nil, core.InternalError("decode stored alert channels").Wrap(err)
	}
	if channels == nil {
		channels = []alertChannelConfig{}
	}
	return channels, nil
}

// alertRuleToResponse projects a rule plus its persisted record into the wire
// shape, surfacing the read-only evaluation state columns when present.
func alertRuleToResponse(rule alert.Rule, rec store.AlertRecord) alertRuleResponse {
	resp := alertRuleResponse{
		ID:              rule.ID,
		Name:            rule.Name,
		Metric:          rule.Metric,
		Op:              string(rule.Op),
		Threshold:       rule.Threshold,
		Severity:        string(rule.Severity),
		ForSeconds:      int64(rule.For / time.Second),
		CooldownSeconds: int64(rule.Cooldown / time.Second),
		Enabled:         rule.Enabled,
		State:           rec.State,
	}
	if rec.LastFiredAt != nil {
		s := rec.LastFiredAt.UTC().Format(time.RFC3339)
		resp.LastFiredAt = &s
	}
	if rec.LastEvalAt != nil {
		s := rec.LastEvalAt.UTC().Format(time.RFC3339)
		resp.LastEvalAt = &s
	}
	return resp
}
