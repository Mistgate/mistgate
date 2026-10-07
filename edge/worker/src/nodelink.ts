import { DurableObject } from "cloudflare:workers";
import type { Env } from "./env";
import type { LinkAccept, LinkEvent, LinkStepOut, PanelLink } from "./panellink";
import { LINK_MARKER } from "./shell";

// The shell of one node's agent link: one object per node (idFromName(node id)), holding the agent's WebSocket and the
// session state as an opaque string. It never reads a frame: the Go panel (PanelLink) runs every handshake step and
// every session step, and this object only moves bytes, keeps the order, and owns the alarm.
//
// Ordering: every entry point runs inside blockConcurrencyWhile, so one event is handled at a time, in arrival order,
// RPC methods included. Go gets 20 s per call (blockConcurrencyWhile resets the object after 30 s); an error or a
// timeout closes the socket with 1011 and the agent reconnects.
//
// Hibernation: nothing is kept in memory but the waiters of `ask` (a pending ask keeps the object alive anyway), and
// no timer is left running when nothing is pending: the one alarm is the earliest of the session's own alarm, the
// handshake deadlines of sockets that have not authenticated, and "now" when a desired-state poke is waiting.
//
// Storage (sync kv): nodeId; gen (last issued generation); live (generation of the open session, absent = none);
// state (the last step's state, kept after the session ends); alarmAt; poke.

const HANDSHAKE_MS = 10_000;
const STEP_MS = 20_000;
/** An alarm further out than this is set to this and re-armed when it fires (setAlarm wants a sane timestamp). */
const MAX_ALARM_MS = 30 * 24 * 3600 * 1000;
const OPEN = 1; // WebSocket.readyState

/** Per socket. generation 0 = not authenticated yet; a session's sockets carry the generation that opened it. */
interface Attachment {
  nonce: Uint8Array;
  deadline: number;
  audience: string;
  generation: number;
}

interface Waiter {
  resolve(frame: Uint8Array | null): void;
  reject(error: Error): void;
}

/** Races p against a timer and never leaves the timer behind. */
function within<T>(p: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | null = null;
  const late = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error("panel link call timed out")), ms);
  });
  return Promise.race([p, late]).finally(() => clearTimeout(timer));
}

/** A socket that is already gone throws on send and close; that must not take the event down. */
function send(ws: WebSocket, data: ArrayBuffer | ArrayBufferView): void {
  try {
    ws.send(data);
  } catch {
    // the close event follows
  }
}

function shut(ws: WebSocket, code: number, reason: string): void {
  try {
    ws.close(code, reason);
  } catch {
    // already closing
  }
}

export class NodeLink extends DurableObject<Env> {
  private waiters = new Map<string, Waiter>();

  private get kv(): SyncKvStorage {
    return this.ctx.storage.kv;
  }

  /** The Go side, through the Worker's own loopback (ctx.exports). */
  private get panel(): Service<PanelLink> {
    return (this.ctx.exports as unknown as { PanelLink: Service<PanelLink> }).PanelLink;
  }

  /**
   * Runs one event alone. blockConcurrencyWhile resets the whole object when its callback throws, which would drop every
   * socket for one bad event, so an error is logged and the event ends there (undefined).
   */
  private serial<T>(work: () => Promise<T>): Promise<T | undefined> {
    return this.ctx.blockConcurrencyWhile(async () => {
      try {
        return await work();
      } catch (error) {
        console.error("link event failed:", error instanceof Error ? error.message : String(error));
        return undefined;
      }
    });
  }

  private attachment(ws: WebSocket): Attachment | null {
    return ws.deserializeAttachment() as Attachment | null;
  }

  private openSockets(): WebSocket[] {
    return this.ctx.getWebSockets().filter((ws) => ws.readyState === OPEN);
  }

  /** The socket of the open session, if it is still connected. */
  private liveSocket(): WebSocket | undefined {
    const live = this.kv.get<number>("live");
    return live ? this.openSockets().find((ws) => this.attachment(ws)?.generation === live) : undefined;
  }

