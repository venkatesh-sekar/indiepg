// Typed API models. These mirror the JSON shapes the Go server package
// (internal/server) marshals from internal/core, internal/store, internal/pg,
// internal/backup, internal/alert, internal/migrate and internal/telemetry.
//
// Field names use snake_case to match Go's encoding/json struct tags.

// ---------------------------------------------------------------------------
// Errors & results (internal/core)
// ---------------------------------------------------------------------------

/** Stable error codes returned by core.Error.Code. */
export type ErrorCode =
  | "validation"
  | "safety"
  | "ownership"
  | "not_found"
  | "conflict"
  | "exec"
  | "auth"
  | "locked"
  | "internal";

/** Shape the server returns in a non-2xx JSON body. */
export interface ApiErrorBody {
  code: ErrorCode;
  message: string;
  hint?: string;
  details?: Record<string, unknown>;
  /** Present on safety errors: the operator must type this to confirm. */
  expected?: string;
  /** Present on safety errors: the blocked operation, e.g. "drop database orders". */
  operation?: string;
  /** Present on safety errors: the flags/inputs the operation requires. */
  required_flags?: string[];
  /** Present on ownership errors (HARD STOP): the conflicting owner. */
  owner?: {
    owner_id: string;
    owner_host: string;
    last_seen: string;
    adoptable: boolean;
  };
}

/** core.Result — the standard success envelope for actions. */
export interface Result {
  ok: boolean;
  message?: string;
  data?: Record<string, unknown>;
  statements?: string[];
}

// ---------------------------------------------------------------------------
// Auth & session (internal/auth, internal/server)
// ---------------------------------------------------------------------------

/** GET /api/auth/status — public; polled on load to choose login vs app vs
 *  first-run, and to surface lockout. Mirrors the server's authStatusResponse. */
export interface AuthStatus {
  authenticated: boolean;
  /** An admin credential exists (install has run). */
  installed: boolean;
  /** The account is temporarily locked after repeated failed logins. */
  locked: boolean;
  /** When locked, the UTC instant the lockout lifts. */
  locked_until?: string;
}

/** POST /api/auth/login success body. The token is also set as an HttpOnly
 *  session cookie; browser flows rely on the cookie and ignore the token. */
export interface LoginResult {
  token: string;
  expires_at: string;
}

/** GET /api/auth/whoami — identifies the signed-in admin for the UI header.
 *  Mirrors the server's whoamiResponse. */
export interface SessionInfo {
  subject: string;
  issued_at: string;
  expires_at: string;
}

export interface InstanceInfo {
  instance_id: string;
  label: string;
  hostname: string;
  pg_system_id: string;
  panel_version: string;
}

// ---------------------------------------------------------------------------
// Dashboard / telemetry (internal/telemetry)
// ---------------------------------------------------------------------------

export interface Snapshot {
  taken_at: string;
  cpu_percent: number;
  mem_used_bytes: number;
  mem_total_bytes: number;
  disk_used_bytes: number;
  disk_total_bytes: number;
  load1: number;
  connections: number;
  max_connections: number;
  cache_hit_ratio: number;
  tps: number;
  deadlocks: number;
  replication_lag_seconds: number;
  last_backup_age_seconds: number;
}

export interface PGHealth {
  running: boolean;
  version?: string;
  system_id?: string;
}

export interface DashboardData {
  pg: PGHealth;
  snapshot: Snapshot;
  /** Latest successful backup summary, if any. */
  last_backup?: BackupRecord | null;
  /** Overall health verdict for the single green/red indicator. */
  health_ok: boolean;
  health_reasons?: string[];
}

// ---------------------------------------------------------------------------
// Query (internal/pg/guard, internal/pg)
// ---------------------------------------------------------------------------

export interface QueryColumn {
  name: string;
  data_type: string;
}

export interface QueryResult {
  columns: QueryColumn[];
  rows: Array<Array<string | number | boolean | null>>;
  row_count: number;
  /** True when the guard injected/enforced a LIMIT. */
  limited: boolean;
  /** The SQL actually executed (may be LIMIT-rewritten). */
  executed_sql: string;
  duration_ms: number;
  /** Classification surfaced to the UI, e.g. "read". */
  classification: string;
}

// ---------------------------------------------------------------------------
// Roles & databases (internal/pg, internal/pg/admin)
// ---------------------------------------------------------------------------

export interface DatabaseInfo {
  name: string;
  owner: string;
  size_bytes: number;
}

export interface RoleInfo {
  name: string;
  can_login: boolean;
  is_superuser: boolean;
  member_of?: string[];
}

