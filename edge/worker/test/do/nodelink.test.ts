import { env, exports } from "cloudflare:workers";
import { runDurableObjectAlarm, runInDurableObject } from "cloudflare:test";
import { afterEach, describe, expect, it } from "vitest";
import type { LinkStepIn } from "../../src/panellink";
import { type FakePanel, defaultPanel } from "./worker";

// NodeLink in workerd with real SQLite-backed storage and hibernatable sockets. Go is a scripted fake PanelLink (worker.ts)
// that the object reaches through ctx.exports, as in production; the "agent" is a plain WebSocket client.

const sleep = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const text = (s: string) => new TextEncoder().encode(s);
const str = (b: ArrayBuffer | Uint8Array | string) => (typeof b === "string" ? b : new TextDecoder().decode(b));

let nodes = 0;
// The nodes of the running test. An earlier test's object can run its closed step after that test ended (afterEach
// closes the sockets) and land on this test's fake: only this test's nodes count.
let mine = new Set<string>();
const newNode = () => {
  const id = `nod_test${++nodes}`;
  mine.add(id);
  return id;
};
const stubFor = (nodeId: string) => env.NODELINK.get(env.NODELINK.idFromName(nodeId));

/** Installs a fake panel: the defaults with the given hooks replaced. */
function script(hooks: Partial<FakePanel> = {}): FakePanel {
  return (globalThis.__fakePanel = { ...defaultPanel(), ...hooks });
}
const steps = (fake: FakePanel) => fake.calls.filter((c) => c.op === "step" && mine.has((c.args as LinkStepIn).nodeId));
const kinds = (fake: FakePanel) => steps(fake).map((c) => (c.args as LinkStepIn).event.kind);

