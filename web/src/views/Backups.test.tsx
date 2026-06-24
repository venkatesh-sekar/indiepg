import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import {
  backupDestination,
  backupFreshness,
  BackupStatusSummary,
  DeepRestoreTestConfirm,
  LocalBackupWarning,
  RestoreModal,
  RestoreTestStatus,
  restoreTestStatus,
} from "./Backups";
import type { BackupRecord, RestoreTestRecord, S3Target } from "@/api/types";

// s3 builds an S3Target; override only the fields a case cares about. Defaults to
// an empty (unconfigured) target so "configured" cases are explicit.
function s3(over: Partial<S3Target> = {}): S3Target {
  return {
    endpoint: "",
    region: "",
    bucket: "",
    prefix: "",
    access_key: "",
    use_ssl: true,
    ...over,
  };
}

// rt builds a RestoreTestRecord; override only what a case cares about.
// Defaults to a passed test so failure cases are explicit at the call site.
function rt(over: Partial<RestoreTestRecord>): RestoreTestRecord {
  return {
    id: 1,
    tested_at: "2026-06-24T10:00:00Z",
    source_label: "20260624-incr",
    verified_rows: 0,
    result: "success",
    duration_ms: 1000,
    detail: "",
    ...over,
  };
}

// rec builds a BackupRecord; override only what a case cares about. Defaults to a
// successful run so failure cases are explicit at the call site.
function rec(over: Partial<BackupRecord>): BackupRecord {
  return {
    id: 1,
    label: "20260624-incr",
    backup_type: "incr",
    started_at: "2026-06-24T10:00:00Z",
    stopped_at: "2026-06-24T10:01:00Z",
    size_bytes: 0,
    database_bytes: 0,
    repo_bytes: 0,
    wal_start: "",
    wal_stop: "",
    result: "success",
    repo_path: "",
    error: "",
    ...over,
  };
}

describe("backupDestination", () => {
  it("reports loading until the config response has arrived", () => {
    // Must not guess "local" for a box that may actually have S3 configured.
    expect(backupDestination(undefined, false)).toEqual({ kind: "loading" });
    expect(backupDestination(s3({ bucket: "b" }), false)).toEqual({ kind: "loading" });
  });

  it("reports local when no endpoint or bucket is set", () => {
    expect(backupDestination(undefined, true)).toEqual({ kind: "local" });
    expect(backupDestination(s3(), true)).toEqual({ kind: "local" });
  });

  it("treats a whitespace-only target as local (not a real off-host destination)", () => {
    expect(backupDestination(s3({ bucket: "  ", endpoint: "  " }), true)).toEqual({
      kind: "local",
    });
  });

  it("reports s3 with the bucket name once a bucket is set", () => {
    expect(backupDestination(s3({ bucket: "my-backups" }), true)).toEqual({
      kind: "s3",
      bucketName: "my-backups",
    });
  });

  it("falls back to the endpoint for the label when only an endpoint is set", () => {
    expect(backupDestination(s3({ endpoint: "s3.example.com" }), true)).toEqual({
      kind: "s3",
      bucketName: "s3.example.com",
    });
  });

  it("prefers the bucket name over the endpoint for the label", () => {
    expect(
      backupDestination(s3({ bucket: "my-backups", endpoint: "s3.example.com" }), true),
    ).toEqual({ kind: "s3", bucketName: "my-backups" });
  });
});

describe("LocalBackupWarning", () => {
  function renderWarning(destination: Parameters<typeof LocalBackupWarning>[0]["destination"]) {
    return render(
      <MemoryRouter>
        <LocalBackupWarning destination={destination} />
      </MemoryRouter>,
    );
  }

  it("warns (warn tone) about disk/host loss and points to S3 when backups are local-only", () => {
    renderWarning({ kind: "local" });
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "warning");
    expect(screen.getByText(/disk failure or losing the server/i)).toBeInTheDocument();
    // Nudges off-host: a link into Settings to connect a bucket.
    const link = screen.getByRole("link", { name: /set up an s3 bucket/i });
    expect(link).toHaveAttribute("href", "/settings");
  });

  it("renders nothing once an off-host S3 destination is configured", () => {
    const { container } = renderWarning({ kind: "s3", bucketName: "my-backups" });
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing while the config is still loading", () => {
    const { container } = renderWarning({ kind: "loading" });
    expect(container).toBeEmptyDOMElement();
  });
});

