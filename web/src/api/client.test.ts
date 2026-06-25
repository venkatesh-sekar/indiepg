import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { api } from "./client";
import type { AlertRule } from "./types";

// A rule as returned by GET /alerts: carries read-only evaluation fields
// (state/last_fired_at/last_eval_at) that the PUT endpoint does not accept.
function fetchedRule(over: Partial<AlertRule> = {}): AlertRule {
  return {
    id: "r1",
    name: "Disk almost full",
    metric: "host.disk_percent",
    op: ">",
    threshold: 90,
    severity: "warning",
    for_seconds: 0,
    cooldown_seconds: 600,
    enabled: true,
    state: "ok",
    last_fired_at: null,
    last_eval_at: "2026-06-25T00:00:00Z",
    ...over,
  };
}

describe("api.saveRule", () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ data: { ok: true } }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  // Regression: toggling a rule's enabled flag spreads the whole fetched rule,
  // which includes read-only state fields. The PUT body must NOT carry them or
  // the server's strict decoder rejects it with "invalid JSON body".
  it("does not send read-only response fields the PUT endpoint rejects", async () => {
    await api.saveRule({ ...fetchedRule(), enabled: false });

    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [, init] = fetchMock.mock.calls[0];
    const body = JSON.parse(init.body as string);

    expect(body).not.toHaveProperty("state");
    expect(body).not.toHaveProperty("last_fired_at");
    expect(body).not.toHaveProperty("last_eval_at");
    // The definition fields are preserved, with the flipped flag.
    expect(body).toMatchObject({ id: "r1", metric: "host.disk_percent", enabled: false });
  });
});
