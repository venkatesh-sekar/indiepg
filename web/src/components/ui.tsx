// Small presentational primitives reused across views. Kept in one file to keep
// the component tree shallow and the bundle lean.

import { useState, type ComponentProps, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Badge as ShadcnBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Spinner as ShadcnSpinner } from "@/components/ui/spinner";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";
import {
  Card as ShadcnCard,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from "@/components/ui/empty";

// --- Badges ----------------------------------------------------------------

type BadgeTone = "neutral" | "ok" | "warn" | "danger" | "info" | "readonly";

/** Maps the panel's semantic tones onto the shadcn Badge variants. */
const toneVariant: Record<
  BadgeTone,
  "secondary" | "success" | "warning" | "destructive" | "info"
> = {
  neutral: "secondary",
  ok: "success",
  warn: "warning",
  danger: "destructive",
  info: "info",
  readonly: "info",
};

export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: BadgeTone;
}) {
  return <ShadcnBadge variant={toneVariant[tone]}>{children}</ShadcnBadge>;
}

/** Prominent "read-only" affordance for the query box and read paths. */
export function ReadOnlyBadge() {
  return (
    <ShadcnBadge variant="info" title="This connection cannot modify data.">
      Read-only
    </ShadcnBadge>
  );
}

/** Maps a backup/result string to a colored badge. */
export function ResultBadge({ result }: { result: string }) {
  const r = result.toLowerCase();
  if (r === "ok" || r === "success" || r === "completed" || r === "pass") {
    return <Badge tone="ok">{result}</Badge>;
  }
  if (r === "running" || r === "pending" || r.includes("ing")) {
    return <Badge tone="info">{result}</Badge>;
  }
  if (r === "fail" || r === "failed" || r === "error") {
    return <Badge tone="danger">{result}</Badge>;
  }
  return <Badge>{result}</Badge>;
}

// --- Cards -----------------------------------------------------------------

export function Card({
  title,
  children,
  actions,
  className,
}: {
  title?: ReactNode;
  children: ReactNode;
  actions?: ReactNode;
  className?: string;
}) {
  return (
    <ShadcnCard className={className}>
      {title || actions ? (
        <CardHeader>
          {title ? <CardTitle>{title}</CardTitle> : <span />}
          {actions ? <CardAction>{actions}</CardAction> : null}
        </CardHeader>
      ) : null}
      <CardContent>{children}</CardContent>
    </ShadcnCard>
  );
}

// --- Callouts --------------------------------------------------------------

/** Maps the panel's callout tones onto the shadcn Alert variants. */
const calloutVariant = {
  info: "info",
  warn: "warning",
  danger: "destructive",
  ok: "success",
} as const;

export function Callout({
  tone = "info",
  title,
  children,
  ...props
}: {
  tone?: "info" | "warn" | "danger" | "ok";
  title?: ReactNode;
  children: ReactNode;
} & ComponentProps<typeof Alert>) {
  return (
    <Alert variant={calloutVariant[tone]} {...props}>
      {title ? <AlertTitle>{title}</AlertTitle> : null}
      <AlertDescription>{children}</AlertDescription>
    </Alert>
  );
}

// --- States ----------------------------------------------------------------

export function Spinner({ label }: { label?: string }) {
  return (
    <div
      className="flex items-center gap-2.5 px-1 py-6 text-muted-foreground"
      role="status"
      aria-live="polite"
      aria-atomic="true"
    >
      {/* Decorative — the row's status role owns the announcement, so the
          icon's own role="status"/aria-label are suppressed to avoid a nested
          live region. (props spread last in the primitive, so this wins.) */}
      <ShadcnSpinner role="presentation" aria-hidden="true" />
      <span>{label ?? "Loading…"}</span>
    </div>
  );
}

export function EmptyState({
  title,
  hint,
  children,
}: {
  title: string;
  hint?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <Empty>
      <EmptyHeader>
        <EmptyTitle>{title}</EmptyTitle>
        {hint ? <EmptyDescription>{hint}</EmptyDescription> : null}
      </EmptyHeader>
      {children ? <EmptyContent>{children}</EmptyContent> : null}
    </Empty>
  );
}

/**
 * Surfaced by a polling view when it still shows cached data but the most
 * recent background refresh FAILED. Without it, a poll that starts erroring
 * after the first successful load silently freezes the screen on stale data
 * (views gate ErrorNotice on `!data`), so a "Healthy" badge or live stats could
 * keep showing while the box is actually unreachable. Non-disruptive (warn,
 * keeps the data visible) but honest about the stall.
 */
export function StaleBanner({ error }: { error: ApiError | Error }) {
  const isApi = error instanceof ApiError;
  return (
    <Alert variant="warning">
      <AlertTitle>Live updates paused</AlertTitle>
      <AlertDescription>
        Showing the last values received — the latest refresh failed: {error.message}
        {isApi && error.hint ? (
          <div className="mt-1.5 text-[13px] opacity-80">{error.hint}</div>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

/** Renders an ApiError in a friendly way, including its hint when present. */
export function ErrorNotice({ error }: { error: ApiError | Error }) {
  const isApi = error instanceof ApiError;
  return (
    <Alert variant="destructive">
      <AlertTitle>
        {isApi ? labelForCode(error.code) : "Something went wrong"}
      </AlertTitle>
      <AlertDescription>
        {error.message}
        {isApi && error.hint ? (
          <div className="mt-1.5 text-[13px] opacity-80">{error.hint}</div>
        ) : null}
      </AlertDescription>
    </Alert>
  );
}

function labelForCode(code: string): string {
  switch (code) {
    case "validation":
      return "Please check your input";
    case "safety":
      return "Confirmation required";
    case "ownership":
      return "This resource is owned by another panel";
    case "not_found":
      return "Not found";
    case "conflict":
      return "Already exists";
    case "auth":
      return "Authentication required";
    case "locked":
      return "Temporarily locked";
    case "exec":
      return "A command failed";
    default:
      return "Something went wrong";
  }
}

// --- Page header -----------------------------------------------------------

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div>
        <h1 className="text-2xl font-semibold text-foreground">{title}</h1>
        {description ? (
          <p className="mt-1 max-w-[60ch] text-muted-foreground">{description}</p>
        ) : null}
      </div>
      {actions ? (
        <div className="flex shrink-0 flex-wrap justify-end gap-2">{actions}</div>
      ) : null}
    </div>
  );
}

// --- Secret reveal ---------------------------------------------------------

/**
 * Shows a generated secret once, hidden by default with reveal + copy. Used for
 * passwords and DSNs that the server returns a single time.
 */
export function SecretValue({ label, value }: { label: string; value: string }) {
  const [revealed, setRevealed] = useState(false);
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="flex flex-col gap-1">
      <div className="text-sm font-medium">{label}</div>
      <div className="flex items-center gap-2">
        <code className="flex-1 overflow-x-auto whitespace-nowrap rounded-md border bg-muted px-2.5 py-2 font-mono text-xs">
          {revealed ? value : "•".repeat(Math.min(value.length, 24))}
        </code>
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-pressed={revealed}
          onClick={() => setRevealed((r) => !r)}
        >
          {revealed ? "Hide" : "Reveal"}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={copy}>
          {copied ? "Copied" : "Copy"}
        </Button>
      </div>
    </div>
  );
}
