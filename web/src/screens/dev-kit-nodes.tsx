import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { Shot } from "@/screens/dev-kit-redesign";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang, useT } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, doctorQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodeQuery, nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { writePref } from "@/lib/storage";
import { AddNodeProvider } from "@/components/add-node";
import { fleetLoadQuery } from "./nodes-load";
import { NodesScreen, type NodesDemo } from "./nodes";
import { BulkBar } from "./nodes-bulk";
import { panelFor, type ActionId } from "./nodes-bulk-actions";
import { ShellLayout } from "./shell";
import type { Job, Row } from "@/lib/batch";
import type { Avail } from "@/components/bulk";

// Development only. /dev-kit?only=nodes: the Nodes screen of the redesign in every state, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for
// the side-by-side with the board:
//   /dev-kit?only=nodes-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper][&fx=1]
// states: list (the board's four nodes) | first (no node) | one | twelve | dead (nl1 does not answer) | mixed (every state a node
// has) | offer (the panel-key offer) | select (selecting, two ticked: the bulk bar) | find (a search that finds one) | nomatch
// (a search that finds none) | load | map | map-card (the map with nl1 selected) | job-running | job-done | job-failed (the bar's
// batch states) | edge (the Cloudflare edition: no load, no map on a phone is the width's doing).
// Made-up data of the board: de1, fi1, nl1, msk1 and the eight more of its twelve, documentation addresses, "Example Cloud".

const now = Math.floor(Date.now() / 1000);
const id = (n: string) => `nod_${n}`;

