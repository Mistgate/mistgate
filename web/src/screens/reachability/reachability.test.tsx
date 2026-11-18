import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang } from "@/i18n";
import { ReachabilityScreen } from "./index";

const getHistory = vi.fn();
const getChecks = vi.fn();
const listAlerts = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  countryReach: {
    getCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }),
    updateCountryReachability: () => Promise.resolve({ settings: { country: "RU" } }),
    getReachabilityHistory: (...a: unknown[]) => getHistory(...a),
  },
  health: { getChecks: (...a: unknown[]) => getChecks(...a), listAlerts: (...a: unknown[]) => listAlerts(...a) },
  nodes: { listNodes: () => Promise.resolve({ nodes: fleet.map((n) => ({ ...n, ipv6: fleetV6[n.id] })) }) },
  // the cards have their own tests (sources.test.tsx); here they only have to render
  globalping: { getGlobalping: () => Promise.resolve({ settings: { enabled: true, intervalMin: 15, hasToken: true } }) },
  blocklist: { getBlocklist: () => Promise.resolve({ settings: { enabled: true, available: true, checkedUnix: 0, listsUnix: 0 } }) },
  tspuProbe: { getTspuProbe: () => Promise.resolve({ settings: { enabled: true, available: true, unavailableReason: "", probeNodeId: "nod_probe1", intervalMin: 120, nodes: [] } }) },
  clientProbe: { getClientProbe: () => Promise.resolve({ settings: { enabled: false, reports: 0, lastUnix: 0 }, nodes: [], nowUnix: 0 }) },
  ownProbe: { get: () => Promise.resolve({ settings: { enabled: false, intervalMin: 60, fallbackUrl: "" }, probes: [] }) },
  cheburcheck: {
    getCheburcheck: () =>
      Promise.resolve({ settings: { enabled: true, publicEnabled: true, hasToken: false, intervalMin: 180, ownIntervalMin: 60, status: "", holdUntilUnix: 0, lastRunUnix: 0, lastVerdict: "", unavailable: "", tokenUnknown: false } }),
  },
  bsbord: {
    getBsbord: () =>
      Promise.resolve({ settings: { enabled: true, hasKey: true, intervalMin: 1440, capCredits: 10000, units: ["mts|krym|off"], spendMonth: "", spentCredits: 0, reservedCredits: 0, lastStatus: "", lastRunUnix: 0, holdUntilUnix: 0, pending: false } }),
    listBsbordUnits: () => Promise.resolve({ units: [], nUnits: 0, nProbeable: 0 }),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({
    children,
    to,
    hash,
    params,
    search,
    className,
    title,
    ...rest
  }: { children?: ReactNode; to: string; hash?: string; params?: { id: string }; search?: Record<string, string>; className?: string; title?: string; "data-testid"?: string }) => (
    <a href={`${to}${search ? `?${new URLSearchParams(search)}` : ""}${hash ? `#${hash}` : ""}`} data-id={params?.id} className={className} title={title} data-testid={rest["data-testid"]}>
      {children}
    </a>
  ),
  // the tab and the open source are the address (`?tab=`, `?source=`): navigate stores the new search and the test draws the page again, as the router would
  useSearch: () => search,
  useNavigate: () => navigate,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const H = 3600;
const NOW = Math.floor(Date.now() / 1000);
const SINCE = NOW - 7 * 24 * H;
const CUT = SINCE + 3 * 24 * H + 10 * H;

const node = (id: string, name: string, countryCode: string, status = NodeStatus.ONLINE) => ({ id, name, countryCode, status });
/** Node.ipv6 of the listed nodes, by id (none by default) */
let fleetV6: Record<string, object> = {};
const fleet = [
  node("nod_se1", "se1", "SE"),
  node("nod_de1", "de1", "DE"),
  node("nod_fi1", "fi1", "FI"),
  node("nod_new", "new1", "DE", NodeStatus.PENDING), // waits for its install: nothing to look at
  node("nod_nl1", "nl1", "NL"),
  node("nod_probe1", "probe1", "RU"),
];

const pts = (...p: [number, CountryVerdict][]) => p.map(([atUnix, verdict]) => ({ atUnix, verdict }));
const lane = (source: string, points: ReturnType<typeof pts>, untilUnix = NOW) => ({ source, points, untilUnix });
const OK = CountryVerdict.OK;
const TCP = CountryVerdict.TCP_BLOCKED;
const lanes: Record<string, ReturnType<typeof lane>[]> = {
  nod_de1: [lane("globalping", pts([SINCE + 300, OK])), lane("lists", pts([SINCE + H, OK])), lane("tspu", pts([SINCE + 1800, OK]))],
  nod_fi1: [lane("globalping", pts([SINCE + 300, OK])), lane("lists", pts([SINCE + H, OK])), lane("tspu", pts([SINCE + 1800, OK], [NOW - 30 * H, TCP]))],
  nod_nl1: [
    lane("globalping", pts([SINCE + 300, OK], [CUT, TCP], [CUT + 16 * H, OK])),
    lane("lists", pts([SINCE + H, CountryVerdict.LISTED])),
    lane("tspu", pts([SINCE + 1800, OK], [CUT + H, TCP], [CUT + 17 * H, OK])),
  ],
  nod_probe1: [lane("globalping", pts([SINCE + 300, OK])), lane("lists", pts([SINCE + H, OK]))],
  nod_se1: [lane("globalping", pts([NOW - 80 * 60, CountryVerdict.UNKNOWN])), lane("lists", pts([NOW - 88 * 60, CountryVerdict.LISTED]))],
};
function history(sources: string[]) {
  const nodes = Object.entries(lanes)
    .map(([nodeId, ls]) => ({ nodeId, lanes: ls.filter((l) => sources.includes(l.source)) }))
    .filter((n) => n.lanes.length > 0);
  return { country: "RU", sources, sinceUnix: SINCE, nowUnix: NOW, nodes: sources.length ? nodes : [] };
}

const vantage = (source: string, key: string, state: string, over: Record<string, unknown> = {}) => ({
  source, key, network: key, region: "", asn: "", kind: "unknown", state, atUnix: NOW - 260, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach", ...over,
});
const check = (nodeId: string, verdict: CountryVerdict, over: Record<string, unknown> = {}) => ({
  nodeId, country: "RU", verdict, atUnix: NOW - 260, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "",
  sources: ["globalping"], globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [], sourceVerdicts: [],
  ...over,
});
const countryChecks = [
  check("nod_de1", OK),
  check("nod_fi1", TCP, { confidence: "conflict", sources: ["globalping", "tspu"], globalpingVerdict: OK, tspuVerdict: TCP, tspuAtUnix: NOW - 700, tspuVantageName: "probe1" }),
  check("nod_nl1", OK, { listedBy: ["antifilter_ipsum"] }),
  check("nod_probe1", OK),
  check("nod_se1", CountryVerdict.LISTED, { confidence: "listed", sources: ["lists"], listedBy: ["antifilter_ipsum"], globalpingVerdict: CountryVerdict.UNKNOWN, reason: "too_few_probes" }),
];
const alert = (id: string, nodeName: string, verdict: string, firstSeenUnix: number, resolvedAtUnix = 0) => ({
  id, severity: AlertSeverity.WARNING, kind: AlertKind.COUNTRY_BLOCKED, nodeId: `nod_${nodeName}`, nodeName, subject: "", titleKey: "", whyKey: "",
  params: { verdict, country: "RU" }, firstSeenUnix, openedUnix: firstSeenUnix, lastSeenUnix: NOW, resolvedAtUnix, resolution: resolvedAtUnix ? "cleared" : "", actions: [], mutedUntilUnix: 0,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let search: Record<string, unknown> = {};
const navigate = vi.fn();
beforeEach(() => {
  role = Role.OWNER;
  getHistory.mockResolvedValue(history(["globalping", "lists", "tspu"]));
  getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks, tspuRun: { status: "ok", atUnix: NOW - 700, nodeId: "nod_probe1" } });
  listAlerts.mockResolvedValue({
    nowUnix: NOW,
    active: [alert("alt_fi1", "fi1", "tcp_blocked", NOW - 29 * H)],
    history: [alert("alt_nl1", "nl1", "tcp_blocked", CUT + 1800, CUT + 16 * H + 1800), alert("alt_old", "de1", "blocked", NOW - 20 * 24 * H, NOW - 9 * 24 * H), { ...alert("alt_x", "de1", "", NOW - H, NOW - 60), kind: AlertKind.NODE_DOWN }],
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  search = {};
  navigate.mockReset();
  window.history.replaceState(null, "", window.location.pathname);
  act(() => setLang("en"));
  for (const m of [getHistory, getChecks, listAlerts]) m.mockReset();
  fleetV6 = {};
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ReachabilityScreen />
      </QueryClientProvider>,
    );
  // called from a click, inside its act(): the address changes and the page is drawn again, as the router would
  navigate.mockImplementation((to: { search: Record<string, unknown> }) => {
    search = to.search;
    draw();
  });
  await act(async () => draw());
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const click = async (el: Element | undefined | null) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
const text = () => document.body.textContent ?? "";
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-testid=reach-node]")];
const row = (name: string) => rows().find((r) => r.querySelector(".hl-cnode b")?.textContent === name)!;
const trigger = (name: string) => row(name).querySelector<HTMLElement>("button.rc-r")!;
/** opens the row of a node (one at a time, as a click does); the board starts with one open (the first that is not all right), so a row that is open stays */
const open = async (name: string) => {
  if (trigger(name).getAttribute("aria-expanded") !== "true") await click(trigger(name));
};
const detail = (name: string) => row(name).querySelector<HTMLElement>(".rc-d")!;
const line = (name: string, source: string) => detail(name).querySelector<HTMLElement>(`[data-testid=line-${source}]`)!;
const segs = (name: string, source: string) => [...line(name, source).querySelectorAll<HTMLElement>("[data-seg]")];
const cell = (name: string, source: string) => row(name).querySelector<HTMLElement>(`[data-testid=cell-${source}]`)!;
const tab = (name: string) => [...document.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name));
/** The text a reader gets: without the decorative glyphs ("!", "×") that are aria-hidden. */
function words(el: Element | null | undefined): string {
  if (!el) return "";
  const copy = el.cloneNode(true) as Element;
  for (const h of copy.querySelectorAll("[aria-hidden]")) h.remove();
  return copy.textContent ?? "";
}
/** The chips of the summary: the board's state (ok, part, tcp, bad, reg, nd) and what a chip says. */
const tally = () => Object.fromEntries([...document.querySelectorAll<HTMLElement>(".rc-sumf button")].map((b) => [b.className, words(b)]));

describe("the Reachability page with every source on", () => {
  it("names the country in its head and offers the owner the country picker", async () => {
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("Reachability");
    expect(text()).toContain("Whether the nodes open from Russia");
    expect(document.querySelector("[aria-label=Country]")?.textContent).toContain("Russia");
    expect(getHistory).toHaveBeenCalledWith({ days: 7 }, expect.anything());
  });

  it("has three tabs, Nodes first, with the count of the events and of the sources that are on", async () => {
    await mount();
    expect(document.querySelector("[role=tablist][aria-label='Reachability sections']")).not.toBeNull();
    expect([...document.querySelectorAll("[role=tab]")].map((t) => t.textContent).slice(0, 3)).toEqual(["Nodes", "Events2", "Sources5"]);
    expect(tab("Nodes")?.getAttribute("aria-selected")).toBe("true");
    expect(tab("Events")?.querySelector(".bdg")?.textContent).toBe("2");
  });

  it("counts the nodes by their verdict now, as the summary of the board: the share of the ones that open, the states as a filter, the bar", async () => {
    await mount();
    expect(document.querySelector(".rc-sumh b")?.textContent).toBe("3 of 5");
    expect(document.querySelector(".rc-sumh span")?.textContent).toBe("nodes open from Russia without caveats");
    expect(tally()).toEqual({ ok: "reachable3", tcp: "TCP blocked1", reg: "listed1" });
    expect([...document.querySelectorAll<HTMLElement>(".rc-bar i")].map((i) => [i.className, i.style.flexGrow])).toEqual([["ok", "3"], ["tcp", "1"], ["reg", "1"]]);
    // a state nobody is in is said once, not drawn
    expect(document.querySelector(".rc-zero")?.textContent).toBe("No nodes with the verdict: some networks and unreachable");
    expect(document.querySelectorAll(".rc-sumf button[aria-pressed=true]")).toHaveLength(0);
  });

  it("lists the nodes by name, without the one that waits for its install, each with its verdict as a pill", async () => {
    await mount();
    expect(rows().map((r) => r.querySelector(".hl-cnode b")?.textContent)).toEqual(["de1", "fi1", "nl1", "probe1", "se1"]);
    expect(rows().map((r) => words(r.querySelector(".pv-pill")))).toEqual(["reachable", "TCP blocked", "reachable", "reachable", "listed"]);
    expect(row("fi1").querySelector(".pv-pill")?.className).toContain("part");
    expect(row("se1").querySelector(".pv-pill")?.className).toContain("bad");
    // the row is a button that opens, and a pill is not a tab stop of its own
    expect(trigger("de1").getAttribute("aria-expanded")).toBe("false");
    expect(row("de1").querySelector("[role=img][tabindex='0']")).toBeNull();
  });

  it("gives each source its word for the node now: the lists say listed or not, the probe node is named, a source with nothing yet has a dash", async () => {
    await mount();
    const now = (name: string) => ["globalping", "lists", "tspu"].map((s) => words(cell(name, s)));
    expect(now("de1")).toEqual(["reachable", "not listed", "reachable"]);
    expect(now("fi1")).toEqual(["reachable", "not listed", "TCP blocked"]);
    expect(now("nl1")).toEqual(["reachable", "listed", "reachable"]);
    expect(now("probe1")).toEqual(["reachable", "not listed", "probe node"]);
    expect(now("se1")).toEqual(["no data", "listed", "—"]);
    expect(cell("fi1", "tspu").className).toContain("part");
    expect(cell("se1", "tspu").className).toContain("mut");
  });

  it("draws the week as seven cells, the worst verdict of each day over every source", async () => {
    await mount();
    const days = (name: string) => [...row(name).querySelectorAll<HTMLElement>(".rc-days i")].map((i) => i.className);
    expect(days("de1")).toEqual(Array(7).fill("ok"));
    // fi1: the probe saw TCP blocked 30 h ago: that is yesterday, or the day before it in the small hours; the first four days are well whatever the clock says
    expect(days("fi1").slice(0, 4)).toEqual(Array(4).fill("ok"));
    expect(days("fi1").slice(4)).toContain("part");
    expect(row("de1").querySelector(".rc-days")?.getAttribute("aria-label")).toBe("reachable: no problems in 7 days");
    expect(row("de1").querySelector(".rc-days i")?.getAttribute("title")).toMatch(/: reachable$/);
  });

  it("lists the country_blocked alerts of the week in the card of the Nodes tab, open first, each a link to its alert", async () => {
    await mount();
    const events = [...document.querySelectorAll("[data-testid=reach-event]")];
    expect(events.map((e) => e.getAttribute("href"))).toEqual(["/health#alert-alt_fi1", `/health#alert-alt_nl1-${CUT + 16 * H + 1800}`]);
    expect(events[0]?.textContent).toContain("fi1 · TCP blocked");
    expect(events[0]?.textContent).toContain("opened 1 d ago");
    expect(events[1]?.textContent).toContain("nl1 · TCP blocked");
    expect(events[1]?.textContent).toContain("lasted 16 h");
  });

  it("brings the Events tab with the same alerts, each with its pill, and what opens and closes an event", async () => {
    search = { tab: "events" };
    await mount();
    expect(tab("Events")?.getAttribute("aria-selected")).toBe("true");
    const events = [...document.querySelectorAll("[data-testid=reach-event]")];
    expect(events.map((e) => words(e.querySelector(".pv-pill")))).toEqual(["Open", "Closed"]);
    expect(text()).toContain("An event opens when a node stops opening from Russia and closes when the recheck is clean");
    expect(document.querySelector(".rc-sum")).toBeNull();
  });
});

describe("the tabs", () => {
  it("put the tab in the address: a click goes to ?tab=, Nodes is the page's own and has no key", async () => {
    await mount();
    await click(tab("Events"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: { tab: "events" }, replace: true, resetScroll: false });
    expect(tab("Events")?.getAttribute("aria-selected")).toBe("true");
    await click(tab("Nodes"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: {}, replace: true, resetScroll: false });
    await click(tab("Sources"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: { tab: "sources" }, replace: true, resetScroll: false });
  });

  it("open the Sources tab for a ?source= link, with that source open", async () => {
    search = { source: "bsbord" };
    await mount();
    expect(tab("Sources")?.getAttribute("aria-selected")).toBe("true");
    expect([...document.querySelectorAll<HTMLElement>(".rc-sp > [role=tabpanel]:not([hidden]) [data-source]")].map((s) => s.getAttribute("data-source"))).toEqual(["bsbord"]);
  });

  it.each([
    ["#sources", { tab: "sources" }],
    ["#reach-sources", { tab: "sources" }],
    ["#reach-events", { tab: "events" }],
    ["#reach-nodes", {}],
  ])("lead the old anchor %s to its tab and take it off the address", async (hash, to) => {
    window.history.replaceState(null, "", `${window.location.pathname}${hash}`);
    await mount();
    expect(navigate).toHaveBeenCalledWith({ to: "/reachability", search: to, replace: true, resetScroll: false });
  });

  it("show the Sources tab at once for the old #sources anchor", async () => {
    window.history.replaceState(null, "", `${window.location.pathname}#sources`);
    await mount();
    expect(tab("Sources")?.getAttribute("aria-selected")).toBe("true");
  });
});

describe("a row of the table", () => {
  it("opens on a click and takes the one that was open with it, one at a time", async () => {
    await mount();
    await open("de1");
    expect(trigger("de1").getAttribute("aria-expanded")).toBe("true");
    expect(row("de1").classList.contains("open")).toBe(true);
    expect(trigger("de1").getAttribute("aria-controls")).toBe(detail("de1").id);
    await open("fi1");
    expect(detail("de1")).toBeNull();
    expect(trigger("fi1").getAttribute("aria-expanded")).toBe("true");
    await click(trigger("fi1"));
    expect(detail("fi1")).toBeNull();
  });

  it("starts as the board does, with one row open: the first that is not all right", async () => {
    await mount();
    const opened = rows().filter((r) => r.querySelector("button.rc-r")?.getAttribute("aria-expanded") === "true");
    expect(opened).toHaveLength(1);
    expect(opened[0]).toBe(rows().find((r) => r.getAttribute("data-state") !== "ok"));
    await click(trigger(opened[0]!.querySelector(".hl-cnode b")!.textContent!));
    expect(rows().some((r) => r.querySelector("button.rc-r")?.getAttribute("aria-expanded") === "true")).toBe(false);
  });

  it("says in the open row what the sources said, the verdict in full, and leads to the node and to its alert", async () => {
    await mount();
    await open("fi1");
    const d = detail("fi1");
    expect(words(d.querySelector(".rc-dsrc"))).toContain("Globalping: reachable");
    expect(words(d.querySelector(".rc-dsrc"))).toContain("Probe: TCP blocked");
    expect(words(d.querySelector("[data-testid=reach-verdict]"))).toContain("The sources disagree");
    expect(words(d.querySelector("[data-testid=reach-verdict]"))).toContain("Globalping: reachable · probe: TCP blocked");
    const links = [...d.querySelectorAll(".hl-act a")];
    expect(links.map((a) => a.textContent)).toEqual(["Open the node", "Open the alert"]);
    expect(links.map((a) => a.getAttribute("href"))).toEqual(["/nodes/$id", "/health#alert-alt_fi1"]);
    // a node that is well has no alert to open
    await open("de1");
    expect([...detail("de1").querySelectorAll(".hl-act a")].map((a) => a.textContent)).toEqual(["Open the node"]);
  });

  it("is a button a keyboard reaches, with its state said", async () => {
    await mount();
    const b = trigger("nl1");
    act(() => b.focus());
    expect(document.activeElement).toBe(b);
    expect(b.tagName).toBe("BUTTON");
    expect(b.getAttribute("aria-expanded")).toBe("false");
  });
});

describe("the chips of the summary", () => {
  it("filter the rows by the state, press again to clear, and say so when nobody is in it", async () => {
    await mount();
    const chip = (k: string) => document.querySelector<HTMLElement>(`.rc-sumf button.${k}`)!;
    await click(chip("tcp"));
    expect(chip("tcp").getAttribute("aria-pressed")).toBe("true");
    expect(rows().map((r) => r.querySelector(".hl-cnode b")?.textContent)).toEqual(["fi1"]);
    await click(chip("tcp"));
    expect(rows()).toHaveLength(5);
    await click(chip("reg"));
    expect(rows().map((r) => r.querySelector(".hl-cnode b")?.textContent)).toEqual(["se1"]);
  });
});

describe("the lanes", () => {
  it("draw the compacted week of a source as segments, each saying its verdict and time, in the open row", async () => {
    await mount();
    expect(row("nl1").querySelector("[data-testid=lane-globalping]")).toBeNull(); // the row is closed: the cells say the week
    await open("nl1");
    const gp = segs("nl1", "globalping");
    expect(gp).toHaveLength(3);
    expect(gp.map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable", "TCP blocked", "reachable"]);
    expect(gp[2]?.getAttribute("aria-label")).toMatch(/ – now$/);
    expect(line("nl1", "globalping").querySelectorAll("[data-verdict]")).toHaveLength(3);
    expect([...line("nl1", "lists").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["listed"]);
    // the lists' "ok" is their own word
    await open("de1");
    expect(segs("de1", "lists")[0]?.getAttribute("aria-label")).toMatch(/^not listed, /);
  });

  it("leave a source with nothing on the node empty: no segment, no tab stop", async () => {
    await mount();
    await open("probe1");
    expect(segs("probe1", "tspu")).toHaveLength(0);
    expect(line("probe1", "tspu").querySelector("[data-testid=lane-tspu]")?.getAttribute("aria-label")).toBe("Probe, last 7 days");
    await open("se1");
    expect(segs("se1", "tspu")).toHaveLength(0);
  });

  it("are one tab stop on the newest segment; the arrows walk the lane and the tooltip follows the focus", async () => {
    await mount();
    await open("nl1");
    const gp = segs("nl1", "globalping");
    expect(gp.map((s) => s.tabIndex)).toEqual([-1, -1, 0]);
    await act(async () => gp[2]!.focus());
    await act(async () => void gp[2]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })));
    for (let i = 0; i < 3; i++) await settle();
    expect(document.activeElement).toBe(gp[1]);
    expect(gp.map((s) => s.tabIndex)).toEqual([-1, 0, -1]);
    expect(text()).toContain("16 h"); // the cut's tooltip: verdict, range, how long
    await act(async () => void gp[1]!.dispatchEvent(new KeyboardEvent("keydown", { key: "Home", bubbles: true })));
    expect(document.activeElement).toBe(gp[0]);
    await act(async () => void gp[0]!.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft", bubbles: true })));
    expect(document.activeElement).toBe(gp[0]); // the first stays first
  });
});

// ---------------------------------------------------------------------------------------------------
// A block on some networks (PARTIAL): de1 is dark on MTS while Beeline reaches it, and MegaFon and T2 had no probe

describe("a block on some networks", () => {
  const PARTIAL = CountryVerdict.PARTIAL;
  const gp = (network: string, asn: number, city: string, reached: boolean) => ({
    network, asn, city, icmpOk: reached, icmpMs: reached ? 44 : 0, tcp: [{ port: 443, ok: reached, ms: reached ? 44 : 0 }], valid: true, dark: !reached, eyeball: true,
  });
  beforeEach(() => {
    const h = history(["globalping", "lists", "tspu"]);
    getHistory.mockResolvedValue({
      ...h,
      nodes: h.nodes.map((n) => (n.nodeId === "nod_de1" ? { ...n, lanes: n.lanes.map((l) => (l.source === "globalping" ? lane("globalping", pts([SINCE + 300, OK], [NOW - 5 * H, PARTIAL])) : l)) } : n)),
    });
    getChecks.mockResolvedValue({
      nowUnix: NOW, intervalS: 300, columns: [], rows: [], tspuRun: { status: "ok", atUnix: NOW - 700, nodeId: "nod_probe1" },
      countryChecks: countryChecks.map((c) =>
        c.nodeId === "nod_de1"
          ? check("nod_de1", PARTIAL, {
              probes: [gp("MTS", 8359, "Novosibirsk", false), gp("Beeline", 3216, "Moscow", true)],
              noProbe: ["MegaFon", "T2"],
              vantages: [vantage("globalping", "AS8359", "dark", { network: "MTS", asn: "8359", region: "Novosibirsk" }), vantage("globalping", "AS3216", "reach", { network: "Beeline", asn: "3216", region: "Moscow" })],
            })
          : c,
      ),
    });
    listAlerts.mockResolvedValue({ nowUnix: NOW, active: [{ ...alert("alt_de1", "de1", "partial", NOW - 3 * H), subject: "partial" }], history: [] });
  });

  it("counts it apart from TCP blocked: its own chip, the warn tone, the short word", async () => {
    await mount();
    expect(tally()).toEqual({ ok: "reachable2", part: "some networks1", tcp: "TCP blocked1", reg: "listed1" });
    expect(document.querySelector(".rc-sumf .part")?.getAttribute("aria-pressed")).toBe("false");
    expect(document.querySelector(".rc-sumh b")?.textContent).toBe("2 of 5");
  });

  it("says it in the pill and the source's word now, and its verdict in the open row", async () => {
    await mount();
    expect(words(row("de1").querySelector(".pv-pill"))).toBe("some networks");
    expect(row("de1").querySelector(".pv-pill")?.className).toContain("part");
    expect(words(cell("de1", "globalping"))).toBe("some networks");
    expect(cell("de1", "globalping").className).toContain("part");
    await open("de1");
    expect(words(detail("de1").querySelector("[data-testid=reach-verdict]"))).toContain("Reachable from Russia, but not from every network");
    expect(words(detail("de1").querySelector("[data-testid=reach-verdict]"))).toContain("MTS");
  });

  it("splits its lane segment, which says the verdict in full, and names it in the recent events", async () => {
    await mount();
    await open("de1");
    const gpLane = segs("de1", "globalping");
    expect(gpLane.map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable", "blocked on some networks"]);
    expect([...line("de1", "globalping").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["ok", "partial"]);
    const events = [...document.querySelectorAll("[data-testid=reach-event]")];
    expect(events.map((e) => e.textContent)).toEqual([expect.stringContaining("de1 · blocked on some networks")]);
  });

  it("opens its breakdown with the row, in full, on that node only", async () => {
    await mount();
    // the board starts with one row open: the first that is not all right, here de1 (some networks)
    expect(document.querySelectorAll("[data-testid=partial-breakdown]")).toHaveLength(1);
    await open("de1");
    const more = detail("de1").querySelector<HTMLElement>("[data-testid=partial-breakdown]")!;
    const lists = [...more.querySelectorAll("ul")];
    expect(lists.map((ul) => document.getElementById(ul.getAttribute("aria-labelledby")!)?.textContent)).toEqual(["Cannot reach the node", "Reach it"]);
    expect([...more.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["✕MTS AS8359 · Novosibirsk · Globalping", "✓Beeline AS3216 · Moscow · Globalping"]);
    expect(more.textContent).toContain("Not checked this round: MegaFon, T2");
    // the box takes the verdict's tone
    expect(more.className).toContain("part");
    // fi1 is TCP blocked with nothing dark named: no breakdown
    await open("fi1");
    expect(detail("fi1").querySelector("[data-testid=partial-breakdown]")).toBeNull();
  });

  it("speaks Russian", async () => {
    act(() => setLang("ru"));
    await mount();
    expect(tally()).toMatchObject({ part: "часть сетей1", tcp: "TCP режут1" });
    expect(words(row("de1").querySelector(".pv-pill"))).toBe("часть сетей");
    expect(document.querySelector("[data-testid=reach-event]")?.textContent).toContain("de1 · недоступна из части сетей");
    await open("de1");
    expect(segs("de1", "globalping")[1]?.getAttribute("aria-label")).toMatch(/^недоступна из части сетей, /);
  });
});

// ---------------------------------------------------------------------------------------------------
// cheburcheck and bsbord: their lines while they are on, the breakdown across sources, and a network that reaches no node

describe("the external sources", () => {
  const PARTIAL = CountryVerdict.PARTIAL;
  const BLOCKED = CountryVerdict.BLOCKED;
  const crimea = (state: string) => vantage("bsbord", "mts|krym|off", state, { network: "MTS", region: "КРЫМ", kind: "mobile", atUnix: NOW - 3 * H });
  const volna = vantage("bsbord", "volna|krym|on", "whitelist", { network: "Volna", region: "КРЫМ", kind: "mobile", atUnix: NOW - 3 * H });
  const rostelecom = vantage("cheburcheck", "probe:7", "reach", { network: "Rostelecom", asn: "12389", region: "Moscow", atUnix: NOW - 2 * H });
  beforeEach(() => {
    const h = history(["globalping", "lists", "tspu"]);
    const ext = (nodeId: string) =>
      nodeId === "nod_de1" ? [lane("cheburcheck", pts([SINCE + 3 * H, OK])), lane("bsbord", pts([SINCE + 6 * H, OK], [NOW - 27 * H, PARTIAL]))] : nodeId === "nod_nl1" ? [lane("bsbord", pts([NOW - 27 * H, BLOCKED]))] : [];
    getHistory.mockResolvedValue({ ...h, sources: ["globalping", "lists", "tspu", "cheburcheck", "bsbord"], nodes: h.nodes.map((n) => ({ ...n, lanes: [...n.lanes, ...ext(n.nodeId)] })) });
    getChecks.mockResolvedValue({
      nowUnix: NOW, intervalS: 300, columns: [], rows: [], tspuRun: { status: "ok", atUnix: NOW - 700, nodeId: "nod_probe1" },
      countryChecks: countryChecks.map((c) =>
        c.nodeId === "nod_de1"
          ? check("nod_de1", PARTIAL, {
              sources: ["globalping", "cheburcheck", "bsbord"],
              vantages: [vantage("globalping", "AS3216", "reach", { network: "Beeline", asn: "3216", region: "Moscow" }), rostelecom, crimea("dark"), volna],
              sourceVerdicts: [
                { source: "globalping", verdict: OK, eyeball: true, atUnix: NOW - 260, reason: "" },
                { source: "cheburcheck", verdict: OK, eyeball: true, atUnix: NOW - 2 * H, reason: "" },
                { source: "bsbord", verdict: BLOCKED, eyeball: true, atUnix: NOW - 3 * H, reason: "" },
              ],
            })
          : c.nodeId === "nod_nl1"
            ? { ...c, vantages: [vantage("globalping", "AS3216", "reach", { network: "Beeline" }), crimea("dark")] }
            : c,
      ),
    });
  });

  it("give each node a line for cheburcheck and bsbord in the open row while they are on, with the word now and the week; the own probe has none", async () => {
    await mount();
    await open("de1");
    expect(["globalping", "lists", "tspu", "cheburcheck", "bsbord"].map((s) => line("de1", s).firstElementChild?.textContent)).toEqual(["Globalping", "Lists", "Probe", "cheburcheck", "bsbord"]);
    expect(detail("de1").querySelector("[data-testid=line-cheburcheck_own]")).toBeNull();
    expect(words(line("de1", "bsbord").children[1])).toBe("some networks");
    expect([...line("de1", "bsbord").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["ok", "partial"]);
    expect(segs("de1", "cheburcheck").map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable"]);
    expect(line("de1", "bsbord").querySelector("[role=group]")?.getAttribute("aria-label")).toBe("bsbord, last 7 days");
    await open("nl1");
    expect(words(line("nl1", "bsbord").children[1])).toBe("unreachable");
    // a node bsbord has nothing on yet: a dash, no segment
    await open("fi1");
    expect(line("fi1", "bsbord").children[1]?.textContent).toBe("—");
    expect(segs("fi1", "bsbord")).toHaveLength(0);
  });

  it("break a verdict down across every source, the white-list unit apart", async () => {
    await mount();
    await open("de1");
    const more = detail("de1").querySelector<HTMLElement>("[data-testid=partial-breakdown]")!;
    const lists = [...more.querySelectorAll("ul")];
    expect(lists.map((ul) => document.getElementById(ul.getAttribute("aria-labelledby")!)?.textContent)).toEqual(["Cannot reach the node", "Reach it", "White list on (cuts every foreign address)"]);
    expect(lists.map((ul) => [...ul.querySelectorAll("li")].map((li) => li.textContent))).toEqual([
      ["✕MTS КРЫМ · mobile · bsbord"],
      ["✓Beeline AS3216 · Moscow · Globalping", "✓Rostelecom AS12389 · Moscow · cheburcheck"],
      ["○Volna КРЫМ · mobile · bsbord"],
    ]);
    expect(more.textContent).toContain("Checked: Globalping 4 min ago · cheburcheck 2 h ago · bsbord 3 h ago");
  });

  it("say which network reaches none of the nodes, the case no alert covers", async () => {
    await mount();
    const lines = [...document.querySelectorAll("[data-testid=dark-everywhere]")];
    expect(lines.map((l) => l.textContent)).toEqual(["MTS (КРЫМ) reaches none of your nodes · bsbord"]);
    const box = lines[0]!.closest("section")!;
    expect(box.getAttribute("aria-label")).toBe("Networks that reach none of your nodes");
    expect(box.textContent).toContain("No alert covers this: the network cuts every node alike.");
    // it sits under the summary, before the nodes
    expect(box.previousElementSibling?.className).toContain("rc-sum");
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("ru"));
    await mount();
    expect(document.querySelector("[data-testid=dark-everywhere]")?.textContent).toBe("MTS (КРЫМ) не достаёт ни до одной твоей ноды · bsbord");
    await open("de1");
    expect(line("de1", "bsbord").firstElementChild?.textContent).toBe("bsbord");
  });

  it("say nothing of the kind while every network reaches some node", async () => {
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks });
    await mount();
    expect(document.querySelector("[data-testid=dark-everywhere]")).toBeNull();
  });
});

describe("the Reachability page with the sources off", () => {
  it("says no source is connected and leads the owner to them; no summary, no nodes", async () => {
    getHistory.mockResolvedValue(history([]));
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] });
    await mount();
    const off = document.querySelector(".rc-emp")!;
    expect(off.textContent).toContain("No sources connected");
    expect(off.textContent).toContain("Until a source is on, the panel does not know whether the nodes open from Russia.");
    expect([...off.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Connect a source"]);
    expect(document.querySelector(".rc-sumf")).toBeNull();
    expect(rows()).toHaveLength(0);
    await click(off.querySelector("button"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: { tab: "sources" }, replace: true, resetScroll: false });
  });

  it("keeps the alerts of the week on the Events tab and says why when there are none", async () => {
    getHistory.mockResolvedValue(history([]));
    search = { tab: "events" };
    await mount();
    expect(document.querySelectorAll("[data-testid=reach-event]")).toHaveLength(2);
    act(() => root?.unmount());
    host?.remove();
    listAlerts.mockResolvedValue({ nowUnix: NOW, active: [], history: [] });
    await mount();
    expect(text()).toContain("No events: no sources are connected");
  });

  it("dims a source that is off and leaves its lanes empty, while the others draw", async () => {
    getHistory.mockResolvedValue(history(["globalping", "tspu"]));
    await mount();
    for (const name of ["de1", "nl1", "se1"]) {
      expect(words(cell(name, "lists"))).toBe("off");
      expect(cell(name, "lists").className).toContain("mut");
    }
    await open("nl1");
    const l = line("nl1", "lists");
    expect(l.getAttribute("data-on")).toBe("false");
    expect(l.className).toContain("off");
    expect(l.children[1]?.textContent).toBe("off");
    expect(l.querySelector("[data-verdict]")).toBeNull();
    expect(l.querySelector(".rc-track.off")).not.toBeNull();
    expect(segs("nl1", "globalping")).toHaveLength(3);
    expect(segs("nl1", "tspu")).toHaveLength(3);
  });

  it("tells anyone but the owner who switches them on, without the cards or the picker", async () => {
    role = Role.READONLY;
    getHistory.mockResolvedValue(history([]));
    await mount();
    expect(document.querySelector(".rc-emp")?.textContent).toContain("Nothing is checked until the owner switches a source on.");
    expect(document.querySelector(".rc-emp button")).toBeNull();
    expect(document.querySelector("[aria-label=Country]")).toBeNull();
    expect(document.querySelectorAll(".rc-sd")).toHaveLength(0);
    // the country of the head comes from the history, which every role reads
    expect(text()).toContain("Whether the nodes open from Russia");
    act(() => root?.unmount());
    host?.remove();
    search = { tab: "sources" };
    await mount();
    expect(text()).toContain("Only the owner sees and changes the sources.");
  });
});

describe("the states of the load", () => {
  it("shows the grey cards while the history comes, and the tabs stay", async () => {
    getHistory.mockReturnValue(new Promise(() => {}));
    await mount();
    expect(document.querySelector(".hl-skl")?.getAttribute("aria-busy")).toBe("true");
    expect(tab("Nodes")).toBeDefined();
    expect(tab("Events")?.querySelector(".bdg")).toBeNull(); // no count until the page is ready
  });

  it("says the page did not load, with a way to ask again", async () => {
    getHistory.mockRejectedValueOnce(new Error("down"));
    await mount();
    const err = document.querySelector<HTMLElement>(".pv-qe")!;
    expect(err.textContent).toContain("Reachability failed to load");
    expect(err.textContent).toContain("Nothing changed on the nodes.");
    await click(err.querySelector("button"));
    expect(rows()).toHaveLength(5);
  });

  it("says the first round is running while sources are on and nothing has been checked yet", async () => {
    getHistory.mockResolvedValue({ ...history(["globalping", "lists", "tspu"]), nodes: [] });
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] });
    await mount();
    expect(document.querySelector("[data-testid=first-round]")?.textContent).toContain("The first round of checks is running");
    expect(document.querySelector(".rc-sumh b")?.textContent).toBe("—");
    expect(document.querySelector(".rc-sumh span")?.textContent).toBe("waiting for the first round of checks");
    expect(rows().every((r) => words(r.querySelector(".pv-pill")).startsWith("no data"))).toBe(true);
    // each source waits for its round, in the quiet tone (the board's "ждёт круга")
    const cells = [...document.querySelectorAll("[data-testid^=cell-]")];
    expect(cells.length).toBeGreaterThan(0);
    expect(cells.every((c) => c.textContent === "waits for a round" && c.className.includes("mut"))).toBe(true);
  });

  it("says there are no nodes yet", async () => {
    const was = fleet.splice(0);
    try {
      await mount();
      expect(document.querySelector(".rc-emp")?.textContent).toContain("No nodes yet");
      expect(document.querySelector(".rc-emp a")?.getAttribute("href")).toBe("/nodes");
    } finally {
      fleet.push(...was);
    }
  });
});

describe("the points of own probes, for everyone who reads the page", () => {
  const probes = [
    vantage("probes", "probe:prb_a", "reach", { network: "ExampleNet · Springfield", probeName: "kitchen", asn: "64496", kind: "home", why: "" }),
    vantage("probes", "probe:prb_b", "uncertain", { network: "Demo Telecom · Shelbyville", probeName: "office", asn: "64497", kind: "dc", why: "no_filter" }),
  ];

  it("gives a helper the lines of the probes under a node, and no card, no button: the sources and the probes are the owner's", async () => {
    role = Role.HELPER;
    getHistory.mockResolvedValue(history(["globalping", "probes"]));
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [check("nod_de1", OK, { sources: ["globalping", "probes"], vantages: probes })] });
    await mount();
    await open("de1");
    const points = [...detail("de1").querySelectorAll("[data-testid=own-points] li")].map((li) => li.textContent);
    expect(points).toEqual([
      "?Demo Telecom · Shelbyville — probe “office” no filtering on its path, so “reachable” proves nothing · 4 min ago",
      "✓ExampleNet · Springfield — probe “kitchen” reaches the node · 4 min ago",
    ]);
    expect(line("de1", "probes").getAttribute("data-on")).toBe("true");
    expect(document.querySelectorAll(".rc-sd")).toHaveLength(0);
    expect(text()).toContain("Only the owner sees and changes the sources.");
    const labels = [...document.querySelectorAll("button")].map((b) => b.textContent?.trim());
    for (const word of ["Revoke", "Add a probe", "Check now", "Update command", "Confirm network", "Disconnect"]) expect(labels).not.toContain(word);
    // nobody sees an address: the lines hold the operator, the city and the name
    expect(text()).not.toMatch(/\d+\.\d+\.\d+\.\d+/);
  });

  it("puts a dark point in the breakdown of the node, with the reason, and not twice", async () => {
    getHistory.mockResolvedValue(history(["globalping", "probes"]));
    getChecks.mockResolvedValue({
      nowUnix: NOW, intervalS: 300, columns: [], rows: [],
      countryChecks: [check("nod_de1", CountryVerdict.PARTIAL, { sources: ["probes"], vantages: [vantage("probes", "probe:prb_a", "dark", { network: "ExampleNet · Springfield", probeName: "kitchen", port: 443, asn: "64496", kind: "home", why: "tls_reset" })] })],
    });
    await mount();
    await open("de1");
    expect(words(detail("de1").querySelector("[data-testid=partial-breakdown]"))).toContain("ExampleNet · Springfield — probe “kitchen” AS64496 · home · TLS is reset (443)");
    expect(detail("de1").querySelector("[data-testid=own-points]")).toBeNull();
  });
});

