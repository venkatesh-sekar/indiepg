// Small presentational primitives reused across views. Kept in one file to keep
// the component tree shallow and the bundle lean.

import { useState, type ReactNode } from "react";
import { ApiError } from "@/api/client";

// --- Badges ----------------------------------------------------------------

type BadgeTone = "neutral" | "ok" | "warn" | "danger" | "info" | "readonly";

export function Badge({
  children,
  tone = "neutral",
}: {
  children: ReactNode;
  tone?: BadgeTone;
}) {
  return <span className={`badge badge-${tone}`}>{children}</span>;
}

/** Prominent "read-only" affordance for the query box and read paths. */
export function ReadOnlyBadge() {
  return (
    <span className="badge badge-readonly" title="This connection cannot modify data.">
      Read-only
    </span>
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

export function Callout({
  tone = "info",
  title,
  children,
}: {
  tone?: "info" | "warn" | "danger" | "ok";
  title?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className={`callout callout-${tone}`}>
      {title ? <strong className="callout-title">{title}</strong> : null}
      <div>{children}</div>
    </div>
  );
}

// --- States ----------------------------------------------------------------

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="loading" role="status">
      <span className="spinner" aria-hidden="true" />
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

/** Renders an ApiError in a friendly way, including its hint when present. */
export function ErrorNotice({ error }: { error: ApiError | Error }) {
  const isApi = error instanceof ApiError;
  return (
    <div className="callout callout-danger" role="alert">
      <strong className="callout-title">
        {isApi ? labelForCode(error.code) : "Something went wrong"}
      </strong>
      <div>{error.message}</div>
      {isApi && error.hint ? <div className="callout-hint">{error.hint}</div> : null}
    </div>
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
