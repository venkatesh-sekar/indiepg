// Migrate: move a database onto this server. Two transports, one engine:
//   • Direct pull (recommended) — this panel runs pg_dump against a source
//     Postgres you can reach and pg_restore into its own. Needs no S3.
//       - one database  (POST /migrate/single-db)
//       - whole cluster (POST /migrate/cluster) — every DB + roles/grants
//   • Cross-panel session — two indiepg panels coordinate through a shared S3
//     bucket via a 6-char code (no direct connection between them).
//
// Every mode is a background job the panel records locally and the UI polls:
// direct jobs by id (GET /migrate/{id}); the session by code. Destructive
// overwrites require typing the database name (or OVERWRITE for a cluster).

import { useMemo, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { bytes, count, dateTime, ago } from "@/lib/format";
import { usePolling } from "@/lib/hooks";
import { Modal } from "@/components/Modal";
import { useToast } from "@/components/Toast";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
  StaleBanner,
} from "@/components/ui";
import {
  CLUSTER_OVERWRITE_CONFIRM,
  type MigrationMode,
  type MigrationPhase,
  type MigrationRecord,
  type MigrationSession,
  type MigrationStatus,
  type SourceConn,
} from "@/api/types";

export function Migrate() {
  const [mode, setMode] = useState<MigrationMode>("single-db");

  return (
    <div className="view">
      <PageHeader
        title="Migrate"
        description="Move a database onto this server from another host — safely, with verification."
      />

      <Callout tone="info" title="Always verified">
        After importing, indiepg compares <strong>row counts</strong> between the source and this
        server table by table. A migration only reports success when every table matches. Existing
        databases are never overwritten unless you explicitly confirm by name.
      </Callout>

      <div className="mode-tabs" role="tablist">
        <ModeTab id="single-db" active={mode} onSelect={setMode} label="One database" hint="Direct pull · recommended" />
        <ModeTab id="cluster" active={mode} onSelect={setMode} label="Whole cluster" hint="All DBs + roles" />
        <ModeTab id="ssh-less" active={mode} onSelect={setMode} label="Cross-panel session" hint="Two panels via S3" />
      </div>

      {mode === "single-db" ? <SingleDBForm /> : null}
      {mode === "cluster" ? <ClusterForm /> : null}
      {mode === "ssh-less" ? <SessionPanel /> : null}

      <MigrationHistory />
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

// ---------------------------------------------------------------------------
// Source connection fields (shared by direct pull + ssh-less export)
// ---------------------------------------------------------------------------

interface ConnState {
  host: string;
  port: string;
  user: string;
  password: string;
  database: string;
  sslmode: string;
}

const emptyConn: ConnState = {
  host: "",
  port: "5432",
  user: "postgres",
  password: "",
  database: "",
  sslmode: "prefer",
};

/** Builds the wire SourceConn, dropping empty optionals. */
function toSourceConn(c: ConnState, includeDatabase: boolean): SourceConn {
  return {
    host: c.host.trim(),
    port: c.port.trim() || undefined,
    user: c.user.trim() || undefined,
    password: c.password || undefined,
    sslmode: c.sslmode.trim() || undefined,
    database: includeDatabase ? c.database.trim() || undefined : undefined,
  };
}

function SourceFields({
  conn,
  set,
  showDatabase = true,
}: {
  conn: ConnState;
  set: (next: ConnState) => void;
  showDatabase?: boolean;
}) {
  const upd = (patch: Partial<ConnState>) => set({ ...conn, ...patch });
  return (
    <fieldset className="field source-fields">
      <legend className="field-label">Source Postgres</legend>
      <div className="field-row">
        <label className="field">
          <span className="field-label">Host</span>
          <input
            type="text"
            value={conn.host}
            placeholder="db.old-server or 10.0.0.5"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => upd({ host: e.target.value })}
          />
        </label>
        <label className="field field-narrow">
          <span className="field-label">Port</span>
          <input type="text" value={conn.port} placeholder="5432" onChange={(e) => upd({ port: e.target.value })} />
        </label>
      </div>
      <div className="field-row">
        <label className="field">
          <span className="field-label">User</span>
          <input
            type="text"
            value={conn.user}
            placeholder="postgres"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => upd({ user: e.target.value })}
          />
        </label>
        <label className="field">
          <span className="field-label">Password</span>
          <input
            type="password"
            value={conn.password}
            autoComplete="new-password"
            placeholder="••••••••"
            onChange={(e) => upd({ password: e.target.value })}
          />
        </label>
      </div>
      {showDatabase ? (
        <label className="field">
          <span className="field-label">Database to copy</span>
          <input
            type="text"
            value={conn.database}
            placeholder="myapp"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => upd({ database: e.target.value })}
          />
        </label>
      ) : null}
      <span className="field-help muted">
        The password is used once to run the copy and is never stored or logged.
      </span>
    </fieldset>
  );
}

