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

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { bytes, count, dateTime, duration, ago } from "@/lib/format";
import { usePolling } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { Modal } from "@/components/Modal";
import { toast } from "sonner";
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
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Field,
  FieldDescription,
  FieldError,
  FieldGroup,
  FieldLabel,
  FieldLegend,
  FieldSet,
} from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  CLUSTER_OVERWRITE_CONFIRM,
  type CreateDropoffResult,
  type DropoffSession,
  type DropoffStatus,
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
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Migrate"
        description="Move a database onto this server from another host — safely, with verification."
      />

      <Callout tone="info" title="Always verified">
        After importing, indiepg compares <strong>row counts</strong> between the source and this
        server table by table. A migration only reports success when every table matches. Existing
        databases are never overwritten unless you explicitly confirm by name.
      </Callout>

      <Tabs value={mode} onValueChange={(v) => setMode(v as MigrationMode)}>
        <TabsList variant="line" className="h-auto w-full">
          <ModeTab id="single-db" label="One database" hint="Direct pull · recommended" />
          <ModeTab id="cluster" label="Whole cluster" hint="All DBs + roles" />
          <ModeTab id="ssh-less" label="Cross-panel session" hint="Two panels via S3" />
          <ModeTab id="drop-off" label="Receive a drop" hint="No inbound · via S3" />
        </TabsList>

        <TabsContent value="single-db">
          <SingleDBForm />
        </TabsContent>
        <TabsContent value="cluster">
          <ClusterForm />
        </TabsContent>
        <TabsContent value="ssh-less">
          <SessionPanel />
        </TabsContent>
        <TabsContent value="drop-off">
          <DropoffPanel />
        </TabsContent>
      </Tabs>

      <MigrationHistory />
    </div>
  );
}

function ModeTab({ id, label, hint }: { id: MigrationMode; label: string; hint: string }) {
  return (
    <TabsTrigger value={id} className="h-auto flex-col gap-0.5 py-2">
      <span className="font-medium">{label}</span>
      <span className="text-xs text-muted-foreground">{hint}</span>
    </TabsTrigger>
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
    <FieldSet className="rounded-md border p-4">
      <FieldLegend>Source Postgres</FieldLegend>
      <FieldGroup>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-[2fr_1fr]">
          <Field>
            <FieldLabel htmlFor="src-host">Host</FieldLabel>
            <Input
              id="src-host"
              value={conn.host}
              placeholder="db.old-server or 10.0.0.5"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => upd({ host: e.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="src-port">Port</FieldLabel>
            <Input
              id="src-port"
              value={conn.port}
              placeholder="5432"
              onChange={(e) => upd({ port: e.target.value })}
            />
          </Field>
        </div>
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="src-user">User</FieldLabel>
            <Input
              id="src-user"
              value={conn.user}
              placeholder="postgres"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => upd({ user: e.target.value })}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor="src-password">Password</FieldLabel>
            <Input
              id="src-password"
              type="password"
              value={conn.password}
              autoComplete="new-password"
              placeholder="••••••••"
              onChange={(e) => upd({ password: e.target.value })}
            />
          </Field>
        </div>
        {showDatabase ? (
          <Field>
            <FieldLabel htmlFor="src-database">Database to copy</FieldLabel>
            <Input
              id="src-database"
              value={conn.database}
              placeholder="myapp"
              autoComplete="off"
              spellCheck={false}
              onChange={(e) => upd({ database: e.target.value })}
            />
          </Field>
        ) : null}
        <FieldDescription>
          The password is used once to run the copy and is never stored or logged.
        </FieldDescription>
      </FieldGroup>
    </FieldSet>
  );
}

// ---------------------------------------------------------------------------
// Mode 1: Direct pull — one database
// ---------------------------------------------------------------------------

export function SingleDBForm() {
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

  // "Start another" keeps the source connection (so the common case — pulling the
  // next database off the same host — needs no re-typing) but clears the per-run
  // fields: the database to copy, the target name, and crucially the destructive
  // overwrite flag, so a leftover "replace" can't carry onto a different target.
  const reset = () => {
    setConn({ ...conn, database: "" });
    setTarget("");
    setOverwrite(false);
    setConfirm("");
    setError(null);
    setJobId(null);
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={reset} />;
  }

  const overwriteMatches = confirm.trim() === effectiveTarget;

  return (
    <Card title="Pull one database from another server">
      <p className="text-muted-foreground">
        This server connects to the source Postgres directly and copies one database in. No S3 and
        no second panel needed — just network access to the source.
      </p>
      <form className="mt-3 flex max-w-xl flex-col gap-5" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <SourceFields conn={conn} set={setConn} />
        <Field>
          <FieldLabel htmlFor="single-target">Name on this server</FieldLabel>
          <Input
            id="single-target"
            value={target}
            placeholder={conn.database || "myapp"}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setTarget(e.target.value)}
          />
          <FieldDescription>
            Defaults to the source name. Use a new name to import alongside an existing database.
          </FieldDescription>
        </Field>
        <Field orientation="horizontal">
          <Checkbox
            id="single-overwrite"
            checked={overwrite}
            onCheckedChange={(c) => setOverwrite(c === true)}
          />
          <FieldLabel htmlFor="single-overwrite" className="font-normal">
            Replace <code>{effectiveTarget || "the target"}</code> if it already exists (destructive)
          </FieldLabel>
        </Field>
        <Button type="submit" className="self-start" disabled={!ready || busy}>
          {busy ? (
            <>
              <InlineSpinner data-icon="inline-start" />
              Starting…
            </>
          ) : overwrite ? (
            "Continue…"
          ) : (
            "Start migration"
          )}
        </Button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm overwrite"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <Button type="button" variant="outline" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={start}
              disabled={busy || !overwriteMatches}
            >
              {busy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Starting…
                </>
              ) : (
                "Overwrite & migrate"
              )}
            </Button>
          </>
        }
      >
        <Callout tone="danger" title="This will drop the existing database">
          <strong>{effectiveTarget}</strong> on this server will be dropped and recreated from the
          source. This cannot be undone.
        </Callout>
        <Field className="mt-4" data-invalid={confirm.length > 0 && !overwriteMatches}>
          <FieldLabel htmlFor="single-confirm">
            Type <code>{effectiveTarget}</code> to confirm
          </FieldLabel>
          <Input
            id="single-confirm"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={effectiveTarget}
            aria-invalid={confirm.length > 0 && !overwriteMatches}
            aria-describedby={confirm.length > 0 && !overwriteMatches ? "single-confirm-err" : undefined}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {confirm.length > 0 && !overwriteMatches ? (
            <FieldError id="single-confirm-err">
              Must match <code>{effectiveTarget}</code> exactly.
            </FieldError>
          ) : null}
        </Field>
      </Modal>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Mode 2: Direct pull — whole cluster
