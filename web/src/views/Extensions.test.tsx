import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { Extensions } from "./Extensions";
import { api } from "@/api/client";
import type { AvailableExtension, DatabaseInfo } from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

function availExt(name: string, over: Partial<AvailableExtension> = {}): AvailableExtension {
  return {
    name,
    description: `does ${name} things`,
    default_version: "1.0",
    tier: "ready",
    requires_preload: false,
    in_catalog: true,
    package: "",
    ...over,
  };
}

const DB: DatabaseInfo = { name: "postgres", owner: "postgres", size_bytes: 1024 };

// Stub the two loads the view fans out on mount: the database list and the
// per-database extension list. `available` is the only thing each test varies.
function stub(available: AvailableExtension[]) {
  vi.spyOn(api, "listDatabases").mockResolvedValue([DB]);
  vi.spyOn(api, "listExtensions").mockResolvedValue({
    database: "postgres",
    installed: [],
    available,
  });
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("Extensions — the Tier-3 (needs_restart) install gate", () => {
  it("gates the server-wide Postgres restart behind typing the EXACT extension name", async () => {
    // A needs_restart install runs `systemctl restart postgresql` — server-wide,
    // every database briefly down. It must never fire off a stray click.
    const installSpy = vi
      .spyOn(api, "installExtension")
      .mockResolvedValue({ ok: true, statements: ["systemctl restart postgresql"] });
    stub([
      availExt("pg_cron", {
        tier: "needs_restart",
        requires_preload: true,
        package: "postgresql-17-cron",
      }),
    ]);
    render(<Extensions />);

    // Open the Add dialog for the needs_restart extension (scoped to its row so
    // the "add by name" submit button — also labelled "Add" — can't be hit).
    const row = (await screen.findByText("pg_cron")).closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: /^add$/i }));

    const dialog = await screen.findByRole("dialog");

    // The consequence — a SERVER-WIDE restart — is stated before the operator can
    // act, not buried after. (States exactly what the action does.)
    expect(within(dialog).getByText(/this restarts postgres/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/whole server/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/unavailable for a few seconds/i)).toBeInTheDocument();

    const confirm = within(dialog).getByRole("button", { name: /install for me/i });
    const input = within(dialog).getByRole("textbox");

    // Disabled on open — the restart can't fire on a single click. This also
    // kills a `name.includes(typed)` / `name.startsWith(typed)` loosening: the
    // empty string is a substring AND a prefix of every name, so such a gate
    // would already be ENABLED here with nothing typed.
    expect(confirm).toBeDisabled();

    // A DIFFERENT extension's name must not authorize THIS one's restart (kills a
    // gate that ignores `typed` or compares against the wrong value).
    fireEvent.change(input, { target: { value: "postgis" } });
    expect(confirm).toBeDisabled();

    // A prefix/substring of the name ("pg") must not satisfy it either — pins the
    // `name.includes/startsWith(typed)` direction with a non-empty value too.
    fireEvent.change(input, { target: { value: "pg" } });
    expect(confirm).toBeDisabled();

    // A SUPERSTRING must not satisfy it: "pg_cron2" contains AND starts with
    // "pg_cron" but is not it. This pins EXACT equality — a fat-fingered extra
    // char, or a `typed.includes/startsWith(name)` loosening, must NOT fire the
    // irreversible server-wide restart.
    fireEvent.change(input, { target: { value: "pg_cron2" } });
    expect(confirm).toBeDisabled();

    // Surrounding whitespace is NOT trimmed here: the check is an exact
    // `typed === ext.name`, so a padded value stays disabled. (Locks the
    // exact-match contract; adding a `.trim()` is a deliberate, gated change.)
    fireEvent.change(input, { target: { value: " pg_cron " } });
    expect(confirm).toBeDisabled();

    // Only the exact name enables it.
    fireEvent.change(input, { target: { value: "pg_cron" } });
    expect(confirm).toBeEnabled();

    // The restart-triggering install fires only after the gate is satisfied, and
    // carries the typed name as the `confirm` token the server re-checks — a
    // dropped/blanked token would let the server refuse or the gate be bypassed.
    expect(installSpy).not.toHaveBeenCalled();
    fireEvent.click(confirm);
    expect(installSpy).toHaveBeenCalledWith({
      database: "postgres",
      name: "pg_cron",
      confirm: "pg_cron",
    });
  });

  it("adds a Tier-1 (ready) extension immediately — no dialog, no restart token", async () => {
    // The safe path: a single CREATE EXTENSION. It must NOT open the restart
    // dialog and must NEVER carry a confirm token that could authorize a restart.
    const installSpy = vi
      .spyOn(api, "installExtension")
      .mockResolvedValue({ ok: true, message: "Installed hstore." });
    stub([availExt("hstore", { tier: "ready" })]);
    render(<Extensions />);

    const row = (await screen.findByText("hstore")).closest("tr")!;
    fireEvent.click(within(row).getByRole("button", { name: /^add$/i }));

    // No confirm dialog is opened for a Tier-1 add...
    expect(screen.queryByRole("dialog")).toBeNull();
    // ...and it fires straight away with an EMPTY confirm — never a restart token.
    expect(installSpy).toHaveBeenCalledWith({
      database: "postgres",
      name: "hstore",
      confirm: "",
    });
  });
});
