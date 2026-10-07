import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Env } from "../src/env";
import { fetchPanelRequest } from "../src/fetch";
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