// ---------------------------------------------------------------------------

function ClusterForm() {
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

  // "Start another" keeps the source connection (see SingleDBForm) but clears the
  // per-run fields — exclude list and, crucially, the destructive overwrite flag,
  // so a leftover "replace" can't carry into the next run.
  const reset = () => {
    setExclude("");
    setOverwrite(false);
    setConfirm("");
    setError(null);
    setJobId(null);
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={reset} />;
  }

  const overwriteMatches = confirm.trim() === CLUSTER_OVERWRITE_CONFIRM;

  return (
    <Card title="Pull an entire cluster from another server">
      <p className="text-muted-foreground">
        Brings over <strong>every database</strong> plus the shared <strong>roles and grants</strong>{" "}
        (globals) from the source. Use this when you&apos;re replacing a whole server.
      </p>
      <Callout tone="warn">
        This is a big operation. With overwrite on, it can drop every matching database on this
        server. Make sure this is the right target.
      </Callout>
      <form className="mt-3 flex max-w-xl flex-col gap-5" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <SourceFields conn={conn} set={setConn} showDatabase={false} />
        <Field>
          <FieldLabel htmlFor="cluster-exclude">Exclude databases (optional)</FieldLabel>
          <Input
            id="cluster-exclude"
            value={exclude}
            placeholder="analytics, scratch"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setExclude(e.target.value)}
          />
          <FieldDescription>Comma-separated names to skip.</FieldDescription>
        </Field>
        <Field orientation="horizontal">
          <Checkbox
            id="cluster-overwrite"
            checked={overwrite}
            onCheckedChange={(c) => setOverwrite(c === true)}
          />
          <FieldLabel htmlFor="cluster-overwrite" className="font-normal">
            Replace databases that already exist here (destructive)
          </FieldLabel>
        </Field>
        <Button type="submit" className="self-start" disabled={!ready || busy}>
          {busy ? (
            <>
              <InlineSpinner data-icon="inline-start" />
              Starting…
            </>
          ) : overwrite ? (
            "Continue…"
          ) : (
            "Migrate cluster"
          )}
        </Button>
      </form>

      <Modal
        open={confirmOpen}
        title="Confirm whole-cluster overwrite"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <Button type="button" variant="outline" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={start}
              disabled={busy || !overwriteMatches}
            >
              {busy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Starting…
                </>
              ) : (
                "Overwrite & migrate cluster"
              )}
            </Button>
          </>
        }
      >
        <Callout tone="danger" title="This replaces databases on this server">
          Every matching database on this server will be dropped and recreated from the source.
        </Callout>
        <Field className="mt-4" data-invalid={confirm.length > 0 && !overwriteMatches}>
          <FieldLabel htmlFor="cluster-confirm">
            Type <code>{CLUSTER_OVERWRITE_CONFIRM}</code> to confirm
          </FieldLabel>
          <Input
            id="cluster-confirm"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={CLUSTER_OVERWRITE_CONFIRM}
            aria-invalid={confirm.length > 0 && !overwriteMatches}
            aria-describedby={confirm.length > 0 && !overwriteMatches ? "cluster-confirm-err" : undefined}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {confirm.length > 0 && !overwriteMatches ? (
            <FieldError id="cluster-confirm-err">
              Must match <code>{CLUSTER_OVERWRITE_CONFIRM}</code> exactly.
            </FieldError>
          ) : null}
        </Field>
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

