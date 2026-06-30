// Fetch-based, cookie-session API client. The server sets an HttpOnly signed
// session cookie on login, so every request just needs credentials:"same-origin".
//
// All methods throw ApiError on a non-2xx response, carrying the typed code,
// message, hint and (for safety errors) the expected confirmation value.

import type {
  AlertRule,
  AlertsConfig,
  ApiErrorBody,
  ApplyTuningRequest,
  AuthStatus,
  BackupHistory,
  ChannelConfig,
  ClusterMigrationRequest,
  ConfigResponse,
  TuningStatus,
  CreateDatabaseRequest,
  CreateDropoffRequest,
  CreateDropoffResult,
  CreateReadonlyUserRequest,
  CreateRoleRequest,
  CreateSessionRequest,
  CredentialResult,
  DropoffSession,
  DashboardData,
  DatabaseInfo,
  DropRequest,
  ErrorCode,
  ExportSessionRequest,
  ExtensionList,
  InstallExtensionRequest,
  GrantRequest,
  InstanceInfo,
  LoginResult,
  MigrationRecord,
  MigrationSession,
  MigrationStarted,
  NewAppRequest,
  PGVersionInfo,
  PoolerEnableRequest,
  PoolerEnableResult,
  PoolerStatus,
  PreflightResult,
  QueryResult,
  Result,
  RestoreRequest,
  RoleInfo,
  RunBackupRequest,
  RunBackupStarted,
  SessionInfo,
  SingleDBMigrationRequest,
  TestChannelRequest,
  UpdateConfigRequest,
  UpgradeStatus,
  WorkloadProfile,
} from "./types";

/** Typed error thrown by every client method on failure. */
export class ApiError extends Error {
  readonly code: ErrorCode;
  readonly status: number;
  readonly hint?: string;
  readonly expected?: string;
  readonly requiredFlags?: string[];
  readonly details?: Record<string, unknown>;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || "Request failed");
    this.name = "ApiError";
    this.status = status;
    this.code = body.code;
    this.hint = body.hint;
    this.expected = body.expected;
    this.requiredFlags = body.required_flags;
    this.details = body.details;
  }

  /** True when this is an authentication / session failure. */
  get isAuth(): boolean {
    return this.code === "auth" || this.status === 401;
  }

  /** True when the account is temporarily locked out. */
  get isLocked(): boolean {
    return this.code === "locked";
  }
}

const BASE = "/api";

interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = {
    Accept: "application/json",
    // Non-simple custom header the server's CSRF backstop accepts as a
    // same-origin signal. A cross-site attacker cannot set it without a
    // preflight that the same-origin policy blocks.
    "X-Indiepg-Csrf": "1",
  };
  const init: RequestInit = {
    method: opts.method ?? "GET",
    credentials: "same-origin",
    headers,
    signal: opts.signal,
  };
  if (opts.body !== undefined) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(opts.body);
  }

  let resp: Response;
  try {
    resp = await fetch(`${BASE}${path}`, init);
  } catch (cause) {
    throw new ApiError(0, {
      code: "internal",
      message: "Could not reach the panel. Check your connection.",
      details: { cause: String(cause) },
    });
  }

  if (resp.status === 204) {
    return undefined as T;
  }

  const text = await resp.text();
  const parsed = text ? safeJson(text) : undefined;

  if (!resp.ok) {
    const body: ApiErrorBody =
      parsed && typeof parsed === "object"
        ? (parsed as ApiErrorBody)
        : {
            code: codeForStatus(resp.status),
            message: text || resp.statusText || "Request failed",
          };
    throw new ApiError(resp.status, body);
  }

  // The server wraps successful payloads as {"data": ...} (see respond.go
  // writeData). Unwrap so callers receive the bare typed value. Error bodies
  // are NOT wrapped and are handled above before we reach here.
  if (parsed && typeof parsed === "object" && "data" in (parsed as object)) {
    return (parsed as { data: T }).data;
  }
  return parsed as T;
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return { message: text };
  }
}

function codeForStatus(status: number): ErrorCode {
  switch (status) {
    case 400:
      return "validation";
    case 401:
      return "auth";
    case 403:
      return "safety";
    case 404:
      return "not_found";
    case 409:
      return "conflict";
    case 429:
      // The server maps a lockout (CodeLocked) to 429 Too Many Requests.
      return "locked";
    default:
      return "internal";
  }
}

// ---------------------------------------------------------------------------
// Endpoint surface. Grouped to mirror the server's route tree.
// ---------------------------------------------------------------------------

