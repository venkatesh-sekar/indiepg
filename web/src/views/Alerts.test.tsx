import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within, waitFor } from "@testing-library/react";
import { Alerts } from "./Alerts";
import { api } from "@/api/client";
import type { AlertRule, AlertsConfig, ChannelConfig } from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function rule(over: Partial<AlertRule> = {}): AlertRule {
  return {
    id: "r1",
    name: "Disk almost full",
    metric: "disk_percent",
    op: ">",
    threshold: 90,
    severity: "warning",
    for_seconds: 0,
    cooldown_seconds: 600,
    enabled: true,
    state: "ok",
    ...over,
  };
}

function channel(over: Partial<ChannelConfig> = {}): ChannelConfig {
  return { kind: "pushover", enabled: true, ...over };
}

function stub(cfg: Partial<AlertsConfig> = {}) {
  vi.spyOn(api, "alerts").mockResolvedValue({ channels: [], rules: [], ...cfg });
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("Alerts", () => {
  it("shows each channel with a configured/not-set-up badge and gates Send test on configuration", async () => {
    stub({ channels: [channel({ kind: "pushover", enabled: true })] });
    render(<Alerts />);

    const pushover = (await screen.findByText("Pushover")).closest("[data-slot=card]")!;
    expect(within(pushover as HTMLElement).getByText("Configured")).toBeInTheDocument();
    // Pushover is configured → its Send test is enabled.
    expect(within(pushover as HTMLElement).getByRole("button", { name: "Send test" })).toBeEnabled();

    // Webhook has no config → not set up, Send test disabled.
    const webhook = screen.getByText("Webhook").closest("[data-slot=card]")!;
    expect(within(webhook as HTMLElement).getByText("Not set up")).toBeInTheDocument();
    expect(within(webhook as HTMLElement).getByRole("button", { name: "Send test" })).toBeDisabled();
  });

  it("renders a rule row with its condition, severity and an enable toggle", async () => {
    stub({ rules: [rule({ name: "CPU high", metric: "cpu_percent", op: ">", threshold: 80 })] });
    render(<Alerts />);

    const row = (await screen.findByText("CPU high")).closest("tr")!;
    expect(within(row).getByText(/CPU usage \(%\) > 80/)).toBeInTheDocument();
    expect(within(row).getByText("Warning")).toBeInTheDocument();
    // Enabled rule → switch is the "Disable …" affordance, in checked state.
    expect(within(row).getByRole("switch", { name: "Disable CPU high" })).toBeChecked();
  });

  it("toggling a rule's switch saves it with the flipped enabled flag", async () => {
    const saveRule = vi.spyOn(api, "saveRule").mockResolvedValue({ ok: true } as never);
    stub({ rules: [rule({ id: "r9", name: "CPU high", enabled: true })] });
    render(<Alerts />);

    const sw = await screen.findByRole("switch", { name: "Disable CPU high" });
    fireEvent.click(sw);
    await waitFor(() =>
      expect(saveRule).toHaveBeenCalledWith(expect.objectContaining({ id: "r9", enabled: false })),
    );
  });

  it("warns when an enabled rule exists but no channel is enabled (silent failure)", async () => {
    stub({ channels: [], rules: [rule({ enabled: true })] });
    render(<Alerts />);
    expect(await screen.findByText("Your rules won't fire")).toBeInTheDocument();
  });

  it("does not warn once a channel is enabled", async () => {
    stub({
      channels: [channel({ kind: "pushover", enabled: true })],
      rules: [rule({ enabled: true })],
    });
    render(<Alerts />);
    await screen.findByText("Alert rules");
    expect(screen.queryByText("Your rules won't fire")).not.toBeInTheDocument();
  });

  it("does not warn when no rule is enabled, even with no channel", async () => {
    stub({ channels: [], rules: [rule({ enabled: false })] });
    render(<Alerts />);
    await screen.findByText("Alert rules");
    expect(screen.queryByText("Your rules won't fire")).not.toBeInTheDocument();
  });

  it("labels the for_seconds column 'Hold for' (self-documenting, matches the editor wording)", async () => {
    stub({ rules: [rule({ name: "CPU high" })] });
    render(<Alerts />);
    await screen.findByText("CPU high");
    expect(screen.getByRole("columnheader", { name: "Hold for" })).toBeInTheDocument();
  });

  it("shows an empty state when there are no rules", async () => {
    stub({ rules: [] });
    render(<Alerts />);
    expect(await screen.findByText("No rules yet")).toBeInTheDocument();
  });

  it("opens the rule editor when Add rule is clicked", async () => {
    stub();
    render(<Alerts />);
    fireEvent.click(await screen.findByRole("button", { name: "+ Add rule" }));
    expect(await screen.findByRole("dialog")).toHaveTextContent("Add alert rule");
  });
});