// ---------------------------------------------------------------------------
// Mode 1: Direct pull — one database
// ---------------------------------------------------------------------------

function SingleDBForm() {
  const toast = useToast();
  const [conn, setConn] = useState<ConnState>(emptyConn);
  const [target, setTarget] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [jobId, setJobId] = useState<number | null>(null);

  // Default the target name to the source database (import alongside), editable.
  const effectiveTarget = (target.trim() || conn.database.trim()).trim();
  const ready = conn.host.trim() !== "" && conn.database.trim() !== "" && effectiveTarget !== "";

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.migrateSingleDB({
        source: toSourceConn(conn, true),
        target_database: effectiveTarget,
        overwrite,
        confirm: overwrite ? confirm.trim() : "",
      });
      toast.success("Migration started.");
      setConfirmOpen(false);
      setConfirm("");
      setJobId(res.id);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (overwrite) setConfirmOpen(true);
    else void start();
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={() => setJobId(null)} />;
  }

  const overwriteMatches = confirm.trim() === effectiveTarget;

  return (
    <Card title="Pull one database from another server">
      <p className="muted">
        This server connects to the source Postgres directly and copies one database in. No S3 and
        no second panel needed — just network access to the source.
      </p>
      <form className="inline-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <SourceFields conn={conn} set={setConn} />
        <label className="field">
          <span className="field-label">Name on this server</span>
          <input
            type="text"
            value={target}
            placeholder={conn.database || "myapp"}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTarget(e.target.value)}
          />
          <span className="field-help muted">
            Defaults to the source name. Use a new name to import alongside an existing database.
          </span>
        </label>
        <label className="checkbox">
          <input type="checkbox" checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
          <span>
            Replace <code>{effectiveTarget || "the target"}</code> if it already exists (destructive)
          </span>
        </label>
        <button type="submit" className="btn btn-primary" disabled={!ready || busy}>
          {busy ? "Starting…" : overwrite ? "Continue…" : "Start migration"}
        </button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm overwrite"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </button>
            <button type="button" className="btn btn-danger" onClick={start} disabled={busy || !overwriteMatches}>
              {busy ? "Starting…" : "Overwrite & migrate"}
            </button>
          </>
        }
      >
        <Callout tone="danger" title="This will drop the existing database">
          <strong>{effectiveTarget}</strong> on this server will be dropped and recreated from the
          source. This cannot be undone.
        </Callout>
        <label className="field">
          <span className="field-label">
            Type <code>{effectiveTarget}</code> to confirm
          </span>
          <input
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={effectiveTarget}
            aria-invalid={confirm.length > 0 && !overwriteMatches}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </label>
      </Modal>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Mode 2: Direct pull — whole cluster
// ---------------------------------------------------------------------------

