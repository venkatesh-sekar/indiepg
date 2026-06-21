// Small, dependency-free formatting helpers shared across views.

const KB = 1024;
const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

/** Human-readable byte size, e.g. 1536 -> "1.5 KB". */
export function bytes(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return "—";
  if (n === 0) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(KB)), UNITS.length - 1);
  const value = n / Math.pow(KB, i);
  const digits = value >= 100 || i === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${UNITS[i]}`;
}

/** Whole-number percentage from a 0..1 ratio. */
export function ratioPct(ratio: number | null | undefined): string {
  if (ratio == null || Number.isNaN(ratio)) return "—";
  return `${Math.round(ratio * 100)}%`;
}

/** Percentage already on a 0..100 scale. */
export function pct(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return "—";
  return `${value.toFixed(value >= 10 ? 0 : 1)}%`;
}

/** Duration in seconds -> compact "2h 5m" / "45s". */
export function duration(seconds: number | null | undefined): string {
  if (seconds == null || Number.isNaN(seconds) || seconds < 0) return "—";
  const s = Math.floor(seconds);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ${m % 60}m`;
  const d = Math.floor(h / 24);
  return `${d}d ${h % 24}h`;
}

/** Milliseconds -> "12 ms" / "1.4 s". */
export function millis(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms)) return "—";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  return `${(ms / 1000).toFixed(2)} s`;
}

/** Local datetime from an RFC3339 string. */
export function dateTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

/** Relative "ago" string from an RFC3339 string. */
export function ago(iso: string | null | undefined, now: number = Date.now()): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const seconds = Math.max(0, (now - d.getTime()) / 1000);
  return `${duration(seconds)} ago`;
}

/** Relative "ago" from a seconds-since value (e.g. last_backup_age_seconds). */
export function agoSeconds(seconds: number | null | undefined): string {
  if (seconds == null || Number.isNaN(seconds)) return "never";
  return `${duration(seconds)} ago`;
}

/** Integer with thousands separators. */
export function count(n: number | null | undefined): string {
  if (n == null || Number.isNaN(n)) return "—";
  return n.toLocaleString();
}
