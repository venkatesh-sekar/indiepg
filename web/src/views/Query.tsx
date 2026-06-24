// Query view: read-only SQL editor with an explicit, unmissable read-only
// affordance + a results table. The server enforces read-only at the DB level
// (indiepg_readonly role) and auto-LIMITs — this UI makes that visible.

import { useState, type KeyboardEvent } from "react";
import { ApiError, api } from "@/api/client";
import { millis, count } from "@/lib/format";
import { cn } from "@/lib/utils";
import {
  Badge,
  Callout,
  ErrorNotice,
  PageHeader,
  ReadOnlyBadge,
} from "@/components/ui";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { Spinner as InlineSpinner } from "@/components/ui/spinner";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { QueryResult } from "@/api/types";

const SAMPLES: Array<{ label: string; sql: string }> = [
  { label: "Tables by size", sql: "SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size\nFROM pg_catalog.pg_statio_user_tables\nORDER BY pg_total_relation_size(relid) DESC;" },
  { label: "Active queries", sql: "SELECT pid, state, query_start, query\nFROM pg_stat_activity\nWHERE state <> 'idle';" },
  { label: "Database sizes", sql: "SELECT datname, pg_size_pretty(pg_database_size(datname)) AS size\nFROM pg_database\nORDER BY pg_database_size(datname) DESC;" },
];

export function Query() {
  const [sql, setSql] = useState("SELECT now();");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<QueryResult | null>(null);
  const [error, setError] = useState<ApiError | null>(null);

  const run = async () => {
    const trimmed = sql.trim();
    if (!trimmed || busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await api.runQuery(trimmed);
      setResult(res);
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError(0, { code: "internal", message: String(err) }));
      setResult(null);
    } finally {
      setBusy(false);
    }
  };

  const onEditorKey = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    // Cmd/Ctrl+Enter runs the query.
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      e.preventDefault();
      void run();
    }
  };

  return (
    <div className="view">
      <PageHeader
        title="Query"
        description="Run SELECT queries against your database. This is a safe, look-only window."
        actions={<ReadOnlyBadge />}
      />

      <Callout tone="info" title="This editor is read-only">
        It connects through a role that <strong>cannot change or delete any data</strong> — even
        if you try. Long queries are stopped automatically and results are capped so nothing can
        slow down your database. To change data, use the guided actions under Roles &amp; Databases.
      </Callout>

      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-muted-foreground">Try:</span>
        {SAMPLES.map((sample) => (
          <Button
            key={sample.label}
            type="button"
            variant="outline"
            size="sm"
            title="Replace the editor with this query"
            onClick={() => setSql(sample.sql)}
          >
            {sample.label}
          </Button>
        ))}
      </div>

      <div className="flex flex-col gap-3">
        <Textarea
          value={sql}
          spellCheck={false}
          onChange={(e) => setSql(e.target.value)}
          onKeyDown={onEditorKey}
          placeholder="SELECT * FROM your_table;"
          aria-label="SQL query editor"
          className="min-h-40 font-mono text-sm leading-relaxed"
        />
        <div className="flex items-center justify-between gap-3">
          <span className="text-xs text-muted-foreground">⌘/Ctrl + Enter to run</span>
          <Button onClick={run} disabled={busy || !sql.trim()}>
            {busy ? (
              <>
                <InlineSpinner data-icon="inline-start" />
                Running…
              </>
            ) : (
              "Run query"
            )}
          </Button>
        </div>
      </div>

      {error ? <ErrorNotice error={error} /> : null}
      {result && !busy ? <ResultsTable result={result} /> : null}
    </div>
  );
}

function ResultsTable({ result }: { result: QueryResult }) {
  if (result.columns.length === 0) {
    return (
      <Callout tone="ok">
        Query ran successfully in {millis(result.duration_ms)}. It returned no columns.
      </Callout>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      <div className="flex items-center gap-2.5">
        <Badge tone="ok">{count(result.row_count)} rows</Badge>
        <span className="text-sm text-muted-foreground">{millis(result.duration_ms)}</span>
        {result.limited ? <Badge tone="warn">Results limited for safety</Badge> : null}
      </div>

      {result.limited ? (
        <p className="text-xs text-muted-foreground">
          Only the first {count(result.row_count)} rows are shown. Add your own{" "}
          <code>LIMIT</code> or a <code>WHERE</code> filter to narrow results.
        </p>
      ) : null}

      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-0 text-right">#</TableHead>
            {result.columns.map((col) => (
              <TableHead key={col.name} className="align-top">
                <span className="block">{col.name}</span>
                <span className="block text-[10px] font-normal text-muted-foreground">
                  {col.data_type}
                </span>
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {result.rows.map((row, i) => (
            <TableRow key={i}>
              <TableCell className="text-right text-muted-foreground tabular-nums">
                {i + 1}
              </TableCell>
              {row.map((cell, j) => (
                <TableCell
                  key={j}
                  className={cn(cell === null && "text-muted-foreground italic")}
                >
                  {cell === null ? "NULL" : String(cell)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>

      {result.rows.length === 0 ? (
        <p className="text-xs text-muted-foreground">No rows matched.</p>
      ) : null}
    </div>
  );
}
