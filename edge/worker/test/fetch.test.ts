import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import { fetchPanelRequest, scheduledPanel } from "../src/fetch";
import { getPanel } from "../src/panel";

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

  it("logs initialization errors and skips the tick", async () => {
    const error = new Error("initialization failed");
    mockedGetPanel.mockRejectedValue(error);
    const log = vi.spyOn(console, "error").mockImplementation(() => {});
    const ctx = { waitUntil: vi.fn() } as unknown as ExecutionContext;

    await expect(scheduledPanel({ scheduledTime: 1 } as ScheduledController, {} as Env, ctx)).resolves.toBeUndefined();

    expect(log).toHaveBeenCalledWith("panel init failed:", error.message);
    expect(ctx.waitUntil).not.toHaveBeenCalled();
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
    expect(ctx.waitUntil).not.toHaveBeenCalled();
    log.mockRestore();
  });
});
