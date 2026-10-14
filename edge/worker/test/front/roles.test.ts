import { describe, expect, it, vi } from "vitest";
import type { PanelEnv } from "../../src/env";
import type { Runtime } from "../../src/runtime";
import { adminRole, jobsRole, linkEntrypoint, linkRole, provisionRole, publicRole } from "../../src/roles/shared";

// The role scripts around the Go panel, with a fake Runtime in place of the wasm (the real Go side of each role is tested in
// Go, cmd/mistgate-edge-*). What matters here is which door of the panel each role opens and what it hands on.

const answer = (status = 200, headers: [string, string][] = [], body = "") => ({ status, headers, body: new TextEncoder().encode(body), waitUntil: Promise.resolve() });

function fake(panel: Record<string, unknown> = {}) {
  const getPanel = vi.fn(async (_env: PanelEnv, _origin: string) => panel as never);
  const getServePanel = vi.fn(async () => panel as never);
  return { getPanel, getServePanel, runtime: { getPanel, getServePanel } as Runtime };
}

const ctx = () => ({ waitUntil: vi.fn(), passThroughOnException: () => {}, props: {} }) as unknown as ExecutionContext;
const env = (extra: Record<string, unknown> = {}) => ({ DB: {}, MASTER_KEY: "ab".repeat(32), ...extra }) as never;

describe("public and admin roles", () => {
  it.each([
    ["public", publicRole],
    ["admin", adminRole],
  ])("%s: a request goes to the panel of its own runtime and its answer comes back", async (_name, make) => {
    const goFetch = vi.fn(async (_request: unknown) => answer(201, [["X-A", "1"]], "from go"));
    const f = fake({ fetch: goFetch });
    const c = ctx();
    const response = await make(f.runtime).fetch(new Request("https://p.example.test/x?y=1", { method: "POST", body: "in" }), env(), c);
    expect(response.status).toBe(201);
    expect(await response.text()).toBe("from go");
    expect(response.headers.get("X-A")).toBe("1");
    expect(f.getPanel).toHaveBeenCalledWith(expect.anything(), "https://p.example.test");
    expect(goFetch).toHaveBeenCalledWith(expect.objectContaining({ method: "POST", url: "https://p.example.test/x?y=1" }));
    expect(c.waitUntil).toHaveBeenCalledTimes(1);
  });

  it("a panel that cannot start answers 503 and the next request tries again", async () => {
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    const f = fake();
    f.getPanel.mockRejectedValueOnce(new Error("no mgPanel"));
    const handler = publicRole(f.runtime);
    expect((await handler.fetch(new Request("https://p.example.test/"), env(), ctx())).status).toBe(503);
    f.getPanel.mockResolvedValueOnce({ fetch: async () => answer(200, [], "ok") } as never);
    expect(await (await handler.fetch(new Request("https://p.example.test/"), env(), ctx())).text()).toBe("ok");
    quiet.mockRestore();
  });

  it("the admin role still forwards what the panel marks for the Provisioner", async () => {
    const forwarded: Request[] = [];
    const provisioner = {
      fetch: async (r: Request) => {
        forwarded.push(r);
        return new Response("installed", { status: 202 });
      },
    };
    const ns = { idFromName: (n: string) => n, get: () => provisioner };
    const f = fake({ fetch: async () => answer(204, [["X-Mistgate-Forward", "provision;v=1.2.3"]]) });
    const response = await adminRole(f.runtime).fetch(new Request("https://p.example.test/adm/api/p", { method: "POST", body: "x" }), env({ PROVISIONER: ns }), ctx());
    expect([response.status, await response.text()]).toEqual([202, "installed"]);
    expect(forwarded[0]!.headers.get("X-Mistgate-Build")).toBe("1.2.3");
  });
});

