// Backups: history table (size / duration / repo-size), run-now, restore-test,
// and a guarded restore flow (typed-name confirm equal to the stanza name).

import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "@/api/client";
import { ago, bytes, dateTime, duration, millis } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
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
  ResultBadge,
  Spinner,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldError,
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
import type {
  BackupHistory,
  BackupRecord,
  BackupType,
  ConfigResponse,
  RecoveryTarget,
  RestoreTestRecord,
  S3Target,
} from "@/api/types";

export function Backups() {
  const history = useAsync<BackupHistory>(() => api.backupHistory(), []);
  const config = useAsync<ConfigResponse>(() => api.getConfig(), []);

  // Where backups actually land: local (this server) when no S3 bucket/endpoint
  // is set, otherwise the configured bucket. Stays "loading" until config arrives
  // so the copy never flashes the wrong destination.
  const destination = backupDestination(config.data?.config.backup, config.data != null);
  const isLocal = destination.kind === "local";

  const [runType, setRunType] = useState<BackupType | null>(null);
  const [runBusy, setRunBusy] = useState(false);
  const [testBusy, setTestBusy] = useState(false);
  const [deepOpen, setDeepOpen] = useState(false);
  const [deepBusy, setDeepBusy] = useState(false);
  const [restoreOpen, setRestoreOpen] = useState(false);

  const runBackup = async () => {
    if (!runType) return;
    setRunBusy(true);
    try {
      const res = await api.runBackup({ type: runType });
      toast.success(res.message || "Backup started.");
      setRunType(null);
      history.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Backup failed to start.");
    } finally {
      setRunBusy(false);
    }
  };

  const runRestoreTest = async () => {
    setTestBusy(true);
    try {
      const res = await api.runRestoreTest();
      toast.success(res.message || "Restore test started.");
      history.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Restore test failed to start.");
    } finally {
      setTestBusy(false);
    }
  };

  const runDeepRestoreTest = async () => {
    setDeepBusy(true);
    try {
      const res = await api.runRestoreTest({ deep: true });
      toast.success(res.message || "Deep restore test started.");
      setDeepOpen(false);
      history.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Deep restore test failed to start.");
    } finally {
      setDeepBusy(false);
    }
  };

  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Backups"
        description={
          <>
            Back up your database and check that your backups actually work.
            {destination.kind !== "loading" ? (
              <>
                {" "}
                <Badge tone={destination.kind === "local" ? "warn" : "ok"}>
                  {destination.kind === "local"
                    ? "Stored on this server"
                    : `Stored in S3 · ${destination.bucketName}`}
                </Badge>
              </>
            ) : null}
          </>
        }
        actions={
          <div className="flex flex-wrap gap-2">
            <Button variant="destructive" onClick={() => setRestoreOpen(true)}>
              Restore…
            </Button>
            <Button
              variant="outline"
              onClick={runRestoreTest}
              disabled={testBusy}
              title="Verify the backup repository is intact (fast, read-only)"
            >
              {testBusy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Testing…
                </>
              ) : (
                "Test a restore"
              )}
            </Button>
            <Button
              variant="outline"
              onClick={() => setDeepOpen(true)}
              disabled={deepBusy}
              title="Restore the latest backup into a throwaway copy and boot it (slower, needs disk headroom)"
            >
              {deepBusy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Testing…
                </>
              ) : (
                "Deep restore test"
              )}
            </Button>
            <Button
              onClick={() => setRunType("incr")}
              title="Run an incremental backup (only changed files)"
            >
              Back up now
            </Button>
            <Button variant="outline" onClick={() => setRunType("full")} title="Run a full backup">
              Full backup
            </Button>
          </div>
        }
      />

      {history.data ? <BackupStatusSummary backups={history.data.backups} /> : null}

      <LocalBackupWarning destination={destination} />

      <Callout tone="info" title="How backups work here">
        {destination.kind === "s3" ? (
          <>
            Backups are stored in your S3 bucket
            {destination.bucketName ? (
              <>
                {" "}
                (<code>{destination.bucketName}</code>)
              </>
            ) : null}{" "}
            using pgBackRest.{" "}
          </>
        ) : null}
        A <strong>full</strong> backup copies everything; an{" "}
        <strong>incremental</strong> backup only copies what changed since the last one, so it&apos;s
        faster and smaller. <strong>Test a restore</strong> verifies your backup repository is intact
        and recoverable — it checks that every backup and WAL file is present with matching checksums,
        without touching your live database.
      </Callout>

      {history.loading ? (
        <Spinner label="Loading backup history…" />
      ) : history.error ? (
        <ErrorNotice error={history.error} />
      ) : history.data ? (
        <>
          <Card title="Backup history">
            {history.data.backups.length === 0 ? (
              <EmptyState
                title="No backups yet"
                hint="Run your first backup to protect your data."
              />
            ) : (
              <BackupTable backups={history.data.backups} />
            )}
          </Card>

          <Card title="Restore tests">
            <RestoreTestStatus tests={history.data.restore_tests} />
            {history.data.restore_tests.length === 0 ? (
              <EmptyState
                title="No restore tests yet"
                hint="A restore test answers the question: do my backups actually work?"
              />
            ) : (
              <RestoreTestTable tests={history.data.restore_tests} />
            )}
          </Card>
        </>
      ) : null}

      {/* Run backup confirmation */}
      <ConfirmDialog
        open={runType !== null}
        title={runType === "full" ? "Run a full backup?" : "Run an incremental backup?"}
        message={
          runType === "full"
            ? isLocal
              ? "This copies the entire database to local storage on this server. It can take a while and use more storage, but it's a complete restore point."
              : "This copies the entire database to your bucket. It can take a while and use more storage, but it's a complete restore point."
            : "This copies only what changed since the last backup. It's fast and small."
        }
        confirmLabel="Start backup"
        busy={runBusy}
        onConfirm={runBackup}
        onCancel={() => setRunType(null)}
      />

      {/* Deep restore-test confirmation */}
      <DeepRestoreTestConfirm
        open={deepOpen}
        busy={deepBusy}
        onConfirm={runDeepRestoreTest}
        onCancel={() => setDeepOpen(false)}
      />

      {restoreOpen ? (
        <RestoreModal
          onClose={() => setRestoreOpen(false)}
          onDone={() => {
            setRestoreOpen(false);
            history.reload();
          }}
        />
      ) : null}
    </div>
  );
}