function ClusterForm() {
  const toast = useToast();
  const [conn, setConn] = useState<ConnState>(emptyConn);
  const [exclude, setExclude] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [jobId, setJobId] = useState<number | null>(null);

  const ready = conn.host.trim() !== "";

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const excludeList = exclude
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
      const res = await api.migrateCluster({
        source: toSourceConn(conn, false),
        overwrite,
        exclude: excludeList.length ? excludeList : undefined,
        confirm: overwrite ? confirm.trim() : "",
      });
      toast.success("Cluster migration started.");
      setConfirmOpen(false);
      setConfirm("");
      setJobId(res.id);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (overwrite) setConfirmOpen(true);
    else void start();
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={() => setJobId(null)} />;
  }

  const overwriteMatches = confirm.trim() === CLUSTER_OVERWRITE_CONFIRM;

  return (
    <Card title="Pull an entire cluster from another server">
      <p className="muted">
        Brings over <strong>every database</strong> plus the shared <strong>roles and grants</strong>{" "}
        (globals) from the source. Use this when you&apos;re replacing a whole server.
      </p>
      <Callout tone="warn">
        This is a big operation. With overwrite on, it can drop every matching database on this
        server. Make sure this is the right target.
      </Callout>
      <form className="inline-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <SourceFields conn={conn} set={setConn} showDatabase={false} />
        <label className="field">
          <span className="field-label">Exclude databases (optional)</span>
          <input
            type="text"
            value={exclude}
            placeholder="analytics, scratch"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setExclude(e.target.value)}
          />
          <span className="field-help muted">Comma-separated names to skip.</span>
        </label>
        <label className="checkbox">
          <input type="checkbox" checked={overwrite} onChange={(e) => setOverwrite(e.target.checked)} />
          <span>Replace databases that already exist here (destructive)</span>
        </label>
        <button type="submit" className="btn btn-primary" disabled={!ready || busy}>
          {busy ? "Starting…" : overwrite ? "Continue…" : "Migrate cluster"}
        </button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm whole-cluster overwrite"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <button type="button" className="btn" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </button>
            <button type="button" className="btn btn-danger" onClick={start} disabled={busy || !overwriteMatches}>
              {busy ? "Starting…" : "Overwrite & migrate cluster"}
            </button>
          </>
        }
      >
        <Callout tone="danger" title="This replaces databases on this server">
          Every matching database on this server will be dropped and recreated from the source.
        </Callout>
        <label className="field">
          <span className="field-label">
            Type <code>{CLUSTER_OVERWRITE_CONFIRM}</code> to confirm
          </span>
          <input
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={CLUSTER_OVERWRITE_CONFIRM}
            aria-invalid={confirm.length > 0 && !overwriteMatches}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </label>
      </Modal>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Direct-job live progress (polls GET /migrate/{id})
// ---------------------------------------------------------------------------

const PHASE_LABELS: Record<MigrationPhase, string> = {
  "": "Working…",
  validating: "Checking the source and target…",
  dumping: "Exporting from the source (pg_dump)…",
  uploading: "Uploading the dump…",
  downloading: "Downloading the dump…",
  restoring: "Importing into this server (pg_restore)…",
  verifying: "Verifying row counts…",
};

function isTerminal(status: MigrationStatus): boolean {
  return status === "completed" || status === "failed" || status === "expired";
}

function DirectJobProgress({ id, onReset }: { id: number; onReset: () => void }) {
  const { data: job, error } = usePolling<MigrationRecord>(
    (signal) => api.getMigration(id, signal),
    2000,
  );

  const terminal = job ? isTerminal(job.status) : false;
  const verification = useVerification(job?.row_counts_src, job?.row_counts_tgt);

  return (
    <Card
      title="Migration in progress"
      actions={
        terminal ? (
          <button type="button" className="btn btn-sm" onClick={onReset}>
            Start another
          </button>
        ) : null
      }
    >
      {error && !job ? <ErrorNotice error={error} /> : null}
      {/* Poll failed while a job is on screen — don't leave a frozen spinner
          looking like progress; say the status check stalled. */}
      {error && job && !terminal ? <StaleBanner error={error} /> : null}
      {!job ? (
        <Spinner label="Starting…" />
      ) : job.status === "failed" ? (
        <Callout tone="danger" title="Migration failed">
          {job.error || "The migration could not complete."}
          <div className="muted">
            Your existing data is intact — the import only writes a freshly created database.
          </div>
        </Callout>
      ) : job.status === "completed" ? (
        <CompletedView job={job} verification={verification} />
      ) : (
        <div className="job-running">
          <Spinner label={PHASE_LABELS[job.phase] ?? "Working…"} />
          <ProgressMeter job={job} />
          {job.source_summary ? <p className="muted small">From {job.source_summary}</p> : null}
        </div>
      )}
    </Card>
  );
}

function ProgressMeter({ job }: { job: MigrationRecord }) {
  const parts: string[] = [];
  if (job.progress_total > 0) {
    parts.push(`${job.progress_done}/${job.progress_total} databases`);
  }
  if (job.bytes_total > 0) {
    parts.push(`${bytes(job.bytes_total)} dumped`);
  }
  if (parts.length === 0) return null;
  return <p className="muted small">{parts.join(" · ")}</p>;
}

function CompletedView({
  job,
  verification,
}: {
  job: MigrationRecord;
  verification: Verification | null;
}) {
  return (
    <>
      <Callout tone="ok" title="Migration complete">
        {job.mode === "cluster" ? "Cluster" : `Database ${job.target_database}`} imported
        successfully.
      </Callout>
      {verification ? <VerificationView v={verification} /> : null}
    </>
  );
}

