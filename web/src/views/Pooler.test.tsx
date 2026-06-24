import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { PoolerPanel } from "./Pooler";
import { api, ApiError } from "@/api/client";
import type { PoolRecommendation, PoolerStatus, RoleInfo } from "@/api/types";

function pool(over: Partial<PoolRecommendation> = {}): PoolRecommendation {
  return {
    profile: "mixed",
    pg_max_connections: 100,
    default_pool_size: 66,
    min_pool_size: 16,
    reserve_pool_size: 13,
    max_client_conn: 660,
    server_idle_timeout: 300,
    ...over,
  };
}

function status(over: Partial<PoolerStatus> = {}): PoolerStatus {
  return {
    enabled: false,
    host: "127.0.0.1",
    listen_port: 6432,
    pool: pool(),
    ...over,
  };
}

function role(name: string, over: Partial<RoleInfo> = {}): RoleInfo {
  return { name, can_login: true, is_superuser: false, ...over };
}

function renderPanel(props: Partial<Parameters<typeof PoolerPanel>[0]> = {}) {
  const onChanged = vi.fn();
  render(
    <PoolerPanel
      status={status()}
      roles={[role("app")]}
      onChanged={onChanged}
      {...props}
    />,
  );
  return { onChanged };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("PoolerPanel — enabled", () => {
  it("shows the loopback connection address and the live pool sizing", () => {
    renderPanel({ status: status({ enabled: true }) });
    expect(screen.getByText("The connection pooler is on")).toBeInTheDocument();
    expect(screen.getByText("127.0.0.1:6432")).toBeInTheDocument();
    // A pool figure is rendered (max_client_conn).
    expect(screen.getByText("660")).toBeInTheDocument();
    // No enable affordance once it is on.
    expect(
      screen.queryByRole("button", { name: /enable connection pooler/i }),
    ).not.toBeInTheDocument();
  });

  it("still reports on when pool sizing is unavailable (PG unreachable)", () => {
    renderPanel({ status: status({ enabled: true, pool: null }) });
    expect(screen.getByText("The connection pooler is on")).toBeInTheDocument();
    expect(screen.getByText("Pool sizing unavailable")).toBeInTheDocument();
  });
});

describe("PoolerPanel — disabled", () => {
  it("previews the address and sizing, and lists only non-superuser login roles", () => {
    renderPanel({
      roles: [
        role("app"),
        role("readonly"),
        role("postgres", { is_superuser: true }),
        role("daemon", { can_login: false }),
      ],
    });
    expect(screen.getByText("A connection pooler is optional")).toBeInTheDocument();
    expect(screen.getByText("127.0.0.1:6432")).toBeInTheDocument();
    expect(screen.getByText("Pool sizing for this server")).toBeInTheDocument();
    // Eligible app roles are offered; superuser and non-login roles are not.
    expect(screen.getByLabelText("app")).toBeInTheDocument();
    expect(screen.getByLabelText("readonly")).toBeInTheDocument();
    expect(screen.queryByLabelText("postgres")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("daemon")).not.toBeInTheDocument();
  });

  it("keeps the enable button disabled until at least one role is chosen", () => {
    renderPanel({ roles: [role("app")] });
    const btn = screen.getByRole("button", { name: /enable connection pooler/i });
    expect(btn).toBeDisabled();
    fireEvent.click(screen.getByLabelText("app"));
    expect(btn).toBeEnabled();
  });

  it("disables enabling when Postgres is unreachable even with a role selected", () => {
    renderPanel({ status: status({ pool: null }), roles: [role("app")] });
    expect(screen.getByText("Postgres is unreachable")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("app"));
    expect(
      screen.getByRole("button", { name: /enable connection pooler/i }),
    ).toBeDisabled();
  });

  it("shows a loading state (not the empty state) while roles are still loading", () => {
    renderPanel({ roles: [], rolesLoading: true });
    expect(screen.getByText("Loading roles…")).toBeInTheDocument();
    // Must NOT claim there are no app roles while the list is still in flight.
    expect(screen.queryByText("No app roles to route yet")).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /enable connection pooler/i }),
    ).toBeDisabled();
  });

  it("shows an empty state and no enable when there are no app roles", () => {
    renderPanel({ roles: [role("postgres", { is_superuser: true })] });
    expect(screen.getByText("No app roles to route yet")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /enable connection pooler/i }),
    ).toBeDisabled();
  });

  it("confirm dialog states exactly what enabling does, then enables on confirm", async () => {
    const spy = vi
      .spyOn(api, "enablePooler")
      .mockResolvedValue({
        pooled_roles: ["app"],
        pool: pool(),
        config_changed: true,
        userlist_changed: true,
        reloaded: false,
        running: true,
      });
    const { onChanged } = renderPanel({ roles: [role("app")] });

    fireEvent.click(screen.getByLabelText("app"));
    fireEvent.click(
      screen.getByRole("button", { name: /enable connection pooler/i }),
    );

    // The confirm spells out the three system effects + the routed role.
    expect(screen.getByText("Enable the connection pooler?")).toBeInTheDocument();
    expect(screen.getByText(/Install the PgBouncer package/)).toBeInTheDocument();
    expect(screen.getByText(/Start the PgBouncer service/)).toBeInTheDocument();
    expect(
      screen.getByText(/Allow 1 role to connect through the pooler/),
    ).toBeInTheDocument();
    expect(
      screen.getByText((_, el) => el?.tagName === "STRONG" && el.textContent === "app"),
    ).toBeInTheDocument();
    // Honest about the blast radius: no Postgres restart, no data touched.
    expect(screen.getByText(/does not touch your data/i)).toBeInTheDocument();
    // Explicit that the user must repoint apps — the pooler does not reroute them.
    expect(
      screen.getByText(/won't move any app over by itself/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/change an app's connection string/i),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Enable pooler" }));

    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    // Roles are sent; max_connections is NOT (server sizes the pool).
    expect(spy).toHaveBeenCalledWith({ roles: ["app"] });
  });

  it("surfaces a failed enable without closing the dialog or signalling success", async () => {
    vi.spyOn(api, "enablePooler").mockRejectedValue(
      new ApiError(409, {
        code: "conflict",
        message: "a foreign pgbouncer.ini is present",
      }),
    );
    const { onChanged } = renderPanel({ roles: [role("app")] });

    fireEvent.click(screen.getByLabelText("app"));
    fireEvent.click(
      screen.getByRole("button", { name: /enable connection pooler/i }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Enable pooler" }));

    await waitFor(() =>
      expect(
        screen.getByText("a foreign pgbouncer.ini is present"),
      ).toBeInTheDocument(),
    );
    expect(onChanged).not.toHaveBeenCalled();
    // Dialog stays open so the operator can read the error.
    expect(screen.getByText("Enable the connection pooler?")).toBeInTheDocument();
  });
});

describe("PoolerPanel — disabling (enabled view)", () => {
  it("offers a disable button only when the pooler is on", () => {
    // On: the disable affordance is present.
    const { rerender } = render(
      <PoolerPanel
        status={status({ enabled: true })}
        roles={[role("app")]}
        onChanged={vi.fn()}
      />,
    );
    expect(
      screen.getByRole("button", { name: /disable connection pooler/i }),
    ).toBeInTheDocument();

    // Off: no disable affordance (the off view offers enable instead).
    rerender(
      <PoolerPanel status={status()} roles={[role("app")]} onChanged={vi.fn()} />,
    );
    expect(
      screen.queryByRole("button", { name: /disable connection pooler/i }),
    ).not.toBeInTheDocument();
  });

  it("confirm dialog states what stops, then disables on confirm", async () => {
    const spy = vi.spyOn(api, "disablePooler").mockResolvedValue(
      status({ enabled: false, pool: null }),
    );
    const { onChanged } = renderPanel({ status: status({ enabled: true }) });

    fireEvent.click(
      screen.getByRole("button", { name: /disable connection pooler/i }),
    );

    // The confirm spells out the consequences before it happens.
    expect(screen.getByText("Disable the connection pooler?")).toBeInTheDocument();
    expect(screen.getByText(/Stop the PgBouncer service/)).toBeInTheDocument();
    expect(screen.getByText(/Prevent it from starting again on reboot/)).toBeInTheDocument();
    expect(screen.getByText(/will fail to connect/i)).toBeInTheDocument();
    // Honest about the blast radius: no Postgres restart, no data touched.
    expect(screen.getByText(/does not touch your data/i)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Disable pooler" }));

    await waitFor(() => expect(onChanged).toHaveBeenCalled());
    expect(spy).toHaveBeenCalledTimes(1);
  });

  it("surfaces a failed disable without closing the dialog or signalling success", async () => {
    vi.spyOn(api, "disablePooler").mockRejectedValue(
      new ApiError(500, {
        code: "exec",
        message: "disabling the pgbouncer service failed",
      }),
    );
    const { onChanged } = renderPanel({ status: status({ enabled: true }) });

    fireEvent.click(
      screen.getByRole("button", { name: /disable connection pooler/i }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Disable pooler" }));

    await waitFor(() =>
      expect(
        screen.getByText("disabling the pgbouncer service failed"),
      ).toBeInTheDocument(),
    );
    expect(onChanged).not.toHaveBeenCalled();
    // Dialog stays open so the operator can read the error.
    expect(screen.getByText("Disable the connection pooler?")).toBeInTheDocument();
  });
});
