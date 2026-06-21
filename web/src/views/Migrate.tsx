// Migrate: three modes — single database, whole cluster, and the SSH-less
// session wizard with a 6-char code and live progress. Every overwrite is
// confirmed; finished sessions show a "verified: N rows matched" badge.

import { useMemo, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { count, dateTime } from "@/lib/format";
import { usePolling } from "@/lib/hooks";
import { Modal } from "@/components/Modal";
import { useToast } from "@/components/Toast";
import {
  Badge,
  Callout,
  Card,
  ErrorNotice,
  PageHeader,
  Spinner,
} from "@/components/ui";
import type {
  MigrationMode,
  MigrationSession,
  MigrationStatus,
} from "@/api/types";

const DEFAULT_TTL_MIN = 60;

export function Migrate() {
  const [mode, setMode] = useState<MigrationMode>("ssh-less");

  return (
    <div className="view">
      <PageHeader
        title="Migrate"
        description="Move a database onto this server from another host — safely, with verification."
      />

      <Callout tone="info" title="Always verified">
        Before any overwrite, indiepg takes a <strong>safety backup</strong>, checks the transferred
        data with a <strong>checksum</strong>, and compares <strong>row counts</strong> between the
        old and new server. You only get a green light when the numbers match.
      </Callout>

      <div className="mode-tabs" role="tablist">
        <ModeTab id="ssh-less" active={mode} onSelect={setMode} label="Guided session" hint="No SSH needed" />
        <ModeTab id="single-db" active={mode} onSelect={setMode} label="One database" hint="Pull a single DB" />
        <ModeTab id="cluster" active={mode} onSelect={setMode} label="Whole cluster" hint="All DBs + roles" />
      </div>

      {mode === "ssh-less" ? <SessionWizard /> : null}
      {mode === "single-db" ? <SingleDBForm /> : null}
      {mode === "cluster" ? <ClusterForm /> : null}
    </div>
  );
}

function ModeTab({
  id,
  active,
  onSelect,
  label,
  hint,
}: {
  id: MigrationMode;
  active: MigrationMode;
  onSelect: (m: MigrationMode) => void;
  label: string;
  hint: string;
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active === id}
      className={`mode-tab ${active === id ? "active" : ""}`}
      onClick={() => onSelect(id)}
    >
      <span className="mode-tab-label">{label}</span>
      <span className="mode-tab-hint muted">{hint}</span>
    </button>
  );
}

// --- Mode 1: SSH-less guided session ---------------------------------------

const PROGRESS_STEPS: MigrationStatus[] = [
  "waiting_for_export",
  "exporting",
  "exported",
  "importing",
  "completed",
];

const STATUS_LABELS: Record<MigrationStatus, string> = {
  waiting_for_export: "Waiting for the other server to start…",
  exporting: "The other server is exporting the database…",
  exported: "Export finished — preparing to import…",
  importing: "Importing onto this server…",
  completed: "Done",
  failed: "Failed",
  expired: "Session expired",
};

function SessionWizard() {
  const toast = useToast();
  const [database, setDatabase] = useState("");
  const [ttlMin, setTtlMin] = useState(String(DEFAULT_TTL_MIN));
  const [code, setCode] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      const session = await api.createSession({
        database: database.trim(),
        ttl_seconds: Math.max(60, Math.round(Number(ttlMin) * 60)),
      });
      setCode(session.code);
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setCreating(false);
    }
  };

  if (code) {
    return <SessionProgress code={code} onReset={() => setCode(null)} onToast={toast} />;
  }

  return (
    <Card title="Start a guided migration">
      <p className="muted">
        This server will generate a short code. On the <strong>source</strong> server&apos;s panel,
        enter that code to begin. The two servers coordinate through your cloud bucket — no SSH, no
        firewall changes. You&apos;ll watch live progress here.
      </p>
      <form onSubmit={create} className="inline-form">
        {error ? <ErrorNotice error={error} /> : null}
        <label className="field">
          <span className="field-label">Database to receive</span>
          <input
            type="text"
            value={database}
            placeholder="myapp"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setDatabase(e.target.value)}
          />
          <span className="field-help muted">The name this database will have on this server.</span>
        </label>
        <label className="field field-narrow">
          <span className="field-label">Code valid for (minutes)</span>
          <input type="number" min="1" value={ttlMin} onChange={(e) => setTtlMin(e.target.value)} />
        </label>
        <button type="submit" className="btn btn-primary" disabled={creating || !database.trim()}>
          {creating ? "Creating…" : "Generate code"}
        </button>
      </form>
    </Card>
  );
}

