import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { frames, pageDataOf, partsOf, sceneOf, seeded } from "./dev-kit-profile";
import { tabFrames, tabsData } from "./dev-kit-profile-tabs";
import { ProfilePage } from "./profiles/page";

// The scenes of /dev-kit?only=pn-profile that are the page's own (the head, the verdict, the Settings tab, the window "Save changes?"): every
// state a scene names must draw, in every language, and say what it is made to say. The tabs' and windows' scenes are tested beside theirs.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, profiles: {}, awg: {}, groups: {}, users: {}, nodes: {}, health: {}, relays: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to: _t, render: _r, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string; render?: unknown }) => (
    <a className={className} {...rest}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
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

const own = new Set(tabFrames.map((f) => f.id));
const states = [...new Set(frames.filter((f) => !own.has(f.id)).map((f) => f.state))];

async function draw(state: string, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfilePage data={tabsData(pageDataOf(partsOf(state).profile), state)} scene={sceneOf(state)} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const body = () => document.body.textContent ?? "";

describe("the scenes of the profile page's Settings tab", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(pf|ps)\.[a-z]/);
    if (state === "hy1.settings") {
      expect(body()).toContain("Did not start on fi1");
      expect(document.querySelectorAll(".pp-sec").length).toBeGreaterThan(2);
    }
    if (state === "hy1.settings.dirty") expect(document.querySelector(".pf-save.on")?.textContent).toContain("Changed 1");
    if (state === "hy1.settings.impact") expect(document.querySelector("[data-shot=impact]")?.textContent).toContain("Where");
    if (state === "hy1.settings.errors") {
      expect(document.querySelector(".pp-rmc")?.textContent).toContain("cannot be saved");
      expect(document.querySelector(".pf-save button:last-child")?.getAttribute("aria-disabled")).toBe("true");
    }
    if (state === "hy1.settings.saved") expect(document.querySelector(".pf-save.ok")?.textContent).toContain("Saved");
    if (state === "hy1.settings.panelold") {
      expect(document.querySelector(".pf-save.on")?.textContent).toContain("The panel was updated: the page is the old version");
      expect(document.querySelector(".pf-save button:last-child")?.textContent).toBe("Reload");
    }
    if (state === "hy1.settings.stale") expect(body()).toContain("The profile was changed elsewhere");
    if (state === "hy3.settings") expect(body()).toContain("Not on any node");
    if (state === "vl3.settings") expect(body()).toContain("In no group");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the page and its window draw in %s", async (lang) => {
    setLang(lang);
    await draw("hy1.settings.impact");
    expect(document.querySelector("[data-shot=impact]")).not.toBeNull();
    expect(body()).not.toContain("undefined");
  });

  it("gives an observer the form to read: no bar, no Delete, the rows out of reach", async () => {
    await draw("hy1.settings", Role.READONLY);
    expect(document.querySelector(".pf-save")).toBeNull();
    expect(body()).not.toContain("Danger zone");
    expect(document.querySelector(".pp-form[inert]")).not.toBeNull();
  });
});

