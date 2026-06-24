// Pooler: the opt-in PgBouncer connection pooler surface on Settings.
//
// Off by default. When off, it explains what a pooler does, lets the operator
// pick which app roles to route, and — behind a confirm dialog that states
// exactly what will happen (install the package, start the service, route the
// chosen roles) — turns it on via POST /api/pooler/enable. When on, it shows the
// loopback address apps connect to and the host-sized pool settings, each labeled
// by effect. It never changes Postgres or touches data; enabling is the only
// action and it is explicitly confirmed.

import { useState } from "react";
import { ApiError, api } from "@/api/client";
import { useAsync } from "@/lib/hooks";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  Spinner,
} from "@/components/ui";
import type { PoolRecommendation, PoolerStatus, RoleInfo } from "@/api/types";

/** Each pool setting with a plain-English meaning, so no number is an
 *  unexplained knob. Order: what apps see → what Postgres sees → lifecycle. */
const POOL_SETTINGS: {
  key: keyof PoolRecommendation;
  label: string;
  help: string;
  unit?: string;
}[] = [
  {
    key: "max_client_conn",
    label: "Client connections accepted",
    help: "How many app connections the pooler will accept at once. Many clients share a few real Postgres connections.",
  },
  {
    key: "default_pool_size",
    label: "Pooled connections per database",
    help: "Real Postgres connections the pooler keeps open and shares across requests.",
  },
  {
    key: "min_pool_size",
    label: "Kept-warm connections",
    help: "Always ready, so the first request after a quiet spell is fast.",
  },
  {
    key: "reserve_pool_size",
    label: "Burst overflow",
    help: "Extra connections allowed briefly when traffic spikes.",
  },
  {
    key: "server_idle_timeout",
    label: "Idle timeout",
    help: "How long an unused pooled connection stays open before it closes.",
    unit: "s",
  },
];

/** PoolSettingsTable shows the host-sized pool sizing, each row labeled by what
 *  it does. */