  /** The agent's upgrade, forwarded by the Worker with the node id the panel checked (forwardLink). The challenge comes from Go. */
  async fetch(request: Request): Promise<Response> {
    if (request.headers.get("Upgrade") !== "websocket") return new Response("Upgrade Required", { status: 426 });
    const url = new URL(request.url);
    const nodeId = request.headers.get(LINK_MARKER) ?? "";
    if (nodeId === "") return new Response("Bad Request", { status: 400 });
    const answer = await this.serial(async () => {
      let challenge;
      try {
        challenge = await within(this.panel.challenge(nodeId, url.host), STEP_MS);
      } catch (error) {
        console.error("link challenge failed:", error instanceof Error ? error.message : String(error));
        return new Response("Service Unavailable", { status: 503 });
      }
      const pair = new WebSocketPair();
      const [client, server] = [pair[0], pair[1]];
      this.ctx.acceptWebSocket(server);
      this.kv.put("nodeId", nodeId);
      const attachment: Attachment = { nonce: challenge.nonce, deadline: Date.now() + HANDSHAKE_MS, audience: url.host, generation: 0 };
      server.serializeAttachment(attachment);
      server.send(challenge.frame);
      await this.arm();
      return new Response(null, { status: 101, webSocket: client });
    });
    return answer ?? new Response("Internal Server Error", { status: 500 });
  }

  async webSocketMessage(ws: WebSocket, message: string | ArrayBuffer): Promise<void> {
    await this.serial(async () => {
      const att = this.attachment(ws);
      if (att === null) return;
      if (att.generation === 0) {
        // The first message of a socket is its LinkAuth. Anything else, or anything late, is refused.
        if (typeof message === "string" || Date.now() > att.deadline) shut(ws, 1008, "handshake");
        else await this.authenticate(ws, att, message);
      } else if (att.generation === this.kv.get<number>("live")) {
        if (typeof message === "string") {
          shut(ws, 1008, "binary frames only");
          await this.dropLive(true);
        } else await this.run(ws, { kind: "frame", at: Date.now(), frame: new Uint8Array(message) });
      } // else: a frame of a socket that has been replaced: dropped
      await this.arm();
    });
  }

  async webSocketClose(ws: WebSocket, code: number): Promise<void> {
    // The agent's close frame is answered here; 1005, 1006 and 1015 only describe a close and cannot be sent.
    shut(ws, code === 1005 || code === 1006 || code === 1015 ? 1000 : code, "");
    await this.serial(() => this.lost(ws));
  }

  async webSocketError(ws: WebSocket): Promise<void> {
    await this.serial(() => this.lost(ws));
  }

  async alarm(): Promise<void> {
    await this.serial(async () => {
      const now = Date.now();
      for (const ws of this.openSockets()) {
        const att = this.attachment(ws);
        if (att?.generation === 0 && att.deadline <= now) shut(ws, 1008, "handshake timeout");
      }
      if (this.kv.get("poke")) {
        this.kv.delete("poke");
        const ws = this.liveSocket();
        if (ws) await this.run(ws, { kind: "desired", at: now });
      }
      const due = this.kv.get<number>("alarmAt");
      if (due !== undefined && due <= Date.now()) {
        this.kv.delete("alarmAt");
        const ws = this.liveSocket();
        if (ws) await this.run(ws, { kind: "alarm", at: Date.now() });
      }
      await this.arm();
    });
  }

  /** Something that feeds desired state changed: run a desired step soon. Repeated pokes merge into one. */
  async poke(): Promise<void> {
    await this.serial(async () => {
      if (!this.liveSocket()) return; // nothing is connected: the next open step reads the new state anyway
      this.kv.put("poke", true);
      await this.ctx.storage.setAlarm(Date.now());
    });
  }

  /**
   * Sends a request frame through a session step and waits for the reply a later step resolves. The wait is outside the
   * serial block (other events must run meanwhile). Rejects with "link lost" when the session ends and "timeout" at waitMs.
   */
  async ask(requestId: string, frame: Uint8Array, waitMs: number): Promise<Uint8Array | null> {
    const reply = new Promise<Uint8Array | null>((resolve, reject) => this.waiters.set(requestId, { resolve, reject }));
    reply.catch(() => {}); // a rejection before the await below is still the caller's
    const timer = setTimeout(() => this.waiters.get(requestId)?.reject(new Error("timeout")), waitMs);
    try {
      const sent = await this.serial(async () => {
        const ws = this.liveSocket();
        if (!ws) return false;
        const at = Date.now();
        await this.run(ws, { kind: "request", at, requestId, frame, deadlineAt: at + waitMs });
        await this.arm();
        return true;
      });
      if (!sent) throw new Error("link lost");
      return await reply;
    } finally {
      clearTimeout(timer);
      this.waiters.delete(requestId);
    }
  }

