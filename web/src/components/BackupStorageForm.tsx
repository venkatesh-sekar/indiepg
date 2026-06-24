// BackupStorageForm: configure the S3 backup destination (endpoint, bucket,
// credentials), retention, and repository encryption. Saving re-renders the
// pgBackRest config and runs stanza-create, so the save doubles as a connection
// test — its result is surfaced inline. Lives here (not inside a view) because
// it is co-located with the backup operations on the Backups page, where you
// configure-then-run without leaving the workflow.

import { useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { toast } from "sonner";
import { Badge, Callout, Card, ErrorNotice } from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Checkbox } from "@/components/ui/checkbox";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import {
  Field,
  FieldContent,
  FieldDescription,
  FieldLabel,
} from "@/components/ui/field";
import type { ConfigResponse, UpdateConfigRequest } from "@/api/types";

export function BackupStorageForm({
  initial,
  onSaved,
}: {
  initial: ConfigResponse;
  onSaved: () => void;
}) {
  const b = initial.config.backup;

  const [endpoint, setEndpoint] = useState(b.endpoint);
  const [region, setRegion] = useState(b.region);
  const [bucket, setBucket] = useState(b.bucket);
  const [prefix, setPrefix] = useState(b.prefix);
  const [accessKey, setAccessKey] = useState(b.access_key);
  const [secretKey, setSecretKey] = useState("");
  const [useSSL, setUseSSL] = useState(b.use_ssl);
  const [cipherPass, setCipherPass] = useState("");
  const [retentionDays, setRetentionDays] = useState(initial.config.retention_days);

  const [secretIsSet, setSecretIsSet] = useState(initial.backup_secret_is_set);
  const [cipherIsSet, setCipherIsSet] = useState(initial.backup_cipher_is_set);

  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | null>(null);
  // Result of the last save's pgBackRest provisioning attempt (the connection test).
  const [warning, setWarning] = useState<string | null>(null);
  const [warningHint, setWarningHint] = useState<string | null>(null);
  const [warningDetail, setWarningDetail] = useState<string | null>(null);
  const [configured, setConfigured] = useState<boolean | null>(null);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setWarning(null);
    setWarningHint(null);
    setWarningDetail(null);
    setConfigured(null);

    const backup: UpdateConfigRequest["backup"] = {
      endpoint: endpoint.trim(),
      region: region.trim(),
      bucket: bucket.trim(),
      prefix: prefix.trim(),
      access_key: accessKey.trim(),
      use_ssl: useSSL,
    };
    // Write-only secrets: only send when the operator typed a new value, so a
    // blank field preserves the stored credential rather than clearing it.
    if (secretKey) backup.secret_key = secretKey;
    if (cipherPass) backup.cipher_pass = cipherPass;

    const req: UpdateConfigRequest = { retention_days: retentionDays, backup };

    try {
      const res = await api.updateConfig(req);
      setSecretIsSet(res.backup_secret_is_set);
      setCipherIsSet(res.backup_cipher_is_set);
      setSecretKey("");
      setCipherPass("");
      setConfigured(res.backup_configured ?? null);
      if (res.backup_warning) {
        setWarning(res.backup_warning);
        setWarningHint(res.backup_hint ?? null);
        setWarningDetail(res.backup_detail ?? null);
        toast.info("Settings saved, but the backup target needs attention.");
      } else {
        toast.success("Settings saved.");
      }
      onSaved();
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

  const hasTarget = endpoint.trim() !== "" || bucket.trim() !== "";

  return (
    <form onSubmit={submit}>
      <Card
        title="Backup storage (S3-compatible)"
        actions={
          secretIsSet ? (
            <Badge tone="ok">Credentials saved</Badge>
          ) : (
            <Badge tone="warn">Not configured</Badge>
          )
        }
      >
        {!hasTarget ? (
          <Callout tone="warn" title="Backups are currently stored on this server">
            With no bucket set, the panel writes backups to{" "}
            <code>/var/lib/pgbackrest</code> on this same machine — a fine starting
            point, but it won&apos;t survive disk or server loss. Add an S3-compatible
            bucket below for real off-server backups. Switching is live and starts a
            fresh backup repo in the bucket (existing local backups stay on disk).
          </Callout>
        ) : null}

        <Callout tone="info" title="Works with any S3-compatible bucket">
          Backblaze B2, Cloudflare R2, AWS S3, MinIO, and friends. Create a bucket
          and an access key with read/write access, then paste the details here.
          The panel stores backups under <code>panel/&lt;your-instance-id&gt;</code>{" "}
          inside the bucket.
        </Callout>

        {error ? <ErrorNotice error={error} /> : null}

        {warning ? (
          <Callout tone="danger" title="Saved, but the backup target isn’t ready">
            <p>
              Your existing backups are untouched and still safe — but new backups
              to this bucket will fail until this is fixed. Correct the problem
              below and save again before relying on this destination.
            </p>
            <p className="mt-2">{warning}</p>
            {warningDetail ? (
              <pre className="mt-2 max-h-[180px] overflow-auto whitespace-pre-wrap break-words rounded-md border bg-muted px-2.5 py-2 font-mono text-xs leading-normal">
                {warningDetail}
              </pre>
            ) : null}
            {warningHint ? (
              <div className="mt-1.5 text-[13px] opacity-80">{warningHint}</div>
            ) : null}
          </Callout>
        ) : configured ? (
          <Callout tone="ok" title="Backup target is ready">
            The bucket is reachable and the pgBackRest stanza is initialized. Close
            this panel and run a backup.
          </Callout>
        ) : null}

        <div className="grid gap-5 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="backup-endpoint">Endpoint</FieldLabel>
            <Input
              id="backup-endpoint"
              type="text"
              value={endpoint}
              autoComplete="off"
              spellCheck={false}
              placeholder="s3.us-west-002.backblazeb2.com"
              aria-describedby="backup-endpoint-desc"
              onChange={(e) => setEndpoint(e.target.value)}
            />
            <FieldDescription id="backup-endpoint-desc">
              The S3 host, without <code>https://</code>.
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-region">Region</FieldLabel>
            <Input
              id="backup-region"
              type="text"
              value={region}
              autoComplete="off"
              spellCheck={false}
              placeholder="us-west-002"
              onChange={(e) => setRegion(e.target.value)}
            />
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-bucket">Bucket</FieldLabel>
            <Input
              id="backup-bucket"
              type="text"
              value={bucket}
              autoComplete="off"
              spellCheck={false}
              placeholder="my-database-backups"
              onChange={(e) => setBucket(e.target.value)}
            />
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-prefix">
              Path prefix{" "}
              <span className="text-muted-foreground">(optional)</span>
            </FieldLabel>
            <Input
              id="backup-prefix"
              type="text"
              value={prefix}
              autoComplete="off"
              spellCheck={false}
              placeholder="(bucket root)"
              aria-describedby="backup-prefix-desc"
              onChange={(e) => setPrefix(e.target.value)}
            />
            <FieldDescription id="backup-prefix-desc">
              A sub-folder inside the bucket. Leave blank to use the root.
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-access-key">Access key ID</FieldLabel>
            <Input
              id="backup-access-key"
              type="text"
              value={accessKey}
              autoComplete="off"
              spellCheck={false}
              placeholder="0026abc…"
              onChange={(e) => setAccessKey(e.target.value)}
            />
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-secret-key">
              Secret access key{" "}
              {secretIsSet ? (
                <span className="text-muted-foreground">— saved</span>
              ) : null}
            </FieldLabel>
            <Input
              id="backup-secret-key"
              type="password"
              value={secretKey}
              autoComplete="new-password"
              spellCheck={false}
              placeholder={secretIsSet ? "Leave blank to keep current" : "Enter secret key"}
              aria-describedby="backup-secret-key-desc"
              onChange={(e) => setSecretKey(e.target.value)}
            />
            <FieldDescription id="backup-secret-key-desc">
              Stored in the panel’s local database and written to a private
              (0600, postgres-owned) pgBackRest config file; never shown again after saving.
            </FieldDescription>
          </Field>
        </div>

        <Field orientation="horizontal">
          <Checkbox
            id="backup-use-ssl"
            checked={useSSL}
            aria-describedby="backup-use-ssl-desc"
            onCheckedChange={(c) => setUseSSL(c === true)}
          />
          <FieldContent>
            <FieldLabel htmlFor="backup-use-ssl" className="font-normal">
              Use TLS (HTTPS) to reach the bucket
            </FieldLabel>
            <FieldDescription id="backup-use-ssl-desc">
              Recommended; turn off only for a local MinIO over plain HTTP.
            </FieldDescription>
          </FieldContent>
        </Field>
      </Card>

      <Card title="Retention &amp; encryption">
        <div className="grid gap-5 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor="backup-retention">Keep backups for (days)</FieldLabel>
            <Input
              id="backup-retention"
              type="number"
              min={0}
              value={retentionDays}
              aria-describedby="backup-retention-desc"
              onChange={(e) => setRetentionDays(Number(e.target.value))}
            />
            <FieldDescription id="backup-retention-desc">
              Older full backups are expired automatically. 0 keeps everything.
            </FieldDescription>
          </Field>

          <Field>
            <FieldLabel htmlFor="backup-cipher">
              Repository encryption passphrase{" "}
              {cipherIsSet ? (
                <span className="text-muted-foreground">— saved</span>
              ) : (
                <span className="text-muted-foreground">(optional)</span>
              )}
            </FieldLabel>
            <Input
              id="backup-cipher"
              type="password"
              value={cipherPass}
              autoComplete="new-password"
              spellCheck={false}
              placeholder={cipherIsSet ? "Leave blank to keep current" : "Encrypt backups at rest"}
              aria-describedby="backup-cipher-desc"
              onChange={(e) => setCipherPass(e.target.value)}
            />
            <FieldDescription id="backup-cipher-desc">
              Enables AES-256 encryption of the backup repository.{" "}
              <strong>Keep it safe</strong> — without it, encrypted backups can never be restored.
            </FieldDescription>
          </Field>
        </div>
      </Card>

      <div className="flex">
        <Button type="submit" disabled={busy}>
          {busy ? (
            <>
              <InlineSpinner data-icon="inline-start" />
              Saving…
            </>
          ) : hasTarget ? (
            "Save & connect"
          ) : (
            "Save"
          )}
        </Button>
      </div>
    </form>
  );
}
