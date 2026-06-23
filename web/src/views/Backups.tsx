// Backups: history table (size / duration / repo-size), run-now, restore-test,
// and a guarded restore flow (typed-name confirm equal to the stanza name).

import { useState, type FormEvent } from "react";
import { Link } from "react-router-dom";
import { ApiError, api } from "@/api/client";
import { ago, bytes, dateTime, duration, millis } from "@/lib/format";
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
  ResultBadge,
  Spinner,
} from "@/components/ui";
import type {
  BackupHistory,
  BackupRecord,
  BackupType,
  ConfigResponse,
  RecoveryTarget,
} from "@/api/types";

export function Backups() {
  const toast = useToast();
  const history = useAsync<BackupHistory>(() => api.backupHistory(), []);
  const config = useAsync<ConfigResponse>(() => api.getConfig(), []);

  // Where backups actually land: local (this server) when no S3 bucket/endpoint
  // is set, otherwise the configured bucket. Undefined while config loads, so the
  // copy stays neutral until we know rather than flashing the wrong destination.
  const backup = config.data?.config.backup;
  const isLocal: boolean | undefined = config.data
    ? !backup?.bucket?.trim() && !backup?.endpoint?.trim()
    : undefined;
  const bucketName = backup?.bucket?.trim() || backup?.endpoint?.trim() || "";

  const [runType, setRunType] = useState<BackupType | null>(null);
  const [runBusy, setRunBusy] = useState(false);
  const [testBusy, setTestBusy] = useState(false);
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

  return (
    <div className="view">
      <PageHeader
        title="Backups"
        description={
          <>
            Back up your database and check that your backups actually work.
            {isLocal !== undefined ? (
              <>
                {" "}
                <Badge tone={isLocal ? "warn" : "ok"}>
                  {isLocal ? "Stored on this server" : `Stored in S3 · ${bucketName}`}
                </Badge>
              </>
            ) : null}
          </>
        }
        actions={
          <div className="btn-row">
            <button
              type="button"
              className="btn btn-danger-ghost"
              onClick={() => setRestoreOpen(true)}
            >
              Restore…
            </button>
            <button
              type="button"
              className="btn"
              onClick={runRestoreTest}
              disabled={testBusy}
            >
              {testBusy ? "Testing…" : "Test a restore"}
            </button>
            <div className="split-btn">
              <button
                type="button"
                className="btn btn-primary"
                onClick={() => setRunType("incr")}
              >
                Back up now
              </button>
              <button
                type="button"
                className="btn btn-primary split-extra"
                onClick={() => setRunType("full")}
                title="Run a full backup"
              >
                Full
              </button>
            </div>
          </div>
        }
      />

      {history.data ? <BackupStatusSummary backups={history.data.backups} /> : null}

      {isLocal ? (
        <Callout tone="warn" title="Your backups are on this server — move them to S3">
          Backups are being written to <code>/var/lib/pgbackrest</code> on this same
          machine. That covers accidental drops and bad migrations, but{" "}
          <strong>not disk failure or losing the server</strong> — if this box goes, the
          backups go with it. <Link to="/settings">Set up an S3 bucket in Settings</Link>{" "}
          for real off-server backups. You can switch anytime and it takes effect
          immediately — new backups go to S3, and the local ones stay on disk.
        </Callout>
      ) : null}

      <Callout tone="info" title="How backups work here">
        {isLocal === false ? (
          <>
            Backups are stored in your S3 bucket
            {bucketName ? (
              <>
                {" "}
                (<code>{bucketName}</code>)
              </>
            ) : null}{" "}
            using pgBackRest.{" "}
          </>
        ) : null}
        A <strong>full</strong> backup copies everything; an{" "}
        <strong>incremental</strong> backup only copies what changed since the last one, so it&apos;s
        faster and smaller. <strong>Test a restore</strong> proves your backups can actually be
        recovered — before you ever need them.
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

function BackupTable({ backups }: { backups: BackupRecord[] }) {
  return (
    <div className="table-scroll">
      <table className="data-table">
        <thead>
          <tr>
            <th>Label</th>
            <th>Type</th>
            <th>Started</th>
            <th>Duration</th>
            <th>Backup size</th>
            <th>Repo (compressed)</th>
            <th>WAL range</th>
            <th>Result</th>
          </tr>
        </thead>
        <tbody>
          {backups.map((b) => {
            const dur =
              b.stopped_at && b.started_at
                ? (new Date(b.stopped_at).getTime() - new Date(b.started_at).getTime()) / 1000
                : null;
            return (
              <tr key={b.id}>
                <td><strong>{b.label}</strong></td>
                <td><Badge tone="info">{b.backup_type}</Badge></td>
                <td>{dateTime(b.started_at)}</td>
                <td>{dur != null ? duration(dur) : "—"}</td>
                <td>{bytes(b.size_bytes)}</td>
                <td>
                  {bytes(b.repo_bytes)}
                  {b.database_bytes > 0 ? (
                    <span className="muted compression">
                      {" "}
                      ({Math.round((1 - b.repo_bytes / b.database_bytes) * 100)}% saved)
                    </span>
                  ) : null}
                </td>
                <td className="mono small">
                  {b.wal_start && b.wal_stop ? `${b.wal_start} → ${b.wal_stop}` : "—"}
                </td>
                <td>
                  <ResultBadge result={b.result} />
                  {b.error ? <div className="cell-error" title={b.error}>{b.error}</div> : null}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function RestoreTestTable({ tests }: { tests: BackupHistory["restore_tests"] }) {
  return (
    <div className="table-scroll">
      <table className="data-table">
        <thead>
          <tr>
            <th>Tested</th>
            <th>Source backup</th>
            <th>Verified rows</th>
            <th>Duration</th>
            <th>Result</th>
          </tr>
        </thead>
        <tbody>
          {tests.map((t) => (
            <tr key={t.id}>
              <td>{dateTime(t.tested_at)}</td>
              <td>{t.source_label}</td>
              <td>{t.verified_rows.toLocaleString()}</td>
              <td>{millis(t.duration_ms)}</td>
              <td>
                <ResultBadge result={t.result} />
                {t.detail ? <div className="cell-error" title={t.detail}>{t.detail}</div> : null}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// --- Restore (guarded) -----------------------------------------------------

function RestoreModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const toast = useToast();
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
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button
            type="submit"
            form="restore-form"
            className="btn btn-danger"
            disabled={busy || !matches}
          >
            {busy ? "Restoring…" : "Restore now"}
          </button>
        </>
      }
    >
      <Callout tone="danger" title="This replaces your current data">
        Restoring overwrites the live database with the contents of a backup. Anything written
        since that backup will be lost. The panel takes a safety snapshot first, but you should
        only do this if you understand the consequences.
      </Callout>

      <form id="restore-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}

        <fieldset className="field">
          <legend className="field-label">What to restore</legend>
          <label className="radio">
            <input
              type="radio"
              name="mode"
              checked={mode === "latest"}
              onChange={() => setMode("latest")}
            />
            <span>
              <strong>Latest backup</strong>
              <span className="muted"> — restore the most recent backup.</span>
            </span>
          </label>
          <label className="radio">
            <input
              type="radio"
              name="mode"
              checked={mode === "pitr"}
              onChange={() => setMode("pitr")}
            />
            <span>
              <strong>Point in time</strong>
              <span className="muted"> — recover the database to a specific moment.</span>
            </span>
          </label>
        </fieldset>

        {mode === "pitr" ? (
          <label className="field">
            <span className="field-label">Recover to</span>
            <input
              type="datetime-local"
              value={pointInTime}
              onChange={(e) => setPointInTime(e.target.value)}
              required
            />
            <span className="field-help muted">
              The database will be recovered to exactly this time.
            </span>
          </label>
        ) : null}

        <label className="checkbox">
          <input type="checkbox" checked={delta} onChange={(e) => setDelta(e.target.checked)} />
          <span>
            Fast restore (only re-copy changed files)
            <span className="muted"> — recommended; turn off to fully wipe and rebuild.</span>
          </span>
        </label>

        <label className="field">
          <span className="field-label">
            Type <code>{expected}</code> to confirm this overwrite
          </span>
          <input
            type="text"
            value={confirm}
            autoComplete="off"
            spellCheck={false}
            placeholder={expected}
            aria-invalid={confirm.length > 0 && !matches}
            onChange={(e) => setConfirm(e.target.value)}
          />
        </label>
      </form>
    </Modal>
  );
}
