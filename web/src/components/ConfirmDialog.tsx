// Two confirmation dialogs:
//   - ConfirmDialog: a plain "are you sure?" for non-destructive but notable acts.
//   - TypedConfirmDialog: destructive ops that require the operator to type the
//     exact object name (mirrors core.RequireConfirmation on the server).

import { useEffect, useState, type ReactNode } from "react";
import { Modal } from "./Modal";
import { Callout } from "./ui";

interface ConfirmDialogProps {
  open: boolean;
  title: string;
  /** Plain-language explanation. No jargon — the operator may not be a PG expert. */
  message: ReactNode;
  confirmLabel?: string;
  cancelLabel?: string;
  tone?: "default" | "danger";
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmDialog({
  open,
  title,
  message,
  confirmLabel = "Confirm",
  cancelLabel = "Cancel",
  tone = "default",
  busy = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  return (
    <Modal
      open={open}
      title={title}
      tone={tone}
      width="sm"
      onClose={busy ? () => undefined : onCancel}
      footer={
        <>
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            {cancelLabel}
          </button>
          <button
            type="button"
            className={tone === "danger" ? "btn btn-danger" : "btn btn-primary"}
            onClick={onConfirm}
            disabled={busy}
          >
            {busy ? "Working…" : confirmLabel}
          </button>
        </>
      }
    >
      <div className="confirm-message">{message}</div>
    </Modal>
  );
}

interface TypedConfirmDialogProps {
  open: boolean;
  title: string;
  /** The object the action will destroy, e.g. a database or role name. */
  objectName: string;
  /** What kind of object it is, used in the plain-language warning. */
  objectKind: string;
  /** Extra explanation of the consequences. */
  consequence?: ReactNode;
  confirmLabel?: string;
  busy?: boolean;
  onConfirm: (typed: string) => void;
  onCancel: () => void;
}

/**
 * Destructive confirmation: the action button stays disabled until the operator
 * types the exact object name, matching the server's typed-name confirmation.
 */
export function TypedConfirmDialog({
  open,
  title,
  objectName,
  objectKind,
  consequence,
  confirmLabel = "Delete permanently",
  busy = false,
  onConfirm,
  onCancel,
}: TypedConfirmDialogProps) {
  const [typed, setTyped] = useState("");

  useEffect(() => {
    if (open) setTyped("");
  }, [open, objectName]);

  const matches = typed === objectName;

  return (
    <Modal
      open={open}
      title={title}
      tone="danger"
      width="sm"
      onClose={busy ? () => undefined : onCancel}
      footer={
        <>
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            type="button"
            className="btn btn-danger"
            disabled={!matches || busy}
            onClick={() => onConfirm(typed)}
          >
            {busy ? "Working…" : confirmLabel}
          </button>
        </>
      }
    >
      <p className="confirm-message">
        This permanently removes the {objectKind}{" "}
        <strong>{objectName}</strong>. This cannot be undone.
      </p>
      {consequence ? <Callout tone="danger">{consequence}</Callout> : null}
      <label className="field">
        <span className="field-label">
          Type <code>{objectName}</code> to confirm
        </span>
        <input
          type="text"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          value={typed}
          placeholder={objectName}
          onChange={(e) => setTyped(e.target.value)}
          aria-invalid={typed.length > 0 && !matches}
        />
      </label>
    </Modal>
  );
}
