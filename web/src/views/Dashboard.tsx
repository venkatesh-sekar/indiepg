// Dashboard: PG health + backup status + host stats cards. Polls every 5s.

import type { ReactNode } from "react";
import { api } from "@/api/client";
import { cn } from "@/lib/utils";
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
  Card,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Badge,
  Callout,
  ErrorNotice,
  PageHeader,
  ResultBadge,
  StaleBanner,
} from "@/components/ui";
import type { DashboardData } from "@/api/types";

const POLL_MS = 5000;

export function Dashboard() {
  const { data, error, loading } = usePolling<DashboardData>(
    (signal) => api.dashboard(signal),
    POLL_MS,
  );

  if (loading && !data) return <DashboardSkeleton />;
  if (error && !data) return <ErrorNotice error={error} />;
  if (!data) return null;

  const { pg, snapshot: s, last_backup, health_ok, health_reasons } = data;

  const memPct = s.mem_total_bytes ? (s.mem_used_bytes / s.mem_total_bytes) * 100 : 0;
  const diskPct = s.disk_total_bytes ? (s.disk_used_bytes / s.disk_total_bytes) * 100 : 0;
  const connPct = s.max_connections ? (s.connections / s.max_connections) * 100 : 0;

  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
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
          <ul className="list-disc pl-5">
            {health_reasons.map((r) => (
              <li key={r}>{r}</li>
            ))}
          </ul>
        </Callout>
      ) : null}

      {/* Postgres + backup status */}
      <div className="grid gap-5 sm:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Postgres</CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="flex flex-col" aria-label="Postgres details">
              <Kv label="Status">
                {pg.running ? <Badge tone="ok">Running</Badge> : <Badge tone="danger">Stopped</Badge>}
              </Kv>
              {/* Connections lives once, in the Server card below, as a tinted
                  saturation gauge alongside CPU/Memory/Disk — keeping it here too
                  was a duplicate with no extra signal. */}
              <Kv label="Cache hit ratio">{ratioPct(s.cache_hit_ratio)}</Kv>
              <Kv label="Transactions / sec">{s.tps.toFixed(1)}</Kv>
              <Kv label="Deadlocks">{count(s.deadlocks)}</Kv>
              <Kv label="Replication lag">
                {s.replication_lag_seconds > 0 ? `${s.replication_lag_seconds.toFixed(1)}s` : "none"}
              </Kv>
            </dl>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Latest backup</CardTitle>
          </CardHeader>
          <CardContent>
            {last_backup ? (
              <dl className="flex flex-col" aria-label="Latest backup details">
                <Kv label="Result">
                  <ResultBadge result={last_backup.result} />
                </Kv>
                <Kv label="Type">{last_backup.backup_type}</Kv>
                <Kv label="When">{dateTime(last_backup.started_at)}</Kv>
                <Kv label="Age">{agoSeconds(s.last_backup_age_seconds)}</Kv>
                <Kv label="Backup size">{bytes(last_backup.size_bytes)}</Kv>
                <Kv label="Compressed in repo">{bytes(last_backup.repo_bytes)}</Kv>
              </dl>
            ) : (
              <Callout tone="warn">
                No successful backup yet. Run one from the Backups page to protect your data.
              </Callout>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Host stats */}
      <Card>
        <CardHeader>
          <CardTitle>Server</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid grid-cols-2 gap-3.5 sm:grid-cols-4">
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
          <p className="mt-3.5 text-xs text-muted-foreground">
            Updated <time dateTime={s.taken_at}>{dateTime(s.taken_at)}</time> · refreshes every 5s
          </p>
        </CardContent>
      </Card>
    </div>
  );
}

/** A labeled key/value row inside a card. */
function Kv({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-dashed py-[7px] last:border-b-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right font-medium">{children}</dd>
    </div>
  );
}

/** A single labeled host metric, tinted when it crosses a threshold. */
function StatCard({
  label,
  value,
  sub,
  tone = "neutral",
}: {
  label: string;
  value: ReactNode;
  sub?: ReactNode;
  tone?: "neutral" | "warn" | "danger";
}) {
  return (
    <Card size="sm" className="gap-1 bg-muted/40">
      <CardContent className="flex flex-col gap-0.5">
        <span className="text-xs uppercase tracking-wide text-muted-foreground">{label}</span>
        <span
          className={cn(
            "text-2xl font-bold tabular-nums",
            tone === "warn" && "text-warning",
            tone === "danger" && "text-destructive",
          )}
        >
          {value}
        </span>
        {sub ? <span className="text-xs text-muted-foreground">{sub}</span> : null}
      </CardContent>
    </Card>
  );
}

/** Skeleton placeholder shown on first load, before any data arrives. */
function DashboardSkeleton() {
  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5" role="status" aria-label="Loading dashboard…">
      <div className="flex items-start justify-between gap-4">
        <div className="flex flex-col gap-2">
          <Skeleton className="h-7 w-40" />
          <Skeleton className="h-4 w-64" />
        </div>
        <Skeleton className="h-6 w-28" />
      </div>
      <div className="grid gap-5 sm:grid-cols-2">
        <Skeleton className="h-64" />
        <Skeleton className="h-64" />
      </div>
      <Skeleton className="h-44" />
    </div>
  );
}