class Client {
  inbox: string[] = [];
  closed: { code: number; reason: string } | undefined;
  constructor(readonly ws: WebSocket) {
    ws.binaryType = "arraybuffer";
    ws.accept();
    ws.addEventListener("message", (e) => {
      this.inbox.push(str(e.data as string | ArrayBuffer));
    });
    ws.addEventListener("close", (e) => {
      this.closed = { code: e.code, reason: e.reason };
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
  const clients = open.splice(0);
  for (const c of clients) {
    try {
      c.ws.close(1000);
    } catch {
      // already closed
    }
  }
  // The objects see these closes asynchronously, and each runs a closed step: let them land on this test's fake.
  await sleep(100);
  globalThis.__fakePanel = undefined;
  mine = new Set();
});

async function connect(nodeId: string): Promise<Client> {
  const res = await stubFor(nodeId).fetch(`https://link.test/p/link/${nodeId}`, { headers: { Upgrade: "websocket" } });
  expect(res.status).toBe(101);
  const c = new Client(res.webSocket!);
  open.push(c);
  await c.until(() => c.inbox.length >= 1, "the challenge");
  return c;
}

/** A connected, authenticated socket: the inbox holds the challenge and the accept frame. */
async function login(nodeId: string): Promise<Client> {
  const c = await connect(nodeId);
  c.send("auth");
  await c.until(() => c.inbox.length >= 2, "the accept frame");
  // The accept frame goes out before the open step runs; the object handles nothing else until that step is done.
  const opened = (x: FakePanel["calls"][number]) => x.op === "step" && (x.args as LinkStepIn).nodeId === nodeId && (x.args as LinkStepIn).event.kind === "open" && x.end !== undefined;
  await c.until(() => globalThis.__fakePanel?.calls.some(opened) === true, "the open step");
  return c;
}

const alarmOf = (nodeId: string) => runInDurableObject(stubFor(nodeId), (_o, state) => state.storage.getAlarm());
const keysOf = (nodeId: string) => runInDurableObject(stubFor(nodeId), (_o, state) => [...state.storage.kv.list()].map(([key]) => key));
/** The events of the step calls, in order, as `kind` or `frame:<text>`. */
const seen = (fake: FakePanel) =>
  steps(fake).map((s) => {
    const e = (s.args as LinkStepIn).event;
    return e.kind === "frame" ? `frame:${str(e.frame)}` : e.kind;
  });

describe("NodeLink handshake", () => {
  it("sends the challenge from Go with the host as audience, then accept, then runs the open step", async () => {
    const fake = script();
    const id = newNode();
    const c = await login(id);
    expect(c.inbox).toEqual(["challenge link.test", "accept"]);
    expect(fake.calls.map((x) => x.op)).toEqual(["challenge", "accept", "step"]);
    expect(steps(fake)[0]?.args).toMatchObject({ nodeId: id, state: null, event: { kind: "open", certSerial: "c1", certNotAfterUnix: 2_000_000_000 } });
    expect(fake.calls[0]?.args).toEqual({ audience: "link.test" });
    expect(fake.calls[1]?.args).toMatchObject({ nodeId: id, audience: "link.test" }); // the node id is the object's name
  });

  it("completes the close handshake when the agent closes", async () => {
    const fake = script();
    const c = await login(newNode());
    c.ws.close(1000, "bye");
    await c.until(() => c.closed !== undefined, "the server's close");
    expect(c.closed?.code).toBe(1000);
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
  });

  it("refuses a LinkAuth Go rejects, a text frame, and a plain request with no upgrade", async () => {
    const fake = script();
    const c = await connect(newNode());
    c.send("bad auth");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1008);
    expect(kinds(fake)).toEqual([]);

    const t = await connect(newNode());
    t.ws.send("text, not a binary frame");
    await t.until(() => t.closed !== undefined, "the close");
    expect(t.closed?.code).toBe(1008);
    expect(fake.calls.filter((x) => x.op === "accept")).toHaveLength(1); // only the bad one reached Go

    const plain = await stubFor(newNode()).fetch("https://link.test/p/link/x");
    expect(plain.status).toBe(426);
  });

  it("closes a socket that never authenticates with 1008 when its deadline passes (alarm)", async () => {
    script();
    const id = newNode();
    const c = await connect(id);
    const alarm = await alarmOf(id);
    expect(alarm).toBeGreaterThan(Date.now() + 5_000);
    expect(alarm).toBeLessThan(Date.now() + 11_000);
    // Move the deadline into the past, as if the 10 s had gone by, and let the alarm run.
    await runInDurableObject(stubFor(id), (_o, state) => {
      for (const ws of state.getWebSockets()) ws.serializeAttachment({ ...(ws.deserializeAttachment() as object), deadline: Date.now() - 1 });
    });
    await runDurableObjectAlarm(stubFor(id));
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1008);
    expect(await alarmOf(id)).toBeNull();
    expect(await keysOf(id)).toEqual([]); // an upgrade that never authenticated leaves nothing behind
  });

  it("refuses a LinkAuth that arrives after the deadline without asking Go", async () => {
    const fake = script();
    const id = newNode();
    const c = await connect(id);
    await runInDurableObject(stubFor(id), (_o, state) => {
      for (const ws of state.getWebSockets()) ws.serializeAttachment({ ...(ws.deserializeAttachment() as object), deadline: Date.now() - 1 });
    });
    c.send("auth");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1008);
    expect(fake.calls.filter((x) => x.op === "accept")).toHaveLength(0);
  });
});