export const api = {
  // session ----------------------------------------------------------------
  session(): Promise<AuthStatus> {
    return request<AuthStatus>("/auth/status");
  },
  login(password: string): Promise<LoginResult> {
    return request<LoginResult>("/auth/login", { method: "POST", body: { password } });
  },
  logout(): Promise<void> {
    return request<void>("/auth/logout", { method: "POST" });
  },
  whoami(): Promise<SessionInfo> {
    return request<SessionInfo>("/auth/whoami");
  },
  instance(): Promise<InstanceInfo> {
    return request<InstanceInfo>("/instance");
  },

  // dashboard --------------------------------------------------------------
  dashboard(signal?: AbortSignal): Promise<DashboardData> {
    return request<DashboardData>("/dashboard", { signal });
  },

  // query ------------------------------------------------------------------
  runQuery(sql: string, signal?: AbortSignal): Promise<QueryResult> {
    return request<QueryResult>("/query", { method: "POST", body: { sql }, signal });
  },

  // roles & databases ------------------------------------------------------
  listDatabases(): Promise<DatabaseInfo[]> {
    return request<DatabaseInfo[]>("/databases");
  },
  listRoles(): Promise<RoleInfo[]> {
    return request<RoleInfo[]>("/roles");
  },
  createRole(req: CreateRoleRequest): Promise<CredentialResult> {
    return request<CredentialResult>("/roles", { method: "POST", body: req });
  },
  createReadonlyUser(req: CreateReadonlyUserRequest): Promise<CredentialResult> {
    return request<CredentialResult>("/roles/readonly", { method: "POST", body: req });
  },
  createDatabase(req: CreateDatabaseRequest): Promise<Result> {
    return request<Result>("/databases", { method: "POST", body: req });
  },
  createNewApp(req: NewAppRequest): Promise<CredentialResult> {
    return request<CredentialResult>("/databases/new-app", { method: "POST", body: req });
  },
  grant(req: GrantRequest): Promise<Result> {
    return request<Result>("/grants", { method: "POST", body: req });
  },
  revoke(req: GrantRequest): Promise<Result> {
    return request<Result>("/grants", { method: "DELETE", body: req });
  },
  rotatePassword(role: string): Promise<CredentialResult> {
    return request<CredentialResult>(`/roles/${encodeURIComponent(role)}/rotate`, {
      method: "POST",
    });
  },
  dropRole(role: string, req: DropRequest): Promise<Result> {
    return request<Result>(`/roles/${encodeURIComponent(role)}`, {
      method: "DELETE",
      body: req,
    });
  },
  dropDatabase(name: string, req: DropRequest): Promise<Result> {
    return request<Result>(`/databases/${encodeURIComponent(name)}`, {
      method: "DELETE",
      body: req,
    });
  },

  // extensions -------------------------------------------------------------
  // List is read-only; `database` defaults to the maintenance DB server-side.
  listExtensions(database?: string): Promise<ExtensionList> {
    const q = database ? `?database=${encodeURIComponent(database)}` : "";
    return request<ExtensionList>(`/extensions${q}`);
  },
  // Install picks the tier server-side. A Tier 3 (needs_restart) install needs
  // req.confirm === req.name; the others ignore it. The Result's `statements`
  // list every command that actually ran.
  installExtension(req: InstallExtensionRequest): Promise<Result> {
    return request<Result>("/extensions", { method: "POST", body: req });
  },
  // Update upgrades an installed extension to its default version (ALTER
  // EXTENSION ... UPDATE). Non-destructive; no confirmation, no restart.
  updateExtension(name: string, database: string): Promise<Result> {
    const q = database ? `?database=${encodeURIComponent(database)}` : "";
    return request<Result>(`/extensions/${encodeURIComponent(name)}/update${q}`, {
      method: "POST",
    });
  },
  // Drop requires req.confirm === name (typed-name confirmation); no CASCADE.
  dropExtension(name: string, database: string, req: DropRequest): Promise<Result> {
    const q = database ? `?database=${encodeURIComponent(database)}` : "";
    return request<Result>(`/extensions/${encodeURIComponent(name)}${q}`, {
      method: "DELETE",
      body: req,
    });
  },

  // backups ----------------------------------------------------------------
  backupHistory(): Promise<BackupHistory> {
    return request<BackupHistory>("/backups");
  },
  // Starts a backup and returns immediately (202) with the new history row id;
  // the run continues in the background. Poll backupHistory() for completion.
  runBackup(req: RunBackupRequest): Promise<RunBackupStarted> {
    return request<RunBackupStarted>("/backups/run", { method: "POST", body: req });
  },
  restore(req: RestoreRequest): Promise<Result> {
    return request<Result>("/backups/restore", { method: "POST", body: req });
  },
  // runRestoreTest proves a backup is recoverable. The default (cheap, always-safe)
  // form runs `pgbackrest verify`, a read-only repo integrity check. `deep: true`
  // opts into a full scratch restore + boot + row count, which catches
  // recovery-time failures verify cannot but runs longer and needs disk headroom.
  runRestoreTest(opts: { deep?: boolean } = {}): Promise<Result> {
    const path = opts.deep ? "/backups/restore-test?deep=true" : "/backups/restore-test";
    return request<Result>(path, { method: "POST" });
  },

  // settings / config ------------------------------------------------------
  getConfig(): Promise<ConfigResponse> {
    return request<ConfigResponse>("/config");
  },
  updateConfig(req: UpdateConfigRequest): Promise<ConfigResponse> {
    return request<ConfigResponse>("/config", { method: "PUT", body: req });
  },
  getTuning(): Promise<TuningStatus> {
    return request<TuningStatus>("/tuning");
  },
  // Switches Postgres to a workload profile. The server applies the host-sized
  // recommendation for `profile` first — resizing shared_buffers/max_connections,
  // which restarts Postgres, rolling back to last-known-good on failure — and only
  // then persists the chosen profile. Returns the fresh TuningStatus (re-read
  // applied values + the now-active profile). Throws on a failed/rolled-back apply,
  // and the profile is NOT persisted in that case.
  applyTuning(profile: WorkloadProfile): Promise<TuningStatus> {
    const body: ApplyTuningRequest = { profile };
    return request<TuningStatus>("/tuning/apply", { method: "POST", body });
  },

  // pooler (opt-in PgBouncer) ---------------------------------------------
  // Read-only status: never mutates host state.
  poolerStatus(): Promise<PoolerStatus> {
    return request<PoolerStatus>("/pooler");
  },
  // Turns the pooler on: installs PgBouncer and starts the service. The pool is
  // sized server-side from the live Postgres, never from this request.
  enablePooler(req: PoolerEnableRequest): Promise<PoolerEnableResult> {
    return request<PoolerEnableResult>("/pooler/enable", { method: "POST", body: req });
  },
  // Turns the pooler off: stops and disables the PgBouncer service. The service is
  // stopped before the off state is recorded server-side, so a failure leaves the
  // pooler reported as still on. Reversible — re-enable at any time.
  disablePooler(): Promise<PoolerStatus> {
    return request<PoolerStatus>("/pooler/disable", { method: "POST" });
  },

  // alerts -----------------------------------------------------------------
  alerts(): Promise<AlertsConfig> {
    return request<AlertsConfig>("/alerts");
  },
  saveChannel(channel: ChannelConfig): Promise<Result> {
    return request<Result>("/alerts/channels", { method: "PUT", body: channel });
  },
  testChannel(req: TestChannelRequest): Promise<Result> {
    return request<Result>("/alerts/channels/test", { method: "POST", body: req });
  },
  saveRule(rule: AlertRule): Promise<Result> {
    // PUT accepts only the rule definition. Callers often pass a rule fetched
    // from GET /alerts (e.g. the enable/disable toggle spreads it), which carries
    // read-only fields (state/last_fired_at/last_eval_at); the server's strict
    // decoder rejects those as unknown, so send only the definition fields.
    const { id, name, metric, op, threshold, severity, for_seconds, cooldown_seconds, enabled } = rule;
    return request<Result>("/alerts/rules", {
      method: "PUT",
      body: { id, name, metric, op, threshold, severity, for_seconds, cooldown_seconds, enabled },
    });
  },
  deleteRule(id: string): Promise<Result> {
    return request<Result>(`/alerts/rules/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  },

  // migration --------------------------------------------------------------
  // Direct pull (needs no S3): starts a background job and returns its id to poll.
  migrateSingleDB(req: SingleDBMigrationRequest): Promise<MigrationStarted> {
    return request<MigrationStarted>("/migrate/single-db", { method: "POST", body: req });
  },
  migrateCluster(req: ClusterMigrationRequest): Promise<MigrationStarted> {
    return request<MigrationStarted>("/migrate/cluster", { method: "POST", body: req });
  },
  // Job history + per-job polling (the panel's source of truth for every mode).
  listMigrations(signal?: AbortSignal): Promise<MigrationRecord[]> {
    return request<{ migrations: MigrationRecord[] }>("/migrate", { signal }).then(
      (r) => r.migrations ?? [],
    );
  },
  getMigration(id: number, signal?: AbortSignal): Promise<MigrationRecord> {
    return request<MigrationRecord>(`/migrate/${id}`, { signal });
  },
  // Cross-panel ssh-less handshake (requires S3 on both panels).
  createSession(req: CreateSessionRequest): Promise<MigrationSession> {
    return request<MigrationSession>("/migrate/sessions", { method: "POST", body: req });
  },
  getSession(code: string, signal?: AbortSignal): Promise<MigrationSession> {
    return request<MigrationSession>(`/migrate/sessions/${encodeURIComponent(code)}`, {
      signal,
    });
  },
  exportToSession(code: string, req: ExportSessionRequest): Promise<MigrationStarted> {
    return request<MigrationStarted>(
      `/migrate/sessions/${encodeURIComponent(code)}/export`,
      { method: "POST", body: req },
    );
  },
  cancelSession(code: string): Promise<void> {
    return request<void>(`/migrate/sessions/${encodeURIComponent(code)}`, {
      method: "DELETE",
    });
  },
  // Drop-off link (requires S3): mint two presigned S3 PUT URLs + a paste-able
  // push command for a source the panel can't reach. The mint response carries
  // the URL-bearing commands ONCE — never persisted or re-served by getDropoff.
  createDropoff(req: CreateDropoffRequest): Promise<CreateDropoffResult> {
    return request<CreateDropoffResult>("/migrate/drops", { method: "POST", body: req });
  },
  // Active (non-terminal, not-yet-expired) drop-off sessions as the safe status
  // view — no URLs, no command. The recovery path: if the minted code was lost to
  // a browser reload before Start/Cancel, the operator resumes from this list.
  listDropoffs(signal?: AbortSignal): Promise<DropoffSession[]> {
    return request<DropoffSession[]>("/migrate/drops", { signal });
  },
  // Safe status poll: no URLs, no command. The badge flips waiting → uploaded
  // once the source's meta.json lands; once migration_id is set, switch to
  // getMigration(migration_id) for the live import/verify progress.
  getDropoff(code: string, signal?: AbortSignal): Promise<DropoffSession> {
    return request<DropoffSession>(`/migrate/drops/${encodeURIComponent(code)}`, { signal });
  },
  // Begins the import once the upload is present (409 if not uploaded yet).
  // Returns the migration job id to poll via getMigration(). When the session was
  // minted with overwrite=true the DROP runs HERE (not at mint), so `confirm` must
  // re-echo the target database name or the server refuses with a safety error —
  // the same typed-name guard as the single-db pull. A non-overwrite Start may pass
  // an empty confirm.
  startDropoff(code: string, confirm = ""): Promise<MigrationStarted> {
    return request<MigrationStarted>(`/migrate/drops/${encodeURIComponent(code)}/start`, {
      method: "POST",
      body: { confirm },
    });
  },
  // Deletes the dump + meta objects (idempotent) and marks the session cancelled.
  cancelDropoff(code: string): Promise<void> {
    return request<void>(`/migrate/drops/${encodeURIComponent(code)}`, { method: "DELETE" });
  },

  // pg version & upgrades --------------------------------------------------
  // Read-only: the running version + available minor/major updates + any
  // pending-finalization state. Drives the Version panel and the dashboard line.
  pgVersion(signal?: AbortSignal): Promise<PGVersionInfo> {
    return request<PGVersionInfo>("/pg/version", { signal });
  },
  // The live upgrade operation + pending-finalization state. Polled for progress
  // and to resume the UI after a reload (mirrors the backup/migration pattern).
  upgradeStatus(signal?: AbortSignal): Promise<UpgradeStatus> {
    return request<UpgradeStatus>("/pg/upgrade/status", { signal });
  },
  // Minor upgrade (apt + restart). `backup` opts into a fresh pgBackRest backup
  // first. Returns the initial operation state; poll upgradeStatus() for progress.
  upgradeMinor(backup: boolean): Promise<UpgradeStatus> {
    return request<UpgradeStatus>("/pg/upgrade/minor", { method: "POST", body: { backup } });
  },
  // Major upgrade Phase A: installs target packages + runs the checklist. Purely
  // non-destructive — nothing is changed, this only previews and validates.
  preflightMajorUpgrade(targetMajor: number): Promise<PreflightResult> {
    return request<PreflightResult>("/pg/upgrade/major/preflight", {
      method: "POST",
      body: { target_major: targetMajor },
    });
  },
  // Major upgrade Phase B: takes the mandatory backup and runs pg_upgradecluster.
  // Refused unless the most recent preflight for this target had no `fail`.
  startMajorUpgrade(targetMajor: number): Promise<UpgradeStatus> {
    return request<UpgradeStatus>("/pg/upgrade/major/start", {
      method: "POST",
      body: { target_major: targetMajor, confirm: true },
    });
  },
  // Finalize: drop the old cluster and reclaim its disk — irreversible.
  // `confirmVersion` must equal the old major (the type-to-confirm guard).
  finalizeUpgrade(confirmVersion: number): Promise<UpgradeStatus> {
    return request<UpgradeStatus>("/pg/upgrade/finalize", {
      method: "POST",
      body: { confirm_version: confirmVersion },
    });
  },
  // Roll back to the old major. DISCARDS any writes made against the new major
  // since the upgrade. Clears the pending-finalization state.
  rollbackUpgrade(confirmVersion: number): Promise<UpgradeStatus> {
    return request<UpgradeStatus>("/pg/upgrade/rollback", {
      method: "POST",
      body: { confirm_version: confirmVersion },
    });
  },
};

export type Api = typeof api;
