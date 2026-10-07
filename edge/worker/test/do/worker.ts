// The entry module of the Durable Object tests: the classes under test (the real Worker's entry also pulls in the wasm),
// and a scripted fake of PanelLink in their place. NodeLink reaches PanelLink through ctx.exports, so the object under
// test runs the code it runs in production against this fake.
import { WorkerEntrypoint } from "cloudflare:workers";
import type { LinkAccept, LinkChallenge, LinkStepIn, LinkStepOut } from "../../src/panellink";
import { forwardLink } from "../../src/shell";

export { Limiter } from "../../src/limiter";
export { NodeLink } from "../../src/nodelink";

const text = (s: string) => new TextEncoder().encode(s);

/** What a test scripts: each hook may be async (to delay) and every call is recorded in `calls`. */
export interface FakePanel {
  calls: { op: "challenge" | "accept" | "step"; args: unknown; start: number; end?: number }[];
  challenge: (nodeId: string, audience: string) => LinkChallenge | Promise<LinkChallenge>;
  accept: (nodeId: string, audience: string, nonce: Uint8Array, auth: Uint8Array) => LinkAccept | Promise<LinkAccept>;
  step: (input: LinkStepIn) => LinkStepOut | Promise<LinkStepOut>;
}

declare global {
  // eslint-disable-next-line no-var
  var __fakePanel: FakePanel | undefined;
}

/** Default behaviour: a fixed challenge, accept unless the auth bytes say "bad", and a step that echoes frames and requests. */
export function defaultPanel(): FakePanel {
  return {
    calls: [],
    challenge: (nodeId, audience) => ({ nonce: new Uint8Array(32).fill(7), frame: text(`challenge ${nodeId} ${audience}`) }),
    accept: (_n, _a, _nonce, auth) =>
      new TextDecoder().decode(auth).startsWith("bad")
        ? { ok: false }
        : { ok: true, frame: text("accept"), certSerial: "c1", certNotAfterUnix: 2_000_000_000 },
    step: ({ state, event }) => ({
      state: `${state ?? ""}${event.kind[0]}`,
      frames: event.kind === "frame" || event.kind === "request" ? [event.frame] : [],
    }),
  };
}

function panel(): FakePanel {
  return (globalThis.__fakePanel ??= defaultPanel());
}

async function record<T>(op: "challenge" | "accept" | "step", args: unknown, run: () => T | Promise<T>): Promise<T> {
  const call: FakePanel["calls"][number] = { op, args, start: Date.now() };
  panel().calls.push(call);
  try {
    return await run();
  } finally {
    call.end = Date.now();
  }
}

export class PanelLink extends WorkerEntrypoint {
  challenge(nodeId: string, audience: string): Promise<LinkChallenge> {
    return record("challenge", { nodeId, audience }, () => panel().challenge(nodeId, audience));
  }
  accept(nodeId: string, audience: string, nonce: Uint8Array, auth: Uint8Array): Promise<LinkAccept> {
    return record("accept", { nodeId, audience, nonce, auth }, () => panel().accept(nodeId, audience, nonce, auth));
  }
  step(input: LinkStepIn): Promise<LinkStepOut> {
    return record("step", input, () => panel().step(input));
  }
}

// The front door, as src/index.ts has it with a fake panel in place of the wasm: /p/link/<id> with an upgrade is
// marked the way fleet.LinkMarker marks it; anything else is an ordinary panel answer.
export default {
  async fetch(request, env): Promise<Response> {
    const id = new URL(request.url).pathname.match(/^\/p\/link\/([^/]+)$/)?.[1];
    const marked = id !== undefined && request.headers.get("Upgrade") === "websocket";
    const answer = { status: marked ? 204 : 200, headers: marked ? [["X-Mistgate-Link", id]] : [], body: new Uint8Array() };
    return (
      (await forwardLink(env.NODELINK, request, answer as Parameters<typeof forwardLink>[2])) ??
      new Response("fake panel", { headers: { "X-Panel": "answered" } })
    );
  },
} satisfies ExportedHandler<Cloudflare.Env>;
