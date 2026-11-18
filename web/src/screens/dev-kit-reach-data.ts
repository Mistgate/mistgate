import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { sourceIds, type ReachTab, type SourceId } from "@/screens/reachability/search";
import type { State } from "@/screens/reachability/nodes";

// Development only: the made-up fleet and week of the Reachability scenes (/dev-kit?only=reach-page). The documentation networks and
// addresses, the nodes de1, fi1, msk1 and nl1, the operators A, B and C, the cities of the board: nothing here is anyone's. The scene's own
// "panel" (dev-kit-reach.tsx) answers the page's calls from a Server made here, so a click that switches a source changes what the page says.

export const NOW = Math.floor(Date.now() / 1000);
export const H = 3600;
export const DAY = 86400;
export const SINCE = NOW - 7 * DAY;
export const nid = (n: string) => `nod_${n}`;

/** The board's short ids of the sources, and the full ones. */
const alias: Record<string, SourceId> = { gp: "globalping", bl: "lists", ts: "tspu", cc: "cheburcheck", bs: "bsbord", kl: "klick", pr: "probes" };

export type Flags = {
  tab: ReachTab;
  /** every word of the state that is not a tab or a pair */
  v: Set<string>;
  src?: SourceId;
  open?: string;
  filter?: State;
};

const states: readonly string[] = ["ok", "part", "tcp", "bad", "reg", "nd"];

/** `<tab>[.variant…]`: `src.<id>`, `open.<node>` and `filter.<state>` take the word after them. */
export function flagsOf(state: string): Flags {
  const [first = "nodes", ...rest] = state.split(".");
  const tab: ReachTab = first === "events" || first === "sources" ? first : "nodes";
  const f: Flags = { tab, v: new Set() };
  for (let i = 0; i < rest.length; i++) {
    const w = rest[i]!;
    const next = rest[i + 1] ?? "";
    if (w === "src" && (alias[next] || (sourceIds as readonly string[]).includes(next))) {
      f.src = alias[next] ?? (next as SourceId);
      i++;
    } else if (w === "open" && next) {
      f.open = next;
      i++;
    } else if (w === "filter" && states.includes(next)) {
      f.filter = next as State;
      i++;
    } else f.v.add(w);
  }
  return f;
}

// ---- the panel's answers -------------------------------------------------------------------------------------------------------------

export type Srv = {
  country: string;
  globalping: { enabled: boolean; intervalMin: number; hasToken: boolean; ipv6: boolean };
  lists: { enabled: boolean };
  tspu: { enabled: boolean; probeNodeId: string; intervalMin: number };
  cheburcheck: { enabled: boolean; publicEnabled: boolean; hasToken: boolean; intervalMin: number; ownIntervalMin: number; status: string; holdUntilUnix: number; lastRunUnix: number };
  bsbord: { enabled: boolean; hasKey: boolean; intervalMin: number; capCredits: number; units: string[]; spentCredits: number; lastStatus: string; lastRunUnix: number };
  klick: { enabled: boolean };
  probes: { enabled: boolean; intervalMin: number; fallbackUrl: string; list: object[] };
};

/** The state of a made-up panel by the scene's words: everything works, unless a word says otherwise. */
export function serverOf(f: Flags): Srv {
  const v = f.v;
  const off = v.has("nosrc");
  const kz = v.has("kz");
  return {
    country: kz ? "KZ" : "RU",
    globalping: { enabled: !off, intervalMin: 10, hasToken: !off || v.has("gperr"), ipv6: v.has("v6") },
    lists: { enabled: !off },
    tspu: { enabled: !off, probeNodeId: off ? "" : nid("msk1"), intervalMin: 120 },
    cheburcheck: { enabled: !off, publicEnabled: true, hasToken: false, intervalMin: 180, ownIntervalMin: 60, status: "", holdUntilUnix: 0, lastRunUnix: NOW - 3600, },
    bsbord: {
      enabled: !off && !kz,
      hasKey: !off,
      intervalMin: 1440,
      capCredits: 10000,
      units: off ? [] : ["kzn|op-c|off", "msk|op-a|off", "spb|op-b|off"],
      spentCredits: v.has("bsbud") ? 10000 : 2340,
      lastStatus: v.has("bsbud") ? "cap_reached" : "",
      lastRunUnix: NOW - (v.has("bsbud") ? 2 * DAY : 7 * H),
    },
    klick: { enabled: !off },
    probes: { enabled: v.has("probes") || v.has("dlgadd") || v.has("dlgupd") || v.has("dlgrev"), intervalMin: 60, fallbackUrl: "", list: v.has("probes") || v.has("dlgadd") || v.has("dlgupd") || v.has("dlgrev") ? probeList() : [] },
  };
}

