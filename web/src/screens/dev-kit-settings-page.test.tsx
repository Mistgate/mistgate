import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { langs, setLang } from "@/i18n";
import { addressOf, scenes, seeded, SettingsSceneBody, variantOf } from "./dev-kit-settings-page";

// The scenes of /dev-kit?only=pn-settings draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", assetUrl: (p: string) => p, auth: {}, instance: {}, ops: {}, updates: {}, backups: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string }) => (
    <a className={className} href={to} {...rest}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
  useParams: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
  document.body.innerHTML = "";
});

async function draw(address: string) {
  const v = variantOf(new URLSearchParams(address));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(v);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <SettingsSceneBody tab={v.tab} state={v.state} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const body = () => document.body.textContent ?? "";
const states = (id: string) => scenes.find((s) => s.id === id)!;

describe("the scenes of Settings", () => {
  it.each(scenes.map((s) => [s.id, s] as const))("%s draws", async (_id, s) => {
    await draw(addressOf(s));
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(document.querySelectorAll("[role=tab]")).toHaveLength(8);
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(sx|settings|set|obs|pres)\.[a-z]/);
    if (s.state === "load") expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    if (s.state === "err") expect(document.querySelector("[role=alert]")?.textContent).toContain("Settings failed to load");
  });

  // the first scene of every tab in each language, right to left included
  it.each(langs.flatMap((lang) => ["settings-if", "settings-sys", "settings-adm", "settings-dom"].map((id) => [lang, id] as const)))("%s: %s draws", async (lang, id) => {
    setLang(lang);
    await draw(addressOf(states(id)));
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(sx|settings|set|obs|pres)\.[a-z]/);
  });

  it("Interface draws its three previews, both cards and the save row for the owner", async () => {
    await draw(addressOf(states("settings-if")));
    expect(document.querySelectorAll(".sx-pv .sx-mock")).toHaveLength(3);
    expect(document.querySelectorAll(".sx-if section")).toHaveLength(2);
    expect(document.querySelector(".sx-save button")).not.toBeNull();
  });

  it("Interface holds the card for everyone shut for a helper and an observer", async () => {
    for (const id of ["settings-if-helper", "settings-if-readonly"]) {
      await draw(addressOf(states(id)));
      expect(document.querySelector(".sx-all.ro")).not.toBeNull();
      expect(document.querySelector(".sx-save")).toBeNull();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("Interface starts a scene from the logo that is too big, and from one just taken", async () => {
    await draw(addressOf(states("settings-if-big")));
    expect(body()).toContain("The file is larger than 64 KB");
    act(() => root?.unmount());
    host?.remove();
    await draw(addressOf(states("settings-if-new")));
    expect(body()).toContain("Uploaded; save so everyone sees it");
    expect(document.querySelector(".sx-ml.new")).not.toBeNull();
  });

  it("System draws the four columns of figures, a bar under load, and the owner's cards", async () => {
    await draw(addressOf(states("settings-sys")));
    expect(document.querySelectorAll(".sx-rg")).toHaveLength(4);
    expect(document.querySelectorAll(".sx-logs .sx-log").length).toBeGreaterThan(3);
    expect(body()).toContain("Node updates");
    expect(body()).toContain("About Mistgate");
    act(() => root?.unmount());
    host?.remove();
    await draw(addressOf(states("settings-sys-hot")));
    expect(document.querySelectorAll(".sx-r.bad").length).toBeGreaterThan(0);
    expect(document.querySelectorAll(".sx-r.warn").length).toBeGreaterThan(0);
  });

  it("System shows a helper no diagnostics, and a panel that is not Linux its reason", async () => {
    await draw(addressOf(states("settings-sys-helper")));
    expect(document.querySelector(".sx-logs")).toBeNull();
    expect(body()).not.toContain("Retention");
    act(() => root?.unmount());
    host?.remove();
    await draw(addressOf(states("settings-sys-none")));
    expect(body()).toContain("not a Linux host");
  });

  it("System opens the metrics window as the scene says", async () => {
    await draw(addressOf(states("settings-sys-made")));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("Token created");
    act(() => root?.unmount());
    host?.remove();
    await draw(addressOf(states("settings-sys-token")));
    expect(document.querySelector("[role=dialog]")?.textContent).toContain("The old token stops working at once");
  });

  it("the tab picked inside a scene is drawn from the same cache", async () => {
    await draw(addressOf(states("settings-if")));
    const adm = [...document.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent === "Admins")!;
    await act(async () => void adm.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(body()).toContain("Signs in with");
  });
});