// name, country, city, online, protocols, today (GB), cpu %, uptime days, address, extra
type Spec = { n: string; cc: string; city: string; on: number; pr: string[]; gb: number; cpu: number; up: number; ip: string; over?: Record<string, unknown> };
const rnd = (k: number) => {
  const x = Math.sin(k * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
};
const all: Spec[] = [
  { n: "de1", cc: "DE", city: "Frankfurt", on: 3, pr: ["hysteria2", "vless", "amneziawg"], gb: 28.9, cpu: 9, up: 31, ip: "203.0.113.7" },
  { n: "fi1", cc: "FI", city: "Helsinki", on: 1, pr: ["hysteria2", "vless", "amneziawg"], gb: 9.5, cpu: 5, up: 31, ip: "203.0.113.15" },
  { n: "nl1", cc: "NL", city: "Amsterdam", on: 1, pr: ["hysteria2", "vless"], gb: 17.1, cpu: 7, up: 31, ip: "203.0.113.41" },
  { n: "msk1", cc: "RU", city: "Moscow", on: 1, pr: ["vless"], gb: 7.7, cpu: 4, up: 31, ip: "203.0.113.20" },
  { n: "de2", cc: "DE", city: "Nuremberg", on: 6, pr: ["hysteria2", "vless", "amneziawg"], gb: 21.4, cpu: 14, up: 18, ip: "203.0.113.67" },
  { n: "nl2", cc: "NL", city: "Amsterdam", on: 5, pr: ["hysteria2", "vless"], gb: 12.2, cpu: 11, up: 22, ip: "203.0.113.74" },
  { n: "fi2", cc: "FI", city: "Helsinki", on: 4, pr: ["hysteria2", "vless", "amneziawg"], gb: 33.0, cpu: 17, up: 9, ip: "203.0.113.81" },
  { n: "de3", cc: "DE", city: "Falkenstein", on: 4, pr: ["vless", "amneziawg"], gb: 6.3, cpu: 6, up: 26, ip: "203.0.113.88" },
  { n: "msk2", cc: "RU", city: "Moscow", on: 3, pr: ["vless"], gb: 4.1, cpu: 8, up: 13, ip: "203.0.113.95" },
  { n: "nl3", cc: "NL", city: "Rotterdam", on: 3, pr: ["hysteria2", "vless"], gb: 15.8, cpu: 12, up: 7, ip: "203.0.113.102" },
  { n: "spb1", cc: "RU", city: "Saint Petersburg", on: 2, pr: ["vless"], gb: 2.9, cpu: 3, up: 5, ip: "203.0.113.109" },
  { n: "fi3", cc: "FI", city: "Helsinki", on: 2, pr: ["hysteria2"], gb: 3.6, cpu: 4, up: 21, ip: "203.0.113.116" },
];

const row = (s: Spec, over: Record<string, unknown> = {}) => ({
  id: id(s.n),
  name: s.n,
  countryCode: s.cc,
  location: s.city,
  provider: "Example Cloud",
  notes: "",
  address: s.ip,
  status: NodeStatus.ONLINE,
  reason: undefined,
  protocols: s.pr,
  online: [{ protocol: s.pr[0] ?? "vless", users: s.on }],
  trafficTodayBytes: Math.round(s.gb * 1e9),
  hasMetrics: true,
  cpuPct: s.cpu,
  ramPct: 40,
  agentVersion: "0.1.71",
  uptimeS: s.up * 86400,
  lastSeenUnix: now,
  awgBackend: "auto",
  decoyMode: "builtin",
  decoySupported: true,
  decoyReasonCode: "",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  warp: { state: WarpState.NOT_CONFIGURED },
  ipv6: { entry: "", entrySupported: false, unavailableReason: "no_ipv6", addresses: [], src: "" },
  ...s.over,
  ...over,
});
const reason = (code: string, params: Record<string, string> = {}) => ({ code, params });

/** The states of the nodes of a scene: the board's one outdated agent (fi1), one that does not answer, or every kind. */
function fleetOf(state: string) {
  const count = state === "first" ? 0 : state === "one" ? 1 : state === "twelve" || state === "mixed" ? 12 : 4;
  const rows = all.slice(0, count).map((s) => row(s));
  const set = (name: string, over: Record<string, unknown>) => {
    const i = rows.findIndex((r) => r.name === name);
    if (i >= 0) rows[i] = { ...rows[i]!, ...over };
  };
  if (state === "dead") set("nl1", { status: NodeStatus.DOWN, reason: reason("agent_silent", { minutes: "14" }), online: [], hasMetrics: false, cpuPct: 0, trafficTodayBytes: 0, uptimeS: 0 });
  if (state === "mixed" || state === "list") set("de1", { reason: reason("state_drift") });
  if (state === "mixed") {
    set("nl1", { notes: "Hosted on the second contract.\nRenewal in March." });
    set("de2", { reason: reason("doctor_fail", { check: "journal" }) });
    set("nl2", { status: NodeStatus.NO_TRAFFIC, reason: reason("no_traffic") });
    set("fi2", { status: NodeStatus.BLIP, reason: reason("host_blip", { minutes: "3" }) });
    set("de3", { status: NodeStatus.UPDATING, online: [], hasMetrics: false });
    set("msk2", { status: NodeStatus.PENDING, reason: reason("enrollment_pending", { expires_in_minutes: "42" }), protocols: [], online: [], hasMetrics: false, trafficTodayBytes: 0, uptimeS: 0 });
    set("nl3", { status: NodeStatus.DOWN, reason: reason("agent_silent", { minutes: "26" }), online: [], hasMetrics: false, trafficTodayBytes: 0, uptimeS: 0 });
    set("spb1", { protocols: [] });
    set("fi3", { status: NodeStatus.RETIRED, online: [], hasMetrics: false, trafficTodayBytes: 0, uptimeS: 0 });
  }
  return rows;
}

const period = (n: Spec) => ({ peak: n.on + 2, cpuPk: Math.round(n.cpu * 2.1 + 6), spare: Math.round(60 + rnd(n.on * 9) * 100) });
const loadRows = (rows: ReturnType<typeof fleetOf>) =>
  rows.map((r, i) => {
    const s = all.find((x) => x.n === r.name)!;
    const p = period(s);
    const status = r.status !== NodeStatus.ONLINE ? "unknown" : p.cpuPk >= 35 ? "busy" : "ok";
    const known = status !== "unknown";
    return {
      nodeId: r.id, nodeName: r.name, connected: r.status === NodeStatus.ONLINE, cpus: 2, status, bottleneck: !known ? "" : i === 0 ? "cpu" : i === 2 ? "bandwidth" : "", hoursWithData: known ? 150 + i : 0, extHours: 120,
      busyHourUnix: now - 7200, busyHourCpuPct: known ? p.cpuPk : 0, peopleBusyHour: known ? p.peak : 0, peoplePeak: known ? p.peak : 0, cpuPeakP95Pct: p.cpuPk + 2, cpuPeakTypicalPct: Math.round(p.cpuPk / 5),
      stealP95Pct: 0.4, ramPeakPct: 31 + i * 3, rxPeakBps: 60e6, txPeakBps: 70e6, capacityMbps: 250, capacitySource: "configured", bandwidthPeakPct: i === 2 ? 62 : 14 + i, fit: "ok", cpuPctPerHundredPeople: 40,
      mbpsPerPerson: 0.21, conntrackPeakPct: 1.8, diskUsedPct: 18 + i * 2, swapUsedMaxBytes: 0, headroomPeople: known ? p.spare : undefined, agentCpuAvgPct: 0.4, agentCpuMaxPct: 1.1, agentRssMaxBytes: 92e6, agentRssGrowthPctPerDay: 0.2,
      recommendations: status === "busy" ? [{ code: "cpu_busy", severity: "warn", params: { cpu_pct: String(p.cpuPk), peak_pct: String(p.cpuPk + 2), target: "fi1", target_headroom: "140" } }] : [],
    };
  });

// ---- the map's data: msk1 and msk2 are entries relaying to the others
const net = (p: string) => (p === "hysteria2" || p === "amneziawg" ? "udp" : "tcp");
const port = (p: string) => (p === "hysteria2" ? 443 : p === "amneziawg" ? 14491 : 8443);
const profileName = (p: string) => (p === "hysteria2" ? "HY2" : p === "vless" ? "VLESS TCP" : "AWG");
const inboundsOf = (name: string, protocols: string[]) =>
  protocols.map((p) => ({ id: `${name}_${p}`, profileId: `prf_${name}_${p}`, profileName: profileName(p), protocol: p, port: port(p), state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, nodeId: id(name), nodeName: name }));
const relay = (rid: string, via: string, exit: string, p: string) => ({
  id: rid, inboundId: `${exit}_${p}`, exitNodeId: id(exit), exitNodeName: exit, profileId: `prf_${exit}_${p}`, profileName: profileName(p), protocol: p, viaNodeId: id(via), viaNodeName: via, network: net(p), port: port(p), exitPort: port(p),
  enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.40", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200, bytes: 0, packets: 0, countedUnix: 0,
});
const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 600, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const verdictOf = (n: string) => (n.startsWith("msk") || n.startsWith("spb") ? CountryVerdict.OK : n.startsWith("de") ? CountryVerdict.BLOCKED : n === "nl1" ? CountryVerdict.PARTIAL : CountryVerdict.OK);
const country = (n: string) => ({
  nodeId: id(n), country: "RU", verdict: verdictOf(n), atUnix: now - 600, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdictOf(n), tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...(verdictOf(n) === CountryVerdict.OK ? [] : [vantage("MTS", "dark")]), vantage("Rostelecom", "reach")], sourceVerdicts: [],
});