export function DirectJobProgress({
  id,
  onReset,
  onRetry,
}: {
  id: number;
  onReset: () => void;
  // Optional: when provided and the job failed, render a "Retry import" action
  // alongside "Start another". Drop-off uses this to re-run the import from the
  // dump it keeps in S3 on failure, without re-pushing from the source.
  onRetry?: () => void;
}) {
  const { data: job, error } = usePolling<MigrationRecord>(
    (signal) => api.getMigration(id, signal),
    2000,
  );

  const terminal = job ? isTerminal(job.status) : false;
  const failed = job?.status === "failed";
  const verification = useVerification(job?.row_counts_src, job?.row_counts_tgt);

  return (
    <Card
      title="Migration in progress"
      actions={
        terminal ? (
          <div className="flex items-center gap-2">
            {onRetry && failed ? (
              <Button type="button" variant="outline" size="sm" onClick={onRetry}>
                Retry import
              </Button>
            ) : null}
            <Button type="button" variant="outline" size="sm" onClick={onReset}>
              Start another
            </Button>
          </div>
        ) : null
      }
    >
      {error && !job ? <ErrorNotice error={error} /> : null}
      {/* Poll failed while a job is on screen — don't leave a frozen spinner
          looking like progress; say the status check stalled. */}
      {error && job && !terminal ? <StaleBanner error={error} /> : null}
      {!job ? (
        // First poll: the error above OR a spinner, never both.
        error ? null : <Spinner label="Starting…" />
      ) : job.status === "failed" ? (
        <Callout tone="danger" title="Migration failed">
          {job.error || "The migration could not complete."}
          <div className="text-muted-foreground">
            {job.overwrite ? (
              <>
                Because you chose to replace{" "}
                {job.mode === "cluster"
                  ? "existing databases"
                  : `the existing ${job.target_database || "database"}`}
                , it may already have been dropped before the failure — restore from a backup if
                you need the old data back.
              </>
            ) : (
              "Your existing data is intact — the import only writes a freshly created database."
            )}
          </div>
        </Callout>
      ) : job.status === "completed" ? (
        <CompletedView job={job} verification={verification} />
      ) : (
        <div className="flex flex-col gap-1.5">
          <Spinner label={PHASE_LABELS[job.phase] ?? "Working…"} />
          <ProgressMeter job={job} />
          {job.source_summary ? (
            <p className="text-sm text-muted-foreground">From {job.source_summary}</p>
          ) : null}
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
  return <p className="text-sm text-muted-foreground">{parts.join(" · ")}</p>;
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
      <p className="text-muted-foreground">
        For moving between two indiepg servers that can&apos;t reach each other&apos;s Postgres but
        share an S3 bucket. The receiving server generates a code; the sending server enters it.
        This mode requires S3 configured on both panels.
      </p>
      <Tabs
        value={role}
        onValueChange={(v) => setRole(v as "receive" | "send")}
        className="mt-3"
      >
        <TabsList>
          <TabsTrigger value="receive">Receive here</TabsTrigger>
          <TabsTrigger value="send">Send from here</TabsTrigger>
        </TabsList>
        <TabsContent value="receive">
          <SessionReceive />
        </TabsContent>
        <TabsContent value="send">
          <SessionSend />
        </TabsContent>
      </Tabs>
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

  // "Start another" clears the database name too, so the next receive session
  // starts blank rather than re-using the last run's target.
  if (code) {
    return (
      <SessionProgress
        code={code}
        onReset={() => {
          setDatabase("");
          setError(null);
          setCode(null);
        }}
      />
    );
  }

  return (
    <form onSubmit={create} className="mt-3 flex max-w-xl flex-col gap-5">
      {error ? <S3OrError error={error} /> : null}
      <Field>
        <FieldLabel htmlFor="receive-database">Database to receive</FieldLabel>
        <Input
          id="receive-database"
          value={database}
          placeholder="myapp"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setDatabase(e.target.value)}
        />
        <FieldDescription>The name this database will have on this server.</FieldDescription>
      </Field>
      <Button type="submit" className="self-start" disabled={creating || !database.trim()}>
        {creating ? (
          <>
            <InlineSpinner data-icon="inline-start" />
            Creating…
          </>
        ) : (
          "Generate code"
        )}
      </Button>
    </form>
  );
}

export function SessionProgress({ code, onReset }: { code: string; onReset: () => void }) {
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
          <Button type="button" variant="outline" size="sm" onClick={onReset}>
            Start another
          </Button>
        ) : (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setCancelOpen(true)}
          >
            Cancel
          </Button>
        )
      }
    >
      {error && !session ? <S3OrError error={error} /> : null}
      {/* Poll failed while a live session is on screen — surface the stall so a
          frozen stepper can't look like the handshake is still progressing. */}
      {error && session && !terminal ? <StaleBanner error={error} /> : null}

      <div className="mb-4 flex flex-col items-center gap-1.5 rounded-md bg-muted p-5">
        <span className="text-muted-foreground">
          On the source server&apos;s panel, choose “Send from here” and enter:
        </span>
        <div className="font-mono text-4xl font-bold tracking-[0.18em] text-primary">{code}</div>
        {session ? (
          <span className="text-sm text-muted-foreground">
            Expires {dateTime(session.expires_at)}
            {session.database ? ` · database: ${session.database}` : ""}
          </span>
        ) : null}
      </div>

      {!session ? (
        // First poll: the error above OR a spinner, never both.
        error ? null : <Spinner label="Connecting…" />
      ) : (
        <>
          {/* Announce step advances to screen readers — the stepper below is
              visual only; terminal states get their own role=alert Callout. */}
          {!terminal ? (
            <p className="sr-only" aria-live="polite" aria-atomic="true">
              {STATUS_LABELS[status]}
            </p>
          ) : null}
          <ol className="mb-3 flex flex-col gap-0.5">
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
                <li
                  key={step}
                  className={cn(
                    "flex items-center gap-3 rounded-md px-3 py-2.5",
                    state === "active" && "bg-primary/10",
                    state === "pending" && "opacity-60",
                  )}
                >
                  <span
                    aria-hidden="true"
                    className={cn(
                      "inline-grid size-6 shrink-0 place-items-center rounded-full border-2 text-xs font-bold",
                      state === "done" && "border-success text-success",
                      state === "active" && "border-primary text-primary",
                      state === "failed" && "border-destructive text-destructive",
                      state === "pending" && "border-input text-muted-foreground",
                    )}
                  >
                    {state === "done" ? "✓" : state === "failed" ? "✕" : i + 1}
                  </span>
                  <span className={cn("text-sm", state === "active" && "font-semibold")}>
                    {STATUS_LABELS[step]}
                  </span>
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
            <Button type="button" variant="outline" onClick={() => setCancelOpen(false)} disabled={cancelBusy}>
              Keep going
            </Button>
            <Button type="button" variant="destructive" onClick={cancel} disabled={cancelBusy}>
              {cancelBusy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Cancelling…
                </>
              ) : (
                "Cancel migration"
              )}
            </Button>
          </>
        }
      >
        <p>The session and its temporary files will be removed. Nothing on this server changes.</p>
      </Modal>
    </Card>
  );
}

