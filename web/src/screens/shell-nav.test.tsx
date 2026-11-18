import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang } from "@/i18n";
import { setMenuFolded } from "@/lib/menu";
import { overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { awaitingQuery } from "@/lib/integrations";
import { updatesQuery } from "@/lib/updates";
import { ShellLayout } from "./shell";

// no router here: a link is an anchor with where it goes, and the page is whatever the test says
let pathname = "/nodes";
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, activeOptions: _a, ...rest }: { children?: ReactNode; to: string; params?: Record<string, string> } & Record<string, unknown>) => (
    <a href={Object.entries(params ?? {}).reduce((p, [k, v]) => p.replace(`$${k}`, v), to)} {...rest}>
      {children}
    </a>
  ),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname } }),
}));
vi.mock("@/components/palette", () => ({ default: ({ open }: { open: boolean }) => <div data-testid="palette">{open ? "open" : "closed"}</div> }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<object>()), basepath: "/" }));

// the window's width: the matchMedia of lib/menu.ts answers the `md` and `rail` steps from it
let width = 1440;
const mediaListeners = new Set<() => void>();
function setWidth(w: number) {
  width = w;
  act(() => mediaListeners.forEach((l) => l()));
}
beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("en");
  vi.stubGlobal("matchMedia", (q: string) => ({
    get matches() {
      return width >= Number(/([\d.]+)rem/.exec(q)?.[1]) * 16;
    },
    addEventListener: (_: string, cb: () => void) => mediaListeners.add(cb),
    removeEventListener: (_: string, cb: () => void) => mediaListeners.delete(cb),
  }));
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  pathname = "/nodes";
  width = 1440;
  localStorage.clear();
  act(() => setMenuFolded(false));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  document.documentElement.style.removeProperty("--menu-w");
});

const card = (id: string, status: NodeStatus, reason?: string) => ({ id, name: id, countryCode: id.slice(0, 2), location: "Somewhere", status, reason: reason ? { code: reason, params: {} } : undefined, online: [], sparkBytes: [] });
const release = (version: string) => ({ version, url: "", checkedUnix: 1, available: true, supported: true, installable: true, installing: false, errorKey: "" });

/** The shell on a seeded cache: three nodes (one broken, one needs a look), 12 people (4 online), 2 approvals, a newer release. */
async function mount(over: { update?: boolean; rollout?: RolloutStatus; role?: Role; demo?: { mobile?: boolean } } = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, refetchInterval: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: over.role ?? Role.OWNER }, version: "0.1.71", sourceUrl: "", stepUpUntilUnix: 0 } as never);
  qc.setQueryData(overviewQuery().queryKey, {
    nodesTotal: 3,
    nodes: [card("nl1", NodeStatus.ONLINE), card("de1", NodeStatus.DOWN, "agent_silent"), card("fi1", NodeStatus.ONLINE, "no_profiles")],
    alertsActive: 0,
    alertsCritical: 0,
  } as never);
  qc.setQueryData(userCountQuery.queryKey, { counts: { all: 12, online: 4 } } as never);
  qc.setQueryData(awaitingQuery.queryKey, { awaiting: 2 } as never);
  qc.setQueryData(updatesQuery.queryKey, {
    nowUnix: 1000,
    panel: { version: "v0.1.71", built: 100, update: release(over.update === false ? "v0.1.71" : "v0.1.86") },
    bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: 100 },
    nodes: [{ nodeId: "n1", name: "nl1", state: NodeUpdateState.UP_TO_DATE, supportsUpdate: true, built: 100 }],
  } as never);
  if (over.rollout) qc.setQueryData(updatesQuery.queryKey, { ...(qc.getQueryData(updatesQuery.queryKey) as object), rollout: { status: over.rollout, steps: [] } } as never);
  if (over.update === false) qc.setQueryData(updatesQuery.queryKey, { ...(qc.getQueryData(updatesQuery.queryKey) as object), panel: { version: "v0.1.71", built: 100, update: { ...release("v0.1.71"), available: false } } } as never);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ShellLayout demo={over.demo}>
          <p>page</p>
        </ShellLayout>
      </QueryClientProvider>,
    ),
  );
}
const aside = () => host!.querySelector("aside")!;
const link = (label: string) => [...aside().querySelectorAll("a")].find((a) => a.textContent?.startsWith(label) || a.querySelector(".sr-only")?.textContent === label) ?? null;
const click = (el: Element | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const key = (k: string, mods: KeyboardEventInit = {}) => act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, ...mods })));

