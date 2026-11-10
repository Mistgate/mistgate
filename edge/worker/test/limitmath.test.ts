import { describe, expect, it } from "vitest";
import {
  type Bucket, type LimitReply, type WindowState, parseRequest, peekWindow, recordWindow, recordWindowN, takeBucket, undoWindowN,
} from "../src/limitmath";
import sharedVectors from "../../../internal/panel/securitylimit/testdata/vectors.json";

interface SharedVectors {
  scenarios: VectorScenario[];
}

interface VectorScenario {
  name: string;
  go_only?: boolean;
  operations: VectorOperation[];
}

interface VectorOperation {
  op: "take" | "peek" | "record" | "recordN" | "undoN" | "reset";
  at_ms: number;
  name: string;
  key: string;
  bucket?: { name: string; burst: number; refill_ms: number };
  window?: { name: string; limit: number; span_ms: number; lockout_ms: number };
  units?: number;
  window_start_ms?: number;
  want: { allowed: boolean; retry_after_ms: number; remaining: number; first: boolean; window_start_ms?: number };
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
        if (!spec) throw new Error(`missing bucket input in operation ${index}`);
        const result = takeBucket(buckets.get(id), operation.at_ms, spec.burst, spec.refill_ms);
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
      case "recordN": {
        const spec = operation.window;
        if (!spec || operation.units === undefined) throw new Error(`missing weighted window input in operation ${index}`);
        const result = recordWindowN(windows.get(id), operation.at_ms, spec.limit, spec.span_ms, operation.units);
        if (result.state) windows.set(id, result.state);
        else windows.delete(id);
        reply = result.reply;
        break;
      }
      case "undoN": {
        const spec = operation.window;
        if (!spec || operation.units === undefined || operation.window_start_ms === undefined) throw new Error(`missing weighted window input in operation ${index}`);
        const state = undoWindowN(windows.get(id), operation.at_ms, spec.span_ms, operation.units, operation.window_start_ms);
        if (state) windows.set(id, state);
        else windows.delete(id);
        reply = { ok: true, retryAfterMs: 0, remaining: 0, first: false };
        break;
      }
      case "reset":
        windows.delete(id);
        reply = { ok: true, retryAfterMs: 0, remaining: 0, first: false };
        break;
    }
    const want: LimitReply = {
      ok: operation.want.allowed,
      retryAfterMs: operation.want.retry_after_ms,
      remaining: operation.want.remaining,
      first: operation.want.first,
    };
    if (operation.want.window_start_ms !== undefined) want.windowStartMs = operation.want.window_start_ms;
    expect(reply, `${scenario.name}, operation ${index} (${operation.op})`).toEqual(want);
  }
}

describe("shared limiter vectors", () => {
  for (const scenario of vectors.scenarios) {
    (scenario.go_only ? it.skip : it)(scenario.name, () => runScenario(scenario));
  }
});

describe("token bucket cleanup", () => {
  it("sets fullAt to when the bucket has refilled", () => {
    const state = takeBucket(undefined, 1000, 10, 3000).state;
    expect(state.tokens).toBe(9);
    expect(state.fullAt).toBe(1000 + 3000);
    expect(takeBucket(state, 1000, 10, 3000).state.fullAt).toBe(1000 + 2 * 3000);
  });
});