describe("the sources in the Nodes tab, for the owner", () => {
  it("lists the seven in a card, each leading to its own place", async () => {
    await mount();
    const names = [...document.querySelectorAll(".rc-sl button")].map((b) => b.querySelector("b")?.textContent);
    expect(names).toEqual(["Globalping", "Block lists", "TSPU probe", "cheburcheck", "bsbord", "kl!ck users", "Own probes"]);
    await click(document.querySelector(".rc-sl button"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: { source: "globalping" }, replace: true, resetScroll: false });
  });

  it("puts a note over the nodes for a source that does not work, and leads to it", async () => {
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: countryChecks.map((c) => ({ ...c, reason: "rate_limited" })) });
    await mount();
    const note = document.querySelector("[data-testid=source-warn]")!;
    expect(note.textContent).toContain("Globalping");
    expect(note.className).toContain("part");
    // the source in trouble says its data is old on every node; the others keep their words
    expect([...document.querySelectorAll("[data-testid=cell-globalping]")].every((c) => c.textContent === "no fresh data" && c.className.includes("mut"))).toBe(true);
    expect(document.querySelector("[data-testid=cell-lists]")?.textContent).not.toBe("no fresh data");
    await click(note.querySelector("button"));
    expect(navigate).toHaveBeenLastCalledWith({ to: "/reachability", search: { source: "globalping" }, replace: true, resetScroll: false });
  });
});

