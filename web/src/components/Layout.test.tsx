import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { Layout } from "./Layout";

const logout = vi.fn().mockResolvedValue(undefined);

vi.mock("@/auth/SessionContext", () => ({
  useSession: () => ({
    ready: true,
    authenticated: true,
    subject: "admin@indiepg",
    logout,
  }),
}));

function renderShell(initial = "/") {
  render(
    <MemoryRouter initialEntries={[initial]}>
      <Routes>
        <Route element={<Layout />}>
          <Route index element={<div>Dashboard view</div>} />
          <Route path="query" element={<div>Query view</div>} />
          <Route path="settings" element={<div>Settings view</div>} />
        </Route>
        <Route path="/login" element={<div>Login screen</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  logout.mockClear();
});

describe("Layout shell", () => {
  it("renders every nav item and the routed outlet", () => {
    renderShell("/");
    for (const label of [
      "Dashboard",
      "Query",
      "Roles & Databases",
      "Backups",
      "Alerts",
      "Migrate",
      "Settings",
    ]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
    expect(screen.getByText("Dashboard view")).toBeInTheDocument();
    expect(screen.getByText("admin@indiepg")).toBeInTheDocument();
  });

  it("shows the current view's label in the top bar", () => {
    renderShell("/query");
    // "Query" appears both as the nav link and the header title.
    expect(screen.getAllByText("Query").length).toBeGreaterThanOrEqual(2);
  });

  it("marks the current route's nav item as active", () => {
    renderShell("/query");
    expect(screen.getByRole("link", { name: "Query" })).toHaveAttribute(
      "data-active",
      "true",
    );
    expect(screen.getByRole("link", { name: "Dashboard" })).toHaveAttribute(
      "data-active",
      "false",
    );
  });

  it("navigates to a view when its nav item is clicked", async () => {
    renderShell("/");
    fireEvent.click(screen.getByRole("link", { name: "Settings" }));
    expect(await screen.findByText("Settings view")).toBeInTheDocument();
  });

  it("resets the main scroll position to the top on navigation", async () => {
    renderShell("/");
    const main = screen.getByTestId("main-content");
    // Simulate the user having scrolled down inside the persistent <main>.
    main.scrollTop = 400;
    expect(main.scrollTop).toBe(400);
    fireEvent.click(screen.getByRole("link", { name: "Settings" }));
    expect(await screen.findByText("Settings view")).toBeInTheDocument();
    expect(main.scrollTop).toBe(0);
  });

  it("signs out and routes to /login", async () => {
    renderShell("/");
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }));
    await waitFor(() => expect(logout).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("Login screen")).toBeInTheDocument();
  });
});
