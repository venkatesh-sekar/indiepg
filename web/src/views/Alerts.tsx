// Alerts: notification channels (Pushover + webhook) each with a "send test"
// button, and a rules list with thresholds, severity and anti-spam controls.

import { useEffect, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { duration, dateTime } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import { Modal } from "@/components/Modal";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { useToast } from "@/components/Toast";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from "@/components/ui";
import type {
  AlertOp,
  AlertRule,
  AlertsConfig,
  ChannelConfig,
  ChannelKind,
  Severity,
} from "@/api/types";

const METRIC_LABELS: Record<string, string> = {
  cpu_percent: "CPU usage (%)",
  mem_percent: "Memory usage (%)",
  disk_percent: "Disk usage (%)",
  connections: "Active connections",
  cache_hit_ratio: "Cache hit ratio",
  replication_lag_seconds: "Replication lag (seconds)",
  deadlocks: "Deadlocks",
  last_backup_age_seconds: "Time since last backup (seconds)",
  pg_up: "Postgres up (1 = up)",
};

const METRIC_OPTIONS = Object.keys(METRIC_LABELS);

export function Alerts() {
  const toast = useToast();
  const cfg = useAsync<AlertsConfig>(() => api.alerts(), []);
  const [editing, setEditing] = useState<ChannelKind | null>(null);
  const [editRule, setEditRule] = useState<AlertRule | "new" | null>(null);
  const [deleteRule, setDeleteRule] = useState<AlertRule | null>(null);
  const [testBusy, setTestBusy] = useState<ChannelKind | null>(null);
  const [delBusy, setDelBusy] = useState(false);

  const channel = (kind: ChannelKind): ChannelConfig | undefined =>
    cfg.data?.channels.find((c) => c.kind === kind);

  const sendTest = async (kind: ChannelKind) => {
    setTestBusy(kind);
    try {
      const res = await api.testChannel({ kind });
      toast.success(res.message || "Test notification sent. Check your device.");
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Test failed.");
    } finally {
      setTestBusy(null);
    }
  };

  const doDeleteRule = async () => {
    if (!deleteRule) return;
    setDelBusy(true);
    try {
      await api.deleteRule(deleteRule.id);
      toast.success("Rule deleted.");
      setDeleteRule(null);
      cfg.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not delete rule.");
    } finally {
      setDelBusy(false);
    }
  };

  const toggleRule = async (rule: AlertRule) => {
    try {
      await api.saveRule({ ...rule, enabled: !rule.enabled });
      cfg.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not update rule.");
    }
  };

  return (
    <div className="view">
      <PageHeader
        title="Alerts"
        description="Get notified when something needs attention — and confirm it works with a test."
        actions={
          <button type="button" className="btn btn-primary" onClick={() => setEditRule("new")}>
            + Add rule
          </button>
        }
      />

      {cfg.loading ? (
        <Spinner label="Loading alerts…" />
      ) : cfg.error ? (
        <ErrorNotice error={cfg.error} />
      ) : cfg.data ? (
        <>
          {/* Channels */}
          <Card title="Where alerts go">
            <p className="muted">
              Set up at least one channel so you actually hear about problems. Use{" "}
              <strong>Send test</strong> to make sure it reaches your phone before you rely on it.
            </p>
            <div className="channel-grid">
              <ChannelCard
                title="Pushover"
                desc="Push notifications to your phone."
                config={channel("pushover")}
                onEdit={() => setEditing("pushover")}
                onTest={() => sendTest("pushover")}
                testing={testBusy === "pushover"}
              />
              <ChannelCard
                title="Webhook"
                desc="Post to Slack, Discord, n8n, or any URL."
                config={channel("webhook")}
                onEdit={() => setEditing("webhook")}
                onTest={() => sendTest("webhook")}
                testing={testBusy === "webhook"}
              />
            </div>
          </Card>

          {/* Rules */}
          <Card title="Alert rules">
            {cfg.data.rules.length === 0 ? (
              <EmptyState
                title="No rules yet"
                hint="Add a rule to be notified when a metric crosses a threshold."
              />
            ) : (
              <div className="table-scroll">
                <table className="data-table">
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Condition</th>
                      <th>Severity</th>
                      <th>Sustained</th>
                      <th>Cooldown</th>
                      <th>State</th>
                      <th className="col-actions">Actions</th>
                    </tr>
                  </thead>
                  <tbody>
                    {cfg.data.rules.map((rule) => (
                      <tr key={rule.id} className={rule.enabled ? "" : "row-disabled"}>
                        <td>
                          <strong>{rule.name}</strong>
                          {rule.last_fired_at ? (
                            <div className="muted small">last fired {dateTime(rule.last_fired_at)}</div>
                          ) : null}
                        </td>
                        <td className="mono small">
                          {METRIC_LABELS[rule.metric] ?? rule.metric} {rule.op} {rule.threshold}
                        </td>
                        <td><SeverityBadge severity={rule.severity} /></td>
                        <td>{rule.for_seconds > 0 ? duration(rule.for_seconds) : "instant"}</td>
                        <td>{duration(rule.cooldown_seconds)}</td>
                        <td><StateBadge state={rule.state} enabled={rule.enabled} /></td>
                        <td className="col-actions">
                          <button type="button" className="btn btn-sm" onClick={() => toggleRule(rule)}>
                            {rule.enabled ? "Disable" : "Enable"}
                          </button>
                          <button type="button" className="btn btn-sm" onClick={() => setEditRule(rule)}>
                            Edit
                          </button>
                          <button
                            type="button"
                            className="btn btn-sm btn-danger-ghost"
                            onClick={() => setDeleteRule(rule)}
                          >
                            Delete
                          </button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </Card>
        </>
      ) : null}

      {editing ? (
        <ChannelModal
          kind={editing}
          config={channel(editing)}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            cfg.reload();
          }}
        />
      ) : null}

      {editRule ? (
        <RuleModal
          rule={editRule === "new" ? null : editRule}
          onClose={() => setEditRule(null)}
          onSaved={() => {
            setEditRule(null);
            cfg.reload();
          }}
        />
      ) : null}

      <ConfirmDialog
        open={deleteRule !== null}
        title="Delete this alert rule?"
        message={`You'll no longer be notified when "${deleteRule?.name}" condition is met.`}
        confirmLabel="Delete rule"
        tone="danger"
        busy={delBusy}
        onConfirm={doDeleteRule}
        onCancel={() => setDeleteRule(null)}
      />
    </div>
  );
}

function ChannelCard({
  title,
  desc,
  config,
  onEdit,
  onTest,
  testing,
}: {
  title: string;
  desc: string;
  config?: ChannelConfig;
  onEdit: () => void;
  onTest: () => void;
  testing: boolean;
}) {
  const configured = Boolean(config?.enabled);
  return (
    <div className="channel-card">
      <div className="channel-head">
        <h4>{title}</h4>
        {configured ? <Badge tone="ok">Configured</Badge> : <Badge>Not set up</Badge>}
      </div>
      <p className="muted">{desc}</p>
      <div className="btn-row">
        <button type="button" className="btn btn-sm" onClick={onEdit}>
          {configured ? "Edit" : "Set up"}
        </button>
        <button
          type="button"
          className="btn btn-sm btn-primary"
          onClick={onTest}
          disabled={!configured || testing}
          title={configured ? "Send a test notification" : "Set up this channel first"}
        >
          {testing ? "Sending…" : "Send test"}
        </button>
      </div>
    </div>
  );
}

function ChannelModal({
  kind,
  config,
  onClose,
  onSaved,
}: {
  kind: ChannelKind;
  config?: ChannelConfig;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [enabled, setEnabled] = useState(config?.enabled ?? true);
  const [token, setToken] = useState(config?.pushover_token ?? "");
  const [user, setUser] = useState(config?.pushover_user ?? "");
  const [url, setUrl] = useState(config?.webhook_url ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const payload: ChannelConfig =
      kind === "pushover"
        ? { kind, enabled, pushover_token: token.trim(), pushover_user: user.trim() }
        : { kind, enabled, webhook_url: url.trim() };
    try {
      await api.saveChannel(payload);
      toast.success("Channel saved.");
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={kind === "pushover" ? "Pushover notifications" : "Webhook notifications"}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" form="channel-form" className="btn btn-primary" disabled={busy}>
            {busy ? "Saving…" : "Save channel"}
          </button>
        </>
      }
    >
      <form id="channel-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        {kind === "pushover" ? (
          <>
            <Callout tone="info">
              Find your <strong>user key</strong> on the Pushover dashboard and create an{" "}
              <strong>application token</strong> for pgpanel.
            </Callout>
            <label className="field">
              <span className="field-label">Application token</span>
              <input
                type="text"
                value={token}
                autoComplete="off"
                onChange={(e) => setToken(e.target.value)}
              />
            </label>
            <label className="field">
              <span className="field-label">User key</span>
              <input
                type="text"
                value={user}
                autoComplete="off"
                onChange={(e) => setUser(e.target.value)}
              />
            </label>
          </>
        ) : (
          <>
            <Callout tone="info">
              Paste any incoming-webhook URL — Slack, Discord, n8n, or your own endpoint. We send a
              small JSON payload describing the alert.
            </Callout>
            <label className="field">
              <span className="field-label">Webhook URL</span>
              <input
                type="url"
                value={url}
                placeholder="https://hooks.example.com/…"
                autoComplete="off"
                onChange={(e) => setUrl(e.target.value)}
              />
            </label>
          </>
        )}
        <label className="checkbox">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          <span>Enabled</span>
        </label>
      </form>
    </Modal>
  );
}

function RuleModal({
  rule,
  onClose,
  onSaved,
}: {
  rule: AlertRule | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState(rule?.name ?? "");
  const [metric, setMetric] = useState(rule?.metric ?? METRIC_OPTIONS[0]);
  const [op, setOp] = useState<AlertOp>(rule?.op ?? ">");
  const [threshold, setThreshold] = useState(String(rule?.threshold ?? 0));
  const [severity, setSeverity] = useState<Severity>(rule?.severity ?? "warning");
  const [forMin, setForMin] = useState(String((rule?.for_seconds ?? 0) / 60));
  const [cooldownMin, setCooldownMin] = useState(String((rule?.cooldown_seconds ?? 600) / 60));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  useEffect(() => {
    if (!name && rule == null) setName(METRIC_LABELS[metric] ?? metric);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [metric]);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const payload: AlertRule = {
      id: rule?.id ?? "",
      name: name.trim() || (METRIC_LABELS[metric] ?? metric),
      metric,
      op,
      threshold: Number(threshold) || 0,
      severity,
      for_seconds: Math.max(0, Math.round(Number(forMin) * 60)),
      cooldown_seconds: Math.max(0, Math.round(Number(cooldownMin) * 60)),
      enabled: rule?.enabled ?? true,
    };
    try {
      await api.saveRule(payload);
      toast.success("Rule saved.");
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={rule ? "Edit alert rule" : "Add alert rule"}
      width="md"
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="submit" form="rule-form" className="btn btn-primary" disabled={busy}>
            {busy ? "Saving…" : "Save rule"}
          </button>
        </>
      }
    >
      <form id="rule-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <label className="field">
          <span className="field-label">Rule name</span>
          <input type="text" value={name} onChange={(e) => setName(e.target.value)} placeholder="Disk almost full" />
        </label>

        <div className="field-row">
          <label className="field">
            <span className="field-label">When this metric</span>
            <select value={metric} onChange={(e) => setMetric(e.target.value)}>
              {METRIC_OPTIONS.map((m) => (
                <option key={m} value={m}>
                  {METRIC_LABELS[m]}
                </option>
              ))}
            </select>
          </label>
          <label className="field field-narrow">
            <span className="field-label">is</span>
            <select value={op} onChange={(e) => setOp(e.target.value as AlertOp)}>
              <option value=">">above</option>
              <option value=">=">at or above</option>
              <option value="<">below</option>
              <option value="<=">at or below</option>
            </select>
          </label>
          <label className="field field-narrow">
            <span className="field-label">value</span>
            <input
              type="number"
              step="any"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
            />
          </label>
        </div>

        <div className="field-row">
          <label className="field">
            <span className="field-label">Severity</span>
            <select value={severity} onChange={(e) => setSeverity(e.target.value as Severity)}>
              <option value="info">Info</option>
              <option value="warning">Warning</option>
              <option value="critical">Critical</option>
            </select>
          </label>
          <label className="field">
            <span className="field-label">Must hold for (minutes)</span>
            <input type="number" min="0" value={forMin} onChange={(e) => setForMin(e.target.value)} />
            <span className="field-help muted">Avoids alerting on brief spikes. 0 = alert immediately.</span>
          </label>
          <label className="field">
            <span className="field-label">Re-notify after (minutes)</span>
            <input type="number" min="0" value={cooldownMin} onChange={(e) => setCooldownMin(e.target.value)} />
            <span className="field-help muted">Quiet period before repeating the same alert.</span>
          </label>
        </div>
      </form>
    </Modal>
  );
}

function SeverityBadge({ severity }: { severity: Severity }) {
  if (severity === "critical") return <Badge tone="danger">Critical</Badge>;
  if (severity === "warning") return <Badge tone="warn">Warning</Badge>;
  return <Badge tone="info">Info</Badge>;
}

function StateBadge({ state, enabled }: { state?: AlertRule["state"]; enabled: boolean }) {
  if (!enabled) return <Badge>Disabled</Badge>;
  if (state === "firing") return <Badge tone="danger">● Firing</Badge>;
  if (state === "resolved") return <Badge tone="ok">Resolved</Badge>;
  return <Badge tone="ok">OK</Badge>;
}
