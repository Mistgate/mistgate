import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { EventSeverity, OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { AlertKind, AlertSeverity, CheckStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { useLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { alertsQuery, checksQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { Shot } from "./dev-kit-redesign";
import { ShellLayout } from "./shell";
import { OverviewScreen, OverviewFailure, OverviewLoading } from "./overview";
import { settingsQuery } from "./subscriptions/queries";
import { groupsQuery, profileListQuery } from "./users/rpc";

// Development only. /dev-kit?only=overview: the Overview of the redesign in every state of the board's switch (nodes 0 / 1 / 4 /
// 12, warnings 0 / 1 / 4 / 9, a fault) and the states of the product the board does not draw (loading, a failed load, a node
// that waits for its install, the first-run list with a node, the list done, a read-only role, the Cloudflare edition), each in
// a frame of its own width (an iframe, so the window's breakpoints answer to the frame), inside the real shell. The frames follow
// the theme and language chosen at the top. Each frame is a page of its own, /dev-kit?only=overview-page&state=<state>:
//   calm | issues | down | many | one | empty | pending | first | done | loading | error | viewer | edge | phone-calm | phone-issues | phone-empty
// `fx=1` on a page turns the reveal animation on (the numbers shuffle, the wave runs after the last warning is fixed).
// Made-up data: nodes de1, nl1, fi1, msk1 (and more), people alice, bob, carol, dave and erin, a panel called owner.

const NOW = Math.floor(Date.now() / 1000);
const HOUR = NOW - (NOW % 3600);
const rnd = (k: number) => {
  const x = Math.sin(k * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
};
const day = (hr: number) => 0.16 + 0.55 * Math.max(0, Math.cos(((hr - 21) / 24) * 2 * Math.PI)) ** 2 + 0.3 * Math.max(0, Math.cos(((hr - 13) / 24) * 2 * Math.PI)) ** 3;
const scale = (a: number[], total: number) => {
  const k = total / a.reduce((x, y) => x + y, 0);
  return a.map((v) => v * k);
};

type Proto = "hy" | "vl" | "aw";
const pid: Record<Proto, string> = { hy: "hysteria2", vl: "vless", aw: "amneziawg" };
// the board's nodes: [country, name, city, people online, Mbit/s, protocols, hour of a missed check (-1 none)]
const ALL: [string, string, string, number, number, Proto[], number][] = [
  ["DE", "de1", "Frankfurt", 3, 64, ["hy", "vl", "aw"], -1],
  ["FI", "fi1", "Helsinki", 1, 21, ["hy", "vl", "aw"], 9],
  ["NL", "nl1", "Amsterdam", 1, 38, ["hy", "vl"], -1],
  ["RU", "msk1", "Moscow", 1, 17, ["hy"], -1],
  ["DE", "de2", "Nuremberg", 6, 88, ["hy", "vl", "aw"], -1],
  ["NL", "nl2", "Amsterdam", 5, 72, ["hy", "vl"], 4],
  ["FI", "fi2", "Helsinki", 4, 51, ["hy", "vl", "aw"], -1],
  ["DE", "de3", "Falkenstein", 4, 47, ["vl", "aw"], -1],
  ["RU", "msk2", "Moscow", 3, 35, ["hy"], -1],
  ["NL", "nl3", "Rotterdam", 3, 29, ["hy", "vl"], 15],
  ["RU", "spb1", "Saint Petersburg", 2, 24, ["hy"], -1],
  ["FI", "fi3", "Helsinki", 2, 19, ["hy"], -1],
];

export type State = "calm" | "issues" | "down" | "many" | "one" | "empty" | "pending" | "first" | "done" | "loading" | "error" | "viewer" | "edge";
type Spec = { nodes: number; issues: number; down?: boolean; role?: Role; edge?: boolean };
const specs: Record<State, Spec> = {
  calm: { nodes: 4, issues: 0 },
  issues: { nodes: 4, issues: 4 },
  down: { nodes: 4, issues: 4, down: true },
  many: { nodes: 12, issues: 9 },
  one: { nodes: 1, issues: 4 },
  empty: { nodes: 0, issues: 0 },
  pending: { nodes: 1, issues: 0 },
  first: { nodes: 1, issues: 0 },
  done: { nodes: 4, issues: 0 },
  loading: { nodes: 4, issues: 0 },
  error: { nodes: 4, issues: 0 },
  viewer: { nodes: 4, issues: 4, role: Role.READONLY },
  edge: { nodes: 4, issues: 2, edge: true },
};

const PEOPLE = { 0: [0, 0], 1: [1, 2], 4: [6, 8], 12: [35, 86] } as Record<number, [number, number]>;
const TOTAL = { 1: [[9.8, 14.1, 2.3], [61, 92, 14]], 4: [[76.2, 124.3, 18.4], [498, 786, 121]], 12: [[286, 471, 69], [1904, 3120, 455]] } as Record<number, number[][]>;
const NOW_ONLINE = { 1: [1, 0, 0], 4: [2, 3, 1], 12: [12, 17, 6] } as Record<number, number[]>;
const GB = 1e9;

function points(dk: number, hours: number, traffic: boolean) {
  const hrOf = (i: number) => new Date((HOUR - (hours - 1 - i) * 3600) * 1000).getHours();
  const idx = Array.from({ length: hours }, (_, i) => i);
  const ko = ({ 1: 0.3, 4: 1, 12: 5.8 } as Record<number, number>)[dk] ?? 1;
  let a: number[], b: number[], c: number[];
  if (traffic) {
    const tot = TOTAL[dk]![hours === 24 ? 0 : 1]!;
    a = scale(idx.map((i) => day(hrOf(i)) * (0.86 + rnd(i) * 0.28)), tot[0]! * GB);
    b = scale(idx.map((i) => day(hrOf(i) + 0.6) * (0.84 + rnd(i + 40) * 0.32)), tot[1]! * GB);
    c = scale(idx.map((i) => day(hrOf(i) - 1) * (0.8 + rnd(i + 70) * 0.4)), tot[2]! * GB);
  } else {
    const lo = dk === 1 ? 0 : 1;
    a = idx.map((i) => Math.max(lo, Math.round(day(hrOf(i)) * 2.6 * ko)));
    b = idx.map((i) => Math.max(lo, Math.round(day(hrOf(i) + 1) * 4 * ko)));
    c = idx.map((i) => Math.round(day(hrOf(i) - 1) * 1.6 * ko));
    [a[hours - 1], b[hours - 1], c[hours - 1]] = NOW_ONLINE[dk] as [number, number, number];
  }
  return idx.map((i) => ({
    startUnix: HOUR - (hours - 1 - i) * 3600,
    values: [a, b, c].map((arr, k) => ({ protocol: ["hysteria2", "vless", "amneziawg"][k]!, value: Math.round(arr[i]!) })),
  }));
}

const event = (id: number, code: string, ago: number, over: Record<string, unknown> = {}) => ({
  id, timeUnix: NOW - ago, severity: EventSeverity.INFO, code, params: {}, nodeId: "", nodeName: "", userId: "", userName: "", inboundId: "", profileName: "", protocol: "", source: "panel", ...over,
});

const alert = (over: Record<string, unknown>) => ({
  id: `alt_${Math.random().toString(36).slice(2, 8)}`, severity: AlertSeverity.WARNING, kind: AlertKind.CHECK_FAILED, nodeId: "", nodeName: "", subject: "", titleKey: "", params: {}, whyKey: "",
  firstSeenUnix: NOW - 1800, openedUnix: NOW - 1800, lastSeenUnix: NOW, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["open_node", "mute"], ...over,
});
const journal = (n: string, gb: string) =>
  alert({ nodeId: `nod_${n}`, nodeName: n, kind: AlertKind.DOCTOR_WARN, subject: "journald_size", titleKey: "health.alert.doctor_warn.title", whyKey: "health.doctor.journald_size.why", params: { check: "journald_size", journal_mb: String(Math.round(Number(gb) * 1024)), cap_mb: "500" }, actions: ["apply_fix:journald_vacuum", "open_node", "accept", "mute"] });
const lossy = (n: string) =>
  alert({ nodeId: `nod_${n}`, nodeName: n, kind: AlertKind.CHECK_FAILED, subject: "inb_1", titleKey: "health.alert.check_failed.title", whyKey: "", params: { profile: "hy2 · 443", inbound: "inb_1", port: "443" }, actions: ["restart_inbound", "open_node", "mute"] });
const cert = (n: string, d: number) =>
  alert({ nodeId: `nod_${n}`, nodeName: n, kind: AlertKind.CERT_EXPIRY, subject: "inb_1", titleKey: "health.alert.cert_expiry.title", whyKey: "health.alert.cert_expiry.why", params: { profile: "hy2 · 443", days_left: String(d), server_part: "" }, actions: ["open_profiles", "mute"] });
const agent = (n: string) =>
  alert({ nodeId: `nod_${n}`, nodeName: n, kind: AlertKind.DOCTOR_WARN, subject: "unit_generation", titleKey: "health.alert.doctor_warn.title", whyKey: "", params: { check: "unit_generation" }, actions: ["apply_fix:refresh_unit", "open_node", "mute"] });

/** The alerts of the board's pool for this many nodes and warnings; the last one of a full list is the agent's request, not an alert. */
function pool(nodes: number, issues: number): { alerts: ReturnType<typeof alert>[]; ask: boolean } {
  const list =
    nodes === 1
      ? [journal("de1", "3.8"), cert("de1", 9), agent("de1")]
      : issues <= 4
        ? [journal("de1", "3.8"), lossy("fi1"), cert("nl1", 9)]
        : nodes === 4
          ? [journal("de1", "3.8"), lossy("fi1"), cert("nl1", 9), agent("msk1"), journal("nl1", "2.6"), cert("fi1", 12), agent("de1"), journal("msk1", "1.9")]
          : [journal("de1", "3.8"), lossy("fi1"), cert("nl1", 9), agent("msk2"), journal("de2", "5.1"), cert("fi2", 12), agent("nl3"), journal("spb1", "1.9")];
  return { alerts: list.slice(0, Math.max(0, issues - 1)), ask: issues > 0 };
}

function checksFor(cards: { id: string; name: string; status: NodeStatus }[], miss: Map<string, number>, protos: Map<string, Proto[]>, failing: Set<string>) {
  const columns = [
    { profileId: "prf_hy", profileName: "hy2 · 443", protocol: "hysteria2" },
    { profileId: "prf_vl", profileName: "vless · 443", protocol: "vless" },
    { profileId: "prf_aw", profileName: "awg · 51820", protocol: "amneziawg" },
  ];
  const rows = cards.map((c) => ({
    nodeId: c.id,
    nodeName: c.name,
    countryCode: "",
    nodeStatus: c.status,
    cells: columns.map((col, ci) => {
      const deployed = (protos.get(c.name) ?? []).map((p) => pid[p]).includes(col.protocol);
      const history = Array.from({ length: 48 }, (_, k) => {
        const start = HOUR - (47 - k) * 1800;
        const hr = Math.floor(k / 2);
        const out = miss.get(c.name) === hr;
        const bad = failing.has(c.name) && hr > 20;
        return { startUnix: start, ok: deployed ? (out || bad ? 1 : 6) : 0, failed: deployed ? (bad ? 5 : out ? 1 : 0) : 0, latencyMs: deployed ? 28 + Math.round(rnd(k * 7 + ci) * 30) : 0 };
      });
      return { deployed, inboundId: deployed ? `inb_${ci}` : "", last: deployed ? { status: CheckStatus.OK, atUnix: NOW, latencyMs: 40 } : undefined, failStreak: 0, history };
    }),
  }));
  return { nowUnix: NOW, intervalS: 300, columns, rows, countryChecks: [] };
}

/** The query cache of a scene: what the panel would have answered in that state (the tests read the same data). */
export function seeded(state: State) {
  const sp = specs[state];
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  const dk = sp.nodes === 0 ? 0 : sp.nodes === 1 ? 1 : sp.nodes === 4 ? 4 : 12;
  const pending = state === "pending";
  const list = ALL.slice(0, sp.nodes);
  const downName = sp.down ? "nl1" : "";
  const pooled = pool(sp.nodes, sp.issues);
  const alerts = pooled.alerts;
  const ask = pooled.ask && !sp.edge;
  const cards = list.map(([cc, name, city, on, mbit, protos]) => {
    const dead = name === downName;
    const first = state === "first";
    const status = pending ? NodeStatus.PENDING : dead ? NodeStatus.DOWN : NodeStatus.ONLINE;
    const people = dead ? 0 : on;
    return {
      id: `nod_${name}`,
      name,
      countryCode: cc,
      location: city,
      provider: "",
      status,
      reason: pending
        ? { code: "enrollment_pending", params: { expires_in_minutes: "52", expires_unix: String(NOW + 52 * 60) } }
        : dead
          ? { code: "agent_silent", params: { minutes: "14" } }
          : first
            ? { code: "no_profiles", params: {} }
            : undefined,
      online: dead || pending || first ? [] : protos.map((p, k) => ({ protocol: pid[p], users: k === 0 ? Math.max(0, people - (protos.length - 1)) : 1 })).filter((o) => o.users > 0),
      downBps: dead || pending || first ? 0 : mbit * 1e6,
      upBps: 0,
      hasMetrics: true,
      cpuPct: 8,
      sparkBytes: Array.from({ length: 24 }, (_, i) => 1e8 + i * 2e7),
    };
  });
  const people = PEOPLE[dk]!;
  const downOn = downName ? (list.find((n) => n[1] === downName)?.[3] ?? 0) : 0;
  const users = dk === 1 ? [["alice", 31, "de1"]] : dk === 12 ? [["alice", 64, "de2"], ["bob", 52, "nl2"], ["carol", 41, "de1"], ["dave", 33, "fi2"], ["erin", 28, "msk2"]] : [["alice", 31, "de1"], ["bob", 12, "nl1"], ["carol", 4, "fi1"], ["dave", 2, "de1"], ["erin", 1, "msk1"]];
  const events = [
    ...(downName ? [event(9, "node_down", 480, { severity: EventSeverity.ERROR, params: { minutes: "14" }, nodeId: `nod_${downName}`, nodeName: downName })] : []),
    ...(dk === 1
      ? [event(5, "user_created", 1300, { userId: "usr_bob", userName: "bob" }), event(4, "user_created", 1900, { userId: "usr_alice", userName: "alice" }), event(3, "node_enrolled", 2900, { nodeId: "nod_de1", nodeName: "de1", params: { agent_version: "0.3.1" } })]
      : [
          event(8, "user_created", 1900, { userId: "usr_carol", userName: "carol" }),
          event(7, "node_recovered", 10_000, { params: { minutes: "2" }, nodeId: "nod_nl1", nodeName: "nl1" }),
          event(6, "cert_renewed", 33_000, { nodeId: "nod_de1", nodeName: "de1" }),
          event(5, "node_enrolled", 40_000, { nodeId: "nod_fi1", nodeName: "fi1", params: { agent_version: "0.3.1" } }),
        ]),
  ].slice(0, 5);
  const overview = (range: OverviewRange) => {
    const hours = range === OverviewRange.OVERVIEW_RANGE_24H ? 24 : 168;
    return {
      nowUnix: NOW,
      nodesTotal: cards.length,
      nodesProblem: 0,
      usersOnline: people[0]! - (downName ? downOn : 0),
      nodes: cards,
      traffic: dk ? points(dk, hours, true) : [],
      online: dk ? points(dk, hours, false) : [],
      events: state === "empty" || pending || state === "first" ? [] : events,
      topConsumers: dk ? users.filter((u) => u[2] !== downName).map((u) => ({ userId: `usr_${u[0]}`, userName: u[0], nodeId: `nod_${u[2]}`, nodeName: u[2], downBps: (u[1] as number) * 1e6 })) : [],
      alertsActive: alerts.length + (downName ? 1 : 0),
      alertsCritical: downName ? 1 : 0,
    };
  };
  qc.setQueryData(overviewQuery(OverviewRange.OVERVIEW_RANGE_24H).queryKey, overview(OverviewRange.OVERVIEW_RANGE_24H) as never);
  qc.setQueryData(overviewQuery(OverviewRange.OVERVIEW_RANGE_7D).queryKey, overview(OverviewRange.OVERVIEW_RANGE_7D) as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes: cards.map((c) => ({ ...c, address: `${c.name}.example.com`, protocols: [], notes: "", trafficTodayBytes: 0, ramPct: 40, agentVersion: "0.3.1", uptimeS: 86400, lastSeenUnix: NOW, awgBackend: "auto" })), expectedAgentVersion: "0.3.1" } as never);
  const done = state === "done";
  qc.setQueryData(profileListQuery.queryKey, (done || dk >= 4 ? [{ id: "prf_1", name: "hy2 · 443", protocol: "hysteria2", nodeCount: 3, userCount: 12 }] : []) as never);
  qc.setQueryData(groupsQuery.queryKey, [{ id: "grp_all", name: "Everyone", profileIds: done || dk >= 4 ? ["prf_1"] : [], userCount: people[1]!, dnsPresetId: "", disabledNodeIds: [] }] as never);
  qc.setQueryData(userCountQuery.queryKey, { counts: { all: people[1]!, online: people[0]! } } as never);
  qc.setQueryData(settingsQuery.queryKey, { settings: { apps: [] }, serverSamples: [] } as never);
  const down = downName
    ? [alert({ severity: AlertSeverity.CRITICAL, kind: AlertKind.NODE_DOWN, nodeId: `nod_${downName}`, nodeName: downName, titleKey: "health.alert.node_down.title", whyKey: "health.alert.node_down.why", params: { minutes: "14" }, actions: ["open_node", "mute"] })]
    : [];
  qc.setQueryData(alertsQuery.queryKey, { nowUnix: NOW, active: [...down, ...alerts], history: [] } as never);
  qc.setQueryData(checksQuery("").queryKey, checksFor(cards, new Map(list.filter((n) => n[6] >= 0).map((n) => [n[1], n[6]])), new Map(list.map((n) => [n[1], n[5]])), new Set(downName ? [downName] : [])) as never);
  qc.setQueryData(awaitingQuery.queryKey, {
    nowUnix: NOW,
    receivedMs: Date.now(),
    awaiting: ask ? 1 : 0,
    approvals: ask ? [{ id: "pln_1", tokenId: "tok_1", tokenName: "assistant", tokenProfile: TokenProfile.OPERATOR, tool: "node_fix", facts: [], danger: [], reason: "", state: ApprovalState.AWAITING, createdUnix: NOW - 60, expiresUnix: NOW + 540 }] : [],
  } as never);
  qc.setQueryData(meQuery.queryKey, {
    admin: { id: "adm_1", displayName: "owner", role: sp.role ?? Role.OWNER },
    version: "0.1.71",
    sourceUrl: "https://github.com/example/mistgate",
    sourceStars: 298,
    stepUpUntilUnix: 0,
    edition: sp.edge ? "edge" : "vps",
    unavailable: sp.edge ? ["health_checks"] : [],
  } as never);
  return qc;
}

/** One whole page of the scene, for a frame or a screenshot at the window's own width. */
export function OverviewPage() {
  const params = new URLSearchParams(location.search);
  const state = (params.get("state") ?? "calm") as State;
  const [qc] = useState(() => {
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return seeded(state);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="overview" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            {state === "loading" ? <OverviewLoading /> : state === "error" ? <OverviewFailure error={new Error("503 Service Unavailable")} onRetry={() => {}} /> : <OverviewScreen />}
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

const frames: { id: string; state: State; title: string; w: number; h: number }[] = [
  { id: "overview-calm", state: "calm", title: "4 nodes, no warnings: the fleet is calm", w: 1440, h: 1500 },
  { id: "overview-issues", state: "issues", title: "4 nodes, 4 things that need you (three warnings, an agent's request)", w: 1440, h: 1500 },
  { id: "overview-down", state: "down", title: "4 nodes, nl1 does not answer: the fault is red", w: 1440, h: 1500 },
  { id: "overview-many", state: "many", title: "12 nodes, 9 things: the list folds into 'more', the table into 8 rows", w: 1440, h: 1700 },
  { id: "overview-one", state: "one", title: "1 node, 4 things", w: 1440, h: 1500 },
  { id: "overview-empty", state: "empty", title: "An empty panel: the first-run card and the ghost previews", w: 1440, h: 1100 },
  { id: "overview-pending", state: "pending", title: "The only node waits for its install", w: 1440, h: 1300 },
  { id: "overview-first", state: "first", title: "A node is up, it has no profile: the first-run card above the fleet", w: 1440, h: 1500 },
  { id: "overview-done", state: "done", title: "The first-run list is done: one line, Hide", w: 1440, h: 1500 },
  { id: "overview-loading", state: "loading", title: "Loading", w: 1440, h: 900 },
  { id: "overview-error", state: "error", title: "The load failed", w: 1440, h: 700 },
  { id: "overview-viewer", state: "viewer", title: "A read-only role: no add buttons, no fix buttons", w: 1440, h: 1500 },
  { id: "overview-edge", state: "edge", title: "The Cloudflare edition: no tunnel checks, so no Checks column", w: 1440, h: 1500 },
  { id: "overview-1100", state: "issues", title: "1100 wide: the hero is one column, the table loses its checks and speed", w: 1100, h: 1700 },
  { id: "overview-phone-calm", state: "calm", title: "A phone, 400 wide, calm", w: 400, h: 2300 },
  { id: "overview-phone-issues", state: "issues", title: "A phone, 400 wide, with warnings", w: 400, h: 2600 },
  { id: "overview-phone-empty", state: "empty", title: "A phone, 400 wide, an empty panel", w: 400, h: 1700 },
];

export function OverviewKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Overview</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={`${f.title} · ${f.w} px`}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=overview-page&state=${f.state}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
