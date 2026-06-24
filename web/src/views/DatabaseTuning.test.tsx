import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TuningPanel, mbLabel, PROFILE_EFFECTS } from "./DatabaseTuning";
import type {
  AppliedTuning,
  TuningRecommendation,
  TuningStatus,
  WorkloadProfile,
} from "@/api/types";

function rec(
  profile: WorkloadProfile,
  over: Partial<TuningRecommendation> = {},
): TuningRecommendation {
  return {
    profile,
    memory_mb: 8192,
    cpu_count: 4,
    shared_buffers_mb: 2048,
    effective_cache_size_mb: 6144,
    work_mem_mb: 16,
    maintenance_work_mem_mb: 512,
    max_connections: 100,
    ...over,
  };
}

function status(over: Partial<TuningStatus> = {}): TuningStatus {
  const applied: AppliedTuning = {
    shared_buffers_mb: 2048,
    effective_cache_size_mb: 6144,
    work_mem_mb: 16,
    maintenance_work_mem_mb: 512,
    max_connections: 100,
  };
  return {
    memory_mb: 8192,
    cpu_count: 4,
    active_profile: "mixed",
    applied,
    profiles: [
      rec("oltp", { shared_buffers_mb: 2048, max_connections: 200 }),
      rec("mixed", { shared_buffers_mb: 2457, max_connections: 100 }),
      rec("olap", { shared_buffers_mb: 3276, max_connections: 50 }),
    ],
    ...over,
  };
}

describe("mbLabel", () => {
  it("renders whole GB, fractional GB, and MB", () => {
    expect(mbLabel(8192)).toBe("8 GB");
    expect(mbLabel(2457)).toBe("2.4 GB");
    expect(mbLabel(512)).toBe("512 MB");
  });
});

describe("TuningPanel", () => {
  it("shows the detected host and the active profile", () => {
    render(<TuningPanel status={status()} />);
    expect(screen.getByText(/8 GB RAM/)).toBeInTheDocument();
    expect(screen.getByText(/4 CPUs/)).toBeInTheDocument();
    // Active profile is named in the intro callout.
    expect(
      screen.getByText((_, el) => el?.tagName === "STRONG" && el.textContent === "Mixed"),
    ).toBeInTheDocument();
  });

  it("renders the applied settings values", () => {
    render(<TuningPanel status={status()} />);
    expect(screen.getByText("Currently applied")).toBeInTheDocument();
    // max_connections (a count, not MB) is rendered verbatim.
    expect(screen.getAllByText("100").length).toBeGreaterThan(0);
  });

  it("warns calmly and still shows recommendations when applied is null", () => {
    render(<TuningPanel status={status({ applied: null })} />);
    expect(screen.getByText("Live settings unavailable")).toBeInTheDocument();
    expect(screen.queryByText("Currently applied")).not.toBeInTheDocument();
    // The recommendation table for the current profile is still shown.
    expect(screen.getByText("Recommended for this server")).toBeInTheDocument();
  });

  it("describes each profile's effect and previews its sizing without claiming to apply it", () => {
    render(<TuningPanel status={status()} />);

    // Defaults to the active profile: no "would size" preview, no change warning.
    expect(screen.getByText(PROFILE_EFFECTS.mixed.effect)).toBeInTheDocument();
    expect(screen.queryByText(/This is a preview/)).not.toBeInTheDocument();

    // The active profile is marked in the toggle and selected by default.
    expect(screen.getByRole("radio", { name: "Mixed — current" })).toHaveAttribute(
      "data-state",
      "on",
    );

    // Switch to OLAP — its effect is described, its sizing previewed, and the
    // copy makes clear nothing changes and a restart is involved.
    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    expect(screen.getByText(PROFILE_EFFECTS.olap.effect)).toBeInTheDocument();
    expect(screen.getByText("OLAP would size this server to")).toBeInTheDocument();
    expect(
      screen.getByText(/This is a preview — nothing changes here/),
    ).toBeInTheDocument();
    expect(screen.getByText(/requires a brief Postgres restart/)).toBeInTheDocument();
    // OLAP's larger shared_buffers (3276 MB → 3.2 GB) is shown.
    expect(screen.getByText("3.2 GB")).toBeInTheDocument();
  });
});
