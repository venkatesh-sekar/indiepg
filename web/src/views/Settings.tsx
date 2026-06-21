// Settings: configure the S3 backup destination (endpoint, bucket, credentials)
// and retention. Saving re-renders the pgBackRest config and runs stanza-create,
// so the save doubles as a connection test — its result is surfaced inline.

import { useState, type FormEvent } from "react";
import { ApiError, api } from "@/api/client";
import { useAsync } from "@/lib/hooks";
import { useToast } from "@/components/Toast";
import {
  Badge,
  Callout,
  Card,
  ErrorNotice,
  PageHeader,
  Spinner,
} from "@/components/ui";
import type { ConfigResponse, UpdateConfigRequest } from "@/api/types";

export function Settings() {
  const config = useAsync<ConfigResponse>(() => api.getConfig(), []);

  return (
    <div className="view">
      <PageHeader
        title="Settings"
        description="Backups are stored on this server until you connect an S3-compatible bucket here — recommended for real, off-server protection."
      />
      {config.loading ? (
        <Spinner label="Loading settings…" />
      ) : config.error ? (
        <ErrorNotice error={config.error} />
      ) : config.data ? (
        <BackupSettingsForm initial={config.data} onSaved={config.reload} />
      ) : null}
    </div>
  );
}

function BackupSettingsForm({
  initial,
  onSaved,
}: {
  initial: ConfigResponse;
  onSaved: () => void;
}) {
  const toast = useToast();
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
            {warning}
            {warningDetail ? (
              <pre className="callout-detail">{warningDetail}</pre>
            ) : null}
            {warningHint ? <div className="callout-hint">{warningHint}</div> : null}
          </Callout>
        ) : configured ? (
          <Callout tone="ok" title="Backup target is ready">
            The bucket is reachable and the pgBackRest stanza is initialized. You can
            run a backup from the Backups page.
          </Callout>
        ) : null}

        <div className="field-grid">
          <label className="field">
            <span className="field-label">Endpoint</span>
            <input
              type="text"
              value={endpoint}
              autoComplete="off"
              spellCheck={false}
              placeholder="s3.us-west-002.backblazeb2.com"
              onChange={(e) => setEndpoint(e.target.value)}
            />
            <span className="field-help muted">
              The S3 host, without <code>https://</code>.
            </span>
          </label>

          <label className="field">
            <span className="field-label">Region</span>
            <input
              type="text"
              value={region}
              autoComplete="off"
              spellCheck={false}
              placeholder="us-west-002"
              onChange={(e) => setRegion(e.target.value)}
            />
          </label>

          <label className="field">
            <span className="field-label">Bucket</span>
            <input
              type="text"
              value={bucket}
              autoComplete="off"
              spellCheck={false}
              placeholder="my-database-backups"
              onChange={(e) => setBucket(e.target.value)}
            />
          </label>

          <label className="field">
            <span className="field-label">
              Path prefix <span className="muted">(optional)</span>
            </span>
            <input
              type="text"
              value={prefix}
              autoComplete="off"
              spellCheck={false}
              placeholder="(bucket root)"
              onChange={(e) => setPrefix(e.target.value)}
            />
            <span className="field-help muted">
              A sub-folder inside the bucket. Leave blank to use the root.
            </span>
          </label>

          <label className="field">
            <span className="field-label">Access key ID</span>
            <input
              type="text"
              value={accessKey}
              autoComplete="off"
              spellCheck={false}
              placeholder="0026abc…"
              onChange={(e) => setAccessKey(e.target.value)}
            />
          </label>

          <label className="field">
            <span className="field-label">
              Secret access key{" "}
              {secretIsSet ? <span className="muted">— saved</span> : null}
            </span>
            <input
              type="password"
              value={secretKey}
              autoComplete="new-password"
              spellCheck={false}
              placeholder={secretIsSet ? "Leave blank to keep current" : "Enter secret key"}
              onChange={(e) => setSecretKey(e.target.value)}
            />
            <span className="field-help muted">
              Stored in the panel’s local database and written to a private
              (0600, postgres-owned) pgBackRest config file; never shown again after saving.
            </span>
          </label>
        </div>

        <label className="checkbox">
          <input type="checkbox" checked={useSSL} onChange={(e) => setUseSSL(e.target.checked)} />
          <span>
            Use TLS (HTTPS) to reach the bucket
            <span className="muted"> — recommended; turn off only for a local MinIO over plain HTTP.</span>
          </span>
        </label>
      </Card>

      <Card title="Retention &amp; encryption">
        <div className="field-grid">
          <label className="field">
            <span className="field-label">Keep backups for (days)</span>
            <input
              type="number"
              min={0}
              value={retentionDays}
              onChange={(e) => setRetentionDays(Number(e.target.value))}
            />
            <span className="field-help muted">
              Older full backups are expired automatically. 0 keeps everything.
            </span>
          </label>

          <label className="field">
            <span className="field-label">
              Repository encryption passphrase{" "}
              {cipherIsSet ? <span className="muted">— saved</span> : <span className="muted">(optional)</span>}
            </span>
            <input
              type="password"
              value={cipherPass}
              autoComplete="new-password"
              spellCheck={false}
              placeholder={cipherIsSet ? "Leave blank to keep current" : "Encrypt backups at rest"}
              onChange={(e) => setCipherPass(e.target.value)}
            />
            <span className="field-help muted">
              Enables AES-256 encryption of the backup repository.{" "}
              <strong>Keep it safe</strong> — without it, encrypted backups can never be restored.
            </span>
          </label>
        </div>
      </Card>

      <div className="btn-row">
        <button type="submit" className="btn btn-primary" disabled={busy}>
          {busy ? "Saving…" : hasTarget ? "Save & connect" : "Save"}
        </button>
      </div>
    </form>
  );
}