function SessionSend() {
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

  // "Start another" clears the one-time session code (so a stale code can't be
  // re-submitted) but keeps the source connection for sending the next database
  // off the same host.
  const reset = () => {
    setCode("");
    setError(null);
    setJobId(null);
  };

  if (jobId !== null) {
    return <DirectJobProgress id={jobId} onReset={reset} />;
  }

  return (
    <form className="mt-3 flex max-w-xl flex-col gap-5" onSubmit={start}>
      {error ? <S3OrError error={error} /> : null}
      <Field className="max-w-[200px]">
        <FieldLabel htmlFor="send-code">Session code</FieldLabel>
        <Input
          id="send-code"
          value={code}
          placeholder="XK7M2P"
          autoComplete="off"
          spellCheck={false}
          maxLength={6}
          className="uppercase"
          onChange={(e) => setCode(e.target.value)}
        />
        <FieldDescription>The code shown on the receiving server.</FieldDescription>
      </Field>
      <SourceFields conn={conn} set={setConn} />
      <Button type="submit" className="self-start" disabled={!ready || busy}>
        {busy ? (
          <>
            <InlineSpinner data-icon="inline-start" />
            Starting…
          </>
        ) : (
          "Send database"
        )}
      </Button>
    </form>
  );
}

// ---------------------------------------------------------------------------
// Mode 4: Drop-off link — push from a source the panel can't reach
//
// The panel mints two presigned S3 PUT URLs + a `curl … | sh` push command. The
// operator pastes it on a source behind NAT/firewall (no inbound, no panel); the
// source uploads one database's dump + a meta.json manifest; then the operator
// clicks Start here and the panel streams it from S3, verifies the SHA-256 and
// row counts, restores into the chosen target, and cleans up. Requires S3.
// ---------------------------------------------------------------------------

export function DropoffPanel() {
  const [created, setCreated] = useState<CreateDropoffResult | null>(null);
  const [resumed, setResumed] = useState<DropoffSession | null>(null);

  if (created) {
    // Freshly minted: hand the one-time push command + URLs to the progress view.
    // "Start another" clears the minted link so a stale (and soon-expired) command
    // can't be re-used, returning to a blank form.
    return (
      <DropoffProgress
        code={created.code}
        target={created.target_database}
        overwrite={created.overwrite}
        expiresAt={created.expires_at}
        commands={{ docker: created.command_docker, native: created.command_native }}
        onReset={() => setCreated(null)}
      />
    );
  }
  if (resumed) {
    // Resumed from the active-sessions list after a reload/tab close: the one-time
    // command is gone (served once, never re-served), but Start/Cancel and live
    // status are fully recoverable from the code alone.
    return (
      <DropoffProgress
        code={resumed.code}
        target={resumed.target_database}
        overwrite={resumed.overwrite}
        expiresAt={resumed.expires_at}
        onReset={() => setResumed(null)}
      />
    );
  }

  return (
    <div className="flex flex-col gap-5">
      <DropoffResumeList onResume={setResumed} />
      <Card title="Receive a drop from a server you can't reach">
        <p className="text-muted-foreground">
          Pulls <strong>one database</strong> off a source this panel can&apos;t connect to — behind a
          NAT or firewall, with no inbound access and no panel installed. The source only needs to
          reach the public internet. This panel mints two upload links; you paste a one-line command
          on the source; it pushes the dump to your S3 bucket; then you click Start here to import and
          verify.
        </p>
        <Callout tone="info">
          This mode requires S3 configured on this panel — it&apos;s the bucket the source uploads to.
          If you <em>can</em> reach the source directly, the “One database” direct pull is simpler and
          needs no S3.
        </Callout>
        <DropoffForm onCreated={setCreated} />
      </Card>
    </div>
  );
}

// DropoffResumeList surfaces active drop-off sessions so a minted-but-not-started
// link is recoverable after a browser reload/tab close — the code lives only in
// transient React state and the push (the expensive, hard-to-repeat part) may
// already be done, so without this the operator would be stranded until the expiry
// sweep. It shows only the safe status view (no URLs/command); Resume re-attaches
// the progress view by code for Start/Cancel/live status.
function DropoffResumeList({ onResume }: { onResume: (s: DropoffSession) => void }) {
  // Poll so a session that finishes uploading (or is started/cancelled in another
  // tab) updates here without a manual refresh.
  const { data, error } = usePolling<DropoffSession[]>((signal) => api.listDropoffs(signal), 5000);
  const sessions = data ?? [];
  // A missing/410 list endpoint or no S3 shouldn't nag on the create screen; the
  // form below surfaces the real S3 error. Only render when there's something to
  // resume.
  if (error || sessions.length === 0) return null;

  return (
    <Card title="Pending drop-offs">
      <p className="text-muted-foreground">
        These links are still open. If you minted one, ran the push on the source, then reloaded this
        page, resume it here to import — you don&apos;t need to re-run the push.
      </p>
      <div className="mt-3 flex flex-col gap-2">
        {sessions.map((s) => (
          <div
            key={s.code}
            className="flex flex-wrap items-center gap-3 rounded-md border bg-muted/30 p-3"
          >
            <DropStatusBadge status={s.status} />
            <span className="text-sm">
              into <code>{s.target_database}</code>
              {s.overwrite ? " (replace)" : ""}
            </span>
            <ResumeExpiry expiresAt={s.expires_at} />
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="ml-auto"
              onClick={() => onResume(s)}
            >
              {s.status === "importing" ? "View" : "Resume"}
            </Button>
          </div>
        ))}
      </div>
    </Card>
  );
}

