// DatabaseTuning: a read-only surface (GET /api/tuning) showing how Postgres is
// sized to this box on best defaults — the live applied settings plus the
// recommendation for each workload profile, so the operator can see what each
// override would do. It changes nothing: a profile switch resizes
// shared_buffers/max_connections and needs a Postgres restart, so it stays a
// provision-time action, not a button here.

import { useState } from "react";
import { api } from "@/api/client";
import { useAsync } from "@/lib/hooks";
import { Callout, Card, ErrorNotice, Spinner } from "@/components/ui";
import { Field, FieldDescription, FieldTitle } from "@/components/ui/field";
import {
  ToggleGroup,
  ToggleGroupItem,
} from "@/components/ui/toggle-group";
import {
  Table,
  TableBody,
  TableCaption,
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

export function DatabaseTuning() {
  const tuning = useAsync<TuningStatus>(() => api.getTuning(), []);

  return (
    <Card title="Database tuning (host-sized)">
      {tuning.loading ? (
        <Spinner label="Loading tuning…" />
      ) : tuning.error ? (
        <ErrorNotice error={tuning.error} />
      ) : tuning.data ? (
        <TuningPanel status={tuning.data} />
      ) : null}
    </Card>
  );
}

/** TuningPanel is the presentational core, split out so it can be tested with
 *  fixed data (no network). */
export function TuningPanel({ status }: { status: TuningStatus }) {
  const [preview, setPreview] = useState<WorkloadProfile>(status.active_profile);
  const active = PROFILE_EFFECTS[status.active_profile];
  const previewRec = status.profiles.find((p) => p.profile === preview);
  const isPreviewingOther = preview !== status.active_profile;

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
        <SettingsTable
          caption="Currently applied"
          values={status.applied}
        />
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
          <ToggleGroup
            type="single"
            variant="outline"
            value={preview}
            // A profile must always be selected; ignore the empty value Radix
            // emits when the active item is clicked again to deselect.
            onValueChange={(v) => v && setPreview(v as WorkloadProfile)}
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
          <FieldDescription id="tuning-profile-effect">
            {PROFILE_EFFECTS[preview].effect}
          </FieldDescription>
        </Field>

        {previewRec ? (
          <SettingsTable
            caption={
              isPreviewingOther
                ? `${PROFILE_EFFECTS[preview].label} would size this server to`
                : "Recommended for this server"
            }
            values={previewRec}
          />
        ) : null}

        {isPreviewingOther ? (
          <Callout tone="info" title="This is a preview — nothing changes here">
            Switching from <strong>{active.label}</strong> to{" "}
            <strong>{PROFILE_EFFECTS[preview].label}</strong> would resize{" "}
            <code>shared_buffers</code> and <code>max_connections</code>, which
            requires a brief Postgres restart. To keep your database safe, a
            profile change is applied at install/provision time — not from this
            screen.
          </Callout>
        ) : null}
      </div>
    </div>
  );
}

/** SettingsTable lists the five host-sized settings for either the applied
 *  values or a profile recommendation (both share the *_mb / count fields). */
function SettingsTable({
  caption,
  values,
}: {
  caption: string;
  values: AppliedTuning | TuningRecommendation;
}) {
  return (
    <Table>
      <TableCaption>{caption}</TableCaption>
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
  );
}
