// Small presentational primitives reused across views. Kept in one file to keep
// the component tree shallow and the bundle lean.

import { useState, type ComponentProps, type ReactNode } from "react";
import { ApiError } from "@/api/client";
import { Badge as ShadcnBadge } from "@/components/ui/badge";
import { Spinner as ShadcnSpinner } from "@/components/ui/spinner";
import {
  Alert,
  AlertDescription,
  AlertTitle,
} from "@/components/ui/alert";

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
    <section className={`card ${className ?? ""}`}>
      {title || actions ? (
        <header className="card-head">
          {title ? <h3>{title}</h3> : <span />}
          {actions ? <div className="card-actions">{actions}</div> : null}
        </header>
      ) : null}
      <div className="card-body">{children}</div>
    </section>
  );
}

/** A single labeled metric for the dashboard. */
export function StatCard({
  label,
  value,
  sub,
  tone = "neutral",
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  tone?: BadgeTone;
}) {
  return (
    <div className={`stat-card stat-${tone}`}>
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {sub ? <div className="stat-sub">{sub}</div> : null}
    </div>
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
    <div className="empty">
      <p className="empty-title">{title}</p>
      {hint ? <p className="empty-hint">{hint}</p> : null}
      {children}
    </div>
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
        {isApi && error.hint ? <div className="callout-hint">{error.hint}</div> : null}
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
        {isApi && error.hint ? <div className="callout-hint">{error.hint}</div> : null}
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
    <div className="page-header">
      <div>
        <h1>{title}</h1>
        {description ? <p className="page-desc">{description}</p> : null}
      </div>
      {actions ? <div className="page-actions">{actions}</div> : null}
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
    <div className="secret">
      <div className="secret-label">{label}</div>
      <div className="secret-row">
        <code className="secret-value">{revealed ? value : "•".repeat(Math.min(value.length, 24))}</code>
        <button type="button" className="btn btn-sm" onClick={() => setRevealed((r) => !r)}>
          {revealed ? "Hide" : "Reveal"}
        </button>
        <button type="button" className="btn btn-sm" onClick={copy}>
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
    </div>
  );
}
