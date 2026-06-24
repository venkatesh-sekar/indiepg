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
        className={cn(
          widthClass[width],
          tone === "danger" && "ring-destructive/30",
        )}
      >
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
        </DialogHeader>
        {/* Only the body scrolls — the header and pinned close button stay put. */}
        <div className="max-h-[65vh] overflow-y-auto">{children}</div>
        {footer ? <DialogFooter>{footer}</DialogFooter> : null}
      </DialogContent>
    </Dialog>
  );
}