describe("NodeLink ordering and ownership", () => {
  it("runs frames one at a time in arrival order", async () => {
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame") await sleep(60);
        return defaultPanel().step(input);
      },
    });
    const c = await login(newNode());
    for (const f of ["a", "b", "c"]) c.send(f);
    await c.until(() => c.inbox.length >= 5, "three echoes");
    expect(c.inbox.slice(2)).toEqual(["a", "b", "c"]);
    const frames = steps(fake).filter((s) => (s.args as LinkStepIn).event.kind === "frame");
    expect(frames.map((s) => str(((s.args as LinkStepIn).event as { frame: Uint8Array }).frame))).toEqual(["a", "b", "c"]);
    for (let i = 1; i < frames.length; i++) expect(frames[i]!.start, `step ${i} started before step ${i - 1} ended`).toBeGreaterThanOrEqual(frames[i - 1]!.end!);
  });

  it("closes the first socket with 4000 when a second one authenticates, and drops the first one's late frames", async () => {
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame") await sleep(80);
        return { state: `${input.state ?? ""}${input.event.kind[0]}`, frames: [] };
      },
    });
    const id = newNode();
    const first = await login(id);
    const second = await connect(id);
    first.send("f1"); // being handled when the others arrive
    second.send("auth");
    first.send("late"); // arrives after the second socket's LinkAuth
    await second.until(() => second.inbox.length >= 2, "the second accept");
    await first.until(() => first.closed !== undefined, "the first close");
    expect(first.closed?.code).toBe(4000);
    await sleep(100);
    expect(kinds(fake)).toEqual(["open", "frame", "closed", "open"]);
    const [, , closed, reopened] = steps(fake).map((s) => s.args as LinkStepIn);
    expect(closed?.event).toMatchObject({ kind: "closed", owned: false });
    expect(reopened?.state).toBe("ofc"); // the state carries over to the new session
    second.send("g");
    await sleep(100);
    expect(kinds(fake).at(-1)).toBe("frame"); // the new socket's frames run
  });

  it("closes the socket with 1011 when a step fails, rejects pending asks, and still ends the session", async () => {
    const fake = script({
      step: (input) => {
        if (input.event.kind === "frame") throw new Error("boom");
        return defaultPanel().step(input);
      },
    });
    const id = newNode();
    const c = await login(id);
    const pending = stubFor(id).ask("r-fail", text("q"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("q"), "the request frame");
    c.send("x");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1011);
    await expect(pending).rejects.toThrow("link lost");
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    expect(await alarmOf(id)).toBeNull();
  });

  it("saves the state a step returned, sends its frames, and hands the state to the next step", async () => {
    const fake = script({
      step: ({ state, event }) => (event.kind === "frame" ? { state: `${state}|S`, frames: [text("F")] } : { state: "base", frames: [] }),
    });
    const id = newNode();
    const c = await login(id);
    c.send("x");
    await c.until(() => c.inbox.includes("F"), "the frame");
    expect(await runInDurableObject(stubFor(id), (_o, state) => state.storage.kv.get("state"))).toBe("base|S");
    c.send("y");
    await c.until(() => c.inbox.filter((m) => m === "F").length === 2, "the second frame");
    expect((steps(fake).at(-1)?.args as LinkStepIn).state).toBe("base|S");
  });
});

describe("NodeLink alarm", () => {
  it("is the earliest of the session's alarm, handshake deadlines and a poke", async () => {
    const at = (event: { kind: string }) => (event.kind === "open" ? Date.now() + 30_000 : Date.now() + 5_000);
    const fake = script({ step: ({ event }) => ({ state: event.kind, frames: [], alarmAt: at(event) }) });
    const id = newNode();
    const near = (alarm: number | null, expected: number) => expect(Math.abs((alarm ?? 0) - expected)).toBeLessThan(1_500);

    const c = await login(id);
    near(await alarmOf(id), Date.now() + 30_000); // only the session's alarm
    await connect(id); // a second socket that has not authenticated: deadline in 10 s
    near(await alarmOf(id), Date.now() + 10_000);
    c.send("x"); // from here every step wants an alarm in 5 s
    await c.until(() => kinds(fake).includes("frame"), "the frame step");
    await sleep(50);
    near(await alarmOf(id), Date.now() + 5_000);

    await stubFor(id).poke(); // "now": the alarm runs a desired step at once, then the earliest is back
    await c.until(() => kinds(fake).includes("desired"), "the desired step");
    await sleep(50);
    near(await alarmOf(id), Date.now() + 5_000);
  });

  it("merges pokes that wait behind a running step into one desired step", async () => {
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame") await sleep(150);
        return { state: input.event.kind, frames: [] };
      },
    });
    const id = newNode();
    const c = await login(id);
    c.send("slow");
    await sleep(30);
    await Promise.all([stubFor(id).poke(), stubFor(id).poke(), stubFor(id).poke()]);
    await c.until(() => kinds(fake).includes("desired"), "the desired step");
    await sleep(250);
    expect(kinds(fake).filter((k) => k === "desired")).toHaveLength(1);
  });

  it("runs an alarm step when the session's alarm is due, and leaves no alarm and no timer once idle", async () => {
    const timers = new Map<unknown, number>();
    const realSet = globalThis.setTimeout;
    const realClear = globalThis.clearTimeout;
    globalThis.setTimeout = ((fn: () => void, ms?: number) => {
      const id = realSet(() => (timers.delete(id), fn()), ms);
      timers.set(id, ms ?? 0);
      return id;
    }) as typeof setTimeout;
    globalThis.clearTimeout = ((id: unknown) => (timers.delete(id), realClear(id as number))) as typeof clearTimeout;
    const longTimers = () => [...timers.values()].filter((ms) => ms >= 5_000); // ours are 20 s and the ask wait; the test's own are short
    try {
      const fake = script({ step: ({ event }) => ({ state: event.kind, frames: [], alarmAt: event.kind === "open" ? Date.now() + 150 : undefined }) });
      const id = newNode();
      const c = await login(id);
      await c.until(() => kinds(fake).includes("alarm"), "the alarm step");
      await sleep(100);
      expect(kinds(fake)).toEqual(["open", "alarm"]);
      expect(await alarmOf(id)).toBeNull();
      expect(longTimers()).toEqual([]);

      // Control: a pending ask is a timer the check can see, and it is gone once the ask has ended.
      const asking = stubFor(id).ask("r-idle", text("q"), Date.now() + 30_000);
      await c.until(() => longTimers().some((ms) => ms > 25_000), "the ask timer");
      await stubFor(id).close(1000, "done");
      await expect(asking).rejects.toThrow("link lost");
      expect(longTimers()).toEqual([]);
      expect(await alarmOf(id)).toBeNull();
    } finally {
      globalThis.setTimeout = realSet;
      globalThis.clearTimeout = realClear;
    }
  });
});