describe("link role", () => {
  it("hands the upgrade the panel marks to the NodeLink object of its node, in FRONT's script", async () => {
    const forwarded: { id: string; request: Request }[] = [];
    const ns = {
      idFromName: (n: string) => n,
      get: (id: string) => ({
        fetch: async (request: Request) => {
          forwarded.push({ id, request });
          return new Response("socket", { status: 202 });
        },
      }),
    };
    const f = fake({ fetch: async () => answer(204, [["X-Mistgate-Link", "nod_7"]]) });
    const upgrade = new Request("https://p.example.test/lnk/link/nod_7", { headers: { Upgrade: "websocket" } });
    const response = await linkRole(f.runtime).fetch(upgrade, env({ NODELINK: ns }), ctx());
    expect([response.status, await response.text()]).toEqual([202, "socket"]);
    expect(forwarded).toHaveLength(1);
    expect(forwarded[0]!.id).toBe("nod_7");
    expect(forwarded[0]!.request).toBe(upgrade);
  });

  it("an ordinary answer of the panel is returned as it is", async () => {
    const f = fake({ fetch: async () => answer(200, [["Content-Type", "application/json"]], "{}") });
    const response = await linkRole(f.runtime).fetch(new Request("https://p.example.test/lnk/enroll", { method: "POST" }), env({ NODELINK: {} }), ctx());
    expect([response.status, response.headers.get("Content-Type"), await response.text()]).toEqual([200, "application/json", "{}"]);
  });

  it("the PanelLink entrypoint calls mgPanel.link with the op and its arguments, and moves waitUntil to its own context", async () => {
    const after = Promise.resolve();
    const link = vi.fn(async (op: string, args: Record<string, unknown>) => ({ op, args, waitUntil: after }));
    const f = fake({ link });
    const Entrypoint = linkEntrypoint(f.runtime);
    const c = ctx();
    const entry = new Entrypoint(c, env({ PUBLIC_URL: "https://p.example.test" }));
    const out = (await entry.challenge("p.example.test")) as unknown as Record<string, unknown>;
    expect(link).toHaveBeenCalledWith("challenge", { audience: "p.example.test" });
    expect(out).toMatchObject({ op: "challenge", args: { audience: "p.example.test" } });
    expect(out).not.toHaveProperty("waitUntil"); // a promise cannot cross the RPC
    expect(c.waitUntil).toHaveBeenCalledWith(after);
    expect(f.getPanel).toHaveBeenCalledWith(expect.anything(), "https://p.example.test");
    const input = { nodeId: "nod_1", state: null, until: 5, event: { kind: "alarm" as const, at: 1 } };
    await entry.step(input);
    expect(link).toHaveBeenLastCalledWith("step", input);
  });
});

describe("jobs role", () => {
  it("answers 404 to a request without starting Go (nothing routes to it)", async () => {
    const f = fake();
    const response = jobsRole(f.runtime).fetch();
    expect(response.status).toBe(404);
    expect(f.getPanel).not.toHaveBeenCalled();
  });

  it("runs the cron tick in the panel and keeps its after-work alive", async () => {
    const after = Promise.resolve();
    const cron = vi.fn(async (_tick: { at: number }) => ({ waitUntil: after }));
    const f = fake({ cron });
    const c = ctx();
    await jobsRole(f.runtime).scheduled({ scheduledTime: 1_700_000_000_000 } as ScheduledController, env({ NODELINK: {} }), c);
    expect(cron).toHaveBeenCalledWith({ at: 1_700_000_000_000 });
    expect(c.waitUntil).toHaveBeenCalledWith(after);
  });

  it("a tick whose panel cannot start is skipped quietly (the next minute tries again)", async () => {
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    const f = fake();
    f.getPanel.mockRejectedValueOnce(new Error("not initialised"));
    await expect(jobsRole(f.runtime).scheduled({ scheduledTime: 1 } as ScheduledController, env(), ctx())).resolves.toBeUndefined();
    quiet.mockRestore();
  });
});

describe("provision role", () => {
  it("serves nobody (404) and loads the serve panel only when the Provisioner needs it", async () => {
    const f = fake();
    const entry = provisionRole(f.runtime, (() => {
      throw new Error("not used here");
    }) as never);
    expect(entry.fetch().status).toBe(404);
    expect(f.getServePanel).not.toHaveBeenCalled();
  });
});
