import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import type { Settings } from "./model";
import { TextsTab } from "./texts";

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  subscriptions: { getSubscriptionSettings: vi.fn(), updateSubscriptionSettings: vi.fn() },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("ru");
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const settings = (over: Partial<Settings> = {}): Settings =>
  ({ title: "", announcement: "", supportUrl: "", updateIntervalHours: 12, serverNameTemplate: "{flag} {country} · {profile}", rules: [], defaultDnsPresetId: "", apps: [], userPage: { showAnnouncement: true, showSupport: true, showQr: true }, ...over }) as Settings;

async function mount(data: Parameters<typeof TextsTab>[0]["data"]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><TextsTab data={data} /></QueryClientProvider>));
}
const rows = () => [...document.querySelectorAll("[aria-live=polite] > div")].map((r) => r.textContent?.trim());
const type = (el: HTMLInputElement | HTMLTextAreaElement, v: string) =>
  act(async () => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, v);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the preview of names and texts", () => {
  const samples = [
    { node: "de1", countryCode: "DE", profile: "hy2", loadPercent: 64 },
    { node: "de1", countryCode: "DE", profile: "hy2 · WARP", loadPercent: 64 },
    { node: "nl1", countryCode: "NL", profile: "hy2", loadPercent: 22 },
  ];

  it("draws the busiest group's real servers by the subscription's rules, and says which group", async () => {
    await mount({ settings: settings(), effectiveTitle: "Mistgate", samples, sampleGroup: "Все", namesLanguage: "ru" });
    expect(rows()).toEqual(["DE · hy2 · 64%", "DE · hy2 · WARP · 64%", "NL · hy2 · 22%"]); // the flags are drawn, not text
    expect(document.body.textContent).toContain("Серверы группы «Все» — так их видят в Happ её люди.");
    expect(document.body.textContent).toContain("большая из скоростей RX и TX");
    await type(document.querySelector<HTMLInputElement>("#subs-template")!, "{node} · {profile}");
    expect(rows()).toEqual(["de1 · hy2 · 64%", "de1 · hy2 · WARP · 64%", "nl1 · hy2 · 22%"]);
  });

  it("an install that runs no server yet gets made-up ones, said to be made up", async () => {
    await mount({ settings: settings(), effectiveTitle: "Mistgate" });
    expect(rows()).toHaveLength(3);
    expect(document.body.textContent).toContain("Пример с выдуманными серверами.");
  });

  it("takes no longer a name or a template than the server stores (subsettings maxTitle, maxName)", async () => {
    await mount({ settings: settings(), effectiveTitle: "Mistgate" });
    expect(document.querySelector<HTMLInputElement>("#subs-title")!.maxLength).toBe(100);
    expect(document.querySelector<HTMLInputElement>("#subs-template")!.maxLength).toBe(100);
  });

  it("counts the announcement against what Happ shows, and the preview cuts it the way Happ gets it", async () => {
    await mount({ settings: settings(), effectiveTitle: "Mistgate", samples, sampleGroup: "Все", namesLanguage: "ru" });
    const count = () => document.querySelector("#subs-announce-count")!;
    expect(count().textContent).toBe("0 / 200 — столько видно в Happ");
    await type(document.querySelector<HTMLTextAreaElement>("#subs-announce")!, "обновляю серверы ".repeat(15).trim());
    expect(count().textContent).toBe("254 / 200 — в Happ видно только начало");
    expect(count().className).toContain("text-warn-text");
    const shown = document.querySelector(".whitespace-pre-line")!.textContent!;
    expect(shown.endsWith(" обновляю…")).toBe(true); // the cut fell inside "серверы": back to the word before
    expect([...shown].length).toBeLessThanOrEqual(200);
  });
});