function seeded(state: string, role: Role, edge: boolean) {
  const rows = fleetOf(state);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(edge ? { edition: "edge" } : {}) } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: rows, expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, {
    nodesTotal: rows.length,
    nodes: rows.map((r) => ({ id: r.id, name: r.name, countryCode: r.countryCode, location: r.location, status: r.status, reason: r.reason, online: [], sparkBytes: [] })),
    alertsActive: 0,
    alertsCritical: 0,
  } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 35, online: rows.reduce((a, r) => a + r.online.reduce((x, o) => x + o.users, 0), 0) } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, {
    nowUnix: now,
    panel: { version: "v0.1.71", built: now, update: { version: "v0.1.86", url: "https://github.com/example/mistgate/releases/tag/v0.1.86", checkedUnix: now, available: true, supported: true, installable: true, installing: false, errorKey: "" } },
    bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: now },
    nodes: rows.map((r) => ({ nodeId: r.id, name: r.name, state: r.name === "fi1" ? NodeUpdateState.OUTDATED : NodeUpdateState.UP_TO_DATE, supportsUpdate: true, built: now, version: r.name === "fi1" ? "0.1.70" : "0.1.71", onlineUsers: 1, inbounds: 2, scheduledUnix: 0, address: "", arch: "amd64" })),
  } as never);
  // the saved SSH access: two nodes log in by password only (the panel-key offer)
  c.setQueryData(["node-server-access"], {
    access: rows.slice(0, 4).map((r, i) => ({ nodeId: r.id, nodeName: r.name, host: r.address, port: 22, username: "root", fingerprint: "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0", configuredUnix: now - 86400 * 20, rotationPending: false, passwordGenerated: false, nodeRetired: false, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: state === "offer" ? "" : i < 2 ? "" : "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n" })),
  } as never);
  if (state === "offer") c.setQueryData(["node-server-access"], { access: rows.slice(0, 2).map((r) => ({ nodeId: r.id, nodeName: r.name, host: r.address, port: 22, username: "root", fingerprint: "", configuredUnix: now, rotationPending: false, passwordGenerated: false, nodeRetired: false, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: "" })) } as never);
  c.setQueryData(fleetLoadQuery(168).queryKey, {
    nowUnix: now, hours: 168, nodes: loadRows(rows),
    summary: { ok: 3, busy: 1, overloaded: 0, unknown: 0, headroomPeople: 330, headroomNodes: 3, mostLoaded: [], mostFree: [{ nodeId: id("msk1"), nodeName: "msk1", status: "ok", busyHourCpuPct: 14, headroomPeople: 150 }] },
  } as never);
  // the map
  const rel = state === "map" || state === "map-card" ? [relay("r1", "msk1", "de1", "vless"), relay("r2", "msk1", "fi1", "vless"), relay("r3", "msk1", "nl1", "hysteria2")] : [];
  c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
  c.setQueryData(checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: rows.map((r) => country(r.name)) } as never);
  c.setQueryData(relaysQuery("").queryKey, rel as never);
  for (const r of rows) {
    c.setQueryData(relaysQuery(r.id).queryKey, rel.filter((x) => x.viaNodeId === r.id || x.exitNodeId === r.id) as never);
    c.setQueryData(nodeQuery(r.id).queryKey, { node: r, inbounds: inboundsOf(r.name, r.protocols), portChecks: [], tcpChecks: [], facts: { hasIpv6: false } } as never);
    c.setQueryData(checksQuery(r.id).queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [country(r.name)] } as never);
    c.setQueryData(doctorQuery(r.id).queryKey, { nowUnix: now, nodes: [{ nodeId: r.id, items: [{ id: "ipv6", status: 1, params: { ipv6: "no" } }] }] } as never);
  }
  return c;
}

