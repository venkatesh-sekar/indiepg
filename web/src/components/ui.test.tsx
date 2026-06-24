import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { fireEvent } from "@testing-library/react";
import {
  ResultBadge,
  ErrorNotice,
  StaleBanner,
  Spinner,
  SecretValue,
  PageHeader,
} from "./ui";
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
    // warning (not destructive) — the cached data is still useful, soft stall.
    expect(alert).toHaveAttribute("data-variant", "warning");
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

describe("Spinner", () => {
  it("announces a loading status with the given label", () => {
    render(<Spinner label="Loading backup history…" />);
    const status = screen.getByRole("status");
    expect(status).toBeInTheDocument();
    expect(
      screen.getByText("Loading backup history…"),
    ).toBeInTheDocument();
    // composed over the shadcn Spinner primitive, not the dead .spinner span.
    expect(status.querySelector('[data-slot="spinner"]')).toBeInTheDocument();
  });

  it("falls back to a default label", () => {
    render(<Spinner />);
    expect(screen.getByText("Loading…")).toBeInTheDocument();
  });
});

describe("SecretValue", () => {
  it("masks the value until revealed, then toggles back", () => {
    render(<SecretValue label="Password" value="hunter2" />);
    // masked by default — the raw value is not shown.
    expect(screen.queryByText("hunter2")).not.toBeInTheDocument();

    const reveal = screen.getByRole("button", { name: "Reveal" });
    expect(reveal).toHaveAttribute("aria-pressed", "false");
    fireEvent.click(reveal);
    expect(screen.getByText("hunter2")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Hide" }),
    ).toHaveAttribute("aria-pressed", "true");

    // toggles to a Hide affordance.
    fireEvent.click(screen.getByRole("button", { name: "Hide" }));
    expect(screen.queryByText("hunter2")).not.toBeInTheDocument();
  });

  it("renders reveal/copy as shadcn outline buttons", () => {
    render(<SecretValue label="DSN" value="postgres://x" />);
    for (const name of ["Reveal", "Copy"]) {
      const btn = screen.getByRole("button", { name });
      expect(btn).toHaveAttribute("data-slot", "button");
      expect(btn).toHaveAttribute("data-variant", "outline");
    }
  });
});

describe("PageHeader", () => {
  it("renders the title as a level-1 heading", () => {
    render(<PageHeader title="Backups" />);
    expect(
      screen.getByRole("heading", { level: 1, name: "Backups" }),
    ).toBeInTheDocument();
  });

  it("renders the description and actions when provided", () => {
    render(
      <PageHeader
        title="Roles"
        description="Manage database roles"
        actions={<button>Create</button>}
      />,
    );
    expect(screen.getByText("Manage database roles")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Create" }),
    ).toBeInTheDocument();
  });

  it("omits the description and actions when not provided", () => {
    render(<PageHeader title="Alerts" />);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});