/** Compact expiry countdown for a row in the pending-drop-offs list. */
function ResumeExpiry({ expiresAt }: { expiresAt: string }) {
  const msLeft = useCountdown(expiresAt);
  return (
    <span className="text-sm text-muted-foreground" aria-live="polite">
      {msLeft <= 0 ? (
        "expired"
      ) : (
        <>
          expires in <span className="font-mono">{duration(Math.floor(msLeft / 1000))}</span>
        </>
      )}
    </span>
  );
}

function DropoffForm({ onCreated }: { onCreated: (r: CreateDropoffResult) => void }) {
  const [target, setTarget] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const targetName = target.trim();
  const ready = targetName !== "";
  const overwriteMatches = confirm.trim() === targetName;

  const create = async () => {
    setBusy(true);
    setError(null);
    try {
      const res = await api.createDropoff({
        target_database: targetName,
        overwrite,
        confirm: overwrite ? confirm.trim() : "",
      });
      toast.success("Drop-off link created.");
      setConfirmOpen(false);
      setConfirm("");
      onCreated(res);
    } catch (err) {
      const ae = asApiError(err);
      // The mint-time pre-check refuses a target that already exists and is
      // non-empty unless overwrite is authorized. Instead of dead-ending, flip to
      // the overwrite + typed-confirm flow so the operator authorizes it NOW (free,
      // before the source runs the expensive, hard-to-repeat upload) rather than
      // discovering it only after the dump has landed.
      if (ae.code === "safety" && !overwrite) {
        setOverwrite(true);
        setConfirmOpen(true);
        setError(null);
        toast.info(ae.hint || ae.message);
      } else {
        setError(ae);
      }
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (overwrite) setConfirmOpen(true);
    else void create();
  };

  return (
    <form className="mt-3 flex max-w-xl flex-col gap-5" onSubmit={submit}>
      {error ? <S3OrError error={error} title="Drop-off link needs S3" /> : null}
      <Field>
        <FieldLabel htmlFor="drop-target">Name on this server</FieldLabel>
        <Input
          id="drop-target"
          value={target}
          placeholder="myapp"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => setTarget(e.target.value)}
        />
        <FieldDescription>The name the imported database will have on this server.</FieldDescription>
      </Field>
      <Field orientation="horizontal">
        <Checkbox
          id="drop-overwrite"
          checked={overwrite}
          onCheckedChange={(c) => setOverwrite(c === true)}
        />
        <FieldLabel htmlFor="drop-overwrite" className="font-normal">
          Replace <code>{targetName || "the target"}</code> if it already exists (destructive)
        </FieldLabel>
      </Field>
      <Button type="submit" className="self-start" disabled={!ready || busy}>
        {busy ? (
          <>
            <InlineSpinner data-icon="inline-start" />
            Creating…
          </>
        ) : overwrite ? (
          "Continue…"
        ) : (
          "Create drop-off link"
        )}
      </Button>

      <Modal
        open={confirmOpen}
        title="Confirm overwrite"
        tone="danger"
        width="sm"
        onClose={busy ? () => undefined : () => setConfirmOpen(false)}
        footer={
          <>
            <Button type="button" variant="outline" onClick={() => setConfirmOpen(false)} disabled={busy}>
              Back
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={create}
              disabled={busy || !overwriteMatches}
            >
              {busy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Creating…
                </>
              ) : (
                "Overwrite & create link"
              )}
            </Button>
          </>
        }
      >
        <Callout tone="danger" title="This will drop the existing database">
          When you click Start after the upload, <strong>{targetName}</strong> on this server will be
          dropped and recreated from the source. This cannot be undone.
        </Callout>
        <Field className="mt-4" data-invalid={confirm.length > 0 && !overwriteMatches}>
          <FieldLabel htmlFor="drop-confirm">
            Type <code>{targetName}</code> to confirm
          </FieldLabel>
          <Input
            id="drop-confirm"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={targetName}
            aria-invalid={confirm.length > 0 && !overwriteMatches}
            aria-describedby={confirm.length > 0 && !overwriteMatches ? "drop-confirm-err" : undefined}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {confirm.length > 0 && !overwriteMatches ? (
            <FieldError id="drop-confirm-err">
              Must match <code>{targetName}</code> exactly.
            </FieldError>
          ) : null}
        </Field>
      </Modal>
    </form>
  );
}

// Statuses where the drop-off itself is finished but no import job exists to hand
// off to (cancelled/expired before Start) — the panel shows a terminal callout.
function isDropTerminal(status: DropoffStatus): boolean {
  return status === "failed" || status === "expired";
}

// The push command + URLs are minted ONCE and never re-served, so a resumed
// session (re-attached from the pending list after a reload) has no commands.
type DropoffCommands = { docker: string; native: string };