const probe = (over: object) => ({
  id: "prb_0123456789abcdef01234567", name: "dacha", city: "Example City", kind: "home", state: "active", asn: 64496, operator: "Operator A", pinnedOperator: "Operator A", version: "0a1b2c3d4e5f",
  lastSeenUnix: NOW - 4 * 60, lastResultUnix: NOW - 12 * 60, checksDay: 24, tokenExpiresUnix: 0, reference: "filtered", ...over,
});
export const probeList = () => [
  probe({}),
  probe({ id: "prb_89abcdef0123456789abcdef", name: "office", city: "Sample Town", kind: "dc", asn: 64497, operator: "Operator C", pinnedOperator: "Operator C", version: "9f8e7d6c5b4a", lastSeenUnix: NOW - 9 * 60, lastResultUnix: NOW - 21 * 60 }),
  probe({ id: "prb_aaaaaaaaaaaaaaaaaaaaaaaa", name: "garage", city: "Example City", operator: "", pinnedOperator: "", asn: 0, version: "", state: "pending", lastSeenUnix: 0, lastResultUnix: 0, checksDay: 0, reference: "", tokenExpiresUnix: NOW + 22 * H }),
];

/** The units bsbord offers: an operator in a district, with or without the white list. */
export const units = [
  { opKey: "kzn|op-c|off", operator: "Operator C", name: "Operator C", region: "Kazan", regionCode: "kzn", dpi: "off", probeable: true, selected: true },
  { opKey: "msk|op-a|off", operator: "Operator A", name: "Operator A", region: "Moscow", regionCode: "msk", dpi: "off", probeable: true, selected: true },
  { opKey: "msk|op-a|on", operator: "Operator A", name: "Operator A, white list", region: "Moscow", regionCode: "msk", dpi: "on", probeable: true, selected: false },
  { opKey: "spb|op-b|off", operator: "Operator B", name: "Operator B", region: "Saint Petersburg", regionCode: "spb", dpi: "off", probeable: true, selected: true },
  { opKey: "ekb|op-e|off", operator: "Operator E", name: "Operator E", region: "Yekaterinburg", regionCode: "ekb", dpi: "off", probeable: true, selected: false },
  { opKey: "nsk|op-d|off", operator: "Operator D", name: "Operator D", region: "Novosibirsk", regionCode: "nsk", dpi: "off", probeable: false, selected: false },
];

// ---- the fleet and the week ----------------------------------------------------------------------------------------------------------

const fleet = [
  { n: "de1", cc: "DE", city: "Frankfurt", ip: "203.0.113.7" },
  { n: "fi1", cc: "FI", city: "Helsinki", ip: "203.0.113.15" },
  { n: "msk1", cc: "RU", city: "Moscow", ip: "203.0.113.20" },
  { n: "nl1", cc: "NL", city: "Amsterdam", ip: "203.0.113.41" },
];

/** What the fi1 row of the board does: "some networks" by default, or the word of the scene. */
export function fiState(f: Flags): State {
  const v = f.v;
  return v.has("allok") ? "ok" : v.has("bad") ? "bad" : v.has("reg") ? "reg" : v.has("tcp") ? "tcp" : "part";
}

const ipv6Entry = { entry: "", entrySupported: true, unavailableReason: "", addresses: [{ address: "2001:db8::5", prefixLen: 64 }], src: "2001:db8::5" };

export function nodesOf(f: Flags, srv: Srv) {
  if (f.v.has("nonodes")) return { nodes: [], expectedAgentVersion: "0.1.71" };
  return {
    nodes: fleet.map((n) => ({
      id: nid(n.n), name: n.n, countryCode: n.cc, location: n.city, provider: "Example Cloud", notes: "", address: n.ip, status: NodeStatus.ONLINE, protocols: [], online: [], trafficTodayBytes: 0, hasMetrics: false, cpuPct: 0,
      uptimeS: 0, lastSeenUnix: NOW, ipv6: f.v.has("v6") && n.n === "fi1" ? ipv6Entry : undefined,
    })),
    expectedAgentVersion: "0.1.71",
    srv,
  };
}

