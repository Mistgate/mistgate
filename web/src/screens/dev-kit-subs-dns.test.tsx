import { QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { fill, setLang, type Lang } from "@/i18n";
import { SubscriptionsScreen } from "./subscriptions";
import { dnsScenes } from "./dev-kit-subs-dns";
import { allScenes, sceneOf } from "./dev-kit-subs-page";

// The scenes of /dev-kit?only=subs-page for the tab "DNS" draw the real tab on made-up data: every state of the board's switch and of
// what the product has beyond it must draw, in every language, and the page answers a click from memory.

vi.mock("@tanstack/react-router", async (orig) => ({
  ...(await orig<object>()),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useBlocker: () => ({ status: "idle" }),
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
  setLang("en");
});

const states = [...new Set(dnsScenes.map((s) => s.state))];

const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
async function draw(state: string, ms = 60, lang: Lang = "en") {
  setLang(lang);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const scene = sceneOf(state);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={scene.qc}>
        <ToastProvider>
          <SubscriptionsScreen demo={{ tab: scene.tab, user: scene.user }} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await wait(0);
  await wait(ms);
  for (let i = 0; i < 4; i++) await wait(0);
}
const text = () => document.body.textContent ?? "";
const rows = () => [...document.querySelectorAll(".dq-list .dq-r:not(.dq-rh)")];
const nameOf = (row: Element) => row.querySelector(".dq-n > b")!.childNodes[0]!.textContent;
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const input = () => document.querySelector<HTMLInputElement>("#dns-name")!;
const bar = () => document.querySelector(".sb-edf .dq-em")?.textContent;
const apps = () => [...document.querySelectorAll(".sb-get > div")].map((r) => [r.querySelector("span")!.textContent, r.querySelector("b")!.textContent, r.querySelector("em")!.textContent]);

/** The words the scenes of a state must carry, once it has drawn (a state not named only has to draw). */
const says: Record<string, () => void> = {
  dns: () => {
    expect(rows()).toHaveLength(5);
    expect(document.querySelector(".dq-now")!.textContent).toContain("Russia: .ru direct");
    expect(document.querySelector(".dq-now")!.textContent).toContain("Link-based apps (Happ, Klick, Mihomo Party)");
    expect(rows()[0]!.querySelector(".dq-def")!.getAttribute("aria-checked")).toBe("true");
    expect(rows().filter((r) => r.querySelector(".dq-def")!.getAttribute("aria-checked") === "true")).toHaveLength(1);
    expect(text()).toContain(en["sx.dns.foot"]);
    expect(button(en["subs.dns.new"])).toBeDefined();
  },
  "dns-new": () => {
    expect(input().value).toBe("");
    expect(button(en["subs.dns.create"])?.disabled).toBe(true);
    expect(text()).toContain(en["sx.dns.edNew"]);
    expect(apps().map((a) => a[1])).toEqual(Array(5).fill(en["subs.dns.res.none"]));
    expect(document.querySelector(".sb-emp")!.textContent).toBe(en["subs.dns.noServers"]);
  },
  "dns-cf": () => {
    expect(input().value).toBe("Cloudflare: без фильтров");
    expect(text()).toContain(en["sx.dns.edOwn"]); // a retired built-in that stayed as an own preset (migration 00157)
    expect(document.querySelectorAll(".sb-srvs .sb-srv")).toHaveLength(1);
    expect(apps()).toEqual([
      ["Happ", "DoH", ""],
      ["Mihomo", "DoH", ""],
      ["Amnezia", "Plain", en["subs.dns.why.amneziawg"]],
      ["Xray JSON", "DoH", ""],
      ["sing-box", "DoH", ""],
    ]);
    expect(document.querySelector("[role=radio][data-checked]")!.textContent).toBe("DoH");
  },
  "dns-cat": () => expect(rows().map(nameOf)).toEqual(["AdGuard: no ads", "No ads on the device"]),
  "dns-just": () => {
    expect(text()).toContain(fill(en["sx.dns.nowDef"], { name: "Quad9: защита от вредоносных" }));
    expect(document.querySelector(".dq-r.just")).not.toBeNull();
    expect(document.querySelector(".dq-now")!.textContent).toContain("Quad9");
  },
  "dns-users": () => {
    expect(rows()).toHaveLength(18);
    expect(rows()[0]!.querySelector(".dq-u")!.textContent).toBe("alldefault");
    expect([...document.querySelectorAll('.dq-cat [role="radio"]')].map((c) => c.textContent)).toContain("Own4");
  },
  "dns-ads": () => expect([...rows().find((r) => nameOf(r) === "No ads on the device")!.querySelectorAll(".dq-n > b > i")].map((i) => i.textContent)).toEqual(["built-in", "no ads"]),
  "dns-nodef": () => {
    expect(document.querySelector(".dq-now")!.textContent).toContain(en["sx.dns.noDef"]);
    expect(rows().every((r) => r.querySelector(".dq-def")!.getAttribute("aria-checked") === "false")).toBe(true);
  },
  "dns-empty": () => {
    expect(document.querySelector(".dq-emp")!.textContent).toBe(en["subs.dns.none"]);
    expect(document.querySelector(".dq-now")!.textContent).toContain(en["sx.dns.noDef"]);
  },
  "dns-long": () => expect(rows()).toHaveLength(4),
  "dns-load": () => expect(text()).toContain(en["common.loading"]),
  "dns-err": () => expect(text()).toContain(en["common.retry"]),
  "dns-helper": () => {
    expect(button(en["subs.dns.new"])).toBeUndefined();
    expect(rows().every((r) => !r.querySelector<HTMLButtonElement>(".dq-def")!.disabled)).toBe(true);
  },
  "dns-edge": () => expect(rows()).toHaveLength(18),
  "dns-own": () => {
    expect(input().value).toBe("Офис");
    expect(document.querySelectorAll(".sb-srvs .sb-srv")).toHaveLength(3); // the main two and the one of the rule
    expect([...document.querySelectorAll(".sb-chips > span")].map((c) => c.textContent)).toEqual([".ru", ".рф"]);
    expect(button(en["subs.dns.delete"])).toBeDefined();
    expect(text()).toContain(en["sx.dns.edOwn"]);
  },
  "dns-dirty": () => {
    expect(bar()).toBe(en["subs.dirty"]);
    expect(button(en["sx.dns.reset"])?.disabled).toBe(false);
    expect(button(en["subs.dns.save"])?.disabled).toBe(false);
  },
  "dns-bad-name": () => {
    expect(bar()).toBe(en["subs.dns.needName"]);
    expect(button(en["subs.dns.create"])?.disabled).toBe(true);
    expect(document.querySelectorAll(".sb-srvs .sb-srv")).toHaveLength(2);
  },
  "dns-bad-addr": () => {
    expect([...document.querySelectorAll(".dq-er")].map((e) => e.textContent)).toEqual([en["subs.dns.bad.plain"], en["subs.dns.bad.doh"], en["subs.dns.bad.dot"]]);
    expect(bar()).toBe(en["subs.dns.needServer"]);
    expect(document.querySelectorAll("[aria-invalid=true]")).toHaveLength(3);
  },
  "dns-split-new": () => {
    expect(document.querySelectorAll(".dq-rule")).toHaveLength(1);
    expect(bar()).toBe(en["subs.dns.needSuffix"]);
  },
  "dns-split": () => {
    expect(document.querySelectorAll(".dq-rule")).toHaveLength(2);
    expect([...document.querySelectorAll(".dq-rule")[0]!.querySelectorAll(".sb-chips > span")].map((c) => c.textContent)).toEqual([".ru", ".su", ".рф"]);
    expect(document.querySelector<HTMLButtonElement>('[role=group][aria-label="Rule 2 · All sites of a country"] button')!.getAttribute("aria-pressed")).toBe("true");
  },
  "dns-catalog": () => {
    expect(document.querySelector(".dq-cp")).not.toBeNull();
    expect([...document.querySelectorAll<HTMLButtonElement>(".dq-pk")].find((b) => b.textContent === "Cloudflare · Standard")!.disabled).toBe(true);
  },
  "dns-dot": () => expect(apps().find((a) => a[0] === "Happ")).toEqual(["Happ", "Plain", en["subs.dns.why.happ"]]),
  "dns-multi": () => expect(apps().find((a) => a[0] === "sing-box")![2]).toBe(en["subs.dns.why.singbox"]),
  "dns-gone": () => expect(text()).toContain(fill(en["subs.dns.variantGone"], { id: "retired/standard" })),
  "dns-busy": () => expect(button(en["subs.dns.save"])?.disabled).toBe(true),
  "dns-fail": () => expect(bar()).toBe("the panel does not answer"),
  "dns-ask": () => expect(text()).toContain(fill(en["subs.dns.deleteT"], { name: "Запасной" })),
  "dns-ask-used": () => expect(text()).toContain("4 users use it"),
  "dns-del-default": () => {
    expect(button(en["subs.dns.delete"])?.disabled).toBe(true);
    expect(text()).toContain(en["subs.dns.deleteIsDefault"]);
  },
  "dns-leave": () => expect(text()).toContain(en["subs.dns.leaveT"]),
  "dns-ro-helper": () => {
    expect(input().readOnly).toBe(true);
    expect(button(en["subs.dns.save"])).toBeUndefined();
    expect(document.querySelector(".sb-edf")!.textContent).toBe(en["sx.dns.roNote"]);
  },
  "dns-long-ed": () => {
    expect(document.querySelectorAll(".sb-srvs .sb-srv")).toHaveLength(6); // five and the one of the rule
    expect(input().value).toContain("Очень длинное название");
  },
};

describe("the scenes of the DNS tab", () => {
  it("are named once, in the board's switch and beyond it", () => {
    expect(states).toHaveLength(dnsScenes.length);
    expect(allScenes.filter((s) => s.tab === "dns")).toHaveLength(dnsScenes.length);
    for (const s of ["dns", "dns-new", "dns-cf", "dns-helper", "dns-edge", "dns-ro-helper", "dns-load", "dns-err", "dns-empty", "dns-long"]) expect(states, s).toContain(s);
    // the looks of the board's three states: the light theme, the phone, Persian
    for (const s of ["dns", "dns-new", "dns-cf"]) for (const l of ["light", "phone", "fa"]) expect(states, `${s}-${l}`).toContain(`${s}-${l}`);
    expect(dnsScenes.filter((s) => s.w === 400)).not.toHaveLength(0);
    expect(dnsScenes.every((s) => s.tab === "dns")).toBe(true);
  });
  it.each(states)("%s draws", async (state) => {
    await draw(state, 60);
    expect(text()).not.toContain("undefined");
    expect(text()).not.toMatch(/\b(sx\.dns|subs\.dns)\./); // no key stands for a missing text
    const base = state.replace(/-(light|phone|fa)$/, "");
    says[base]?.();
  }, 15000);
  it.each(["ru", "zh", "fa", "tr"] as const)("the list, an editor and the question draw in %s", async (lang) => {
    await draw("dns-users", 60, lang);
    expect(text()).not.toMatch(/\b(sx\.dns|subs\.dns)\./);
    expect(rows()).toHaveLength(18);
    act(() => root?.unmount());
    host?.remove();
    await draw("dns-ask-used", 60, lang);
    expect(text()).not.toMatch(/\b(sx\.dns|subs\.dns)\./);
    expect(input().value).toBe("Офис");
    expect(document.querySelectorAll(".sb-srvs .sb-srv")).toHaveLength(3);
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
  }, 15000);
  it("answers a click on a dot from memory: the default moves, the note says so, Undo moves it back, nothing leaves the page", async () => {
    await draw("dns", 60);
    const dot = (i: number) => rows()[i]!.querySelector<HTMLButtonElement>(".dq-def")!;
    await act(async () => void dot(2).dispatchEvent(new MouseEvent("click", { bubbles: true }))); // row 2 is Quad9 (row 1 is the retired Cloudflare, an own preset)
    await wait(80);
    expect(dot(2).getAttribute("aria-checked")).toBe("true");
    expect(dot(0).getAttribute("aria-checked")).toBe("false");
    expect(text()).toContain(fill(en["sx.dns.nowDef"], { name: "Quad9: malware blocking" }));
    await act(async () => void button(en["common.undo"])!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await wait(80);
    expect(dot(0).getAttribute("aria-checked")).toBe("true");
    expect(document.querySelector(".dq-list")).not.toBeNull();
  }, 15000);
  it("creates a preset in memory and the list has it, marked own", async () => {
    await draw("dns-bad-name", 60);
    const nameBox = input();
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(nameBox, "Новый офис");
      nameBox.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void button(en["subs.dns.create"])!.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    await wait(100);
    expect(rows().map(nameOf)).toContain("Новый офис");
  }, 15000);
});
