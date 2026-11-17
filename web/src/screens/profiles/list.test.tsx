import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { ProfilesScreen } from "./list";
import { validateProfilesSearch } from "./search";

let role = Role.OWNER;
const all = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON];
const hist = (ok: number) => Array.from({ length: 48 }, (_, i) => ({ startUnix: 1000 + i * 1800, ok, failed: 0, latencyMs: 30 }));
const cell = (inboundId: string, ms: number) => ({ deployed: true, inboundId, last: { status: CheckStatus.OK, atUnix: Math.floor(Date.now() / 1000) - 240, latencyMs: ms, responseMs: ms, errorCode: "", errorDetail: "" }, failStreak: 0, history: hist(5), responseHourMs: ms, latencyHourMs: ms });
const none = { deployed: false, inboundId: "", failStreak: 0, history: [] };
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    listProfiles: () =>
      Promise.resolve({
        profiles: [
          // the server's order: the list sorts by protocol (Hysteria2, VLESS, AmneziaWG, MTProto) and keeps it within one
          { id: "p_tg", name: "tg", protocol: "mtproto", nodeCount: 1, userCount: 3, summary: "TCP 443 · Fake TLS · www.example.com", warnings: [], formats: all.map((format) => ({ format, ok: false, note: "unsupported" })) },
          { id: "p_run", name: "runs", protocol: "vless", nodeCount: 2, userCount: 5, summary: "XHTTP 443 · REALITY · WARP", warnings: [], formats: all.map((format) => ({ format, ok: format !== SubFormat.SINGBOX_JSON, note: format === SubFormat.SINGBOX_JSON ? "xhttp" : "" })) },
          { id: "p_lonely", name: "lonely", protocol: "awg", nodeCount: 1, userCount: 0, summary: "UDP 51842 · AWG 3.1 · DNS", warnings: [{ code: "inbound_failed", params: { node: "de1", inbound: "inb_1", error: "x" } }], formats: all.map((format) => ({ format, ok: format === SubFormat.MIHOMO_YAML, note: format === SubFormat.MIHOMO_YAML ? "" : "awg" })) },
          { id: "p_nowhere", name: "nowhere", protocol: "hysteria2", nodeCount: 0, userCount: 5, summary: "UDP 8443 · hop 20000-30000 · Salamander · Let's Encrypt", warnings: [], formats: all.map((format) => ({ format, ok: true, note: "" })) },
        ],
      }),
    listProtocols: () =>
      Promise.resolve({
        protocols: [
          { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP] },
          { id: "vless", displayName: "VLESS", apps: [App.HAPP] },
          { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA] },
          { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM] },
        ],
      }),
  },
  health: {
    // columns: the four profiles; rows: de1 (runs, lonely), nl1 (runs, tg)
    getChecks: () =>
      Promise.resolve({
        nowUnix: Math.floor(Date.now() / 1000),
        intervalS: 300,
        columns: [
          { profileId: "p_tg", profileName: "tg", protocol: "mtproto" },
          { profileId: "p_run", profileName: "runs", protocol: "vless" },
          { profileId: "p_lonely", profileName: "lonely", protocol: "awg" },
          { profileId: "p_nowhere", profileName: "nowhere", protocol: "hysteria2" },
        ],
        rows: [
          { nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [none, cell("inb_run_de1", 41), cell("inb_1", 0), none] },
          { nodeId: "nod_nl1", nodeName: "nl1", countryCode: "NL", nodeStatus: NodeStatus.ONLINE, cells: [cell("inb_tg", 77), cell("inb_run_nl1", 63), none, none] },
        ],
        countryChecks: [],
      }),
  },
  nodes: { getFleetConnections: () => Promise.resolve({ nodes: [{ nodeId: "nod_de1", inbounds: [{ inboundId: "inb_run_de1", connects: 12, unsupported: [], hours: [], clients: [] }] }] }) },
  relays: { listRelays: () => Promise.resolve({ relays: [] }) },
  users: { listUsers: () => Promise.resolve({ users: [], counts: { all: 140 } }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  groups: { listGroups: () => Promise.resolve({ groups: [{ id: "g", name: "Family", profileIds: ["p_run", "p_nowhere", "p_tg"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 }] }) },
}));