describe("the menu column", () => {
  it("lists the sections under Setup, Operations and System, marks the open one and counts what the fleet has", async () => {
    await mount();
    const text = aside().textContent!;
    for (const w of ["Setup", "Operations", "System"]) expect(text).toContain(w);
    expect(aside().dataset.menu).toBe("full");
    expect(link("Nodes")!.getAttribute("aria-current")).toBe("page");
    expect(link("Users")!.getAttribute("aria-current")).toBeNull();
    // the nodes: a dot for each, then how many; the people online; the problems of Health (the broken node and the one to look at); the approvals of AI; the release beside Updates
    expect(link("Nodes")!.querySelectorAll("i.tone-bad, i.tone-warn, i.tone-ok")).toHaveLength(3);
    expect(link("Nodes")!.textContent).toBe("Nodes3");
    expect(link("Users")!.textContent).toBe("Users4 online");
    expect(link("Health")!.querySelector("[role=img]")!.getAttribute("aria-label")).toBe("2 problems");
    expect(link("AI")!.querySelector("[role=img]")!.textContent).toBe("2");
    expect(link("Updates")!.textContent).toBe("Updatesv0.1.86");
    // the page keeps clear of the column: 264px
    expect(document.documentElement.style.getPropertyValue("--menu-w")).toBe("264px");
  });

  it("folds to a rail of icons with the button at its top, and remembers it", async () => {
    await mount();
    const fold = aside().querySelector<HTMLButtonElement>("button[aria-label='Collapse or expand the menu']")!;
    expect(fold.getAttribute("aria-pressed")).toBe("false");
    await click(fold);
    expect(aside().dataset.menu).toBe("rail");
    expect(document.documentElement.style.getPropertyValue("--menu-w")).toBe("72px");
    expect(localStorage.getItem("menu-rail")).toBe("1");
    // no words or counters on screen; the name stays for a screen reader and comes up as a tooltip
    expect(link("Nodes")!.textContent).toBe("Nodes");
    expect(link("Nodes")!.querySelector(".sr-only")!.textContent).toBe("Nodes");
    expect(link("Nodes")!.querySelector("i")).toBeNull();
    expect(aside().querySelector("section")).toBeNull(); // the update card is for the full menu
    expect(link("Nodes")!.getAttribute("aria-current")).toBe("page");
    await click(aside().querySelector("button[aria-label='Collapse or expand the menu']"));
    expect(aside().dataset.menu).toBe("full");
    expect(localStorage.getItem("menu-rail")).toBeNull();
  });

  it("is a rail by itself below 1000px, without the button, and gives the page 72px; a phone has none", async () => {
    await mount();
    setWidth(900);
    expect(aside().dataset.menu).toBe("rail");
    expect(aside().querySelector("button[aria-label='Collapse or expand the menu']")).toBeNull();
    expect(document.documentElement.style.getPropertyValue("--menu-w")).toBe("72px");
    setWidth(400);
    expect(document.documentElement.style.getPropertyValue("--menu-w")).toBe("0px");
    setWidth(1440);
    expect(aside().dataset.menu).toBe("full");
  });

  it("shows the update card to the owner with the version he runs, and to nobody else", async () => {
    await mount();
    const notice = aside().querySelector("section")!;
    expect(notice.textContent).toContain("Version v0.1.86 is out");
    expect(notice.textContent).toContain("You run v0.1.71");
    expect(notice.querySelector("a")!.getAttribute("href")).toBe("/updates");
    act(() => root?.unmount());
    host?.remove();
    await mount({ role: Role.HELPER });
    expect(aside().querySelector("section")).toBeNull();
    expect(link("Updates")!.textContent).toBe("Updates");
  });

  it("marks Updates while a rollout waits for the owner, over the release beside it", async () => {
    await mount({ rollout: RolloutStatus.PAUSED });
    expect(link("Updates")!.textContent).toBe("Updates!");
    expect(link("Updates")!.querySelector("[role=img]")!.getAttribute("aria-label")).toBe("Paused");
  });

  it("opens the owner's menu above the button, with the way to sign-in, sessions and settings, and Esc brings the focus back", async () => {
    await mount();
    const button = aside().querySelector<HTMLButtonElement>("button[aria-label='Account menu: owner, Owner']")!;
    expect(button.textContent).toContain("owner");
    await act(async () => button.click());
    const items = [...document.querySelectorAll("[role=menu] a, [role=menu] [role=menuitem]")];
    expect(items.map((i) => i.textContent)).toEqual(["Sign-in and passkeys", "Sessions", "Settings", "Sign out"]);
    expect(items.slice(0, 3).map((i) => i.getAttribute("href"))).toEqual(["/settings/security", "/settings/sessions", "/settings"]);
    expect(document.querySelector("[role=menu]")!.textContent).toContain("Signed in as owner");
    await key("Escape");
    await act(async () => {});
    expect(document.querySelector("[role=menu]")).toBeNull();
    expect(document.activeElement).toBe(button);
  });
});