// --- Backup destination (local-on-this-server vs off-host S3) ---------------

/**
 * BackupDestination is where backups physically land:
 *
 *  - loading: config hasn't arrived yet — render neutral copy, never guess.
 *  - local:   no S3 endpoint/bucket set, so pgBackRest writes to this same
 *             server — survives bad drops/migrations but NOT disk or host loss.
 *  - s3:      an off-host S3-compatible bucket is configured.
 */
export type BackupDestination =
  | { kind: "loading" }
  | { kind: "local" }
  | { kind: "s3"; bucketName: string };

/**
 * backupDestination classifies where backups land from the saved S3 target. A
 * target counts as configured (off-host) once either an endpoint or a bucket is
 * set, so the local-only warning only shows when nothing at all points off this
 * server. `loaded` is the "config response has arrived" flag; until then we report
 * loading rather than flashing "stored on this server" for a box that actually has
 * S3 configured.
 *
 * We trim before testing for emptiness. The Settings form already trims on save
 * (so a whitespace-only value can't normally be persisted), and the Go server's
 * authoritative `remoteTargetConfigured` compares against "" without trimming —
 * trimming here is a deliberate, belt-and-suspenders UX choice so that a target
 * that is only whitespace (e.g. a hand-edited DB value) still surfaces the
 * local-only nudge rather than silently claiming an off-host destination exists.
 */
export function backupDestination(
  backup: S3Target | undefined,
  loaded: boolean,
): BackupDestination {
  if (!loaded) return { kind: "loading" };
  const bucket = backup?.bucket?.trim() ?? "";
  const endpoint = backup?.endpoint?.trim() ?? "";
  if (!bucket && !endpoint) return { kind: "local" };
  return { kind: "s3", bucketName: bucket || endpoint };
}

