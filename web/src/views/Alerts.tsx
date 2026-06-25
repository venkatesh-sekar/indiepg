// Alerts: notification channels (Pushover + webhook) each with a "send test"
// button, and a rules list with thresholds, severity and anti-spam controls.

import { useEffect, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { duration, dateTime } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Modal } from "@/components/Modal";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { toast } from "sonner";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Switch } from "@/components/ui/switch";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import {
  Card as ChannelCardShell,
  CardAction,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Field,
  FieldDescription,
  FieldLabel,
} from "@/components/ui/field";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type {
  AlertOp,
  AlertRule,
  AlertsConfig,
  ChannelConfig,
  ChannelKind,
  Severity,
} from "@/api/types";

// Keys MUST match the engine's metric keys exactly (internal/alert/metrics.go).
// A rule whose metric the engine doesn't recognize is silently skipped and never
// fires, so a mismatch here is a dead alert — the save API now rejects unknown
// metrics to catch any drift loudly.
const METRIC_LABELS: Record<string, string> = {
  "host.cpu_percent": "CPU usage (%)",
  "host.mem_percent": "Memory usage (%)",
  "host.disk_percent": "Disk usage (%)",
  "host.load1": "Load average (1m)",
  "pg.up": "Postgres up (1 = up)",
  "pg.connections": "Active connections",
  "pg.connections_percent": "Connections (% of max)",
  "pg.cache_hit_ratio": "Cache hit ratio",
  "pg.replication_lag_seconds": "Replication lag (seconds)",
  "pg.deadlocks": "Deadlocks",
  "backup.last_age_seconds": "Time since last backup (seconds)",
  "backup.last_failed": "Most recent backup failed (1 = failed)",
};

const METRIC_OPTIONS = Object.keys(METRIC_LABELS);