export type AccessLevel = "readonly" | "readwrite" | "owner";

export interface CreateRoleRequest {
  username: string;
  can_login: boolean;
  /** When true the server generates a strong password and returns it once. */
  generate_password: boolean;
}

export interface CreateReadonlyUserRequest {
  username: string;
  database: string;
  schema: string;
}

export interface CreateDatabaseRequest {
  name: string;
  owner: string;
}

export interface NewAppRequest {
  database: string;
}

/** Returned once after a create flow that generated credentials. */
export interface CredentialResult {
  result: Result;
  /** Generated passwords / DSNs shown exactly once. */
  secrets?: Record<string, string>;
}

export interface GrantRequest {
  level: AccessLevel;
  role: string;
  database: string;
  schema: string;
}

export interface DropRequest {
  /** The exact object name the operator typed to confirm. */
  confirm: string;
}

// ---------------------------------------------------------------------------
// Backups (internal/backup)
// ---------------------------------------------------------------------------

export type BackupType = "full" | "diff" | "incr";

export interface BackupRecord {
  id: number;
  label: string;
  backup_type: string;
  started_at: string;
  stopped_at?: string | null;
  size_bytes: number;
  database_bytes: number;
  repo_bytes: number;
  wal_start: string;
  wal_stop: string;
  result: string;
  repo_path: string;
  error: string;
}

export interface RestoreTestRecord {
  id: number;
  tested_at: string;
  source_label: string;
  verified_rows: number;
  result: string;
  duration_ms: number;
  detail: string;
}

export interface BackupHistory {
  backups: BackupRecord[];
  restore_tests: RestoreTestRecord[];
}

export interface RunBackupRequest {
  type: BackupType;
}

export interface RecoveryTarget {
  /** RFC3339 timestamp for point-in-time recovery. */
  time?: string;
  xid?: string;
  lsn?: string;
  name?: string;
  action?: "promote" | "pause" | "shutdown";
}

export interface RestoreRequest {
  target?: RecoveryTarget | null;
  delta: boolean;
  /** Must equal the stanza name to confirm a destructive overwrite. */
  confirm: string;
}

// ---------------------------------------------------------------------------
// Alerts (internal/alert)
// ---------------------------------------------------------------------------

export type Severity = "info" | "warning" | "critical";
export type AlertOp = ">" | "<" | ">=" | "<=";
export type AlertState = "ok" | "firing" | "resolved";

export interface AlertRule {
  id: string;
  name: string;
  metric: string;
  op: AlertOp;
  threshold: number;
  severity: Severity;
  /** Seconds the condition must hold before firing. */
  for_seconds: number;
  /** Seconds between re-notifications. */
  cooldown_seconds: number;
  enabled: boolean;
  state?: AlertState;
  last_fired_at?: string | null;
  last_eval_at?: string | null;
}

export type ChannelKind = "pushover" | "webhook";

export interface ChannelConfig {
  kind: ChannelKind;
  enabled: boolean;
  /** Pushover. */
  pushover_token?: string;
  pushover_user?: string;
  /** Webhook. */
  webhook_url?: string;
}

export interface AlertsConfig {
  channels: ChannelConfig[];
  rules: AlertRule[];
}

export interface TestChannelRequest {
  kind: ChannelKind;
}

// ---------------------------------------------------------------------------
// Migration (internal/migrate)
// ---------------------------------------------------------------------------

export type MigrationMode = "single-db" | "cluster" | "ssh-less";

export type MigrationStatus =
  | "waiting_for_export"
  | "exporting"
  | "exported"
  | "importing"
  | "completed"
  | "failed"
  | "expired";

export interface MigrationSession {
  code: string;
  database: string;
  status: MigrationStatus;
  target_host: string;
  source_host?: string;
  created_at: string;
  expires_at: string;
  dump_key?: string;
  dump_size?: number;
  dump_checksum?: string;
  source_row_counts?: Record<string, number>;
  target_row_counts?: Record<string, number>;
  error?: string;
}

export interface RowCountDiff {
  table: string;
  source: number;
  target: number;
}

/** Verification verdict for a finished session. */
export interface MigrationVerification {
  verified: boolean;
  total_rows: number;
  diffs: RowCountDiff[];
}

export interface CreateSessionRequest {
  database: string;
  /** Time-to-live in seconds for the migration session. */
  ttl_seconds: number;
}

export interface SingleDBMigrationRequest {
  database: string;
  source_host: string;
  transport: "s3" | "ssh";
  /** Required when overwriting an existing target database. */
  confirm: string;
}

export interface ClusterMigrationRequest {
  source_host: string;
  confirm: string;
}
