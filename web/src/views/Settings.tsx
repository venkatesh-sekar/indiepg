// Settings: database-level configuration that isn't tied to a single workflow —
// performance tuning (host-sized) and the optional connection pooler. Backup
// storage (S3 destination, retention, encryption) lives on the Backups page now,
// co-located with the backups it configures, so you set it up and run it in one
// place instead of bouncing between routes.

import { Link } from "react-router-dom";
import { Callout, PageHeader } from "@/components/ui";
import { DatabaseTuning } from "./DatabaseTuning";
import { Pooler } from "./Pooler";

export function Settings() {
  return (
    <div className="mx-auto flex max-w-[1100px] flex-col gap-5">
      <PageHeader
        title="Settings"
        description="Tune Postgres for this server and turn on the optional connection pooler."
      />
      <Callout tone="info" title="Looking for backup storage?">
        Configuring your S3 bucket, retention, and encryption now lives on the{" "}
        <Link to="/backups">Backups page</Link>, right next to the backups it
        controls — so you can set it up and run a backup in one place.
      </Callout>
      <DatabaseTuning />
      <Pooler />
    </div>
  );
}