describe("NodeLink ask", () => {
  const answering = () =>
    script({
      step: ({ event }) => {
        if (event.kind === "request") return { state: "r", frames: [event.frame] };
        const said = event.kind === "frame" ? str(event.frame) : "";
        const reply = said.startsWith("answer:") ? [{ requestId: said.slice(7), frame: text("done") }] : said.startsWith("gone:") ? [{ requestId: said.slice(5), frame: null }] : undefined;
        return { state: event.kind, frames: [], replies: reply };
      },
    });

  it("resolves from a later step's reply, and null when the request is gone", async () => {
    const fake = answering();
    const id = newNode();
    const c = await login(id);
    const first = stubFor(id).ask("r1", text("who"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("who"), "the request frame");
    c.send("answer:r1");
    expect(str((await first)!)).toBe("done");
    const second = stubFor(id).ask("r2", text("who2"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("who2"), "the second request frame");
    c.send("gone:r2");
    expect(await second).toBeNull();
    const request = steps(fake).find((s) => (s.args as LinkStepIn).event.kind === "request")?.args as LinkStepIn;
    expect(request.event).toMatchObject({ kind: "request", requestId: "r1" });
    const { at, deadlineAt } = request.event as { at: number; deadlineAt: number };
    expect(deadlineAt).toBeGreaterThan(at - 1); // start + waitMs, the start taken before the request queued
    expect(deadlineAt - at).toBeGreaterThan(4_000);
    expect(deadlineAt - at).toBeLessThanOrEqual(5_000);
  });

  it("rejects with timeout when no reply comes in time", async () => {
    answering();
    const id = newNode();
    await login(id);
    await expect(stubFor(id).ask("r3", text("x"), Date.now() + 100)).rejects.toThrow("timeout");
  });

  it("rejects with link lost when the socket closes, and when there is no session", async () => {
    const fake = answering();
    const answer = fake.step;
    // A slow closed step, as on a loaded runner: the ask after the close waits behind it at the input gate past its deadline.
    fake.step = async (input) => {
      if (input.event.kind === "closed") await sleep(200);
      return answer(input);
    };
    const id = newNode();
    const c = await login(id);
    const pending = stubFor(id).ask("r4", text("x4"), Date.now() + 5_000);
    await c.until(() => c.inbox.includes("x4"), "the request frame");
    c.ws.close(1000, "bye");
    await expect(pending).rejects.toThrow("link lost"); // before the closed step runs
    await expect(stubFor(id).ask("r5", text("x"), Date.now() + 100)).rejects.toThrow("link lost"); // not "timeout"
    await expect(stubFor(id).ask("r6", text("x"), Date.now() + 2_000)).rejects.toThrow("link lost"); // at once, not at the deadline
  });
});

describe("NodeLink failures", () => {
  it("drops the frames queued behind a step that failed: Go sees the failed frame and then only the closed step", async () => {
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame" && str(input.event.frame) === "x") {
          await sleep(100);
          throw new Error("boomJ");
        }
        return defaultPanel().step(input);
      },
    });
    const c = await login(newNode());
    c.send("x");
    c.send("y");
    c.send("z");
    await c.until(() => c.closed !== undefined, "the close");
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    await sleep(200);
    expect(c.closed?.code).toBe(1011);
    expect(seen(fake)).toEqual(["open", "frame:x", "closed"]);
  });

  it("refuses a text frame on an authenticated socket with 1008 and ends the session as owned", async () => {
    const fake = script();
    const c = await login(newNode());
    c.ws.send("text, not a binary frame");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1008);
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    expect((steps(fake).at(-1)?.args as LinkStepIn).event).toMatchObject({ kind: "closed", owned: true });
  });

  it("closes the socket with the close Go asks for, then runs the closed step", async () => {
    const fake = script({ step: (input) => (input.event.kind === "frame" ? { state: "s", frames: [], close: { code: 4001, reason: "done" } } : defaultPanel().step(input)) });
    const c = await login(newNode());
    c.send("x");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed).toEqual({ code: 4001, reason: "done" });
    await c.until(() => kinds(fake).includes("closed"), "the closed step");
    expect(kinds(fake)).toEqual(["open", "frame", "closed"]);
    expect((steps(fake).at(-1)?.args as LinkStepIn).event).toMatchObject({ kind: "closed", owned: true });
  });

  it("closes the socket with 1011 when the accept call throws, and runs no step", async () => {
    const fake = script({
      accept: () => {
        throw new Error("db down");
      },
    });
    const c = await connect(newNode());
    c.send("auth");
    await c.until(() => c.closed !== undefined, "the close");
    expect(c.closed?.code).toBe(1011);
    expect(kinds(fake)).toEqual([]);
  });

  it("answers 503 with no socket when the challenge call throws, and stores nothing", async () => {
    script({
      challenge: () => {
        throw new Error("no wasm");
      },
    });
    const id = newNode();
    const res = await stubFor(id).fetch(`https://link.test/p/link/${id}`, { headers: { Upgrade: "websocket" } });
    expect(res.status).toBe(503);
    expect(res.webSocket).toBeNull();
    expect(await runInDurableObject(stubFor(id), (_o, state) => state.getWebSockets().length)).toBe(0);
    expect(await keysOf(id)).toEqual([]);
  });

  it("takes the Upgrade header case-insensitively", async () => {
    script();
    const id = newNode();
    const res = await stubFor(id).fetch(`https://link.test/p/link/${id}`, { headers: { Upgrade: "WebSocket" } });
    expect(res.status).toBe(101);
    open.push(new Client(res.webSocket!));
  });

  it("makes no Go call once a block has spent its budget, and the step counts as failed", async () => {
    // One block may make several Go calls; blockConcurrencyWhile resets the object at 30 s. The clock is moved past the
    // block's 25 s while accept runs, so the open step that follows has nothing left.
    let skew = 0;
    const realNow = Date.now;
    Date.now = () => realNow() + skew;
    try {
      const fake = script({
        accept: (n, a, nonce, auth) => {
          skew += 26_000;
          return defaultPanel().accept(n, a, nonce, auth);
        },
      });
      const c = await connect(newNode());
      c.send("auth");
      await c.until(() => c.closed !== undefined, "the close");
      expect(c.closed?.code).toBe(1011);
      await c.until(() => kinds(fake).includes("closed"), "the closed step"); // a fresh block, a fresh budget
      expect(seen(fake)).toEqual(["closed"]); // no open step reached Go
    } finally {
      Date.now = realNow;
    }
  });

  it("ends a session whose socket vanished without a close event when the alarm runs", async () => {
    const fake = script();
    const id = newNode();
    await runInDurableObject(stubFor(id), async (_o, state) => {
      state.storage.kv.put("live", 3);
      state.storage.kv.put("state", "s");
      await state.storage.setAlarm(Date.now() + 60_000); // run by hand below, not by the clock
    });
    expect(await runDurableObjectAlarm(stubFor(id))).toBe(true);
    expect(seen(fake)).toEqual(["closed"]);
    expect(steps(fake)[0]?.args).toMatchObject({ nodeId: id, state: "s", event: { kind: "closed", owned: true } });
    expect(await keysOf(id)).not.toContain("live");
    expect(await alarmOf(id)).toBeNull();
  });

  it("never sets the session's alarm closer than a second, whatever Go answers", async () => {
    script({ step: ({ event }) => ({ state: event.kind, frames: [], alarmAt: Date.now() }) });
    const id = newNode();
    await login(id);
    expect((await alarmOf(id)) ?? 0).toBeGreaterThan(Date.now() + 800);
  });

  it("closes only the other authenticated sockets with 4000: a handshake in progress stays", async () => {
    script();
    const id = newNode();
    const first = await login(id);
    const pending = await connect(id);
    const third = await login(id);
    await first.until(() => first.closed !== undefined, "the first close");
    expect(first.closed?.code).toBe(4000);
    expect(pending.closed).toBeUndefined();
    pending.send("auth"); // still free to authenticate, and then it takes over
    await pending.until(() => pending.inbox.length >= 2, "the pending socket's accept");
    await third.until(() => third.closed !== undefined, "the third close");
    expect(third.closed?.code).toBe(4000);
  });
});

