import { describe, expect, it, vi } from "vitest";
import { routeLimit } from "../src/shell";

// A fake namespace that records which object name each call was routed to.
function namespace(reply = { ok: true, retryAfterMs: 0, remaining: 0, first: false }) {
  const calls: { id: string; request: unknown }[] = [];
  const ns = {
    idFromName: vi.fn((name: string) => name),
    get: (id: string) => ({
      limit: async (request: unknown) => {
        calls.push({ id, request });
        return reply;
      },
    }),
  };
  return { ns: ns as unknown as Parameters<typeof routeLimit>[0], calls, idFromName: ns.idFromName };
}

describe("routeLimit", () => {
  it("sends each (name, key) to its own object and passes the request through", async () => {
    const { ns, calls } = namespace();
    const request = { operation: "take", name: "auth", key: "203.0.113.7", burst: 10, refillMs: 3000, cost: 1 };
    await routeLimit(ns, request);
    await routeLimit(ns, { ...request, key: "203.0.113.8" });
    await routeLimit(ns, { ...request, name: "api-token" });
    expect(calls.map((c) => c.id)).toEqual(["auth\u0000203.0.113.7", "auth\u0000203.0.113.8", "api-token\u0000203.0.113.7"]);
    expect(calls[0]?.request).toBe(request);
  });

  it("returns the object's reply and lets a failure through (the panel then refuses the request)", async () => {
    const { ns } = namespace({ ok: false, retryAfterMs: 5, remaining: 0, first: true });
    expect(await routeLimit(ns, { name: "page-password", key: "t:x" })).toEqual({ ok: false, retryAfterMs: 5, remaining: 0, first: true });
    const broken = {
      idFromName: () => {
        throw new Error("name too long");
      },
    } as unknown as Parameters<typeof routeLimit>[0];
    await expect(routeLimit(broken, { name: "auth", key: "k" })).rejects.toThrow("name too long"); // a rejection, never a sync throw
  });
});
