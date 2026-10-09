// The pure parts of the Worker shell: the fetch contract of mgPanel.fetch (cmd/mistgate-edge), the static-asset reader
// and the init de-duplication. Nothing here touches the wasm, so it runs under plain Node in the tests.

import type { Limiter } from "./limiter";
import type { LimitReply } from "./limitmath";
import type { NodeLink } from "./nodelink";

/** Header pairs keep repeated names (Set-Cookie) as separate entries. */
export type HeaderPairs = [string, string][];

export interface FetchRequest {
  method: string;
  url: string;
  headers: HeaderPairs;
  body: Uint8Array | null;
}

export interface FetchResponse {
  status: number;
  headers: HeaderPairs;
  body: Uint8Array;
  waitUntil: Promise<void>;
}

/** A Go panel link result may carry work that must outlive the current request. */
export type WaitUntilOutput = Record<string, unknown> & { waitUntil?: Promise<void> };

/** Moves after-response work to the Worker context without sending its promise over the PanelLink RPC. */
export function detachWaitUntil<T extends WaitUntilOutput>(out: T, ctx: Pick<ExecutionContext, "waitUntil">): Omit<T, "waitUntil"> {
  const { waitUntil, ...result } = out;
  if (waitUntil !== undefined) ctx.waitUntil(waitUntil);
  return result;
}

/**
 * The panel only accepts absolute https URLs. Cloudflare terminates TLS, so a Worker request is https in production;
 * a local `wrangler dev` may be plain http, and the panel (cookies, origin checks) must see the same shape.
 */
export function panelURL(raw: string): string {
  const url = new URL(raw);
  url.protocol = "https:";
  return url.href;
}

export async function toFetchRequest(request: Request): Promise<FetchRequest> {
  return {
    method: request.method,
    url: panelURL(request.url),
    headers: Array.from(request.headers.entries()),
    body: request.body === null ? null : new Uint8Array(await request.arrayBuffer()),
  };
}

const NULL_BODY_STATUS = new Set([101, 204, 205, 304]);

export function toResponse(response: FetchResponse): Response {
  const headers = new Headers();
  for (const [name, value] of response.headers) headers.append(name, value);
  const body = NULL_BODY_STATUS.has(response.status) ? null : response.body;
  // The panel already encodes what it compresses (Connect answers gzip when the client accepts it) and says so in
  // Content-Encoding; "manual" stops the Workers runtime from encoding that body a second time.
  return new Response(body, { status: response.status, headers, encodeBody: "manual" });
}

/** The header of the panel's answer to an agent link upgrade (fleet.LinkMarkerHeader). */
export const LINK_MARKER = "x-mistgate-link";

/**
 * The panel cannot hold a WebSocket, so it answers an agent link upgrade with 204 and the node's id instead of upgrading.
 * The original request, upgrade headers and all, then goes to the NodeLink object named by that id; the object takes the
 * node id from its own name, never from the request. Its response (the 101) is the answer, and the marker never reaches
 * the client. Undefined for any other panel answer.
 */
export function forwardLink(ns: DurableObjectNamespace<NodeLink>, request: Request, answer: FetchResponse): Promise<Response> | undefined {
  const id = answer.status === 204 ? answer.headers.find(([name]) => name.toLowerCase() === LINK_MARKER)?.[1] : undefined;
  if (!id) return undefined;
  return ns.get(ns.idFromName(id)).fetch(request);
}

/**
 * Reads one file of the SPA build from the Workers static assets, for the panel's `assets` init option. A path that is
 * not an exact 200 hit (missing, redirected) is null. Errors reject: the panel treats them as a failed read, not as a
 * missing file.
 */
export async function readAsset(assets: Fetcher, path: string): Promise<Uint8Array | null> {
  const encoded = path.split("/").map(encodeURIComponent).join("/");
  const response = await assets.fetch(`https://assets.invalid/${encoded}`, { redirect: "manual" });
  if (response.status !== 200) {
    await response.body?.cancel();
    return null;
  }
  return new Uint8Array(await response.arrayBuffer());
}

/**
 * Sends one security-limit call (the panel's `limit` init option) to the Durable Object of its (name, key) pair
 * (name, NUL, key). Names are fixed constants of the panel and never contain a NUL, so the pair maps to one object name without
 * ambiguity. A key too long for an object name makes idFromName throw: the call rejects and the panel refuses the
 * guarded request, as for any limiter failure.
 */
export async function routeLimit(ns: DurableObjectNamespace<Limiter>, request: { name: string; key: string }): Promise<LimitReply> {
  // async on purpose: a synchronous throw would reach Go as a js.Invoke panic instead of a rejected promise.
  return ns.get(ns.idFromName(`${request.name}\u0000${request.key}`)).limit(request);
}

export type AskNodeResult = { reply: Uint8Array | null } | { error: "timeout" | "lost" };

/** Routes one panel request to its node and turns every failure into the result shape expected by Go. */
export async function askNode(
  ns: DurableObjectNamespace<NodeLink>,
  nodeId: string,
  requestId: string,
  frame: Uint8Array,
  deadlineAt: number,
): Promise<AskNodeResult> {
  try {
    const reply = await ns.get(ns.idFromName(nodeId)).ask(requestId, frame, deadlineAt);
    return { reply };
  } catch (error) {
    return { error: error instanceof Error && error.message === "timeout" ? "timeout" : "lost" };
  }
}

/** Closes the node's current link session, if it has one. */
export function closeNode(ns: DurableObjectNamespace<NodeLink>, nodeId: string, reason: string): Promise<void> {
  return ns.get(ns.idFromName(nodeId)).close(4000, reason);
}

/** Pokes all named nodes and returns how many object calls failed. */
export async function pokeNodes(ns: DurableObjectNamespace<NodeLink>, nodeIds: string[]): Promise<number> {
  const results = await Promise.allSettled(nodeIds.map(async (nodeId) => ns.get(ns.idFromName(nodeId)).poke()));
  return results.filter((result) => result.status === "rejected").length;
}

/**
 * Runs start() once and shares the promise between concurrent callers. A failure is not remembered: the next call
 * starts again, so a transient error (D1 not reachable yet) does not wedge the isolate.
 */
export function memoizeRetry<A extends unknown[], T>(start: (...args: A) => Promise<T>): (...args: A) => Promise<T> {
  let current: Promise<T> | undefined;
  return (...args) => {
    const attempt: Promise<T> = (current ??= start(...args));
    attempt.catch(() => {
      if (current === attempt) current = undefined;
    });
    return attempt;
  };
}
