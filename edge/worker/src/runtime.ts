import type { PanelEnv, ProvisionEnv } from "./env";
import { askNode, closeNode, kickProvisioner, pokeNodes, type FetchRequest, type FetchResponse, memoizeRetry, readAsset, routeLimit } from "./shell";

// The Go panel inside a Worker isolate, for one wasm module. Which module it is does not matter here: the single script
// passes dist/panel.wasm (src/panel.ts), each role script of the split edition passes its own (src/roles). The module's
// glue, dist/wasm_exec.js, is imported by the entry that owns the wasm, because it defines the global `Go`.

/** The object the Go program publishes as globalThis.mgPanel (cmd/mistgate-edge). */
export interface PanelApi {
  init(options: Record<string, unknown>): Promise<unknown>;
  fetch(request: FetchRequest): Promise<FetchResponse>;
  cron(request: { at: number }): Promise<{ waitUntil: Promise<void> }>;
  /** One step of the agent link (edge/bridge/link_js.go); bytes are Uint8Array. */
  link(op: string, args: Record<string, unknown>): Promise<unknown>;
  /** One pass of the Provisioner's alarm (edge/bridge/provision_js.go); only the serve role has it. more: another install waits; busy: a pass already runs here. */
  provision(): Promise<{ more: boolean; busy: boolean; waitUntil: Promise<void> }>;
}

declare global {
  // eslint-disable-next-line no-var
  var mgPanel: PanelApi | undefined;
}

/** What the Provisioner object gives the panel of its isolate: how to wake itself and how to open an SSH socket. */
export interface ServeHooks {
  kick(): Promise<void>;
  sshConnect(host: string, port: number, timeoutMs: number): Promise<unknown>;
}

/** The initialised panel of one script. */
export interface Runtime {
  /** The initialised panel of this isolate. Concurrent first requests share one init; a failed init is retried by the next request. */
  getPanel(env: PanelEnv, origin: string): Promise<PanelApi>;
  /** The initialised panel of the Provisioner's isolate (the serve role). Concurrent callers share one init; a failed init is retried. */
  getServePanel(env: ProvisionEnv, hooks: ServeHooks): Promise<PanelApi>;
}

/** The node link callbacks of the panel's `nodeLink` option, over the NODELINK binding. */
function nodeLinkOf(ns: PanelEnv["NODELINK"]) {
  if (!ns) {
    const none = () => Promise.reject(new Error("this script has no NODELINK binding"));
    return { ask: none, close: none, poke: none };
  }
  return {
    ask: (nodeId: string, requestId: string, frame: Uint8Array, deadlineAt: number) => askNode(ns, nodeId, requestId, frame, deadlineAt),
    close: (nodeId: string, reason: string) => closeNode(ns, nodeId, reason),
    poke: (nodeIds: string[]) => pokeNodes(ns, nodeIds),
  };
}

/** The `limit` option over the LIMITER binding; without it every guarded request is refused (a rejection is how the panel hears "no"). */
function limitOf(ns: PanelEnv["LIMITER"]) {
  return (request: { name: string; key: string }) => (ns ? routeLimit(ns, request) : Promise.reject(new Error("this script has no LIMITER binding")));
}

/**
 * The Go program of `wasm`, once per isolate. One isolate holds one script and so one wasm: the global mgPanel the program
 * publishes is not per runtime, so two runtimes in one isolate (a test) must not run at the same time.
 */
export function createRuntime(wasm: WebAssembly.Module): Runtime {
  let running: Promise<PanelApi> | undefined;
  let ready = memoizeRetry(init);
  let serving = memoizeRetry(initServe);

  /** Instantiates the Go program once per isolate (instance reuse is mandatory: a fresh instance costs ~90 ms of CPU). */
  function startGo(): Promise<PanelApi> {
    running ??= (async () => {
      const go = new Go();
      const instance = await WebAssembly.instantiate(wasm, go.importObject);
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

  async function init(env: PanelEnv, origin: string): Promise<PanelApi> {
    const panel = await startGo();
    const assets = env.ASSETS;
    await panel.init({
      d1: env.DB,
      masterKey: env.MASTER_KEY,
      // A role without static assets (everything but the admin and the public role) has no files to read.
      assets: assets ? (path: string) => readAsset(assets, path) : undefined,
      // Resolves {ok, retryAfterMs, remaining, first}; a rejection makes the panel refuse the guarded request.
      limit: limitOf(env.LIMITER),
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
      recoveryTokenSHA256: env.RECOVERY_TOKEN_SHA256?.trim() || undefined,
    });
    return panel;
  }

  /**
   * The panel of the Provisioner's script: the wasm in the "serve" role. It reads the instance the admin script created
   * (no public URL, no assets, no first-run settings, no setup token), serves the ProvisioningService and the install page
   * for requests of its own build, and runs one install per pass of mgPanel.provision().
   */
  async function initServe(env: ProvisionEnv, hooks: ServeHooks): Promise<PanelApi> {
    const panel = await startGo();
    await panel.init({
      d1: env.DB,
      masterKey: env.MASTER_KEY,
      limit: limitOf(env.LIMITER),
      nodeLink: nodeLinkOf(env.NODELINK),
      provision: { role: "serve", kick: hooks.kick, sshConnect: hooks.sshConnect },
    });
    return panel;
  }

  return { getPanel: (env, origin) => ready(env, origin), getServePanel: (env, hooks) => serving(env, hooks) };
}