// the router: the list reads its filters from the URL and writes them back through navigate({ search })
let search: Record<string, unknown> = {};
let rerender = () => {};
const opened = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => search,
  useNavigate: () => (o: { to?: string; search?: Record<string, unknown> | ((prev: Record<string, unknown>) => Record<string, unknown>) }) => {
    if (typeof o.search === "function") search = o.search(search);
    else opened(o);
    rerender();
    return Promise.resolve();
  },
  Link: ({ children, to, className, params: _p, search: _s, ...rest }: { children?: ReactNode; to: string; className?: string; params?: unknown; search?: unknown }) => (
    <a href={to} className={className} {...rest}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  search = {};
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  opened.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () => root!.render(<QueryClientProvider client={qc}><ProfilesScreen /></QueryClientProvider>);
  rerender = () => act(draw);
  await act(async () => draw());
  await settle();
}
const rows = () => [...document.querySelectorAll<HTMLElement>("[role=listitem]")];
const names = () => rows().map((r) => r.querySelector("b")!.textContent);
const row = (name: string) => rows().find((r) => r.querySelector("b")!.textContent === name)!;
const chip = (start: string) => [...document.querySelectorAll<HTMLButtonElement>(".pv-chips button")].find((r) => r.textContent!.startsWith(start))!;
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent === label)!;

