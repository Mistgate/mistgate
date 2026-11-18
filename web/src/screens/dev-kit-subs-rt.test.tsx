import { QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { fill, setLang, type Lang } from "@/i18n";
import { SubscriptionsScreen } from "./subscriptions";
import { rtScenes } from "./dev-kit-subs-rt";
import { sceneOf } from "./dev-kit-subs-page";

// The scenes of /dev-kit?only=subs-page for the tab "Routing" draw the real tab on made-up data: every state of the list, of a pasted
// text, of the mistakes of the form and of the save must draw, in every language.

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

const states = [...new Set(rtScenes.map((s) => s.state))];
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
const reads = 700; // the form's own wait before it asks the panel (0.4 s) and the answer

/** The words the scenes of a state must carry, once it has drawn. */
const says: Record<string, () => void> = {
  rt: () => {
    expect(heads()).toHaveLength(2);
    expect(heads()[0]!.getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain("Для семьи");
    expect(text()).toContain("geo.example.com");
    expect(text()).toContain(fill(en["sx.rt.inHeader"], { n: heads().length > 0 ? text().match(/routing header \((\d+) bytes\)/)![1]! : "" }));
  },
  "rt-empty": () => expect(text()).toContain(en["sx.rt.noneT"]),
  "rt-closed": () => expect(document.querySelector("textarea.rt-y")).toBeNull(),
  "rt-second": () => expect(heads()[1]!.textContent).toContain(en["sx.rt.modeAdd"]),
  "rt-new": () => {
    expect(heads()).toHaveLength(3);
    expect(text()).toContain(en["sx.rt.empty"]);
    expect(disabled(en["subs.save"])).toBe(true);
  },
  "rt-garbage": () => {
    expect(text()).toContain(fill(en["sx.rt.refused"], { text: "this is not a link, JSON or base64 text of a profile" }));
    expect(disabled(en["subs.save"])).toBe(true);
  },
  "rt-http-geo": () => expect(text()).toContain("Geoipurl: an https address"),
  "rt-body": () => expect(text()).toContain(fill(en["sx.rt.inBody"], { n: text().match(/subscription after the servers \((\d+) bytes/)![1]!, max: 3000 })),
  "rt-dirty": () => expect(text()).toContain(en["subs.discard"]),
  "rt-busy": () => expect(text()).toContain(en["sx.hx.h.saving"]),
  "rt-saved": () => expect(document.querySelector(".hx-fh")!.textContent).toBe(en["subs.saved"]),
  "rt-fail": () => expect(document.querySelector(".hx-fh.bad")!.textContent).toBe("the panel does not answer"),
  "rt-refused-row": () => expect(text()).toContain(`${en["sx.rt.refusedSave"]}: Geoipurl`),
  "rt-refused-all": () => expect(text()).toContain(`${en["sx.rt.refusedSave"]}: routing_profiles: at most 20`),
  "rt-dup-name": () => expect(text()).toContain(fill(en["sx.rt.eName"], { name: "для СЕМЬИ" })),
  "rt-two-groups": () => expect(text()).toContain(fill(en["sx.rt.eGroup"], { group: "Семья" })),
  "rt-two-all": () => expect(text()).toContain(en["sx.rt.eAll"]),
  "rt-nobody": () => expect(text()).toContain(en["sx.rt.eNoGroup"]),
  "rt-gone": () => expect(text()).toContain(en["sx.rt.eGone"]),
  "rt-max": () => {
    expect(heads()).toHaveLength(20);
    expect(disabled(en["sx.rt.add"])).toBe(true);
  },
};
/** The ones that wait for the panel to read a pasted text. */
const asks: Record<string, () => void> = {
  "rt-pasted": () => {
    expect(text()).toContain("Из 3x-ui");
    expect(text()).toContain(en["sx.rt.sDropped"]);
    expect(text()).toContain("Evil, Odd");
    expect(document.querySelector('[aria-label="' + en["sx.rt.mode"] + '"] [aria-pressed=true]')!.textContent).toBe(en["sx.rt.mAdd"]);
  },
  "rt-garbage": () => expect(text()).toContain(en["sx.rt.refused"].split("{")[0]),
  "rt-reading": () => expect(text()).toContain(en["sx.rt.sWait"]),
  "rt-unavailable": () => expect(text()).toContain(en["sx.rt.noAnswer"].split("{")[0]),
  "rt-leave": () => expect(text()).toContain(en["sx.leave.title"]),
};

describe("the scenes of the Routing tab", () => {
  it("are named once, and cover the list, a pasted text, the mistakes and the save", () => {
    expect(states).toHaveLength(rtScenes.length);
    for (const s of ["rt", "rt-empty", "rt-pasted", "rt-garbage", "rt-body", "rt-dup-name", "rt-two-groups", "rt-refused-row", "rt-busy", "rt-saved"]) expect(states, s).toContain(s);
    expect(rtScenes.every((s) => s.tab === "routing")).toBe(true);
  });
  it.each(states)("%s draws", async (state) => {
    await draw(state, asks[state.replace(/-(light|phone|fa)$/, "")] || state.includes("garbage") ? reads : 60);
    expect(text()).not.toContain("undefined");
    expect(text()).not.toMatch(/\bsx\.rt\.|\bsubs\.tab\./); // no key stands for a missing text
    expect(document.querySelectorAll(".hx-tab .hx-card").length).toBe(2);
    const base = state.replace(/-(light|phone|fa)$/, "");
    says[base]?.();
    asks[base]?.();
  }, 15000);
  it.each(["ru", "zh", "fa", "tr"] as const)("the tab draws in %s", async (lang) => {
    await draw("rt", 60, lang);
    expect(text()).not.toMatch(/\bsx\.rt\.|\bsubs\.tab\./);
    expect(heads()).toHaveLength(2);
  }, 15000);
});
