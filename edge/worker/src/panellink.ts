import { WorkerEntrypoint } from "cloudflare:workers";
import type { Env } from "./env";
import { getPanel } from "./panel";

// The Go side of the agent link. A NodeLink object holds the socket and an opaque state string and reaches Go through
// this entrypoint (this.ctx.exports.PanelLink, a loopback call into the Worker): the Go panel wasm lives in the Worker's
// isolates, never in the object. Go runs every handshake step and every session step; the object never reads a frame.

/** What happened to the session; Go's step answers each one. `at` is the object's clock (Date.now()). */
export type LinkEvent =
  | { kind: "open"; at: number; certSerial: string; certNotAfterUnix: number }
  | { kind: "frame"; at: number; frame: Uint8Array }
  | { kind: "alarm"; at: number }
  | { kind: "desired"; at: number }
  | { kind: "request"; at: number; requestId: string; frame: Uint8Array; deadlineAt: number }
  | { kind: "closed"; at: number; owned: boolean };

export interface LinkStepIn {
  nodeId: string;
  /** What the previous step returned; null before the first one. */
  state: string | null;
  event: LinkEvent;
}

export interface LinkStepOut {
  state: string;
  /** Sent to the agent in order. */
  frames: Uint8Array[];
  close?: { code: number; reason: string };
  /** The next time the session wants a step (ms since the epoch); none = nothing is due. */
  alarmAt?: number;
  /** An answer for a pending `ask`; a null frame means the request is gone. */
  replies?: { requestId: string; frame: Uint8Array | null }[];
}

export interface LinkChallenge {
  nonce: Uint8Array;
  frame: Uint8Array;
}

export type LinkAccept = { ok: true; frame: Uint8Array; certSerial: string; certNotAfterUnix: number } | { ok: false };

export class PanelLink extends WorkerEntrypoint<Env> {
  challenge(nodeId: string, audience: string): Promise<LinkChallenge> {
    return this.call("challenge", { nodeId, audience }) as Promise<LinkChallenge>;
  }

  accept(nodeId: string, audience: string, nonce: Uint8Array, auth: Uint8Array): Promise<LinkAccept> {
    return this.call("accept", { nodeId, audience, nonce, auth }) as Promise<LinkAccept>;
  }

  step(input: LinkStepIn): Promise<LinkStepOut> {
    return this.call("step", input as unknown as Record<string, unknown>) as Promise<LinkStepOut>;
  }

  private async call(op: string, args: Record<string, unknown>): Promise<unknown> {
    // The origin only fills publicURL while the database is empty, and a node link cannot exist before the panel has
    // run once (the link prefix is made then), so this placeholder is never stored.
    const panel = await getPanel(this.env, this.env.PUBLIC_URL || "https://panel.invalid");
    return panel.link(op, args);
  }
}
