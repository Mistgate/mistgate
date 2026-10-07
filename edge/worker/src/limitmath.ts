// The pure math of the Limiter Durable Object: one (name, key) pair, no storage, no clock of its own.

export type LimitRequest =
  | { operation: "take"; name: string; key: string; burst: number; refillMs: number }
  | { operation: "peek" | "record"; name: string; key: string; limit: number; spanMs: number; lockoutMs: number }
  | { operation: "reset"; name: string; key: string };

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

/** A counted window, including the optional lockout deadline. */
export interface WindowState {
  start: number;
  n: number;
  logged: boolean;
  lockedUntil: number;
  end: number;
}

const allowed = (remaining = 0): LimitReply => ({ ok: true, retryAfterMs: 0, remaining, first: false });

function positive(value: unknown, what: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value <= 0) throw new Error(`limiter: invalid ${what}`);
  return value;
}

function nonnegative(value: unknown, what: string): number {
  if (typeof value !== "number" || !Number.isFinite(value) || value < 0) throw new Error(`limiter: invalid ${what}`);
  return value;
}

function text(value: unknown, what: string): string {
  if (typeof value !== "string") throw new Error(`limiter: invalid ${what}`);
  return value;
}

/** Validates what the Go side sent. Anything else throws, which Go turns into a refused request (fail closed). */
export function parseRequest(raw: unknown): LimitRequest {
  if (typeof raw !== "object" || raw === null) throw new Error("limiter: request is not an object");
  const r = raw as Record<string, unknown>;
  const name = text(r.name, "name");
  const key = text(r.key, "key");
  switch (r.operation) {
    case "take":
      return {
        operation: "take", name, key,
        burst: positive(r.burst, "burst"), refillMs: positive(r.refillMs, "refillMs"),
      };
    case "peek":
    case "record": {
      const limit = positive(r.limit, "limit");
      if (!Number.isInteger(limit)) throw new Error("limiter: invalid limit");
      return {
        operation: r.operation, name, key, limit,
        spanMs: positive(r.spanMs, "spanMs"), lockoutMs: nonnegative(r.lockoutMs, "lockoutMs"),
      };
    }
    case "reset":
      return { operation: "reset", name, key };
    default:
      throw new Error("limiter: unknown operation");
  }
}

/** Refill by the time passed (never above the burst), then spend one token or say when it fits. */
export function takeBucket(prev: Bucket | undefined, now: number, burst: number, refillMs: number): { state: Bucket; reply: LimitReply } {
  const tokens = Math.min(burst, (prev?.tokens ?? burst) + Math.max(0, now - (prev?.last ?? now)) / refillMs);
  if (tokens < 1) {
    return {
      state: { tokens, last: now, fullAt: bucketFullAt(tokens, now, burst, refillMs) },
      reply: { ok: false, retryAfterMs: Math.ceil((1 - tokens) * refillMs), remaining: 0, first: false },
    };
  }
  const left = tokens - 1;
  return { state: { tokens: left, last: now, fullAt: bucketFullAt(left, now, burst, refillMs) }, reply: allowed() };
}

function bucketFullAt(tokens: number, now: number, burst: number, refillMs: number): number {
  return now + Math.max(0, Math.ceil((burst - tokens) * refillMs));
}

/** Reads only. An active lockout takes precedence over the counting window. */
export function peekWindow(w: WindowState | undefined, now: number, limit: number, spanMs: number): LimitReply {
  if (!w) return allowed(limit);
  if (w.lockedUntil > now) return { ok: false, retryAfterMs: w.lockedUntil - now, remaining: 0, first: false };
  if (now - w.start >= spanMs || (w.lockedUntil > 0 && now >= w.lockedUntil)) return allowed(limit);
  if (w.n >= limit) return { ok: false, retryAfterMs: w.start + spanMs - now, remaining: 0, first: !w.logged };
  return allowed(limit - w.n);
}

/** The window as it stands for a write at `now`: a missing or ended one starts again at zero. */
function current(w: WindowState | undefined, now: number, spanMs: number): WindowState {
  if (w && w.lockedUntil > now) return { ...w, end: Math.max(w.end, w.lockedUntil) };
  if (!w || now - w.start >= spanMs || (w.lockedUntil > 0 && now >= w.lockedUntil)) {
    return { start: now, n: 0, logged: false, lockedUntil: 0, end: now + spanMs };
  }
  return { ...w, end: w.start + spanMs };
}

/** Counts one attempt; with a lockout, reaching the limit refuses and locks in this operation. */
export function recordWindow(
  prev: WindowState | undefined,
  now: number,
  limit: number,
  spanMs: number,
  lockoutMs: number,
): { state: WindowState; reply: LimitReply } {
  const w = current(prev, now, spanMs);
  if (w.lockedUntil > now) {
    return { state: w, reply: { ok: false, retryAfterMs: w.lockedUntil - now, remaining: 0, first: false } };
  }
  if (lockoutMs > 0 && w.n + 1 >= limit) {
    w.n++;
    w.logged = true;
    w.lockedUntil = now + lockoutMs;
    w.end = Math.max(w.end, w.lockedUntil);
    return { state: w, reply: { ok: false, retryAfterMs: lockoutMs, remaining: 0, first: true } };
  }
  if (w.n >= limit) {
    const first = !w.logged;
    w.logged = true;
    return { state: w, reply: { ok: false, retryAfterMs: w.start + spanMs - now, remaining: 0, first } };
  }
  w.n++;
  return { state: w, reply: allowed(limit - w.n) };
}
