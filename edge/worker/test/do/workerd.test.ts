import { env, exports } from "cloudflare:workers";
import { runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import type { LinkStepIn } from "../../src/panellink";
import { askNode } from "../../src/shell";
import { type FakePanel, defaultPanel } from "./worker";

// What the Go NodeLink emulator (internal/node/agent/nodelink_emu_test.go) assumed about the platform, checked against
// the real workerd (design/cloudflare-edition/AGENT-LINK.md §6.6 and §7.5). Each test asserts what workerd does and says
// so in a comment next to the assertion. The workerd here is the one that ships with @cloudflare/vitest-pool-workers,
// compatibility_date 2026-08-01 (test/do/wrangler.test.toml): re-run these tests when either moves.

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const text = (s: string) => new TextEncoder().encode(s);
const str = (b: ArrayBuffer | Uint8Array | string) => (typeof b === "string" ? b : new TextDecoder().decode(b));

// Isolation: a late closed step of an earlier test's object can land on this test's fake, so only the nodes of the
// running test count (as in nodelink.test.ts), and afterEach closes what the test opened.
let nodes = 0;
let mine = new Set<string>();
const newNode = () => {
  const id = `nod_wd${++nodes}`;
  mine.add(id);
  return id;
};
const stubFor = (nodeId: string) => env.NODELINK.get(env.NODELINK.idFromName(nodeId));
function script(hooks: Partial<FakePanel> = {}): FakePanel {
  return (globalThis.__fakePanel = { ...defaultPanel(), ...hooks });
}
const steps = (fake: FakePanel) => fake.calls.filter((c) => c.op === "step" && mine.has((c.args as LinkStepIn).nodeId));
const kinds = (fake: FakePanel) => steps(fake).map((c) => (c.args as LinkStepIn).event.kind);
/** The events of the step calls, in order, as `kind` or `frame:<text>`. */
const seen = (fake: FakePanel) =>
  steps(fake).map((s) => {
    const e = (s.args as LinkStepIn).event;
    return e.kind === "frame" ? `frame:${str(e.frame)}` : e.kind;
  });

class Client {
  inbox: string[] = [];
  closed: { code: number; reason: string } | undefined;
  constructor(readonly ws: WebSocket, readonly answers = true) {
    ws.binaryType = "arraybuffer";
    ws.accept();
    ws.addEventListener("message", (e) => {
      this.inbox.push(str(e.data as string | ArrayBuffer));
    });
    ws.addEventListener("close", (e) => {
      this.closed = { code: e.code, reason: e.reason };
      if (!answers) return;
      try {
        ws.close(1000);
      } catch {
        // already closed
      }
    });
  }
  send(s: string) {
    this.ws.send(text(s));
  }
  async until(cond: () => boolean, what = "condition") {
    for (let i = 0; i < 300 && !cond(); i++) await sleep(10);
    expect(cond(), `timed out waiting for ${what}`).toBe(true);
  }
}

const open: Client[] = [];
afterEach(async () => {
  for (const c of open.splice(0)) {
    try {
      c.ws.close(1000);
    } catch {
      // already closed
    }
  }
  await sleep(100); // the objects see these closes asynchronously and run a closed step each: let them land on this test's fake
  globalThis.__fakePanel = undefined;
  mine = new Set();
});

async function connect(nodeId: string, answers = true): Promise<Client> {
  const res = await stubFor(nodeId).fetch(`https://link.test/p/link/${nodeId}`, { headers: { Upgrade: "websocket" } });
  expect(res.status).toBe(101);
  const c = new Client(res.webSocket!, answers);
  open.push(c);
  await c.until(() => c.inbox.length >= 1, "the challenge");
  return c;
}

/** A connected, authenticated socket whose open step has finished. */
async function login(nodeId: string, answers = true): Promise<Client> {
  const c = await connect(nodeId, answers);
  c.send("auth");
  await c.until(() => c.inbox.length >= 2, "the accept frame");
  const opened = (x: FakePanel["calls"][number]) => x.op === "step" && (x.args as LinkStepIn).nodeId === nodeId && (x.args as LinkStepIn).event.kind === "open" && x.end !== undefined;
  await c.until(() => globalThis.__fakePanel?.calls.some(opened) === true, "the open step");
  return c;
}

const alarmOf = (nodeId: string) => runInDurableObject(stubFor(nodeId), (_o, state) => state.storage.getAlarm());
const keysOf = (nodeId: string) => runInDurableObject(stubFor(nodeId), (_o, state) => [...state.storage.kv.list()].map(([key]) => key));

type Heard = { event: "message" | "close"; readyState: number; arg: string | number };
/**
 * Wraps the object's webSocketMessage and webSocketClose (as workerd calls them, on the instance) to record the readyState
 * the socket has when each event is delivered. The wrapper still calls the real handler. Read with heard().
 */
function spy(nodeId: string) {
  return runInDurableObject(stubFor(nodeId), (instance) => {
    const obj = instance as any;
    const log: Heard[] = [];
    (globalThis as any)[`heard_${nodeId}`] = log;
    const message = obj.webSocketMessage;
    obj.webSocketMessage = function (ws: WebSocket, data: string | ArrayBuffer) {
      log.push({ event: "message", readyState: ws.readyState, arg: str(data) });
      return message.call(this, ws, data);
    };
    const close = obj.webSocketClose;
    obj.webSocketClose = function (ws: WebSocket, code: number, ...rest: unknown[]) {
      log.push({ event: "close", readyState: ws.readyState, arg: code });
      return close.call(this, ws, code, ...rest);
    };
  });
}
const heard = (nodeId: string) => runInDurableObject(stubFor(nodeId), () => (globalThis as any)[`heard_${nodeId}`] as Heard[]);

const OPEN = 1;
const CLOSING = 2;
const CLOSED = 3;

describe("workerd: sockets", () => {
  it("1. our own close() makes readyState CLOSING at once, in the same turn", async () => {
    script();
    const id = newNode();
    await login(id);
    const r = await runInDurableObject(stubFor(id), (_o, state) => {
      const ws = state.getWebSockets()[0]!;
      const before = ws.readyState;
      ws.close(4000, "mine");
      return { before, after: ws.readyState };
    });
    // The emulator assumed "not open at once": right. openSockets()/liveSocket() already skip the socket in the same event.
    expect(r).toEqual({ before: OPEN, after: CLOSING });
  });

  it("2. frames the agent sent before its close are delivered with the socket already CLOSING, and are stepped", async () => {
    // The first step is slowed 100 ms so that b and the close frame are already queued behind it.
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame" && str(input.event.frame) === "a") await sleep(100);
        return defaultPanel().step(input);
      },
    });
    const id = newNode();
    const c = await login(id);
    await spy(id);
    c.send("a");
    c.send("b");
    c.ws.close(1000, "bye");
    await c.until(() => c.closed !== undefined, "the close");
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    // The emulator assumed the frames are stepped, and nodelink.ts gated on readyState, which would have dropped both: workerd
    // delivers a message after the peer's close frame has arrived with readyState CLOSING (not even the first one sees OPEN
    // when the agent sends and closes in one turn). Hence att.shut, set only by our own close, instead of the readyState gate.
    expect(await heard(id)).toEqual([
      { event: "message", readyState: CLOSING, arg: "a" },
      { event: "message", readyState: CLOSING, arg: "b" },
      { event: "close", readyState: CLOSING, arg: 1000 },
    ]);
    expect(seen(fake)).toEqual(["open", "frame:a", "frame:b", "closed"]);
    expect((steps(fake).at(-1)?.args as LinkStepIn).event).toMatchObject({ kind: "closed", owned: true });
  });

  it("2b. so an admin reply the agent sent just before it closed still resolves the ask", async () => {
    script({
      step: ({ event }) => {
        const said = event.kind === "frame" ? str(event.frame) : "";
        if (event.kind === "request") return { state: "r", frames: [event.frame] };
        return { state: event.kind, frames: [], replies: said.startsWith("answer:") ? [{ requestId: said.slice(7), frame: text("done") }] : undefined };
      },
    });
    const id = newNode();
    const c = await login(id);
    const asked = stubFor(id).ask("r-last", text("who"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("who"), "the request frame");
    c.send("answer:r-last");
    c.ws.close(1000, "bye");
    expect(str((await asked)!)).toBe("done"); // not "link lost"
  });

  for (const answers of [true, false]) {
    it(`3. webSocketClose arrives after our own close(4000) with our code and readyState CLOSED (the agent ${answers ? "answers" : "does not answer"})`, async () => {
      const fake = script();
      const id = newNode();
      const c = await login(id, answers);
      await spy(id);
      await stubFor(id).close(4000, "mine");
      await c.until(() => c.closed !== undefined, "the client's close");
      await sleep(150);
      // The emulator assumed it is delivered when the peer answers, with the peer's code. On workerd it is delivered with
      // the code we sent (4000, even though the test client answers 1000) and the socket already CLOSED, whether or not the
      // client called close() itself. The echo in webSocketClose then closes a CLOSED socket, which does not throw: a no-op.
      expect(c.closed).toEqual({ code: 4000, reason: "mine" });
      expect(await heard(id)).toEqual([{ event: "close", readyState: CLOSED, arg: 4000 }]);
      // dropLive had already ended the session, so the close event runs no second closed step.
      expect(kinds(fake)).toEqual(["open", "closed"]);
    });
  }

  it("3b. the agent's own close leaves the socket CLOSING until we answer it, and a missing code (1005) is answered 1000", async () => {
    const fake = script();
    const id = newNode();
    const c = await login(id);
    await spy(id);
    c.ws.close(); // no code: the object hears 1005
    await c.until(() => c.closed !== undefined, "the answer");
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    // Needed echo: workerd does not answer a close frame by itself while a webSocketClose handler exists (the handler sees CLOSING).
    expect(await heard(id)).toEqual([{ event: "close", readyState: CLOSING, arg: 1005 }]);
    expect(c.closed?.code).toBe(1000);
  });

  // The codes ws.close() refuses on workerd. The code is checked after the length of the reason, but both throw and leave the socket OPEN.
  const refused = new Set([999, 1004, 1005, 1006, 1015, 5000]);
  const codes = [1000, 1001, 1003, 1008, 1011, 1014, 2999, 3000, 4000, 4999, 999, 1004, 1005, 1006, 1015, 5000];
  it("5. ws.close() throws for 999, 1004, 1005, 1006, 1015, 5000 and a reason over 123 bytes, and leaves the socket open; others (1008, 1011) are sent", async () => {
    const results: Record<string, string> = {};
    const expected: Record<string, string> = {};
    for (const code of codes) {
      script();
      const id = newNode();
      const c = await connect(id);
      const threw = await runInDurableObject(stubFor(id), (_o, state) => {
        const ws = state.getWebSockets()[0]!;
        try {
          ws.close(code, "r");
          return false;
        } catch (error) {
          return (error as Error).message.startsWith("Invalid WebSocket close code") && ws.readyState === OPEN;
        }
      });
      if (!refused.has(code)) await c.until(() => c.closed !== undefined, `the close ${code}`);
      results[code] = threw ? "throws, still open" : `sent ${c.closed?.code}`;
      expected[code] = refused.has(code) ? "throws, still open" : `sent ${code}`;
    }
    // The emulator threw for 1005, 1006, 1015 only: 1004 was the one it missed (the others are the range ends).
    expect(results).toEqual(expected);

    script();
    const id = newNode();
    const c = await connect(id);
    const long = await runInDurableObject(stubFor(id), (_o, state) => {
      const ws = state.getWebSockets()[0]!;
      const attempt = (reason: string) => {
        try {
          ws.close(4000, reason);
          return "sent";
        } catch (error) {
          return (error as Error).message;
        }
      };
      return { at124: attempt("x".repeat(124)), stillOpen: ws.readyState === OPEN, at123: attempt("x".repeat(123)) };
    });
    expect(long.at124).toMatch(/must not be longer than 123 bytes/);
    expect(long.stillOpen).toBe(true);
    expect(long.at123).toBe("sent");
    await c.until(() => c.closed !== undefined, "the 123-byte close");
    expect(c.closed?.reason).toBe("x".repeat(123));
  });

  it("5b. so a close Go asks for with a code workerd refuses goes out as 1000 instead of leaving the socket open", async () => {
    for (const code of [1004, 5000]) {
      const fake = script({ step: (input) => (input.event.kind === "frame" ? { state: "s", frames: [], close: { code, reason: "done" } } : defaultPanel().step(input)) });
      const c = await login(newNode());
      c.send("x");
      await c.until(() => c.closed !== undefined, `the close ${code}`);
      expect(c.closed).toEqual({ code: 1000, reason: "done" });
      await c.until(() => kinds(fake).includes("closed"), "the closed step");
    }
  });
});

