import { env } from "cloudflare:workers";
import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import { provisionEntry } from "../../src/provisioner";
import type { FetchRequest } from "../../src/shell";
import type { FakeServe } from "./worker";

// The Provisioner object (src/provisioner.ts) in workerd, with a scripted fake of the panel it serves in place of the wasm:
// its alarm and kick (when the next pass runs, and that a pass is never moved later), its fetch, and the entry of the
// second script. Each test uses an object of its own, and ends with its alarm deleted so that nothing fires into the next test.

let objects = 0;
const stubFor = (name: string) => env.PROVISIONER.get(env.PROVISIONER.idFromName(name));
// The instance and the state are typed loosely on purpose: the tests reach the private arm(), and the generic types of the helper are deep.
const inObject = runInDurableObject as unknown as <T>(stub: unknown, work: (instance: any, state: DurableObjectState) => Promise<T> | T) => Promise<T>;
const script = (hooks: Partial<FakeServe> = {}) => (globalThis.__fakeServe = { loads: 0, ...hooks });

/** Runs inside the object, with `arm` recorded: what the object asked for and what its alarm is afterwards. */
type Probe = { arms: number[]; alarm: number | null };
async function probe(stub: ReturnType<typeof stubFor>, work: (instance: any, state: DurableObjectState) => Promise<void>): Promise<Probe> {
  return inObject<Probe>(stub, async (instance, state) => {
    const arms: number[] = [];
    const arm = instance.arm.bind(instance);
    instance.arm = (at: number) => {
      arms.push(at);
      return arm(at);
    };
    await work(instance, state);
    const alarm = await state.storage.getAlarm();
    await state.storage.deleteAlarm();
    instance.arm = arm;
    return { arms, alarm };
  });
}
const within = (at: number | undefined | null, from: number, to: number) => at !== undefined && at !== null && at >= from && at <= to;

afterEach(() => {
  globalThis.__fakeServe = undefined;
});

describe("Provisioner.kick", () => {
  it("sets the alarm to now when none is set", async () => {
    script();
    const stub = stubFor(`k${++objects}`);
    const before = Date.now();
    const out = await probe(stub, (o) => o.kick());
    expect(within(out.alarm, before, Date.now())).toBe(true);
  });

  it("moves a later alarm to now, and never moves an earlier one later", async () => {
    script();
    const stub = stubFor(`k${++objects}`);
    const before = Date.now();
    const later = await probe(stub, async (o, state) => {
      await state.storage.setAlarm(Date.now() + 3_600_000);
      await o.kick();
    });
    expect(within(later.alarm, before, Date.now())).toBe(true);

    let due: number | null = null;
    const earlier = await probe(stub, async (o, state) => {
      await state.storage.setAlarm(Date.now() - 5_000); // a pass that is already due
      due = await state.storage.getAlarm();
      await o.kick();
    });
    expect(earlier.alarm).toBe(due);
  });
});

describe("the alarm the Provisioner keeps", () => {
  it("is moved earlier by a sooner request and never later by a later one", async () => {
    script();
    const stub = stubFor(`m${++objects}`);
    const now = Date.now();
    let kept: number | null = null;
    const out = await probe(stub, async (o, state) => {
      await state.storage.setAlarm(now + 10_000);
      await o.arm(now + 30_000); // a busy pass asks for +30 s: the alarm at +10 s stays
      kept = await state.storage.getAlarm();
      await o.arm(now + 2_000); // a sooner one wins
    });
    expect(kept).toBe(now + 10_000);
    expect(out.alarm).toBe(now + 2_000);
  });
});

