import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, waitFor } from "@testing-library/react";
import { SessionProvider, useSession } from "./SessionContext";
import { notifySessionExpired } from "./expiry";

// Stub the API so the provider boots into an authenticated session.
vi.mock("@/api/client", () => ({
  api: {
    session: () => Promise.resolve({ authenticated: true }),
    whoami: () => Promise.resolve({ subject: "admin" }),
  },
}));

function Probe() {
  const { ready, authenticated } = useSession();
  if (!ready) return <span>booting</span>;
  return <span>{authenticated ? "authed" : "anon"}</span>;
}

describe("SessionProvider reacts to a mid-session 401", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("drops to unauthenticated when a request signals session expiry", async () => {
    render(
      <SessionProvider>
        <Probe />
      </SessionProvider>,
    );

    // Boots into an authenticated session.
    await waitFor(() => expect(screen.getByText("authed")).toBeInTheDocument());

    // A poll/fetch returning 401 trips the bridge → guard would now bounce to /login.
    act(() => {
      notifySessionExpired();
    });

    expect(screen.getByText("anon")).toBeInTheDocument();
  });
});
