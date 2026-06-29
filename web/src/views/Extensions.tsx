// Extensions: discover, install and remove PostgreSQL extensions per database.
// Installs are fully transparent — for the ones that need an OS package or a
// Postgres restart the Add dialog shows exactly what the panel will run and
// offers an "I'll run these myself" escape hatch. Drops require typing the name.

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { useAsync } from "@/lib/hooks";
import { Modal } from "@/components/Modal";
import { TypedConfirmDialog } from "@/components/ConfirmDialog";
import { toast } from "sonner";
import {
  Badge,
  Callout,
  Card,
  EmptyState,
  ErrorNotice,
  PageHeader,
  Spinner,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import {
  Field,
  FieldDescription,
  FieldLabel,
} from "@/components/ui/field";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type {
  AvailableExtension,
  DatabaseInfo,
  ExtensionList,
  ExtensionTier,
  Result,
} from "@/api/types";

const DEFAULT_DB = "postgres";

export function Extensions() {
  const dbs = useAsync<DatabaseInfo[]>(() => api.listDatabases(), []);
  const [database, setDatabase] = useState<string>(DEFAULT_DB);

  // Once the database list loads, fall back to the first database if the target
  // (default "postgres") isn't actually present on this cluster.
  useEffect(() => {
    if (dbs.data && dbs.data.length > 0 && !dbs.data.some((d) => d.name === database)) {
      setDatabase(dbs.data[0].name);
    }
  }, [dbs.data, database]);

  const exts = useAsync<ExtensionList>(() => api.listExtensions(database), [database]);

  const [addTarget, setAddTarget] = useState<AvailableExtension | null>(null);
  const [dropTarget, setDropTarget] = useState<string | null>(null);
  const [dropBusy, setDropBusy] = useState(false);
  // Track every in-flight Tier 1 add by name, so two quick clicks each keep
  // their own "Adding…" spinner instead of one overwriting the other.
  const [readyBusy, setReadyBusy] = useState<Set<string>>(new Set());
  const [updateBusy, setUpdateBusy] = useState<Set<string>>(new Set());

  // A Tier 1 ("ready") add is a single CREATE EXTENSION — no dialog, just do it.
  const addReady = async (ext: AvailableExtension) => {
    setReadyBusy((prev) => new Set(prev).add(ext.name));
    try {
      const res = await api.installExtension({ database, name: ext.name, confirm: "" });
      toast.success(res.message || `Installed ${ext.name}.`);
      exts.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Could not install ${ext.name}.`);
    } finally {
      setReadyBusy((prev) => {
        const next = new Set(prev);
        next.delete(ext.name);
        return next;
      });
    }
  };

  // Apply an available ALTER EXTENSION ... UPDATE for an installed extension.
  const doUpdate = async (name: string) => {
    setUpdateBusy((prev) => new Set(prev).add(name));
    try {
      const res = await api.updateExtension(name, database);
      toast.success(res.message || `Updated ${name}.`);
      exts.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Could not update ${name}.`);
    } finally {
      setUpdateBusy((prev) => {
        const next = new Set(prev);
        next.delete(name);
        return next;
      });
    }
  };

  const onAdd = (ext: AvailableExtension) => {
    if (ext.tier === "ready") {
      void addReady(ext);
    } else {
      setAddTarget(ext);
    }
  };

  const doDrop = async (typed: string) => {
    if (!dropTarget) return;
    setDropBusy(true);
    try {
      await api.dropExtension(dropTarget, database, { confirm: typed });
      toast.success(`Removed ${dropTarget}.`);
      setDropTarget(null);
      exts.reload();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not remove the extension.");
    } finally {
      setDropBusy(false);
    }
  };

  const databases = dbs.data ?? [];

  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Extensions"
        description="Add capabilities like pgvector, PostGIS or pg_cron. The panel installs the OS package and — when an extension needs it — restarts Postgres safely, always showing you exactly what it runs."
      />

      <Callout tone="info" title="Extensions are per-database">
        <code>CREATE EXTENSION</code> installs into one database, not the whole
        cluster. Pick the target database below — what&apos;s installed and what you
        can add are both shown for that database.
      </Callout>

      {/* Target database selector */}
      <Card title="Target database">
        <Field className="max-w-sm">
          <FieldLabel htmlFor="ext-database">Database</FieldLabel>
          <Select
            value={database}
            onValueChange={setDatabase}
            disabled={databases.length === 0}
          >
            <SelectTrigger id="ext-database" className="w-full">
              <SelectValue placeholder="Select a database" />
            </SelectTrigger>
            <SelectContent>
              {databases.map((db) => (
                <SelectItem key={db.name} value={db.name}>
                  {db.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <FieldDescription>
            Extensions are listed and managed for this database.
          </FieldDescription>
        </Field>
      </Card>

      {/* Installed */}
      <Card title="Installed">
        {exts.loading ? (
          <Spinner />
        ) : exts.error ? (
          <ErrorNotice error={exts.error} />
        ) : !exts.data || exts.data.installed.length === 0 ? (
          <EmptyState
            title="No extensions installed"
            hint={`Nothing is installed in ${database} yet — add one from the list below.`}
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Version</TableHead>
                <TableHead>Latest on disk</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {exts.data.installed.map((ext) => (
                <TableRow key={ext.name}>
                  <TableCell className="font-medium">{ext.name}</TableCell>
                  <TableCell>{ext.installed_version || "—"}</TableCell>
                  <TableCell>
                    {ext.update_available ? (
                      <Badge tone="info">update to {ext.default_version}</Badge>
                    ) : (
                      ext.default_version || "—"
                    )}
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-2">
                      {ext.update_available ? (
                        <Button
                          variant="outline"
                          size="sm"
                          disabled={updateBusy.has(ext.name)}
                          onClick={() => doUpdate(ext.name)}
                        >
                          {updateBusy.has(ext.name) ? (
                            <>
                              <InlineSpinner data-icon="inline-start" />
                              Updating…
                            </>
                          ) : (
                            `Update to ${ext.default_version}`
                          )}
                        </Button>
                      ) : null}
                      <Button
                        variant="destructive"
                        size="sm"
                        disabled={dropBusy}
                        onClick={() => setDropTarget(ext.name)}
                      >
                        Remove
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      {/* Available */}
      <Card
        title="Available to add"
        actions={<AddByName database={database} onInstalled={() => exts.reload()} />}
      >
        {exts.loading ? (
          <Spinner />
        ) : exts.error ? (
          <ErrorNotice error={exts.error} />
        ) : !exts.data || exts.data.available.length === 0 ? (
          <EmptyState
            title="Nothing left to add"
            hint="Every catalog and on-disk extension is already installed here."
          />
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>What it does</TableHead>
                <TableHead>Requirements</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {exts.data.available.map((ext) => (
                <TableRow key={ext.name}>
                  <TableCell className="font-medium">{ext.name}</TableCell>
                  <TableCell className="text-muted-foreground">
                    {ext.description || "—"}
                  </TableCell>
                  <TableCell>
                    <TierBadge tier={ext.tier} />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={readyBusy.has(ext.name)}
                      onClick={() => onAdd(ext)}
                    >
                      {readyBusy.has(ext.name) ? (
                        <>
                          <InlineSpinner data-icon="inline-start" />
                          Adding…
                        </>
                      ) : (
                        "Add"
                      )}
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      {/* Add dialog for Tier 2 / Tier 3 */}
      {addTarget ? (
        <AddExtensionDialog
          ext={addTarget}
          database={database}
          onClose={() => setAddTarget(null)}
          onDone={() => {
            setAddTarget(null);
            exts.reload();
          }}
          onRecheck={() => {
            toast.info("Re-checking extensions…");
            setAddTarget(null);
            exts.reload();
          }}
        />
      ) : null}

      {/* Drop confirmation */}
      <TypedConfirmDialog
        open={dropTarget !== null}
        title="Remove extension"
        objectName={dropTarget ?? ""}
        objectKind="extension"
        confirmLabel="Remove extension"
        consequence={
          <>
            This runs <code>DROP EXTENSION</code> in <strong>{database}</strong>. Any
            tables, types or functions that depend on it will block the drop — there is
            no CASCADE here, so Postgres will tell you what to resolve first.
          </>
        }
        busy={dropBusy}
        onConfirm={doDrop}
        onCancel={() => setDropTarget(null)}
      />
    </div>
  );
}

// --- Badges ----------------------------------------------------------------

function TierBadge({ tier }: { tier: ExtensionTier }) {
  switch (tier) {
    case "ready":
      return <Badge tone="ok">Ready</Badge>;
    case "needs_package":
      return <Badge tone="warn">Needs package</Badge>;
    case "needs_restart":
      return <Badge tone="danger">Needs restart</Badge>;
    default:
      return <Badge>{tier}</Badge>;
  }
}

// --- Add by name -----------------------------------------------------------

function AddByName({
  database,
  onInstalled,
}: {
  database: string;
  onInstalled: () => void;
}) {
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);

  // Free-form add is SQL-only: the server runs a plain CREATE EXTENSION and never
  // apt-installs a package or restarts Postgres off a typed name. A name whose
  // files aren't on disk comes back as a friendly not-found with a hint pointing
  // at the catalog below — no apt, no restart, no tier-from-error-code guessing.
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const trimmed = name.trim();
    if (!trimmed) return;
    setBusy(true);
    try {
      const res = await api.installExtension({
        database,
        name: trimmed,
        confirm: "",
        freeform: true,
      });
      toast.success(res.message || `Installed ${trimmed}.`);
      setName("");
      onInstalled();
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : `Could not install ${trimmed}.`);
    } finally {
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex items-center gap-2">
      <Input
        type="text"
        value={name}
        placeholder="add by name…"
        aria-label="Add extension by name"
        autoComplete="off"
        spellCheck={false}
        className="h-8 w-44"
        onChange={(e) => setName(e.target.value)}
      />
      <Button type="submit" variant="outline" size="sm" disabled={busy || !name.trim()}>
        {busy ? (
          <>
            <InlineSpinner data-icon="inline-start" />
            Adding…
          </>
        ) : (
          "Add"
        )}
      </Button>
    </form>
  );
}

// --- Add dialog ------------------------------------------------------------

type AddStage = "preview" | "manual" | "done";

/**
 * The Add dialog for Tier 2 (needs package) and Tier 3 (needs restart). It shows
 * the steps the panel will run, then either runs them ("Install for me") or steps
 * aside ("I'll run these myself") with a Re-check. Tier 3 also requires typing the
 * extension name to authorize the Postgres restart.
 */
function AddExtensionDialog({
  ext,
  database,
  onClose,
  onDone,
  onRecheck,
}: {
  ext: AvailableExtension;
  database: string;
  onClose: () => void;
  onDone: () => void;
  onRecheck: () => void;
}) {
  const [stage, setStage] = useState<AddStage>("preview");
  const [busy, setBusy] = useState(false);
  const [typed, setTyped] = useState("");
  const [error, setError] = useState<ApiError | null>(null);
  const [ran, setRan] = useState<string[]>([]);

  const needsRestart = ext.tier === "needs_restart";
  const confirmOk = !needsRestart || typed === ext.name;

  // What the panel will run, derived from the tier. The exact Debian package name
  // is resolved from the running PostgreSQL version at install time, so the
  // preview names it generically — the precise commands are shown afterward.
  const predicted = useMemo(() => predictedSteps(ext), [ext]);

  const install = async () => {
    setBusy(true);
    setError(null);
    try {
      const res: Result = await api.installExtension({
        database,
        name: ext.name,
        confirm: needsRestart ? typed : "",
      });
      setRan(res.statements ?? []);
      setStage("done");
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err
          : new ApiError(0, { code: "internal", message: String(err) }),
      );
    } finally {
      setBusy(false);
    }
  };

  const title =
    stage === "done"
      ? `Installed ${ext.name}`
      : `Add ${ext.name}`;

  return (
    <Modal
      open
      title={title}
      tone={needsRestart ? "danger" : "default"}
      // While the install runs, the Modal itself blocks Escape, the backdrop and
      // the corner X (dismissible=false) rather than rendering a dead X that
      // silently does nothing — matching the one-time-secret dialogs.
      dismissible={!busy}
      onClose={onClose}
      footer={
        stage === "done" ? (
          <Button type="button" onClick={onDone}>
            Done
          </Button>
        ) : stage === "manual" ? (
          <>
            <Button type="button" variant="outline" onClick={() => setStage("preview")}>
              Back
            </Button>
            <Button type="button" onClick={onRecheck}>
              Re-check
            </Button>
          </>
        ) : (
          <>
            <Button type="button" variant="outline" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button
              type="button"
              variant={needsRestart ? "destructive" : "default"}
              onClick={install}
              disabled={busy || !confirmOk}
            >
              {busy ? (
                <>
                  <InlineSpinner data-icon="inline-start" />
                  Working…
                </>
              ) : (
                "Install for me"
              )}
            </Button>
          </>
        )
      }
    >
      {stage === "done" ? (
        <>
          <p className="text-muted-foreground">
            <strong>{ext.name}</strong> is installed in <strong>{database}</strong>.
            Here&apos;s exactly what ran:
          </p>
          <CommandList commands={ran} />
        </>
      ) : stage === "manual" ? (
        <>
          <p className="text-muted-foreground">
            Run these yourself on the host, then come back and Re-check.
          </p>
          <CommandList commands={predicted} />
          {needsRestart ? (
            <Callout tone="danger" title="This restarts Postgres">
              <code>systemctl restart postgresql</code> is server-wide: every
              database — and anything connecting to it — is unavailable for a few
              seconds while Postgres restarts. Do it during low traffic.
            </Callout>
          ) : null}
        </>
      ) : (
        <>
          {error ? <ErrorNotice error={error} /> : null}
          {ext.description ? (
            <p className="text-muted-foreground">{ext.description}</p>
          ) : null}
          <p className="mt-3 text-muted-foreground">
            {ext.tier === "needs_package"
              ? "This extension isn't on disk yet, so the panel will install its OS package, then create it:"
              : "This extension needs to be loaded at startup, so the panel will install its package if needed, add it to shared_preload_libraries, restart Postgres, then create it:"}
          </p>
          <CommandList commands={predicted} />
          {needsRestart ? (
            <p className="mt-2 text-[13px] text-muted-foreground">
              The current <code>shared_preload_libraries</code> value is read and{" "}
              <code>{ext.name}</code> appended to it (existing entries are preserved) —
              the precise commands that actually ran are shown afterward.
            </p>
          ) : (
            <p className="mt-2 text-[13px] text-muted-foreground">
              The exact package name is resolved from the running PostgreSQL version —
              the precise commands that actually ran are shown afterward.
            </p>
          )}
          <p className="mt-2 text-[13px] text-muted-foreground">
            Prefer to run these yourself?{" "}
            <button
              type="button"
              className="underline underline-offset-2 hover:text-foreground"
              onClick={() => setStage("manual")}
              disabled={busy}
            >
              See the commands and do it manually
            </button>
          </p>
          {needsRestart ? (
            <>
              <Callout tone="danger" title="This restarts Postgres (server-wide)">
                The preload change and restart affect the <strong>whole server</strong>,
                not just <strong>{database}</strong>: your database and anything
                connecting to it are unavailable for a few seconds while Postgres
                restarts. If it doesn&apos;t come back up, the panel automatically rolls
                the change back — it&apos;s never left down.
              </Callout>
              <Field className="mt-4">
                <FieldLabel htmlFor="ext-restart-confirm">
                  Type <code>{ext.name}</code> to confirm the restart
                </FieldLabel>
                <Input
                  id="ext-restart-confirm"
                  type="text"
                  autoComplete="off"
                  autoCorrect="off"
                  spellCheck={false}
                  value={typed}
                  placeholder={ext.name}
                  onChange={(e) => setTyped(e.target.value)}
                  aria-invalid={typed.length > 0 && !confirmOk}
                />
              </Field>
            </>
          ) : null}
        </>
      )}
    </Modal>
  );
}

function CommandList({ commands }: { commands: string[] }) {
  const [copied, setCopied] = useState(false);

  if (commands.length === 0) {
    return <p className="mt-3 text-muted-foreground">No commands were recorded.</p>;
  }

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(commands.join("\n"));
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Couldn't copy to the clipboard.");
    }
  };

  return (
    <div className="relative mt-3">
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="absolute right-2 top-2 h-7"
        onClick={copy}
      >
        {copied ? "Copied" : "Copy"}
      </Button>
      <pre className="overflow-x-auto rounded-md border bg-muted/40 p-3 pr-20 text-[13px] leading-relaxed text-foreground">
        {commands.join("\n")}
      </pre>
    </div>
  );
}

// Derives the steps the panel will run for a Tier 2/3 install. Used only for the
// preview and the "I'll run these myself" view — the authoritative list is the
// `statements` the server returns after a real install. The package name is the
// real one the server resolved for this PostgreSQL version (ext.package). The
// shared_preload_libraries line is shown as a *comment*, never a runnable
// ALTER SYSTEM with a placeholder: pasting `'…, pg_cron'` literally would clobber
// the existing preload list, so we describe the read-modify-write instead and let
// the post-install "exactly what ran" block carry the real statement.
function predictedSteps(ext: AvailableExtension): string[] {
  const create = `CREATE EXTENSION IF NOT EXISTS "${ext.name}";`;
  const pkg = ext.package || "<the OS package for this extension>";
  if (ext.tier === "needs_restart") {
    return [
      "apt-get update",
      `apt-get install -y ${pkg}`,
      // The preload edit is deliberately NOT a runnable statement: the frontend
      // can't know the cluster's real shared_preload_libraries, and pasting a
      // literal would overwrite it. Emit it as commented guidance the operator
      // adapts — never something destructive that copy-pastes verbatim.
      `-- ${ext.name} must be loaded at startup, so it has to go in shared_preload_libraries.`,
      "-- DON'T paste a literal value: it would overwrite your real preload list and can",
      "-- stop Postgres from booting. Instead, read your current value and append to it:",
      "--   1. SHOW shared_preload_libraries;",
      `--   2. ALTER SYSTEM SET shared_preload_libraries = '<your current value>, ${ext.name}';`,
      "systemctl restart postgresql",
      create,
    ];
  }
  if (ext.tier === "needs_package") {
    return [
      "apt-get update",
      `apt-get install -y ${pkg}`,
      create,
    ];
  }
  return [create];
}
