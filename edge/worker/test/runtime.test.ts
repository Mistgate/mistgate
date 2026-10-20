import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRuntime } from "../src/runtime";
import { PANEL_START_BOUND_MS } from "../src/shell";

// The runtime of one wasm module (src/runtime.ts) with the wasm replaced by a recorder: which module it instantiates, and what
// init gets in a role script that has only some of the bindings (panel.test.ts has the single script's full set).

type Options = Record<string, any>;
let initCalls: Options[];
let instantiated: unknown[];

beforeEach(() => {
  initCalls = [];
  instantiated = [];
  (globalThis as any).Go = class {
    importObject = {};
    run() {
      (globalThis as any).mgPanel = { init: async (options: Options) => void initCalls.push(options) };
      return new Promise<void>(() => {}); // the Go program never returns
    }
  };
  vi.spyOn(WebAssembly, "instantiate").mockImplementation((async (module: unknown) => {
    instantiated.push(module);
    return {};
  }) as any);
});

afterEach(() => {
  vi.restoreAllMocks();
  delete (globalThis as any).Go;
  delete (globalThis as any).mgPanel;
});

const key = "ab".repeat(32);

describe("createRuntime", () => {
  it("instantiates the module it was given, once for concurrent callers", async () => {
    const wasm = { name: "link.wasm" } as unknown as WebAssembly.Module;
    const runtime = createRuntime(wasm);
    const env = { DB: {}, MASTER_KEY: key } as any;
    await Promise.all([runtime.getPanel(env, "https://x.test"), runtime.getPanel(env, "https://x.test")]);
    expect(instantiated).toEqual([wasm]);
    expect(initCalls).toHaveLength(1);
  });

  it("an init that never settles answers its waiters with an error after the bound, and the next request starts a new init", async () => {
    vi.useFakeTimers();
    try {
      let calls = 0;
      (globalThis as any).Go = class {
        importObject = {};
        run() {
          (globalThis as any).mgPanel = { init: (): Promise<void> => (++calls === 1 ? new Promise(() => {}) : Promise.resolve()) };
          return new Promise<void>(() => {});
        }
      };
      const runtime = createRuntime({} as WebAssembly.Module);
      const env = { DB: {}, MASTER_KEY: key } as any;
      const first = runtime.getPanel(env, "https://x.test").catch((error: Error) => error as Error);
      const second = runtime.getPanel(env, "https://x.test").catch((error: Error) => error as Error);
      await vi.advanceTimersByTimeAsync(PANEL_START_BOUND_MS);
      expect(((await first) as Error).message).toMatch(/did not start within/);
      expect(((await second) as Error).message).toMatch(/did not start within/);
      expect(calls).toBe(1); // the concurrent first requests shared one init
      await expect(runtime.getPanel(env, "https://x.test")).resolves.toBeDefined();
      expect(calls).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("an init that is rejected is retried by the next request", async () => {
    let calls = 0;
    (globalThis as any).Go = class {
      importObject = {};
      run() {
        (globalThis as any).mgPanel = {
          init: async () => {
            if (++calls === 1) throw new Error("d1 down");
          },
        };
        return new Promise<void>(() => {});
      }
    };
    const runtime = createRuntime({} as WebAssembly.Module);
    const env = { DB: {}, MASTER_KEY: key } as any;
    await expect(runtime.getPanel(env, "")).rejects.toThrow("d1 down");
    await expect(runtime.getPanel(env, "")).resolves.toBeDefined();
    expect(calls).toBe(2);
  });

  it("two runtimes keep their own modules", async () => {
    const [a, b] = [{ name: "a" }, { name: "b" }] as unknown as WebAssembly.Module[];
    const env = { DB: {}, MASTER_KEY: key } as any;
    await createRuntime(a!).getPanel(env, "");
    delete (globalThis as any).mgPanel; // one isolate holds one script: here the second one starts as if in its own
    await createRuntime(b!).getPanel(env, "");
    expect(instantiated).toEqual([a, b]);
  });

  it("a role with only DB and MASTER_KEY: no assets, nothing to wake, and every guarded or node call is a rejection", async () => {
    const env = { DB: { db: true }, MASTER_KEY: key } as any;
    await createRuntime({} as WebAssembly.Module).getPanel(env, "https://x.test");
    const o = initCalls[0]!;
    expect(o.d1).toBe(env.DB);
    expect(o.masterKey).toBe(env.MASTER_KEY);
    expect(o.assets).toBeUndefined();
    expect(o.provision).toBeUndefined();
    await expect(o.limit({ name: "login", key: "k" })).rejects.toThrow(/LIMITER/);
    await expect(o.nodeLink.ask("nod_1", "r", new Uint8Array(), 1)).rejects.toThrow(/NODELINK/);
    await expect(o.nodeLink.send("nod_1", "r", new Uint8Array(), 1)).rejects.toThrow(/NODELINK/);
    await expect(o.nodeLink.close("nod_1", "x")).rejects.toThrow(/NODELINK/);
    await expect(o.nodeLink.poke(["nod_1"])).rejects.toThrow(/NODELINK/);
  });

  it("a role with the bindings: limit and poke reach the objects", async () => {
    const poked: string[] = [];
    const ns = (calls: string[]) => ({ idFromName: (n: string) => n, get: (id: string) => ({ limit: async () => ({ ok: true }), poke: async () => void calls.push(id), send: async (r: string, _f: Uint8Array, at: number) => r === "r1" && at === 5 }) });
    const env = { DB: {}, MASTER_KEY: key, LIMITER: ns([]), NODELINK: ns(poked), ASSETS: {} } as any;
    await createRuntime({} as WebAssembly.Module).getPanel(env, "https://x.test");
    const o = initCalls[0]!;
    expect(o.assets).toBeTypeOf("function");
    expect(await o.limit({ name: "login", key: "k" })).toEqual({ ok: true });
    expect(await o.nodeLink.poke(["nod_1", "nod_2"])).toBe(0);
    expect(poked).toEqual(["nod_1", "nod_2"]);
    expect(await o.nodeLink.send("nod_1", "r1", new Uint8Array(), 5)).toBe(true);
    expect(await o.nodeLink.send("nod_1", "r2", new Uint8Array(), 5)).toBe(false);
  });
});
