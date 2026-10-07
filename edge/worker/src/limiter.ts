import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env";
import { type Bucket, type LimitReply, type WindowState, parseRequest, peekWindow, recordWindow, takeBucket } from "./limitmath";

// One Durable Object per (name, key) pair (see routeLimit in shell.ts): it holds the token bucket and/or the failure
// window of that one pair, which is all internal/panel/securitylimit.Memory keeps per key. The Worker isolates share no
// memory, so this object is where "10 wrong passwords from one address" is counted across all of them.
//
// State is two optional records in the object's SQLite-backed storage: "b" (bucket) and "w" (window). A request runs
// read-modify-write on the synchronous KV API with no await in between, so it is atomic. Idle state costs nothing: an
// alarm is set for the moment the bucket is full again / the window has ended (the state is then identical to none) and
// deletes it, after which the object holds no data at all.

/** An alarm further out than this is set to this and re-armed when it fires (setAlarm wants a sane timestamp). */
const MAX_ALARM_MS = 30 * 24 * 3600 * 1000;

export class Limiter extends DurableObject<Env> {
  /** The only RPC method: the four operations of cmd/mistgate-edge/limiter_js.go. A bad request throws (Go refuses the guarded request). */
  async limit(raw: unknown): Promise<LimitReply> {
    const req = parseRequest(raw);
    const now = Date.now(); // this object's clock is authoritative
    const kv = this.ctx.storage.kv;
    let reply: LimitReply;
    switch (req.operation) {
      case "take": {
        const r = takeBucket(kv.get<Bucket>("b"), now, req.burst, req.refillMs);
        kv.put("b", r.state);
        reply = r.reply;
        break;
      }
      case "peek":
        // Reads only; nothing to re-arm. A Durable Object owns only one (name, key) pair.
        return peekWindow(kv.get<WindowState>("w"), now, req.limit, req.spanMs);
      case "record": {
        const r = recordWindow(kv.get<WindowState>("w"), now, req.limit, req.spanMs, req.lockoutMs);
        kv.put("w", r.state);
        reply = r.reply;
        break;
      }
      case "reset": // Memory.Reset drops the failure window only; a token bucket of the same key stays
        kv.delete("w");
        reply = { ok: true, retryAfterMs: 0, remaining: 0, first: false };
        break;
    }
    await this.arm(now);
    return reply;
  }

  /** Deletes what has run its course, then waits for the next record to do so. */
  async alarm(): Promise<void> {
    const now = Date.now();
    const kv = this.ctx.storage.kv;
    if ((kv.get<Bucket>("b")?.fullAt ?? Infinity) <= now) kv.delete("b");
    if ((kv.get<WindowState>("w")?.end ?? Infinity) <= now) kv.delete("w");
    await this.arm(now);
  }

  private async arm(now: number): Promise<void> {
    const kv = this.ctx.storage.kv;
    const at = Math.min(kv.get<Bucket>("b")?.fullAt ?? Infinity, kv.get<WindowState>("w")?.end ?? Infinity);
    if (at === Infinity) await this.ctx.storage.deleteAlarm();
    else await this.ctx.storage.setAlarm(Math.min(at, now + MAX_ALARM_MS));
  }
}
