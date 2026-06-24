import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { ConfirmDialog, TypedConfirmDialog } from "./ConfirmDialog";

// These dialogs are the panel's last line of defence before a notable or
// irreversible action. The tests below lock in two invariants the usability
// audit relies on:
//   1. Every dialog states the consequence in plain language before acting.
//   2. The destructive (typed-name) dialog cannot fire until the operator has
//      typed the exact object name — a future refactor must not weaken that gate.

describe("ConfirmDialog", () => {
  it("renders the title, message, and confirm/cancel handlers", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Run a full backup?"
        message="This copies the entire database to your bucket."
        confirmLabel="Start backup"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    expect(screen.getByText("Run a full backup?")).toBeInTheDocument();
    expect(
      screen.getByText("This copies the entire database to your bucket."),
    ).toBeInTheDocument();

    fireEvent.click(screen.getByText("Start backup"));
    expect(onConfirm).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it("uses the danger tone for destructive confirmations", () => {
    render(
      <ConfirmDialog
        open
        title="Delete this alert rule?"
        message="You'll no longer be notified."
        confirmLabel="Delete rule"
        tone="danger"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.getByText("Delete rule")).toHaveClass("btn-danger");
  });

  it("shows a working state and disables both buttons while busy", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog
        open
        title="Run a full backup?"
        message="This copies the entire database to your bucket."
        confirmLabel="Start backup"
        busy
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    );

    const confirm = screen.getByText("Working…");
    expect(confirm).toBeDisabled();
    expect(screen.getByText("Cancel")).toBeDisabled();

    // A disabled confirm must not fire even if clicked.
    fireEvent.click(confirm);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("renders nothing when closed", () => {
    render(
      <ConfirmDialog
        open={false}
        title="Run a full backup?"
        message="hidden"
        onConfirm={vi.fn()}
        onCancel={vi.fn()}
      />,
    );
    expect(screen.queryByText("hidden")).toBeNull();
  });
});

describe("TypedConfirmDialog", () => {
  function setup(over: Partial<Parameters<typeof TypedConfirmDialog>[0]> = {}) {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <TypedConfirmDialog
        open
        title="Delete database"
        objectName="orders"
        objectKind="database"
        consequence="Every table and row in this database will be permanently deleted."
        onConfirm={onConfirm}
        onCancel={onCancel}
        {...over}
      />,
    );
    return { onConfirm, onCancel };
  }

  it("states exactly what will be destroyed and that it cannot be undone", () => {
    setup();
    expect(
      screen.getByText(/This permanently removes the database/),
    ).toBeInTheDocument();
    expect(screen.getByText(/cannot be undone/)).toBeInTheDocument();
    // The object name is named explicitly (in the warning and in the prompt).
    expect(screen.getAllByText("orders").length).toBeGreaterThan(0);
    // The caller's consequence text is surfaced inside a destructive alert. Query
    // the container by variant so the assertion proves the structural invariant
    // ("shown in a danger callout") rather than which text node happens to carry
    // it — a child wrapper added later must not silently pass this.
    const callout = document.querySelector('[data-variant="destructive"]');
    expect(callout).toBeInTheDocument();
    expect(callout).toHaveTextContent(
      "Every table and row in this database will be permanently deleted.",
    );
  });

  it("keeps the delete button disabled until the exact name is typed", () => {
    const { onConfirm } = setup();
    const button = screen.getByText("Delete permanently");
    const input = screen.getByRole("textbox");

    // Disabled out of the gate.
    expect(button).toBeDisabled();

    // A near-miss does not unlock it, and marks the input invalid.
    fireEvent.change(input, { target: { value: "order" } });
    expect(button).toBeDisabled();
    expect(input).toHaveAttribute("aria-invalid", "true");

    // Clicking while disabled is a no-op (defence in depth).
    fireEvent.click(button);
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it("fires onConfirm with the typed name only on an exact match", () => {
    const { onConfirm } = setup();
    const button = screen.getByText("Delete permanently");
    const input = screen.getByRole("textbox");

    fireEvent.change(input, { target: { value: "orders" } });
    expect(button).not.toBeDisabled();
    // Only assert the input is not flagged invalid — not the exact serialisation,
    // so tightening to the spec-recommended "omit when valid" stays green.
    expect(input).not.toHaveAttribute("aria-invalid", "true");

    fireEvent.click(button);
    expect(onConfirm).toHaveBeenCalledWith("orders");
  });

  it("does not fire while busy, even with a matching name", () => {
    const { onConfirm } = setup({ busy: true });
    // Type the exact name so only `busy` can be gating the action.
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "orders" } });
    // While busy the button shows the working label and stays disabled.
    const button = screen.getByText("Working…");
    expect(button).toBeDisabled();
    fireEvent.click(button);
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
