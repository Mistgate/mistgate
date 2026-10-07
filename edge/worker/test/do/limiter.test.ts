import { env } from "cloudflare:workers";
import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { describe, expect, it } from "vitest";

// The Limiter class itself, in workerd with real SQLite-backed storage: RPC, per-key isolation, validation, alarm cleanup.

const stubFor = (name: string, key: string) => env.LIMITER.get(env.LIMITER.idFromName(`${name}\u0000${key}`));
const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

describe("Limiter Durable Object", () => {
  it("allows the burst of a token bucket over RPC, then refuses; another key has its own bucket", async () => {
    const a = stubFor("auth", "do-bucket-a");
    const take = { operation: "take", name: "auth", key: "do-bucket-a", burst: 3, refillMs: 3_600_000 };
    for (let i = 0; i < 3; i++) expect((await a.limit(take)).ok, `burst request ${i}`).toBe(true);
    const refused = await a.limit(take);
    expect(refused.ok).toBe(false);
    expect(refused.first).toBe(false);
    expect(refused.retryAfterMs).toBeGreaterThan(3_599_000);
    expect((await stubFor("auth", "do-bucket-b").limit({ ...take, key: "do-bucket-b" })).ok).toBe(true);
  });

  it("counts failures in a window: the lockout is reported once, peek agrees, reset clears it", async () => {
    const s = stubFor("page-password", "t:do-window");
    const base = { name: "page-password", key: "t:do-window", limit: 3, spanMs: 600_000, lockoutMs: 0 };
    expect(await s.limit({ ...base, operation: "peek" })).toEqual({ ok: true, retryAfterMs: 0, remaining: 3, first: false });
    for (let i = 0; i < 3; i++) expect(await s.limit({ ...base, operation: "record" })).toMatchObject({ ok: true, remaining: 2 - i });
    const locked = await s.limit({ ...base, operation: "record" });
    expect(locked).toMatchObject({ ok: false, remaining: 0, first: true });
    expect(locked.retryAfterMs).toBeGreaterThan(599_000);
    expect(await s.limit({ ...base, operation: "record" })).toMatchObject({ ok: false, first: false });
    expect(await s.limit({ ...base, operation: "peek" })).toMatchObject({ ok: false, first: false });
    expect(await s.limit({ ...base, operation: "reset" })).toMatchObject({ ok: true });
    expect(await s.limit({ ...base, operation: "peek" })).toMatchObject({ ok: true, remaining: 3 });
  });

  it("records the threshold and starts its lockout in one RPC", async () => {
    const s = stubFor("subscription-miss", "client-do-lockout");
    const base = { name: "subscription-miss", key: "client-do-lockout", limit: 3, spanMs: 60_000, lockoutMs: 900_000 };
    expect(await s.limit({ ...base, operation: "record" })).toMatchObject({ ok: true, remaining: 2 });
    expect(await s.limit({ ...base, operation: "record" })).toMatchObject({ ok: true, remaining: 1 });
    const first = await s.limit({ ...base, operation: "record" });
    expect(first).toMatchObject({ ok: false, retryAfterMs: 900_000, first: true });
    expect(await s.limit({ ...base, operation: "peek" })).toMatchObject({ ok: false, first: false });
  });

  it("rejects a malformed request (the panel then refuses the guarded request)", async () => {
    const s = stubFor("auth", "do-bad");
    await expect(s.limit({ operation: "drop", name: "auth", key: "do-bad" })).rejects.toThrow(/unknown operation/);
    await expect(s.limit({ operation: "take", name: "auth", key: "do-bad", burst: 0, refillMs: 1 })).rejects.toThrow(/invalid burst/);
    await expect(s.limit(null)).rejects.toThrow();
  });

  it("deletes its state and alarm once a window has ended", async () => {
    const s = stubFor("page-password", "t:do-alarm");
    await s.limit({ operation: "record", name: "page-password", key: "t:do-alarm", limit: 5, spanMs: 2_000, lockoutMs: 0 }); // the first check must come well before the alarm
    expect(await runInDurableObject(s, async (_o, state) => [state.storage.kv.get("w") !== undefined, (await state.storage.getAlarm()) !== null])).toEqual([true, true]);
    // The alarm fires by itself once the window is over; nothing is left behind (poll: its timing is the runtime's).
    const left = () => runInDurableObject(s, async (_o, state) => [state.storage.kv.get("w") !== undefined, (await state.storage.getAlarm()) !== null]);
    for (let i = 0; i < 300 && (await left()).includes(true); i++) await sleep(20);
    expect(await left()).toEqual([false, false]);
  });

  it("deletes a bucket once it is full again, and keeps it before that", async () => {
    const s = stubFor("auth", "do-alarm-bucket");
    const held = () => runInDurableObject(s, async (_o, state) => [state.storage.kv.get("b") !== undefined, (await state.storage.getAlarm()) !== null]);
    await s.limit({ operation: "take", name: "auth", key: "do-alarm-bucket", burst: 2, refillMs: 400 }); // full again after 400 ms
    await runDurableObjectAlarm(s); // far too early: it runs, finds the bucket not yet full, keeps it and sets the alarm again
    expect(await held()).toEqual([true, true]);
    for (let i = 0; i < 100 && (await held()).includes(true); i++) await sleep(20);
    expect(await held()).toEqual([false, false]);
  });
});
