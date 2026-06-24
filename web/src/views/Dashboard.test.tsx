import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import type { AsyncState } from "@/lib/hooks";
import type { DashboardData } from "@/api/types";
import { ApiError } from "@/api/client";

// Drive the view by stubbing the polling hook directly — no timers, no fetch.
// We only care that the view reacts correctly to {data, error, loading}.
const pollState = vi.hoisted(() => ({ current: null as unknown }));
vi.mock("@/lib/hooks", () => ({
  usePolling: () => pollState.current,
}));

import { Dashboard } from "./Dashboard";

function state(over: Partial<AsyncState<DashboardData>>): AsyncState<DashboardData> {
  return { data: null, error: null, loading: false, reload: () => {}, ...over };
}

const SAMPLE: DashboardData = {
  pg: { running: true, version: "16.2" },
  snapshot: {
    taken_at: "2026-06-24T10:00:00Z",
    cpu_percent: 12,
    mem_used_bytes: 1_000,
    mem_total_bytes: 4_000,
    disk_used_bytes: 1_000,
    disk_total_bytes: 10_000,
    load1: 0.1,
    connections: 3,
    max_connections: 100,
    cache_hit_ratio: 0.99,
    tps: 4.2,
    deadlocks: 0,
    replication_lag_seconds: 0,
    last_backup_age_seconds: 60,
  },
  last_backup: null,
  health_ok: true,
  health_reasons: [],
};

const NETWORK_ERR = new ApiError(0, {
  code: "internal",
  message: "Could not reach the panel. Check your connection.",
});

describe("Dashboard refresh-failure surfacing", () => {
  beforeEach(() => {
    pollState.current = null;
  });

  it("shows the stale banner — not a silent freeze — when a refresh fails but cached data remains", () => {
    pollState.current = state({ data: SAMPLE, error: NETWORK_ERR });
    render(<Dashboard />);

    // The cached dashboard is still rendered (we don't blank it on a blip)...
    expect(screen.getByText("Dashboard")).toBeInTheDocument();
    expect(screen.getByText("● Healthy")).toBeInTheDocument();
    // ...but the operator is told the live view stalled, so a frozen "Healthy"
    // badge can never silently mislead.
    expect(screen.getByText("Live updates paused")).toBeInTheDocument();
  });

  it("does not show the stale banner on a clean poll", () => {
    pollState.current = state({ data: SAMPLE });
    render(<Dashboard />);
    expect(screen.queryByText("Live updates paused")).toBeNull();
  });

  it("shows a full error notice (not the stale banner) when the very first load fails", () => {
    pollState.current = state({ error: NETWORK_ERR, loading: false });
    render(<Dashboard />);
    // No cached data → hard error, and the dashboard body is not rendered.
    expect(screen.queryByText("Live updates paused")).toBeNull();
    expect(screen.queryByText("Dashboard")).toBeNull();
    expect(screen.getByRole("alert")).toBeInTheDocument();
  });

  it("shows a labeled loading skeleton (not the dashboard) on the very first load", () => {
    pollState.current = state({ loading: true });
    render(<Dashboard />);
    // First load with no cached data → skeleton placeholder, announced to AT.
    expect(screen.getByRole("status", { name: "Loading dashboard…" })).toBeInTheDocument();
    expect(screen.queryByText("Dashboard")).toBeNull();
  });

  it("renders the live Postgres + Server cards once data arrives", () => {
    pollState.current = state({ data: SAMPLE });
    render(<Dashboard />);
    // Card titles render, and host metrics are surfaced from the snapshot.
    expect(screen.getByText("Postgres")).toBeInTheDocument();
    expect(screen.getByText("Latest backup")).toBeInTheDocument();
    expect(screen.getByText("Server")).toBeInTheDocument();
    expect(screen.getByText("Running")).toBeInTheDocument();
    // No backup yet → the warn callout points the operator at Backups.
    expect(screen.getByText(/No successful backup yet/)).toBeInTheDocument();
  });
});
