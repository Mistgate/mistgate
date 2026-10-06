// The pure math of the Limiter Durable Object: one (name, key) pair, no storage, no clock of its own. It mirrors
// internal/panel/securitylimit.Memory (the VPS implementation, which is the contract) for a single key, so it runs
// under plain Node in the tests.
//
// What differs from Memory, on purpose:
//   - `now` is the Durable Object's own Date.now(); the Go side's `now` is not sent (a Worker isolate's clock and the
//     object's can differ, and the object is the one place every isolate agrees on).
//   - `maxKeys` is accepted and ignored: Memory bounds a map of keys, here every (name, key) is its own object, so
//     there is no map to bound; an idle object's state is deleted by its alarm instead.
//   - retryAfterMs is rounded up to whole milliseconds, so a client told to wait that long is never early.

export type LimitRequest =
  | { operation: "take"; burst: number; refillMs: number; cost: number }
  | { operation: "peek"; limit: number; windowMs: number }
  | { operation: "record"; limit: number; windowMs: number }
  | { operation: "fail"; windowMs: number }
  | { operation: "reset" };

/** What every operation resolves to; Go (cmd/mistgate-edge/limiter_js.go) checks the four types. */
export interface LimitReply {
  ok: boolean;
  retryAfterMs: number;
  remaining: number;
  first: boolean;
}

/** A token bucket. `fullAt` is when it has refilled to the burst again: from then on the state is the same as none. */
export interface Bucket {
  tokens: number;
  last: number;
  fullAt: number;
}

/** A failure window. `end` is start + the window length: from then on the state is the same as none. */
export interface Window {
  start: number;
  n: number;
  logged: boolean;
  end: number;
}

const allowed = (remaining = 0): LimitReply => ({ ok: true, retryAfterMs: 0, remaining, first: false });

function positive(value: unknown, what: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) throw new Error(`limiter: invalid ${what}`);
  return value;
}

function text(value: unknown, what: string): void {
  if (typeof value !== "string") throw new Error(`limiter: invalid ${what}`);
}

/** Validates what the Go side sent. Anything else throws, which Go turns into a refused request (fail closed). */
export function parseRequest(raw: unknown): LimitRequest {
  if (typeof raw !== "object" || raw === null) throw new Error("limiter: request is not an object");
  const r = raw as Record<string, unknown>;
  text(r.name, "name");
  text(r.key, "key");
  switch (r.operation) {
    case "take":
      return { operation: "take", burst: positive(r.burst, "burst"), refillMs: positive(r.refillMs, "refillMs"), cost: positive(r.cost, "cost") };
    case "peek":
    case "record": {
      const limit = positive(r.limit, "limit");
      if (!Number.isInteger(limit)) throw new Error("limiter: invalid limit");
      return { operation: r.operation, limit, windowMs: positive(r.windowMs, "windowMs") };
    }
    case "fail":
      return { operation: "fail", windowMs: positive(r.windowMs, "windowMs") };
    case "reset":
      return { operation: "reset" };
    default:
      throw new Error("limiter: unknown operation");
  }
}

/** Memory.Take: refill by the time passed (never above the burst), then spend `cost` or say how long until it fits. */
export function takeBucket(prev: Bucket | undefined, now: number, burst: number, refillMs: number, cost: number): { state: Bucket; reply: LimitReply } {
  const tokens = Math.min(burst, (prev?.tokens ?? burst) + Math.max(0, now - (prev?.last ?? now)) / refillMs);
  if (tokens < cost) {
    return {
      state: { tokens, last: now, fullAt: bucketFullAt(tokens, now, burst, refillMs) },
      reply: { ok: false, retryAfterMs: Math.ceil((cost - tokens) * refillMs), remaining: 0, first: false },
    };
  }
  const left = tokens - cost;
  return { state: { tokens: left, last: now, fullAt: bucketFullAt(left, now, burst, refillMs) }, reply: allowed() };
}

function bucketFullAt(tokens: number, now: number, burst: number, refillMs: number): number {
  return now + Math.max(0, Math.ceil((burst - tokens) * refillMs));
}

/** Memory.CheckWindow: reads only. No window, or one that has ended, is a clean slate. `first` is not consumed here. */
export function peekWindow(w: Window | undefined, now: number, limit: number, windowMs: number): LimitReply {
  if (!w || now - w.start >= windowMs) return allowed(limit);
  if (w.n >= limit) return { ok: false, retryAfterMs: w.start + windowMs - now, remaining: 0, first: !w.logged };
  return allowed(limit - w.n);
}

/** The window as it stands for a write at `now`: a missing or ended one starts again at zero. */
function current(w: Window | undefined, now: number, windowMs: number): Window {
  if (!w || now - w.start >= windowMs) return { start: now, n: 0, logged: false, end: now + windowMs };
  return { ...w, end: w.start + windowMs };
}

/** Memory.RecordWindow: count one attempt; at the limit, refuse (and report `first` once per window). */
export function recordWindow(prev: Window | undefined, now: number, limit: number, windowMs: number): { state: Window; reply: LimitReply } {
  const w = current(prev, now, windowMs);
  if (w.n >= limit) {
    const first = !w.logged;
    w.logged = true;
    return { state: w, reply: { ok: false, retryAfterMs: w.start + windowMs - now, remaining: 0, first } };
  }
  w.n++;
  return { state: w, reply: allowed(limit - w.n) };
}

/** Memory.FailWindow: count one failure with no threshold check (paired with a peek). */
export function failWindow(prev: Window | undefined, now: number, windowMs: number): Window {
  const w = current(prev, now, windowMs);
  w.n++;
  return w;
}