describe("the top bar", () => {
  it("opens the list of the nodes under the fleet button: problems first, each a link to its node, and the way to Health", async () => {
    await mount();
    const pill = [...host!.querySelectorAll<HTMLButtonElement>("header button")].find((b) => b.textContent?.includes("problems"))!;
    expect(pill.textContent).toContain("2 problems");
    expect(pill.querySelectorAll("i")).toHaveLength(3);
    await act(async () => pill.click());
    const dialog = document.querySelector("[role=dialog][aria-label='Node status']")!;
    const rows = [...dialog.querySelectorAll("a")];
    expect(rows.map((r) => r.getAttribute("href"))).toEqual(["/nodes/de1", "/nodes/fi1", "/nodes/nl1", "/health"]);
    expect(rows[0]!.textContent).toContain("de1");
    expect(dialog.textContent).toContain("Open Health");
    await key("Escape");
    await act(async () => {});
    expect(document.querySelector("[role=dialog][aria-label='Node status']")).toBeNull();
    expect(document.activeElement).toBe(pill);
  });

  it("opens the search on Ctrl K and from its box", async () => {
    await mount();
    expect(document.querySelector("[data-testid=palette]")).toBeNull();
    await key("k", { ctrlKey: true });
    await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(document.querySelector("[data-testid=palette]")!.textContent).toBe("open");
    expect(host!.querySelector("header button[aria-label='Search nodes, users and sections']")!.textContent).toContain("Ctrl");
  });

  it("changes the language from a list of the five, each in its own script, and the theme with two buttons", async () => {
    await mount();
    const lang = host!.querySelector<HTMLButtonElement>("header button[aria-label='Language: English']")!;
    await act(async () => lang.click());
    const rows = [...document.querySelectorAll<HTMLElement>("[role=menuitemradio]")];
    expect(rows.map((r) => r.getAttribute("lang"))).toEqual(["en", "ru", "zh-CN", "fa", "tr"]);
    expect(rows.map((r) => r.getAttribute("aria-checked"))).toEqual(["true", "false", "false", "false", "false"]);
    await act(async () => rows[1]!.click());
    await act(async () => {});
    expect(document.documentElement.lang).toBe("ru");
    act(() => setLang("en"));
    const theme = host!.querySelector("header [role=group]")!;
    const [light, dark] = [...theme.querySelectorAll("button")];
    expect([light!.getAttribute("aria-pressed"), dark!.getAttribute("aria-pressed")]).toEqual(["false", "true"]);
    await click(light!);
    expect(document.documentElement.dataset.theme).toBe("light");
    expect(document.documentElement.classList.contains("mg-thx")).toBe(true); // no transition runs while the theme changes
    expect(light!.getAttribute("aria-pressed")).toBe("true");
    await click(dark!);
  });
});

describe("a phone", () => {
  it("has the dock and the burger; the burger opens the full menu under the bar, Esc and a followed link close it", async () => {
    width = 400;
    await mount();
    const burger = host!.querySelector<HTMLButtonElement>("header button[aria-label='Menu']")!;
    const dock = host!.querySelector("nav[data-dock]")!;
    expect([...dock.querySelectorAll("a, button")].map((e) => e.textContent)).toEqual(["Overview", "Nodes", "Users", "More"]);
    expect(dock.querySelector("a[aria-current=page]")!.textContent).toBe("Nodes");
    expect(host!.querySelector("#mobile-menu")).toBeNull();
    burger.focus();
    await click(burger);
    const menu = host!.querySelector<HTMLElement>("#mobile-menu")!;
    expect(burger.getAttribute("aria-expanded")).toBe("true");
    expect(menu.getAttribute("role")).toBe("dialog");
    expect([...menu.querySelectorAll("nav a")].map((a) => a.textContent)).toContain("Nodes3");
    expect(menu.textContent).toContain("Setup");
    expect(menu.querySelector("a[aria-current=page]")!.textContent).toContain("Nodes");
    // the language as five codes, the theme, the owner and the version
    expect([...menu.querySelectorAll("[aria-label=Language] button")].map((b) => b.textContent)).toEqual(["EN", "RU", "中文", "FA", "TR"]);
    expect(menu.textContent).toContain("Panel version 0.1.71");
    expect(menu.textContent).toContain("Sign out");
    await key("Escape");
    expect(host!.querySelector("#mobile-menu")).toBeNull();
    expect(document.activeElement).toBe(burger);

    // "More" in the dock opens the same menu; following a link closes it, even to the page that is already open
    await click(dock.querySelector("button"));
    expect(host!.querySelector("#mobile-menu")).not.toBeNull();
    await click(host!.querySelector("#mobile-menu nav a[aria-current=page]"));
    expect(host!.querySelector("#mobile-menu")).toBeNull();
  });

  it("puts a dot on More while a problem or an approval waits", async () => {
    width = 400;
    await mount();
    expect(host!.querySelector("nav[data-dock] button span.bg-warn")).not.toBeNull();
  });
});