export function Alerts() {
  const cfg = useAsync<AlertsConfig>(() => api.alerts(), []);
  const [editing, setEditing] = useState<ChannelKind | null>(null);
  const [editRule, setEditRule] = useState<AlertRule | "new" | null>(null);
  const [deleteRule, setDeleteRule] = useState<AlertRule | null>(null);
  const [testBusy, setTestBusy] = useState<ChannelKind | null>(null);
  const [delBusy, setDelBusy] = useState(false);

  const channel = (kind: ChannelKind): ChannelConfig | undefined =>
    cfg.data?.channels.find((c) => c.kind === kind);

  // Silent-failure guard: an enabled rule with no enabled channel will never
  // actually notify anyone. Warn the user so they don't trust a dead pipeline.
  const noChannelEnabled = !cfg.data?.channels.some((c) => c.enabled);
  const hasEnabledRule = Boolean(cfg.data?.rules.some((r) => r.enabled));
  const rulesWontFire = hasEnabledRule && noChannelEnabled;

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
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Alerts"
        description="Get notified when something needs attention — and confirm it works with a test."
        actions={<Button onClick={() => setEditRule("new")}>+ Add rule</Button>}
      />

      {cfg.loading ? (
        <Spinner label="Loading alerts…" />
      ) : cfg.error ? (
        <ErrorNotice error={cfg.error} />
      ) : cfg.data ? (
        <>
          {/* Channels */}
          <Card title="Where alerts go">
            <p className="text-muted-foreground">
              Set up at least one channel so you actually hear about problems. Use{" "}
              <strong>Send test</strong> to make sure it reaches your phone before you rely on it.
            </p>
            <div className="mt-3 grid gap-4 sm:grid-cols-2">
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

          {rulesWontFire ? (
            <Callout tone="warn" title="Your rules won't fire">
              No notification channel is enabled, so these alert rules can't reach you.
              Set up and enable Pushover or a Webhook above first.
            </Callout>
          ) : null}

          {/* Rules */}
          <Card title="Alert rules">
            {cfg.data.rules.length === 0 ? (
              <EmptyState
                title="No rules yet"
                hint="Add a rule to be notified when a metric crosses a threshold."
              />
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Name</TableHead>
                    <TableHead>Condition</TableHead>
                    <TableHead>Severity</TableHead>
                    <TableHead>Hold for</TableHead>
                    <TableHead>Cooldown</TableHead>
                    <TableHead>State</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {cfg.data.rules.map((rule) => (
                    <TableRow key={rule.id} className={cn(!rule.enabled && "opacity-60")}>
                      <TableCell>
                        <span className="font-medium">{rule.name}</span>
                        {rule.last_fired_at ? (
                          <div className="text-xs text-muted-foreground">
                            last fired {dateTime(rule.last_fired_at)}
                          </div>
                        ) : null}
                      </TableCell>
                      <TableCell className="font-mono text-sm">
                        {METRIC_LABELS[rule.metric] ?? rule.metric} {rule.op} {rule.threshold}
                      </TableCell>
                      <TableCell>
                        <SeverityBadge severity={rule.severity} />
                      </TableCell>
                      <TableCell>
                        {rule.for_seconds > 0 ? duration(rule.for_seconds) : "instant"}
                      </TableCell>
                      <TableCell>{duration(rule.cooldown_seconds)}</TableCell>
                      <TableCell>
                        <StateBadge state={rule.state} enabled={rule.enabled} />
                      </TableCell>
                      <TableCell className="text-right">
                        <div className="flex items-center justify-end gap-2">
                          <Switch
                            checked={rule.enabled}
                            onCheckedChange={() => toggleRule(rule)}
                            aria-label={
                              rule.enabled ? `Disable ${rule.name}` : `Enable ${rule.name}`
                            }
                          />
                          <Button variant="outline" size="sm" onClick={() => setEditRule(rule)}>
                            Edit
                          </Button>
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-destructive"
                            onClick={() => setDeleteRule(rule)}
                          >
                            Delete
                          </Button>
                        </div>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
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
    <ChannelCardShell>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
        <CardDescription>{desc}</CardDescription>
        <CardAction>
          {configured ? <Badge tone="ok">Configured</Badge> : <Badge>Not set up</Badge>}
        </CardAction>
      </CardHeader>
      <CardFooter className="gap-2">
        <Button variant="outline" size="sm" onClick={onEdit}>
          {configured ? "Edit" : "Set up"}
        </Button>
        <Button
          size="sm"
          onClick={onTest}
          disabled={!configured || testing}
          title={configured ? "Send a test notification" : "Set up this channel first"}
        >
          {testing ? (
            <>
              <InlineSpinner data-icon="inline-start" />
              Sending…
            </>
          ) : (
            "Send test"
          )}
        </Button>
      </CardFooter>
    </ChannelCardShell>
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
  const [enabled, setEnabled] = useState(config?.enabled ?? true);
  const [token, setToken] = useState(config?.pushover_token ?? "");
  const [user, setUser] = useState(config?.pushover_user ?? "");
  const [url, setUrl] = useState(config?.webhook_url ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  // A brand-new channel with no credentials can be saved enabled, after which it
  // shows a green "Configured" badge but can never actually notify — a silent
  // dead pipeline on the one feature meant to warn you. Block creating such a
  // channel while it's enabled. Scoped to new channels only (`!config`): on an
  // existing channel the secret fields come back blank (write-only/masked), so a
  // blank token there means "keep the stored one", not "no credentials".
  const requiredMissing =
    kind === "pushover" ? !token.trim() || !user.trim() : !url.trim();
  const blockEmptyNew = !config && enabled && requiredMissing;

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
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="channel-form"
            disabled={busy || blockEmptyNew}
            title={
              blockEmptyNew
                ? kind === "pushover"
                  ? "Enter the application token and user key first"
                  : "Enter the webhook URL first"
                : undefined
            }
          >
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Saving…
              </>
            ) : (
              "Save channel"
            )}
          </Button>
        </>
      }
    >
      <form id="channel-form" onSubmit={submit} className="flex flex-col gap-5">
        {error ? <ErrorNotice error={error} /> : null}
        {kind === "pushover" ? (
          <>
            <Callout tone="info">
              Find your <strong>user key</strong> on the Pushover dashboard and create an{" "}
              <strong>application token</strong> for indiepg.
            </Callout>
            <Field>
              <FieldLabel htmlFor="channel-token">Application token</FieldLabel>
              <Input
                id="channel-token"
                type="text"
                value={token}
                autoComplete="off"
                onChange={(e) => setToken(e.target.value)}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="channel-user">User key</FieldLabel>
              <Input
                id="channel-user"
                type="text"
                value={user}
                autoComplete="off"
                onChange={(e) => setUser(e.target.value)}
              />
            </Field>
          </>
        ) : (
          <>
            <Callout tone="info">
              Paste any incoming-webhook URL — Slack, Discord, n8n, or your own endpoint. We send a
              small JSON payload describing the alert.
            </Callout>
            <Field>
              <FieldLabel htmlFor="channel-url">Webhook URL</FieldLabel>
              <Input
                id="channel-url"
                type="url"
                value={url}
                placeholder="https://hooks.example.com/…"
                autoComplete="off"
                onChange={(e) => setUrl(e.target.value)}
              />
            </Field>
          </>
        )}
        <Field orientation="horizontal">
          <Checkbox
            id="channel-enabled"
            checked={enabled}
            onCheckedChange={(c) => setEnabled(c === true)}
          />
          <FieldLabel htmlFor="channel-enabled" className="font-normal">
            Enabled
          </FieldLabel>
        </Field>
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
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="rule-form" disabled={busy}>
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Saving…
              </>
            ) : (
              "Save rule"
            )}
          </Button>
        </>
      }
    >
      <form id="rule-form" onSubmit={submit} className="flex flex-col gap-5">
        {error ? <ErrorNotice error={error} /> : null}
        <Field>
          <FieldLabel htmlFor="rule-name">Rule name</FieldLabel>
          <Input
            id="rule-name"
            type="text"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Disk almost full"
          />
        </Field>

        <div className="grid gap-4 sm:grid-cols-[2fr_1fr_1fr]">
          <Field>
            <FieldLabel htmlFor="rule-metric" id="rule-metric-label">
              When this metric
            </FieldLabel>
            <Select value={metric} onValueChange={setMetric}>
              <SelectTrigger id="rule-metric" aria-labelledby="rule-metric-label" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {METRIC_OPTIONS.map((m) => (
                  <SelectItem key={m} value={m}>
                    {METRIC_LABELS[m]}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel htmlFor="rule-op" id="rule-op-label">
              is
            </FieldLabel>
            <Select value={op} onValueChange={(v) => setOp(v as AlertOp)}>
              <SelectTrigger id="rule-op" aria-labelledby="rule-op-label" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value=">">above</SelectItem>
                <SelectItem value=">=">at or above</SelectItem>
                <SelectItem value="<">below</SelectItem>
                <SelectItem value="<=">at or below</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel htmlFor="rule-threshold">value</FieldLabel>
            <Input
              id="rule-threshold"
              type="number"
              step="any"
              value={threshold}
              onChange={(e) => setThreshold(e.target.value)}
            />
          </Field>
        </div>

        <div className="grid gap-4 sm:grid-cols-3">
          <Field>
            <FieldLabel htmlFor="rule-severity" id="rule-severity-label">
              Severity
            </FieldLabel>
            <Select value={severity} onValueChange={(v) => setSeverity(v as Severity)}>
              <SelectTrigger id="rule-severity" aria-labelledby="rule-severity-label" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="info">Info</SelectItem>
                <SelectItem value="warning">Warning</SelectItem>
                <SelectItem value="critical">Critical</SelectItem>
              </SelectContent>
            </Select>
          </Field>
          <Field>
            <FieldLabel htmlFor="rule-for">Must hold for (minutes)</FieldLabel>
            <Input
              id="rule-for"
              type="number"
              min="0"
              value={forMin}
              aria-describedby="rule-for-help"
              onChange={(e) => setForMin(e.target.value)}
            />
            <FieldDescription id="rule-for-help">
              Avoids alerting on brief spikes. 0 = alert immediately.
            </FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor="rule-cooldown">Re-notify after (minutes)</FieldLabel>
            <Input
              id="rule-cooldown"
              type="number"
              min="0"
              value={cooldownMin}
              aria-describedby="rule-cooldown-help"
              onChange={(e) => setCooldownMin(e.target.value)}
            />
            <FieldDescription id="rule-cooldown-help">
              Quiet period before repeating the same alert.
            </FieldDescription>
          </Field>
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
