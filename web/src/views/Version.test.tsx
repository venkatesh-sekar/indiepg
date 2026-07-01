import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, within } from "@testing-library/react";
import { PendingFinalizationBanner } from "./Version";
import { api } from "@/api/client";
import type { PendingFinalization, UpgradeStatus } from "@/api/types";

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

// from_major != to_major on purpose: the two dialogs demand DIFFERENT numbers
// (finalize wants the OLD major it deletes; rollback wants the NEW major it
// abandons). Distinct values let each test prove its gate rejects the *other*
// dialog's number, which is exactly the cross-wire mutation we must catch.
function pending(over: Partial<PendingFinalization> = {}): PendingFinalization {
  return {
    from_major: 16,
    to_major: 17,
    reclaimable_bytes: 1024 * 1024 * 1024,
    upgraded_at: "2026-07-01T00:00:00Z",
    ...over,
  };
}

const NO_OP_STATUS: UpgradeStatus = { operation: null, pending_finalization: null };

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("PendingFinalizationBanner — irreversible upgrade confirm gates", () => {
  it("gates finalize behind typing the OLD major, then finalizes with it", async () => {
    const finalizeSpy = vi.spyOn(api, "finalizeUpgrade").mockResolvedValue(NO_OP_STATUS);
    render(<PendingFinalizationBanner pending={pending()} onChanged={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /finalize \(reclaim/i }));
    const dialog = await screen.findByRole("dialog");

    // The copy states the irreversible consequence BEFORE the operator acts.
    expect(within(dialog).getByText(/point of no return/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/cannot roll back/i)).toBeInTheDocument();

    const confirm = within(dialog).getByRole("button", { name: /finalize & reclaim space/i });
    const input = within(dialog).getByRole("textbox");

    // Disabled on open — no accidental one-click drop.
    expect(confirm).toBeDisabled();

    // Typing the NEW major (17) — the value ROLLBACK wants, not this dialog —
    // must NOT enable finalize. Catches a from/to cross-wire.
    fireEvent.change(input, { target: { value: "17" } });
    expect(confirm).toBeDisabled();

    // A superstring of the old major must NOT satisfy the gate: "169" contains
    // "16" but is not it. This pins EXACT equality — a loosened gate (.includes/
    // .startsWith/Number coercion) would let a fat-fingered "169" fire the
    // irreversible deletion.
    fireEvent.change(input, { target: { value: "169" } });
    expect(confirm).toBeDisabled();

    // A numeric-equivalent-but-non-exact spelling must also be rejected: the gate
    // is an exact-string attention check, not numeric coercion — Number("16.0")
    // === 16 must NOT pass it.
    fireEvent.change(input, { target: { value: "16.0" } });
    expect(confirm).toBeDisabled();

    // Only the exact OLD major enables it (surrounding whitespace is trimmed,
    // so the gate can't be defeated or broken by a stray space).
    fireEvent.change(input, { target: { value: " 16 " } });
    expect(confirm).toBeEnabled();

    // The destructive API fires only after the gate is satisfied, and with the
    // OLD major as the confirm token.
    expect(finalizeSpy).not.toHaveBeenCalled();
    fireEvent.click(confirm);
    expect(finalizeSpy).toHaveBeenCalledWith(16);
  });

  it("gates rollback behind typing the NEW major (the one abandoned), then rolls back with it", async () => {
    const rollbackSpy = vi.spyOn(api, "rollbackUpgrade").mockResolvedValue(NO_OP_STATUS);
    render(<PendingFinalizationBanner pending={pending()} onChanged={vi.fn()} />);

    // The banner's rollback button is labelled with the OLD major ("Roll back to
    // 16"); the dialog it opens is scoped separately below so the two don't clash.
    fireEvent.click(screen.getByRole("button", { name: /^roll back to 16$/i }));
    const dialog = await screen.findByRole("dialog");

    // The copy warns about permanent data loss BEFORE the operator acts.
    expect(within(dialog).getByText(/will be lost/i)).toBeInTheDocument();
    expect(within(dialog).getByText(/discarded and cannot be recovered/i)).toBeInTheDocument();

    const confirm = within(dialog).getByRole("button", { name: /^roll back to 16$/i });
    const input = within(dialog).getByRole("textbox");

    // Disabled on open.
    expect(confirm).toBeDisabled();

    // Typing the OLD major (16) — the value FINALIZE wants, not this dialog —
    // must NOT enable rollback. Catches a to/from cross-wire.
    fireEvent.change(input, { target: { value: "16" } });
    expect(confirm).toBeDisabled();

    // A superstring of the new major must NOT satisfy the gate: "179" contains
    // "17" but is not it. Pins EXACT equality so a loosened gate can't let a
    // fat-fingered "179" fire the permanent-data-loss rollback.
    fireEvent.change(input, { target: { value: "179" } });
    expect(confirm).toBeDisabled();

    // Numeric-equivalent-but-non-exact spelling must also be rejected — exact
    // string, not numeric coercion (Number("17.0") === 17 must NOT pass).
    fireEvent.change(input, { target: { value: "17.0" } });
    expect(confirm).toBeDisabled();

    // Only the exact NEW major (the version being abandoned) enables it; the
    // surrounding whitespace proves the trim.
    fireEvent.change(input, { target: { value: " 17 " } });
    expect(confirm).toBeEnabled();

    // The data-discarding API fires only after the gate is satisfied, and with
    // the NEW major as the confirm token.
    expect(rollbackSpy).not.toHaveBeenCalled();
    fireEvent.click(confirm);
    expect(rollbackSpy).toHaveBeenCalledWith(17);
  });
});