const pts = (...p: [number, CountryVerdict][]) => p.map(([atUnix, verdict]) => ({ atUnix, verdict }));
const lane = (source: string, points: ReturnType<typeof pts>) => ({ source, points, untilUnix: NOW });
const OK = CountryVerdict.OK;
const since20 = NOW - 20 * H;

const verdictOf: Record<State, CountryVerdict> = { ok: OK, part: CountryVerdict.PARTIAL, tcp: CountryVerdict.TCP_BLOCKED, bad: CountryVerdict.BLOCKED, reg: CountryVerdict.LISTED, nd: CountryVerdict.UNKNOWN };

export function historyOf(f: Flags, srv: Srv, family: 4 | 6 = 4) {
  const on = [
    srv.globalping.enabled && "globalping",
    srv.lists.enabled && srv.country === "RU" && "lists",
    srv.tspu.enabled && srv.country === "RU" && "tspu",
    srv.cheburcheck.enabled && srv.country === "RU" && "cheburcheck",
    srv.bsbord.enabled && srv.country === "RU" && "bsbord",
    srv.probes.enabled && "probes",
  ].filter(Boolean) as string[];
  const quiet = f.v.has("first") || f.v.has("nosrc") || f.v.has("nonodes");
  const fi = fiState(f);
  const start = SINCE + 600;
  if (family === 6) return { country: srv.country, sources: ["globalping"], sinceUnix: SINCE, nowUnix: NOW, family: 6, nodes: srv.globalping.ipv6 && !quiet ? [{ nodeId: nid("fi1"), lanes: [lane("globalping", pts([NOW - 50 * H, OK]))] }] : [] };
  const lanesOf = (n: string) => {
    const bad = n === "fi1" && fi !== "ok";
    const out = [];
    for (const s of on) {
      if (n === "msk1" && s === "tspu") continue; // the probe node does not probe itself
      let points = pts([start, OK]);
      if (bad && s === "globalping" && (fi === "part" || fi === "bad")) points = pts([start, OK], [since20, verdictOf[fi]]);
      if (bad && s === "lists" && fi === "reg") points = pts([start, OK], [since20, CountryVerdict.LISTED]);
      if (bad && s === "tspu" && (fi === "tcp" || fi === "bad")) points = pts([start, OK], [since20, fi === "bad" ? CountryVerdict.BLOCKED : CountryVerdict.TCP_BLOCKED]);
      if (s === "cheburcheck" || s === "bsbord" || s === "probes") points = pts([NOW - 3 * DAY, OK]);
      out.push(lane(s, points));
    }
    return out;
  };
  return { country: srv.country, sources: on, sinceUnix: SINCE, nowUnix: NOW, nodes: quiet ? [] : fleet.map((n) => ({ nodeId: nid(n.n), lanes: lanesOf(n.n) })) };
}

const vantage = (over: object) => ({ source: "globalping", key: "gp:x", network: "", region: "", asn: "", kind: "home", state: "reach", atUnix: NOW - 9 * 60, dpiHop: 0, icmpOk: true, tcpOk: true, why: "", probeName: "", port: 0, ...over });

const nets = [
  { source: "globalping", key: "gp:a", network: "Operator A", region: "Moscow", asn: "64496", kind: "mobile" },
  { source: "globalping", key: "gp:b", network: "Operator B", region: "Novosibirsk", asn: "64497", kind: "home" },
  { source: "bsbord", key: "bs:c", network: "Operator C", region: "Kazan", asn: "64498", kind: "mobile" },
  { source: "globalping", key: "gp:d", network: "Operator D", region: "Yekaterinburg", asn: "64499", kind: "home" },
];
/** `many`: twelve networks; `long`: the names of live data (an operator with its legal name, a city with its district, an own probe with a story) */
const netsOf = (f: Flags) => {
  if (!f.v.has("many") && !f.v.has("long")) return nets;
  const names = f.v.has("long")
    ? ["Example Telecom of the Central Federal District", "Operator Mobile Communications Limited", "Operator C", "Operator D", "Sample Cable and Broadband Services of the Volga Region", "Operator F"]
    : ["Operator A", "Operator B", "Operator C", "Operator D", "Operator E", "Operator F", "Operator G", "Operator H", "Operator I", "Operator J", "Operator K", "Operator L"];
  const cities = ["Moscow", "Novosibirsk", "Kazan", "Yekaterinburg", "Saint Petersburg", "Samara", "Perm", "Omsk", "Ufa", "Chelyabinsk", "Rostov-on-Don", "Krasnodar"];
  const more = names.map((network, i) => ({ source: i % 3 === 2 ? "bsbord" : "globalping", key: `x:${i}`, network, region: f.v.has("long") ? `${cities[i]} Federal District, Example Region` : cities[i]!, asn: String(64500 + i), kind: i % 2 ? "home" : "mobile" }));
  return f.v.has("long") ? [...more, { source: "probes", key: "probe:long", network: "Example Telecom of the Central Federal District", region: "", asn: "", kind: "home", probeName: "dacha-in-the-village-with-a-long-name", why: "", state: "reach" }] : more;
};

