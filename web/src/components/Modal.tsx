// Modal: a thin wrapper over the shadcn Dialog that keeps the panel's
// `open/title/onClose/children/footer/tone/width` contract. Radix handles the
// focus trap, Escape-to-close, backdrop dismiss and focus restore that this
// component used to hand-roll.

import { type ReactNode } from "react";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { cn } from "@/lib/utils";

interface ModalProps {
  open: boolean;
  title: string;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  /** Visual emphasis for destructive flows. */
  tone?: "default" | "danger";
  /** Width hint. */
  width?: "sm" | "md" | "lg";
  /**
   * When `false`, the dialog can only be closed by an explicit action inside
   * `children`/`footer` — Escape, a backdrop click and the corner X are all
   * disabled. Use for one-time content that's destroyed on close (e.g. secrets
   * shown only once) so a reflexive dismiss can't lose it. Defaults to `true`.
   */
  dismissible?: boolean;
}

const widthClass: Record<NonNullable<ModalProps["width"]>, string> = {
  sm: "sm:max-w-md",
  md: "sm:max-w-xl",
  lg: "sm:max-w-3xl",
};

export function Modal({
  open,
  title,
  onClose,
  children,
  footer,
  tone = "default",
  width = "md",
  dismissible = true,
}: ModalProps) {
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // Radix fires this for Escape, backdrop click and the close button.
        // A no-op `onClose` (e.g. while busy) keeps the dialog open, matching
        // the old behaviour.
        if (!next) onClose();
      }}
    >
      <DialogContent
        // Children are free-form (forms, paragraphs); opt out of Radix's
        // auto-generated description reference rather than ship a dangling id.
        aria-describedby={undefined}
        data-tone={tone}
        // When not dismissible, drop the corner X and swallow Escape / backdrop
        // dismissal so the only way out is the explicit action in the footer.
        showCloseButton={dismissible}
        onEscapeKeyDown={dismissible ? undefined : (e) => e.preventDefault()}
        onInteractOutside={dismissible ? undefined : (e) => e.preventDefault()}
        className={cn(
          widthClass[width],
          tone === "danger" && "ring-destructive/30",
        )}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        {/* Only the body scrolls — the header and pinned close button stay put.
            Pull the scroll area out to the dialog's inner edges (-mx-4) so the
            scrollbar sits flush, then re-pad (px-4) to restore a 16px gutter on
            both sides. Without the gutter, `overflow-y-auto` also clips overflow-x,
            shaving the 1px ring off any card/section flush against the edge. */}
        <div className="-mx-4 max-h-[65vh] overflow-y-auto px-4">{children}</div>
        {footer ? <DialogFooter>{footer}</DialogFooter> : null}
      </DialogContent>
    </Dialog>
  );
}
