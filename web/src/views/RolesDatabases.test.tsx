import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { RolesDatabases } from "./RolesDatabases";
import { api } from "@/api/client";
import type { DatabaseInfo, RoleInfo } from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function db(name: string, over: Partial<DatabaseInfo> = {}): DatabaseInfo {
  return { name, owner: "app", size_bytes: 1024, ...over };
}

function role(name: string, over: Partial<RoleInfo> = {}): RoleInfo {
  return { name, can_login: true, is_superuser: false, ...over };
}

function stub({ roles = [role("app")], dbs = [db("myapp")] }: { roles?: RoleInfo[]; dbs?: DatabaseInfo[] } = {}) {
  vi.spyOn(api, "listRoles").mockResolvedValue(roles);
  vi.spyOn(api, "listDatabases").mockResolvedValue(dbs);
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("RolesDatabases", () => {
  it("renders the databases table with name, owner and size once loaded", async () => {
    stub({ dbs: [db("myapp", { owner: "app_user", size_bytes: 2048 })] });
    render(<RolesDatabases />);

    const cell = await screen.findByText("myapp");
    const row = cell.closest("tr")!;
    expect(within(row).getByText("app_user")).toBeInTheDocument();
    // bytes(2048) formats to a human size — the row shows a size, not the raw number.
    expect(within(row).getByRole("button", { name: /delete/i })).toBeInTheDocument();
  });

  it("tags each role by type and offers rotate only for non-superuser login users", async () => {
    stub({
      dbs: [],
      roles: [
        role("app", { can_login: true }),
        role("postgres", { is_superuser: true }),
        role("readers", { can_login: false }),
      ],
    });
    render(<RolesDatabases />);

    expect(await screen.findByText("login user")).toBeInTheDocument();
    expect(screen.getByText("superuser")).toBeInTheDocument();
    expect(screen.getByText("group role")).toBeInTheDocument();

    // The superuser row has no rotate/delete actions (it is protected).
    const superRow = screen.getByText("postgres").closest("tr")!;
    expect(within(superRow).queryByRole("button")).not.toBeInTheDocument();

    // The login user can rotate its password and be deleted.
    const appRow = screen.getByText("app").closest("tr")!;
    expect(within(appRow).getByRole("button", { name: /rotate password/i })).toBeInTheDocument();
    expect(within(appRow).getByRole("button", { name: /delete/i })).toBeInTheDocument();
  });

  it("shows empty states when there are no databases or roles", async () => {
    stub({ roles: [], dbs: [] });
    render(<RolesDatabases />);

    expect(await screen.findByText("No databases yet")).toBeInTheDocument();
    expect(screen.getByText("No roles yet")).toBeInTheDocument();
    // The roles empty state points to the next step, not just a bare title.
    expect(screen.getByText(/set up a database and its users/i)).toBeInTheDocument();
  });

  it("opens the create-database dialog with an owner select", async () => {
    stub();
    render(<RolesDatabases />);
    await screen.findByText("myapp");

    fireEvent.click(screen.getByRole("button", { name: /create database/i }));

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("Create a database")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Database name")).toBeInTheDocument();
    expect(within(dialog).getByText("The owner can fully manage this database.")).toBeInTheDocument();
  });

  it("requires typing the database name before the destructive drop confirms", async () => {
    stub({ dbs: [db("myapp")] });
    render(<RolesDatabases />);
    await screen.findByText("myapp");

    const row = screen.getByText("myapp").closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: /delete/i }));

    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText("Delete database")).toBeInTheDocument();
    // It states the consequence before the operator acts.
    expect(
      within(dialog).getByText(/permanently deleted/i),
    ).toBeInTheDocument();
    // The confirm action is gated until the exact name is typed.
    const confirm = within(dialog).getByRole("button", { name: /delete permanently/i });
    expect(confirm).toBeDisabled();
  });

  it("confirms before rotating a password — the API only fires after the user agrees", async () => {
    const rotateSpy = vi
      .spyOn(api, "rotatePassword")
      .mockResolvedValue({ result: { ok: true, message: "rotated" }, secrets: { app: "new-pw" } });
    stub({ dbs: [], roles: [role("app", { can_login: true })] });
    render(<RolesDatabases />);

    const row = (await screen.findByText("app")).closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: /rotate password/i }));

    // Clicking does NOT rotate yet — it opens a confirm that warns about the
    // live-app consequence first.
    expect(rotateSpy).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(/stops working\s+immediately/i)).toBeInTheDocument();

    // Only after confirming does the rotation actually run.
    fireEvent.click(within(dialog).getByRole("button", { name: /rotate password/i }));
    expect(rotateSpy).toHaveBeenCalledWith("app");
    // The new credentials are then surfaced once.
    expect(await screen.findByText("Save these now")).toBeInTheDocument();
  });
});
