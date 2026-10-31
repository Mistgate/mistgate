import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import { fetchPanelRequest, scheduledPanel } from "../src/fetch";
import { getPanel } from "../src/panel";
import { MAX_BODY_BYTES } from "../src/shell";

vi.mock("../src/panel", () => ({ getPanel: vi.fn() }));

const mockedGetPanel = vi.mocked(getPanel);

describe("fetchPanelRequest", () => {
  beforeEach(() => vi.resetAllMocks());

  it("passes the panel's background promise to waitUntil without delaying the response", async () => {
    let finishBackground!: () => void;
    const background = new Promise<void>((resolve) => {
      finishBackground = resolve;
    });
    const panel = {
      fetch: vi.fn().mockResolvedValue({
        status: 200,
        headers: [],
        body: new Uint8Array(),
        waitUntil: background,
      }),
    };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;

    const response = await fetchPanelRequest(new Request("https://example.com/sub/token"), {} as Env, ctx);

    expect(response.status).toBe(200);
    expect(ctx.waitUntil).toHaveBeenCalledWith(background);
    let completed = false;
    void background.then(() => { completed = true; });
    await Promise.resolve();
    expect(completed).toBe(false);
    finishBackground();
    await background;
  });
});

describe("fetchPanelRequest keeps started work alive past a client disconnect", () => {
  beforeEach(() => vi.resetAllMocks());

  const answer = { status: 200, headers: [], body: new Uint8Array(), waitUntil: Promise.resolve() };
  const settledFlag = (promise: Promise<unknown>) => {
    const flag = { done: false };
    void promise.then(() => (flag.done = true));
    return flag;
  };
  const context = () => {
    const kept: Promise<unknown>[] = [];
    const ctx = { waitUntil: vi.fn((promise: Promise<unknown>) => void kept.push(promise)) } as unknown as ExecutionContext;
    return { ctx, kept };
  };

  it("hands the panel promise to waitUntil before it is awaited, and the promise of panel.fetch once it is running", async () => {
    let startPanel!: (panel: any) => void;
    mockedGetPanel.mockReturnValue(new Promise((resolve) => (startPanel = resolve)));
    let finishFetch!: (value: typeof answer) => void;
    const panelFetch = vi.fn(() => new Promise<typeof answer>((resolve) => (finishFetch = resolve)));
    const { ctx, kept } = context();

    const response = fetchPanelRequest(new Request("https://example.com/admin/x"), {} as Env, ctx);
    expect(kept).toHaveLength(1); // the start is already protected while the request still waits for it
    const started = settledFlag(kept[0]!);
    await Promise.resolve();
    expect(started.done).toBe(false);

    startPanel({ fetch: panelFetch });
    await vi.waitFor(() => expect(panelFetch).toHaveBeenCalled());
    expect(started.done).toBe(true);
    expect(kept).toHaveLength(2);
    const running = settledFlag(kept[1]!);
    await Promise.resolve();
    expect(running.done).toBe(false);

    finishFetch(answer);
    expect((await response).status).toBe(200);
    expect(running.done).toBe(true);
  });

  it("a failed start or fetch does not make the kept-alive promise reject, and the client still gets its 503 or 500", async () => {
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const { ctx, kept } = context();
    mockedGetPanel.mockRejectedValue(new Error("d1 down"));
    const unavailable = await fetchPanelRequest(new Request("https://example.com/"), {} as Env, ctx);
    expect(unavailable.status).toBe(503);
    expect(unavailable.headers.get("Retry-After")).toBe("5");

    mockedGetPanel.mockResolvedValue({ fetch: vi.fn().mockRejectedValue(new Error("go failed")) } as unknown as Awaited<ReturnType<typeof getPanel>>);
    expect((await fetchPanelRequest(new Request("https://example.com/"), {} as Env, ctx)).status).toBe(500);

    expect(kept).toHaveLength(3);
    await expect(Promise.all(kept)).resolves.toBeDefined();
    log.mockRestore();
  });
});

