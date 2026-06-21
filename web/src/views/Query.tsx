// Query view: read-only SQL editor with an explicit, unmissable read-only
// affordance + a results table. The server enforces read-only at the DB level
// (pgpanel_readonly role) and auto-LIMITs — this UI makes that visible.

import { useState, type KeyboardEvent } from "react";
import { ApiError, api } from "@/api/client";
import { millis, count } from "@/lib/format";
import {
  Badge,
  Callout,
  ErrorNotice,
  PageHeader,
  ReadOnlyBadge,
  Spinner,
} from "@/components/ui";
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

      <div className="query-toolbar">
        <div className="query-samples">
          <span className="muted">Try:</span>
          {SAMPLES.map((sample) => (
            <button
              key={sample.label}
              type="button"
              className="chip"
              onClick={() => setSql(sample.sql)}
            >
              {sample.label}
            </button>
          ))}
        </div>
      </div>

      <div className="editor-wrap">
        <textarea
          className="sql-editor"
          value={sql}
          spellCheck={false}
          onChange={(e) => setSql(e.target.value)}
          onKeyDown={onEditorKey}
          placeholder="SELECT * FROM your_table;"
          aria-label="Read-only SQL editor"
        />
        <div className="editor-actions">
          <span className="muted kbd-hint">⌘/Ctrl + Enter to run</span>
          <button
            type="button"
            className="btn btn-primary"
            onClick={run}
            disabled={busy || !sql.trim()}
          >
            {busy ? "Running…" : "Run query"}
          </button>
        </div>
      </div>

      {busy ? <Spinner label="Running query…" /> : null}
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
    <div className="results">
      <div className="results-meta">
        <Badge tone="ok">{count(result.row_count)} rows</Badge>
        <span className="muted">{millis(result.duration_ms)}</span>
        {result.limited ? (
          <Badge tone="warn" >Results limited for safety</Badge>
        ) : null}
      </div>

      {result.limited ? (
        <p className="muted results-note">
          Only the first {count(result.row_count)} rows are shown. Add your own{" "}
          <code>LIMIT</code> or a <code>WHERE</code> filter to narrow results.
        </p>
      ) : null}

      <div className="table-scroll">
        <table className="data-table">
          <thead>
            <tr>
              <th className="row-num">#</th>
              {result.columns.map((col) => (
                <th key={col.name}>
                  <span className="col-name">{col.name}</span>
                  <span className="col-type muted">{col.data_type}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {result.rows.map((row, i) => (
              <tr key={i}>
                <td className="row-num">{i + 1}</td>
                {row.map((cell, j) => (
                  <td key={j} className={cell === null ? "cell-null" : ""}>
                    {cell === null ? "NULL" : String(cell)}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {result.rows.length === 0 ? (
        <p className="muted results-note">No rows matched.</p>
      ) : null}
    </div>
  );
}
