import "../dist/wasm_exec.js";
import panelModule from "../dist/panel.wasm";
import type { Env, ProvisionEnv } from "./env";
import { askNode, closeNode, kickProvisioner, pokeNodes, type FetchRequest, type FetchResponse, memoizeRetry, readAsset, routeLimit } from "./shell";

/** The object the Go program publishes as globalThis.mgPanel (cmd/mistgate-edge). */
export interface PanelApi {
  init(options: Record<string, unknown>): Promise<unknown>;
  fetch(request: FetchRequest): Promise<FetchResponse>;
  cron(request: { at: number }): Promise<{ waitUntil: Promise<void> }>;
  /** One step of the agent link (cmd/mistgate-edge/link_js.go); bytes are Uint8Array. */
  link(op: string, args: Record<string, unknown>): Promise<unknown>;
  /** One pass of the Provisioner's alarm (cmd/mistgate-edge/provision_js.go); only the serve role has it. more: another install waits; busy: a pass already runs here. */
  provision(): Promise<{ more: boolean; busy: boolean; waitUntil: Promise<void> }>;
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
      serving = memoizeRetry(initServe);
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
    nodeLink: nodeLinkOf(env.NODELINK),
    // SSH installs run in the second script's object: this panel marks their requests and wakes it from the cron.
    // Without the binding it still marks them (the Worker then answers 503) and has nobody to wake.
    provision: env.PROVISIONER ? { role: "forward", kick: () => kickProvisioner(env.PROVISIONER!) } : undefined,
    // Used only while the database is empty; the panel keeps what it stored afterwards.
    publicURL: env.PUBLIC_URL || origin,
    adminHost: env.ADMIN_HOST || undefined,
    adminPrefix: env.ADMIN_PREFIX || undefined,
    subPrefix: env.SUB_PREFIX || undefined,
    // The deployer's setup token (hash only); without it the panel mints a random one and prints its link once.
    setupTokenSHA256: env.SETUP_TOKEN_SHA256?.trim() || undefined,
  });
  return panel;
}

/** The node link callbacks of the panel's `nodeLink` option, over the NODELINK binding. */
function nodeLinkOf(ns: Env["NODELINK"]) {
  return {
    ask: (nodeId: string, requestId: string, frame: Uint8Array, deadlineAt: number) => askNode(ns, nodeId, requestId, frame, deadlineAt),
    close: (nodeId: string, reason: string) => closeNode(ns, nodeId, reason),
    poke: (nodeIds: string[]) => pokeNodes(ns, nodeIds),
  };
}

/** What the Provisioner object gives the panel of its isolate: how to wake itself and how to open an SSH socket. */
export interface ServeHooks {
  kick(): Promise<void>;
  sshConnect(host: string, port: number, timeoutMs: number): Promise<unknown>;
}

/**
 * The panel of the Provisioner's script: the same wasm in the "serve" role. It reads the instance the main Worker created
 * (no public URL, no assets, no first-run settings, no setup token), serves the ProvisioningService and the install page
 * for requests of its own build, and runs one install per pass of mgPanel.provision().
 */
async function initServe(env: ProvisionEnv, hooks: ServeHooks): Promise<PanelApi> {
  const panel = await startGo();
  await panel.init({
    d1: env.DB,
    masterKey: env.MASTER_KEY,
    limit: (request: { name: string; key: string }) => routeLimit(env.LIMITER, request),
    nodeLink: nodeLinkOf(env.NODELINK),
    provision: { role: "serve", kick: hooks.kick, sshConnect: hooks.sshConnect },
  });
  return panel;
}

let ready = memoizeRetry(init);
let serving = memoizeRetry(initServe);

/** The initialised panel of this isolate. Concurrent first requests share one init; a failed init is retried by the next request. */
export function getPanel(env: Env, origin: string): Promise<PanelApi> {
  return ready(env, origin);
}

/** The initialised panel of the Provisioner's isolate (the serve role). Concurrent callers share one init; a failed init is retried. */
export function getServePanel(env: ProvisionEnv, hooks: ServeHooks): Promise<PanelApi> {
  return serving(env, hooks);
}
