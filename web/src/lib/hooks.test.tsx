import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { ApiError } from "@/api/client";
import { useAsync, usePolling } from "./hooks";

// Spy on the expiry bridge so we can assert the hooks trip it on a 401.
const notify = vi.hoisted(() => vi.fn());
vi.mock("@/auth/expiry", () => ({
  notifySessionExpired: notify,
}));

const authErr = new ApiError(401, { code: "auth", message: "session expired" });
const otherErr = new ApiError(500, { code: "internal", message: "boom" });

describe("data hooks trip session expiry on a 401", () => {
  beforeEach(() => {
    notify.mockClear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("useAsync signals expiry when the loader fails with an auth error", async () => {
    const { result } = renderHook(() => useAsync(() => Promise.reject(authErr)));
    await waitFor(() => expect(result.current.error).toBe(authErr));
    expect(notify).toHaveBeenCalledTimes(1);
  });

  it("useAsync does NOT signal expiry on a non-auth error", async () => {
    const { result } = renderHook(() => useAsync(() => Promise.reject(otherErr)));
    await waitFor(() => expect(result.current.error).toBe(otherErr));
    expect(notify).not.toHaveBeenCalled();
  });

  it("usePolling signals expiry when a poll fails with an auth error", async () => {
    const { result } = renderHook(() =>
      usePolling(() => Promise.reject(authErr), 60_000),
    );
    await waitFor(() => expect(result.current.error).toBe(authErr));
    expect(notify).toHaveBeenCalledTimes(1);
  });

  it("usePolling does NOT signal expiry on a non-auth error", async () => {
    const { result } = renderHook(() =>
      usePolling(() => Promise.reject(otherErr), 60_000),
    );
    await waitFor(() => expect(result.current.error).toBe(otherErr));
    expect(notify).not.toHaveBeenCalled();
  });
});