describe("backupFreshness", () => {
  it("reports none when no backups have run", () => {
    expect(backupFreshness([])).toEqual({ kind: "none" });
  });

  it("reports good when the most recent run succeeded", () => {
    const newest = rec({ id: 2, label: "new", result: "success" });
    const older = rec({ id: 1, label: "old", result: "success" });
    expect(backupFreshness([newest, older])).toEqual({ kind: "good", good: newest });
  });

  it("reports stale (with the last GOOD backup) when the most recent run failed", () => {
    // "fail" is the exact string the server writes (internal/backup/manager.go).
    const failed = rec({ id: 3, label: "failed", result: "fail" });
    const lastGood = rec({ id: 2, label: "lastgood", result: "success" });
    const older = rec({ id: 1, label: "older", result: "success" });
    // newest-first order, as the server returns it
    expect(backupFreshness([failed, lastGood, older])).toEqual({
      kind: "stale",
      good: lastGood,
    });
  });

  it("reports never-good when backups have run but none ever succeeded", () => {
    expect(backupFreshness([rec({ result: "fail" }), rec({ result: "fail" })])).toEqual({
      kind: "never-good",
    });
  });

  it("treats an unrecognized latest result as not-failed (keeps showing the last good backup)", () => {
    // A latest run whose result is neither success nor failure must not be read
    // as a failure — the data is still protected by the prior good backup.
    const unknown = rec({ id: 3, result: "running" });
    const lastGood = rec({ id: 2, result: "success" });
    expect(backupFreshness([unknown, lastGood])).toEqual({ kind: "good", good: lastGood });
  });
});

describe("BackupStatusSummary", () => {
  it("shouts (danger) and warns data is unprotected when there are no backups", () => {
    render(<BackupStatusSummary backups={[]} />);
    const callout = document.querySelector('[data-slot="alert"]');
    expect(callout).toHaveAttribute("data-variant", "destructive");
    expect(screen.getByText(/your data is not protected/i)).toBeInTheDocument();
  });

  it("shouts (danger) when the most recent backup failed, citing the last good one", () => {
    render(
      <BackupStatusSummary backups={[rec({ result: "fail" }), rec({ result: "success" })]} />,
    );
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "destructive");
    expect(screen.getByText(/most recent backup failed/i)).toBeInTheDocument();
  });

  it("shouts (danger) when every backup has failed", () => {
    render(<BackupStatusSummary backups={[rec({ result: "fail" })]} />);
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "destructive");
    expect(screen.getByText(/no working backup yet/i)).toBeInTheDocument();
  });

  it("shows an ok banner with the backup type when the latest backup succeeded", () => {
    render(<BackupStatusSummary backups={[rec({ backup_type: "full", result: "success" })]} />);
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "success");
    expect(screen.getByText(/your data is backed up/i)).toBeInTheDocument();
    expect(screen.getByText("full")).toBeInTheDocument();
  });
});

describe("restoreTestStatus", () => {
  it("reports never when no restore test has run", () => {
    expect(restoreTestStatus([])).toEqual({ kind: "never" });
  });

  it("reports passed when the most recent test passed", () => {
    const newest = rt({ id: 2, result: "success" });
    const older = rt({ id: 1, result: "success" });
    expect(restoreTestStatus([newest, older])).toEqual({ kind: "passed", passed: newest });
  });

  it("reports failed (with the last PASSED test) when the most recent test failed", () => {
    const failed = rt({ id: 3, result: "fail" });
    const lastPassed = rt({ id: 2, result: "success" });
    const older = rt({ id: 1, result: "success" });
    // newest-first order, as the server returns it
    expect(restoreTestStatus([failed, lastPassed, older])).toEqual({
      kind: "failed",
      passed: lastPassed,
    });
  });

  it("reports never-passed when tests have run but none ever passed", () => {
    expect(restoreTestStatus([rt({ result: "fail" }), rt({ result: "fail" })])).toEqual({
      kind: "never-passed",
    });
  });

  it("treats an unrecognized latest result as not-failed (keeps showing the last pass)", () => {
    const unknown = rt({ id: 3, result: "running" });
    const lastPassed = rt({ id: 2, result: "success" });
    expect(restoreTestStatus([unknown, lastPassed])).toEqual({ kind: "passed", passed: lastPassed });
  });
});

