import { describe, it, expect, vi, beforeEach } from "vitest";
import { useState } from "react";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { TuningPanel, mbLabel, PROFILE_EFFECTS } from "./DatabaseTuning";
import { api, ApiError } from "@/api/client";
import type {
  AppliedTuning,
  TuningRecommendation,
  TuningStatus,
  WorkloadProfile,
} from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

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

/** The Mixed recommendation our fixtures pin "applied" to, so the normal case is
 *  genuinely identical (applied == active recommendation) and the duplicate table
 *  is suppressed. Drift tests perturb `applied` away from this. */
const MIXED_REC = rec("mixed", { shared_buffers_mb: 2048, max_connections: 100 });

function applied(over: Partial<AppliedTuning> = {}): AppliedTuning {
  return {
    shared_buffers_mb: MIXED_REC.shared_buffers_mb,
    effective_cache_size_mb: MIXED_REC.effective_cache_size_mb,
    work_mem_mb: MIXED_REC.work_mem_mb,
    maintenance_work_mem_mb: MIXED_REC.maintenance_work_mem_mb,
    max_connections: MIXED_REC.max_connections,
    ...over,
  };
}

function status(over: Partial<TuningStatus> = {}): TuningStatus {
  return {
    memory_mb: 8192,
    cpu_count: 4,
    active_profile: "mixed",
    applied: applied(),
    profiles: [
      rec("oltp", { shared_buffers_mb: 2048, max_connections: 200 }),
      MIXED_REC,
      rec("olap", { shared_buffers_mb: 3276, max_connections: 50 }),
    ],
    ...over,
  };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("mbLabel", () => {
  it("renders whole GB, fractional GB, and MB", () => {
    expect(mbLabel(8192)).toBe("8 GB");
    expect(mbLabel(2457)).toBe("2.4 GB");
    expect(mbLabel(512)).toBe("512 MB");
  });
});

describe("TuningPanel", () => {
  it("shows the detected host and the active profile", () => {
    render(<TuningPanel status={status()} onApplied={vi.fn()} />);
    expect(screen.getByText(/8 GB RAM/)).toBeInTheDocument();
    expect(screen.getByText(/4 CPUs/)).toBeInTheDocument();
    // Active profile is named in the intro callout.
    expect(
      screen.getByText((_, el) => el?.tagName === "STRONG" && el.textContent === "Mixed"),
    ).toBeInTheDocument();
  });

  // Regression guard for the confusing duplicate: in the normal case (active ==
  // preview, applied == the active profile's recommendation) only ONE table shows.
  it("shows a single table when applied already matches the active recommendation", () => {
    render(<TuningPanel status={status()} onApplied={vi.fn()} />);
    expect(screen.getByText("Currently applied")).toBeInTheDocument();
    // No duplicate recommendation table, no apply affordance — nothing to change.
    expect(screen.queryByText("Recommended for this server")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Apply / }),
    ).not.toBeInTheDocument();
    // No drift in the normal case, so no drift warning.
    expect(
      screen.queryByText("Settings have drifted from this profile"),
    ).not.toBeInTheDocument();
  });

  // Each table's label must be a visible heading ABOVE its rows (caption-bottom
  // would orient the operator only after the fifth row) and programmatically name
  // the table for assistive tech.
  it("labels each table with a heading above the rows, tied to the table", () => {
    render(
      <TuningPanel
        status={status({ applied: applied({ shared_buffers_mb: 1024 }) })}
        onApplied={vi.fn()}
      />,
    );
    // The applied table is reachable by its accessible name (aria-labelledby), and
    // its label precedes its first data cell in the DOM.
    const appliedTable = screen.getByRole("table", { name: "Currently applied" });
    expect(appliedTable).toBeInTheDocument();
    const heading = screen.getByText("Currently applied");
    expect(heading.tagName).toBe("P");
    expect(
      heading.compareDocumentPosition(appliedTable) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      screen.getByRole("table", { name: "Recommended for this server" }),
    ).toBeInTheDocument();
  });

  it("warns calmly and still shows recommendations when applied is null", () => {
    render(<TuningPanel status={status({ applied: null })} onApplied={vi.fn()} />);
    expect(screen.getByText("Live settings unavailable")).toBeInTheDocument();
    expect(screen.queryByText("Currently applied")).not.toBeInTheDocument();
    // The recommendation for the current profile is still shown — but with PG
    // unreachable we can't offer to apply it.
    expect(screen.getByText("Recommended for this server")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Apply / }),
    ).not.toBeInTheDocument();
    // The blocking reason is collocated with where the Apply button would be, not
    // only in the callout above.
    expect(
      screen.getByText("Start Postgres to apply this profile."),
    ).toBeInTheDocument();

    // Switching to a different profile previews its sizing, but still no Apply and
    // still the same in-place explanation — the system status that blocks the
    // action is visible at the point of action.
    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    expect(screen.getByText("OLAP would size this server to")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Apply / }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("Start Postgres to apply this profile."),
    ).toBeInTheDocument();
  });

  // Drift: same profile, but the live applied values diverged from the active
  // profile's recommendation (e.g. a manual ALTER SYSTEM). Now BOTH tables show
  // and re-applying is offered to bring it back in line.
  it("shows both tables and an Apply when the active profile has drifted", () => {
    render(
      <TuningPanel
        status={status({ applied: applied({ shared_buffers_mb: 1024 }) })}
        onApplied={vi.fn()}
      />,
    );
    expect(screen.getByText("Currently applied")).toBeInTheDocument();
    expect(screen.getByText("Recommended for this server")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Apply Mixed profile" }),
    ).toBeInTheDocument();
    // Drift is explained so two same-profile tables + an Apply for the already
    // "current" profile don't read as a bug.
    expect(
      screen.getByText("Settings have drifted from this profile"),
    ).toBeInTheDocument();
  });

  it("previews another profile's sizing and offers to apply it", () => {
    render(<TuningPanel status={status()} onApplied={vi.fn()} />);

    // Defaults to the active profile: its effect is described, no other-profile
    // preview, no apply (applied already matches it).
    expect(screen.getByText(PROFILE_EFFECTS.mixed.effect)).toBeInTheDocument();
    expect(screen.queryByText(/would size this server to/)).not.toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "Mixed — current" })).toHaveAttribute(
      "data-state",
      "on",
    );

    // Switch to OLAP — its effect + sizing are previewed and an Apply appears.
    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    expect(screen.getByText(PROFILE_EFFECTS.olap.effect)).toBeInTheDocument();
    expect(screen.getByText("OLAP would size this server to")).toBeInTheDocument();
    // OLAP's larger shared_buffers (3276 MB → 3.2 GB) is shown.
    expect(screen.getByText("3.2 GB")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Apply OLAP profile" }),
    ).toBeInTheDocument();
  });

  it("confirms with honest wording, applies the previewed profile, and flips the active profile from the returned status", async () => {
    const next = status({
      active_profile: "olap",
      applied: applied({ shared_buffers_mb: 3276, max_connections: 50 }),
    });
    const spy = vi.spyOn(api, "applyTuning").mockResolvedValue(next);

    // A tiny harness lets onApplied feed the fresh status back, so we can assert
    // the panel re-renders the new active profile (mirrors how the view refreshes).
    function Harness() {
      const [s, setS] = useState(status());
      return <TuningPanel status={s} onApplied={setS} />;
    }
    render(<Harness />);

    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    // The confirm states the exact, honest blast radius before it happens.
    expect(screen.getByText("Apply the OLAP profile?")).toBeInTheDocument();
    expect(
      screen.getByText(
        /This resizes shared_buffers and max_connections, so it restarts Postgres now/,
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Open connections will drop and reconnect\. Nothing else changes\./),
    ).toBeInTheDocument();
    // Not all-downside: the reassurance line states the rollback safety net and
    // that data is never touched, so a cautious non-DBA isn't scared off.
    expect(
      screen.getByText(
        /If the restart fails, Postgres automatically rolls back to its current settings — your data is never touched\./,
      ),
    ).toBeInTheDocument();
    // The choice is reversible — stated plainly so a cautious non-DBA isn't scared
    // off a safe change by fear of being stuck on the new profile.
    expect(
      screen.getByText("You can switch profiles again anytime."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    await waitFor(() => expect(spy).toHaveBeenCalledWith("olap"));
    expect(toast.success).toHaveBeenCalledWith(
      "Applied OLAP profile — Postgres restarted",
    );

    // State updated from the returned status: OLAP is now the current profile and
    // the apply affordance is gone (applied == the active recommendation again).
    await waitFor(() =>
      expect(
        screen.getByRole("radio", { name: "OLAP — current" }),
      ).toHaveAttribute("data-state", "on"),
    );
    expect(
      screen.queryByRole("button", { name: /^Apply / }),
    ).not.toBeInTheDocument();
  });

  // A reloadable-only drift (shared_buffers + max_connections already match, only
  // work_mem diverged) is applied with a zero-downtime reload server-side. So the
  // confirm must NOT threaten a restart/downtime, and the success toast must NOT
  // claim "Postgres restarted" for an apply that only reloaded.
  it("uses no-downtime wording and a plain toast when only a reloadable setting drifted", async () => {
    const spy = vi.spyOn(api, "applyTuning").mockResolvedValue(status());

    function Harness() {
      const [s, setS] = useState(status({ applied: applied({ work_mem_mb: 64 }) }));
      return <TuningPanel status={s} onApplied={setS} />;
    }
    render(<Harness />);

    fireEvent.click(screen.getByRole("button", { name: "Apply Mixed profile" }));

    // No restart/downtime threat for a reloadable-only change — the operator sees
    // the reload wording instead.
    expect(screen.queryByText(/restarts Postgres now/)).not.toBeInTheDocument();
    expect(
      screen.getByText(
        /applies new settings with a quick reload — no restart and no downtime/,
      ),
    ).toBeInTheDocument();
    // The reversibility reassurance shows on the reload path too, not just restart.
    expect(
      screen.getByText("You can switch profiles again anytime."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Apply Mixed profile" }));

    await waitFor(() => expect(spy).toHaveBeenCalledWith("mixed"));
    // The toast must not claim a restart that did not happen.
    expect(toast.success).toHaveBeenCalledWith("Applied Mixed profile");
  });

  // Persist-recovery: Postgres was retuned to OLAP but recording the choice failed,
  // so the active profile reads the stale "mixed" while the live applied settings
  // already match OLAP. Previewing OLAP makes `differs` false — yet because it's a
  // DIFFERENT profile than the stale active one, Apply MUST still appear so the
  // operator can record the choice the failed persist dropped. Applying is a pure
  // server-side no-op, so the confirm must promise no restart and no reload, and the
  // apply still calls through to persist the profile.
  it("offers a no-op apply to record the profile when settings already match (persist-recovery)", async () => {
    // applied already holds OLAP's sizing, but active_profile is the stale "mixed".
    const olapApplied = applied({ shared_buffers_mb: 3276, max_connections: 50 });
    const spy = vi
      .spyOn(api, "applyTuning")
      .mockResolvedValue(status({ active_profile: "olap", applied: olapApplied }));

    function Harness() {
      const [s, setS] = useState(status({ applied: olapApplied }));
      return <TuningPanel status={s} onApplied={setS} />;
    }
    render(<Harness />);

    // Preview OLAP: its sizing equals the live applied values, so no recommendation
    // diff — but Apply is still offered (the recovery path).
    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    expect(
      screen.getByRole("button", { name: "Apply OLAP profile" }),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    // The confirm must NOT threaten a restart OR a reload — nothing changes
    // server-side; it only records the profile choice.
    expect(screen.queryByText(/restarts Postgres now/)).not.toBeInTheDocument();
    expect(
      screen.queryByText(/applies new settings with a quick reload/),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText(/only records OLAP as the active profile/),
    ).toBeInTheDocument();

    // Confirm: it applies (and the server persists) the chosen profile.
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));
    await waitFor(() => expect(spy).toHaveBeenCalledWith("olap"));
    // Honest toast: it recorded the choice; it did not "apply"/restart anything.
    expect(toast.success).toHaveBeenCalledWith("Recorded OLAP profile");
  });

  // Regression: after a successful apply the Apply button unmounts (applied now
  // matches the active recommendation). Radix's dialog-close focus restoration
  // would target that removed button and drop focus to <body>, stranding keyboard
  // / screen-reader users. apply() re-anchors focus on the profile toggles, a
  // recoverable spot in the panel.
  it("re-anchors focus on the profile toggles after a successful apply", async () => {
    const next = status({
      active_profile: "olap",
      applied: applied({ shared_buffers_mb: 3276, max_connections: 50 }),
    });
    vi.spyOn(api, "applyTuning").mockResolvedValue(next);

    function Harness() {
      const [s, setS] = useState(status());
      return <TuningPanel status={s} onApplied={setS} />;
    }
    const { container } = render(<Harness />);

    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    await waitFor(() => expect(toast.success).toHaveBeenCalled());

    const toggleWrapper = container.querySelector(
      '[data-slot="toggle-group"]',
    )?.parentElement;
    // Focus must not have fallen to <body>; it lands on the toggle-group wrapper.
    await waitFor(() => {
      expect(document.activeElement).not.toBe(document.body);
      expect(document.activeElement).toBe(toggleWrapper);
    });
  });

  it("surfaces a rolled-back apply without changing state or signalling success", async () => {
    vi.spyOn(api, "applyTuning").mockRejectedValue(
      new ApiError(403, {
        code: "safety",
        message: "restart failed; rolled back to the last-known-good settings",
      }),
    );
    const onApplied = vi.fn();
    render(<TuningPanel status={status()} onApplied={onApplied} />);

    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    await waitFor(() =>
      expect(
        screen.getByText(
          "restart failed; rolled back to the last-known-good settings",
        ),
      ).toBeInTheDocument(),
    );
    // Nothing applied: parent is not notified and no success toast fires.
    expect(onApplied).not.toHaveBeenCalled();
    expect(toast.success).not.toHaveBeenCalled();
    // Dialog stays open so the operator can read the rollback reason.
    expect(screen.getByText("Apply the OLAP profile?")).toBeInTheDocument();
  });

  // Regression: a failed apply must leave a permanent record. The dialog holds the
  // reason while open, but the instant it's dismissed the dialog unmounts — and the
  // reason must NOT vanish with it, or a keyboard user who pressed Escape would see
  // a panel that looks untouched after a real restart was attempted and rolled back.
  // The reason is mirrored below the Apply button and survives dismissal, then
  // clears when the operator moves on to a different profile.
  it("keeps the rollback reason visible after the failed-apply dialog is dismissed", async () => {
    vi.spyOn(api, "applyTuning").mockRejectedValue(
      new ApiError(403, {
        code: "safety",
        message: "restart failed; rolled back to the last-known-good settings",
      }),
    );
    render(<TuningPanel status={status()} onApplied={vi.fn()} />);

    fireEvent.click(screen.getByRole("radio", { name: "OLAP" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));
    fireEvent.click(screen.getByRole("button", { name: "Apply OLAP profile" }));

    // Shown inside the open dialog first.
    await waitFor(() =>
      expect(
        screen.getByText(
          "restart failed; rolled back to the last-known-good settings",
        ),
      ).toBeInTheDocument(),
    );

    // Dismiss via Cancel — the dialog closes…
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() =>
      expect(
        screen.queryByText("Apply the OLAP profile?"),
      ).not.toBeInTheDocument(),
    );
    // …but the reason persists below the Apply button as a permanent record.
    expect(
      screen.getByText(
        "restart failed; rolled back to the last-known-good settings",
      ),
    ).toBeInTheDocument();

    // Moving to a different profile is a fresh action context, so the stale error
    // clears rather than lingering under an unrelated preview.
    fireEvent.click(screen.getByRole("radio", { name: "Mixed — current" }));
    expect(
      screen.queryByText(
        "restart failed; rolled back to the last-known-good settings",
      ),
    ).not.toBeInTheDocument();
  });
});
