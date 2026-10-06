import { describe, expect, it } from "vitest";
import { type Bucket, type Window, failWindow, parseRequest, peekWindow, recordWindow, takeBucket } from "../src/limitmath";

// The cases follow the Go tests of the VPS implementation (auth TestLimiter, subs unlock_test, fleet enroll limiter):
// the Durable Object must answer exactly as securitylimit.Memory does for one key.

/** Applies take() with the new state carried over, like the object's storage does. */
function bucket(burst: number, refillMs: number) {
  let state: Bucket | undefined;
  return (now: number, cost = 1) => {
    const r = takeBucket(state, now, burst, refillMs, cost);
    state = r.state;
    return r.reply;
  };
}

function window(limit: number, windowMs: number) {
  let state: Window | undefined;
  return {
    peek: (now: number) => peekWindow(state, now, limit, windowMs),
    record: (now: number) => {
      const r = recordWindow(state, now, limit, windowMs);
      state = r.state;
      return r.reply;
    },
    fail: (now: number) => {
      state = failWindow(state, now, windowMs);
    },
    reset: () => {
      state = undefined;
    },
    get state() {
      return state;
    },
  };
}

describe("token bucket (Memory.Take)", () => {
  it("allows the burst, then refuses, like the auth limiter", () => {
    const take = bucket(3, 1000);
    for (let i = 0; i < 3; i++) expect(take(0).ok, `burst request ${i}`).toBe(true);
    expect(take(0)).toEqual({ ok: false, retryAfterMs: 1000, remaining: 0, first: false });
  });

  it("refills one token per refill period, exactly", () => {
    const take = bucket(3, 1000);
    for (let i = 0; i < 3; i++) take(0);
    expect(take(0).ok).toBe(false);
    expect(take(1100).ok).toBe(true); // 1.1 tokens: one spent, 0.1 left
    expect(take(1100).ok).toBe(false);
  });

  it("never refills past the burst", () => {
    const take = bucket(3, 1000);
    take(0);
    const hour = 3_600_000;
    for (let i = 0; i < 3; i++) expect(take(hour).ok).toBe(true);
    expect(take(hour).ok).toBe(false);
  });

  it("charges the cost and says how long until it fits", () => {
    const take = bucket(5, 1000);
    expect(take(0, 4).ok).toBe(true); // 1 left
    expect(take(0, 3)).toEqual({ ok: false, retryAfterMs: 2000, remaining: 0, first: false }); // 2 tokens short
    expect(take(2000, 3).ok).toBe(true); // refilled to 3
  });

  it("a refused request still counts the time that passed", () => {
    const take = bucket(1, 1000);
    take(0);
    expect(take(400).retryAfterMs).toBe(600);
    expect(take(900).retryAfterMs).toBe(100);
    expect(take(1000).ok).toBe(true);
  });

  it("rounds retryAfter up to a whole millisecond", () => {
    const take = bucket(1, 2.5);
    take(0);
    expect(take(1).retryAfterMs).toBe(2); // 0.4 tokens refilled, 0.6 short = 1.5 ms -> 2
  });

  it("a cost above the burst is never allowed, as in Memory", () => {
    const take = bucket(2, 1000);
    expect(take(0, 3).ok).toBe(false);
    expect(take(10_000, 3).ok).toBe(false);
  });

  it("sets fullAt to when the bucket has refilled", () => {
    const state = takeBucket(undefined, 1000, 10, 3000, 1).state;
    expect(state.tokens).toBe(9);
    expect(state.fullAt).toBe(1000 + 3000);
    expect(takeBucket(state, 1000, 10, 3000, 4).state.fullAt).toBe(1000 + 5 * 3000);
  });
});

