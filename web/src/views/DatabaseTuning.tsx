// DatabaseTuning: how Postgres is sized to this box, and the one place to change
// it. It shows the live applied settings (GET /api/tuning) and, when they would
// differ, the host-sized recommendation for the previewed workload profile
// (OLTP / Mixed / OLAP). When the previewed profile's sizing differs from what's
// applied — a different profile, or drift from the active one — it offers an
// Apply button. Applying usually resizes shared_buffers/max_connections, which
// restarts Postgres (a few seconds of downtime) with rollback to last-known-good
// on failure; a reloadable-only drift instead applies with a zero-downtime
// reload, and we tailor the confirm copy + success toast to which one will
// actually happen. The server applies FIRST and only then persists the chosen
// profile, so the returned TuningStatus is the source of truth we render
// afterwards. In the normal case (applied already matches the active profile's
// recommendation) only the single "Currently applied" table shows — no confusing
// duplicate.

import { useId, useRef, useState } from "react";
import { ApiError, api } from "@/api/client";
import { useAsync } from "@/lib/hooks";
import { toast } from "sonner";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import { Callout, Card, ErrorNotice, Spinner } from "@/components/ui";
import { Field, FieldDescription, FieldTitle } from "@/components/ui/field";
import {
  ToggleGroup,
  ToggleGroupItem,
} from "@/components/ui/toggle-group";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
} from "@/components/ui/table";
import type {
  AppliedTuning,
  TuningRecommendation,
  TuningStatus,
  WorkloadProfile,
} from "@/api/types";

/** Plain-English label + effect for each workload profile, so an override is
 *  never an unexplained knob. */
export const PROFILE_EFFECTS: Record<
  WorkloadProfile,
  { label: string; effect: string }
> = {
  oltp: {
    label: "OLTP",
    effect:
      "Many short, concurrent transactions — typical web apps. Allows more connections and caches less per query.",
  },
  mixed: {
    label: "Mixed",
    effect:
      "Balanced general-purpose sizing — the best default for an indie-hacker box running an app plus the occasional report.",
  },
  olap: {
    label: "OLAP",
    effect:
      "Fewer, heavier analytical queries — reporting and dashboards. Caches more aggressively and gives each query more memory, with fewer connections.",
  },
};

/** Order profiles are displayed in: lighter → heavier per query. */
export const PROFILE_ORDER: WorkloadProfile[] = ["oltp", "mixed", "olap"];

/** mbLabel renders a megabyte figure as the friendliest unit: whole GB when it
 *  divides evenly, one decimal GB above a gigabyte, otherwise MB. */
export function mbLabel(mb: number): string {
  if (mb >= 1024) {
    const gb = mb / 1024;
    return Number.isInteger(gb) ? `${gb} GB` : `${gb.toFixed(1)} GB`;
  }
  return `${mb} MB`;
}

/** The five host-sized settings, in the order shown, with a one-line meaning. */
const SETTINGS: {
  key: keyof AppliedTuning & keyof TuningRecommendation;
  label: string;
  help: string;
  kind: "mem" | "count";
}[] = [
  {
    key: "shared_buffers_mb",
    label: "Shared buffers",
    help: "Memory Postgres uses to cache data pages. Sized to your RAM.",
    kind: "mem",
  },
  {
    key: "effective_cache_size_mb",
    label: "Effective cache size",
    help: "A hint about total cache available (RAM the OS + Postgres can use). Guides query plans.",
    kind: "mem",
  },
  {
    key: "work_mem_mb",
    label: "Work mem",
    help: "Memory each sort/hash step may use before spilling to disk.",
    kind: "mem",
  },
  {
    key: "maintenance_work_mem_mb",
    label: "Maintenance work mem",
    help: "Memory for VACUUM, index builds, and other maintenance.",
    kind: "mem",
  },
  {
    key: "max_connections",
    label: "Max connections",
    help: "How many clients can connect at once. Sized to your CPU and profile.",
    kind: "count",
  },
];