  /** Closes the open session's socket (re-enrolment). */
  async close(code: number, reason: string): Promise<void> {
    await this.serial(async () => {
      const ws = this.liveSocket();
      if (!ws) return;
      shut(ws, code, reason);
      await this.dropLive(true);
      await this.arm();
    });
  }

  /** LinkAuth: Go decides. A proven node takes the link over from any other socket, whose session ends. */
  private async authenticate(ws: WebSocket, att: Attachment, auth: ArrayBuffer): Promise<void> {
    const nodeId = this.kv.get<string>("nodeId") ?? "";
    let res: LinkAccept;
    try {
      res = await within<LinkAccept>(this.panel.accept(nodeId, att.audience, att.nonce, new Uint8Array(auth)), STEP_MS);
    } catch (error) {
      console.error("link accept failed:", error instanceof Error ? error.message : String(error));
      shut(ws, 1011, "link error");
      return;
    }
    if (!res.ok) {
      shut(ws, 1008, "unauthorized");
      return;
    }
    const generation = (this.kv.get<number>("gen") ?? 0) + 1;
    this.kv.put("gen", generation);
    ws.serializeAttachment({ ...att, generation });
    for (const other of this.openSockets()) {
      if (this.attachment(other)?.generation !== generation) shut(other, 4000, "superseded");
    }
    await this.dropLive(false);
    this.kv.put("live", generation);
    send(ws, res.frame);
    await this.run(ws, { kind: "open", at: Date.now(), certSerial: res.certSerial, certNotAfterUnix: res.certNotAfterUnix });
  }

  /**
   * One session step on the Go side, applied to the session's socket (undefined once it is gone). The state is written
   * first: the output gate holds every send below until it is durable, so a frame the agent sees is never ahead of the
   * state that produced it.
   */
  private async run(ws: WebSocket | undefined, event: LinkEvent): Promise<void> {
    let out: LinkStepOut;
    try {
      out = await within(
        this.panel.step({ nodeId: this.kv.get<string>("nodeId") ?? "", state: this.kv.get<string>("state") ?? null, event }),
        STEP_MS,
      );
    } catch (error) {
      console.error("link step failed:", error instanceof Error ? error.message : String(error));
      // The session stays "live" until the socket's close event (or the next accept) runs its closed step.
      if (ws) shut(ws, 1011, "link error");
      this.rejectWaiters();
      return;
    }
    this.kv.put("state", out.state);
    if (ws) for (const frame of out.frames) send(ws, frame);
    for (const r of out.replies ?? []) this.waiters.get(r.requestId)?.resolve(r.frame);
    if (out.alarmAt === undefined) this.kv.delete("alarmAt");
    else this.kv.put("alarmAt", out.alarmAt);
    if (ws && out.close) {
      shut(ws, out.close.code, out.close.reason);
      await this.dropLive(true);
    }
  }

  /** The open session is over: its waiters fail, its alarm goes, and Go gets the closed step (owned = this socket's close). */
  private async dropLive(owned: boolean): Promise<void> {
    if (!this.kv.get("live")) return;
    this.kv.delete("live");
    this.rejectWaiters();
    await this.run(undefined, { kind: "closed", at: Date.now(), owned });
    this.kv.delete("alarmAt");
  }

  /** A socket closed or failed; only the open session's socket matters. */
  private async lost(ws: WebSocket): Promise<void> {
    const att = this.attachment(ws);
    if (att !== null && att.generation !== 0 && att.generation === this.kv.get<number>("live")) await this.dropLive(true);
    await this.arm();
  }

  private rejectWaiters(): void {
    for (const w of this.waiters.values()) w.reject(new Error("link lost"));
  }

  /** One alarm for everything pending; none when nothing is, so the object can hibernate. */
  private async arm(): Promise<void> {
    let at = this.kv.get<number>("alarmAt") ?? Infinity;
    for (const ws of this.openSockets()) {
      const att = this.attachment(ws);
      if (att?.generation === 0) at = Math.min(at, att.deadline);
    }
    if (this.kv.get("poke")) at = 0;
    if (at === Infinity) await this.ctx.storage.deleteAlarm();
    else await this.ctx.storage.setAlarm(Math.min(Math.max(at, Date.now()), Date.now() + MAX_ALARM_MS));
  }
}
