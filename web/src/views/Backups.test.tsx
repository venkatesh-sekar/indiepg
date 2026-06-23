import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { backupFreshness, BackupStatusSummary } from "./Backups";
import type { BackupRecord } from "@/api/types";

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
    const callout = document.querySelector(".callout");
    expect(callout).toHaveClass("callout-danger");
    expect(screen.getByText(/your data is not protected/i)).toBeInTheDocument();
  });

  it("shouts (danger) when the most recent backup failed, citing the last good one", () => {
    render(
      <BackupStatusSummary backups={[rec({ result: "fail" }), rec({ result: "success" })]} />,
    );
    expect(document.querySelector(".callout")).toHaveClass("callout-danger");
    expect(screen.getByText(/most recent backup failed/i)).toBeInTheDocument();
  });

  it("shouts (danger) when every backup has failed", () => {
    render(<BackupStatusSummary backups={[rec({ result: "fail" })]} />);
    expect(document.querySelector(".callout")).toHaveClass("callout-danger");
    expect(screen.getByText(/no working backup yet/i)).toBeInTheDocument();
  });

  it("shows an ok banner with the backup type when the latest backup succeeded", () => {
    render(<BackupStatusSummary backups={[rec({ backup_type: "full", result: "success" })]} />);
    expect(document.querySelector(".callout")).toHaveClass("callout-ok");
    expect(screen.getByText(/your data is backed up/i)).toBeInTheDocument();
    expect(screen.getByText("full")).toBeInTheDocument();
  });
});
