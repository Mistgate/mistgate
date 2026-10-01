import { act, createRef } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Turnstile, type TurnstileHandle } from "./turnstile";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(async () => {
  // a script that never loaded would keep the loader waiting for the next test
  await act(async () => scripts().forEach((s) => s.dispatchEvent(new Event("error"))));
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  delete window.turnstile;
  document.querySelectorAll("script").forEach((s) => s.remove());
});

async function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(ui));
  return host;
}

const scripts = () => [...document.querySelectorAll<HTMLScriptElement>("script[src*='challenges.cloudflare.com']")];

function fakeApi() {
  const opts: Record<string, (...a: unknown[]) => unknown>[] = [];
  const raw: Record<string, unknown>[] = [];
  const api = {
    render: vi.fn((_el: HTMLElement, o: Record<string, unknown>) => {
      raw.push(o);
      opts.push(o as never);
      return `w${raw.length}`;
    }),
    reset: vi.fn(),
    remove: vi.fn(),
  };
  window.turnstile = api;
  return { api, raw, opts };
}

describe("Turnstile", () => {
  it("loads Cloudflare's script only once it is mounted", async () => {
    expect(scripts()).toHaveLength(0);
    await mount(<Turnstile siteKey="0x4AAA" onToken={() => {}} />);
    expect(scripts()).toHaveLength(1);
    expect(scripts()[0]!.src).toBe("https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit");
  });

  it("renders explicitly with the site key, theme and language, and reports the token, expiry and reset", async () => {
    const { api, raw, opts } = fakeApi();
    const onToken = vi.fn();
    const handle = createRef<TurnstileHandle>();
    await mount(<Turnstile siteKey="0x4AAA" onToken={onToken} ref={handle} />);
    expect(scripts()).toHaveLength(0); // already loaded by someone else: nothing to fetch
    expect(api.render).toHaveBeenCalledTimes(1);
    expect(raw[0]).toMatchObject({ sitekey: "0x4AAA", theme: "dark", language: expect.stringMatching(/^(en|ru)$/), size: "flexible" });

    act(() => void opts[0]!.callback!("tok-1"));
    expect(onToken).toHaveBeenLastCalledWith("tok-1");
    act(() => void opts[0]!["expired-callback"]!());
    expect(onToken).toHaveBeenLastCalledWith(null);

    act(() => void opts[0]!.callback!("tok-2"));
    act(() => handle.current!.reset()); // a token works once: the sign-in form resets after each attempt
    expect(api.reset).toHaveBeenCalledWith("w1");
    expect(onToken).toHaveBeenLastCalledWith(null);

    act(() => root!.unmount());
    root = null;
    expect(api.remove).toHaveBeenCalledWith("w1");
  });

  it("shows a retry when the script cannot load, and tries again with a fresh script", async () => {
    const onToken = vi.fn();
    const el = await mount(<Turnstile siteKey="0x4AAA" onToken={onToken} />);
    await act(async () => void scripts()[0]!.dispatchEvent(new Event("error")));
    const retry = el.querySelector("button")!;
    expect(retry).not.toBeNull();
    expect(scripts()).toHaveLength(0); // the dead tag is gone, so a retry is a real retry
    expect(onToken).toHaveBeenLastCalledWith(null);
    await act(async () => retry.click());
    expect(scripts()).toHaveLength(1);
    expect(el.querySelector("button")).toBeNull();
  });

  it("says the check failed (not that Cloudflare is unreachable) when a loaded widget keeps erroring, and retry starts a new widget", async () => {
    const { api, opts } = fakeApi();
    const el = await mount(<Turnstile siteKey="2x00000000000000000000AB" onToken={() => {}} />);
    // two transient errors are retried by the widget itself; the third gives up
    for (let i = 0; i < 3; i++) await act(async () => void opts[0]!["error-callback"]!());
    const retry = el.querySelector("button")!;
    expect(retry.textContent).toContain("The check could not be completed.");
    expect(retry.textContent).not.toContain("Cloudflare reachable");
    await act(async () => retry.click());
    expect(api.render).toHaveBeenCalledTimes(2);
    expect(el.querySelector("button")).toBeNull();
  });
});
