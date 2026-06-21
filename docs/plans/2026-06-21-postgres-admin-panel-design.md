# Postgres Admin Panel — Design

- **Date:** 2026-06-21
- **Status:** Approved design (pre-implementation)
- **Working name:** `pgpanel` (placeholder — trivially renamed; it's just the binary/module name)
- **Supersedes:** the `sm` CLI at `/primary01/git/server-management` (fresh rewrite, not a fork)

---

## 1. One-liner

> A single self-hosted binary that installs and **owns** a native Postgres, serves a
> private web admin panel to safely browse/query it and run backups, and pushes
> Postgres + backup + host metrics to any OTLP collector.

**Audience:** indie hackers running their own database server. Assume the operator
may not be a Postgres expert — the tool must be lightweight, fast, simple, and
**very safe**, asking for confirmation on anything risky.

---

## 2. Goals & non-goals

**Goals**
- Install one binary on a fresh box; it provisions Postgres and serves a dashboard.
- Safe day-2 operation: browse data, run basic read queries, see tables/schema.
- Manage roles & databases (including read-only users done correctly).
- pgBackRest backups with rich stats; restore and restore-*testing*.
- Cross-host migration (carried over from `sm`).
- Observability in-panel **and** exported via OTLP to any collector (SigNoz, etc.).
- Alerting via Pushover + webhooks with safe defaults out of the box.

**Non-goals (v1)**
- Pointing at a pre-existing/remote Postgres (the tool *creates* and owns the DB).
- Multi-server fleet management from one panel (stays one-box / one-panel).
- A team RBAC system inside the panel (single admin for now).
- Public internet exposure as the default access model.

---

## 3. Key decisions (locked)

| Area | Decision | Rationale |
|---|---|---|
| Language/stack | **Go**, single static binary, **embedded SPA** (`embed.FS`), **SQLite** for panel state | "Download one file, done." Instant startup, tiny footprint, no runtime on the box. |
| Frontend | React/Svelte SPA built to static files at compile time, embedded & served by Go | No Node on the server, ever. |
| Topology | **One box, one panel**, bound to a **private** address (localhost / Tailscale / private net) | Simplest + safest; matches "install it, it creates the Postgres." |
| PG runtime | **Native** (apt + systemd), reached over the **unix socket** | Best perf, simplest pgBackRest story, matches existing `sm` knowledge. |
| Auth | **Admin password** set at first run; signed session cookies; lockout; SSH-only `reset-password` | Universally understood; no SMTP; works over tunnel or Tailscale. |
| Query safety | **Read-first, guard-railed**: read-only role enforces read-only at the DB level; auto-LIMIT; statement timeout; mutations via guided, confirmed actions | A beginner cannot hang or harm the DB from the query box. |
| Backups | **pgBackRest → S3-compatible** (B2/S3/R2/MinIO), full + incremental, PITR | Rich built-in stats; battle-tested; carries over from `sm`. |
| Telemetry | Panel collects metrics → **in-panel dashboard** always; **OTLP push** optional | "Point it at SigNoz and it just works"; works with zero external setup too. |
| Alerting | **Pushover + generic webhook**, smart default rules, anti-spam, recovery notices | What indie hackers actually use; no extra infra. |
| Collision safety | **Single-writer ownership + fail-fast** on shared resources (esp. S3 repo) | Two panels sharing a backup repo would silently corrupt both. |

---

## 4. Architecture

### 4.1 Process & deployment
- The panel runs as its **own `systemd` service** (`pgpanel.service`).
- The Postgres it provisions runs as the normal **`postgresql` systemd service**.
- The panel talks to Postgres over the **local unix socket** — no TCP password juggling.
- **One panel per host**, enforced by systemd + a pidfile/socket lock (refuse to start a second instance).

### 4.2 Internal modules (all inside the one binary)
- **Web server** — serves the embedded SPA + a small JSON API. Binds private by default; never `0.0.0.0` unless explicitly forced.
- **Auth** — argon2id password hash in SQLite, signed session cookies, failure lockout, `pgpanel reset-password` CLI escape hatch (requires SSH/root on the box).
- **PG manager** — installs/configures Postgres; connects via two distinct roles:
  - **read-only role** → query box + table browsing (read-only enforced at the DB level).
  - **privileged path** → only for guided admin actions, each gated by confirmation.
- **Backup manager** — wraps pgBackRest (stanza + S3 target), runs full/incremental, parses JSON output into history, drives restore/PITR and restore-testing.
- **Migration manager** — single-DB / cluster / SSH-less session modes (see §5.6).
- **Telemetry** — sampling loop → in-panel dashboard + optional OTLP export.
- **Alerting** — rule engine over collected metrics → Pushover/webhook channels.
- **Scheduler** — internal cron for backups + sampling (no external cron).
- **Ownership/identity** — instance identity + S3 ownership markers (see §6).
- **Local store (SQLite)** — config, password hash, audit log, backup history, telemetry buffer. Separate from the managed Postgres so the panel still works if PG is down.

### 4.3 The core safety idea
The **read-only role enforces read-only at the Postgres level** — so even a bug in the
UI cannot turn a `SELECT` into a `DELETE`. The UI guard rails are a second layer, not
the only layer.

---

## 5. Feature surface

### 5.1 Provisioning
- Install native Postgres (apt + systemd), sane defaults, unix-socket access.
- Auto-enable required extensions: `pg_stat_statements` (slow queries), and one-click
  others (e.g. `pgvector`) via the extensions manager.
- Manage **PgBouncer** so apps pool connections; panel shows pool stats.

### 5.2 Browse & query (read-first, guard-railed)
- Query box is **read-only by default** (SELECT), **auto-LIMIT**, **statement timeout**.
- Browse tables with **keyset (cursor) pagination** — never `OFFSET` over millions of rows.
- View schema (columns, types, indexes, constraints, sizes).

### 5.3 Roles & databases (guided, each confirmed; via privileged path)
- Create **login users** with generated strong passwords (shown once, rotatable).
- Create **read-only users done correctly** — `CONNECT` + `USAGE` + `SELECT`
  **plus `ALTER DEFAULT PRIVILEGES`** so future tables are covered automatically.
- Create **databases** with an owner; grant/revoke at **readonly / readwrite / owner**.
- Rotate passwords; drop role/db gated by **typed-name confirmation**.
- **"New app" one-click** — create db + readwrite user + readonly user, print DSNs (through PgBouncer).

### 5.4 Backups (pgBackRest → S3-compatible)
- Full + incremental, scheduled by the internal scheduler, with retention.
- Stats surfaced (dashboard + OTLP): label, type, backup size, **compressed repo size**,
  duration, WAL range, success/fail.
- Destinations: any S3-compatible bucket (Backblaze B2, AWS S3, Cloudflare R2, MinIO),
  optional local copy.

### 5.5 Restore & restore-testing
- Point-in-time recovery (PITR) with guarded confirmation.
- **Restore-testing**: periodically restore the latest backup to a throwaway instance and
  verify it — answers "do my backups actually work?" before disaster strikes.

### 5.6 Migration (carried over from `sm`, three modes)
- **Single database** — pull one DB from another host. Transports: **via S3** (safer,
  resumable, no direct connectivity) or **direct SSH pipe** (faster on a LAN).
- **Whole cluster** — all databases + **globals (roles/grants)** via S3.
- **SSH-less session wizard** — target generates a 6-char code, source joins with it,
  **S3 is the only channel**. In the web UI: target shows the code + live "waiting for
  source…", source shows export progress, target auto-imports — **live progress bars**.
- **Safety DNA (all modes):** automatic **safety backup** before overwrite (with on-screen
  recovery instructions on failure), **checksum verification** of the dump, and
  **row-count comparison** source vs. target → green **"verified: N rows matched"** badge.
  Overwrite gated by explicit confirmation.

### 5.7 Observability (in-panel dashboard + OTLP)
- **Slow queries** via `pg_stat_statements` — top statements by total/mean time, calls, rows.
- **Live activity** via `pg_stat_activity` — running queries, lock waits, idle-in-transaction,
  with a **guarded cancel / terminate** action (risky → confirm).
- **Core health** — connections vs. max, cache-hit ratio, DB/table sizes & bloat hints,
  transaction rate, replication lag, deadlocks.
- All of it renders in the panel with zero external setup; the same metrics export via
  OTLP when an endpoint is configured.

### 5.8 Alerting
- **Channels:** Pushover + generic webhook (Slack/Discord/n8n/custom), each with a **send-test** button.
- **Smart defaults out of the box:** Postgres down; disk almost full; backup failed or no
  successful backup in N hours; connections near max; replication lag high.
- Tunable thresholds + custom rules (e.g. "any query > 30s").
- **Anti-spam:** severity levels, cooldown/re-notify intervals, automatic **recovery ("resolved")** notifications.
- **Dead-man's switch:** the panel heartbeats; if it goes silent, an external webhook can alert you.

### 5.9 Indie-hacker toolkit (built on the same foundation)
- Weekly **digest** to Pushover/webhook (size growth, backup summary, slowest queries, "disk full in ~N days" forecast).
- **Health score** — one green/red "is my DB OK."
- **Audit log** of every panel action.
- **Extensions manager** (`pg_stat_statements`, `pgvector`, …).
- **Disk & growth forecasting**.

---

## 6. Cross-cutting tenet: single-writer ownership & fail-fast

**Problem:** people may install two panels on two servers pointing at the **same S3
bucket**. Two panels sharing a pgBackRest repo would **silently corrupt both**. This must
be impossible.

**Every panel has a unique identity.** At install it generates a stable `instance_id`
(UUID + human label like hostname), stored in SQLite.

**Two layers of defense:**

1. **No collisions by construction** — default pgBackRest repo path / S3 prefix is
   namespaced by identity: `s3://bucket/panel/<instance_id>/...`. Two panels on the same
   bucket land in different prefixes automatically.

2. **Ownership marker + fail-fast** — before using any S3 location for backups, the panel
   reads/writes `s3://bucket/<repo>/.panel-owner.json` =
   `{instance_id, hostname, pg_system_identifier, claimed_at, last_seen}`:
   - **Absent** → claim it, proceed.
   - **Mine** → proceed.
   - **Someone else's** → **HARD STOP**, loud and actionable:
     > ⛔ This S3 location is already owned by panel `web-db-02` (host `10.0.0.5`), last
     > active 4 minutes ago. Two panels must never share a backup repository — it will
     > corrupt both. Use a different bucket/prefix, or if that server is truly gone, run
     > **Adopt this repository** (typed-name confirm).

   The marker carries a **heartbeat** (`last_seen`, updated each backup) to distinguish
   *actively-owned* from *abandoned*. Abandoned repos can be **adopted** with explicit
   confirmation; active ones cannot, period. The stored **Postgres system identifier**
   catches "different cluster, same repo" precisely — *before* pgBackRest fails cryptically.

**The one deliberate exception: migration.** Two panels intentionally share the bucket,
but only inside a **session-scoped, time-boxed, code-isolated** prefix
(`pg-migrations/sessions/<code>/`) that expires. Sharing is explicit, namespaced, temporary.

**General application of the tenet:**
- Any shared external resource is claimed, verified single-writer, and conflicts **stop the
  operation immediately with an actionable message** — never proceed into corruption.
- **Locally:** one panel per host (systemd + pidfile/socket lock).
- **Telemetry:** each panel exports a distinct `service.instance.id` so SigNoz never merges
  two servers' metrics.

---

## 7. Safety model (summary of guard rails)

- Read-only enforced **at the DB level**, not just the UI.
- Auto-LIMIT + statement timeout on the query box.
- All mutations are guided, explicit actions; **destructive ops require typed-name confirmation**.
- Automatic safety backup before any overwrite (migration/restore).
- Checksum + row-count verification on data movement.
- Single-writer ownership + fail-fast on shared resources.
- Private-by-default network binding.
- Full audit log of every action.

---

## 8. Local data model (SQLite, panel's own state)

Indicative tables:
- `instance` — `instance_id`, label, created_at, panel_version.
- `config` — key/value (binding, OTLP endpoint, backup target, schedules…).
- `auth` — admin password hash (argon2id), session secrets, lockout counters.
- `audit_log` — timestamp, actor, action, target, before/after summary, result.
- `backup_history` — label, type, sizes, duration, WAL range, result, repo path.
- `restore_tests` — when, source backup, verified rows, result.
- `alerts` — rule definitions, channel config, last-fired, cooldown state.
- `telemetry_buffer` — recent samples for the in-panel dashboard.

---

## 9. Telemetry metric catalog (in-panel + OTLP)

- **Host:** CPU, memory, disk used/free, disk-full forecast, load.
- **Postgres health:** connections vs. max, cache-hit ratio, TPS, deadlocks, replication lag,
  db/table sizes, bloat hints.
- **Slow queries:** top-N by total/mean time, calls, rows (`pg_stat_statements`).
- **Backups:** last run, size, compressed repo size, duration, success/fail, age of last good backup.
- **Restore tests:** last test time, verified rows, pass/fail.
- All exported with distinct `service.instance.id` resource attributes per panel.

---

## 10. Phased build plan

Everything is in scope; this is **build order** so there's something usable early.

1. **Foundation** — Go binary, embedded UI shell, install flow, admin auth, provision native Postgres, instance identity.
2. **Operate** — browse/query (read-only role), roles & databases, in-panel dashboard.
3. **Protect** — pgBackRest backups + stats, restore, restore-testing, **ownership/fail-fast**.
4. **Observe & alert** — OTLP export, slow-query/activity views, Pushover/webhook alerts + smart defaults.
5. **Move** — migration (all three modes).

---

## 11. Open questions

- **Final name** (placeholder `pgpanel`).
- Frontend framework: **React vs. Svelte** (either embeds fine; pick on dev preference).
- Default backup schedule & retention (proposed: daily incremental + weekly full, 14-day retention).
- Whether v1 ships the managed-OTel-Collector option for log scraping, or stays OTLP-push-only.

---

## 12. Carryover from `sm` (reference)

Reuse the *ideas* (and hard-won SQL/safety details), reimplemented in Go:
- pgBackRest stanza + S3 setup; `BackupInfo` stats model.
- pg_dump/pg_restore migration with safety backup, checksum, row-count verification.
- S3-coordinated, SSH-less migration **session** model (6-char code, expiry, status machine).
- Read-only user creation with `ALTER DEFAULT PRIVILEGES`.
- Preflight checks + audit logging + dry-run discipline.