export function DropoffProgress({
  code,
  target,
  overwrite,
  expiresAt: initialExpiresAt,
  commands,
  onReset,
}: {
  code: string;
  target: string;
  overwrite: boolean;
  expiresAt: string;
  /** Present only for a freshly minted session; absent when resumed from the list. */
  commands?: DropoffCommands;
  onReset: () => void;
}) {
  const { data: drop, error } = usePolling<DropoffSession>(
    (signal) => api.getDropoff(code, signal),
    2500,
  );
  const [startedId, setStartedId] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const [cancelOpen, setCancelOpen] = useState(false);
  const [cancelBusy, setCancelBusy] = useState(false);
  // Start-time re-affirmation of a destructive overwrite. The typed-name confirm at
  // mint authorized the drop, but the actual DROP DATABASE runs only here, when
  // Start is clicked — potentially close to the TTL later. Re-affirm so a database
  // that looked disposable at mint isn't silently dropped much later.
  const [startConfirmOpen, setStartConfirmOpen] = useState(false);
  const [startConfirm, setStartConfirm] = useState("");
  const startConfirmMatches = startConfirm.trim() === target;

  const status: DropoffStatus = drop?.status ?? "waiting_for_upload";

  // Retry re-runs the import from the dump the panel KEEPS in S3 on failure, so a
  // transient restore/transport error doesn't force a full re-push from the
  // source the operator can barely reach. startDropoff records a fresh migration
  // row and re-points the live poller at it.
  const retryImport = async () => {
    try {
      const res = await api.startDropoff(code);
      toast.success("Retrying the import.");
      setStartedId(res.id);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not retry the import.");
    }
  };

  // Once an import job exists, the existing per-job poller owns the live
  // progress + verification view — identical to every other migration mode.
  const migrationId = startedId ?? drop?.migration_id ?? null;
  if (migrationId != null) {
    // Offer "Retry import" only while the dump is still in S3 (the session hasn't
    // expired); once expired the sweep has deleted it and a re-push is required.
    return (
      <DirectJobProgress
        id={migrationId}
        onReset={onReset}
        onRetry={status === "expired" ? undefined : retryImport}
      />
    );
  }

  const expiresAt = drop?.expires_at ?? initialExpiresAt;
  const terminal = isDropTerminal(status);

  const start = async () => {
    setStarting(true);
    try {
      const res = await api.startDropoff(code);
      toast.success("Import started.");
      setStartConfirmOpen(false);
      setStartConfirm("");
      setStartedId(res.id);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not start the import.");
    } finally {
      setStarting(false);
    }
  };

  // A non-destructive import starts immediately; a destructive overwrite must be
  // re-affirmed by name first, because the DROP executes now, not at mint.
  const onStartClick = () => {
    if (overwrite) setStartConfirmOpen(true);
    else void start();
  };

  const cancel = async () => {
    setCancelBusy(true);
    try {
      await api.cancelDropoff(code);
      toast.info("Drop-off cancelled.");
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
      title="Drop-off link ready"
      actions={
        terminal ? (
          <Button type="button" variant="outline" size="sm" onClick={onReset}>
            Start another
          </Button>
        ) : (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="text-destructive hover:text-destructive"
            onClick={() => setCancelOpen(true)}
          >
            Cancel
          </Button>
        )
      }
    >
      {error && !drop ? <S3OrError error={error} title="Drop-off link needs S3" /> : null}
      {/* Poll failed while a live link is on screen — surface the stall so a
          frozen "waiting" badge can't look like it's still checking. */}
      {error && drop && !terminal ? <StaleBanner error={error} /> : null}

      <DropoffStatusRow status={status} expiresAt={expiresAt} byteSize={drop?.byte_size ?? 0} />

      {status === "expired" ? (
        <Callout tone="warn" title="This drop-off link expired">
          The upload links are no longer valid. Start a new drop-off to try again.
        </Callout>
      ) : status === "failed" ? (
        <Callout tone="danger" title="Drop-off failed">
          {drop?.error || "The drop-off could not complete."}
        </Callout>
      ) : commands ? (
        <>
          <p className="mt-1 text-sm text-muted-foreground">
            On the <strong>source</strong> server — the one this panel can&apos;t reach — paste this
            into a shell. It needs <code>curl</code> and either Docker or <code>pg_dump</code>.
          </p>
          <DropoffCommand commands={commands} />
          <DropoffSteps target={target} />
          <Callout tone="warn" title="Treat these links like a password">
            The command carries two upload links anyone could use to write to your bucket until they
            expire. Don&apos;t share them. They ride in environment variables (
            <code>INDIEPG_DUMP_URL</code> / <code>INDIEPG_META_URL</code>), not arguments, so they
            stay out of the source&apos;s <code>ps</code> listing — the same protection the database
            password gets (read from <code>PGPASSWORD</code> or a prompt, never a flag, never sent to
            this panel).
          </Callout>
        </>
      ) : (
        // Resumed from the pending list: the one-time command is no longer shown
        // (served once, by design). The upload may already be done — if so, Start
        // below; if you still need to push, cancel and create a fresh link.
        <Callout tone="info" title="Resumed an open drop-off">
          {status === "uploaded"
            ? "The source finished uploading — click Start import below to restore and verify."
            : "Waiting for the source to finish uploading. The one-time push command isn’t shown again for security — if you still need to run it on the source, cancel this and create a new link."}
        </Callout>
      )}

      {!terminal ? (
        <div className="mt-4 flex flex-wrap items-center gap-3">
          <Button
            type="button"
            onClick={onStartClick}
            disabled={status !== "uploaded" || starting}
          >
            {starting ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Starting…
              </>
            ) : (
              "Start import"
            )}
          </Button>
          {status !== "uploaded" ? (
            <span className="text-sm text-muted-foreground">
              Enabled once the source finishes uploading.
            </span>
          ) : (
            <span className="text-sm text-muted-foreground">
              Imports into <code>{target}</code>
              {overwrite ? " (replacing the existing database)" : ""}, then verifies row counts.
            </span>
          )}
        </div>
      ) : null}

      <Modal
        open={startConfirmOpen}
        title="Replace the existing database?"
        tone="danger"
        width="sm"
        onClose={starting ? () => undefined : () => setStartConfirmOpen(false)}
        footer={
          <>
            <Button
              type="button"
              variant="outline"
              onClick={() => setStartConfirmOpen(false)}
              disabled={starting}
            >
              Back
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={start}
              disabled={starting || !startConfirmMatches}
            >
              {starting ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Starting…
                </>
              ) : (
                "Drop & import"
              )}
            </Button>
          </>
        }
      >
        <Callout tone="danger" title="This drops the database now">
          Starting the import will drop <strong>{target}</strong> on this server and recreate it from
          the upload. You confirmed this when you created the link; confirm again since the drop
          happens now. This cannot be undone.
        </Callout>
        <Field className="mt-4" data-invalid={startConfirm.length > 0 && !startConfirmMatches}>
          <FieldLabel htmlFor="drop-start-confirm">
            Type <code>{target}</code> to confirm
          </FieldLabel>
          <Input
            id="drop-start-confirm"
            value={startConfirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={target}
            aria-invalid={startConfirm.length > 0 && !startConfirmMatches}
            onChange={(e) => setStartConfirm(e.target.value)}
          />
          {startConfirm.length > 0 && !startConfirmMatches ? (
            <FieldError>
              Must match <code>{target}</code> exactly.
            </FieldError>
          ) : null}
        </Field>
      </Modal>

      <Modal
        open={cancelOpen}
        title="Cancel this drop-off?"
        tone="danger"
        width="sm"
        onClose={() => setCancelOpen(false)}
        footer={
          <>
            <Button type="button" variant="outline" onClick={() => setCancelOpen(false)} disabled={cancelBusy}>
              Keep going
            </Button>
            <Button type="button" variant="destructive" onClick={cancel} disabled={cancelBusy}>
              {cancelBusy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Cancelling…
                </>
              ) : (
                "Cancel drop-off"
              )}
            </Button>
          </>
        }
      >
        <p>
          The upload links are invalidated and any uploaded dump is deleted from your bucket. Nothing
          on this server changes.
        </p>
      </Modal>
    </Card>
  );
}

