import { Code, ConnectError } from "@connectrpc/connect";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import { staleVersion } from "./stale";

describe("staleVersion", () => {
  it("is the server version when the page was loaded from another one", () => {
    expect(staleVersion("0.1.35", "0.1.36")).toBe("0.1.36");
  });
  it("is empty when the versions match", () => {
    expect(staleVersion("0.1.36", "0.1.36")).toBe("");
  });
  it("is empty when either side did not say (dev server, older panel, signed out)", () => {
    expect(staleVersion("", "0.1.36")).toBe("");
    expect(staleVersion("0.1.36", "")).toBe("");
    expect(staleVersion("", "")).toBe("");
  });
});

// loadedVersion is read once when the module loads, like on a page load: each case loads the modules afresh.
async function page(meta: string) {
  vi.resetModules();
  document.head.innerHTML = meta ? `<meta name="mistgate-version" content="${meta}">` : "";
  const [stale, banner] = await Promise.all([import("./stale"), import("@/components/stale-toast")]);
  return { ...stale, StaleToast: banner.StaleToast };
}
const headers = (v: string) => new Headers({ "X-Mistgate-Version": v });
const buttons = (el: HTMLElement) => [...el.querySelectorAll("button")].map((b) => b.textContent);

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("en");
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});
function show(el: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(el));
  return host;
}

describe("StaleToast", () => {
  it("appears when the server reports another version than the page was loaded from, and offers a reload", async () => {
    const p = await page("0.1.35");
    const el = show(<p.StaleToast />);
    expect(el.textContent).toBe("");
    act(() => p.noteServerVersion(headers("0.1.35")));
    expect(el.textContent).toBe("");
    act(() => p.noteServerVersion(headers("0.1.36")));
    expect(el.querySelector("h2")?.textContent).toBe("The panel was updated to v0.1.36");
    expect(el.textContent).toContain("Reload the page so the admin works with the new version.");
    expect(buttons(el)).toContain("Reload");
  });

  it("speaks Russian too", async () => {
    setLang("ru");
    const p = await page("0.1.35");
    const el = show(<p.StaleToast />);
    act(() => p.noteServerVersion(headers("0.1.36")));
    expect(el.querySelector("h2")?.textContent).toBe("Панель обновлена до v0.1.36");
    expect(buttons(el)).toContain("Перезагрузить");
    setLang("en");
  });

  it("stays out of the way without a version in the page (Vite dev server, older panel)", async () => {
    const p = await page("");
    const el = show(<p.StaleToast />);
    act(() => p.noteServerVersion(headers("0.1.36")));
    expect(el.textContent).toBe("");
  });

  it("takes the version off a failed call too: the refused save is the one that matters", async () => {
    const p = await page("0.1.35");
    const el = show(<p.StaleToast />);
    const next = () => Promise.reject(new ConnectError("certificate mode is only available with TLS security", Code.InvalidArgument, headers("0.1.36")));
    await expect(p.versionInterceptor(next as never)({} as never)).rejects.toBeInstanceOf(ConnectError);
    expect(el.textContent).toContain("v0.1.36");
  });

  it("takes the version off an answer", async () => {
    const p = await page("0.1.35");
    const el = show(<p.StaleToast />);
    const res = await p.versionInterceptor((async () => ({ header: headers("0.1.36") })) as never)({} as never);
    expect(res).toBeTruthy();
    expect(el.textContent).toContain("v0.1.36");
  });
});