/**
 * LocalBackupWarning is the panel's primary "move your backups off-host" nudge:
 * it shouts (warn tone) that backups live on this same server and won't survive
 * disk or host loss, and links to Settings to connect S3. Rendered only when the
 * destination is local; kept as its own component so the message is covered by a
 * test and can't silently regress as the page evolves.
 */
export function LocalBackupWarning({ destination }: { destination: BackupDestination }) {
  if (destination.kind !== "local") return null;
  return (
    <Callout tone="warn" title="Your backups are on this server — move them to S3">
      Backups are being written to <code>/var/lib/pgbackrest</code> on this same
      machine. That covers accidental drops and bad migrations, but{" "}
      <strong>not disk failure or losing the server</strong> — if this box goes, the
      backups go with it. <Link to="/settings">Set up an S3 bucket in Settings</Link>{" "}
      for real off-server backups. You can switch anytime and it takes effect
      immediately — new backups go to S3, and the local ones stay on disk.
    </Callout>
  );
}

// --- Backup freshness (the at-a-glance "are my backups good?" answer) -------

const SUCCESS_RESULTS = new Set(["success", "ok", "completed", "pass"]);
const FAILURE_RESULTS = new Set(["fail", "failed", "error"]);

/**
 * BackupFreshness is the at-a-glance state of "can I restore right now, and how
 * fresh is my last good backup?" derived from the run history.
 *
 *  - none:       no backups have ever run — nothing to restore from.
 *  - good:       the most recent run succeeded.
 *  - stale:      a good backup exists, but the MOST RECENT attempt failed — so
 *                everything written since the last good backup is unprotected.
 *  - never-good: backups have run but not one has ever succeeded.
 */
export type BackupFreshness =
  | { kind: "none" }
  | { kind: "good"; good: BackupRecord }
  | { kind: "stale"; good: BackupRecord }
  | { kind: "never-good" };

/**
 * backupFreshness classifies the history. It relies on the server contract that
 * `backups` is newest-first (`ORDER BY started_at DESC`), so backups[0] is the
 * most recent run and the first success encountered is the most recent good one.
 */
export function backupFreshness(backups: BackupRecord[]): BackupFreshness {
  if (backups.length === 0) return { kind: "none" };
  const good = backups.find((b) => SUCCESS_RESULTS.has(b.result.toLowerCase()));
  if (!good) return { kind: "never-good" };
  if (FAILURE_RESULTS.has(backups[0].result.toLowerCase())) return { kind: "stale", good };
  return { kind: "good", good };
}

/** When a backup is considered "done": completion time, falling back to start. */
function backupWhen(b: BackupRecord): string {
  return b.stopped_at || b.started_at;
}

/**
 * BackupStatusSummary is the prominent banner at the top of the Backups page: it
 * states, in one line, whether the data is protected and how long ago the last
 * good backup was — and shouts loudly (danger tone) when it is not, so an indie
 * hacker never has to read the history table to learn their data is at risk.
 */
export function BackupStatusSummary({ backups }: { backups: BackupRecord[] }) {
  const state = backupFreshness(backups);

  if (state.kind === "none") {
    return (
      <Callout tone="danger" title="No backups yet — your data is not protected">
        You haven&apos;t made a backup. If something goes wrong now, there is nothing to restore
        from. Run your first backup to protect your data.
      </Callout>
    );
  }

  if (state.kind === "never-good") {
    return (
      <Callout tone="danger" title="No working backup yet — your data is not protected">
        Every backup attempt so far has failed, so there is nothing to restore from. Fix the most
        recent error below and run a backup until one succeeds.
      </Callout>
    );
  }

  const when = backupWhen(state.good);

  if (state.kind === "stale") {
    return (
      <Callout tone="danger" title="Your most recent backup failed">
        Your last <strong>working</strong> backup was <strong>{ago(when)}</strong> ({dateTime(when)}
        ). Everything written since then is unprotected until a new backup succeeds — check the
        failure below and run another.
      </Callout>
    );
  }

  return (
    <Callout tone="ok" title="Your data is backed up">
      Last good backup <strong>{ago(when)}</strong> ({dateTime(when)}) ·{" "}
      <Badge tone="info">{state.good.backup_type}</Badge>
    </Callout>
  );
}

