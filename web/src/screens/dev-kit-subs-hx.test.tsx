import { QueryClientProvider } from "@tanstack/react-query";
import * as ace from "ace-builds";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { fill, setLang, type Lang } from "@/i18n";
import { SubscriptionsScreen } from "./subscriptions";
import { hxScenes } from "./dev-kit-subs-hx";
import { demoGroups, demoPeople, sceneOf } from "./dev-kit-subs-page";
import { groups, people } from "./dev-kit-subs-hx-data";

// The scenes of /dev-kit?only=subs-page for the tabs "Response headers" and "Mihomo templates" draw the real tabs on made-up data:
// every state of the board's two switches and of what the product has beyond them must draw, in every language.

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

const states = [...new Set(hxScenes.map((s) => s.state))];

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
const heads = () => [...document.querySelectorAll<HTMLButtonElement>("button.hx-rh")];
const disabled = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label)?.hasAttribute("disabled");
const sk = () => document.querySelector(".hx-sk[aria-busy=true]");
const verdict = () => document.querySelector(".mt-res")?.textContent;
const preview = 700; // the form's own wait before it asks the panel (0.3 s, 0.4 s) and the answer

/** The words the scenes of a state must carry, once it has drawn (a state not named only has to draw). */
const says: Record<string, () => void> = {
  hdr: () => {
    expect(heads()).toHaveLength(3);
    expect(heads()[2]!.querySelector("em")?.textContent).toBe(en["sx.hx.h.shadow"]);
    expect(text()).toContain("3 of 20 · ");
  },
  "hdr-empty": () => expect(text()).toContain(en["sx.hx.h.noneT"]),
  "hdr-bad": () => {
    expect(heads()).toHaveLength(5);
    expect(document.querySelectorAll(".hx-r.bad")).toHaveLength(5);
    expect(text()).toContain(fill(en["sx.hx.h.eFamily"], { name: "Profile-*" }));
    expect(text()).toContain(en["sx.hx.h.ftBad"]);
    expect(disabled(en["subs.save"])).toBe(true);
  },
  "hdr-max": () => {
    expect(heads()).toHaveLength(20);
    expect(disabled(en["sx.hx.h.add"])).toBe(true);
    expect(text()).toContain(fill(en["sx.hx.h.ftMax"], { max: 20 }));
  },
  "hdr-open": () => expect(document.querySelector<HTMLInputElement>("input.hx-n")!.value).toBe("X-Example-Notice"),
  "hdr-open-b64": () => expect(document.querySelector("[role=switch]")!.getAttribute("aria-checked")).toBe("true"),
  "hdr-add": () => expect(text()).toContain(en["sx.hx.h.eNoName"]),
  "hdr-dirty": () => expect(text()).toContain(en["subs.discard"]),
  "hdr-covered": () => expect(text()).toContain(en["sx.hx.h.shadowNote"]),
  "hdr-over": () => expect(text()).toContain(fill(en["sx.hx.h.ftOver"], { max: 4096 })),
  "hdr-busy": () => expect(text()).toContain(en["sx.hx.h.saving"]),
  "hdr-saved": () => expect(document.querySelector(".hx-fh")!.textContent).toBe(en["subs.saved"]),
  "hdr-fail": () => expect(document.querySelector(".hx-fh.bad")!.textContent).toBe("the panel does not answer"),
  "hdr-names": () => expect(document.querySelectorAll(".hx-all .mono")).toHaveLength(23),
  "hdr-nobody": () => expect(text()).toContain(en["subs.hdr.pv.noUsers"]),
  "hdr-many": () => expect(document.querySelector("[role=combobox]")).not.toBeNull(),
  "hdr-users-load": () => expect(document.querySelector(".hx-sk")).not.toBeNull(),
  "hdr-users-err": () => expect(text()).toContain(en["common.retry"]),
  mt: () => {
    expect(heads()).toHaveLength(2);
    expect(heads()[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(ace.edit(document.querySelector<HTMLElement>(".hx-ed .mt-source")!).getValue()).toContain("mixed-port: 7890");
    expect(text()).toContain(en["sx.hx.m.intro"].slice(0, 30));
  },
  "mt-empty": () => {
    expect(text()).toContain(en["sx.hx.m.noneT"]);
    expect(verdict()).toContain(en["sx.hx.m.rBuilt"]);
  },
  "mt-err": () => {
    expect(heads()[0]!.textContent).toContain(en["sx.hx.m.stBad"]);
  },
  "mt-free": () => expect(heads()[0]!.textContent).toContain(en["sx.hx.m.stFree"]),
  "mt-ro-helper": () => {
    expect(text()).toContain(en["sx.hx.m.ro"]);
    expect(disabled(en["subs.save"])).toBeUndefined();
    expect(ace.edit(document.querySelector<HTMLElement>(".hx-ed .mt-source")!).getReadOnly()).toBe(true);
  },
  "mt-closed": () => {
    expect(document.querySelector(".hx-ed .mt-source")).toBeNull();
    expect(verdict()).toContain(en["sx.hx.m.rOther"]);
  },
  "mt-second": () => expect(text()).toContain(en["sx.hx.m.busyEnd"]),
  "mt-three": () => expect(heads()[2]!.textContent).toContain(en["sx.hx.m.stFree"]),
  "mt-dirty": () => expect(text()).toContain(en["subs.discard"]),
  "mt-busy": () => expect(text()).toContain(en["sx.hx.h.saving"]),
  "mt-saved": () => expect(document.querySelector(".hx-fh")!.textContent).toBe(en["subs.saved"]),
  "mt-ask": () => expect(text()).toContain(fill(en["sx.hx.m.ask"], { name: "Для друзей" })),
  "mt-ask-free": () => expect(text()).toContain(fill(en["sx.hx.m.askFree"], { name: "Черновик" })),
  "mt-refused-row": () => {
    expect(text()).toContain(en["sx.hx.m.refused"]);
    expect(document.querySelector("button.mt-ln")!.textContent).toBe(fill(en["sx.hx.m.line"], { n: 11 }));
  },
  "mt-refused-all": () => expect(text()).toContain(en["sx.hx.m.refusedAll"]),
  "mt-over": () => expect(text()).toContain(fill(en["sx.hx.m.ftOver"], { max: 256 })),
  "mt-big": () => expect(text()).toContain(fill(en["sx.hx.m.eBig"], { max: 64 })),
  "mt-file-big": () => expect(text()).toContain(en["sx.hx.m.eFile"]),
  "mt-drag": () => expect(document.querySelector(".hx-ed .mt-editor")?.classList.contains("drag")).toBe(true),
  "mt-max": () => expect(disabled(en["sx.hx.m.add"])).toBe(true),
  "mt-nobody": () => expect(text()).toContain(en["sx.hx.m.pvNoUsers"]),
  "mt-many": () => expect(document.querySelector("[role=combobox]")).not.toBeNull(),
  "mt-users-load": () => expect(document.querySelector(".hx-sk")).not.toBeNull(),
  "mt-users-err": () => expect(text()).toContain(en["common.retry"]),
};
/** The ones that wait for the panel's preview. */
const asks: Record<string, () => void> = {
  hdr: () => {
    expect(document.querySelector(".hx-out")!.textContent).toContain("X-Example-Notice");
    expect(document.querySelector(".hx-out")!.textContent).toContain("base64:");
  },
  "hdr-pv-page": () => expect(text()).toContain(en["subs.hdr.pv.page"]),
  "hdr-pv-noaccess": () => expect(text()).toContain(en["subs.hdr.pv.noAccess"]),
  "hdr-pv-none": () => expect(text()).toContain(en["sx.hx.h.pvNone"]),
  "hdr-pv-load": () => expect(sk()).not.toBeNull(),
  "hdr-pv-err": () => expect(text()).toContain(en["common.retry"]),
  mt: () => {
    expect(verdict()).toBe(en["sx.hx.m.rThis"] + fill(en["sx.hx.m.rThisS"], { group: "Семья" }));
    expect(ace.edit(document.querySelector<HTMLElement>(".mt-preview .mt-source")!).getValue()).toContain("добавила панель");
  },
  "mt-err": () => {
    expect(document.querySelector("button.mt-ln")!.textContent).toBe(fill(en["sx.hx.m.line"], { n: 10 }));
    expect(verdict()).toContain(en["sx.hx.m.rBad"]);
    expect(document.querySelector(".mt-preview")).toBeNull();
  },
  "mt-warn": () => {
    expect(text()).toContain(en["subs.mt.warn.own_dns"]);
    expect(text()).toContain(fill(en["subs.mt.warn.unknown_rule_target"], { arg: "Прямой" }));
    expect(heads()[0]!.textContent).toContain(en["sx.hx.m.stWarn"].split("{")[0]!);
  },
  "mt-leave": () => expect(text()).toContain(en["sx.leave.title"]),
  "mt-pv-bob": () => {
    expect(verdict()).toContain(en["sx.hx.m.rOther"]);
    expect(document.querySelector(".mt-preview")).toBeNull();
  },
  "mt-pv-carol": () => expect(verdict()).toContain(en["sx.hx.m.rOff"]),
  "mt-pv-dave": () => expect(verdict()).toContain(en["sx.hx.m.rNoSrv"]),
  "mt-pv-load": () => expect(sk()).not.toBeNull(),
  "mt-pv-err": () => expect(text()).toContain(en["common.retry"]),
};

describe("the scenes of the Response headers and Mihomo templates tabs", () => {
  it("know the people and the groups of the page they are drawn on", () => {
    expect(people).toEqual(demoPeople);
    expect(groups).toEqual(demoGroups.map(({ id, name }) => ({ id, name })));
  });
  it("are named once, and each of the tabs has the board's states", () => {
    expect(states).toHaveLength(hxScenes.length);
    for (const s of ["hdr", "hdr-empty", "hdr-bad", "hdr-max", "mt", "mt-empty", "mt-err", "mt-free", "mt-ro-helper"]) expect(states, s).toContain(s);
    expect(hxScenes.filter((s) => s.tab === "headers").length).toBeGreaterThan(20);
    expect(hxScenes.filter((s) => s.tab === "mihomo").length).toBeGreaterThan(25);
  });
  it.each(states)("%s draws", async (state) => {
    const waits = asks[state] ? preview : 60;
    await draw(state, waits);
    expect(text()).not.toContain("undefined");
    expect(text()).not.toMatch(/\bsx\.hx\.|\bsubs\.(hdr|mt)\./); // no key stands for a missing text
    expect(document.querySelectorAll(".hx-tab .hx-card").length).toBe(2);
    // the base state of each variant says what the base says
    const base = state.replace(/-(light|phone|fa)$/, "");
    says[base]?.();
    asks[base]?.();
  }, 15000);
  it.each(["ru", "zh", "fa", "tr"] as const)("a tab and the other draw in %s", async (lang) => {
    await draw("hdr-open", 60, lang);
    expect(text()).not.toMatch(/\bsx\.hx\.|\bsubs\.(hdr|mt)\./);
    expect(heads()).toHaveLength(3);
    act(() => root?.unmount());
    host?.remove();
    await draw("mt", 60, lang);
    expect(text()).not.toMatch(/\bsx\.hx\.|\bsubs\.(hdr|mt)\./);
    expect(heads()).toHaveLength(2);
  }, 15000);
});