/** settingsDiffer is true when any of the five host-sized settings in the live
 *  `applied` values differs from a profile `rec`. It drives both the
 *  duplicate-table fix (no point showing a recommendation identical to what's
 *  applied) and whether an Apply is meaningful — an identical re-apply would be a
 *  server-side no-op, so we don't offer the button for it. */
function settingsDiffer(
  applied: AppliedTuning,
  rec: TuningRecommendation,
): boolean {
  return SETTINGS.some((s) => applied[s.key] !== rec[s.key]);
}

export function DatabaseTuning() {
  const tuning = useAsync<TuningStatus>(() => api.getTuning(), []);
  // An apply returns the fresh TuningStatus (re-read applied values + the
  // now-persisted active profile). We render that response in place of the
  // initial fetch rather than refetching — the server already handed us the
  // truth, so a second round-trip would only add latency. This mirrors how
  // Pooler refreshes after an action, but from the response we already hold.
  const [appliedStatus, setAppliedStatus] = useState<TuningStatus | null>(null);
  const status = appliedStatus ?? tuning.data;

  return (
    <Card title="Database tuning (host-sized)">
      {tuning.loading ? (
        <Spinner label="Loading tuning…" />
      ) : tuning.error ? (
        <ErrorNotice error={tuning.error} />
      ) : status ? (
        <TuningPanel status={status} onApplied={setAppliedStatus} />
      ) : null}
    </Card>
  );
}

/** TuningPanel is the presentational + action core, split out so it can be
 *  tested with fixed data (no network). `onApplied` receives the fresh status the
 *  apply returns so the parent can re-render the new active profile without a
 *  second fetch (mirrors how Pooler refreshes from its action result). */