// --- Restore verification (have my backups been proven recoverable?) --------

/**
 * RestoreVerification is the at-a-glance answer to "have my backups actually
 * been test-restored, and did it work?" — a stronger guarantee than "a backup
 * file exists", since a backup you have never restored is one you do not know
 * works. Derived from the restore-test history (newest-first).
 *
 *  - never:        no restore test has ever run — recoverability is unverified.
 *  - passed:       the most recent restore test passed.
 *  - failed:       a test has passed before, but the MOST RECENT one failed — so
 *                  recoverability is in doubt right now.
 *  - never-passed: restore tests have run but not one has ever passed.
 */
export type RestoreVerification =
  | { kind: "never" }
  | { kind: "passed"; passed: RestoreTestRecord }
  | { kind: "failed"; passed: RestoreTestRecord }
  | { kind: "never-passed" };

/**
 * restoreTestStatus classifies the restore-test history. It mirrors
 * backupFreshness and relies on the same server contract that records are
 * newest-first (`ORDER BY tested_at DESC`), so tests[0] is the most recent run
 * and the first pass encountered is the most recent good one. It reuses the
 * shared SUCCESS_RESULTS/FAILURE_RESULTS vocabulary so backups and restore tests
 * classify "pass"/"fail" identically.
 */
export function restoreTestStatus(tests: RestoreTestRecord[]): RestoreVerification {
  if (tests.length === 0) return { kind: "never" };
  const passed = tests.find((t) => SUCCESS_RESULTS.has(t.result.toLowerCase()));
  if (!passed) return { kind: "never-passed" };
  if (FAILURE_RESULTS.has(tests[0].result.toLowerCase())) return { kind: "failed", passed };
  return { kind: "passed", passed };
}

/**
 * RestoreTestStatus is the at-a-glance banner above the restore-test history: it
 * answers, in one line, whether the operator's backup repository has been
 * verified intact and when. The "never" state is intentionally calm (info, not a
 * call-to-action) — it states the fact without alarm, since the operator simply
 * hasn't run a verification yet ("Test a restore" at the top of the page runs
 * one). A failed/never-passed result shouts (danger), because a repository that
 * fails its integrity check is a data-loss risk the operator must see without
 * reading the table.
 */
export function RestoreTestStatus({ tests }: { tests: RestoreTestRecord[] }) {
  const state = restoreTestStatus(tests);

  if (state.kind === "never") {
    return (
      <Callout tone="info" title="Your backups haven't been test-restored yet">
        You&apos;ve confirmed backups are being made, but not that they can actually be recovered.
        A restore test is the only way to be sure — until one runs, treat recoverability as
        unverified.
      </Callout>
    );
  }

  if (state.kind === "never-passed") {
    return (
      <Callout tone="danger" title="No restore test has passed yet">
        Every restore test so far has failed, so your backups have not been proven recoverable.
        Check the most recent failure below.
      </Callout>
    );
  }

  const when = state.passed.tested_at;

  if (state.kind === "failed") {
    return (
      <Callout tone="danger" title="Your most recent restore test failed">
        The last restore test did not pass, so your backups may not be recoverable right now. The
        last test that passed was <strong>{ago(when)}</strong> ({dateTime(when)}) — check the
        failure below.
      </Callout>
    );
  }

  return (
    <Callout tone="ok" title="Your backup repository is verified intact">
      Last verified <strong>{ago(when)}</strong> ({dateTime(when)}) — every backup and WAL file was
      present with matching checksums
      {state.passed.verified_rows > 0 ? (
        <> · {state.passed.verified_rows.toLocaleString()} rows restored and verified</>
      ) : null}
      .
    </Callout>
  );
}

