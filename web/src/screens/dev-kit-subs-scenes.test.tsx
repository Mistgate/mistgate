import { QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { setLang, type Lang } from "@/i18n";
import { SubscriptionsScreen } from "./subscriptions";
import { baseScenes } from "./dev-kit-subs-scenes";
import { sceneOf } from "./dev-kit-subs-page";

// The scenes of /dev-kit?only=subs-page for the tabs "Apps and formats", "Who gets what", "App versions", "Names and texts" and "Page" draw
// the real tabs on made-up data: every state of the board's switch and of what the product has beyond it must draw, in every language.

vi.mock("@tanstack/react-router", async (orig) => ({
  ...(await orig<object>()),
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useBlocker: () => ({ status: "idle" }),
  Link: ({ children, className }: { children?: React.ReactNode; className?: string }) => <a className={className}>{children}</a>,
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

const states = [...new Set(baseScenes.map((s) => s.state))];

const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
async function draw(state: string, lang: Lang = "en") {
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
  for (let i = 0; i < 6; i++) await wait(0);
  await wait(400);
  for (let i = 0; i < 4; i++) await wait(0);
}
const text = () => document.body.textContent ?? "";
const q = (sel: string) => document.querySelectorAll(sel);

/** What the scene of a state must carry once it has drawn (a state not named only has to draw). */
const says: Record<string, () => void> = {
  apps: () => {
    expect(q(".sb-ar:not(.sb-ah)")).toHaveLength(6);
    expect(q(".sb-fc")).toHaveLength(6);
    expect(q(".sb-ar:not(.sb-ah) .sb-y.y").length).toBeGreaterThan(8);
    expect(text()).toContain(en["subs.clientNoSub"]);
  },
  "apps-awg-off": () => expect(text()).toContain(en["subs.linkAwg.chip"]),
  "apps-none": () => expect(text()).toContain(en["subs.clientsNone"]),
  rules: () => {
    expect(q(".sb-rls .sb-rl:not(.sb-rest)")).toHaveLength(7);
    expect(q(".sb-tg").length).toBeGreaterThanOrEqual(4);
    expect(q(".pf-opt [role=radio]")).toHaveLength(3);
    expect(q(".sb-ex button")).toHaveLength(6);
    expect(text()).toContain(en["sx.test.hit"].replace("{n}", "1").replace("{format}", en["subs.fmt.mihomo"]));
  },
  "rules-hit": () => expect(q(".sb-rl.hit")).toHaveLength(1),
  "rules-rest": () => expect(q(".sb-rest.hit")).toHaveLength(1),
  "rules-new": () => expect(q(".sb-rl.sb-new input.an-i")).toHaveLength(1),
  "rules-saved": () => expect(q(".sb-msg")).toHaveLength(1),
  "rules-undo": () => expect(q(".sb-msg button")).toHaveLength(1),
  "rules-empty": () => expect(q(".sb-rl:not(.sb-rest)")).toHaveLength(0),
  "rules-off": () => expect(q("[role=switch][aria-checked=false]").length).toBeGreaterThanOrEqual(3),
  "rules-mg-off": () => expect(text()).toContain(en["subs.clientApi.off"]),
  "rules-mg-helper": () => expect(text()).toContain(en["subs.clientApi.ownerOnly"]),
  "rules-mg-empty": () => expect(text()).toContain("0 of 3"),
  "rules-load": () => expect(document.querySelector("[aria-busy=true], [role=status]")).not.toBeNull(),
  "rules-err": () => expect(text()).toContain(en["common.retry"]),
  ver: () => {
    expect(q(".sb-tiles [role=radio]")).toHaveLength(5);
    expect(q(".sb-vt tbody tr").length).toBeGreaterThanOrEqual(6);
    expect(q(".sb-ch [role=radio]")).toHaveLength(5);
    expect(q(".sb-mv2 .sb-mvh")).toHaveLength(1);
    expect(q(".sb-tabs .bdg").length).toBe(1);
  },
  "ver-ok": () => expect(q(".sb-tiles [aria-checked=true]")[0]?.textContent).toContain(en["cc.state.ok"]),
  "ver-min": () => expect(q(".sb-mv2 input").length).toBeGreaterThan(2),
  "ver-min-helper": () => expect(q(".sb-mv2 input[type=text]")).toHaveLength(0),
  "ver-user": () => expect(q(".sb-who")).toHaveLength(1),
  "ver-only-ok": () => expect(text()).toContain(en["cc.empty.ok"]),
  "ver-empty": () => expect(text()).toContain(en["cc.empty.none"]),
  "ver-nodes": () => expect(q(".sb-note").length).toBeGreaterThanOrEqual(3),
  txt: () => {
    expect(q(".sb-ph")).toHaveLength(1);
    expect(q(".sb-phr").length).toBeGreaterThanOrEqual(5);
    expect(q(".pp-row")).toHaveLength(6);
    expect(q(".sb-save.on")).toHaveLength(0);
  },
  "txt-made-up": () => expect(text()).toContain(en["subs.texts.previewHint"]),
  "txt-many": () => expect(q(".sb-phr").length).toBeGreaterThan(12),
  "txt-dirty": () => {
    expect(q(".sb-save.on")).toHaveLength(1);
    expect(q(".sb-phr").length).toBeGreaterThan(0);
  },
  "txt-over": () => expect(q(".sb-cnt.bad")).toHaveLength(1),
  "txt-bad": () => expect(q("[role=alert]").length).toBeGreaterThan(0),
  "txt-tcp": () => expect(q("[role=switch][aria-checked=true]")).toHaveLength(1),
  page: () => {
    expect(q(".sb-ap")).toHaveLength(9);
    expect(q(".sb-ap.open")).toHaveLength(0);
    expect(q(".sb-recb[aria-pressed=true]")).toHaveLength(2);
    expect(q(".sb-dr").length).toBeGreaterThan(4);
    expect(q(".sb-dl.off")).toHaveLength(1);
  },
  "page-open": () => expect(q(".sb-ap.open .sb-apb input")).toHaveLength(4),
  "page-dns": () => expect(q(".sb-dl.off")).toHaveLength(0),
  "page-dns-nodef": () => expect(text()).toContain(en["subs.dnsOpt.defNoneHint"]),
  "page-add": () => expect(q(".sb-known").length).toBe(1),
  "page-none": () => expect(text()).toContain(en["subs.page.appsNone"]),
};

describe("the scenes of the Subscriptions screen (apps, rules, versions, texts, page)", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(text()).not.toContain("undefined");
    expect(q("h1")[0]?.textContent).toBe(en["subs.title"]);
    expect(q(".sb-tabs [role=tab][aria-selected=true]")).toHaveLength(1);
    says[state]?.();
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("every tab draws in %s", async (lang) => {
    for (const state of ["apps", "rules", "ver", "txt", "page"]) {
      await draw(state, lang);
      expect(text()).not.toContain("undefined");
      expect(text()).not.toContain("sx.");
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
