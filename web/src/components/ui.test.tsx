import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ResultBadge, ErrorNotice } from "./ui";
import { ApiError } from "@/api/client";

describe("ResultBadge", () => {
  it("renders success-like results with the ok tone", () => {
    render(<ResultBadge result="completed" />);
    const badge = screen.getByText("completed");
    expect(badge).toHaveClass("badge-ok");
  });

  it("renders failures with the danger tone", () => {
    render(<ResultBadge result="failed" />);
    expect(screen.getByText("failed")).toHaveClass("badge-danger");
  });

  it("treats in-progress results as info", () => {
    render(<ResultBadge result="running" />);
    expect(screen.getByText("running")).toHaveClass("badge-info");
  });

  it("falls back to the neutral tone for unknown results", () => {
    render(<ResultBadge result="skipped" />);
    expect(screen.getByText("skipped")).toHaveClass("badge-neutral");
  });
});

describe("ErrorNotice", () => {
  it("shows the friendly label, message, and hint for an ApiError", () => {
    const err = new ApiError(400, {
      code: "validation",
      message: "Name is required",
      hint: "Pick a different name",
    });
    render(<ErrorNotice error={err} />);

    expect(screen.getByRole("alert")).toBeInTheDocument();
    expect(screen.getByText("Please check your input")).toBeInTheDocument();
    expect(screen.getByText("Name is required")).toBeInTheDocument();
    expect(screen.getByText("Pick a different name")).toBeInTheDocument();
  });

  it("uses a generic heading for a plain Error and omits the hint", () => {
    render(<ErrorNotice error={new Error("boom")} />);
    expect(screen.getByText("Something went wrong")).toBeInTheDocument();
    expect(screen.getByText("boom")).toBeInTheDocument();
    expect(document.querySelector(".callout-hint")).toBeNull();
  });
});