function SessionProgress({
  code,
  onReset,
  onToast,
}: {
  code: string;
  onReset: () => void;
  onToast: ReturnType<typeof useToast>;
}) {
  const { data: session, error } = usePolling<MigrationSession>(
    (signal) => api.getSession(code, signal),
    2500,
  );
  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelBusy, setCancelBusy] = useState(false);

  const status = session?.status ?? "waiting_for_export";
  const currentStep = Math.max(0, PROGRESS_STEPS.indexOf(status));
  const terminal = status === "completed" || status === "failed" || status === "expired";

  const verification = useMemo(() => {
    if (!session?.source_row_counts || !session?.target_row_counts) return null;
    const src = session.source_row_counts;
    const tgt = session.target_row_counts;
    let total = 0;
    const diffs: Array<{ table: string; source: number; target: number }> = [];
    for (const table of Object.keys(src)) {
      const s = src[table] ?? 0;
      const t = tgt[table] ?? 0;
      total += t;
      if (s !== t) diffs.push({ table, source: s, target: t });
    }
    return { verified: diffs.length === 0, total, diffs };
  }, [session]);

  const cancel = async () => {
    setCancelBusy(true);
    try {
      await api.cancelSession(code);
      onToast.info("Session cancelled.");
      onReset();
    } catch (err) {
      onToast.error(err instanceof ApiError ? err.message : "Could not cancel.");
    } finally {
      setCancelBusy(false);
      setCancelOpen(false);
    }
  };

  return (
    <Card
      title="Migration in progress"
      actions={
        terminal ? (
          <button type="button" className="btn btn-sm" onClick={onReset}>
            Start another
          </button>
        ) : (
          <button type="button" className="btn btn-sm btn-danger-ghost" onClick={() => setCancelOpen(true)}>
            Cancel
          </button>
        )
      }
    >
      {error && !session ? <ErrorNotice error={error} /> : null}

      <div className="session-code-block">
        <span className="muted">Share this code on the source server:</span>
        <div className="session-code">{code}</div>
        {session ? (
          <span className="muted small">
            Expires {dateTime(session.expires_at)}
            {session.database ? ` · database: ${session.database}` : ""}
          </span>
        ) : null}
      </div>

      {!session ? (
        <Spinner label="Connecting…" />
      ) : (
        <>
          <ol className="progress-steps">
            {PROGRESS_STEPS.map((step, i) => {
              const state =
                status === "failed" || status === "expired"
                  ? i <= currentStep
                    ? "failed"
                    : "pending"
                  : i < currentStep
                    ? "done"
                    : i === currentStep
                      ? "active"
                      : "pending";
              return (
                <li key={step} className={`progress-step step-${state}`}>
                  <span className="step-dot" aria-hidden="true">
                    {state === "done" ? "✓" : state === "failed" ? "✕" : i + 1}
                  </span>
                  <span className="step-label">{STATUS_LABELS[step]}</span>
                </li>
              );
            })}
          </ol>

          {status === "failed" ? (
            <Callout tone="danger" title="Migration failed">
              {session.error || "The migration could not complete."}
              <div className="muted">
                Your existing data is safe — indiepg took a safety backup before importing.
              </div>
            </Callout>
          ) : status === "expired" ? (
            <Callout tone="warn" title="Session expired">
              The code was not used in time. Start a new session to try again.
            </Callout>
          ) : null}

          {verification ? (
            verification.verified ? (
              <Callout tone="ok" title="Verified">
                <Badge tone="ok">✓ Verified: {count(verification.total)} rows matched</Badge>
                <div className="muted">Source and target row counts are identical.</div>
              </Callout>
            ) : (
              <Callout tone="warn" title="Row counts do not match">
                <table className="data-table compact">
                  <thead>
                    <tr>
                      <th>Table</th>
                      <th>Source</th>
                      <th>Target</th>
                    </tr>
                  </thead>
                  <tbody>
                    {verification.diffs.map((d) => (
                      <tr key={d.table}>
                        <td>{d.table}</td>
                        <td>{count(d.source)}</td>
                        <td>{count(d.target)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Callout>
            )
          ) : null}
        </>
      )}

      <Modal
        open={cancelOpen}
        title="Cancel this migration?"
        tone="danger"
        width="sm"
        onClose={() => setCancelOpen(false)}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setCancelOpen(false)} disabled={cancelBusy}>
              Keep going
            </button>
            <button type="button" className="btn btn-danger" onClick={cancel} disabled={cancelBusy}>
              {cancelBusy ? "Cancelling…" : "Cancel migration"}
            </button>
          </>
        }
      >
        <p>The session and its temporary files will be removed. Nothing on this server changes.</p>
      </Modal>
    </Card>
  );
}

// --- Mode 2: Single database -----------------------------------------------

function SingleDBForm() {
  const toast = useToast();
  const [database, setDatabase] = useState("");
  const [sourceHost, setSourceHost] = useState("");
  const [transport, setTransport] = useState<"s3" | "ssh">("s3");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.migrateSingleDB({
        database: database.trim(),
        source_host: sourceHost.trim(),
        transport,
        confirm: confirm.trim(),
      });
      toast.success(res.message || "Migration started.");
      setConfirmOpen(false);
      setConfirm("");
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setBusy(false);
    }
  };

  const matches = confirm.trim() === database.trim();

  return (
    <Card title="Pull one database from another host">
      <p className="muted">
        Copy a single database from another server onto this one. Choose{" "}
        <strong>cloud bucket</strong> (safer, works anywhere) or a direct{" "}
        <strong>SSH pipe</strong> (faster on the same network).
      </p>
      <form
        className="inline-form"
        onSubmit={(e) => {
          e.preventDefault();
          setConfirmOpen(true);
        }}
      >
        {error ? <ErrorNotice error={error} /> : null}
        <label className="field">
          <span className="field-label">Database name</span>
          <input type="text" value={database} placeholder="myapp" onChange={(e) => setDatabase(e.target.value)} />
        </label>
        <label className="field">
          <span className="field-label">Source host</span>
          <input
            type="text"
            value={sourceHost}
            placeholder="10.0.0.5 or db.old-server"
            onChange={(e) => setSourceHost(e.target.value)}
          />
        </label>
        <fieldset className="field">
          <legend className="field-label">Transport</legend>
          <label className="radio">
            <input type="radio" checked={transport === "s3"} onChange={() => setTransport("s3")} />
            <span>Cloud bucket — safer, resumable, no direct connection needed</span>
          </label>
          <label className="radio">
            <input type="radio" checked={transport === "ssh"} onChange={() => setTransport("ssh")} />
            <span>Direct SSH pipe — faster on a local network</span>
          </label>
        </fieldset>
        <button type="submit" className="btn btn-primary" disabled={!database.trim() || !sourceHost.trim()}>
          Continue
        </button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm migration"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </button>
            <button type="button" className="btn btn-danger" onClick={start} disabled={busy || !matches}>
              {busy ? "Starting…" : "Start migration"}
            </button>
          </>
        }
      >
        <Callout tone="danger" title="This will overwrite the target database">
          If <strong>{database || "this database"}</strong> already exists here, it will be replaced.
          A safety backup is taken first.
        </Callout>
        <label className="field">
          <span className="field-label">
            Type <code>{database}</code> to confirm
          </span>
          <input
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={database}
            aria-invalid={confirm.length > 0 && !matches}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </label>
      </Modal>
    </Card>
  );
}