describe("failure window (Memory.RecordWindow / CheckWindow / FailWindow / Reset)", () => {
  it("counts down remaining, refuses at the limit and reports first only once", () => {
    const w = window(5, 600_000);
    expect(w.peek(0)).toEqual({ ok: true, retryAfterMs: 0, remaining: 5, first: false });
    for (let i = 0; i < 5; i++) expect(w.record(i)).toEqual({ ok: true, retryAfterMs: 0, remaining: 4 - i, first: false });
    expect(w.record(10)).toEqual({ ok: false, retryAfterMs: 599_990, remaining: 0, first: true });
    expect(w.record(20)).toEqual({ ok: false, retryAfterMs: 599_980, remaining: 0, first: false });
    expect(w.record(30).first).toBe(false);
  });

  it("peek does not consume first: the first record that hits the limit still reports it", () => {
    const w = window(2, 1000);
    w.record(0);
    w.record(0);
    expect(w.peek(1)).toEqual({ ok: false, retryAfterMs: 999, remaining: 0, first: true });
    expect(w.peek(2).first).toBe(true); // peeking never logs
    expect(w.record(3).first).toBe(true);
    expect(w.peek(4)).toEqual({ ok: false, retryAfterMs: 996, remaining: 0, first: false });
  });

  it("peek counts nothing", () => {
    const w = window(2, 1000);
    for (let i = 0; i < 10; i++) expect(w.peek(i).remaining).toBe(2);
    expect(w.state).toBeUndefined();
  });

  it("the window ends: peek is clean, the next record starts a new window with first re-armed", () => {
    const w = window(1, 1000);
    w.record(0);
    expect(w.record(10).first).toBe(true);
    expect(w.peek(999).ok).toBe(false);
    expect(w.peek(1000)).toEqual({ ok: true, retryAfterMs: 0, remaining: 1, first: false });
    expect(w.record(1000)).toEqual({ ok: true, retryAfterMs: 0, remaining: 0, first: false });
    expect(w.state?.start).toBe(1000);
    expect(w.record(1001)).toMatchObject({ ok: false, first: true, retryAfterMs: 999 });
  });

  it("fail counts without a threshold check; peek then refuses", () => {
    const w = window(3, 1000);
    for (let i = 0; i < 3; i++) w.fail(i);
    expect(w.state?.n).toBe(3);
    expect(w.peek(10)).toMatchObject({ ok: false, retryAfterMs: 990, first: true });
    w.fail(20); // past the limit is fine: it only counts
    expect(w.state?.n).toBe(4);
    w.fail(1000); // a fresh window
    expect(w.state).toMatchObject({ start: 1000, n: 1, logged: false });
  });

  it("reset forgets the window", () => {
    const w = window(2, 1000);
    w.record(0);
    w.record(0);
    expect(w.record(1).ok).toBe(false);
    w.reset();
    expect(w.record(2)).toEqual({ ok: true, retryAfterMs: 0, remaining: 1, first: false });
  });

  it("the window length of the call decides when it has ended (the stored length is only for cleanup)", () => {
    const first = recordWindow(undefined, 100, 5, 1000);
    expect(first.state).toEqual({ start: 100, n: 1, logged: false, end: 1100 });
    expect(peekWindow(first.state, 400, 5, 200).remaining).toBe(5); // the caller says 200 ms: ended
  });

  it("an unlock lockout: 5 wrong passwords in 10 minutes, then wait, like the subs test", () => {
    const tries = 5;
    const w = window(tries, 600_000);
    for (let i = 0; i < tries; i++) expect(w.record(i).remaining).toBe(tries - 1 - i);
    const locked = w.record(tries);
    expect(locked).toMatchObject({ ok: false, first: true });
    expect(w.peek(tries + 1)).toMatchObject({ ok: false, first: false });
    expect(w.peek(600_001).ok).toBe(true);
  });
});

describe("parseRequest", () => {
  const base = { name: "auth", key: "203.0.113.1" };

  it("accepts the five operations as Go sends them (including the extra fields it adds)", () => {
    expect(parseRequest({ ...base, operation: "take", burst: 10, refillMs: 3000, cost: 1 })).toEqual({ operation: "take", burst: 10, refillMs: 3000, cost: 1 });
    expect(parseRequest({ ...base, operation: "peek", limit: 5, windowMs: 600_000, maxKeys: 4096, touch: true })).toEqual({ operation: "peek", limit: 5, windowMs: 600_000 });
    expect(parseRequest({ ...base, operation: "record", limit: 5, windowMs: 600_000, maxKeys: 4096 })).toEqual({ operation: "record", limit: 5, windowMs: 600_000 });
    expect(parseRequest({ ...base, operation: "fail", windowMs: 60_000, maxKeys: 4096 })).toEqual({ operation: "fail", windowMs: 60_000 });
    expect(parseRequest({ ...base, operation: "reset" })).toEqual({ operation: "reset" });
  });

  it.each([
    ["not an object", null],
    ["a string", "take"],
    ["no operation", { ...base }],
    ["unknown operation", { ...base, operation: "drop" }],
    ["no name", { key: "k", operation: "reset" }],
    ["numeric key", { name: "n", key: 1, operation: "reset" }],
    ["take without burst", { ...base, operation: "take", refillMs: 1, cost: 1 }],
    ["take zero cost", { ...base, operation: "take", burst: 1, refillMs: 1, cost: 0 }],
    ["take negative refill", { ...base, operation: "take", burst: 1, refillMs: -1, cost: 1 }],
    ["take NaN burst", { ...base, operation: "take", burst: Number.NaN, refillMs: 1, cost: 1 }],
    ["take infinite refill", { ...base, operation: "take", burst: 1, refillMs: Infinity, cost: 1 }],
    ["take string burst", { ...base, operation: "take", burst: "3", refillMs: 1, cost: 1 }],
    ["record zero limit", { ...base, operation: "record", limit: 0, windowMs: 1 }],
    ["peek fractional limit", { ...base, operation: "peek", limit: 1.5, windowMs: 1 }],
    ["record without a window", { ...base, operation: "record", limit: 1 }],
    ["fail zero window", { ...base, operation: "fail", windowMs: 0 }],
  ])("rejects %s", (_what, request) => {
    expect(() => parseRequest(request)).toThrow(/limiter:/);
  });
});