describe("scheduledPanel", () => {
  beforeEach(() => vi.resetAllMocks());

  it("passes the scheduled time to the panel and its background promise to waitUntil", async () => {
    let finishBackground!: () => void;
    const background = new Promise<void>((resolve) => {
      finishBackground = resolve;
    });
    const cron = vi.fn().mockResolvedValue({ waitUntil: background });
    const panel = { cron };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const env = {} as Env;
    const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;
    const controller = { scheduledTime: 1_800_000_000_000 } as ScheduledController;

    await scheduledPanel(controller, env, ctx);

    expect(mockedGetPanel).toHaveBeenCalledWith(env, "");
    expect(cron).toHaveBeenCalledWith({ at: controller.scheduledTime });
    expect(ctx.waitUntil).toHaveBeenCalledWith(background);
    finishBackground();
  });

  it("hands the panel promise and the cron promise to waitUntil", async () => {
    let startPanel!: (panel: any) => void;
    mockedGetPanel.mockReturnValue(new Promise((resolve) => (startPanel = resolve)));
    const kept: Promise<unknown>[] = [];
    const ctx = { waitUntil: vi.fn((promise: Promise<unknown>) => void kept.push(promise)) } as unknown as ExecutionContext;
    const cron = vi.fn().mockResolvedValue({ waitUntil: Promise.resolve() });

    const tick = scheduledPanel({ scheduledTime: 1 } as ScheduledController, {} as Env, ctx);
    expect(kept).toHaveLength(1);
    startPanel({ cron });
    await tick;
    expect(cron).toHaveBeenCalled();
    expect(kept).toHaveLength(3); // the start, the cron call, the cron's own after-work
  });

  it("logs initialization errors and skips the tick", async () => {
    const error = new Error("initialization failed");
    mockedGetPanel.mockRejectedValue(error);
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;

    await expect(scheduledPanel({ scheduledTime: 1 } as ScheduledController, {} as Env, ctx)).resolves.toBeUndefined();

    expect(log).toHaveBeenCalledWith("panel init failed:", error.message);
    expect(ctx.waitUntil).toHaveBeenCalledTimes(1); // only the kept-alive start; no cron work was handed over
    log.mockRestore();
  });

  it("logs cron errors without throwing", async () => {
    const error = new Error("tick failed");
    const cron = vi.fn().mockRejectedValue(error);
    mockedGetPanel.mockResolvedValue({ cron } as unknown as Awaited<ReturnType<typeof getPanel>>);
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;

    await expect(scheduledPanel({ scheduledTime: 1 } as ScheduledController, {} as Env, ctx)).resolves.toBeUndefined();

    expect(log).toHaveBeenCalledWith("panel cron failed:", error.message);
    expect(ctx.waitUntil).toHaveBeenCalledTimes(2); // the kept-alive start and cron call; no after-work came back
    log.mockRestore();
  });
});

describe("fetchPanelRequest streams a marked node update download", () => {
  beforeEach(() => vi.resetAllMocks());

  it("answers a 204 with the asset marker with the release stream, and sends upstream no Authorization", async () => {
    const upstream = vi.fn(async (_url: URL, _init: RequestInit) => new Response("file-bytes", { status: 206, headers: { "Content-Length": "10", "Content-Range": "bytes 5-14/15" } }));
    vi.stubGlobal("fetch", upstream);
    try {
      const answer = { status: 204, headers: [["X-Mistgate-Asset", "https://github.com/example/mistgate/releases/download/v1.2.3/mistgate-node-linux-amd64"]], body: new Uint8Array(), waitUntil: Promise.resolve() };
      mockedGetPanel.mockResolvedValue({ fetch: vi.fn().mockResolvedValue(answer) } as unknown as Awaited<ReturnType<typeof getPanel>>);
      const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;
      const request = new Request("https://example.com/p/link/update/nl1/mistgate-node-linux-amd64", { headers: { Authorization: "Bearer invented-token", Range: "bytes=5-" } });
      const response = await fetchPanelRequest(request, {} as Env, ctx);
      expect(response.status).toBe(206);
      expect(await response.text()).toBe("file-bytes");
      expect(response.headers.get("content-range")).toBe("bytes 5-14/15");
      expect(new Headers(upstream.mock.calls[0]![1].headers).has("authorization")).toBe(false);
      expect(new Headers(upstream.mock.calls[0]![1].headers).get("range")).toBe("bytes=5-");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe("fetchPanelRequest refuses an oversized body before the panel sees it", () => {
  beforeEach(() => vi.resetAllMocks());

  const answer = { status: 200, headers: [], body: new Uint8Array(), waitUntil: Promise.resolve() };
  const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;
  const streamOf = (sizes: number[]) =>
    new ReadableStream<Uint8Array>({
      start(c) {
        for (const n of sizes) c.enqueue(new Uint8Array(n));
        c.close();
      },
    });

  it("by Content-Length, and while a body without one streams in", async () => {
    const panel = { fetch: vi.fn().mockResolvedValue(answer) };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const declared = new Request("https://example.com/x", { method: "POST", body: "a", headers: { "Content-Length": String(MAX_BODY_BYTES + 1) } });
    expect((await fetchPanelRequest(declared, {} as Env, ctx)).status).toBe(413);
    const streamed = new Request("https://example.com/x", { method: "POST", body: streamOf([MAX_BODY_BYTES, 1]), duplex: "half" } as RequestInit);
    expect((await fetchPanelRequest(streamed, {} as Env, ctx)).status).toBe(413);
    expect(panel.fetch).not.toHaveBeenCalled();
  });

  it("passes a body of exactly the cap whole", async () => {
    const panel = { fetch: vi.fn().mockResolvedValue(answer) };
    mockedGetPanel.mockResolvedValue(panel as unknown as Awaited<ReturnType<typeof getPanel>>);
    const request = new Request("https://example.com/x", { method: "POST", body: streamOf([MAX_BODY_BYTES - 7, 7]), duplex: "half" } as RequestInit);
    expect((await fetchPanelRequest(request, {} as Env, ctx)).status).toBe(200);
    expect(panel.fetch.mock.calls[0]![0].body.byteLength).toBe(MAX_BODY_BYTES);
  });
});