describe("the profiles list", () => {
  it("offers a new profile to the owner only: the helper and the observer read the list", async () => {
    await mount();
    expect(document.body.textContent).toContain("New profile");
    act(() => root?.unmount());
    host?.remove();
    for (const r of [Role.HELPER, Role.READONLY]) {
      role = r;
      await mount();
      expect(names()).toEqual(["nowhere", "runs", "lonely", "tg"]);
      expect(document.body.textContent).not.toContain("New profile");
      expect(document.body.textContent).not.toContain("Put on nodes");
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("is one row per profile in the protocols' order, the name being the row's link", async () => {
    await mount();
    expect(names()).toEqual(["nowhere", "runs", "lonely", "tg"]);
    const link = row("runs").querySelector("a")!;
    expect(link.getAttribute("href")).toBe("/profiles/$id");
    expect(link.getAttribute("aria-label")).toBe("Open profile runs");
  });

  it("says in one line how many profiles, people and nowhere-profiles there are", async () => {
    await mount();
    expect(document.querySelector(".pv-sum")!.textContent).toBe("4 profiles140 people1 not on any node");
  });

  it("puts the protocol's name before the server's own words, and the nodes the profile is on as chips", async () => {
    await mount();
    expect(row("runs").querySelector(".pv-n > span:last-child > span")!.textContent).toBe("VLESS · XHTTP 443 · REALITY · WARP");
    expect([...row("runs").querySelectorAll(".pv-wh > span")].map((c) => c.textContent)).toEqual(["de1", "nl1"]);
    // a server that did not start is the red chip; no node at all is "nowhere" and the owner's offer
    expect(row("lonely").querySelector(".pv-wh > span.bad")!.textContent).toBe("de1");
    expect(row("nowhere").querySelector(".pv-wh")!.textContent).toBe("nowherePut on nodes…");
  });

  it("counts the people and names their groups; a profile in no group says so in amber", async () => {
    await mount();
    expect(row("runs").querySelector(".pv-2")!.textContent).toBe("5Family");
    expect(row("lonely").querySelector(".pv-2")!.textContent).toBe("—in no group");
    expect(row("lonely").querySelector(".pv-2.warn")).not.toBeNull();
    // Telegram reaches people by its own link
    expect(row("tg").querySelector(".pv-2 b")!.textContent).toBe("3 · Telegram");
  });

  it("folds a node's warning and a format that leaves the profile out into the remarks; AmneziaWG's keys are no remark", async () => {
    await mount();
    const rm = (name: string) => row(name).querySelector(".pv-rm")!;
    expect(rm("lonely").querySelector("b")!.textContent).toBe("1");
    expect(rm("lonely").className).toContain("bad");
    expect(rm("lonely").querySelector("[role=tooltip]")!.textContent).toBe("Did not start on de1");
    expect(rm("runs").querySelector("b")!.textContent).toBe("1");
    expect(rm("runs").className).toContain("part");
    expect(rm("runs").querySelector("[role=tooltip]")!.textContent).toContain("sing-box");
    expect(rm("nowhere").querySelector("b")).toBeNull();
    expect(row("lonely").querySelector("a")!.getAttribute("aria-describedby")).toBe(rm("lonely").querySelector("[role=tooltip]")!.id);
  });

  it("filters by protocol, name and state through the URL, each count being what that choice would show", async () => {
    await mount();
    expect(chip("All").textContent).toBe("All4");
    expect(chip("VLESS").textContent).toBe("VLESS1");
    await act(async () => chip("VLESS").click());
    expect(search).toEqual({ protocol: "vless" });
    expect(names()).toEqual(["runs"]);

    await act(async () => chip("All").click());
    const box = document.querySelector<HTMLInputElement>("input[type=search]")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, "  NO ");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(search).toEqual({ q: "NO" });
    expect(names()).toEqual(["nowhere"]);
    expect(chip("All").textContent).toBe("All1");
  });

  it("opens on the state in the URL, and says when nothing matches with a way back", async () => {
    search = { state: "idle" };
    await mount();
    expect(names()).toEqual(["nowhere"]);
    expect(chip("Hysteria2").textContent).toBe("Hysteria21");
    expect(chip("VLESS").textContent).toBe("VLESS0");
    expect(chip("Not deployed").getAttribute("aria-pressed")).toBe("true");

    search = { protocol: "vless", state: "idle" };
    rerender();
    expect(rows()).toHaveLength(0);
    expect(document.body.textContent).toContain("Nothing matches these filters");
    await act(async () => button("Reset filters").click());
    expect(search).toEqual({});
    expect(names()).toHaveLength(4);
  });
});

describe("the matrix \"Where it passes\"", () => {
  it("is a view of its own in the URL, a column per node and a cell where the profile is", async () => {
    await mount();
    await act(async () => [...document.querySelectorAll<HTMLElement>(".pv-tr [role=radio]")].at(-1)!.click());
    expect(search).toEqual({ view: "where" });
    expect(document.querySelector(".pv-rows")).toBeNull();
    expect([...document.querySelectorAll(".pv-mh .pv-nh")].map((n) => n.querySelector(".mono")!.textContent)).toEqual(["de1", "nl1"]);
    const cellsOf = (name: string) => [...[...document.querySelectorAll(".pv-mr")].find((r) => r.querySelector(".pv-n b")?.textContent === name)!.querySelectorAll(".pv-c")];
    expect(cellsOf("runs").map((c) => c.textContent)).toEqual(["41 ms12 conn.", "63 ms"]);
    // where the profile is not: a quiet plus for the owner, a link to the profile
    expect(cellsOf("lonely")[1]!.className).toContain("none");
    expect(cellsOf("lonely")[1]!.querySelector("a")).not.toBeNull();
    // a server that did not start
    expect(cellsOf("lonely")[0]!.className).toContain("bad");
    expect(cellsOf("lonely")[0]!.textContent).toBe("failed");
  });

  it("opens the drawer of a cell and shuts it on Escape; the helper has no plus", async () => {
    search = { view: "where" };
    await mount();
    const target = [...document.querySelectorAll<HTMLButtonElement>("button.pv-c")].find((b) => b.getAttribute("aria-label")!.startsWith("runs, de1"))!;
    await act(async () => target.click());
    const drawer = document.querySelector("aside.pv-dr")!;
    expect(drawer.getAttribute("aria-label")).toBe("Server on the node");
    expect(drawer.textContent).toContain("runs on de1");
    expect(drawer.textContent).toContain("12 connections");
    expect(drawer.textContent).toContain("Open profile ›");
    await act(async () => void drawer.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(document.querySelector("aside.pv-dr")).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    role = Role.HELPER;
    await mount();
    expect(document.querySelector(".pv-c.none a")).toBeNull();
  });
});

describe("New profile from the list", () => {
  it("opens the wizard, or straight the wizard of the protocol the list is filtered by", async () => {
    await mount();
    const create = () => button("New profile");
    await act(async () => create().click());
    expect(opened).toHaveBeenLastCalledWith({ to: "/profiles/new", search: {} });
    search = { protocol: "vless" };
    rerender();
    await act(async () => create().click());
    expect(opened).toHaveBeenLastCalledWith({ to: "/profiles/new", search: { protocol: "vless" } });
  });
});

describe("the list's URL", () => {
  it("keeps known filters, turns a typed number back into text and drops the rest", () => {
    expect(validateProfilesSearch({ protocol: "vless", q: 443, state: "warp" })).toEqual({ protocol: "vless", q: "443", state: "warp" });
    expect(validateProfilesSearch({ protocol: "../x", q: "", state: "broken" })).toEqual({});
    expect(validateProfilesSearch({ view: "where" })).toEqual({ view: "where" });
    expect(validateProfilesSearch({ view: "grid" })).toEqual({});
  });
});
