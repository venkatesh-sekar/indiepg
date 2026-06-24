import { describe, it, expect, beforeEach, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import type { AsyncState } from "@/lib/hooks";
import { ApiError } from "@/api/client";
import type { MigrationRecord, MigrationSession } from "@/api/types";

// Drive the views by stubbing the polling hook directly — no timers, no fetch.
// Every poller in Migrate.tsx calls usePolling exactly once, so a single shared
// state controls whichever sub-component a test renders in isolation.
// Default to a valid zero-state so a forgotten per-test assignment surfaces as a
// failed expectation, not a confusing destructure TypeError in usePolling.
const pollState = vi.hoisted(() => ({
  current: { data: null, error: null, loading: false, reload: () => {} } as unknown,
}));
vi.mock("@/lib/hooks", () => ({
  usePolling: () => pollState.current,
}));
// SessionProgress fires sonner toasts; stub the module so no Toaster need be mounted.
vi.mock("sonner", () => ({
  toast: { info: () => {}, error: () => {}, success: () => {} },
}));

import { MigrationHistory, DirectJobProgress, SessionProgress, Migrate } from "./Migrate";

function state<T>(over: Partial<AsyncState<T>>): AsyncState<T> {
  return { data: null, error: null, loading: false, reload: () => {}, ...over };
}

const NETWORK_ERR = new ApiError(0, {
  code: "internal",
  message: "Could not reach the panel. Check your connection.",
});

const JOB: MigrationRecord = {
  id: 7,
  mode: "single-db",
  role: "direct",
  status: "importing",
  phase: "restoring",
  source_summary: "app@db.example:5432/shop",
  target_database: "shop",
  overwrite: false,
  code: "",
  progress_done: 1,
  progress_total: 3,
  bytes_total: 0,
  error: "",
  row_counts_src: {},
  row_counts_tgt: {},
  created_at: "2026-06-24T10:00:00Z",
  updated_at: "2026-06-24T10:00:05Z",
  finished_at: null,
};

const SESSION: MigrationSession = {
  code: "XK7M2P",
  database: "shop",
  status: "exporting",
  target_host: "this-box",
  created_at: "2026-06-24T10:00:00Z",
  expires_at: "2026-06-24T11:00:00Z",
};

describe("Migrate pollers — no error-plus-infinite-spinner on first-load failure", () => {
  beforeEach(() => {
    pollState.current = state({});
  });

  it("MigrationHistory: a failed first load shows the error and NOT a perpetual spinner", () => {
    pollState.current = state<MigrationRecord[]>({ error: NETWORK_ERR });
    render(<MigrationHistory />);

    // The operator is told it failed...
    expect(screen.getByText(NETWORK_ERR.message)).toBeInTheDocument();
    // ...and is NOT also shown a "Loading…" spinner implying progress that isn't happening.
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("DirectJobProgress: a failed first poll shows the error and NOT a 'Starting…' spinner", () => {
    pollState.current = state<MigrationRecord>({ error: NETWORK_ERR });
    render(<DirectJobProgress id={7} onReset={() => {}} />);

    expect(screen.getByText(NETWORK_ERR.message)).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });

  it("SessionProgress: a failed first poll shows the error and NOT a 'Connecting…' spinner", () => {
    pollState.current = state<MigrationSession>({ error: NETWORK_ERR });
    render(<SessionProgress code="XK7M2P" onReset={() => {}} />);

    expect(screen.getByText(NETWORK_ERR.message)).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
  });
});

describe("Migrate pollers — honest about a poll that fails AFTER first success", () => {
  beforeEach(() => {
    pollState.current = state({});
  });

  it("MigrationHistory: keeps the cached list visible but warns the live view stalled", () => {
    pollState.current = state<MigrationRecord[]>({ data: [JOB], error: NETWORK_ERR });
    render(<MigrationHistory />);

    // The cached row is still on screen (we don't blank a working list on a blip)...
    expect(screen.getByText("One database")).toBeInTheDocument();
    // ...but the stale poll is surfaced, not silently swallowed.
    expect(screen.getByText(/Live updates paused/i)).toBeInTheDocument();
  });

  it("MigrationHistory: a clean poll shows neither error nor stale banner", () => {
    pollState.current = state<MigrationRecord[]>({ data: [JOB] });
    render(<MigrationHistory />);

    expect(screen.getByText("One database")).toBeInTheDocument();
    expect(screen.queryByText(/Live updates paused/i)).not.toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("DirectJobProgress: a live job + failed poll shows the job AND the stale banner (no silent freeze)", () => {
    pollState.current = state<MigrationRecord>({ data: JOB, error: NETWORK_ERR });
    render(<DirectJobProgress id={7} onReset={() => {}} />);

    expect(screen.getByText(/Live updates paused/i)).toBeInTheDocument();
  });

  it("SessionProgress: a live session + failed poll shows the stale banner", () => {
    pollState.current = state<MigrationSession>({ data: SESSION, error: NETWORK_ERR });
    render(<SessionProgress code="XK7M2P" onReset={() => {}} />);

    expect(screen.getByText(/Live updates paused/i)).toBeInTheDocument();
  });
});

describe("Migrate mode tabs", () => {
  beforeEach(() => {
    pollState.current = state({});
  });

  it("defaults to the one-database direct-pull form", () => {
    render(<Migrate />);
    expect(screen.getByText("Pull one database from another server")).toBeInTheDocument();
    expect(
      screen.queryByText("Pull an entire cluster from another server"),
    ).not.toBeInTheDocument();
  });

  it("switching tabs swaps the active form (only one mode mounted at a time)", () => {
    render(<Migrate />);
    // Radix Tabs activates on mousedown (button 0), not a synthetic click.
    fireEvent.mouseDown(screen.getByRole("tab", { name: /whole cluster/i }), { button: 0 });

    expect(screen.getByText("Pull an entire cluster from another server")).toBeInTheDocument();
    expect(
      screen.queryByText("Pull one database from another server"),
    ).not.toBeInTheDocument();
  });
});

describe("Migrate pollers — first load still shows a spinner while genuinely loading", () => {
  beforeEach(() => {
    pollState.current = state({});
  });

  it("MigrationHistory: no data and no error yet → a loading spinner", () => {
    pollState.current = state<MigrationRecord[]>({ loading: true });
    render(<MigrationHistory />);

    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("DirectJobProgress: no job and no error yet → a loading spinner", () => {
    pollState.current = state<MigrationRecord>({ loading: true });
    render(<DirectJobProgress id={7} onReset={() => {}} />);

    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("SessionProgress: no session and no error yet → a loading spinner", () => {
    pollState.current = state<MigrationSession>({ loading: true });
    render(<SessionProgress code="XK7M2P" onReset={() => {}} />);

    expect(screen.getByRole("status")).toBeInTheDocument();
  });
});
