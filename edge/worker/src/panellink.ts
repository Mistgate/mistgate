import { WorkerEntrypoint } from "cloudflare:workers";
import type { Env } from "./env";
import { getPanel } from "./panel";
import { detachWaitUntil, type WaitUntilOutput } from "./shell";

// The Go side of the agent link. A NodeLink object holds the socket and an opaque state string and reaches Go through
// this entrypoint (this.ctx.exports.PanelLink, a loopback call into the Worker): the Go panel wasm lives in the Worker's
// isolates, never in the object. Go runs every handshake step and every session step; the object never reads a frame.
//
// Two rules for the Go side of a step:
// - `closed{owned:false}` is not only a takeover by a second socket: it also follows a deploy or reset that took the
//   socket away, and a session whose socket is gone when the alarm runs. Do not read it as "another agent connected".
// - A step must never await a call into the same node's NodeLink (ask, poke, close): the object runs one event at a
//   time, so the call waits for the very step that made it, until the block's budget runs out and the step fails. Fan
//   out to other nodes through waitUntil, outside the step.

/** What happened to the session; Go's step answers each one. `at` is the object's clock (Date.now()). */
export type LinkEvent =
  | { kind: "open"; at: number; generation: number; certSerial: string; certNotAfterUnix: number }
  | { kind: "frame"; at: number; frame: Uint8Array }
  | { kind: "alarm"; at: number }
  | { kind: "desired"; at: number }
  | { kind: "request"; at: number; requestId: string; frame: Uint8Array; deadlineAt: number }
  | { kind: "closed"; at: number; owned: boolean };

export interface LinkStepIn {
  nodeId: string;
  /** What the previous step returned; null before the first one. */
  state: string | null;
  /** End of this Go call's budget (ms since the epoch). */
  until: number;
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
  /** The node was retired; NodeLink deletes its stored session after this closed step. */
  forget?: true;
}

export interface LinkChallenge {
  nonce: Uint8Array;
  frame: Uint8Array;
}

export type LinkAccept = { ok: true; frame: Uint8Array; certSerial: string; certNotAfterUnix: number } | { ok: false };

export class PanelLink extends WorkerEntrypoint<Env> {
  challenge(audience: string): Promise<LinkChallenge> {
    return this.call<LinkChallenge>("challenge", { audience });
  }

  accept(nodeId: string, audience: string, nonce: Uint8Array, auth: Uint8Array, until: number): Promise<LinkAccept> {
    return this.call<LinkAccept>("accept", { nodeId, audience, nonce, auth, until });
  }

  step(input: LinkStepIn): Promise<LinkStepOut> {
    return this.call<LinkStepOut>("step", input as unknown as Record<string, unknown>);
  }

  // The Go op's answer is the shape its caller names; only its waitUntil is taken off before it crosses the RPC.
  private async call<T>(op: string, args: Record<string, unknown>): Promise<T> {
    const panel = await getPanel(this.env, this.env.PUBLIC_URL || "");
    return detachWaitUntil((await panel.link(op, args)) as WaitUntilOutput, this.ctx) as unknown as T;
  }
}