export function PoolSettingsTable({ pool }: { pool: PoolRecommendation }) {
  return (
    <table className="tuning-table">
      <caption className="tuning-caption">Pool sizing for this server</caption>
      <tbody>
        {POOL_SETTINGS.map((s) => (
          <tr key={s.key}>
            <th scope="row">
              {s.label}
              <span className="field-help muted"> — {s.help}</span>
            </th>
            <td className="tuning-value">
              {pool[s.key]}
              {s.unit ?? ""}
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

export function Pooler() {
  const status = useAsync<PoolerStatus>(() => api.poolerStatus(), []);
  // Roles drive which app traffic gets routed; only needed to enable, so a roles
  // load error is surfaced inside the panel, not fatal to showing status.
  const roles = useAsync<RoleInfo[]>(() => api.listRoles(), []);

  return (
    <Card title="Connection pooler (PgBouncer)">
      {status.loading ? (
        <Spinner label="Loading pooler status…" />
      ) : status.error ? (
        <ErrorNotice error={status.error} />
      ) : status.data ? (
        <PoolerPanel
          status={status.data}
          roles={roles.data ?? []}
          rolesLoading={roles.loading}
          rolesError={roles.error}
          onChanged={status.reload}
        />
      ) : null}
    </Card>
  );
}

/** PoolerPanel is the presentational + action core, split out so it can be
 *  tested with fixed data (no network). */
export function PoolerPanel({
  status,
  roles,
  rolesLoading = false,
  rolesError,
  onChanged,
}: {
  status: PoolerStatus;
  roles: RoleInfo[];
  rolesLoading?: boolean;
  rolesError?: ApiError | null;
  onChanged: () => void;
}) {
  const address = `${status.host}:${status.listen_port}`;
  if (status.enabled) {
    return <EnabledView status={status} address={address} onChanged={onChanged} />;
  }
  return (
    <DisabledView
      status={status}
      address={address}
      roles={roles}
      rolesLoading={rolesLoading}
      rolesError={rolesError}
      onChanged={onChanged}
    />
  );
}

/** The pooler is on: show where apps connect, the pool sizing in force, and let
 *  the operator turn it back off behind a confirm that states what stops. */
function EnabledView({
  status,
  address,
  onChanged,
}: {
  status: PoolerStatus;
  address: string;
  onChanged: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const disable = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.disablePooler();
      toast.success("Connection pooler disabled.");
      // Close before reloading status so the panel doesn't re-render (loading on)
      // while the dialog is still marked open.
      setConfirming(false);
      onChanged();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err
          : new ApiError(0, { code: "internal", message: String(err) }),
      );
      // Keep the dialog open so the operator sees what failed, but stop "Working…".
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="tuning">
      <Callout tone="ok" title="The connection pooler is on">
        Your apps can connect through PgBouncer at <code>{address}</code> instead
        of connecting to Postgres directly. The pooler shares a small set of real
        Postgres connections across many app connections, so a busy app
        won&apos;t exhaust <code>max_connections</code>.
      </Callout>
      {status.pool ? (
        <PoolSettingsTable pool={status.pool} />
      ) : (
        <Callout tone="info" title="Pool sizing unavailable">
          The panel couldn&apos;t read Postgres just now to show the live pool
          sizing. The pooler is still running.
        </Callout>
      )}

      <div className="btn-row">
        <button
          type="button"
          className="btn btn-danger"
          onClick={() => {
            setError(null);
            setConfirming(true);
          }}
        >
          Disable connection pooler
        </button>
      </div>

      <ConfirmDialog
        open={confirming}
        title="Disable the connection pooler?"
        confirmLabel="Disable pooler"
        tone="danger"
        busy={busy}
        onCancel={() => setConfirming(false)}
        onConfirm={disable}
        message={
          <>
            <p>Disabling the pooler will, on this server:</p>
            <ul>
              <li>
                Stop the PgBouncer service — it will no longer accept connections
                at <code>{address}</code>.
              </li>
              <li>Prevent it from starting again on reboot.</li>
            </ul>
            <p>
              Any app still pointed at <code>{address}</code> will fail to connect
              until you point it back at Postgres directly. This does{" "}
              <strong>not</strong> restart Postgres and does not touch your data.
              You can re-enable the pooler at any time.
            </p>
            {error ? <ErrorNotice error={error} /> : null}
          </>
        }
      />
    </div>
  );
}

/** The pooler is off: explain it, let the operator choose roles, and enable it
 *  behind a confirm that states exactly what will happen. */
function DisabledView({
  status,
  address,
  roles,
  rolesLoading,
  rolesError,
  onChanged,
}: {
  status: PoolerStatus;
  address: string;
  roles: RoleInfo[];
  rolesLoading: boolean;
  rolesError?: ApiError | null;
  onChanged: () => void;
}) {
  // Only non-superuser login roles are app roles worth routing; superusers
  // connect directly (the pool reserves connections for admin/superuser).
  const eligible = roles.filter((r) => r.can_login && !r.is_superuser);

  const [selected, setSelected] = useState<string[]>([]);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const toggle = (name: string) =>
    setSelected((cur) =>
      cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name],
    );

  const enable = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.enablePooler({ roles: selected });
      toast.success("Connection pooler enabled.");
      // Close the dialog before reloading status so the panel never re-renders
      // (status.reload flips loading on) while the dialog is still marked open.
      setConfirming(false);
      onChanged();
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err
          : new ApiError(0, { code: "internal", message: String(err) }),
      );
      // Keep the dialog open so the operator sees what failed, but stop "Working…".
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="tuning">
      <header className="card-head" style={{ padding: 0 }}>
        <span />
        <Badge tone="neutral">Off</Badge>
      </header>

      <Callout tone="info" title="A connection pooler is optional">
        Postgres opens a new process for every connection, so a busy app (or
        serverless functions) can exhaust <code>max_connections</code> and start
        refusing clients. PgBouncer sits in front and shares a small set of real
        Postgres connections across many app connections. If your app holds a
        steady, modest number of connections, you likely don&apos;t need this.
      </Callout>

      {status.pool ? (
        <>
          <p className="muted">
            When enabled, your apps connect through the pooler at{" "}
            <code>{address}</code> instead of Postgres directly. It would be sized
            for this server as:
          </p>
          <PoolSettingsTable pool={status.pool} />
        </>
      ) : (
        <Callout tone="warn" title="Postgres is unreachable">
          The pool is sized from Postgres&apos; <code>max_connections</code>, which
          the panel can&apos;t read right now. Start Postgres, then reload this page
          to enable the pooler.
        </Callout>
      )}

      {rolesError ? <ErrorNotice error={rolesError} /> : null}

      <fieldset className="field" style={{ border: "none", padding: 0, margin: 0 }}>
        <legend className="field-label">Route these roles through the pooler</legend>
        {rolesLoading ? (
          <Spinner label="Loading roles…" />
        ) : eligible.length === 0 ? (
          <EmptyState
            title="No app roles to route yet"
            hint="Create a login role for your app on the Roles & Databases page first, then come back to enable the pooler."
          />
        ) : (
          <>
            <p className="field-help muted">
              Pick the login roles your apps use. At least one is required — the
              pooler only accepts connections for roles you route here.
            </p>
            {eligible.map((r) => (
              <label className="checkbox" key={r.name}>
                <input
                  type="checkbox"
                  checked={selected.includes(r.name)}
                  onChange={() => toggle(r.name)}
                />
                <span>{r.name}</span>
              </label>
            ))}
          </>
        )}
      </fieldset>

      <div className="btn-row">
        <button
          type="button"
          className="btn btn-primary"
          disabled={!status.pool || selected.length === 0}
          onClick={() => {
            setError(null);
            setConfirming(true);
          }}
        >
          Enable connection pooler
        </button>
      </div>

      <ConfirmDialog
        open={confirming}
        title="Enable the connection pooler?"
        confirmLabel="Enable pooler"
        busy={busy}
        onCancel={() => setConfirming(false)}
        onConfirm={enable}
        message={
          <>
            <p>Enabling the pooler will, on this server:</p>
            <ul>
              <li>Install the PgBouncer package.</li>
              <li>
                Start the PgBouncer service, listening on <code>{address}</code>.
              </li>
              <li>
                Route {selected.length} role{selected.length === 1 ? "" : "s"}{" "}
                through it: <strong>{selected.join(", ")}</strong>.
              </li>
            </ul>
            <p>
              Your apps then connect to <code>{address}</code> instead of Postgres
              directly. This does <strong>not</strong> restart Postgres and does
              not touch your data. You can keep connecting directly to Postgres as
              well.
            </p>
            {error ? <ErrorNotice error={error} /> : null}
          </>
        }
      />
    </div>
  );
}