const demoOf: Record<string, NodesDemo> = {
  select: { selecting: true, ticked: [id("de1"), id("nl1")] },
  find: { find: "hel" },
  nomatch: { find: "zzz" },
  "map-card": { focus: id("nl1") },
};

// the batch the floating bar shows when a state asks for one (the bar's own states: running, done, failed)
const bar = (state: string) => {
  const rowOf = (n: string, st: Row["state"], over: Partial<Row> = {}): Row => ({ id: id(n), name: n, host: all.find((x) => x.n === n)!.ip, state: st, ...over });
  const run: Job = { spec: { action: "ports", limit: 1, error: () => "" }, running: true, stopping: false, rows: [rowOf("de1", "ok"), rowOf("nl1", "running")] };
  const done: Job = { ...run, spec: { ...run.spec, call: async () => {} }, running: false, rows: [rowOf("de1", "ok", { kind: "ok", label: "Fine", note: "3 ports fine", link: true }), rowOf("nl1", "ok", { kind: "ok", label: "Fine", note: "2 ports fine", link: true })] };
  const failed: Job = { ...done, rows: [done.rows[0]!, rowOf("nl1", "bad", { note: "No answer from the node" })] };
  return state === "job-running" ? run : state === "job-done" ? done : failed;
};

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function NodesPage() {
  const t = useT();
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "list";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    // the view the screen opens on is the browser's last choice (the scene has no ?view= of its own)
    writePref("nodes.view", state === "load" ? "load" : state === "map" || state === "map-card" ? "map" : "list");
    return seeded(state, params.get("role") === "helper" ? Role.HELPER : Role.OWNER, state === "edge");
  });
  const job = state.startsWith("job-");
  const picked = [id("de1"), id("nl1"), id("fi1")];
  const avail = Object.fromEntries((["ports", "doctor", "agent", "password", "panelKey", "bindKey", "unbindKey", "decoy"] as ActionId[]).map((a) => [a, { count: 3 }])) as Record<ActionId, Avail>;
  const noop = () => {};
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="nodes" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <NodesScreen demo={job ? { selecting: true, ticked: picked } : demoOf[state]} />
          </ShellLayout>
          {job && (
            <BulkBar
              nodes={picked.map((p) => ({ id: p, countryCode: all.find((x) => id(x.n) === p)!.cc }))}
              hidden={0}
              allShown={false}
              total={4}
              avail={avail}
              agentOff={false}
              busy={state === "job-running"}
              job={bar(state)}
              rows={bar(state).rows}
              panel={panelFor(t, "ports")}
              onAction={noop}
              onClear={noop}
              onAll={noop}
              onStop={noop}
              onRetry={noop}
              onCloseJob={noop}
            />
          )}
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "nodes-list", state: "list", title: "The board's four nodes: de1 with a remark, fi1 with an old agent (1440)", w: 1440, h: 780 },
  { id: "nodes-list-light", state: "list", title: "The same on the light theme", w: 1440, h: 780, params: "&theme=light" },
  { id: "nodes-list-rail", state: "list", title: "A 900 window: the rail, the card narrows (profiles, online, arrow)", w: 900, h: 780 },
  { id: "nodes-list-phone", state: "list", title: "A phone, 400 wide", w: 400, h: 780 },
  { id: "nodes-list-fa", state: "list", title: "Right to left (fa)", w: 1440, h: 780, params: "&lang=fa" },
  { id: "nodes-twelve", state: "twelve", title: "Twelve nodes", w: 1440, h: 1100 },
  { id: "nodes-mixed", state: "mixed", title: "Every state a node has: attention, broken, no traffic, blip, updating, waiting, down, retired, notes", w: 1440, h: 1100 },
  { id: "nodes-dead", state: "dead", title: "A node that does not answer (nl1)", w: 1440, h: 780 },
  { id: "nodes-one", state: "one", title: "One node", w: 1440, h: 780 },
  { id: "nodes-first", state: "first", title: "No node yet", w: 1440, h: 780 },
  { id: "nodes-first-narrow", state: "first", title: "No node yet, 900", w: 900, h: 900 },
  { id: "nodes-first-phone", state: "first", title: "No node yet, a phone", w: 400, h: 900 },
  { id: "nodes-offer", state: "offer", title: "The panel-key offer above the list (owner)", w: 1440, h: 780 },
  { id: "nodes-select", state: "select", title: "Selecting: the check column and the bulk bar", w: 1440, h: 780 },
  { id: "nodes-select-phone", state: "select", title: "Selecting on a phone", w: 400, h: 780 },
  { id: "nodes-find", state: "find", title: "A search that finds fi1 (the search is open while it holds a word)", w: 1440, h: 780 },
  { id: "nodes-nomatch", state: "nomatch", title: "A search that finds nothing", w: 1440, h: 780 },
  { id: "nodes-helper", state: "list", title: "A helper: no Select, no bulk", w: 1440, h: 780, params: "&role=helper" },
  { id: "nodes-job-running", state: "job-running", title: "The bar while a batch runs", w: 1440, h: 780 },
  { id: "nodes-job-done", state: "job-done", title: "The bar when a batch ended well", w: 1440, h: 780 },
  { id: "nodes-job-failed", state: "job-failed", title: "The bar when one node failed (its list opens by itself)", w: 1440, h: 780 },
  { id: "nodes-load", state: "load", title: "Load (1440)", w: 1440, h: 900 },
  { id: "nodes-load-phone", state: "load", title: "Load on a phone", w: 400, h: 900 },
  { id: "nodes-map", state: "map", title: "The map", w: 1440, h: 900 },
  { id: "nodes-map-card", state: "map-card", title: "The map with a node's card open", w: 1440, h: 900 },
  { id: "nodes-edge", state: "edge", title: "The Cloudflare edition: no load view", w: 1440, h: 780 },
];

export function NodesKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Nodes</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=nodes-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