// ---------------------------------------------------------------------------
// Mode 3: Cross-panel ssh-less session
// ---------------------------------------------------------------------------

function SessionPanel() {
  const [role, setRole] = useState<"receive" | "send">("receive");
  return (
    <Card title="Cross-panel migration (no direct connection)">
      <p className="muted">
        For moving between two indiepg servers that can&apos;t reach each other&apos;s Postgres but
        share an S3 bucket. The receiving server generates a code; the sending server enters it.
        This mode requires S3 configured on both panels.
      </p>
      <div className="segmented" role="tablist">
        <button
          type="button"
          role="tab"
          aria-selected={role === "receive"}
          className={`seg ${role === "receive" ? "active" : ""}`}
          onClick={() => setRole("receive")}
        >
          Receive here
        </button>
        <button
          type="button"
          role="tab"
          aria-selected={role === "send"}
          className={`seg ${role === "send" ? "active" : ""}`}
          onClick={() => setRole("send")}
        >
          Send from here
        </button>
      </div>
      {role === "receive" ? <SessionReceive /> : <SessionSend />}
    </Card>
  );
}

const PROGRESS_STEPS: MigrationStatus[] = [
  "waiting_for_export",
  "exporting",
  "exported",
  "importing",
  "completed",
];

const STATUS_LABELS: Record<MigrationStatus, string> = {
  waiting_for_export: "Waiting for the other server to start…",
  exporting: "The other server is exporting…",
  exported: "Export finished — importing here…",
  importing: "Importing onto this server…",
  completed: "Done",
  failed: "Failed",
  expired: "Session expired",
};

function SessionReceive() {
  const [database, setDatabase] = useState("");
  const [code, setCode] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setCreating(true);
    setError(null);
    try {
      const session = await api.createSession({ database: database.trim() });
      setCode(session.code);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setCreating(false);
    }
  };

  if (code) {
    return <SessionProgress code={code} onReset={() => setCode(null)} />;
  }

  return (
    <form onSubmit={create} className="inline-form">
      {error ? <S3OrError error={error} /> : null}
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
      <button type="submit" className="btn btn-primary" disabled={creating || !database.trim()}>
        {creating ? "Creating…" : "Generate code"}
      </button>
    </form>
  );
}

function SessionProgress({ code, onReset }: { code: string; onReset: () => void }) {
  const toast = useToast();
  const { data: session, error } = usePolling<MigrationSession>(
    (signal) => api.getSession(code, signal),
    2500,
  );
  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelBusy, setCancelBusy] = useState(false);

  const status = session?.status ?? "waiting_for_export";
  const currentStep = Math.max(0, PROGRESS_STEPS.indexOf(status));
  const terminal = isTerminal(status);
  const verification = useVerification(session?.source_row_counts, session?.target_row_counts);

  const cancel = async () => {
    setCancelBusy(true);
    try {
      await api.cancelSession(code);
      toast.info("Session cancelled.");
      onReset();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not cancel.");
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
      {error && !session ? <S3OrError error={error} /> : null}
      {/* Poll failed while a live session is on screen — surface the stall so a
          frozen stepper can't look like the handshake is still progressing. */}
      {error && session && !terminal ? <StaleBanner error={error} /> : null}

      <div className="session-code-block">
        <span className="muted">On the source server&apos;s panel, choose “Send from here” and enter:</span>
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
            </Callout>
          ) : status === "expired" ? (
            <Callout tone="warn" title="Session expired">
              The code was not used in time. Start a new session to try again.
            </Callout>
          ) : null}

          {verification ? <VerificationView v={verification} /> : null}
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

function SessionSend() {
  const toast = useToast();
  const [code, setCode] = useState("");
  const [conn, setConn] = useState<ConnState>(emptyConn);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  const [jobId, setJobId] = useState<number | null>(null);

  const ready = code.trim() !== "" && conn.host.trim() !== "" && conn.database.trim() !== "";

  const start = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await api.exportToSession(code.trim().toUpperCase(), {
        source: toSourceConn(conn, true),
        database: conn.database.trim(),
      });
      toast.success("Export started.");
      setJobId(res.id);
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={() => setJobId(null)} />;
  }

  return (
    <form className="inline-form" onSubmit={start}>
      {error ? <S3OrError error={error} /> : null}
      <label className="field field-narrow">
        <span className="field-label">Session code</span>
        <input
          type="text"
          value={code}
          placeholder="XK7M2P"
          autoComplete="off"
          spellCheck={false}
          maxLength={6}
          style={{ textTransform: "uppercase" }}
          onChange={(e) => setCode(e.target.value)}
        />
        <span className="field-help muted">The code shown on the receiving server.</span>
      </label>
      <SourceFields conn={conn} set={setConn} />
      <button type="submit" className="btn btn-primary" disabled={!ready || busy}>
        {busy ? "Starting…" : "Send database"}
      </button>
    </form>
  );
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

