// Two confirmation dialogs, built on the shadcn AlertDialog (Radix):
//   - ConfirmDialog: a plain "are you sure?" for non-destructive but notable acts.
//   - TypedConfirmDialog: destructive ops that require the operator to type the
//     exact object name (mirrors core.RequireConfirmation on the server).
//
// AlertDialog (not Dialog) is the right primitive here: it has no click-outside
// dismiss, traps focus, and forces an explicit Confirm/Cancel choice. Both
// dialogs are controlled via `open`; the parent decides when to close, so the
// action handlers preventDefault to keep that control rather than letting Radix
// auto-close. Escape is the only built-in dismiss and is ignored while busy.

import { useEffect, useState, type ReactNode } from "react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Field, FieldLabel } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
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
    <AlertDialog
      open={open}
      // Escape is the only built-in dismiss (no backdrop click). Ignore it while
      // busy, matching the old no-op onClose.
      onOpenChange={(next) => {
        if (!next && !busy) onCancel();
      }}
    >
      <AlertDialogContent
        aria-busy={busy}
        className={cn(tone === "danger" && "ring-destructive/30")}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          {/* Message is free-form (may contain paragraphs/lists); render it as a
              div so the wired description still allows block content. */}
          <AlertDialogDescription asChild>
            <div>{message}</div>
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel
            disabled={busy}
            onClick={(e) => {
              e.preventDefault();
              onCancel();
            }}
          >
            {cancelLabel}
          </AlertDialogCancel>
          <AlertDialogAction
            variant={tone === "danger" ? "destructive" : "default"}
            disabled={busy}
            onClick={(e) => {
              e.preventDefault();
              onConfirm();
            }}
          >
            {busy ? "Working…" : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
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
    <AlertDialog
      open={open}
      onOpenChange={(next) => {
        if (!next && !busy) onCancel();
      }}
    >
      <AlertDialogContent aria-busy={busy} className="ring-destructive/30">
        <AlertDialogHeader>
          <AlertDialogTitle>{title}</AlertDialogTitle>
          <AlertDialogDescription>
            This permanently removes the {objectKind}{" "}
            <strong>{objectName}</strong>. This cannot be undone.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {consequence ? <Callout tone="danger">{consequence}</Callout> : null}
        <Field>
          <FieldLabel htmlFor="typed-confirm-name">
            Type <code>{objectName}</code> to confirm
          </FieldLabel>
          <Input
            id="typed-confirm-name"
            type="text"
            autoComplete="off"
            autoCorrect="off"
            spellCheck={false}
            value={typed}
            placeholder={objectName}
            onChange={(e) => setTyped(e.target.value)}
            aria-invalid={typed.length > 0 && !matches}
          />
        </Field>
        <AlertDialogFooter>
          <AlertDialogCancel
            disabled={busy}
            onClick={(e) => {
              e.preventDefault();
              onCancel();
            }}
          >
            Cancel
          </AlertDialogCancel>
          <AlertDialogAction
            variant="destructive"
            disabled={!matches || busy}
            onClick={(e) => {
              e.preventDefault();
              onConfirm(typed);
            }}
          >
            {busy ? "Working…" : confirmLabel}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  );
}
