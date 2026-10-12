// The pure parts of the Worker shell: the fetch contract of mgPanel.fetch (cmd/mistgate-edge), the static-asset reader
// and the init de-duplication. Nothing here touches the wasm, so it runs under plain Node in the tests.

import type { Limiter } from "./limiter";
import type { LimitReply } from "./limitmath";
import type { NodeLink } from "./nodelink";
import type { Provisioner } from "./provisioner";

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

/** The header of the panel's answer to an SSH-install request (provision.ForwardHeader): "provision;v=<build>". */
export const PROVISION_MARKER = "x-mistgate-forward";
/** The header that carries the main script's build to the Provisioner (cmd/mistgate-edge: requireBuild). */
export const BUILD_HEADER = "x-mistgate-build";
/** The one Provisioner object: SSH installs are serialized by the process anyway. */
export const PROVISIONER_NAME = "provisioner";

function provisionerUnavailable(): Response {
  return new Response(JSON.stringify({ code: "unavailable", message: "provisioner_unavailable" }), {
    status: 503,
    headers: { "Content-Type": "application/json", "Retry-After": "5" },
  });
}

/**
 * The panel of the main script cannot hold an SSH socket, so it answers an SSH-install request (the ProvisioningService
 * and the install page, behind the sign-in check) with 204 and the marker. The request, whose body the panel has already
 * read, then goes to the Provisioner object of the second script, with the build named by the marker in X-Mistgate-Build (a
 * header the client sent is dropped). The object's answer is the answer; the marker never reaches the client. Undefined for
 * any other panel answer. With no binding, or when the object cannot be reached, the answer is 503 (the Provisioner script
 * is not deployed, or being deployed: Connect clients read it as "unavailable").
 */
export function forwardProvision(
  ns: DurableObjectNamespace<Provisioner> | undefined,
  request: FetchRequest,
  answer: FetchResponse,
): Promise<Response> | undefined {
  const marker = answer.status === 204 ? answer.headers.find(([name]) => name.toLowerCase() === PROVISION_MARKER)?.[1] : undefined;
  const build = marker?.match(/^provision;v=(.+)$/)?.[1];
  if (!build) return undefined;
  if (!ns) return Promise.resolve(provisionerUnavailable());
  const headers = new Headers();
  for (const [name, value] of request.headers) {
    const lower = name.toLowerCase();
    if (lower !== BUILD_HEADER && lower !== "content-length") headers.append(name, value);
  }
  headers.set("X-Mistgate-Build", build);
  const body = request.method === "GET" || request.method === "HEAD" ? undefined : (request.body ?? undefined);
  return (async () => {
    try {
      // redirect "manual": a page answers a form post with a 303 to the job's page, and that is for the browser to follow (a
      // stub's fetch follows redirects like fetch() does, and the POST would be answered with the page it redirects to)
      return await ns.get(ns.idFromName(PROVISIONER_NAME)).fetch(new Request(request.url, { method: request.method, headers, body, redirect: "manual" }));
    } catch (error) {
      console.error("provisioner request failed:", error instanceof Error ? error.message : String(error));
      return provisionerUnavailable();
    }
  })();
}

/** Wakes the Provisioner (sets its alarm); the panel's `provision.kick` option. Rejects when the object cannot be reached. */
export async function kickProvisioner(ns: DurableObjectNamespace<Provisioner>): Promise<void> {
  await ns.get(ns.idFromName(PROVISIONER_NAME)).kick();
}

/** What `connect()` of cloudflare:sockets is, so that this file runs under plain Node in the tests. */
export type Connect = (address: SocketAddress | string, options?: SocketOptions) => Socket;

/** The socket object the panel's sshConnect hands to Go (cmd/mistgate-edge/sshconn_js.go). */
export type SshSocket =
  | { ok: true; read(): Promise<Uint8Array | null>; write(bytes: Uint8Array): Promise<void>; close(): void }
  | { ok: false; error: "refused" | "failed" | "timeout" };

/**
 * What a Worker's connect() says when it will not open an address (a Cloudflare, private or loopback address) and, as measured
 * on a local workerd, when nothing listens there. The two cannot be told apart, so Go words both as "refused or unreachable".
 * The text a production Worker gives for a Cloudflare address is still to be seen (design §2.7).
 */
const REFUSED = /proxy request failed|cannot connect to the specified address/i;

class OpenTimeout extends Error {}

/**
 * Opens a TCP socket for the panel's SSH client. Opening is bounded by timeoutMs (Go derives it from its own deadline), so a
 * socket nobody waits for any more does not hold one of the Worker's six outgoing connections: it is closed when the limit
 * passes. read() ends with null at the end of the stream, and neither read nor write throws once close() was called (a closed
 * socket rejects the read that was waiting; that is not news to the caller that closed it).
 */
export async function openSocket(connect: Connect, host: string, port: number, timeoutMs: number): Promise<SshSocket> {
  let socket: Socket | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    socket = connect({ hostname: host, port }, { secureTransport: "off", allowHalfOpen: false });
    const late = new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new OpenTimeout()), timeoutMs);
    });
    await Promise.race([socket.opened, late]);
  } catch (error) {
    try {
      void socket?.close().catch(() => {});
    } catch {
      // nothing to close
    }
    if (error instanceof OpenTimeout) return { ok: false, error: "timeout" };
    return { ok: false, error: REFUSED.test(error instanceof Error ? error.message : String(error)) ? "refused" : "failed" };
  } finally {
    if (timer !== undefined) clearTimeout(timer);
  }
  const open = socket;
  const reader = open.readable.getReader();
  const writer = open.writable.getWriter();
  let closed = false;
  return {
    ok: true,
    async read() {
      if (closed) return null;
      try {
        const chunk = await reader.read();
        return chunk.done ? null : chunk.value;
      } catch (error) {
        if (closed) return null;
        throw error;
      }
    },
    async write(bytes) {
      try {
        await writer.write(bytes);
      } catch (error) {
        if (closed) return;
        throw error;
      }
    },
    close() {
      if (closed) return;
      closed = true;
      try {
        void open.close().catch(() => {});
      } catch {
        // already closed
      }
    },
  };
}