/** Live state badge + uploaded size + an expiry countdown for a drop-off link. */
function DropoffStatusRow({
  status,
  expiresAt,
  byteSize,
}: {
  status: DropoffStatus;
  expiresAt: string;
  byteSize: number;
}) {
  const msLeft = useCountdown(expiresAt);
  const expired = status === "expired" || msLeft <= 0;
  return (
    <div className="mb-3 flex flex-wrap items-center gap-3 rounded-md bg-muted p-4">
      <DropStatusBadge status={status} />
      {byteSize > 0 ? (
        <span className="text-sm text-muted-foreground">{bytes(byteSize)} uploaded</span>
      ) : null}
      <span className="ml-auto text-sm text-muted-foreground" aria-live="polite">
        {expired ? (
          "Link expired"
        ) : (
          <>
            Expires in <span className="font-mono">{duration(Math.floor(msLeft / 1000))}</span>
          </>
        )}
      </span>
    </div>
  );
}

function DropStatusBadge({ status }: { status: DropoffStatus }) {
  switch (status) {
    case "uploaded":
      return <Badge tone="ok">✓ Uploaded — ready to import</Badge>;
    case "importing":
      return <Badge tone="info">Importing…</Badge>;
    case "completed":
      return <Badge tone="ok">Completed</Badge>;
    case "failed":
      return <Badge tone="danger">Failed</Badge>;
    case "expired":
      return <Badge tone="warn">Expired</Badge>;
    default:
      return <Badge tone="info">Waiting for upload…</Badge>;
  }
}

/** The docker/native command toggle + copy block. The presigned URLs live in
 *  these commands and are sensitive, but must be copy-pasteable, so they're shown
 *  in full with a Copy button (the callout above frames them as a password). */
function DropoffCommand({ commands }: { commands: DropoffCommands }) {
  const [variant, setVariant] = useState<"docker" | "native">("docker");
  const command = variant === "docker" ? commands.docker : commands.native;
  return (
    <div className="mt-3 flex flex-col gap-2">
      <Tabs value={variant} onValueChange={(v) => setVariant(v as "docker" | "native")}>
        <TabsList>
          <TabsTrigger value="docker">Docker container</TabsTrigger>
          <TabsTrigger value="native">Native (host / port)</TabsTrigger>
        </TabsList>
      </Tabs>
      <CommandBlock command={command} />
      <p className="text-sm text-muted-foreground">
        {variant === "docker" ? (
          <>
            Replace <code>CONTAINER</code> with the Postgres container name and{" "}
            <code>DBNAME</code> with the database to copy. If the container&apos;s Postgres
            superuser isn&apos;t <code>postgres</code>, append <code>--user NAME</code>.
          </>
        ) : (
          <>
            Replace <code>SOURCE_HOST</code>, <code>POSTGRES_USER</code> and <code>DBNAME</code>. Set
            the password first, e.g. <code>export PGPASSWORD=…</code> — it&apos;s read from the
            environment or a terminal prompt, never a flag.
          </>
        )}
      </p>
    </div>
  );
}

