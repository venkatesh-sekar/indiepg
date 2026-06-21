// Backups: history table (size / duration / repo-size), run-now, restore-test,
// and a guarded restore flow (typed-name confirm equal to the stanza name).

import { useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { bytes, dateTime, duration, millis } from "@/lib/format";
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
  RecoveryTarget,
} from "@/api/types";

export function Backups() {
  const toast = useToast();
  const history = useAsync<BackupHistory>(() => api.backupHistory(), []);

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
        description="Back up your database to cloud storage, and check that your backups actually work."
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

      <Callout tone="info" title="How backups work here">
        Backups are stored in your cloud bucket using pgBackRest. A{" "}
        <strong>full</strong> backup copies everything; an{" "}
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
            ? "This copies the entire database to your bucket. It can take a while and use more storage, but it's a complete restore point."
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
