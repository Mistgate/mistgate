import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createRuntime } from "../src/runtime";

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
    await expect(o.nodeLink.close("nod_1", "x")).rejects.toThrow(/NODELINK/);
    await expect(o.nodeLink.poke(["nod_1"])).rejects.toThrow(/NODELINK/);
  });

  it("a role with the bindings: limit and poke reach the objects", async () => {
    const poked: string[] = [];
    const ns = (calls: string[]) => ({ idFromName: (n: string) => n, get: (id: string) => ({ limit: async () => ({ ok: true }), poke: async () => void calls.push(id) }) });
    const env = { DB: {}, MASTER_KEY: key, LIMITER: ns([]), NODELINK: ns(poked), ASSETS: {} } as any;
    await createRuntime({} as WebAssembly.Module).getPanel(env, "https://x.test");
    const o = initCalls[0]!;
    expect(o.assets).toBeTypeOf("function");
    expect(await o.limit({ name: "login", key: "k" })).toEqual({ ok: true });
    expect(await o.nodeLink.poke(["nod_1", "nod_2"])).toBe(0);
    expect(poked).toEqual(["nod_1", "nod_2"]);
  });
});