describe("NodeLink ask guards", () => {
  const echo = () => script({ step: ({ event }) => ({ state: event.kind, frames: event.kind === "request" ? [event.frame] : [] }) });

  it("never sends a request whose caller's deadline passed while it waited behind a slow step", async () => {
    // The input gate holds the RPC back while a block runs; the deadline is the caller's, so the wait counts from the call.
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame" && str(input.event.frame) === "slow") await sleep(300);
        return defaultPanel().step(input);
      },
    });
    const id = newNode();
    const c = await login(id);
    c.send("slow");
    await sleep(50);
    await expect(stubFor(id).ask("r-late", text("late"), Date.now() + 100)).rejects.toThrow("timeout");
    await sleep(50);
    expect(c.inbox).not.toContain("late");
    expect(seen(fake)).toEqual(["open", "frame:slow"]);
  });

  it("sends a request that waited behind a slow step within its deadline, and resolves it", async () => {
    const fake = script({
      step: async (input) => {
        if (input.event.kind === "frame" && str(input.event.frame) === "slow") await sleep(300);
        return input.event.kind === "frame" && str(input.event.frame).startsWith("answer:")
          ? { state: "a", frames: [], replies: [{ requestId: str(input.event.frame).slice(7), frame: text("done") }] }
          : defaultPanel().step(input);
      },
    });
    const id = newNode();
    const c = await login(id);
    c.send("slow");
    await sleep(50);
    const asked = stubFor(id).ask("r-q", text("who"), Date.now() + 2_000);
    await c.until(() => c.inbox.includes("who"), "the request frame");
    c.send("answer:r-q");
    expect(str((await asked)!)).toBe("done");
    expect(seen(fake)).toEqual(["open", "frame:slow", "request", "frame:answer:r-q"]);
  });
  it("rejects a request id that is already pending, and leaves the first ask alone", async () => {
    echo();
    const id = newNode();
    const c = await login(id);
    const first = stubFor(id).ask("r-dup", text("one"), Date.now() + 300);
    await c.until(() => c.inbox.includes("one"), "the first request frame");
    await expect(stubFor(id).ask("r-dup", text("two"), Date.now() + 300)).rejects.toThrow("duplicate request id");
    await expect(first).rejects.toThrow("timeout"); // its own timer, not overwritten or cancelled by the second
    expect(c.inbox).not.toContain("two");
  });
});
describe("the front door forward", () => {
  const fetchFront = (url: string, init?: RequestInit) => (exports as unknown as { default: Fetcher }).default.fetch(url, init);

  it("hands a 204 + marker answer's upgrade to the node's object, which takes its node id from its name, not from the client", async () => {
    const fake = script();
    const res = await fetchFront("https://panel.test/p/link/nod_front", { headers: { Upgrade: "websocket", "X-Mistgate-Link": "nod_other" } });
    expect(res.status).toBe(101);
    expect(res.headers.get("x-mistgate-link")).toBeNull();
    const c = new Client(res.webSocket!);
    open.push(c);
    await c.until(() => c.inbox.length >= 1, "the challenge");
    expect(c.inbox[0]).toBe("challenge panel.test");
    expect(fake.calls[0]?.args).toEqual({ audience: "panel.test" });
    c.send("auth");
    await c.until(() => fake.calls.some((x) => x.op === "accept"), "the accept call");
    expect(fake.calls.find((x) => x.op === "accept")?.args).toMatchObject({ nodeId: "nod_front" }); // not the client's nod_other
  });

  it("leaves every other panel answer alone", async () => {
    const res = await fetchFront("https://panel.test/p/other");
    expect(res.status).toBe(200);
    expect(res.headers.get("x-panel")).toBe("answered");
  });
});