describe("the Reachability page in Russian and Chinese", () => {
  it("speaks both, the country in the genitive in Russian", async () => {
    act(() => setLang("ru"));
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("Доступность");
    expect(text()).toContain("Открываются ли ноды из России");
    expect(tally()).toMatchObject({ ok: "доступны3", tcp: "TCP режут1", reg: "в реестре1" });
    expect(document.querySelector(".rc-sumh b")?.textContent).toBe("3 из 5");
    expect(["globalping", "lists", "tspu"].map((s) => words(cell("probe1", s)))).toEqual(["доступна", "нет в списках", "нода-зонд"]);
    expect(tab("Ноды")).toBeDefined();
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("zh"));
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("可达性");
    expect(text()).toContain("能否从俄罗斯访问节点");
    expect(words(cell("de1", "lists"))).toBe("不在名单中");
  });
});

describe("IPv6 on the Reachability page", () => {
  // de1: no IPv6; fi1: IPv4 TCP blocked, IPv6 reachable; nl1: broken at the hoster; probe1: an old agent; se1: no IPv6 probes
  // in the country, and a week of IPv6 rounds that never got a verdict
  const v6 = (nodeId: string, verdict: CountryVerdict, reason = "") => check(nodeId, verdict, { reason, sources: reason ? [] : ["globalping"] });
  const by: Record<string, ReturnType<typeof check>> = {
    nod_de1: v6("nod_de1", CountryVerdict.UNKNOWN, "no_ipv6"),
    nod_fi1: v6("nod_fi1", OK),
    nod_nl1: v6("nod_nl1", CountryVerdict.UNKNOWN, "ipv6_broken"),
    nod_probe1: v6("nod_probe1", CountryVerdict.UNKNOWN, "agent_old"),
    nod_se1: v6("nod_se1", CountryVerdict.UNKNOWN, "no_probes"),
  };
  const week6 = {
    country: "RU", sources: ["globalping"], sinceUnix: SINCE, nowUnix: NOW, family: 6,
    nodes: [
      { nodeId: "nod_fi1", lanes: [lane("globalping", pts([NOW - 50 * H, OK]))] },
      { nodeId: "nod_se1", lanes: [lane("globalping", pts([NOW - 50 * H, CountryVerdict.UNKNOWN]))] },
    ],
  };
  beforeEach(() => {
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: countryChecks.map((c) => ({ ...c, ipv6: by[c.nodeId] })) });
    getHistory.mockImplementation((req: { family?: number }) => Promise.resolve(req.family === 6 ? week6 : history(["globalping", "lists", "tspu"])));
  });
  const pills = (name: string) => [...row(name).querySelectorAll<HTMLElement>(".rc-now .pv-pill")];
  const none = (name: string) => detail(name)?.querySelector("[data-testid=line-ipv6-none]");

  it("asks for the IPv6 week and puts an IPv6 pill after the IPv4 one, with its lane right under Globalping's", async () => {
    await mount();
    expect(getHistory).toHaveBeenCalledWith({ days: 7, family: 6 }, expect.anything());
    expect(pills("fi1").map((p) => p.textContent)).toEqual(["IPv4TCP blocked", "IPv6reachable"]);
    await open("fi1");
    expect(line("fi1", "globalping").nextElementSibling).toBe(line("fi1", "globalping6"));
    expect(words(line("fi1", "globalping6").children[1])).toBe("reachable");
    expect(line("fi1", "globalping6").querySelector("[data-testid=lane-globalping6]")?.getAttribute("aria-label")).toBe("Globalping · IPv6, last 7 days");
    expect(segs("fi1", "globalping6")).toHaveLength(1);
    expect(none("fi1")).toBeNull();
  });

  it("gives a node without an IPv6 verdict one quiet line why, in Globalping's place, and only the IPv4 pill", async () => {
    await mount();
    const why = async (name: string) => {
      await open(name);
      return none(name)?.lastElementChild?.textContent;
    };
    expect(await why("de1")).toBe("The server has no IPv6");
    expect(await why("nl1")).toBe("The server has an IPv6 address, but IPv6 does not work at the hoster");
    expect(await why("probe1")).toBe("The node agent is too old to report IPv6: update it");
    // a week of rounds without a verdict is no data either
    expect(await why("se1")).toBe("Globalping has no IPv6 probes in Russia right now");
    for (const name of ["de1", "nl1", "probe1", "se1"]) {
      expect(pills(name).map((p) => p.textContent?.slice(0, 4))).toEqual(["IPv4"]);
      await open(name);
      expect(detail(name).querySelector("[data-testid=line-globalping6]")).toBeNull();
      expect(line(name, "globalping").nextElementSibling).toBe(none(name));
    }
  });

  it("a node no longer measured over IPv6 (its entry off, IPv4 seen) gets the quiet line, not last week's red IPv6 lane", async () => {
    getChecks.mockResolvedValue({
      nowUnix: NOW, intervalS: 300, columns: [], rows: [],
      countryChecks: countryChecks.map((c) => ({ ...c, ipv6: c.nodeId === "nod_se1" ? v6("nod_se1", CountryVerdict.UNKNOWN, "entry_off") : by[c.nodeId] })),
    });
    getHistory.mockImplementation((req: { family?: number }) =>
      Promise.resolve(req.family === 6 ? { ...week6, nodes: [{ nodeId: "nod_se1", lanes: [lane("globalping", pts([NOW - 50 * H, CountryVerdict.BLOCKED]))] }] } : history(["globalping", "lists", "tspu"])),
    );
    await mount();
    await open("se1");
    expect(none("se1")?.lastElementChild?.textContent).toBe("The IPv6 entry is off and IPv4 is seen from here: IPv6 is not measured");
    expect(pills("se1").map((p) => p.textContent?.slice(0, 4))).toEqual(["IPv4"]);
    expect(detail("se1").querySelector("[data-testid=line-globalping6]")).toBeNull();
  });

  it("counts the nodes cut over IPv4 that IPv6 still reaches, under the IPv4 summary", async () => {
    await mount();
    expect(document.querySelector("[data-testid=ipv6-helps]")?.textContent).toContain("Cut over IPv4, open over IPv6: 1");
    expect(tally().tcp).toBe("TCP blocked1"); // the summary stays IPv4
  });

  it("tags the events: the IPv6 alert, and an IPv4 one whose node IPv6 reaches", async () => {
    listAlerts.mockResolvedValue({
      nowUnix: NOW,
      active: [{ ...alert("alt_fi1", "fi1", "tcp_blocked", NOW - 29 * H), params: { verdict: "tcp_blocked", country: "RU", ipv6: "open" } }],
      history: [{ ...alert("alt_v6", "de1", "blocked", NOW - 30 * H, NOW - 20 * H), subject: "ipv6", params: { verdict: "blocked", country: "RU", family: "6" } }],
    });
    await mount();
    const events = [...document.querySelectorAll("[data-testid=reach-event]")].map((e) => words(e));
    expect(events[0]).toContain("TCP blocked");
    expect(events[0]).toContain("reachable over IPv6");
    expect(events[1]).toContain("unreachable");
    expect(events[1]).toContain("IPv6");
  });

  describe("the hint for the IPv6 entry", () => {
    const entry = (over: object = {}) => ({ entry: "", entrySupported: true, unavailableReason: "", addresses: [{ address: "2001:db8::5", prefixLen: 64 }], src: "2001:db8::5", ...over });
    const hint = (name: string) => detail(name)?.querySelector<HTMLElement>("[data-testid=reach-v6-hint]");

    it("under a node cut over IPv4 that IPv6 reaches, with the entry off: the hint and, for the owner, the way to the entry's dialog", async () => {
      fleetV6 = { nod_fi1: entry(), nod_de1: entry() };
      await mount();
      await open("fi1");
      expect(hint("fi1")?.textContent).toBe("IPv4 is blocked from Russia, IPv6 gets through.Add IPv6 entry…");
      const add = hint("fi1")!.querySelector("a")!;
      expect(add.getAttribute("href")).toBe("/nodes/$id?tab=ways&way=ipv6&add=ipv6");
      expect(add.dataset.id).toBe("nod_fi1");
      // de1 is reachable over IPv4: nothing to say
      await open("de1");
      expect(hint("de1")).toBeNull();
    });

    it("says it without a button to a helper, and to the owner of a node that cannot take an entry", async () => {
      fleetV6 = { nod_fi1: entry({ entrySupported: false, unavailableReason: "agent_old" }) };
      await mount();
      await open("fi1");
      expect(hint("fi1")?.textContent).toBe("IPv4 is blocked from Russia, IPv6 gets through.");
      act(() => root?.unmount());
      host?.remove();
      role = Role.HELPER;
      fleetV6 = { nod_fi1: entry() };
      await mount();
      await open("fi1");
      expect(hint("fi1")?.querySelector("a")).toBeNull();
    });

    it("is gone once the entry is on", async () => {
      fleetV6 = { nod_fi1: entry({ entry: "2001:db8::5" }) };
      await mount();
      await open("fi1");
      expect(hint("fi1")).toBeNull();
    });
  });

  it("speaks Russian", async () => {
    act(() => setLang("ru"));
    fleetV6 = { nod_fi1: { entry: "", entrySupported: true, unavailableReason: "", addresses: [], src: "" } };
    await mount();
    await open("fi1");
    expect(detail("fi1").querySelector("[data-testid=reach-v6-hint]")?.textContent).toBe("Из России IPv4 заблокирован, а IPv6 проходит.Добавить вход по IPv6…");
    await open("de1");
    expect(none("de1")?.textContent).toBe("Globalping · IPv6На сервере нет IPv6"); // the source for a screen reader
    await open("se1");
    expect(none("se1")?.lastElementChild?.textContent).toBe("У Globalping сейчас нет IPv6-зондов из России");
    expect(document.querySelector("[data-testid=ipv6-helps]")?.textContent).toContain("По IPv4 режут, по IPv6 доступны: 1");
  });
});

describe("the Reachability page while Globalping does not check IPv6", () => {
  it("looks as before: no IPv6 week asked for, no family tags, no IPv6 lines", async () => {
    await mount();
    expect(getHistory.mock.calls.some(([req]) => (req as { family?: number }).family === 6)).toBe(false);
    const table = document.querySelector(".rc-main")?.textContent ?? "";
    expect(table).toContain("de1");
    expect(table).not.toMatch(/IPv[46]/);
    await open("de1");
    expect(document.querySelector("[data-testid=line-ipv6-none], [data-testid=line-globalping6], [data-testid=ipv6-helps]")).toBeNull();
  });
});
