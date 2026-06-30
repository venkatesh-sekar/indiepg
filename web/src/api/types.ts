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
// Extensions (internal/pg, internal/server/handlers_extensions)
// ---------------------------------------------------------------------------

/** How much work installing an extension takes, decided server-side from the
 *  catalog plus on-disk presence:
 *  - "ready"         — files on disk; a plain CREATE EXTENSION.
 *  - "needs_package" — curated but not on disk; the panel apt-get installs it.
 *  - "needs_restart" — needs shared_preload_libraries; install + restart Postgres. */
export type ExtensionTier = "ready" | "needs_package" | "needs_restart";

/** One installed extension — pg_extension joined to pg_available_extensions. */
export interface InstalledExtension {
  name: string;
  installed_version: string;
  default_version: string;
  /** The on-disk default differs from the installed version (an UPDATE would move it forward). */
  update_available: boolean;
}

/** One extension available to add, with the tier badge that tells the UI how
 *  much work the install is (and whether it restarts Postgres). */
export interface AvailableExtension {
  name: string;
  description: string;
  /** "" for a needs_package catalog entry whose files aren't on disk yet. */
  default_version: string;
  tier: ExtensionTier;
  requires_preload: boolean;
  /** True when the extension is part of the curated catalog. */
  in_catalog: boolean;
  /** Resolved OS package for a catalog entry that may need an apt install
   *  (e.g. "postgresql-17-pgvector"), so the Add dialog can preview the real
   *  command. "" for ready/free-form entries. */
  package: string;
}

/** GET /api/extensions?database= — installed + available for one target database.
 *  Both arrays are always present (never null). */
export interface ExtensionList {
  database: string;
  installed: InstalledExtension[];
  available: AvailableExtension[];
}

/** POST /api/extensions — install one extension into a database. `confirm`
 *  carries the typed extension name a Tier 3 (needs_restart) install requires;
 *  send "" for the other tiers. The success body is a {@link Result} whose
 *  `data.tier` is the tier that ran and whose `statements` list every command /
 *  SQL executed, in order (already password-redacted). */