/**
 * DeepRestoreTestConfirm is the opt-in confirmation for the heavier "deep"
 * restore test. Unlike the default verify (read-only checksum check), a deep
 * test actually restores the latest backup into a throwaway copy, boots it, and
 * counts the rows — the strongest proof a backup is truly recoverable. The copy
 * states up front exactly what it does and its costs (runs longer, needs disk
 * headroom roughly the size of the database) so the operator confirms knowing
 * the consequences, and reassures that the live database is never touched and
 * the scratch copy is cleaned up. Kept as its own exported component so the copy
 * is covered by a test and can't silently regress.
 */
export function DeepRestoreTestConfirm({
  open,
  busy,
  onConfirm,
  onCancel,
}: {
  open: boolean;
  busy: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  return (
    <ConfirmDialog
      open={open}
      title="Run a deep restore test?"
      confirmLabel="Run deep test"
      busy={busy}
      onConfirm={onConfirm}
      onCancel={onCancel}
      message={
        <>
          A deep restore test <strong>actually restores your latest backup into a throwaway
          copy</strong>, boots it, and counts the rows — proving the backup can really be
          recovered, not just that its files are present. It is the strongest check there is.
          <br />
          <br />
          It <strong>runs longer</strong> than the regular test and needs{" "}
          <strong>free disk space</strong> (roughly the size of your database) for the temporary
          copy. Your live database is never touched, and the temporary copy is deleted when the
          test finishes. If there isn&apos;t enough disk headroom, the test refuses to run rather
          than risk filling the disk.
        </>
      }
    />
  );
}

/** Truncated, danger-tinted per-row error detail (full text in the `title`). */
function CellError({ detail }: { detail: string }) {
  return (
    <div className="mt-0.5 max-w-[220px] truncate text-xs text-destructive" title={detail}>
      {detail}
    </div>
  );
}

function BackupTable({ backups }: { backups: BackupRecord[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Label</TableHead>
          <TableHead>Type</TableHead>
          <TableHead>Started</TableHead>
          <TableHead>Duration</TableHead>
          <TableHead>Backup size</TableHead>
          <TableHead>Repo (compressed)</TableHead>
          <TableHead>WAL range</TableHead>
          <TableHead>Result</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {backups.map((b) => {
          const dur =
            b.stopped_at && b.started_at
              ? (new Date(b.stopped_at).getTime() - new Date(b.started_at).getTime()) / 1000
              : null;
          return (
            <TableRow key={b.id}>
              <TableCell className="font-medium">{b.label}</TableCell>
              <TableCell>
                <Badge tone="info">{b.backup_type}</Badge>
              </TableCell>
              <TableCell>{dateTime(b.started_at)}</TableCell>
              <TableCell>{dur != null ? duration(dur) : "—"}</TableCell>
              <TableCell>{bytes(b.size_bytes)}</TableCell>
              <TableCell>
                {bytes(b.repo_bytes)}
                {b.database_bytes > 0 ? (
                  <span className="text-xs text-muted-foreground">
                    {" "}
                    ({Math.round((1 - b.repo_bytes / b.database_bytes) * 100)}% saved)
                  </span>
                ) : null}
              </TableCell>
              <TableCell className="font-mono text-xs">
                {b.wal_start && b.wal_stop ? `${b.wal_start} → ${b.wal_stop}` : "—"}
              </TableCell>
              <TableCell>
                <ResultBadge result={b.result} />
                {b.error ? <CellError detail={b.error} /> : null}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function RestoreTestTable({ tests }: { tests: BackupHistory["restore_tests"] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Tested</TableHead>
          <TableHead>Source backup</TableHead>
          <TableHead>Verified rows</TableHead>
          <TableHead>Duration</TableHead>
          <TableHead>Result</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {tests.map((t) => (
          <TableRow key={t.id}>
            <TableCell>{dateTime(t.tested_at)}</TableCell>
            <TableCell>{t.source_label}</TableCell>
            <TableCell>{t.verified_rows.toLocaleString()}</TableCell>
            <TableCell>{millis(t.duration_ms)}</TableCell>
            <TableCell>
              <ResultBadge result={t.result} />
              {t.detail ? <CellError detail={t.detail} /> : null}
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

// --- Restore (guarded) -----------------------------------------------------

export function RestoreModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [mode, setMode] = useState<"latest" | "pitr">("latest");
  const [pointInTime, setPointInTime] = useState("");
  const [delta, setDelta] = useState(true);
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  // The server requires the typed confirmation to equal the stanza name (default
  // "main"). If the value is wrong, the server's error carries the exact
  // `expected` string, which we surface below.
  const defaultStanza = "main";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      let target: RecoveryTarget | null = null;
      if (mode === "pitr" && pointInTime) {
        target = { time: new Date(pointInTime).toISOString(), action: "promote" };
      }
      const res = await api.restore({ target, delta, confirm: confirm.trim() });
      toast.success(res.message || "Restore started.");
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
    } finally {
      setBusy(false);
    }
  };

  const expected = error?.expected ?? defaultStanza;
  const matches = confirm.trim() === expected;

  return (
    <Modal
      open
      title="Restore from a backup"
      tone="danger"
      width="md"
      onClose={busy ? () => undefined : onClose}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="restore-form" variant="destructive" disabled={busy || !matches}>
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Restoring…
              </>
            ) : (
              "Restore now"
            )}
          </Button>
        </>
      }
    >
      <Callout tone="danger" title="This replaces your current data">
        Restoring overwrites the live database with the contents of a backup. Anything written
        since that backup will be lost. The panel takes a safety snapshot first, but you should
        only do this if you understand the consequences.
      </Callout>

      <form id="restore-form" onSubmit={submit} className="mt-4 flex flex-col gap-5">
        {error ? <ErrorNotice error={error} /> : null}

        <FieldSet>
          <FieldLegend>What to restore</FieldLegend>
          <RadioGroup value={mode} onValueChange={(v) => setMode(v as "latest" | "pitr")}>
            <Field orientation="horizontal">
              <RadioGroupItem value="latest" id="restore-latest" />
              <FieldContent>
                <FieldLabel htmlFor="restore-latest">Latest backup</FieldLabel>
                <FieldDescription>Restore the most recent backup.</FieldDescription>
              </FieldContent>
            </Field>
            <Field orientation="horizontal">
              <RadioGroupItem value="pitr" id="restore-pitr" />
              <FieldContent>
                <FieldLabel htmlFor="restore-pitr">Point in time</FieldLabel>
                <FieldDescription>Recover the database to a specific moment.</FieldDescription>
              </FieldContent>
            </Field>
          </RadioGroup>
        </FieldSet>

        {mode === "pitr" ? (
          <Field>
            <FieldLabel htmlFor="restore-pit">Recover to</FieldLabel>
            <Input
              id="restore-pit"
              type="datetime-local"
              value={pointInTime}
              onChange={(e) => setPointInTime(e.target.value)}
              required
            />
            <FieldDescription>The database will be recovered to exactly this time.</FieldDescription>
          </Field>
        ) : null}

        <Field orientation="horizontal">
          <Checkbox
            id="restore-delta"
            checked={delta}
            onCheckedChange={(c) => setDelta(c === true)}
          />
          <FieldContent>
            <FieldLabel htmlFor="restore-delta">Fast restore (only re-copy changed files)</FieldLabel>
            <FieldDescription>Recommended; turn off to fully wipe and rebuild.</FieldDescription>
          </FieldContent>
        </Field>

        <Field data-invalid={confirm.length > 0 && !matches}>
          <FieldLabel htmlFor="restore-confirm">
            Type <code>{expected}</code> to confirm this overwrite
          </FieldLabel>
          <Input
            id="restore-confirm"
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={expected}
            aria-invalid={confirm.length > 0 && !matches}
            aria-describedby={confirm.length > 0 && !matches ? "restore-confirm-err" : undefined}
            onChange={(e) => setConfirm(e.target.value)}
          />
          {confirm.length > 0 && !matches ? (
            <FieldError id="restore-confirm-err">
              Must match <code>{expected}</code> exactly.
            </FieldError>
          ) : null}
        </Field>
      </form>
    </Modal>
  );
}