// --- Mode 3: Whole cluster -------------------------------------------------

function ClusterForm() {
  const toast = useToast();
  const [sourceHost, setSourceHost] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const EXPECTED = "MIGRATE CLUSTER";

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.migrateCluster({ source_host: sourceHost.trim(), confirm: confirm.trim() });
      toast.success(res.message || "Cluster migration started.");
      setConfirmOpen(false);
      setConfirm("");
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setBusy(false);
    }
  };

  const matches = confirm.trim() === EXPECTED;

  return (
    <Card title="Migrate an entire cluster">
      <p className="muted">
        Bring over <strong>every database</strong> plus the shared{" "}
        <strong>roles and permissions</strong> (globals) from another server, through your cloud
        bucket. Use this when you&apos;re replacing a whole server.
      </p>
      <Callout tone="warn">
        This is a big operation. It can overwrite databases and roles that already exist here.
        Take it slow and make sure this is the right target server.
      </Callout>
      <form
        className="inline-form"
        onSubmit={(e) => {
          e.preventDefault();
          setConfirmOpen(true);
        }}
      >
        {error ? <ErrorNotice error={error} /> : null}
        <label className="field">
          <span className="field-label">Source host</span>
          <input
            type="text"
            value={sourceHost}
            placeholder="10.0.0.5 or db.old-server"
            onChange={(e) => setSourceHost(e.target.value)}
          />
        </label>
        <button type="submit" className="btn btn-primary" disabled={!sourceHost.trim()}>
          Continue
        </button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm whole-cluster migration"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </button>
            <button type="button" className="btn btn-danger" onClick={start} disabled={busy || !matches}>
              {busy ? "Starting…" : "Migrate cluster"}
            </button>
          </>
        }
      >
        <Callout tone="danger" title="This replaces databases and roles">
          All databases and roles from <strong>{sourceHost || "the source"}</strong> will be brought
          here, overwriting conflicting ones. A safety backup is taken first.
        </Callout>
        <label className="field">
          <span className="field-label">
            Type <code>{EXPECTED}</code> to confirm
          </span>
          <input
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={EXPECTED}
            aria-invalid={confirm.length > 0 && !matches}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </label>
      </Modal>
    </Card>
  );
}