/** A single-command copy block, matching the Extensions "exactly what ran" view. */
function CommandBlock({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Couldn't copy to the clipboard.");
    }
  };
  return (
    <div className="relative">
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="absolute right-2 top-2 h-7"
        onClick={copy}
      >
        {copied ? "Copied" : "Copy"}
      </Button>
      <pre className="overflow-x-auto whitespace-pre-wrap break-all rounded-md border bg-muted/40 p-3 pr-20 text-[13px] leading-relaxed text-foreground">
        {command}
      </pre>
    </div>
  );
}

/** Numbered "what to do next" guidance shown until the upload lands. */
function DropoffSteps({ target }: { target: string }) {
  const steps = [
    "On the source server, paste the command above. It runs pg_dump, uploads the dump to your S3 bucket, then uploads a small checksum + row-count manifest.",
    "Watch the badge above flip to “Uploaded” when the push finishes — this page checks every few seconds.",
  ];
  return (
    <>
      <Callout tone="info" title="Keep the source idle during the push">
        The push captures each table’s row count just after the dump and freezes it for verification.
        If the source takes writes mid-push, the counts won’t match and the import will fail — and
        because the manifest is frozen, retrying can’t fix it (you’d re-run the whole push). Pause
        writes or put the source read-only first.
      </Callout>
      <ol className="mt-4 flex flex-col gap-2">
      {steps.map((text, i) => (
        <li key={i} className="flex items-start gap-3 text-sm">
          <span
            aria-hidden="true"
            className="inline-grid size-6 shrink-0 place-items-center rounded-full border-2 border-input text-xs font-bold text-muted-foreground"
          >
            {i + 1}
          </span>
          <span>{text}</span>
        </li>
      ))}
      <li className="flex items-start gap-3 text-sm">
        <span
          aria-hidden="true"
          className="inline-grid size-6 shrink-0 place-items-center rounded-full border-2 border-input text-xs font-bold text-muted-foreground"
        >
          3
        </span>
        <span>
          Click <strong>Start import</strong>. This panel streams the dump from S3, verifies its
          SHA-256 checksum, restores into <code>{target}</code>, and compares row counts table by
          table — only reporting success when every table matches.
        </span>
      </li>
      </ol>
      <p className="mt-3 text-sm text-muted-foreground">
        The push stages the whole dump on the source&apos;s disk before uploading. If the source&apos;s{" "}
        <code>/tmp</code> is small or RAM-backed (tmpfs), point it at a roomier disk-backed directory
        first, e.g. <code>export INDIEPG_TMPDIR=/var/tmp</code>.
      </p>
    </>
  );
}

/** Milliseconds remaining until `iso`, ticking once a second (never negative). */
function useCountdown(iso: string): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);
  return Math.max(0, new Date(iso).getTime() - now);
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

export function MigrationHistory() {
  const { data, error } = usePolling<MigrationRecord[]>((signal) => api.listMigrations(signal), 5000);

  return (
    <Card title="Recent migrations">
      {error && !data ? <ErrorNotice error={error} /> : null}
      {/* Poll failed after the list already loaded — keep it visible but say the
          refresh stalled, rather than silently showing a possibly-stale log. */}
      {error && data ? <StaleBanner error={error} /> : null}
      {!data ? (
        // First load: a spinner OR the error above, never both (a spinner beside
        // an error implies progress that isn't happening).
        error ? null : <Spinner label="Loading…" />
      ) : data.length === 0 ? (
        <EmptyState title="No migrations yet" hint="Started migrations appear here with live status." />
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>Mode</TableHead>
              <TableHead>Source / target</TableHead>
              <TableHead>Status</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data.map((m) => (
              <TableRow key={m.id}>
                <TableCell title={dateTime(m.created_at)}>{ago(m.created_at)}</TableCell>
                <TableCell>{MODE_LABELS[m.mode] ?? m.mode}</TableCell>
                <TableCell className="text-sm text-muted-foreground">
                  {m.source_summary || m.target_database || `code ${m.code}`}
                </TableCell>
                <TableCell>
                  <StatusBadge status={m.status} phase={m.phase} />
                  {m.status === "failed" && m.error ? (
                    <div className="text-sm text-muted-foreground">{m.error}</div>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </Card>
  );
}

const MODE_LABELS: Record<MigrationMode, string> = {
  "single-db": "One database",
  cluster: "Whole cluster",
  "ssh-less": "Cross-panel",
  "drop-off": "Drop-off link",
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
        <div className="text-muted-foreground">Source and target row counts are identical.</div>
      </Callout>
    );
  }
  return (
    <Callout tone="warn" title="Row counts do not match">
      <Table aria-label="Row count mismatches by table">
        <TableHeader>
          <TableRow>
            <TableHead>Table</TableHead>
            <TableHead>Source</TableHead>
            <TableHead>Target</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {v.diffs.map((d) => (
            <TableRow key={d.table}>
              <TableCell>{d.table}</TableCell>
              <TableCell>{count(d.source)}</TableCell>
              <TableCell>{count(d.target)}</TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Callout>
  );
}

/** Renders the honest "requires S3" error as a helpful callout (with a pointer
 *  to Settings / direct pull), and any other error as the standard notice. The
 *  title is overridable so each S3-dependent mode can name itself. */
function S3OrError({
  error,
  title = "Cross-panel migration needs S3",
}: {
  error: ApiError;
  title?: string;
}) {
  const aboutS3 = /S3/i.test(error.message) || /S3/i.test(error.hint ?? "");
  if (aboutS3) {
    return (
      <Callout tone="warn" title={title}>
        {error.hint || "Configure an S3 backup target in Settings, or use the Direct pull modes which need no S3."}
      </Callout>
    );
  }
  return <ErrorNotice error={error} />;
}

function asApiError(err: unknown): ApiError {
  return err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) });
}
