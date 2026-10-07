import { describe, expect, it } from "vitest";
import { type Bucket, type LimitReply, type WindowState, parseRequest, peekWindow, recordWindow, takeBucket } from "../src/limitmath";
import sharedVectors from "../../../internal/panel/securitylimit/testdata/vectors.json";

interface SharedVectors {
  scenarios: VectorScenario[];
}

interface VectorScenario {
  name: string;
  operations: VectorOperation[];
}

interface VectorOperation {
  op: "take" | "peek" | "record" | "reset";
  at_ms: number;
  name: string;
  key: string;
  cost?: number;
  bucket?: { name: string; burst: number; refill_ms: number };
  window?: { name: string; limit: number; span_ms: number; lockout_ms: number };
  want: { allowed: boolean; retry_after_ms: number; remaining: number; first: boolean };
}

const vectors = sharedVectors as SharedVectors;

function pair(name: string, key: string): string {
  return `${name}\u0000${key}`;
}

function runScenario(scenario: VectorScenario): void {
  const buckets = new Map<string, Bucket>();
  const windows = new Map<string, WindowState>();
  for (const [index, operation] of scenario.operations.entries()) {
    const id = pair(operation.name, operation.key);
    let reply: LimitReply;
    switch (operation.op) {
      case "take": {
        const spec = operation.bucket;
        if (!spec || operation.cost === undefined) throw new Error(`missing bucket input in operation ${index}`);
        const result = takeBucket(buckets.get(id), operation.at_ms, spec.burst, spec.refill_ms, operation.cost);
        buckets.set(id, result.state);
        reply = result.reply;
        break;
      }
      case "peek": {
        const spec = operation.window;
        if (!spec) throw new Error(`missing window input in operation ${index}`);
        reply = peekWindow(windows.get(id), operation.at_ms, spec.limit, spec.span_ms);
        break;
      }
      case "record": {
        const spec = operation.window;
        if (!spec) throw new Error(`missing window input in operation ${index}`);
        const result = recordWindow(windows.get(id), operation.at_ms, spec.limit, spec.span_ms, spec.lockout_ms);
        windows.set(id, result.state);
        reply = result.reply;
        break;
      }
      case "reset":
        windows.delete(id);
        reply = { ok: true, retryAfterMs: 0, remaining: 0, first: false };
        break;
    }
    expect(reply, `${scenario.name}, operation ${index} (${operation.op})`).toEqual({
      ok: operation.want.allowed,
      retryAfterMs: operation.want.retry_after_ms,
      remaining: operation.want.remaining,
      first: operation.want.first,
    });
  }
}

describe("shared limiter vectors", () => {
  for (const scenario of vectors.scenarios) {
    it(scenario.name, () => runScenario(scenario));
  }
});

describe("token bucket cleanup", () => {
  it("sets fullAt to when the bucket has refilled", () => {
    const state = takeBucket(undefined, 1000, 10, 3000, 1).state;
    expect(state.tokens).toBe(9);
    expect(state.fullAt).toBe(1000 + 3000);
    expect(takeBucket(state, 1000, 10, 3000, 4).state.fullAt).toBe(1000 + 5 * 3000);
  });
});

describe("parseRequest", () => {
  const base = { name: "auth", key: "203.0.113.1" };

  it("accepts the four callback operations and their Go value fields", () => {
    expect(parseRequest({ ...base, operation: "take", burst: 10, refillMs: 3000, cost: 1 })).toEqual({
      operation: "take", ...base, burst: 10, refillMs: 3000, cost: 1,
    });
    expect(parseRequest({ ...base, operation: "peek", limit: 5, spanMs: 600_000, lockoutMs: 0 })).toEqual({
      operation: "peek", ...base, limit: 5, spanMs: 600_000, lockoutMs: 0,
    });
    expect(parseRequest({ ...base, operation: "record", limit: 20, spanMs: 60_000, lockoutMs: 900_000 })).toEqual({
      operation: "record", ...base, limit: 20, spanMs: 60_000, lockoutMs: 900_000,
    });
    expect(parseRequest({ ...base, operation: "reset" })).toEqual({ operation: "reset", ...base });
  });

  it.each([
    ["not an object", null],
    ["a string", "take"],
    ["no operation", base],
    ["unknown operation", { ...base, operation: "drop" }],
    ["old failure operation", { ...base, operation: "fail", spanMs: 60_000 }],
    ["no name", { key: "k", operation: "reset" }],
    ["numeric key", { name: "n", key: 1, operation: "reset" }],
    ["take without burst", { ...base, operation: "take", refillMs: 1, cost: 1 }],
    ["take zero cost", { ...base, operation: "take", burst: 1, refillMs: 1, cost: 0 }],
    ["take negative refill", { ...base, operation: "take", burst: 1, refillMs: -1, cost: 1 }],
    ["take NaN burst", { ...base, operation: "take", burst: Number.NaN, refillMs: 1, cost: 1 }],
    ["take infinite refill", { ...base, operation: "take", burst: 1, refillMs: Infinity, cost: 1 }],
    ["take string burst", { ...base, operation: "take", burst: "3", refillMs: 1, cost: 1 }],
    ["record zero limit", { ...base, operation: "record", limit: 0, spanMs: 1, lockoutMs: 0 }],
    ["peek fractional limit", { ...base, operation: "peek", limit: 1.5, spanMs: 1, lockoutMs: 0 }],
    ["record without a span", { ...base, operation: "record", limit: 1, lockoutMs: 0 }],
    ["negative lockout", { ...base, operation: "record", limit: 1, spanMs: 1, lockoutMs: -1 }],
  ] as Array<[string, unknown]>)("rejects %s", (_what, request) => {
    expect(() => parseRequest(request)).toThrow(/limiter:/);
  });
});
