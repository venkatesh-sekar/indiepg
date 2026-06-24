import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { Settings } from "./Settings";
import { api } from "@/api/client";
import type { ConfigResponse } from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function config(over: Partial<ConfigResponse> = {}): ConfigResponse {
  return {
    config: {
      bind_addr: "127.0.0.1:8443",
      force_public_bind: false,
      otlp_endpoint: "",
      otlp_insecure: false,
      stanza: "main",
      backup: {
        endpoint: "",
        region: "",
        bucket: "",
        prefix: "",
        access_key: "",
        use_ssl: true,
      },
      retention_days: 14,
      schedules: {
        full_backup: "",
        incremental_backup: "",
        restore_test: "",
        telemetry_sample: "",
        digest: "",
      },
      statement_timeout: 0,
      query_limit: 1000,
      pg_socket_dir: "/var/run/postgresql",
    },
    backup_secret_is_set: false,
    backup_cipher_is_set: false,
    ...over,
  };
}

function stub(cfg: ConfigResponse = config()) {
  vi.spyOn(api, "getConfig").mockResolvedValue(cfg);
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
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("Settings", () => {
  it("renders the backup form with a 'Not configured' badge when no credentials are saved", async () => {
    stub();
    render(<Settings />);

    expect(await screen.findByLabelText("Endpoint")).toBeInTheDocument();
    expect(screen.getByText("Not configured")).toBeInTheDocument();
    // No target set yet → the on-disk warning callout is shown.
    expect(
      screen.getByText(/Backups are currently stored on this server/i),
    ).toBeInTheDocument();
  });

  it("labels the save button 'Save' with no target and 'Save & connect' once a bucket is entered", async () => {
    stub();
    render(<Settings />);

    await screen.findByLabelText("Endpoint");
    expect(screen.getByRole("button", { name: "Save" })).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Bucket"), {
      target: { value: "my-backups" },
    });
    expect(
      screen.getByRole("button", { name: "Save & connect" }),
    ).toBeInTheDocument();
  });

  it("shows a saved badge and keeps the secret field blank when credentials are stored", async () => {
    stub(
      config({
        backup_secret_is_set: true,
        config: {
          ...config().config,
          backup: { ...config().config.backup, bucket: "existing" },
        },
      }),
    );
    render(<Settings />);

    expect(await screen.findByText("Credentials saved")).toBeInTheDocument();
    const secret = screen.getByLabelText(/Secret access key/i) as HTMLInputElement;
    expect(secret.value).toBe("");
    expect(secret).toHaveAttribute("placeholder", "Leave blank to keep current");
  });

  it("only sends a new secret when one is typed, preserving the stored credential", async () => {
    stub(
      config({
        backup_secret_is_set: true,
        config: {
          ...config().config,
          backup: {
            endpoint: "s3.example.com",
            region: "us-east-1",
            bucket: "kept",
            prefix: "",
            access_key: "AKIA",
            use_ssl: true,
          },
        },
      }),
    );
    const update = vi
      .spyOn(api, "updateConfig")
      .mockResolvedValue(config({ backup_secret_is_set: true, backup_configured: true }));
    render(<Settings />);

    await screen.findByText("Credentials saved");
    fireEvent.click(screen.getByRole("button", { name: "Save & connect" }));

    await waitFor(() => expect(update).toHaveBeenCalledTimes(1));
    const sent = update.mock.calls[0][0];
    // Blank secret field → no secret_key in the request (stored value preserved).
    expect(sent.backup?.secret_key).toBeUndefined();
    expect(sent.backup?.bucket).toBe("kept");
  });
});
