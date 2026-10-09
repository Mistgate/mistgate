import "../dist/wasm_exec.js";
import panelModule from "../dist/panel.wasm";
import type { Env } from "./env";
import { askNode, closeNode, pokeNodes, type FetchRequest, type FetchResponse, memoizeRetry, readAsset, routeLimit } from "./shell";

/** The object the Go program publishes as globalThis.mgPanel (cmd/mistgate-edge). */
interface PanelApi {
  init(options: Record<string, unknown>): Promise<unknown>;
  fetch(request: FetchRequest): Promise<FetchResponse>;
  cron(request: { at: number }): Promise<{ waitUntil: Promise<void> }>;
  /** One step of the agent link (cmd/mistgate-edge/link_js.go); bytes are Uint8Array. */
  link(op: string, args: Record<string, unknown>): Promise<unknown>;
}

declare global {
  // eslint-disable-next-line no-var
  var mgPanel: PanelApi | undefined;
}

let running: Promise<PanelApi> | undefined;

/** Instantiates the Go program once per isolate (instance reuse is mandatory: a fresh instance costs ~90 ms of CPU). */
function startGo(): Promise<PanelApi> {
  running ??= (async () => {
    const go = new Go();
    const instance = await WebAssembly.instantiate(panelModule, go.importObject);
    // main() publishes mgPanel and then blocks forever; when the program ends (a fatal Go error) the next request starts anew.
    const forget = () => {
      running = undefined;
      globalThis.mgPanel = undefined;
      ready = memoizeRetry(init);
    };
    void go.run(instance).then(forget, forget);
    for (let i = 0; !globalThis.mgPanel && i < 200; i++) await new Promise((resolve) => setTimeout(resolve, 1));
    if (!globalThis.mgPanel) throw new Error("the Go program did not publish mgPanel");
    return globalThis.mgPanel;
  })().catch((error: unknown) => {
    running = undefined;
    throw error;
  });
  return running;
}

async function init(env: Env, origin: string): Promise<PanelApi> {
  const panel = await startGo();
  await panel.init({
    d1: env.DB,
    masterKey: env.MASTER_KEY,
    assets: (path: string) => readAsset(env.ASSETS, path),
    // Resolves {ok, retryAfterMs, remaining, first}; a rejection makes the panel refuse the guarded request.
    limit: (request: { name: string; key: string }) => routeLimit(env.LIMITER, request),
    nodeLink: {
      ask: (nodeId: string, requestId: string, frame: Uint8Array, deadlineAt: number) => askNode(env.NODELINK, nodeId, requestId, frame, deadlineAt),
      close: (nodeId: string, reason: string) => closeNode(env.NODELINK, nodeId, reason),
      poke: (nodeIds: string[]) => pokeNodes(env.NODELINK, nodeIds),
    },
    // Used only while the database is empty; the panel keeps what it stored afterwards.
    publicURL: env.PUBLIC_URL || origin,
    adminHost: env.ADMIN_HOST || undefined,
    adminPrefix: env.ADMIN_PREFIX || undefined,
    subPrefix: env.SUB_PREFIX || undefined,
  });
  return panel;
}

let ready = memoizeRetry(init);

/** The initialised panel of this isolate. Concurrent first requests share one init; a failed init is retried by the next request. */
export function getPanel(env: Env, origin: string): Promise<PanelApi> {
  return ready(env, origin);
}
