import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { Query } from "./Query";
import { api } from "@/api/client";
import type { QueryResult } from "@/api/types";

function result(over: Partial<QueryResult> = {}): QueryResult {
  return {
    columns: [
      { name: "id", data_type: "int4" },
      { name: "name", data_type: "text" },
    ],
    rows: [
      [1, "alice"],
      [2, null],
    ],
    row_count: 2,
    limited: false,
    executed_sql: "SELECT id, name FROM users;",
    duration_ms: 12,
    classification: "read",
    ...over,
  };
}

beforeEach(() => {
  vi.restoreAllMocks();
});

describe("Query", () => {
  it("shows the read-only affordance and the editor", () => {
    render(<Query />);
    expect(screen.getByText("Read-only")).toBeInTheDocument();
    expect(screen.getByLabelText("SQL query editor")).toBeInTheDocument();
    // The read-only guarantee is stated, not hidden.
    expect(screen.getByText(/cannot change or delete any data/i)).toBeInTheDocument();
  });

  it("runs the query and renders the results table with a NULL cell", async () => {
    vi.spyOn(api, "runQuery").mockResolvedValue(result());
    render(<Query />);

    fireEvent.click(screen.getByRole("button", { name: /run query/i }));

    expect(await screen.findByText("2 rows")).toBeInTheDocument();
    expect(api.runQuery).toHaveBeenCalledWith("SELECT now();");
    // Both column headers, including the type sublabel.
    expect(screen.getByText("name")).toBeInTheDocument();
    expect(screen.getByText("text")).toBeInTheDocument();
    expect(screen.getByText("alice")).toBeInTheDocument();
    expect(screen.getByText("NULL")).toBeInTheDocument();
  });

  it("loads a sample into the editor when its button is clicked", () => {
    render(<Query />);
    fireEvent.click(screen.getByRole("button", { name: "Database sizes" }));
    const editor = screen.getByLabelText("SQL query editor") as HTMLTextAreaElement;
    expect(editor.value).toContain("pg_database_size");
  });

  it("surfaces the auto-LIMIT safety messaging when results are capped", async () => {
    vi.spyOn(api, "runQuery").mockResolvedValue(result({ limited: true, row_count: 1, rows: [[1, "alice"]] }));
    render(<Query />);

    fireEvent.click(screen.getByRole("button", { name: /run query/i }));

    expect(await screen.findByText("Results limited for safety")).toBeInTheDocument();
    expect(screen.getByText(/Only the first/i)).toBeInTheDocument();
  });

  it("disables Run query when the editor is empty", () => {
    render(<Query />);
    const editor = screen.getByLabelText("SQL query editor");
    fireEvent.change(editor, { target: { value: "   " } });
    expect(screen.getByRole("button", { name: /run query/i })).toBeDisabled();
  });
});