export interface InstallExtensionRequest {
  database: string;
  name: string;
  confirm: string;
  /** Set by the "add by name" field. A free-form install is SQL-only — the
   *  server never apt-installs a package or edits shared_preload_libraries off a
   *  typed name. Catalog Add buttons omit it (defaults to false). */
  freeform?: boolean;
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

/**
 * RunBackupStarted is the async start acknowledgement (202) for a backup: the new
 * history row id and its initial "running" state. The run continues in the
 * background — poll backup history for the row to transition to success/fail.
 */
export interface RunBackupStarted {
  id: number;
  type: BackupType;
  result: string;
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
// Configuration / settings (internal/config, internal/server/handlers_config)
// ---------------------------------------------------------------------------

/** config.S3Target — the S3 backup destination. Secrets (secret_key, cipher_pass)
 *  are write-only and never returned; presence is reported by the *_is_set flags
 *  on ConfigResponse. */
export interface S3Target {
  endpoint: string;
  region: string;
  bucket: string;
  prefix: string;
  access_key: string;
  use_ssl: boolean;
}

export interface Schedules {
  full_backup: string;
  incremental_backup: string;
  restore_test: string;
  telemetry_sample: string;
  digest: string;
}

/** config.Config — the full panel configuration as returned by GET /api/config.
 *  Secrets are omitted server-side (json:"-"). */
export interface PanelConfig {
  bind_addr: string;
  force_public_bind: boolean;
  otlp_endpoint: string;
  otlp_insecure: boolean;
  stanza: string;
  backup: S3Target;
  retention_days: number;
  schedules: Schedules;
  statement_timeout: number;
  query_limit: number;
  pg_socket_dir: string;
}

/** Envelope returned by GET and PUT /api/config. The backup_* fields on the PUT
 *  response report whether re-provisioning pgBackRest succeeded after the save. */
export interface ConfigResponse {
  config: PanelConfig;
  backup_secret_is_set: boolean;
  backup_cipher_is_set: boolean;
  /** Present on the update response: did pgBackRest (re)configure cleanly? */
  backup_configured?: boolean;
  /** Present when provisioning failed (non-fatal): the reason to surface. */
  backup_warning?: string;
  backup_hint?: string;
  /** The underlying command's stderr (the precise failure reason), when available. */
  backup_detail?: string;
}

/** pg.TuningRecommendation — host-sized settings for one workload profile.
 *  Memory figures are in megabytes; computing it touches no Postgres. */
export interface TuningRecommendation {
  profile: WorkloadProfile;
  memory_mb: number;
  cpu_count: number;
  shared_buffers_mb: number;
  effective_cache_size_mb: number;
  work_mem_mb: number;
  maintenance_work_mem_mb: number;
  max_connections: number;
}

/** pg.AppliedTuning — the live value of each host-sized setting in force now,
 *  read from pg_settings and rendered in whole MB (max_connections is a count). */
export interface AppliedTuning {
  shared_buffers_mb: number;
  effective_cache_size_mb: number;
  work_mem_mb: number;
  maintenance_work_mem_mb: number;
  max_connections: number;
}

/** The workload profile Postgres is sized for. Mixed is the best default. */
export type WorkloadProfile = "oltp" | "mixed" | "olap";

/** pg.TuningStatus — the tuning surface from GET /api/tuning and the body POST
 *  /api/tuning/apply returns. `applied` is null when Postgres is unreachable
 *  (recommendations still load); `active_profile` is the persisted chosen
 *  profile, which an apply flips to the profile just written. */
export interface TuningStatus {
  memory_mb: number;
  cpu_count: number;
  active_profile: WorkloadProfile;
  applied: AppliedTuning | null;
  profiles: TuningRecommendation[];
}

/** POST /api/tuning/apply input — switch Postgres to a workload profile. The
 *  server resolves the host-sized recommendation for `profile`, applies it (a
 *  restart-bearing change to shared_buffers/max_connections, with rollback to
 *  last-known-good on failure), and only on success persists the chosen profile. */
export interface ApplyTuningRequest {
  profile: WorkloadProfile;
}

/** pgbouncer.PoolRecommendation — host-sized PgBouncer pool sizing, coordinated
 *  with Postgres' max_connections. Computing it touches no host state. */
export interface PoolRecommendation {
  profile: WorkloadProfile;
  pg_max_connections: number;
  default_pool_size: number;
  min_pool_size: number;
  reserve_pool_size: number;
  max_client_conn: number;
  server_idle_timeout: number;
}

/** GET /api/pooler — the read-only status of the opt-in PgBouncer pooler. `pool`
 *  is null when Postgres is unreachable (sizing is then computed at enable time).
 *  Reading this never mutates anything. */
export interface PoolerStatus {
  enabled: boolean;
  host: string;
  listen_port: number;
  pool: PoolRecommendation | null;
}

/** POST /api/pooler/enable input. `max_connections` is intentionally NOT sent —
 *  the server sizes the pool from the live Postgres so a forged value can't widen
 *  it. `profile` empty defaults to the mixed best default. */
export interface PoolerEnableRequest {
  roles: string[];
  profile?: WorkloadProfile;
}

/** pgbouncer.EnableResult — the outcome of turning the pooler on. */
export interface PoolerEnableResult {
  pooled_roles: string[];
  pool: PoolRecommendation;
  config_changed: boolean;
  userlist_changed: boolean;
  reloaded: boolean;
  running: boolean;
}

/** Editable S3 fields. Secrets are write-only: omit (or send empty) to keep the
 *  stored value; send a non-empty value to replace it. */
export interface BackupTargetUpdate {
  endpoint?: string;
  region?: string;
  bucket?: string;
  prefix?: string;
  access_key?: string;
  secret_key?: string;
  use_ssl?: boolean;
  cipher_pass?: string;
}

/** Partial config update — only the provided fields are applied (PUT /api/config). */
export interface UpdateConfigRequest {
  stanza?: string;
  retention_days?: number;
  query_limit?: number;
  backup?: BackupTargetUpdate;
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

export type MigrationMode = "single-db" | "cluster" | "ssh-less" | "drop-off";

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

/** A user-supplied source Postgres connection for a direct pull (or an ssh-less
 *  export). The password is sent once to start the job and is never persisted,
 *  logged, or echoed back — only the redacted "user@host:port/db" summary is. */
export interface SourceConn {
  host: string;
  port?: string;
  user?: string;
  password?: string;
  database?: string;
  sslmode?: string;
}

/** The fixed phrase an operator must type to authorize a destructive
 *  whole-cluster overwrite (mirrors the server's clusterOverwriteConfirm). */
export const CLUSTER_OVERWRITE_CONFIRM = "OVERWRITE";

/** POST /api/migrate/sessions — create the ssh-less TARGET session. The server
 *  uses its own default TTL. */
export interface CreateSessionRequest {
  database: string;
}

/** POST /api/migrate/single-db — direct pull of one database. Needs no S3. */
export interface SingleDBMigrationRequest {
  source: SourceConn;
  target_database: string;
  overwrite: boolean;
  /** When overwrite is true, must equal target_database to authorize the drop. */
  confirm: string;
}

/** POST /api/migrate/cluster — direct pull of every non-template database plus
 *  globals (roles/grants). Needs no S3. */
export interface ClusterMigrationRequest {
  source: SourceConn;
  overwrite: boolean;
  exclude?: string[];
  /** When overwrite is true, must equal CLUSTER_OVERWRITE_CONFIRM. */
  confirm: string;
}

/** POST /api/migrate/sessions/{code}/export — join a session as the SOURCE and
 *  push the named database to the shared bucket. */
export interface ExportSessionRequest {
  source: SourceConn;
  database: string;
}

/** The immediate response to starting an async migration job: the local record
 *  id to poll via GET /api/migrate/{id}, plus the initial status. */
export interface MigrationStarted {
  id: number;
  status: MigrationStatus;
}

// ---------------------------------------------------------------------------
// Drop-off link migration (internal/migrate dropoff, internal/server handlers_dropoff)
//
// Move ONE database from a source the panel can't reach (NAT/firewall, no
// inbound, no panel installed) but which can reach the public internet + S3.
// The panel mints two presigned S3 PUT URLs and a paste-able push command; the
// source runs `curl … | sh` to upload the dump + a meta.json manifest; then the
// panel imports from S3, verifies the SHA-256 and row counts, and cleans up.
// ---------------------------------------------------------------------------

/** Lifecycle state of a drop-off session (mirrors migrate.DropStatus). The
 *  source's upload is complete and verifiable once `uploaded` (meta.json, written
 *  last by the push script, is present); only then may the import Start. */
export type DropoffStatus =
  | "waiting_for_upload"
  | "uploaded"
  | "importing"
  | "completed"
  | "failed"
  // Operator-cancelled: terminal and NOT retryable (distinct from `failed`, whose
  // kept dump can be re-imported). The presigned PUT URLs can't be revoked, so a
  // cancelled session must never be restartable.
  | "canceled"
  | "expired";

/** POST /api/migrate/drops — mint a drop-off link for ONE target database. A
 *  destructive overwrite of a non-empty target requires `confirm` === the typed
 *  target_database (the same typed-name guard as the direct single-db pull). */
export interface CreateDropoffRequest {
  target_database: string;
  overwrite: boolean;
  /** When overwrite is true, must equal target_database to authorize the drop. */
  confirm: string;
}

/** The mint response — returned ONCE. This is the only place the presigned-URL-
 *  bearing push commands are served; the status endpoint never re-serves them.
 *  Treat command_docker / command_native as sensitive: each embeds two
 *  short-lived, PUT-only, single-key bearer URLs (an upload password). */
export interface CreateDropoffResult {
  code: string;
  target_database: string;
  overwrite: boolean;
  expires_at: string;
  /** `curl … | sh` push line using `docker exec` against a container. */
  command_docker: string;
  /** Native variant using --host/--port/--user (password via PGPASSWORD/prompt). */
  command_native: string;
}

/** GET /api/migrate/drops/{code} — the safe, re-servable status view: no URLs,
 *  no command. Polled for the upload-readiness badge; once `migration_id` is set
 *  the UI switches to polling GET /migrate/{id} for live import progress. */
export interface DropoffSession {
  code: string;
  status: DropoffStatus;
  target_database: string;
  overwrite: boolean;
  expires_at: string;
  /** Set once Start links the import job; drives the hand-off to the job poller. */
  migration_id?: number | null;
  /** The uploaded dump size in bytes (0 until the source finishes uploading). */
  byte_size: number;
  error?: string;
}

// ---------------------------------------------------------------------------
// PostgreSQL version & upgrade (internal/pg, internal/server/handlers_pgversion)
//
// Mirrors the API contract in docs/plans/2026-06-29-postgres-version-upgrade-
// design.md §7. The explicitly-documented JSON shapes (PGVersionInfo,
// PreflightResult, PendingFinalization, Check) match §7 field-for-field. The
// async-operation envelope (UpgradeOperation / UpgradeStatus) mirrors the
// backup/migration long-running pattern; §7 only says these endpoints "return an
// operation handle" + expose GET /api/pg/upgrade/status, so the UI treats the
// status endpoint as the source of truth and re-polls it after every mutation.
// ---------------------------------------------------------------------------

/** The running PostgreSQL version: the full server_version string + its major. */
export interface PGCurrentVersion {
  /** e.g. "16.2 (Debian 16.2-1.pgdg120+2)". */
  full: string;
  major: number;
}

/** An available minor update for the running major (e.g. 16.2 → 16.4). */
export interface PGMinorUpdate {
  available: boolean;
  /** The candidate version apt would install, e.g. "16.4"; "" when none. */
  target: string;
}

/** A major release the panel can upgrade to (a catalog entry newer than current). */
export interface PGMajorTarget {
  major: number;
  /** The latest stable major the panel recommends. */
  default?: boolean;
}

/** The set of upgrades available from the running version. */
export interface PGAvailableUpgrades {
  minor: PGMinorUpdate;
  majors: PGMajorTarget[];
}

/** A completed major upgrade awaiting finalize: the old cluster is stopped but
 *  still on disk (the rollback target). Non-null only while the two-phase
 *  finalize/rollback window is open. */
export interface PendingFinalization {
  from_major: number;
  to_major: number;
  /** Disk the old cluster frees when finalized. */
  reclaimable_bytes: number;
  upgraded_at: string;
}

/** GET /api/pg/version — drives the Version panel + the dashboard line. */
export interface PGVersionInfo {
  running: boolean;
  current: PGCurrentVersion;
  available: PGAvailableUpgrades;
  /** Non-null when a major upgrade awaits finalize/rollback. */
  pending_finalization: PendingFinalization | null;
}

/** A single pre-flight check outcome (internal/pg/preflight). A `fail` blocks the
 *  operation; a `warn` is shown but proceedable. */
export type CheckStatus = "pass" | "warn" | "fail";

export interface Check {
  id: string;
  title: string;
  status: CheckStatus;
  message: string;
  /** Optional hint on how to clear a warn/fail. */
  remediation?: string;
}

/** The preview of what a major upgrade will do, shown before committing (§7). */
export interface UpgradePreview {
  from_major: number;
  to_major: number;
  disk_required_bytes: number;
  disk_free_bytes: number;
  /** Extensions carried over to the new major. */
  extensions: string[];
  /** True when any check failed — the upgrade is blocked. */
  blocking: boolean;
}

/** POST /api/pg/upgrade/major/preflight response (§5 Phase A). */
export interface PreflightResult {
  checks: Check[];
  preview: UpgradePreview;
}

/** Which upgrade an operation is performing, for progress labelling. */
export type UpgradeKind = "minor" | "major" | "finalize" | "rollback";

/** A long-running upgrade operation, mirroring the backup/migration async model.
 *  Polled via GET /api/pg/upgrade/status; the mutating POSTs return the same
 *  envelope so the UI can switch straight to progress. */
interface UpgradeOperationBase {
  kind: UpgradeKind;
  from_major?: number;
  target_major: number;
  /** Human-readable current step, e.g. "Running pg_upgradecluster…". */
  phase: string;
  message: string;
  /** Captured command output so far, oldest first (already redacted). The backend
   *  omits this field while empty (omitempty), so treat it as optional. */
  log?: string[];
  started_at: string;
}

/** Lifecycle of a single upgrade operation. The status discriminates terminal
 * payloads so a failed operation always carries an error. */
export type UpgradeOperation =
  | (UpgradeOperationBase & { status: "running"; error?: never; finished_at?: null })
  | (UpgradeOperationBase & { status: "success"; error?: never; finished_at: string })
  | (UpgradeOperationBase & { status: "failed"; error: string; finished_at: string });

/** GET /api/pg/upgrade/status — the current operation + pending-finalization
 *  state, used to drive live progress and to resume the UI after a reload. Both
 *  fields are null when nothing is in flight and no upgrade awaits finalize. */
export interface UpgradeStatus {
  operation: UpgradeOperation | null;
  pending_finalization: PendingFinalization | null;
}

/** POST /api/pg/upgrade/minor body (§4). `backup: true` takes a fresh pgBackRest
 *  backup before upgrading (the one-click stale-backup option). */
export interface MinorUpgradeRequest {
  backup: boolean;
}

/** POST /api/pg/upgrade/major/preflight body (§5 Phase A). */
export interface PreflightRequest {
  target_major: number;
}

/** POST /api/pg/upgrade/major/start body (§5 Phase B). The server refuses unless
 *  the most recent preflight for this target had no `fail`, and always takes a
 *  mandatory fresh backup as the first step. */
export interface StartMajorUpgradeRequest {
  target_major: number;
  confirm: boolean;
}

/** POST /api/pg/upgrade/finalize body (§6). `confirm_version` must equal the old
 *  major to authorise dropping the old cluster (type-to-confirm guard). */
export interface FinalizeRequest {
  confirm_version: number;
}

/** POST /api/pg/upgrade/rollback body (§6). `confirm_version` must equal the
 * live/new major whose post-upgrade writes will be discarded. */
export interface RollbackRequest {
  confirm_version: number;
}

/** Finer step within a status, surfaced for progress. Empty in terminal states. */
export type MigrationPhase =
  | ""
  | "validating"
  | "dumping"
  | "uploading"
  | "downloading"
  | "restoring"
  | "verifying";

/** A migration job record — this panel's source of truth, polled by the UI.
 *  Mirrors the server's migrationResponse (store.MigrationRecord on the wire). */
export interface MigrationRecord {
  id: number;
  mode: MigrationMode;
  /** "direct" | "target" | "source". */
  role: string;
  status: MigrationStatus;
  phase: MigrationPhase;
  /** Redacted "user@host:port/db" — never a password. */
  source_summary: string;
  target_database: string;
  overwrite: boolean;
  /** Set for ssh-less jobs; empty for direct pulls. */
  code: string;
  progress_done: number;
  progress_total: number;
  bytes_total: number;
  error: string;
  row_counts_src: Record<string, number>;
  row_counts_tgt: Record<string, number>;
  created_at: string;
  updated_at: string;
  finished_at?: string | null;
}