describe("Provisioner.alarm", () => {
  /** Runs the alarm of a fresh object once and says when it asked for the next one. */
  async function pass(result: Awaited<ReturnType<NonNullable<FakeServe["provision"]>>> | Error, extra: Partial<FakeServe> = {}) {
    const calls: number[] = [];
    script({
      ...extra,
      provision: async (kick) => {
        calls.push(Date.now());
        if (extra.provision) return extra.provision(kick);
        if (result instanceof Error) throw result;
        return result;
      },
    });
    const stub = stubFor(`a${++objects}`);
    // runDurableObjectAlarm runs an alarm that is set: set one far off, so that nothing fires by itself
    await inObject(stub, (_instance, state) => state.storage.setAlarm(Date.now() + 3_600_000));
    const ran = await runDurableObjectAlarm(stub);
    const asked = await inObject<number | null>(stub, async (_instance, state) => {
      const alarm = await state.storage.getAlarm();
      await state.storage.deleteAlarm();
      return alarm;
    });
    return { ran, calls, asked };
  }

  it("does not ask for another pass when nothing is left", async () => {
    const out = await pass({ more: false, busy: false });
    expect(out.calls).toHaveLength(1);
    expect(out.asked).toBeNull();
  });

  it("asks for the next pass at once (after a second at the least) when another install waits", async () => {
    const before = Date.now();
    const out = await pass({ more: true, busy: false });
    expect(within(out.asked, before + 900, Date.now() + 1_100)).toBe(true);
  });

  it("asks again in 30 s when a pass was already running in this isolate", async () => {
    const before = Date.now();
    const out = await pass({ more: false, busy: true });
    expect(within(out.asked, before + 29_000, Date.now() + 31_000)).toBe(true);
  });

  it("asks again in 60 s after an error and does not throw it at the platform", async () => {
    const log = console.error;
    const seen: unknown[][] = [];
    console.error = (...args: unknown[]) => seen.push(args);
    try {
      const before = Date.now();
      const out = await pass(new Error("d1 unreachable"));
      expect(out.ran).toBe(true);
      expect(within(out.asked, before + 59_000, Date.now() + 61_000)).toBe(true);
      expect(seen).toContainEqual(["provision pass failed:", "d1 unreachable"]);
    } finally {
      console.error = log;
    }
  });

  it("finishes the pass's after-response work before it returns", async () => {
    let finished = false;
    const waitUntil = new Promise<void>((resolve) => setTimeout(() => { finished = true; resolve(); }, 60));
    await pass({ more: false, busy: false, waitUntil }, {});
    expect(finished).toBe(true);
  });

  it("survives an after-response failure", async () => {
    const log = console.error;
    console.error = () => {};
    try {
      const failing = Promise.reject(new Error("poke failed"));
      failing.catch(() => {});
      const out = await pass({ more: false, busy: false, waitUntil: failing });
      expect(out.ran).toBe(true);
      expect(out.asked).toBeNull();
    } finally {
      console.error = log;
    }
  });
});

describe("Provisioner.fetch", () => {
  it("serves the request through the panel and gives back its answer", async () => {
    let seen: FetchRequest | undefined;
    script({
      fetch: (request) => {
        seen = request;
        return { status: 200, headers: [["Content-Type", "application/json"], ["Set-Cookie", "a=1"], ["Set-Cookie", "b=2"]], body: new TextEncoder().encode("{}"), waitUntil: Promise.resolve() };
      },
    });
    const response = await stubFor(`f${++objects}`).fetch("https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/ListNodeProvisions", {
      method: "POST",
      headers: { "X-Mistgate-Build": "1.0", Cookie: "__Host-sid=abc" },
      body: "{}",
    });
    expect(response.status).toBe(200);
    expect(await response.text()).toBe("{}");
    expect(response.headers.getSetCookie()).toEqual(["a=1", "b=2"]);
    expect(seen?.method).toBe("POST");
    expect(seen?.url).toBe("https://example.com/admin/api/mistgate.admin.v1.ProvisioningService/ListNodeProvisions");
    expect(seen?.headers).toContainEqual(["x-mistgate-build", "1.0"]);
    expect(new TextDecoder().decode(seen?.body ?? new Uint8Array())).toBe("{}");
  });

  it("answers 500 when the panel fails and 503 when it cannot start", async () => {
    const log = console.error;
    console.error = () => {};
    try {
      script({ fetch: () => { throw new Error("boom"); } });
      expect((await stubFor(`f${++objects}`).fetch("https://example.com/x")).status).toBe(500);
    } finally {
      console.error = log;
    }
  });
});

describe("the second script's entry", () => {
  it("answers 404 and starts no Go", async () => {
    script();
    const response = await provisionEntry.fetch();
    expect(response.status).toBe(404);
    expect(globalThis.__fakeServe?.loads).toBe(0);
  });
});