function MigrationHistory() {
  const { data, error } = usePolling<MigrationRecord[]>((signal) => api.listMigrations(signal), 5000);

  return (
    <Card title="Recent migrations">
      {error && !data ? <ErrorNotice error={error} /> : null}
      {!data ? (
        <Spinner label="Loading…" />
      ) : data.length === 0 ? (
        <EmptyState title="No migrations yet" hint="Started migrations appear here with live status." />
      ) : (
        <table className="data-table compact">
          <thead>
            <tr>
              <th>When</th>
              <th>Mode</th>
              <th>Source / target</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {data.map((m) => (
              <tr key={m.id}>
                <td title={dateTime(m.created_at)}>{ago(m.created_at)}</td>
                <td>{MODE_LABELS[m.mode] ?? m.mode}</td>
                <td className="muted small">{m.source_summary || m.target_database || `code ${m.code}`}</td>
                <td>
                  <StatusBadge status={m.status} phase={m.phase} />
                  {m.status === "failed" && m.error ? <div className="muted small">{m.error}</div> : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

const MODE_LABELS: Record<MigrationMode, string> = {
  "single-db": "One database",
  cluster: "Whole cluster",
  "ssh-less": "Cross-panel",
};

function StatusBadge({ status, phase }: { status: MigrationStatus; phase: MigrationPhase }) {
  if (status === "completed") return <Badge tone="ok">Completed</Badge>;
  if (status === "failed") return <Badge tone="danger">Failed</Badge>;
  if (status === "expired") return <Badge tone="warn">Expired</Badge>;
  return <Badge tone="info">{phase ? phase : status.replace(/_/g, " ")}</Badge>;
}

// ---------------------------------------------------------------------------
// Shared: verification + helpers
// ---------------------------------------------------------------------------

interface Verification {
  verified: boolean;
  total: number;
  diffs: Array<{ table: string; source: number; target: number }>;
}

/** Compares source vs target row-count maps into a verification verdict, or null
 *  when counts aren't available yet (job still running / no data). */
function useVerification(
  src?: Record<string, number>,
  tgt?: Record<string, number>,
): Verification | null {
  return useMemo(() => {
    if (!src || !tgt) return null;
    const tables = new Set([...Object.keys(src), ...Object.keys(tgt)]);
    if (tables.size === 0) return null;
    let total = 0;
    const diffs: Verification["diffs"] = [];
    for (const table of tables) {
      const s = src[table] ?? 0;
      const t = tgt[table] ?? 0;
      total += t;
      if (s !== t) diffs.push({ table, source: s, target: t });
    }
    diffs.sort((a, b) => a.table.localeCompare(b.table));
    return { verified: diffs.length === 0, total, diffs };
  }, [src, tgt]);
}

function VerificationView({ v }: { v: Verification }) {
  if (v.verified) {
    return (
      <Callout tone="ok" title="Verified">
        <Badge tone="ok">✓ {count(v.total)} rows matched</Badge>
        <div className="muted">Source and target row counts are identical.</div>
      </Callout>
    );
  }
  return (
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
          {v.diffs.map((d) => (
            <tr key={d.table}>
              <td>{d.table}</td>
              <td>{count(d.source)}</td>
              <td>{count(d.target)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </Callout>
  );
}

/** Renders the honest "requires S3" error as a helpful callout (with a pointer
 *  to Settings / direct pull), and any other error as the standard notice. */
function S3OrError({ error }: { error: ApiError }) {
  const aboutS3 = /S3/i.test(error.message) || /S3/i.test(error.hint ?? "");
  if (aboutS3) {
    return (
      <Callout tone="warn" title="Cross-panel migration needs S3">
        {error.hint || "Configure an S3 backup target in Settings, or use the Direct pull modes which need no S3."}
      </Callout>
    );
  }
  return <ErrorNotice error={error} />;
}

function asApiError(err: unknown): ApiError {
  return err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) });
}
