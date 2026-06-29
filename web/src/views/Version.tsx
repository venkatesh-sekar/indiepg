// Version: see the running PostgreSQL version, apply minor updates, run a gated
// major upgrade, and resolve the two-phase finalize/rollback that follows one.
//
// The flows are long-running operations that follow the same async pattern as
// backups/migration: a POST kicks the work off, then this page polls
// GET /api/pg/upgrade/status for live progress and to resume after a reload.
// While an operation is in flight — or a major upgrade is awaiting finalize —
// the panel refuses to start a second one (a single global lock, server-side).

import { useEffect, useRef, useState, type ReactNode } from "react";
import { ApiError, api } from "@/api/client";
import { useAsync, usePolling } from "@/lib/hooks";
import { bytes } from "@/lib/format";
import { Modal } from "@/components/Modal";
import { toast } from "sonner";
import { backupFreshness } from "@/views/Backups";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
  StaleBanner,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import {
  Field,
  FieldDescription,
  FieldLabel,
} from "@/components/ui/field";
import type {
  BackupHistory,
  Check,
  CheckStatus,
  PendingFinalization,
  PGVersionInfo,
  PreflightResult,
  UpgradeOperation,
  UpgradeStatus,
} from "@/api/types";

const STATUS_POLL_MS = 3000;

/** The short "16.2" form pulled from a full server_version string. */
function shortVersion(full: string): string {
  return full.split(" ")[0] || full;
}

export function Version() {
  const version = useAsync<PGVersionInfo>((s) => api.pgVersion(s), []);
  const status = usePolling<UpgradeStatus>((s) => api.upgradeStatus(s), STATUS_POLL_MS);

  const op = status.data?.operation ?? null;
  const running = op?.status === "running";
  // Prefer the live status feed for pending state; fall back to the version
  // snapshot so the banner still shows on first paint before status arrives.
  const pending = status.data?.pending_finalization ?? version.data?.pending_finalization ?? null;

  const [wizardTarget, setWizardTarget] = useState<number | null>(null);
  const [minorOpen, setMinorOpen] = useState(false);
  // A failed op surfaces a dismissible banner so the stderr isn't lost in a toast.
  const [dismissedFailure, setDismissedFailure] = useState<string | null>(null);

  // When an operation finishes, refresh the version info (new running version,
  // cleared update offers) and drop the wizard. A one-shot toast confirms it.
  const prevRunning = useRef(false);
  const reloadVersion = version.reload;
  useEffect(() => {
    if (prevRunning.current && !running) {
      reloadVersion();
      setWizardTarget(null);
      if (op?.status === "success") toast.success(successMessage(op));
      else if (op?.status === "failed") toast.error(op.error || "The upgrade did not complete.");
    }
    prevRunning.current = running;
  }, [running, op, reloadVersion]);

  const refresh = () => {
    version.reload();
    status.reload();
  };

  const failedOp =
    !running && op?.status === "failed" && op.started_at !== dismissedFailure ? op : null;

  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Version"
        description="See the PostgreSQL version you're running, apply updates, and upgrade to a new major release — safely and reversibly."
      />

      {version.error && version.data ? <StaleBanner error={version.error} /> : null}

      {running && op ? (
        <UpgradeProgress op={op} />
      ) : wizardTarget !== null && version.data ? (
        <MajorUpgradeWizard
          target={wizardTarget}
          current={version.data.current.major}
          onCancel={() => setWizardTarget(null)}
          onStarted={refresh}
        />
      ) : version.loading && !version.data ? (
        <Spinner label="Reading the PostgreSQL version…" />
      ) : version.error && !version.data ? (
        <ErrorNotice error={version.error} />
      ) : version.data ? (
        <>
          {failedOp ? (
            <FailedOpBanner op={failedOp} onDismiss={() => setDismissedFailure(failedOp.started_at)} />
          ) : null}

          <RunningVersionCard info={version.data} />

          {pending ? (
            <PendingFinalizationBanner pending={pending} onChanged={refresh} />
          ) : (
            <>
              <MinorUpdateCard info={version.data} onUpgrade={() => setMinorOpen(true)} />
              <MajorUpgradeCard info={version.data} onUpgrade={(m) => setWizardTarget(m)} />
            </>
          )}
        </>
      ) : null}

      {minorOpen && version.data ? (
        <MinorUpgradeDialog
          info={version.data}
          onClose={() => setMinorOpen(false)}
          onStarted={() => {
            setMinorOpen(false);
            refresh();
          }}
        />
      ) : null}
    </div>
  );
}

