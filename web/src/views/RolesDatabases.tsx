// Roles & Databases: list users and databases, with guided create flows and
// confirmation dialogs. Destructive drops require typing the object name.

import { useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { bytes } from "@/lib/format";
import { useAsync } from "@/lib/hooks";
import { Modal } from "@/components/Modal";
import { TypedConfirmDialog } from "@/components/ConfirmDialog";
import { useToast } from "@/components/Toast";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  SecretValue,
  Spinner,
} from "@/components/ui";
import type { CredentialResult, DatabaseInfo, RoleInfo } from "@/api/types";

type CreateFlow =
  | { kind: "none" }
  | { kind: "user" }
  | { kind: "readonly" }
  | { kind: "database" }
  | { kind: "new-app" };

type DropTarget =
  | { kind: "role"; name: string }
  | { kind: "database"; name: string }
  | null;

export function RolesDatabases() {
  const toast = useToast();
  const roles = useAsync<RoleInfo[]>(() => api.listRoles(), []);
  const dbs = useAsync<DatabaseInfo[]>(() => api.listDatabases(), []);

  const [flow, setFlow] = useState<CreateFlow>({ kind: "none" });
  const [dropTarget, setDropTarget] = useState<DropTarget>(null);
  const [dropBusy, setDropBusy] = useState(false);
  const [secrets, setSecrets] = useState<CredentialResult | null>(null);
  const [rotateBusy, setRotateBusy] = useState<string | null>(null);

  const reloadAll = () => {
    roles.reload();
    dbs.reload();
  };

  const onCreated = (msg: string, creds?: CredentialResult) => {
    setFlow({ kind: "none" });
    reloadAll();
    if (creds && creds.secrets && Object.keys(creds.secrets).length > 0) {
      setSecrets(creds);
    } else {
      toast.success(msg);
    }
  };

  const doDrop = async (typed: string) => {
    if (!dropTarget) return;
    setDropBusy(true);
    try {
      if (dropTarget.kind === "role") {
        await api.dropRole(dropTarget.name, { confirm: typed });
        toast.success(`Removed user ${dropTarget.name}.`);
      } else {
        await api.dropDatabase(dropTarget.name, { confirm: typed });
        toast.success(`Removed database ${dropTarget.name}.`);
      }
      setDropTarget(null);
      reloadAll();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not delete.");
    } finally {
      setDropBusy(false);
    }
  };

  const rotate = async (role: string) => {
    setRotateBusy(role);
    try {
      const creds = await api.rotatePassword(role);
      setSecrets(creds);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not rotate password.");
    } finally {
      setRotateBusy(null);
    }
  };

  return (
    <div className="view">
      <PageHeader
        title="Roles & Databases"
        description="Create databases and users safely. Every action here is guided and confirmed."
        actions={
          <button type="button" className="btn btn-primary" onClick={() => setFlow({ kind: "new-app" })}>
            + New app (one-click)
          </button>
        }
      />

      <Callout tone="info" title="What is a “role”?">
        In Postgres, a <strong>role</strong> is a login user or a group. Use{" "}
        <strong>New app</strong> below to create everything an application needs in one step:
        a database, a read/write user, and a read-only user, each with a strong password.
      </Callout>

      {/* Databases */}
      <Card
        title="Databases"
        actions={
          <button type="button" className="btn btn-sm" onClick={() => setFlow({ kind: "database" })}>
            + Create database
          </button>
        }
      >
        {dbs.loading ? (
          <Spinner />
        ) : dbs.error ? (
          <ErrorNotice error={dbs.error} />
        ) : !dbs.data || dbs.data.length === 0 ? (
          <EmptyState title="No databases yet" hint="Create one to get started." />
        ) : (
          <div className="table-scroll">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Owner</th>
                  <th>Size</th>
                  <th className="col-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {dbs.data.map((db) => (
                  <tr key={db.name}>
                    <td><strong>{db.name}</strong></td>
                    <td>{db.owner}</td>
                    <td>{bytes(db.size_bytes)}</td>
                    <td className="col-actions">
                      <button
                        type="button"
                        className="btn btn-sm btn-danger-ghost"
                        onClick={() => setDropTarget({ kind: "database", name: db.name })}
                      >
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* Roles */}
      <Card
        title="Users & roles"
        actions={
          <>
            <button type="button" className="btn btn-sm" onClick={() => setFlow({ kind: "readonly" })}>
              + Read-only user
            </button>
            <button type="button" className="btn btn-sm" onClick={() => setFlow({ kind: "user" })}>
              + Login user
            </button>
          </>
        }
      >
        {roles.loading ? (
          <Spinner />
        ) : roles.error ? (
          <ErrorNotice error={roles.error} />
        ) : !roles.data || roles.data.length === 0 ? (
          <EmptyState title="No roles yet" />
        ) : (
          <div className="table-scroll">
            <table className="data-table">
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Type</th>
                  <th className="col-actions">Actions</th>
                </tr>
              </thead>
              <tbody>
                {roles.data.map((role) => (
                  <tr key={role.name}>
                    <td><strong>{role.name}</strong></td>
                    <td>
                      {role.is_superuser ? (
                        <Badge tone="warn">superuser</Badge>
                      ) : role.can_login ? (
                        <Badge tone="info">login user</Badge>
                      ) : (
                        <Badge>group role</Badge>
                      )}
                    </td>
                    <td className="col-actions">
                      {role.can_login && !role.is_superuser ? (
                        <button
                          type="button"
                          className="btn btn-sm"
                          disabled={rotateBusy === role.name}
                          onClick={() => rotate(role.name)}
                        >
                          {rotateBusy === role.name ? "…" : "Rotate password"}
                        </button>
                      ) : null}
                      {!role.is_superuser ? (
                        <button
                          type="button"
                          className="btn btn-sm btn-danger-ghost"
                          onClick={() => setDropTarget({ kind: "role", name: role.name })}
                        >
                          Delete
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {/* Create flows */}
      {flow.kind === "user" ? (
        <CreateUserModal
          onClose={() => setFlow({ kind: "none" })}
          onCreated={(creds) => onCreated("Created login user.", creds)}
        />
      ) : null}
      {flow.kind === "readonly" ? (
        <CreateReadonlyModal
          databases={dbs.data ?? []}
          onClose={() => setFlow({ kind: "none" })}
          onCreated={(creds) => onCreated("Created read-only user.", creds)}
        />
      ) : null}
      {flow.kind === "database" ? (
        <CreateDatabaseModal
          roles={roles.data ?? []}
          onClose={() => setFlow({ kind: "none" })}
          onCreated={() => onCreated("Created database.")}
        />
      ) : null}
      {flow.kind === "new-app" ? (
        <NewAppModal
          onClose={() => setFlow({ kind: "none" })}
          onCreated={(creds) => onCreated("Created app.", creds)}
        />
      ) : null}

      {/* Drop confirmation */}
      <TypedConfirmDialog
        open={dropTarget !== null}
        title={dropTarget?.kind === "database" ? "Delete database" : "Delete user"}
        objectName={dropTarget?.name ?? ""}
        objectKind={dropTarget?.kind === "database" ? "database" : "user"}
        consequence={
          dropTarget?.kind === "database"
            ? "Every table and row in this database will be permanently deleted."
            : "Any application using this user will immediately lose access."
        }
        busy={dropBusy}
        onConfirm={doDrop}
        onCancel={() => setDropTarget(null)}
      />

      {/* One-time secrets */}
      {secrets ? <SecretsModal creds={secrets} onClose={() => setSecrets(null)} /> : null}
    </div>
  );
}

// --- Modals ----------------------------------------------------------------

function ModalFooter({
  onClose,
  busy,
  submitLabel,
  disabled,
}: {
  onClose: () => void;
  busy: boolean;
  submitLabel: string;
  disabled?: boolean;
}) {
  return (
    <>
      <button type="button" className="btn" onClick={onClose} disabled={busy}>
        Cancel
      </button>
      <button type="submit" form="create-form" className="btn btn-primary" disabled={busy || disabled}>
        {busy ? "Working…" : submitLabel}
      </button>
    </>
  );
}

function useCreateError() {
  const [error, setError] = useState<ApiError | null>(null);
  const capture = (err: unknown) =>
    setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
  return { error, capture, clear: () => setError(null) };
}

function CreateUserModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (creds: CredentialResult) => void;
}) {
  const [username, setUsername] = useState("");
  const [busy, setBusy] = useState(false);
  const { error, capture, clear } = useCreateError();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    clear();
    try {
      const creds = await api.createRole({
        username: username.trim(),
        can_login: true,
        generate_password: true,
      });
      onCreated(creds);
    } catch (err) {
      capture(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="Create a login user"
      onClose={onClose}
      footer={<ModalFooter onClose={onClose} busy={busy} submitLabel="Create user" disabled={!username.trim()} />}
    >
      <p className="muted">
        A login user (role) can connect to the database. We generate a strong password and show it
        once — copy it now, it can&apos;t be shown again.
      </p>
      <form id="create-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <NameField label="User name" value={username} onChange={setUsername} placeholder="app_user" />
      </form>
    </Modal>
  );
}

function CreateReadonlyModal({
  databases,
  onClose,
  onCreated,
}: {
  databases: DatabaseInfo[];
  onClose: () => void;
  onCreated: (creds: CredentialResult) => void;
}) {
  const [username, setUsername] = useState("");
  const [database, setDatabase] = useState(databases[0]?.name ?? "");
  const [schema, setSchema] = useState("public");
  const [busy, setBusy] = useState(false);
  const { error, capture, clear } = useCreateError();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    clear();
    try {
      const creds = await api.createReadonlyUser({
        username: username.trim(),
        database,
        schema: schema.trim() || "public",
      });
      onCreated(creds);
    } catch (err) {
      capture(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="Create a read-only user"
      onClose={onClose}
      footer={
        <ModalFooter
          onClose={onClose}
          busy={busy}
          submitLabel="Create read-only user"
          disabled={!username.trim() || !database}
        />
      }
    >
      <p className="muted">
        This creates a user that can <strong>read but never change</strong> your data. We also set
        it up so it can automatically read <em>future</em> tables, so you don&apos;t have to
        re-grant access every time you add one.
      </p>
      <form id="create-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <NameField label="User name" value={username} onChange={setUsername} placeholder="readonly_user" />
        <label className="field">
          <span className="field-label">Database</span>
          <select value={database} onChange={(e) => setDatabase(e.target.value)} required>
            {databases.length === 0 ? <option value="">No databases — create one first</option> : null}
            {databases.map((db) => (
              <option key={db.name} value={db.name}>
                {db.name}
              </option>
            ))}
          </select>
        </label>
        <NameField label="Schema" value={schema} onChange={setSchema} placeholder="public" />
      </form>
    </Modal>
  );
}

function CreateDatabaseModal({
  roles,
  onClose,
  onCreated,
}: {
  roles: RoleInfo[];
  onClose: () => void;
  onCreated: () => void;
}) {
  const toast = useToast();
  const [name, setName] = useState("");
  const owners = roles.filter((r) => r.can_login || !r.is_superuser);
  const [owner, setOwner] = useState(owners[0]?.name ?? "");
  const [busy, setBusy] = useState(false);
  const { error, capture, clear } = useCreateError();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    clear();
    try {
      await api.createDatabase({ name: name.trim(), owner });
      toast.success(`Created database ${name.trim()}.`);
      onCreated();
    } catch (err) {
      capture(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="Create a database"
      onClose={onClose}
      footer={
        <ModalFooter
          onClose={onClose}
          busy={busy}
          submitLabel="Create database"
          disabled={!name.trim() || !owner}
        />
      }
    >
      <form id="create-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <NameField label="Database name" value={name} onChange={setName} placeholder="myapp" />
        <label className="field">
          <span className="field-label">Owner</span>
          <select value={owner} onChange={(e) => setOwner(e.target.value)} required>
            {owners.length === 0 ? <option value="">No suitable roles — create a user first</option> : null}
            {owners.map((r) => (
              <option key={r.name} value={r.name}>
                {r.name}
              </option>
            ))}
          </select>
          <span className="field-help muted">The owner can fully manage this database.</span>
        </label>
      </form>
    </Modal>
  );
}

function NewAppModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (creds: CredentialResult) => void;
}) {
  const [database, setDatabase] = useState("");
  const [busy, setBusy] = useState(false);
  const { error, capture, clear } = useCreateError();

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    clear();
    try {
      const creds = await api.createNewApp({ database: database.trim() });
      onCreated(creds);
    } catch (err) {
      capture(err);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="New app — one-click setup"
      onClose={onClose}
      footer={
        <ModalFooter onClose={onClose} busy={busy} submitLabel="Create app" disabled={!database.trim()} />
      }
    >
      <p className="muted">This creates everything a new application needs:</p>
      <ul className="bullet-list">
        <li>A database</li>
        <li>A read/write user (for your app to use)</li>
        <li>A read-only user (for dashboards, reporting, debugging)</li>
      </ul>
      <p className="muted">
        You&apos;ll get connection strings (DSNs) at the end — copy them now, the passwords are
        shown only once.
      </p>
      <form id="create-form" onSubmit={submit}>
        {error ? <ErrorNotice error={error} /> : null}
        <NameField label="App / database name" value={database} onChange={setDatabase} placeholder="myapp" />
      </form>
    </Modal>
  );
}

function SecretsModal({ creds, onClose }: { creds: CredentialResult; onClose: () => void }) {
  const secrets = creds.secrets ?? {};
  return (
    <Modal
      open
      title="Save these now"
      tone="danger"
      onClose={onClose}
      footer={
        <button type="button" className="btn btn-primary" onClick={onClose}>
          I&apos;ve saved them
        </button>
      }
    >
      <Callout tone="warn" title="Shown only once">
        These passwords and connection strings cannot be retrieved again. Copy them into your
        password manager before closing.
      </Callout>
      {creds.result.message ? <p className="muted">{creds.result.message}</p> : null}
      <div className="secrets-list">
        {Object.entries(secrets).map(([key, value]) => (
          <SecretValue key={key} label={prettyKey(key)} value={value} />
        ))}
      </div>
    </Modal>
  );
}

function NameField({
  label,
  value,
  onChange,
  placeholder,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  placeholder?: string;
}) {
  const invalid = value.length > 0 && !/^[a-z_][a-z0-9_]*$/.test(value);
  return (
    <label className="field">
      <span className="field-label">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        autoComplete="off"
        spellCheck={false}
        aria-invalid={invalid}
        onChange={(e) => onChange(e.target.value)}
      />
      <span className="field-help muted">
        Lowercase letters, numbers and underscores. Must start with a letter or underscore.
      </span>
      {invalid ? <span className="field-error">That name has characters Postgres won&apos;t accept.</span> : null}
    </label>
  );
}

function prettyKey(key: string): string {
  return key
    .replace(/_/g, " ")
    .replace(/\bdsn\b/gi, "DSN")
    .replace(/^\w/, (c) => c.toUpperCase());
}