function checkOf(n: string, f: Flags, srv: Srv) {
  const on = historyOf(f, srv).sources;
  const fi = n === "fi1" ? fiState(f) : "ok";
  const verdict = verdictOf[fi];
  const live = fi !== "reg";
  const sources = on.filter((s) => s === "globalping" || s === "lists" || (s === "tspu" && n !== "msk1"));
  // the networks that saw the node this round: the board's A to D (B and D do not see fi1 on "some networks", none sees it when it is unreachable)
  const all = netsOf(f);
  const dark = fi === "bad" ? all.map((x) => x.network) : fi === "part" ? all.filter((_, i) => i % 5 === 1 || i % 5 === 3).map((x) => x.network) : [];
  const vantages = all
    .filter((x) => on.includes(x.source) || x.source === "probes")
    .map((x) => vantage({ ...x, ...(dark.includes(x.network) ? { state: "dark", icmpOk: false, tcpOk: false } : {}) }));
  return {
    nodeId: nid(n), country: srv.country, verdict, atUnix: NOW - 9 * 60,
    probes: live && sources.includes("globalping") ? [{ network: "Operator A", city: "Moscow", valid: true, dark: false, icmpOk: true, icmpMs: 38, tcp: [{ port: 443, ok: true }] }] : [],
    reason: live ? "" : "too_few_probes", eyeball: true, confidence: "eyeball", listedBy: fi === "reg" ? ["antifilter_ipsum"] : [], cdnProvider: "", cdnRegion: "",
    sources, globalpingVerdict: live ? (fi === "tcp" || fi === "bad" ? OK : verdict) : CountryVerdict.UNKNOWN,
    tspuVerdict: sources.includes("tspu") ? (fi === "bad" ? CountryVerdict.BLOCKED : fi === "tcp" ? CountryVerdict.TCP_BLOCKED : OK) : CountryVerdict.UNSPECIFIED,
    tspuPorts: sources.includes("tspu") ? [{ port: 443, verdict: fi === "tcp" || fi === "bad" ? "blocked" : "open" }] : [], dpiHop: sources.includes("tspu") ? 6 : 0,
    tspuVantageId: nid("msk1"), tspuVantageName: "msk1", tspuAtUnix: sources.includes("tspu") ? NOW - 48 * 60 : 0, noProbe: ["Operator E"], vantages, sourceVerdicts: [],
    ...(f.v.has("v6") ? { ipv6: v6Check(n) } : {}),
  };
}

const v6Check = (n: string) => ({
  nodeId: nid(n), country: "RU", verdict: n === "fi1" ? OK : CountryVerdict.UNKNOWN, atUnix: NOW - 9 * 60, probes: [], reason: n === "fi1" ? "" : n === "de1" ? "no_ipv6" : "ipv6_broken", eyeball: true, confidence: "eyeball",
  listedBy: [], cdnProvider: "", cdnRegion: "", sources: n === "fi1" ? ["globalping"] : [], globalpingVerdict: OK, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [], vantages: [], sourceVerdicts: [],
});

export function checksOf(f: Flags, srv: Srv) {
  const quiet = f.v.has("first") || f.v.has("nosrc") || f.v.has("nonodes") || f.v.has("load");
  const rows = quiet ? [] : fleet.map((n) => checkOf(n.n, f, srv));
  // a source that says nothing for a reason: the hourly limit used up, the TSPU probe silent
  if (f.v.has("gplim")) for (const r of rows) Object.assign(r, { reason: "rate_limited", probes: r.probes.slice(0, 1) });
  if (f.v.has("dark")) {
    for (const r of rows) r.vantages = [...r.vantages.filter((x) => x.network !== "Operator B"), vantage({ key: "gp:crimea", network: "MTS (Crimea)", region: "Simferopol", asn: "64499", state: "dark", icmpOk: false, tcpOk: false, kind: "mobile" })];
    for (const r of rows) if (r.nodeId !== nid("fi1")) r.verdict = CountryVerdict.PARTIAL;
  }
  return { nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: rows, tspuRun: srv.tspu.enabled && !quiet ? { status: "ok", atUnix: NOW - 48 * 60, nodeId: nid("msk1") } : undefined };
}