// --- Running version --------------------------------------------------------

function RunningVersionCard({ info }: { info: PGVersionInfo }) {
  return (
    <Card title="Running version">
      <dl className="flex flex-col" aria-label="PostgreSQL version">
        <Row label="Status">
          {info.running ? (
            <Badge tone="ok">Running</Badge>
          ) : (
            <Badge tone="danger">Stopped</Badge>
          )}
        </Row>
        <Row label="Major">{info.current.major || "—"}</Row>
        <Row label="Full version">
          <span className="font-mono text-sm">{info.current.full || "—"}</span>
        </Row>
      </dl>
    </Card>
  );
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-dashed py-[7px] last:border-b-0">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right font-medium">{children}</dd>
    </div>
  );
}

// --- Minor update -----------------------------------------------------------

function MinorUpdateCard({
  info,
  onUpgrade,
}: {
  info: PGVersionInfo;
  onUpgrade: () => void;
}) {
  const { minor } = info.available;
  return (
    <Card title="Minor update">
      {minor.available ? (
        <div className="flex flex-col gap-3">
          <p className="text-muted-foreground">
            A minor update is available. Minor updates are low-risk bug-and-security
            fixes — the on-disk format is unchanged, so it&apos;s an apt upgrade and a
            few-second restart.
          </p>
          <div className="flex items-center gap-3">
            <span className="font-mono text-lg font-semibold">
              {shortVersion(info.current.full)} <span aria-hidden="true">→</span>{" "}
              <span className="text-primary">{minor.target}</span>
            </span>
            <Badge tone="info">update available</Badge>
          </div>
          <Button className="self-start" onClick={onUpgrade}>
            Update to {minor.target}
          </Button>
        </div>
      ) : (
        <EmptyState
          title="Up to date"
          hint={`You're on the latest minor release for PostgreSQL ${info.current.major}.`}
        />
      )}
    </Card>
  );
}

// --- Major upgrade entry points ---------------------------------------------