describe("workerd: reset, alarm, storage", () => {
  it("4. an ask pending when the object is reset rejects at once with the reset's own message, which askNode maps to lost", async () => {
    script();
    const id = newNode();
    const c = await login(id);
    const direct = stubFor(id).ask("r-reset", text("q"), Date.now() + 5_000);
    const mapped = askNode(env.NODELINK, id, "r-reset2", text("q2"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("q") && c.inbox.includes("q2"), "both request frames");
    const startedAt = Date.now();
    await expect(runInDurableObject(stubFor(id), (_o, state) => state.abort("reset by the test"))).rejects.toThrow("reset by the test");
    // The emulator rejected with "link lost". workerd rejects the RPC with an Error carrying the reset's reason (for a real
    // reset a runtime message, never "timeout"), well before the ask's own deadline. askNode maps any message but
    // "timeout" to "lost", so Go sees a lost link and no change is needed.
    const error = await direct.catch((e: Error) => e);
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toBe("reset by the test");
    expect(await mapped).toEqual({ error: "lost" });
    expect(Date.now() - startedAt).toBeLessThan(2_000);
  });

  it("6. an alarm step runs only when its alarm is due, and an alarm set by the alarm step itself is kept", async () => {
    const marks = { open: 0, alarms: [] as number[], wanted: [] as number[] };
    const fake = script({
      step: ({ event }) => {
        let alarmAt: number | undefined;
        if (event.kind === "open") marks.open = Date.now();
        if (event.kind === "open" || (event.kind === "alarm" && marks.alarms.push(Date.now()) < 2)) marks.wanted.push((alarmAt = Date.now() + 1_200));
        return { state: event.kind, frames: [], alarmAt };
      },
    });
    const id = newNode();
    const c = await login(id);
    // (a) 1.2 s away: nothing runs earlier (the 1 s minimum gap is not the reason: 1.2 s is above it).
    expect(await alarmOf(id)).toBeGreaterThan(Date.now() + 100);
    await sleep(1_000);
    expect(kinds(fake)).toEqual(["open"]);
    await c.until(() => kinds(fake).filter((k) => k === "alarm").length === 1, "the first alarm step");
    expect(marks.alarms[0]! - marks.wanted[0]!).toBeGreaterThanOrEqual(-5); // never early; observed about +10 ms
    // (b) the alarm step asked for another one: it is armed when the handler returns and fires again, 1.2 s later.
    expect(await alarmOf(id)).toBeGreaterThan(Date.now());
    await c.until(() => kinds(fake).filter((k) => k === "alarm").length === 2, "the second alarm step");
    expect(marks.alarms[1]! - marks.wanted[1]!).toBeGreaterThanOrEqual(-5);
    // The second alarm step asked for none, so nothing is armed and nothing fires again.
    await sleep(100);
    expect(await alarmOf(id)).toBeNull();
    await sleep(1_300);
    expect(kinds(fake)).toEqual(["open", "alarm", "alarm"]);
  });

  it("7a. deleteAll() removes the alarm too, so a forgetting object must arm() again from what is left", async () => {
    const r = await runInDurableObject(stubFor(newNode()), async (_o, state) => {
      state.storage.kv.put("x", 1);
      await state.storage.setAlarm(Date.now() + 60_000);
      await state.storage.deleteAll();
      return { alarm: await state.storage.getAlarm(), keys: [...state.storage.kv.list()].length };
    });
    // The emulator assumed deleteAll leaves the old alarm (arm() overwrites it); workerd deletes it. Either way arm() decides, no change.
    expect(r).toEqual({ alarm: null, keys: 0 });
  });

  it("7b. a closed step that returns forget while another socket is mid-handshake leaves no keys and the handshake deadline as the alarm", async () => {
    const fake = script({
      step: (input) => (input.event.kind === "closed" ? { state: "closed", frames: [], forget: true } : { state: "s", frames: [], alarmAt: Date.now() + 60_000 }),
    });
    const id = newNode();
    const live = await login(id);
    expect(await alarmOf(id)).toBeGreaterThan(Date.now() + 50_000); // the session's alarm, a minute away
    const handshaking = await connect(id);
    const deadline = await runInDurableObject(stubFor(id), (_o, state) => {
      const pending = state.getWebSockets().find((ws) => (ws.deserializeAttachment() as { generation: number }).generation === 0);
      return (pending!.deserializeAttachment() as { deadline: number }).deadline;
    });
    await stubFor(id).close(4000, "retired");
    await live.until(() => live.closed !== undefined && kinds(fake).includes("closed"), "the retire");
    expect(await keysOf(id)).toEqual([]);
    expect(await alarmOf(id)).toBe(deadline); // not the session's alarm a minute away, and not gone
    expect(handshaking.closed).toBeUndefined();
  });

  it("7c. with no other socket, forget leaves no alarm at all", async () => {
    const fake = script({ step: (input) => (input.event.kind === "closed" ? { state: "closed", frames: [], forget: true } : { state: "s", frames: [], alarmAt: Date.now() + 60_000 }) });
    const id = newNode();
    const c = await login(id);
    await stubFor(id).close(4000, "retired");
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    expect(await keysOf(id)).toEqual([]);
    expect(await alarmOf(id)).toBeNull();
  });
});

describe("workerd: the fan-out and shared module state", () => {
  it("8. a step that hands poke() of its own node to waitUntil does not deadlock: the poke runs after the block as one desired step", async () => {
    let poked: Promise<void> | undefined;
    let pokeDone = 0;
    const fake = script({
      step: (input, ctx) => {
        if (input.event.kind === "frame") {
          expect(ctx).toBeDefined();
          // Never awaited inside the step (the poke RPC queues behind the block this very step belongs to).
          poked = stubFor(input.nodeId).poke().then(() => void (pokeDone = Date.now()));
          ctx!.waitUntil(poked);
        }
        return defaultPanel().step(input);
      },
    });
    const id = newNode();
    const c = await login(id);
    c.send("a");
    await c.until(() => kinds(fake).includes("desired"), "the desired step");
    await sleep(200);
    // As the emulator assumed: one desired step after the frame step, no 1011, the session still open.
    expect(seen(fake)).toEqual(["open", "frame:a", "desired"]);
    expect(c.closed).toBeUndefined();
    const [, frame, desired] = steps(fake);
    expect(desired!.start).toBeGreaterThanOrEqual(frame!.end!);
    expect(pokeDone).toBeGreaterThanOrEqual(frame!.end!); // the poke RPC could not have run inside the block
    await poked;
  });

  it("9. a module-scope counter is shared by the objects and PanelLink (local workerd: one isolate)", async () => {
    script();
    const panelLink = (exports as unknown as { PanelLink: { moduleCounter(): Promise<number> } }).PanelLink;
    const bump = (nodeId: string) => (stubFor(nodeId) as unknown as { bumpModuleCounter(): Promise<number> }).bumpModuleCounter();
    const before = await panelLink.moduleCounter();
    const a = newNode();
    const b = newNode();
    const last = [await bump(a), await bump(a), await bump(b)].at(-1);
    // Two different objects and PanelLink see one variable here. This is the local answer only: production isolates are
    // placed by Cloudflare and nothing may rely on it (a cache would at best be per isolate); 6g measures the real thing.
    expect(last).toBe(before + 3);
    expect(await panelLink.moduleCounter()).toBe(before + 3);
  });
});