export function TuningPanel({
  status,
  onApplied,
}: {
  status: TuningStatus;
  onApplied: (next: TuningStatus) => void;
}) {
  const [preview, setPreview] = useState<WorkloadProfile>(status.active_profile);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  // A focus anchor over the profile toggles. On a successful apply the Apply
  // button unmounts (applied now matches the active recommendation); Radix's
  // dialog-close focus restoration then targets that removed button and drops
  // focus to <body>, stranding keyboard / screen-reader users. We park focus
  // here instead, a stable, recoverable spot next to the toggles (see apply()).
  const toggleGroupRef = useRef<HTMLDivElement>(null);

  const active = PROFILE_EFFECTS[status.active_profile];
  const previewLabel = PROFILE_EFFECTS[preview].label;
  const previewRec = status.profiles.find((p) => p.profile === preview);
  const isPreviewingOther = preview !== status.active_profile;

  // Does the previewed profile's sizing actually differ from the live applied
  // values? When Postgres is unreachable (applied is null) there's nothing to
  // compare against, so we can't claim a difference.
  const differs =
    status.applied != null &&
    previewRec != null &&
    settingsDiffer(status.applied, previewRec);

  // Show the recommendation table only when it tells the operator something the
  // "Currently applied" table doesn't: a different profile's sizing, real drift
  // from the active profile's recommendation, or (PG unreachable) the only sizing
  // we can show. In the normal case (active == preview, applied == recommendation)
  // the two tables would be identical, so we show just the applied one.
  const showRec =
    previewRec != null && (isPreviewingOther || differs || status.applied == null);

  // When may we offer Apply? Whenever PG is reachable AND either the previewed
  // sizing actually differs (a real write: a profile switch, or drift to repair),
  // OR the operator is previewing a DIFFERENT profile than the active/persisted one
  // even though its sizing already matches. That second case is the persist-recovery
  // path: Postgres was retuned but recording the profile choice failed, so the
  // active profile reads stale while the live settings already match the intended
  // one — `differs` is false, and hiding Apply there would block the very re-apply
  // the server told the operator is safe. We still require status.applied != null:
  // with Postgres unreachable there is nothing to apply against (and `differs`
  // already implies it, but isPreviewingOther does not).
  const canApply = status.applied != null && (isPreviewingOther || differs);

  // A pure no-op apply: previewing a different profile whose sizing already matches
  // the live applied values (so `differs` is false). Nothing changes server-side —
  // ApplyProfile issues no ALTER SYSTEM, no restart, no reload — it only records the
  // chosen profile. The confirm copy must therefore promise neither downtime nor a
  // settings change, just that we'll save the selection. (`canApply` guarantees
  // status.applied != null here.)
  const recordOnly = isPreviewingOther && !differs;

  // Will applying actually restart Postgres? Only shared_buffers and
  // max_connections are restart-requiring (PGC_POSTMASTER — see
  // internal/pg/tuning_apply.go tunedSettings); the other three are reloadable.
  // A profile SWITCH always changes both, so it restarts; a reloadable-only DRIFT
  // (e.g. a hand-edited work_mem) applies with a zero-downtime pg_reload_conf. We
  // branch the confirm copy + success toast off this so a non-DBA is never warned
  // of downtime that won't happen, and a "restarted" toast is never shown for an
  // apply that only reloaded.
  const restartNeeded =
    status.applied != null &&
    previewRec != null &&
    (status.applied.shared_buffers_mb !== previewRec.shared_buffers_mb ||
      status.applied.max_connections !== previewRec.max_connections);

  // Drift: the active profile is the one selected, yet the live applied values no
  // longer match its recommendation (e.g. a hand edit to postgresql.conf / a manual
  // ALTER SYSTEM). We call this out explicitly — otherwise the operator sees two
  // same-profile tables and an Apply button for an already-"current" profile and
  // can't tell it from a bug. (`differs` already implies status.applied != null.)
  const driftedFromActive = !isPreviewingOther && differs;

  // Apply the previewed profile: the server resizes shared_buffers/max_connections
  // (a Postgres restart, rolled back on failure) and only then persists the
  // profile, returning the fresh status. On success we hand that up and the active
  // profile flips; on failure we surface the rollback reason and change nothing.
  const apply = async () => {
    setBusy(true);
    setError(null);
    try {
      const next = await api.applyTuning(preview);
      // Match the toast to what actually happened: a restart-bearing switch, a
      // zero-downtime reload, or a pure record (settings already matched). Claiming
      // "Postgres restarted" for a reload — or "Applied" for a no-op that only saved
      // the choice — would be a lie that erodes trust the next time it matters.
      toast.success(
        restartNeeded
          ? `Applied ${previewLabel} profile — Postgres restarted`
          : recordOnly
            ? `Recorded ${previewLabel} profile`
            : `Applied ${previewLabel} profile`,
      );
      // Close the dialog before handing the fresh status up so the panel doesn't
      // re-render while the dialog is still marked open.
      setConfirming(false);
      onApplied(next);
      // The Apply button is being unmounted by the re-render onApplied triggers,
      // so Radix's dialog-close focus restoration would land on a removed node and
      // fall back to <body>. Re-anchor focus on the toggles AFTER that render
      // commits (a macrotask later) so a keyboard/AT user keeps their place.
      setTimeout(() => toggleGroupRef.current?.focus(), 0);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err
          : new ApiError(0, { code: "internal", message: String(err) }),
      );
      // Keep the dialog open so the operator can read the rollback reason; the
      // applied profile is unchanged (the server didn't persist it).
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="flex flex-col gap-4">
      <Callout tone="info" title="Sized to this server automatically">
        Postgres is tuned to this machine on safe best defaults — you don&apos;t
        need to tune anything by hand. This server has{" "}
        <strong>{mbLabel(status.memory_mb)} RAM</strong> and{" "}
        <strong>
          {status.cpu_count} CPU{status.cpu_count === 1 ? "" : "s"}
        </strong>
        , and is tuned for the <strong>{active.label}</strong> profile.
      </Callout>

      {status.applied ? (
        <SettingsTable label="Currently applied" values={status.applied} />
      ) : (
        <Callout tone="warn" title="Live settings unavailable">
          The panel couldn&apos;t read the running settings from Postgres right
          now (it may be stopped or unreachable). The recommended sizing for this
          server is still shown below.
        </Callout>
      )}

      <div className="flex flex-col gap-4">
        <Field>
          {/* A <div> (FieldTitle), not a <label>: it labels the ToggleGroup's
              role="group" via aria-labelledby, and a <label> can only target a
              form control. */}
          <FieldTitle id="tuning-profile-label">Workload profile</FieldTitle>
          {/* tabIndex={-1} wrapper: not in the tab order, but a programmatic
              focus() target so apply() can re-anchor focus here after the Apply
              button unmounts (see toggleGroupRef). */}
          <div tabIndex={-1} ref={toggleGroupRef}>
            <ToggleGroup
              type="single"
              variant="outline"
              value={preview}
              // A profile must always be selected; ignore the empty value Radix
              // emits when the active item is clicked again to deselect. Switching
              // profiles is a fresh action context, so drop any stale error from a
              // previous failed apply rather than leaving it stranded under an
              // unrelated preview.
              onValueChange={(v) => {
                if (!v) return;
                setPreview(v as WorkloadProfile);
                setError(null);
              }}
              aria-labelledby="tuning-profile-label"
              aria-describedby="tuning-profile-effect"
            >
              {PROFILE_ORDER.map((p) => (
                <ToggleGroupItem key={p} value={p}>
                  {PROFILE_EFFECTS[p].label}
                  {p === status.active_profile ? " — current" : ""}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>
          {/* aria-describedby alone is not a live region: when an AT user arrows
              between profiles inside the group the effect text changes silently.
              aria-live="polite" announces the new description after the toggle's own
              selection announcement, so the choice is informed without a focus
              round-trip. */}
          <FieldDescription id="tuning-profile-effect" aria-live="polite">
            {PROFILE_EFFECTS[preview].effect}
          </FieldDescription>
        </Field>

        {driftedFromActive ? (
          <Callout tone="warn" title="Settings have drifted from this profile">
            The live Postgres settings no longer match the {active.label} profile
            recommendation — possibly after a manual <code>ALTER SYSTEM</code> or a
            direct edit to <code>postgresql.conf</code>. Applying will restore them.
          </Callout>
        ) : null}

        {showRec && previewRec ? (
          <SettingsTable
            label={
              isPreviewingOther
                ? `${previewLabel} would size this server to`
                : "Recommended for this server"
            }
            values={previewRec}
          />
        ) : null}

        {canApply ? (
          <div className="flex flex-col gap-2">
            <div className="flex">
              {/* disabled={busy} is a no-op in the normal case (busy is false while
                  the button is shown), but it closes the brief async window after a
                  dialog confirm — setConfirming(false) then onApplied() are two
                  un-batched commits across components — during which a second click
                  could otherwise open a duplicate confirm while an apply is in flight. */}
              <Button
                disabled={busy}
                onClick={() => {
                  setError(null);
                  setConfirming(true);
                }}
              >
                {/* "… profile" so the trigger reads identically to the dialog's
                    confirm button — one action, one label, for screen-reader users. */}
                Apply {previewLabel} profile
              </Button>
            </div>
            {/* A failed apply (rollback / CodeSafety / store write) keeps the dialog
                open so the operator can read the reason — but the instant they dismiss
                it the dialog unmounts and that message would vanish, leaving the panel
                looking exactly as it did before a real restart was attempted. Mirror
                the reason here so it survives dialog dismissal as a permanent record,
                without blocking further interaction. It clears on the next Apply click
                and whenever the previewed profile changes (a fresh action context). */}
            {error && !confirming ? <ErrorNotice error={error} /> : null}
          </div>
        ) : status.applied == null && previewRec != null ? (
          // Postgres is unreachable, so the recommendation is shown but there's
          // nothing live to diff against and nothing to restart — applying is
          // impossible. Say why right where the Apply button would be, not only in
          // the "Live settings unavailable" callout further up the panel.
          <p className="text-sm text-muted-foreground">
            Start Postgres to apply this profile.
          </p>
        ) : null}
      </div>

      <ConfirmDialog
        open={confirming}
        title={`Apply the ${previewLabel} profile?`}
        confirmLabel={`Apply ${previewLabel} profile`}
        busy={busy}
        onCancel={() => setConfirming(false)}
        onConfirm={apply}
        message={
          <>
            {restartNeeded ? (
              <>
                {/* Exact, honest wording: name the blast radius (restart, dropped
                    connections) before it happens, and that nothing else changes. */}
                <p>
                  This resizes shared_buffers and max_connections, so it restarts
                  Postgres now — about a few seconds of downtime. Open connections
                  will drop and reconnect. Nothing else changes.
                </p>
                {/* The paragraph above is all-downside; add the safety net so a
                    cautious non-DBA isn't scared off a reversible, data-safe change.
                    This is literally what ApplyTuning's restartWithRollback /
                    CodeSafety path does — revert to last-known-good if the postmaster
                    rejects the new value — and only sizing knobs change, never data. */}
                <p>
                  If the restart fails, Postgres automatically rolls back to its
                  current settings — your data is never touched.
                </p>
              </>
            ) : recordOnly ? (
              <>
                {/* Pure no-op recovery: the previewed profile's sizing already
                    matches the running settings, so the server changes nothing — it
                    only records the choice. Don't promise a restart OR a reload that
                    won't happen; say exactly what this does. */}
                <p>
                  The {previewLabel} sizing already matches what Postgres is
                  running, so this only records {previewLabel} as the active profile
                  — no restart, no reload, and no settings change. Open connections
                  stay up.
                </p>
              </>
            ) : (
              <>
                {/* Reloadable-only drift: shared_buffers and max_connections already
                    match, so the server applies the change with pg_reload_conf — no
                    restart, no downtime. Don't threaten downtime that won't happen. */}
                <p>
                  This applies new settings with a quick reload — no restart and no
                  downtime, and open connections stay up. Nothing else changes.
                </p>
                <p>Only sizing settings change — your data is never touched.</p>
              </>
            )}
            {/* Whichever path it takes, the choice isn't a one-way door. The "scary
                moment" for a non-DBA is fear of being stuck on the new profile; state
                plainly that it's reversible (the toggle stays, "— current" just
                moves), so a cautious operator isn't talked out of a safe change. */}
            <p>You can switch profiles again anytime.</p>
            {error ? <ErrorNotice error={error} /> : null}
          </>
        }
      />
    </div>
  );
}

/** SettingsTable lists the five host-sized settings for either the applied
 *  values or a profile recommendation (both share the *_mb / count fields).
 *  `label` is rendered as a visible heading ABOVE the rows — not in the bottom
 *  caption the shared Table defaults to (caption-bottom) — so when two tables
 *  stack (applied vs recommended) a sighted operator knows which one they're
 *  reading before the first row, not after the fifth. aria-labelledby ties the
 *  heading to the table so the association is programmatic for assistive tech,
 *  not merely visual. */
function SettingsTable({
  label,
  values,
}: {
  label: string;
  values: AppliedTuning | TuningRecommendation;
}) {
  const labelId = useId();
  return (
    <div className="flex flex-col gap-1">
      <p id={labelId} className="text-sm font-medium text-foreground">
        {label}
      </p>
      <Table aria-labelledby={labelId}>
        <TableBody>
          {SETTINGS.map((s) => {
            const v = values[s.key];
            return (
              <TableRow key={s.key}>
                <TableHead
                  scope="row"
                  className="align-top whitespace-normal font-normal"
                >
                  <span className="font-medium">{s.label}</span>
                  <span className="text-muted-foreground"> — {s.help}</span>
                </TableHead>
                <TableCell className="text-right align-top font-mono tabular-nums">
                  {s.kind === "mem" ? mbLabel(v) : v}
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </div>
  );
}