describe("RestoreTestStatus", () => {
  it("calmly states (info) recoverability is unverified when no test has run", () => {
    // Intentionally NOT a danger shout: the operator simply hasn't run a
    // verification yet, so it states the fact without alarm.
    render(<RestoreTestStatus tests={[]} />);
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "info");
    expect(screen.getByText(/haven't been test-restored yet/i)).toBeInTheDocument();
  });

  it("shouts (danger) when the most recent restore test failed", () => {
    render(
      <RestoreTestStatus tests={[rt({ result: "fail" }), rt({ result: "success" })]} />,
    );
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "destructive");
    expect(screen.getByText(/most recent restore test failed/i)).toBeInTheDocument();
  });

  it("shouts (danger) when no restore test has ever passed", () => {
    render(<RestoreTestStatus tests={[rt({ result: "fail" })]} />);
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "destructive");
    expect(screen.getByText(/no restore test has passed yet/i)).toBeInTheDocument();
  });

  it("shows an ok banner (with restored rows) when the latest test passed", () => {
    render(<RestoreTestStatus tests={[rt({ result: "success", verified_rows: 1234 })]} />);
    expect(document.querySelector('[data-slot="alert"]')).toHaveAttribute("data-variant", "success");
    expect(screen.getByText(/verified intact/i)).toBeInTheDocument();
    expect(screen.getByText(/1,234 rows restored and verified/i)).toBeInTheDocument();
  });
});

describe("DeepRestoreTestConfirm", () => {
  const noop = () => undefined;

  it("renders nothing when closed", () => {
    const { container } = render(
      <DeepRestoreTestConfirm open={false} busy={false} onConfirm={noop} onCancel={noop} />,
    );
    expect(container).toBeEmptyDOMElement();
  });

  it("states up front what it does and its costs (longer + disk headroom)", () => {
    render(<DeepRestoreTestConfirm open busy={false} onConfirm={noop} onCancel={noop} />);
    // It must say what the action actually does before it does it.
    expect(screen.getByText(/restores your latest backup into a throwaway/i)).toBeInTheDocument();
    // The two costs the operator must know about up front.
    expect(screen.getByText(/runs longer/i)).toBeInTheDocument();
    expect(screen.getByText(/free disk space/i)).toBeInTheDocument();
    // The safety reassurance: live DB untouched, refuses rather than fill disk.
    expect(screen.getByText(/live database is never touched/i)).toBeInTheDocument();
    expect(screen.getByText(/refuses to run rather/i)).toBeInTheDocument();
  });

  it("confirms via the confirm button and cancels via cancel", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(<DeepRestoreTestConfirm open busy={false} onConfirm={onConfirm} onCancel={onCancel} />);
    fireEvent.click(screen.getByRole("button", { name: /run deep test/i }));
    expect(onConfirm).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByRole("button", { name: /cancel/i }));
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it("disables the actions while busy", () => {
    render(<DeepRestoreTestConfirm open busy onConfirm={noop} onCancel={noop} />);
    expect(screen.getByRole("button", { name: /working/i })).toBeDisabled();
  });
});

describe("RestoreModal", () => {
  const noop = () => undefined;

  it("warns the restore overwrites current data", () => {
    render(<RestoreModal onClose={noop} onDone={noop} />);
    expect(screen.getByText(/replaces your current data/i)).toBeInTheDocument();
  });

  it("gates the destructive Restore button until the stanza name is typed exactly", () => {
    render(<RestoreModal onClose={noop} onDone={noop} />);
    const restore = screen.getByRole("button", { name: /restore now/i });
    // Disabled until the typed confirmation matches the default stanza ("main").
    expect(restore).toBeDisabled();

    const confirm = screen.getByLabelText(/to confirm this overwrite/i);
    fireEvent.change(confirm, { target: { value: "wrong" } });
    // A mismatch surfaces an inline error and keeps the button disabled.
    expect(restore).toBeDisabled();
    expect(confirm).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByText(/must match/i)).toBeInTheDocument();

    fireEvent.change(confirm, { target: { value: "main" } });
    expect(restore).toBeEnabled();
    expect(confirm).toHaveAttribute("aria-invalid", "false");
  });

  it("reveals the point-in-time picker only when that mode is chosen", () => {
    render(<RestoreModal onClose={noop} onDone={noop} />);
    // Latest is the default; no datetime picker yet.
    expect(screen.queryByLabelText(/recover to/i)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: /point in time/i }));
    expect(screen.getByLabelText(/recover to/i)).toBeInTheDocument();
  });
});
