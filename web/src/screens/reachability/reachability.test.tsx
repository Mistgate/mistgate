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
  nodes: { listNodes: () => Promise.resolve({ nodes: fleet }) },
  // the cards have their own tests (sources.test.tsx); here they only have to render
  globalping: { getGlobalping: () => Promise.resolve({ settings: { enabled: true, intervalMin: 15, hasToken: true } }) },
  blocklist: { getBlocklist: () => Promise.resolve({ settings: { enabled: true, available: true, checkedUnix: 0, listsUnix: 0 } }) },
  tspuProbe: { getTspuProbe: () => Promise.resolve({ settings: { enabled: true, available: true, unavailableReason: "", probeNodeId: "nod_probe1", intervalMin: 120, nodes: [] } }) },
  clientProbe: { getClientProbe: () => Promise.resolve({ settings: { enabled: false, reports: 0, lastUnix: 0 }, nodes: [], nowUnix: 0 }) },
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
  Link: ({ children, to, hash, className, title, ...rest }: { children?: ReactNode; to: string; hash?: string; className?: string; title?: string; "data-testid"?: string }) => (
    <a href={hash ? `${to}#${hash}` : to} className={className} title={title} data-testid={rest["data-testid"]}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const H = 3600;
const NOW = Math.floor(Date.now() / 1000);
const SINCE = NOW - 7 * 24 * H;
const CUT = SINCE + 3 * 24 * H + 10 * H;

const node = (id: string, name: string, countryCode: string, status = NodeStatus.ONLINE) => ({ id, name, countryCode, status });
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
  act(() => setLang("en"));
  for (const m of [getHistory, getChecks, listAlerts]) m.mockReset();
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ReachabilityScreen />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const rows = () => [...document.querySelectorAll<HTMLElement>("[data-testid=reach-node]")];
const row = (name: string) => rows().find((r) => r.querySelector("a span:last-child")?.textContent === name)!;
const line = (name: string, source: string) => row(name).querySelector<HTMLElement>(`[data-testid=line-${source}]`)!;
const segs = (name: string, source: string) => [...line(name, source).querySelectorAll<HTMLElement>("[data-seg]")];
/** The text a reader gets: without the decorative glyphs ("!", "×") that are aria-hidden. */
function words(el: Element | null | undefined): string {
  if (!el) return "";
  const copy = el.cloneNode(true) as Element;
  for (const h of copy.querySelectorAll("[aria-hidden]")) h.remove();
  return copy.textContent ?? "";
}
const tally = () => Object.fromEntries([...document.querySelectorAll("[data-kind]")].map((li) => [li.getAttribute("data-kind"), words(li)]));

describe("the Reachability page with every source on", () => {
  it("names the country in its subtitle and offers the owner the country picker", async () => {
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("Reachability");
    expect(text()).toContain("Whether the nodes open from Russia");
    expect(document.querySelector("[aria-label=Country]")?.textContent).toContain("Russia");
    expect(getHistory).toHaveBeenCalledWith({ days: 7 }, expect.anything());
  });

  it("counts the nodes by their verdict now, in the colours of Health", async () => {
    await mount();
    expect(tally()).toEqual({ ok: "3reachable", partial: "0blocked on some networks", warn: "1TCP blocked", bad: "0unreachable", listed: "1listed", unknown: "0no data" });
    const warn = document.querySelector("[data-kind=warn]")!;
    expect(warn.className).toContain("tone-warn");
    expect(document.querySelector("[data-kind=bad]")!.className).not.toContain("tone-bad"); // nobody there: quiet
    expect(document.querySelector("[data-kind=listed]")!.className).toContain("tint-dashed");
  });

  it("lists the nodes by name, without the one that waits for its install, each with its verdict and the Health tooltip", async () => {
    await mount();
    expect(rows().map((r) => r.querySelector("a span:last-child")?.textContent)).toEqual(["de1", "fi1", "nl1", "probe1", "se1"]);
    expect(row("de1").querySelector("a")?.getAttribute("href")).toBe("/nodes/$id");
    const pill = row("fi1").querySelector<HTMLElement>("[role=img][tabindex='0']")!;
    expect(pill.getAttribute("aria-label")).toBe("fi1, From Russia: TCP is blocked · The sources disagree · Globalping: reachable · probe: TCP blocked");
    expect(words(row("fi1"))).toContain("sources disagree");
    expect(row("nl1").querySelector("[role=img]")?.getAttribute("aria-label")).toBe("nl1, From Russia: Reachable from Russia · in the RKN registry");
    await act(async () => pill.focus());
    for (let i = 0; i < 3; i++) await settle();
    expect(text()).toContain("The sources disagreeGlobalping: reachable · probe: TCP blocked");
  });

  it("gives each source its word for the node now: the lists say listed or not, the probe node is named, a source with nothing yet has a dash", async () => {
    await mount();
    const now = (name: string) => ["globalping", "lists", "tspu"].map((s) => words(line(name, s).children[1]));
    expect(now("de1")).toEqual(["reachable", "not listed", "reachable"]);
    expect(now("fi1")).toEqual(["reachable", "not listed", "TCP blocked"]);
    expect(now("nl1")).toEqual(["reachable", "listed", "reachable"]);
    expect(now("probe1")).toEqual(["reachable", "not listed", "probe node"]);
    expect(now("se1")).toEqual(["no data", "listed", "—"]);
    expect(line("fi1", "tspu").children[1]?.className).toContain("tone-warn");
    for (const r of rows()) for (const s of ["globalping", "lists", "tspu"]) expect(r.querySelector(`[data-testid=line-${s}]`)?.getAttribute("data-on")).toBe("true");
  });

  it("lists the country_blocked alerts of the week, open first, each a link to its alert", async () => {
    await mount();
    const events = [...document.querySelectorAll("[data-testid=reach-event]")];
    expect(events.map((e) => e.getAttribute("href"))).toEqual(["/health#alert-alt_fi1", `/health#alert-alt_nl1-${CUT + 16 * H + 1800}`]);
    expect(events[0]?.textContent).toContain("fi1TCP blocked · opened 1 d ago");
    expect(events[1]?.textContent).toContain("nl1TCP blocked · closed");
    expect(events[1]?.textContent).toContain("lasted 16 h");
  });

  it("ends with the six source cards for the owner", async () => {
    await mount();
    expect(document.querySelector("#sources h2")?.textContent).toBe("Sources");
    expect([...document.querySelectorAll("#sources h3")].map((h) => h.textContent)).toEqual(["Globalping", "Block lists", "TSPU probe", "cheburcheck", "bsbord", "kl!ck users"]);
  });
});

describe("the lanes", () => {
  it("draw the compacted week of a source as segments, each saying its verdict and time", async () => {
    await mount();
    const gp = segs("nl1", "globalping");
    expect(gp).toHaveLength(3);
    expect(gp.map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable", "TCP blocked", "reachable"]);
    expect(gp[2]?.getAttribute("aria-label")).toMatch(/ – now$/);
    expect(line("nl1", "globalping").querySelectorAll("[data-verdict]")).toHaveLength(3);
    expect([...line("nl1", "lists").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["listed"]);
    // the lists' "ok" is their own word
    expect(segs("de1", "lists")[0]?.getAttribute("aria-label")).toMatch(/^not listed, /);
  });

  it("leave a source with nothing on the node empty: no segment, no tab stop", async () => {
    await mount();
    expect(segs("probe1", "tspu")).toHaveLength(0);
    expect(segs("se1", "tspu")).toHaveLength(0);
    expect(line("probe1", "tspu").querySelector("[data-testid=lane-tspu]")?.getAttribute("aria-label")).toBe("Probe, last 7 days");
  });

  it("are one tab stop on the newest segment; the arrows walk the lane and the tooltip follows the focus", async () => {
    await mount();
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

  it("counts it apart from TCP blocked: the warn colour with a half disc, not the “!”", async () => {
    await mount();
    expect(tally()).toEqual({ ok: "2reachable", partial: "1blocked on some networks", warn: "1TCP blocked", bad: "0unreachable", listed: "1listed", unknown: "0no data" });
    const partial = document.querySelector("[data-kind=partial]")!;
    expect(partial.className).toContain("tone-warn");
    expect(partial.querySelector("svg circle")).not.toBeNull();
    expect(partial.textContent).not.toContain("!");
    expect(document.querySelector("[data-kind=warn]")!.textContent).toContain("!");
  });

  it("names the networks in the pill, its tooltip on keyboard focus, and the source's word now", async () => {
    await mount();
    const pill = row("de1").querySelector<HTMLElement>("[role=img][tabindex='0']")!;
    expect(pill.getAttribute("aria-label")).toBe("de1, From Russia: Reachable from Russia, but not from every network · Blocked on some networks: MTS (AS8359, Novosibirsk)");
    expect(pill.className).toContain("tone-warn");
    expect(words(pill)).toBe("some networks");
    expect(words(pill.parentElement)).toBe("some networksMTS"); // the dark networks under the pill
    expect(words(line("de1", "globalping").children[1])).toBe("some networks");
    expect(line("de1", "globalping").children[1]?.className).toContain("tone-warn");
    await act(async () => pill.focus());
    for (let i = 0; i < 3; i++) await settle();
    for (const l of ["Blocked on some networks: MTS (AS8359, Novosibirsk)", "MTS, Novosibirsk (reaches your other nodes, not this one)", "Beeline, Moscow", "No home/mobile probe this round: MegaFon, T2"])
      expect(text()).toContain(l);
  });

  it("splits its lane segment, which says the verdict in full, and names it in the recent events", async () => {
    await mount();
    const gpLane = segs("de1", "globalping");
    expect(gpLane.map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable", "blocked on some networks"]);
    expect([...line("de1", "globalping").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["ok", "partial"]);
    expect(line("de1", "globalping").querySelector("[data-verdict=partial]")?.className).toContain("linear-gradient");
    const events = [...document.querySelectorAll("[data-testid=reach-event]")];
    expect(events.map((e) => e.textContent)).toEqual([expect.stringContaining("de1blocked on some networks · opened 3 h ago")]);
  });

  it("offers its breakdown behind a disclosure across the row, closed until asked, on that node only", async () => {
    await mount();
    expect(document.querySelectorAll("[data-testid=reach-breakdown]")).toHaveLength(1);
    const more = row("de1").querySelector<HTMLDetailsElement>("details[data-testid=reach-breakdown]")!;
    expect(more.className).toContain("xl:col-span-3");
    expect(more.open).toBe(false);
    expect(more.querySelector("summary")?.textContent).toBe("Networks in detail");
    act(() => more.querySelector("summary")!.click());
    expect(more.open).toBe(true);
    const lists = [...more.querySelectorAll("ul")];
    expect(lists.map((ul) => document.getElementById(ul.getAttribute("aria-labelledby")!)?.textContent)).toEqual(["Cannot reach the node", "Reach it"]);
    expect([...more.querySelectorAll("li")].map((li) => li.textContent)).toEqual(["✕MTS AS8359 · Novosibirsk · Globalping", "✓Beeline AS3216 · Moscow · Globalping"]);
    expect(more.textContent).toContain("Not checked this round: MegaFon, T2");
    // the box takes the verdict's tone
    expect(more.querySelector("[data-testid=partial-breakdown]")?.className).toContain("tone-warn");
    // fi1 is TCP blocked with nothing dark named: no breakdown
    expect(row("fi1").querySelector("[data-testid=reach-breakdown]")).toBeNull();
  });

  it("speaks Russian", async () => {
    act(() => setLang("ru"));
    await mount();
    expect(tally()).toMatchObject({ partial: "1недоступны из части сетей", warn: "1TCP режут" });
    expect(row("de1").querySelector("summary")?.textContent).toBe("Подробнее по сетям");
    expect(words(row("de1").querySelector("[role=img][tabindex='0']"))).toBe("часть сетей");
    expect(document.querySelector("[data-testid=reach-event]")?.textContent).toContain("de1недоступна из части сетей");
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

  it("give each node a line for cheburcheck and bsbord while they are on, with the word now and the week; the own probe has none", async () => {
    await mount();
    expect(["globalping", "lists", "tspu", "cheburcheck", "bsbord"].map((s) => line("de1", s).firstElementChild?.textContent)).toEqual(["Globalping", "Lists", "Probe", "cheburcheck", "bsbord"]);
    expect(row("de1").querySelector("[data-testid=line-cheburcheck_own]")).toBeNull();
    expect(words(line("de1", "bsbord").children[1])).toBe("some networks");
    expect([...line("de1", "bsbord").querySelectorAll("[data-verdict]")].map((s) => s.getAttribute("data-verdict"))).toEqual(["ok", "partial"]);
    expect(segs("de1", "cheburcheck").map((s) => s.getAttribute("aria-label")?.split(",")[0])).toEqual(["reachable"]);
    expect(line("de1", "bsbord").querySelector("[role=group]")?.getAttribute("aria-label")).toBe("bsbord, last 7 days");
    expect(words(line("nl1", "bsbord").children[1])).toBe("unreachable");
    // a node bsbord has nothing on yet: a dash, no segment
    expect(line("fi1", "bsbord").children[1]?.textContent).toBe("—");
    expect(segs("fi1", "bsbord")).toHaveLength(0);
  });

  it("break a verdict down across every source, the white-list unit apart", async () => {
    await mount();
    const more = row("de1").querySelector<HTMLDetailsElement>("details[data-testid=reach-breakdown]")!;
    act(() => more.querySelector("summary")!.click());
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
    expect(lines.map((l) => l.textContent)).toEqual(["✕MTS (КРЫМ) reaches none of your nodes · bsbord"]);
    const box = lines[0]!.closest("section")!;
    expect(box.getAttribute("aria-label")).toBe("Networks that reach none of your nodes");
    expect(box.textContent).toContain("No alert covers this: the network cuts every node alike.");
    // it sits under the counts, before the nodes
    expect(box.previousElementSibling?.getAttribute("aria-label")).toBe("Nodes by verdict");
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("ru"));
    await mount();
    expect(document.querySelector("[data-testid=dark-everywhere]")?.textContent).toBe("✕MTS (КРЫМ) не достаёт ни до одной твоей ноды · bsbord");
    expect(line("de1", "bsbord").firstElementChild?.textContent).toBe("bsbord");
  });

  it("say nothing of the kind while every network reaches some node", async () => {
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks });
    await mount();
    expect(document.querySelector("[data-testid=dark-everywhere]")).toBeNull();
  });
});

describe("the Reachability page with the sources off", () => {
  it("says every source is off and leads the owner to them; no counts, no nodes, the alerts of the week stay", async () => {
    getHistory.mockResolvedValue(history([]));
    getChecks.mockResolvedValue({ nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: [] });
    await mount();
    const off = document.querySelector("[data-testid=all-off]")!;
    expect(off.textContent).toContain("Every source is off");
    expect(off.textContent).toContain("Switch one on below: Globalping, the block lists, the TSPU probe, cheburcheck or bsbord.");
    expect([...off.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["To the sources"]);
    expect(document.querySelector("[data-kind]")).toBeNull();
    expect(rows()).toHaveLength(0);
    expect(document.querySelectorAll("[data-testid=reach-event]")).toHaveLength(2);
    expect(document.querySelectorAll("#sources h3")).toHaveLength(6);
  });

  it("dims a source that is off and leaves its lanes empty, while the others draw", async () => {
    getHistory.mockResolvedValue(history(["globalping", "tspu"]));
    await mount();
    for (const name of ["de1", "nl1", "se1"]) {
      const l = line(name, "lists");
      expect(l.getAttribute("data-on")).toBe("false");
      expect(l.className).toContain("opacity-55");
      expect(l.children[1]?.textContent).toBe("off");
      expect(l.querySelector("[data-verdict]")).toBeNull();
      expect(l.querySelector("[aria-hidden].border-dashed")).not.toBeNull();
    }
    expect(segs("nl1", "globalping")).toHaveLength(3);
    expect(segs("nl1", "tspu")).toHaveLength(3);
  });

  it("tells anyone but the owner who switches them on, without the cards or the picker", async () => {
    role = Role.READONLY;
    getHistory.mockResolvedValue(history([]));
    await mount();
    expect(document.querySelector("[data-testid=all-off]")?.textContent).toContain("Nothing is checked until the owner switches a source on.");
    expect(document.querySelector("[data-testid=all-off] button")).toBeNull();
    expect(document.querySelector("[aria-label=Country]")).toBeNull();
    expect(document.querySelectorAll("#sources h3")).toHaveLength(0);
    expect(text()).toContain("Only the owner sees and changes the sources.");
    // the country of the subtitle comes from the history, which every role reads
    expect(text()).toContain("Whether the nodes open from Russia");
  });
});

describe("the Reachability page on a phone (375 px)", () => {
  it("turns each row into a card: the verdict next to the name on top, then the source lines with their lanes stacked under them", async () => {
    await mount();
    // six kinds: two to a row on a phone, three on a tablet, all six on the wide screen; none spans a row
    const sum = document.querySelector("ul[aria-label='Nodes by verdict']")!;
    expect(sum.className).toContain("grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6");
    expect([...sum.children].map((li) => li.getAttribute("data-kind"))).toEqual(["ok", "partial", "warn", "bad", "listed", "unknown"]);
    for (const li of sum.children) expect(li.className).not.toContain("col-span");
    const card = row("nl1");
    expect(card.className).toMatch(/(^| )flex flex-col/);
    expect(card.className).toContain("xl:grid");
    expect(card.firstElementChild?.className).toContain("xl:contents"); // name and pill share the top line
    expect(card.firstElementChild?.querySelector("[role=img]")).not.toBeNull();
    // each lane takes the whole width under its name and word below md
    for (const s of ["globalping", "lists", "tspu"]) expect(line("nl1", s).querySelector(`[data-testid=lane-${s}]`)?.parentElement?.className).toContain("col-span-2 md:col-span-1");
    const axis = [...card.querySelectorAll("[aria-hidden].xl\\:hidden")].map((a) => a.textContent);
    expect(axis).toEqual(["7 days agonow"]);
    // the day marks over the lanes are for the wide screen only
    expect(document.querySelector("[aria-hidden].hidden.xl\\:grid")).not.toBeNull();
  });
});

describe("the Reachability page in Russian and Chinese", () => {
  it("speaks both, the country in the genitive in Russian", async () => {
    act(() => setLang("ru"));
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("Доступность");
    expect(text()).toContain("Открываются ли ноды из России");
    expect(tally()).toMatchObject({ ok: "3доступны", warn: "1TCP режут", listed: "1в реестре" });
    expect(["globalping", "lists", "tspu"].map((s) => line("probe1", s).firstElementChild?.textContent)).toEqual(["Globalping", "Списки", "Зонд"]);
    expect(line("probe1", "tspu").children[1]?.textContent).toBe("нода-зонд");
    act(() => root?.unmount());
    host?.remove();
    act(() => setLang("zh"));
    await mount();
    expect(document.querySelector("h1")?.textContent).toBe("可达性");
    expect(text()).toContain("能否从俄罗斯访问节点");
    expect(line("de1", "lists").children[1]?.textContent).toBe("不在名单中");
  });
});