const alert = (id: string, node: string, verdict: string, openedAgo: number, closedAgo = 0, over: object = {}) => ({
  id, severity: verdict === "blocked" ? AlertSeverity.CRITICAL : AlertSeverity.WARNING, kind: AlertKind.COUNTRY_BLOCKED, nodeId: nid(node), nodeName: node, subject: "", titleKey: "", whyKey: "",
  params: { verdict, country: "RU" }, firstSeenUnix: NOW - openedAgo, openedUnix: NOW - openedAgo, lastSeenUnix: NOW, resolvedAtUnix: closedAgo ? NOW - closedAgo : 0, resolution: closedAgo ? "cleared" : "", actions: [], mutedUntilUnix: 0, ...over,
});

export function alertsOf(f: Flags, srv: Srv) {
  const none = f.v.has("first") || f.v.has("nosrc") || f.v.has("nonodes") || !srv.globalping.enabled;
  const fi = fiState(f);
  const word = { part: "partial", bad: "blocked", tcp: "tcp_blocked", reg: "tcp_blocked", ok: "" }[fi as "part" | "bad" | "tcp" | "reg" | "ok"];
  const open = fi === "ok" || none ? [] : [alert("alt_fi1", "fi1", word, 20 * H, 0, f.v.has("v6") ? { params: { verdict: word, country: "RU", ipv6: "open" } } : {})];
  const closed = none || f.v.has("allok") ? [] : [alert("alt_nl1", "nl1", "tcp_blocked", 3 * DAY + 4 * H, 3 * DAY - 14 * H)];
  return { nowUnix: NOW, active: open, history: closed };
}

export function clientProbeOf(f: Flags, srv: Srv) {
  const reports = srv.klick.enabled ? 142 : 0;
  const tally = (n: number, over: object = {}) => ({ reports: n, ok: n, freeze: 0, tcpBlocked: 0, handshakeFailed: 0, noAnswer: 0, lastUnix: NOW - 7 * 60, ...over });
  const fi = fiState(f);
  const trouble = fi === "ok" ? {} : { freeze: 1, ok: 31, tcpBlocked: fi === "tcp" ? 3 : 0, noAnswer: 2 };
  const group = (name: string, n: number, over: object = {}) => ({ name, tally: tally(n, over) });
  const nodes = srv.klick.enabled
    ? [
        { nodeId: nid("de1"), tally: tally(38, { freeze: 1, ok: 37 }), operators: [group("Operator A", 14), group("Operator B", 11), group("Operator C", 8)], regions: [group("Moscow", 19), group("Saint Petersburg", 9)] },
        ...(f.v.has("noreports") ? [] : [{ nodeId: nid("fi1"), tally: tally(32, trouble), operators: [group("Operator A", 14), group("Operator B", 11, { ok: 6, tcpBlocked: 4, noAnswer: 1 }), group("Operator C", 8)], regions: [group("Moscow", 19), group("Saint Petersburg", 9)] }]),
        { nodeId: nid("msk1"), tally: tally(20, { freeze: 1, ok: 19 }), operators: [group("Operator A", 14)], regions: [group("Moscow", 19)] },
        { nodeId: nid("nl1"), tally: tally(26, { freeze: 1, ok: 25 }), operators: [group("Operator A", 14), group("Operator C", 8)], regions: [group("Moscow", 19)] },
      ]
    : [];
  return { settings: { enabled: srv.klick.enabled, reports, lastUnix: srv.klick.enabled ? NOW - 7 * 60 : 0 }, nodes, nowUnix: NOW };
}

export const tspuNodes = (f: Flags) => [
  { id: nid("msk1"), name: "msk1", online: !f.v.has("tsoff"), declares: true, countryCode: "RU" },
  { id: nid("de1"), name: "de1", online: true, declares: true, countryCode: "DE" },
];
