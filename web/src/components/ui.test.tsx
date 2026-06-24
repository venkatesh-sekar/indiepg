import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { ResultBadge, ErrorNotice, StaleBanner } from "./ui";
import { ApiError } from "@/api/client";

describe("ResultBadge", () => {
  it("renders success-like results with the success variant", () => {
    render(<ResultBadge result="completed" />);
    expect(screen.getByText("completed")).toHaveAttribute(
      "data-variant",
      "success",
    );
  });

  it("renders failures with the destructive variant", () => {
    render(<ResultBadge result="failed" />);
    expect(screen.getByText("failed")).toHaveAttribute(
      "data-variant",
      "destructive",
    );
  });

  it("treats in-progress results as info", () => {
    render(<ResultBadge result="running" />);
    expect(screen.getByText("running")).toHaveAttribute("data-variant", "info");
  });

  it("falls back to the neutral (secondary) variant for unknown results", () => {
    render(<ResultBadge result="skipped" />);
    expect(screen.getByText("skipped")).toHaveAttribute(
      "data-variant",
      "secondary",
    );
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

describe("StaleBanner", () => {
  it("announces that live updates paused, keeps a warn tone, and shows the cause", () => {
    const err = new ApiError(0, {
      code: "internal",
      message: "Could not reach the panel. Check your connection.",
    });
    render(<StaleBanner error={err} />);

    // role=alert so the freeze is announced, not silent.
    const alert = screen.getByRole("alert");
    expect(alert).toBeInTheDocument();
    // warn (not danger) — the cached data is still useful, this is a soft stall.
    expect(alert).toHaveClass("callout-warn");
    expect(screen.getByText("Live updates paused")).toBeInTheDocument();
    expect(
      screen.getByText(/Could not reach the panel\. Check your connection\./),
    ).toBeInTheDocument();
  });

  it("includes the hint when the error carries one", () => {
    const err = new ApiError(401, {
      code: "auth",
      message: "Session expired",
      hint: "Sign in again to resume live updates",
    });
    render(<StaleBanner error={err} />);
    expect(
      screen.getByText("Sign in again to resume live updates"),
    ).toBeInTheDocument();
  });
});
