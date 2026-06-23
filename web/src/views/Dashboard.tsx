// Dashboard: PG health + backup status + host stats cards. Polls every 5s.

import type { ReactNode } from "react";
import { api } from "@/api/client";
import { usePolling } from "@/lib/hooks";
import {
  agoSeconds,
  bytes,
  count,
  dateTime,
  pct,
  ratioPct,
} from "@/lib/format";
import {
  Badge,
  Callout,
  Card,
  ErrorNotice,
  PageHeader,
  ResultBadge,
  Spinner,
  StaleBanner,
  StatCard,
} from "@/components/ui";
import type { DashboardData } from "@/api/types";

const POLL_MS = 5000;

export function Dashboard() {
  const { data, error, loading } = usePolling<DashboardData>(
    (signal) => api.dashboard(signal),
    POLL_MS,
  );

  if (loading && !data) return <Spinner label="Loading dashboard…" />;
  if (error && !data) return <ErrorNotice error={error} />;
  if (!data) return null;

  const { pg, snapshot: s, last_backup, health_ok, health_reasons } = data;

  const memPct = s.mem_total_bytes ? (s.mem_used_bytes / s.mem_total_bytes) * 100 : 0;
  const diskPct = s.disk_total_bytes ? (s.disk_used_bytes / s.disk_total_bytes) * 100 : 0;
  const connPct = s.max_connections ? (s.connections / s.max_connections) * 100 : 0;

  return (
    <div className="view">
      <PageHeader
        title="Dashboard"
        description="A live look at your database and server."
        actions={
          health_ok ? (
            <Badge tone="ok">● Healthy</Badge>
          ) : (
            <Badge tone="danger">● Needs attention</Badge>
          )
        }
      />

      {/* error here means a refresh failed while we still hold cached data —
          the first-load failure path already returned <ErrorNotice> above. */}
      {error ? <StaleBanner error={error} /> : null}

      {!health_ok && health_reasons && health_reasons.length > 0 ? (
        <Callout tone="warn" title="Things to look at">
          <ul className="bullet-list">
            {health_reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        </Callout>
      ) : null}

      {/* Postgres + backup status */}
      <div className="card-grid">
        <Card title="Postgres">
          <div className="kv-list">
            <Kv label="Status">
              {pg.running ? <Badge tone="ok">Running</Badge> : <Badge tone="danger">Stopped</Badge>}
            </Kv>
            <Kv label="Version">{pg.version ?? "—"}</Kv>
            <Kv label="Connections">
              {count(s.connections)} / {count(s.max_connections)}{" "}
              <span className="muted">({pct(connPct)})</span>
            </Kv>
            <Kv label="Cache hit ratio">{ratioPct(s.cache_hit_ratio)}</Kv>
            <Kv label="Transactions / sec">{s.tps.toFixed(1)}</Kv>
            <Kv label="Deadlocks">{count(s.deadlocks)}</Kv>
            <Kv label="Replication lag">
              {s.replication_lag_seconds > 0 ? `${s.replication_lag_seconds.toFixed(1)}s` : "none"}
            </Kv>
          </div>
        </Card>

        <Card title="Latest backup">
          {last_backup ? (
            <div className="kv-list">
              <Kv label="Result">
                <ResultBadge result={last_backup.result} />
              </Kv>
              <Kv label="Type">{last_backup.backup_type}</Kv>
              <Kv label="When">{dateTime(last_backup.started_at)}</Kv>
              <Kv label="Age">{agoSeconds(s.last_backup_age_seconds)}</Kv>
              <Kv label="Backup size">{bytes(last_backup.size_bytes)}</Kv>
              <Kv label="Compressed in repo">{bytes(last_backup.repo_bytes)}</Kv>
            </div>
          ) : (
            <Callout tone="warn">
              No successful backup yet. Run one from the Backups page to protect your data.
            </Callout>
          )}
        </Card>
      </div>

      {/* Host stats */}
      <Card title="Server">
        <div className="stat-grid">
          <StatCard
            label="CPU"
            value={pct(s.cpu_percent)}
            sub={`load ${s.load1.toFixed(2)}`}
            tone={s.cpu_percent > 90 ? "danger" : s.cpu_percent > 75 ? "warn" : "neutral"}
          />
          <StatCard
            label="Memory"
            value={pct(memPct)}
            sub={`${bytes(s.mem_used_bytes)} of ${bytes(s.mem_total_bytes)}`}
            tone={memPct > 90 ? "danger" : memPct > 80 ? "warn" : "neutral"}
          />
          <StatCard
            label="Disk"
            value={pct(diskPct)}
            sub={`${bytes(s.disk_used_bytes)} of ${bytes(s.disk_total_bytes)}`}
            tone={diskPct > 90 ? "danger" : diskPct > 80 ? "warn" : "neutral"}
          />
          <StatCard
            label="Connections"
            value={`${count(s.connections)}/${count(s.max_connections)}`}
            sub={pct(connPct)}
            tone={connPct > 90 ? "danger" : connPct > 75 ? "warn" : "neutral"}
          />
        </div>
        <p className="muted updated-at">Updated {dateTime(s.taken_at)} · refreshes every 5s</p>
      </Card>
    </div>
  );
}

function Kv({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="kv">
      <span className="kv-label">{label}</span>
      <span className="kv-value">{children}</span>
    </div>
  );
}