describe("parseRequest", () => {
  const base = { name: "auth", key: "203.0.113.1" };

  it("accepts every callback operation and its Go value fields", () => {
    expect(parseRequest({ ...base, operation: "take", burst: 10, refillMs: 3000 })).toEqual({
      operation: "take", ...base, burst: 10, refillMs: 3000,
    });
    expect(parseRequest({ ...base, operation: "peek", limit: 5, spanMs: 600_000, lockoutMs: 0 })).toEqual({
      operation: "peek", ...base, limit: 5, spanMs: 600_000, lockoutMs: 0,
    });
    expect(parseRequest({ ...base, operation: "record", limit: 20, spanMs: 60_000, lockoutMs: 900_000 })).toEqual({
      operation: "record", ...base, limit: 20, spanMs: 60_000, lockoutMs: 900_000,
    });
    expect(parseRequest({ ...base, operation: "recordN", limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 3 })).toEqual({
      operation: "recordN", ...base, limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 3,
    });
    expect(parseRequest({ ...base, operation: "undoN", limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 1, windowStartMs: 900 })).toEqual({
      operation: "undoN", ...base, limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 1, windowStartMs: 900,
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
    ["take without burst", { ...base, operation: "take", refillMs: 1 }],
    ["take negative refill", { ...base, operation: "take", burst: 1, refillMs: -1 }],
    ["take NaN burst", { ...base, operation: "take", burst: Number.NaN, refillMs: 1 }],
    ["take infinite refill", { ...base, operation: "take", burst: 1, refillMs: Infinity }],
    ["take string burst", { ...base, operation: "take", burst: "3", refillMs: 1 }],
    ["record zero limit", { ...base, operation: "record", limit: 0, spanMs: 1, lockoutMs: 0 }],
    ["peek fractional limit", { ...base, operation: "peek", limit: 1.5, spanMs: 1, lockoutMs: 0 }],
    ["record without a span", { ...base, operation: "record", limit: 1, lockoutMs: 0 }],
    ["weighted record without units", { ...base, operation: "recordN", limit: 10, spanMs: 3_600_000, lockoutMs: 0 }],
    ["weighted record with lockout", { ...base, operation: "recordN", limit: 10, spanMs: 3_600_000, lockoutMs: 1, units: 1 }],
    ["weighted undo without its window", { ...base, operation: "undoN", limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 1 }],
    ["weighted undo with a fractional window", { ...base, operation: "undoN", limit: 10, spanMs: 3_600_000, lockoutMs: 0, units: 1, windowStartMs: 1.5 }],
    ["negative lockout", { ...base, operation: "record", limit: 1, spanMs: 1, lockoutMs: -1 }],
  ] as Array<[string, unknown]>)("rejects %s", (_what, request) => {
    expect(() => parseRequest(request)).toThrow(/limiter:/);
  });
});

describe("weighted window reservations", () => {
  it("reserves the full weight or none and refunds failed work", () => {
    const spec = { limit: 10, spanMs: 3_600_000 };
    const first = recordWindowN(undefined, 1_000, spec.limit, spec.spanMs, 8);
    expect(first.reply).toEqual({ ok: true, retryAfterMs: 0, remaining: 2, first: false, windowStartMs: 1_000 });
    const refused = recordWindowN(first.state, 1_000, spec.limit, spec.spanMs, 3);
    expect(refused.reply).toEqual({ ok: false, retryAfterMs: spec.spanMs, remaining: 2, first: false, windowStartMs: 1_000 });
    expect(refused.state.n).toBe(8);
    const final = recordWindowN(first.state, 1_000, spec.limit, spec.spanMs, 2);
    expect(final.reply).toEqual({ ok: true, retryAfterMs: 0, remaining: 0, first: false, windowStartMs: 1_000 });
    expect(undoWindowN(final.state!, 1_001, spec.spanMs, 2, 1_000)?.n).toBe(8);
    expect(undoWindowN(first.state!, 3_601_000, spec.spanMs, 8, 1_000)?.n).toBe(8);
  });

  it("does not start an hour for an impossible reservation", () => {
    const spec = { limit: 10, spanMs: 3_600_000 };
    const refused = recordWindowN(undefined, 1_000, spec.limit, spec.spanMs, 11);
    expect(refused.state).toBeUndefined();
    expect(refused.reply).toEqual({ ok: false, retryAfterMs: 0, remaining: 10, first: false });
    const first = recordWindowN(refused.state, 2_000, spec.limit, spec.spanMs, 10);
    expect(first.reply.windowStartMs).toBe(2_000);
  });

  it("keeps the start at zero usage and ignores refunds for an older window", () => {
    const spec = { limit: 10, spanMs: 3_600_000 };
    const first = recordWindowN(undefined, 1_000, spec.limit, spec.spanMs, 8);
    const refunded = undoWindowN(first.state!, 1_001, spec.spanMs, 8, 1_000);
    expect(refunded).toEqual({ start: 1_000, n: 0, logged: false, lockedUntil: 0, end: 3_601_000 });
    const second = recordWindowN(refunded, 1_800_000, spec.limit, spec.spanMs, 3);
    expect(second.reply.windowStartMs).toBe(1_000);
    const next = recordWindowN(second.state, 3_601_001, spec.limit, spec.spanMs, 3);
    const unchanged = undoWindowN(next.state, 3_601_002, spec.spanMs, 8, 1_000);
    expect(unchanged).toEqual(next.state);
    const full = recordWindowN(unchanged, 3_601_003, spec.limit, spec.spanMs, 7);
    expect(full.reply.remaining).toBe(0);
  });
});
