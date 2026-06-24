import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Settings } from "./Settings";
import { api } from "@/api/client";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function stub() {
  // The embedded DatabaseTuning + Pooler subviews fetch on mount; stub them so
  // the page renders without their network calls interfering.
  vi.spyOn(api, "getTuning").mockResolvedValue({
    memory_mb: 4096,
    cpu_count: 2,
    active_profile: "mixed",
    applied: null,
    profiles: [],
  });
  vi.spyOn(api, "listRoles").mockResolvedValue([]);
  vi.spyOn(api, "poolerStatus").mockResolvedValue({
    enabled: false,
    host: "",
    listen_port: 0,
    pool: null,
  });
}

beforeEach(() => {
  vi.restoreAllMocks();
});

function renderSettings() {
  return render(
    <MemoryRouter>
      <Settings />
    </MemoryRouter>,
  );
}

describe("Settings", () => {
  it("hosts database tuning and the connection pooler", async () => {
    stub();
    renderSettings();

    expect(
      await screen.findByText("Database tuning (host-sized)"),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Connection pooler (PgBouncer)"),
    ).toBeInTheDocument();
  });

  it("no longer hosts the backup-storage form and points to the Backups page", () => {
    stub();
    renderSettings();

    // Backup storage moved to /backups — the S3 form is not on Settings anymore.
    expect(screen.queryByLabelText("Endpoint")).not.toBeInTheDocument();
    const link = screen.getByRole("link", { name: /backups page/i });
    expect(link).toHaveAttribute("href", "/backups");
  });
});
