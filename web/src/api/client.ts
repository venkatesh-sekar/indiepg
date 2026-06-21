// Fetch-based, cookie-session API client. The server sets an HttpOnly signed
// session cookie on login, so every request just needs credentials:"same-origin".
//
// All methods throw ApiError on a non-2xx response, carrying the typed code,
// message, hint and (for safety errors) the expected confirmation value.

import type {
  AlertRule,
  AlertsConfig,
  ApiErrorBody,
  BackupHistory,
  ChannelConfig,
  ClusterMigrationRequest,
  CreateDatabaseRequest,
  CreateReadonlyUserRequest,
  CreateRoleRequest,
  CreateSessionRequest,
  CredentialResult,
  DashboardData,
  DatabaseInfo,
  DropRequest,
  ErrorCode,
  GrantRequest,
  InstanceInfo,
  MigrationSession,
  NewAppRequest,
  QueryResult,
  Result,
  RestoreRequest,
  RoleInfo,
  RunBackupRequest,
  SessionInfo,
  SingleDBMigrationRequest,
  TestChannelRequest,
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
    case 423:
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
  session(): Promise<SessionInfo> {
    return request<SessionInfo>("/auth/status");
  },
  login(password: string): Promise<SessionInfo> {
    return request<SessionInfo>("/auth/login", { method: "POST", body: { password } });
  },
  logout(): Promise<void> {
    return request<void>("/auth/logout", { method: "POST" });
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

  // backups ----------------------------------------------------------------
  backupHistory(): Promise<BackupHistory> {
    return request<BackupHistory>("/backups");
  },
  runBackup(req: RunBackupRequest): Promise<Result> {
    return request<Result>("/backups/run", { method: "POST", body: req });
  },
  restore(req: RestoreRequest): Promise<Result> {
    return request<Result>("/backups/restore", { method: "POST", body: req });
  },
  runRestoreTest(): Promise<Result> {
    return request<Result>("/backups/restore-test", { method: "POST" });
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
    return request<Result>("/alerts/rules", { method: "PUT", body: rule });
  },
  deleteRule(id: string): Promise<Result> {
    return request<Result>(`/alerts/rules/${encodeURIComponent(id)}`, {
      method: "DELETE",
    });
  },

  // migration --------------------------------------------------------------
  createSession(req: CreateSessionRequest): Promise<MigrationSession> {
    return request<MigrationSession>("/migrate/sessions", { method: "POST", body: req });
  },
  getSession(code: string, signal?: AbortSignal): Promise<MigrationSession> {
    return request<MigrationSession>(`/migrate/sessions/${encodeURIComponent(code)}`, {
      signal,
    });
  },
  cancelSession(code: string): Promise<void> {
    return request<void>(`/migrate/sessions/${encodeURIComponent(code)}`, {
      method: "DELETE",
    });
  },
  migrateSingleDB(req: SingleDBMigrationRequest): Promise<Result> {
    return request<Result>("/migrate/single-db", { method: "POST", body: req });
  },
  migrateCluster(req: ClusterMigrationRequest): Promise<Result> {
    return request<Result>("/migrate/cluster", { method: "POST", body: req });
  },
};

export type Api = typeof api;
