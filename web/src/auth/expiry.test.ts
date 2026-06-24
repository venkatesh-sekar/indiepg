import { describe, it, expect, vi, afterEach } from "vitest";
import { onSessionExpired, notifySessionExpired } from "./expiry";

// `listeners` in expiry.ts is a module-level singleton shared across tests in
// this file. Track every subscription and drain it after each test so a test
// that throws before its own off() can't leak a listener into the next one.
const offs: Array<() => void> = [];
function track(fn: () => void): () => void {
  const off = onSessionExpired(fn);
  offs.push(off);
  return off;
}
afterEach(() => {
  while (offs.length) offs.pop()!();
});

describe("session-expiry pub/sub", () => {
  it("delivers a notify to a subscriber", () => {
    const fn = vi.fn();
    track(fn);
    notifySessionExpired();
    expect(fn).toHaveBeenCalledTimes(1);
  });

  it("stops delivering after unsubscribe", () => {
    const fn = vi.fn();
    const off = track(fn);
    off();
    notifySessionExpired();
    expect(fn).not.toHaveBeenCalled();
  });

  it("notifies every active subscriber", () => {
    const a = vi.fn();
    const b = vi.fn();
    track(a);
    track(b);
    notifySessionExpired();
    expect(a).toHaveBeenCalledTimes(1);
    expect(b).toHaveBeenCalledTimes(1);
  });
});