function MajorUpgradeCard({
  info,
  onUpgrade,
}: {
  info: PGVersionInfo;
  onUpgrade: (major: number) => void;
}) {
  const majors = info.available.majors;
  return (
    <Card title="Major upgrade">
      {majors.length === 0 ? (
        <EmptyState
          title="On the latest major"
          hint={`PostgreSQL ${info.current.major} is the newest major the panel offers — nothing to upgrade to.`}
        />
      ) : (
        <div className="flex flex-col gap-3">
          <p className="text-muted-foreground">
            A major upgrade (e.g. {info.current.major} → {majors[0].major}) moves the
            on-disk format forward via <code>pg_upgrade</code>. It&apos;s gated behind
            pre-flight checks and a mandatory backup, and stays reversible — the old
            version is kept until you finalize.
          </p>
          <div className="flex flex-wrap gap-2">
            {majors.map((m) => (
              <Button key={m.major} variant="outline" onClick={() => onUpgrade(m.major)}>
                Upgrade to {m.major}
                {m.default ? (
                  <Badge tone="ok">recommended</Badge>
                ) : null}
              </Button>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
}

// --- Minor upgrade dialog ---------------------------------------------------

function MinorUpgradeDialog({
  info,
  onClose,
  onStarted,
}: {
  info: PGVersionInfo;
  onClose: () => void;
  onStarted: () => void;
}) {
  const history = useAsync<BackupHistory>(() => api.backupHistory(), []);
  const [backup, setBackup] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const target = info.available.minor.target;

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.upgradeMinor(backup);
      toast.success(backup ? "Backing up, then updating…" : "Minor update started.");
      onStarted();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={`Update to ${target}?`}
      width="md"
      dismissible={!busy}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="button" onClick={start} disabled={busy}>
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Starting…
              </>
            ) : (
              `Update to ${target}`
            )}
          </Button>
        </>
      }
    >
      {error ? <ErrorNotice error={error} /> : null}
      <p className="text-muted-foreground">
        This upgrades PostgreSQL {info.current.major} from{" "}
        <strong>{shortVersion(info.current.full)}</strong> to <strong>{target}</strong> with
        apt, then restarts the service. The only downtime is the restart — a few seconds.
      </p>

      <BackupFreshnessNotice history={history} />

      <Field orientation="horizontal" className="mt-4">
        <Checkbox
          id="minor-backup"
          checked={backup}
          onCheckedChange={(c) => setBackup(c === true)}
          disabled={busy}
        />
        <FieldLabel htmlFor="minor-backup" className="font-normal">
          Take a fresh backup first (recommended)
        </FieldLabel>
      </Field>
    </Modal>
  );
}

/** A tailored stale-backup warning derived from the real backup history, reusing
 *  the Backups page's freshness classifier so the wording stays consistent. */
function BackupFreshnessNotice({
  history,
}: {
  history: { data: BackupHistory | null; loading: boolean };
}) {
  if (history.loading || !history.data) return null;
  const state = backupFreshness(history.data.backups);

  if (state.kind === "good") {
    return (
      <Callout tone="ok" title="You have a recent backup" className="mt-3">
        Your most recent backup succeeded. A minor update is low-risk, but keeping the
        box below ticked makes it bulletproof.
      </Callout>
    );
  }
  if (state.kind === "none") {
    return (
      <Callout tone="warn" title="No backup yet" className="mt-3">
        You haven&apos;t made a backup. Even though a minor update is low-risk, take one
        first — leave the box below ticked.
      </Callout>
    );
  }
  return (
    <Callout tone="warn" title="Your backup is stale" className="mt-3">
      Your most recent backup attempt failed or none has ever succeeded. Take a fresh
      one before updating — leave the box below ticked.
    </Callout>
  );
}

// --- Major upgrade wizard ---------------------------------------------------

function MajorUpgradeWizard({
  target,
  current,
  onCancel,
  onStarted,
}: {
  target: number;
  current: number;
  onCancel: () => void;
  onStarted: () => void;
}) {
  const pre = useAsync<PreflightResult>(() => api.preflightMajorUpgrade(target), [target]);
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [startErr, setStartErr] = useState<ApiError | null>(null);

  const blocked =
    !pre.data || pre.data.preview.blocking || pre.data.checks.some((c) => c.status === "fail");

  const start = async () => {
    setBusy(true);
    setStartErr(null);
    try {
      await api.startMajorUpgrade(target);
      toast.success(`Upgrading to PostgreSQL ${target}…`);
      onStarted();
    } catch (err) {
      setStartErr(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card
      title={`Upgrade to PostgreSQL ${target}`}
      actions={
        <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>
          Cancel
        </Button>
      }
    >
      <p className="text-muted-foreground">
        Moving from major <strong>{current}</strong> to <strong>{target}</strong>. The panel
        first installs the target packages and runs the checks below — nothing is changed yet.
      </p>

      {pre.loading ? (
        <div className="mt-4">
          <Spinner label="Installing target packages and running pre-flight checks…" />
        </div>
      ) : pre.error ? (
        <div className="mt-4">
          <ErrorNotice error={pre.error} />
        </div>
      ) : pre.data ? (
        <div className="mt-4 flex flex-col gap-5">
          <CheckList checks={pre.data.checks} />
          <UpgradePreviewView result={pre.data} />

          {blocked ? (
            <Callout tone="danger" title="This upgrade is blocked">
              One or more checks failed. Resolve the red items above, then re-open this
              upgrade to run the checks again.
            </Callout>
          ) : (
            <Callout tone="warn" title="A fresh backup is taken automatically">
              When you start, the panel takes a mandatory pgBackRest backup before touching
              anything. The old PostgreSQL {current} cluster is preserved afterward so you can
              roll back during a verification window.
            </Callout>
          )}

          {startErr ? <ErrorNotice error={startErr} /> : null}

          {!blocked ? (
            <Field orientation="horizontal">
              <Checkbox
                id="major-ack"
                checked={ack}
                onCheckedChange={(c) => setAck(c === true)}
                disabled={busy}
              />
              <FieldLabel htmlFor="major-ack" className="font-normal">
                I understand: a backup is taken first, there is a short downtime window, and the
                old cluster is kept for rollback until I finalize.
              </FieldLabel>
            </Field>
          ) : null}

          <div className="flex gap-2">
            <Button variant="outline" onClick={onCancel} disabled={busy}>
              Back
            </Button>
            <Button onClick={start} disabled={blocked || !ack || busy}>
              {busy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Starting…
                </>
              ) : (
                `Start upgrade to ${target}`
              )}
            </Button>
          </div>
        </div>
      ) : null}
    </Card>
  );
}

function CheckList({ checks }: { checks: Check[] }) {
  if (checks.length === 0) return null;
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-muted-foreground">Pre-flight checks</h3>
      <ul className="flex flex-col gap-2">
        {checks.map((c) => (
          <li
            key={c.id}
            className="flex items-start gap-3 rounded-md border p-3"
          >
            <CheckBadge status={c.status} />
            <div className="flex flex-col gap-0.5">
              <span className="font-medium">{c.title}</span>
              <span className="text-sm text-muted-foreground">{c.message}</span>
              {c.remediation ? (
                <span className="text-sm text-foreground">
                  <strong>Fix:</strong> {c.remediation}
                </span>
              ) : null}
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
}

function CheckBadge({ status }: { status: CheckStatus }) {
  switch (status) {
    case "pass":
      return <Badge tone="ok">✓ pass</Badge>;
    case "warn":
      return <Badge tone="warn">! warn</Badge>;
    case "fail":
      return <Badge tone="danger">✕ fail</Badge>;
    default:
      return <Badge>{status}</Badge>;
  }
}

function UpgradePreviewView({ result }: { result: PreflightResult }) {
  const p = result.preview;
  const diskTight = p.disk_required_bytes > 0 && p.disk_required_bytes > p.disk_free_bytes;
  return (
    <div className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-muted-foreground">What will happen</h3>
      <dl className="flex flex-col rounded-md border p-3">
        <Row label="Upgrade">
          PostgreSQL {p.from_major} <span aria-hidden="true">→</span> {p.to_major}
        </Row>
        <Row label="Disk required">
          <span className={diskTight ? "text-destructive" : undefined}>
            {bytes(p.disk_required_bytes)}
          </span>
        </Row>
        <Row label="Disk free">{bytes(p.disk_free_bytes)}</Row>
        <Row label="Extensions carried over">
          {p.extensions.length > 0 ? (
            <span className="flex flex-wrap justify-end gap-1">
              {p.extensions.map((e) => (
                <Badge key={e} tone="neutral">
                  {e}
                </Badge>
              ))}
            </span>
          ) : (
            "none"
          )}
        </Row>
      </dl>
      <p className="text-sm text-muted-foreground">
        The old cluster is preserved (stopped, moved aside) so the upgrade is reversible until
        you finalize.
      </p>
    </div>
  );
}

// --- Live operation progress ------------------------------------------------

const KIND_TITLES: Record<UpgradeOperation["kind"], string> = {
  minor: "Applying minor update",
  major: "Major upgrade in progress",
  finalize: "Finalizing upgrade",
  rollback: "Rolling back",
};

function UpgradeProgress({ op }: { op: UpgradeOperation }) {
  return (
    <Card title={KIND_TITLES[op.kind] ?? "Upgrade in progress"}>
      <div className="flex flex-col gap-3">
        <Spinner label={op.phase || "Working…"} />
        <p className="text-sm text-muted-foreground">
          This continues even if you leave the page — come back here any time to check on it.
          {op.kind === "major"
            ? " Do not stop PostgreSQL or reboot the server while this runs."
            : null}
        </p>
        {(op.log?.length ?? 0) > 0 ? <OpLog log={op.log ?? []} /> : null}
      </div>
    </Card>
  );
}

function FailedOpBanner({ op, onDismiss }: { op: UpgradeOperation; onDismiss: () => void }) {
  return (
    <Callout tone="danger" title={`${KIND_TITLES[op.kind] ?? "Upgrade"} failed`}>
      <p>{op.error || "The operation did not complete."}</p>
      <p className="mt-1 text-sm">
        Nothing was deleted — your data and the old cluster are intact. Review the output below,
        resolve the cause, and try again. If a major upgrade left the new version in a bad state,
        the rollback option appears once it reaches the pending-finalization step.
      </p>
      {(op.log?.length ?? 0) > 0 ? <OpLog log={op.log ?? []} /> : null}
      <Button variant="outline" size="sm" className="mt-2" onClick={onDismiss}>
        Dismiss
      </Button>
    </Callout>
  );
}

function OpLog({ log }: { log: string[] }) {
  return (
    <pre className="mt-2 max-h-64 overflow-auto rounded-md border bg-muted/40 p-3 text-[13px] leading-relaxed text-foreground">
      {log.join("\n")}
    </pre>
  );
}

// --- Pending finalization (shared with the dashboard) -----------------------

/**
 * The persistent banner shown after a major upgrade lands: live on the new
 * major, the old cluster stopped and ready as a rollback. Offers Finalize
 * (reclaim the old cluster's disk, behind a type-the-old-version guard) and
 * Roll back (loudly warned: it discards any writes made since the upgrade).
 * Used on both the Version panel and the dashboard.
 */
export function PendingFinalizationBanner({
  pending,
  onChanged,
}: {
  pending: PendingFinalization;
  onChanged: () => void;
}) {
  const [finalizeOpen, setFinalizeOpen] = useState(false);
  const [rollbackOpen, setRollbackOpen] = useState(false);

  return (
    <Callout
      tone="info"
      title={`Upgraded to PostgreSQL ${pending.to_major} — verify your app, then reclaim space`}
    >
      <p>
        You&apos;re now running PostgreSQL {pending.to_major}. The old PostgreSQL{" "}
        {pending.from_major} cluster is kept (stopped) as a rollback for now, using{" "}
        <strong>{bytes(pending.reclaimable_bytes)}</strong> of disk. Once you&apos;ve confirmed
        your app works, finalize to free that space — or roll back if something&apos;s wrong.
      </p>
      <div className="mt-3 flex flex-wrap gap-2">
        <Button onClick={() => setFinalizeOpen(true)}>
          Finalize (reclaim {bytes(pending.reclaimable_bytes)})
        </Button>
        <Button
          variant="outline"
          className="text-destructive hover:text-destructive"
          onClick={() => setRollbackOpen(true)}
        >
          Roll back to {pending.from_major}
        </Button>
      </div>

      {finalizeOpen ? (
        <FinalizeDialog
          pending={pending}
          onClose={() => setFinalizeOpen(false)}
          onDone={() => {
            setFinalizeOpen(false);
            onChanged();
          }}
        />
      ) : null}
      {rollbackOpen ? (
        <RollbackDialog
          pending={pending}
          onClose={() => setRollbackOpen(false)}
          onDone={() => {
            setRollbackOpen(false);
            onChanged();
          }}
        />
      ) : null}
    </Callout>
  );
}

function FinalizeDialog({
  pending,
  onClose,
  onDone,
}: {
  pending: PendingFinalization;
  onClose: () => void;
  onDone: () => void;
}) {
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const expected = String(pending.from_major);
  const matches = typed.trim() === expected;

  const finalize = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.finalizeUpgrade(pending.from_major);
      toast.success("Finalizing — reclaiming the old cluster's disk.");
      onDone();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title="Finalize the upgrade?"
      tone="danger"
      width="md"
      dismissible={!busy}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={finalize}
            disabled={busy || !matches}
          >
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Finalizing…
              </>
            ) : (
              "Finalize & reclaim space"
            )}
          </Button>
        </>
      }
    >
      {error ? <ErrorNotice error={error} /> : null}
      <Callout tone="danger" title="This is the point of no return">
        Finalizing drops the old PostgreSQL {pending.from_major} cluster and frees{" "}
        <strong>{bytes(pending.reclaimable_bytes)}</strong>. After this you{" "}
        <strong>cannot roll back</strong>. Only finalize once you&apos;ve verified your app on
        PostgreSQL {pending.to_major}.
      </Callout>
      <Field className="mt-4">
        <FieldLabel htmlFor="finalize-confirm">
          Type <code>{expected}</code> (the old major) to confirm
        </FieldLabel>
        <Input
          id="finalize-confirm"
          type="text"
          autoComplete="off"
          autoCorrect="off"
          spellCheck={false}
          inputMode="numeric"
          value={typed}
          placeholder={expected}
          aria-invalid={typed.length > 0 && !matches}
          onChange={(e) => setTyped(e.target.value)}
        />
        <FieldDescription>This guards against an accidental, irreversible drop.</FieldDescription>
      </Field>
    </Modal>
  );
}

function RollbackDialog({
  pending,
  onClose,
  onDone,
}: {
  pending: PendingFinalization;
  onClose: () => void;
  onDone: () => void;
}) {
  const [ack, setAck] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);

  const rollback = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.rollbackUpgrade();
      toast.success(`Rolling back to PostgreSQL ${pending.from_major}…`);
      onDone();
    } catch (err) {
      setError(asApiError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      title={`Roll back to PostgreSQL ${pending.from_major}?`}
      tone="danger"
      width="md"
      dismissible={!busy}
      onClose={onClose}
      footer={
        <>
          <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button
            type="button"
            variant="destructive"
            onClick={rollback}
            disabled={busy || !ack}
          >
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Rolling back…
              </>
            ) : (
              `Roll back to ${pending.from_major}`
            )}
          </Button>
        </>
      }
    >
      {error ? <ErrorNotice error={error} /> : null}
      <Callout tone="danger" title="Any writes since the upgrade will be LOST">
        Rolling back returns you to the old PostgreSQL {pending.from_major} cluster exactly as it
        was at the moment of the upgrade. <strong>Every change written to PostgreSQL{" "}
        {pending.to_major} since then — new rows, edits, deletes — is discarded and cannot be
        recovered.</strong> If your app has been live on the new version, do not roll back
        without understanding this.
      </Callout>
      <Field orientation="horizontal" className="mt-4">
        <Checkbox
          id="rollback-ack"
          checked={ack}
          onCheckedChange={(c) => setAck(c === true)}
          disabled={busy}
        />
        <FieldLabel htmlFor="rollback-ack" className="font-normal">
          I understand that writes made on PostgreSQL {pending.to_major} since the upgrade will be
          permanently lost.
        </FieldLabel>
      </Field>
    </Modal>
  );
}

// --- Dashboard wrapper ------------------------------------------------------

/**
 * Self-fetching pending-finalization banner for the dashboard. Reads the version
 * endpoint (which carries the pending state) and renders the shared banner only
 * when a major upgrade awaits finalize; otherwise nothing.
 */
export function DashboardUpgradeBanner() {
  const version = useAsync<PGVersionInfo>((s) => api.pgVersion(s), []);
  const pending = version.data?.pending_finalization ?? null;
  if (!pending) return null;
  return <PendingFinalizationBanner pending={pending} onChanged={version.reload} />;
}

// --- helpers ----------------------------------------------------------------

function successMessage(op: UpgradeOperation): string {
  switch (op.kind) {
    case "minor":
      return "PostgreSQL updated.";
    case "major":
      return `Upgraded to PostgreSQL ${op.target_major}. Verify your app, then finalize.`;
    case "finalize":
      return "Finalized — the old cluster's disk has been reclaimed.";
    case "rollback":
      return `Rolled back to PostgreSQL ${op.target_major}.`;
    default:
      return "Done.";
  }
}

function asApiError(err: unknown): ApiError {
  return err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) });
}