describe("the scenes of AmneziaWG's settings", () => {
  const rows = (card: number) => [...document.querySelectorAll("section.pp-sec")].filter((s) => s.querySelector(".pp-sh"))[card];
  it("aw1 is the board's three cards in the board's order, with the remarks, the score and the oldest clients", async () => {
    await draw("aw1.settings");
    const titles = [...document.querySelectorAll("section.pp-sec .pp-sh h3")].map((h) => h.textContent);
    expect(titles.slice(0, 4)).toEqual(["Issues 2", "Basics", "Masquerade and bypass", "Network"]);
    expect(titles.at(-1)).toBe("Apps");
    // the remarks of the server have the board's buttons
    const rm = [...document.querySelectorAll(".pp-rm")].map((r) => r.textContent);
    expect(rm[0]).toContain("Keepalive 31 s is longer than the UDP timeout of many NATs");
    expect(rm[0]).toContain("Set 25");
    expect(rm[1]).toContain("DNS traffic usually goes over port 53");
    expect(rm[1]).toContain("Port 53critical");
    expect(document.querySelector(".pp-score")?.textContent).toBe("Disguise 88/100");
    expect(document.querySelector(".pf-cl")?.textContent).toContain("AmneziaVPN 5.0.1.5");
    expect(document.querySelector(".pr-fm")?.textContent).toContain("AmneziaVPN keys");
    // the bar comes after the danger zone, as the board has it
    const col = document.querySelector(".pp-grid > .col")!;
    expect([...col.children].map((c) => (c.classList.contains("pf-save") ? "bar" : c.classList.contains("up-dz") ? "danger" : "")).filter(Boolean)).toEqual(["danger", "bar"]);
    expect(document.querySelector(".up-dz .btn.up-del")?.textContent).toBe("Delete…");
    expect(rows(1)).toBeTruthy();
  });

  it("the remarks' buttons put the numbers in the form: the keepalive 25, the look's port 53", async () => {
    await draw("aw1.settings");
    const act2 = async (el: Element | undefined) => {
      await act(async () => (el as HTMLElement).click());
      for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    };
    await act2([...document.querySelectorAll(".pp-rm .btn")].find((b) => b.textContent?.includes("Set 25")));
    expect(document.querySelector(".pf-save.on")?.textContent).toContain("Changed 1");
    // Why? opens the reason under the remark
    await act2([...document.querySelectorAll(".pp-rm .an-lk")].find((b) => b.textContent === "Why?"));
    expect(document.querySelector(".pp-rm em")?.textContent).toContain("Many NATs close a UDP connection sooner");
  });

  it("2.0 picks its card and says what it is for; the folds, the score and the oldest clients are 2.0's", async () => {
    await draw("aw2.settings");
    expect(document.querySelector("[data-k=version] [role=radio][aria-checked=true]")?.textContent).toContain("AmneziaWG 2.0");
    expect(document.querySelector("[data-k=version] .pp-rmk")?.textContent).toContain("only for old clients");
    expect(document.querySelector(".pp-score")?.className).toContain("part");
    expect(document.querySelector(".pf-cl")?.textContent).toContain("AmneziaVPN 4.8.12.9");
  });

  it.each([
    ["aw1.settings.score", () => expect(document.querySelector(".pp-scb h3")?.textContent).toBe("What the score is made of")],
    ["aw1.settings.adv", () => expect(document.querySelectorAll(".pp-hgr").length).toBe(5)],
    ["aw1.settings.adv.show", () => expect((document.querySelector("[data-k='obfuscation.jc'] input") as HTMLInputElement).value).toBe("5")],
    ["aw1.settings.adv.conf", () => expect(document.querySelector(".pf-conf textarea")).not.toBeNull()],
    ["aw1.settings.net", () => expect(document.querySelectorAll(".pf-lock").length).toBe(2)],
    ["aw1.settings.net.free", () => expect(document.querySelectorAll("[data-k=subnets] input").length).toBe(2)],
    ["aw1.settings.warp", () => expect(document.querySelector("[data-k=egress] .pp-twin")?.textContent).toBe("Make a copy without WARP…")],
  ])("%s says what it is made to say", async (state, check) => {
    await draw(state);
    check();
    expect(body()).not.toContain("undefined");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the three cards, the folds and the score draw in %s with no key left bare", async (lang) => {
    setLang(lang);
    await draw("aw1.settings.score.adv.net.show");
    expect(document.querySelectorAll(".pp-hgr").length).toBe(5);
    expect(body()).not.toMatch(/\b(awg|pf|profiles)\.[a-z]+\./);
    expect(body()).not.toContain("undefined");
  });

  it("an observer reads the cards: no Random port that works, the bar and the danger zone are gone", async () => {
    await draw("aw1.settings", Role.READONLY);
    expect(document.querySelector(".pf-save")).toBeNull();
    expect(document.querySelector(".pp-form[inert]")).not.toBeNull();
    expect(document.querySelector(".pp-rm .btn")).toBeNull();
  });
});
