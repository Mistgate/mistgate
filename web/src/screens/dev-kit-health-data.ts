import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CheckStatus, CountryVerdict, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";

// Development only. The made-up fleet and findings of the Health board (PnHealth): four nodes (de1, fi1, nl1, msk1), the alerts and the history of its script, the
// client-eye checks (the country check from Russia on, the TSPU probe's last run), the doctor's report with the one finding on de1; and what the panel would answer
// to GetAlerts, GetChecks and GetDoctor for them in the state a scene asks for. Documentation addresses and invented names only.

export const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86_400;
export const nid = (n: string) => `nod_${n}`;
export const FLEET = [
  { n: "de1", cc: "DE", city: "Frankfurt" },
  { n: "fi1", cc: "FI", city: "Helsinki" },
  { n: "nl1", cc: "NL", city: "Amsterdam" },
  { n: "msk1", cc: "RU", city: "Moscow" },
];

/** The scene's variants (a state is `<tab>.<variant>.<variant>…`): what changes the data, what changes a window or a row. */
export type Flags = {
  calm: boolean;
  nonodes: boolean;
  first: boolean;
  load: boolean;
  err: boolean;
  crit: boolean;
  fixfail: boolean;
  nock: boolean;
  off: boolean;
  /** Product states the board does not draw. */
  muted: boolean;
  grouped: boolean;
  loss: boolean;
  warpreach: boolean;
  history: boolean;
  nodeproblems: boolean;
  edge: boolean;
  /** msk1 has no alert (the filter then finds nothing on it). */
  mskclean: boolean;
};
export const flagsOf = (variant: string[]): Flags => {
  const has = (k: string) => variant.includes(k);
  return {
    calm: has("calm"),
    nonodes: has("nonodes"),
    first: has("first"),
    load: has("load"),
    err: has("err"),
    crit: has("crit") || has("off"),
    fixfail: has("fixfail"),
    nock: has("nock"),
    off: has("off"),
    muted: has("muted"),
    grouped: has("grouped"),
    loss: has("loss"),
    warpreach: has("warpreach"),
    history: has("history"),
    nodeproblems: has("nodeproblems"),
    edge: has("edge"),
    mskclean: has("mskclean"),
  };
};

const alert = (over: Record<string, unknown>) => ({
  id: "alt",
  severity: AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "",
  nodeName: "",
  subject: "",
  titleKey: "",
  params: {} as Record<string, string>,
  whyKey: "",
  firstSeenUnix: NOW - H,
  openedUnix: NOW - H,
  lastSeenUnix: NOW,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["open_node", "mute"],
  ...over,
});
const at = (node: string) => ({ nodeId: nid(node), nodeName: node });
const lossy = (id: string, node: string, port: string, got: string, over: Record<string, unknown> = {}) =>
  alert({
    id,
    ...at(node),
    kind: AlertKind.PORT_LOSSY,
    subject: `inb_${id}`,
    titleKey: "health.alert.port_lossy.title",
    whyKey: "health.alert.port_lossy.why",
    params: { port, profile: `Hysteria2 · ${port}`, sent: "300", got, sender: "fi1" },
    actions: ["open_profiles", "open_node", "mute"],
    openedUnix: NOW - 9 * H,
    firstSeenUnix: NOW - 9 * H,
    ...over,
  });

// the alerts about nodes of the board's script, and the one that replaces nl1's when it falls (crit)
const journal = alert({
  id: "j",
  ...at("de1"),
  kind: AlertKind.DOCTOR_WARN,
  subject: "journald_size",
  titleKey: "health.alert.doctor_warn.title",
  whyKey: "health.doctor.journald_size.why",
  params: { check: "journald_size", journal_mb: "3870", cap_mb: "500" },
  actions: ["apply_fix:journald_vacuum", "open_node", "accept", "mute"],
  openedUnix: NOW - 2 * D,
  firstSeenUnix: NOW - 2 * D,
});
const russia = alert({
  id: "r",
  ...at("fi1"),
  kind: AlertKind.COUNTRY_BLOCKED,
  subject: "partial",
  titleKey: "health.alert.country_blocked.title",
  whyKey: "health.alert.country_blocked.why.partial",
  params: { country: "RU", verdict: "partial", networks: "Operator A (AS64496, Moscow, Globalping), Operator B (AS64497, Kazan, Globalping)", sources: "globalping" },
  actions: ["open_node", "mute"],
  openedUnix: NOW - 20 * H,
  firstSeenUnix: NOW - 20 * H,
});
const xhttp = alert({
  id: "x",
  ...at("fi1"),
  subject: "inb_xhttp",
  titleKey: "health.alert.check_failed.title",
  whyKey: "health.alert.check_failed.why.timeout",
  params: { inbound: "inb_xhttp", profile: "VLESS · XHTTP", port: "2053", error_code: "timeout", online: "2" },
  actions: ["restart_inbound", "open_node", "mute"],
  openedUnix: NOW - 26 * 60,
  firstSeenUnix: NOW - 26 * 60,
});
const portsNl1 = [lossy("u1", "nl1", "2083", "207"), lossy("u2", "nl1", "33183", "264"), lossy("u3", "nl1", "4443", "276")];
const portMsk1 = lossy("m", "msk1", "443", "243", { openedUnix: NOW - 3 * H, firstSeenUnix: NOW - 3 * H, params: { port: "443", profile: "Hysteria2 · 443", sent: "300", got: "243", sender: "fi1" } });
const down = alert({
  id: "c",
  ...at("nl1"),
  severity: AlertSeverity.CRITICAL,
  kind: AlertKind.NODE_DOWN,
  subject: "agent",
  titleKey: "health.alert.node_down.title",
  whyKey: "health.alert.node_down.why",
  params: { minutes: "4" },
  actions: ["open_node", "mute"],
  openedUnix: NOW - 4 * 60,
  firstSeenUnix: NOW - 4 * 60,
});

// the alerts about people of the board's script
const person = (over: Record<string, unknown>) => alert({ nodeId: "", nodeName: "", ...over });
const bob = person({
  id: "p1",
  kind: AlertKind.USER_CONNECTION,
  subject: "dev_bob",
  titleKey: "health.alert.user_connection.title",
  whyKey: "health.alert.user_connection.why.silent",
  params: { user_name: "bob", user_id: "usr_bob" },
  actions: ["open_user", "mute"],
  openedUnix: NOW - D,
  firstSeenUnix: NOW - D,
});
const alice = person({
  id: "p2",
  kind: AlertKind.TORRENT,
  subject: "usr_alice",
  titleKey: "health.alert.torrent.title",
  whyKey: "health.alert.torrent.why",
  params: { user_name: "alice", user_id: "usr_alice", nodes: "de1", count: "6", last_unix: String(NOW - 12 * 60), evidence: "dht", ports: "" },
  actions: ["open_user", "mute"],
  openedUnix: NOW - 5 * H,
  firstSeenUnix: NOW - 5 * H,
});
const limit = person({
  id: "p3",
  severity: AlertSeverity.INFO,
  kind: AlertKind.DEVICE_LIMIT,
  subject: "usr_alice",
  titleKey: "health.alert.device_limit.title",
  whyKey: "health.alert.device_limit.why",
  params: { user_name: "alice", user_id: "usr_alice", used: "5", limit: "5" },
  actions: ["open_user", "mute"],
  openedUnix: NOW - 2 * D,
  firstSeenUnix: NOW - 2 * D,
});

// the history of the week: today, yesterday, and a few days back (the clock times of the board)
const today = new Date();
const clock = (daysAgo: number, h: number, m: number) => Math.floor(new Date(today.getFullYear(), today.getMonth(), today.getDate() - daysAgo, h, m).getTime() / 1000);
const closed = (id: string, over: Record<string, unknown>, end: number, lasted: number) => alert({ id, resolvedAtUnix: end, openedUnix: end - lasted, firstSeenUnix: end - lasted, resolution: "node_returned", ...over });
const board = [
  closed("h1", { ...at("fi1"), severity: AlertSeverity.INFO, kind: AlertKind.HOST_BLIP, subject: "agent", titleKey: "health.alert.host_blip.title", actions: [] }, clock(0, 10, 11), 60),
  closed("h2", { ...at("nl1"), severity: AlertSeverity.CRITICAL, kind: AlertKind.NODE_DOWN, subject: "agent", titleKey: "health.alert.node_down.title", params: { minutes: "2" }, resolution: "cleared" }, clock(0, 11, 20), 120),
  closed("h3", { ...at("de1"), severity: AlertSeverity.INFO, kind: AlertKind.HOST_BLIP, subject: "agent", titleKey: "health.alert.host_blip.title", actions: [] }, clock(1, 11, 21), 60),
  closed("h4", { ...at("nl1"), kind: AlertKind.CERT_EXPIRY, subject: "inb_cert", titleKey: "health.alert.cert_expiry.title", params: { inbound: "inb_cert", profile: "Hysteria2 · 443", days_left: "9" }, resolution: "fix_applied" }, clock(4, 10, 20), 20 * H),
];
// a longer week: many events and the same event on several nodes (the product's paged list and its folded episodes)
const many = Array.from({ length: 23 }, (_, i) =>
  closed(`hm${i}`, { ...at(FLEET[i % 4]!.n), severity: AlertSeverity.INFO, kind: AlertKind.HOST_BLIP, subject: "agent", titleKey: "health.alert.host_blip.title", actions: [] }, NOW - i * 6 * H - 600, 60 + i * 20),
);
const mtproto = (id: string, node: string, dt: number) => closed(id, { ...at(node), subject: "inb_mt", params: { profile: "MTProto · 443" }, actions: ["open_node"], resolution: "cleared" }, clock(0, 9, 30) + dt, 360);

/** What GetAlerts answers: the open ones and the closed ones of the week. */
export function alertsOf(f: Flags) {
  if (f.calm || f.first || f.nonodes) return { nowUnix: NOW, active: [] as ReturnType<typeof alert>[], history: f.first || f.nonodes ? [] : f.history ? many : [] };
  const msk = f.mskclean ? [] : [portMsk1];
  const nodes = f.crit ? [down, journal, russia, xhttp, ...msk] : [journal, russia, xhttp, ...portsNl1, ...msk];
  const grouped = f.grouped
    ? [alert({ id: "g1", ...at("de1"), subject: "inb_g1", titleKey: "health.alert.check_failed.title", whyKey: "health.alert.check_failed.why.timeout", params: { inbound: "inb_g1", profile: "Hysteria2 · 443", port: "443", online: "3" }, actions: ["restart_inbound", "open_node", "mute"] }), alert({ id: "g2", ...at("de1"), subject: "inb_g2", titleKey: "health.alert.check_failed.title", whyKey: "health.alert.check_failed.why.timeout", params: { inbound: "inb_g2", profile: "VLESS · TCP", port: "8443", online: "5" }, actions: ["restart_inbound", "open_node", "mute"] })]
    : [];
  const muted = f.muted ? [{ ...lossy("mu1", "de1", "8443", "250", { actions: ["open_profiles", "open_node", "mute"] }), mutedUntilUnix: NOW + 5 * H }] : [];
  const loss = f.loss ? [alert({ id: "pl", ...at("fi1"), kind: AlertKind.PATH_LOSSY, nodeId: nid("fi1"), subject: "nod_nl1", titleKey: "health.alert.path_lossy.title", whyKey: "health.alert.path_lossy.why", params: { node: "fi1", target: "nl1", target_id: "nod_nl1", ports: "2053, 2083", sent: "300", got: "236", other: "de1", other_got: "299", region: "cis", other_region: "other" } })] : [];
  const warp = f.warpreach ? [alert({ id: "wr", ...at("nl1"), kind: AlertKind.WARP_REACH, subject: "example.com", titleKey: "health.alert.warp_reach.title", whyKey: "health.alert.warp_reach.why", params: { domain: "example.com", colo: "AMS", http_status: "403", streak: "3" }, actions: ["open_warp", "mute"] })] : [];
  const hist = f.history ? [mtproto("e1", "de1", 33), mtproto("e2", "nl1", 21), mtproto("e3", "fi1", 9), mtproto("e4", "msk1", 0), ...many] : board;
  return { nowUnix: NOW, active: [...nodes, ...grouped, ...muted, ...loss, ...warp, bob, alice, limit], history: hist };
}

const history48 = (fail: boolean) => Array.from({ length: 48 }, (_, i) => ({ startUnix: NOW - (47 - i) * 1800, ok: fail && i > 40 ? 0 : 6, failed: fail && i > 40 ? 6 : 0, latencyMs: 30 + ((i * 7) % 25) }));
const okCell = (id: string, ms: number, connect = 0, exit = "203.0.113.200") => ({
  deployed: true,
  inboundId: id,
  failStreak: 0,
  history: history48(false),
  latencyHourMs: ms + 20,
  responseHourMs: ms,
  connectHourMs: connect,
  last: { status: CheckStatus.OK, atUnix: NOW - 120, latencyMs: ms + 20, responseMs: ms, connectMs: connect, exitIp: exit, exitCountry: "NL", errorCode: "", errorDetail: "" },
});
const badCell = (id: string) => ({
  deployed: true,
  inboundId: id,
  failStreak: 3,
  history: history48(true),
  latencyHourMs: 0,
  responseHourMs: 0,
  connectHourMs: 0,
  last: { status: CheckStatus.FAILED, atUnix: NOW - 120, latencyMs: 0, responseMs: 0, connectMs: 0, exitIp: "", exitCountry: "", errorCode: "timeout", errorDetail: "handshake timeout after 8 s" },
});
const noResult = (id: string) => ({ deployed: true, inboundId: id, failStreak: 0, history: [], latencyHourMs: 0, responseHourMs: 0, connectHourMs: 0 });
const offCell = (id: string) => ({ deployed: true, inboundId: id, failStreak: 0, history: [], latencyHourMs: 0, responseHourMs: 0, connectHourMs: 0, last: { status: CheckStatus.SKIPPED, atUnix: NOW - 120, latencyMs: 0, responseMs: 0, connectMs: 0, exitIp: "", exitCountry: "", errorCode: "node_offline", errorDetail: "" } });

const probe = (network: string, city: string, ms: number, ok: boolean) => ({ network, asn: 64496, city, icmpOk: ok, icmpMs: ms, tcp: [{ port: 443, ok, ms: ok ? 40 : 0 }], valid: true, dark: !ok, eyeball: true });
const country = (node: string, v: CountryVerdict, extra: Record<string, unknown> = {}) => ({
  nodeId: nid(node),
  country: "RU",
  verdict: v,
  atUnix: NOW - 9 * 60,
  probes: [probe("Operator A", "Moscow", 42, v === CountryVerdict.OK), probe("Operator B", "Kazan", 47, v === CountryVerdict.OK)],
  reason: "",
  eyeball: true,
  confidence: "eyeball",
  listedBy: [] as string[],
  cdnProvider: "",
  cdnRegion: "",
  sources: ["globalping"],
  globalpingVerdict: v,
  tspuVerdict: CountryVerdict.UNSPECIFIED,
  tspuPorts: [] as { port: number; verdict: string }[],
  dpiHop: 0,
  tspuVantageId: "",
  tspuVantageName: "",
  tspuAtUnix: 0,
  noProbe: [] as string[],
  vantages: [] as object[],
  sourceVerdicts: [] as object[],
  ...extra,
});

const vantage = (network: string, asn: string, state: string) => ({ source: "globalping", key: `AS${asn}`, network, region: "Moscow", asn, kind: "unknown", state, atUnix: NOW - 9 * 60, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
// fi1 is dark on two networks of Russia and seen by a third: the board's "unreachable from some networks"
const partial = {
  probes: [{ ...probe("Operator A", "Moscow", 0, false), asn: 64496, dark: true }, { ...probe("Operator B", "Kazan", 0, false), asn: 64497, dark: true }, { ...probe("Operator C", "Omsk", 44, true), asn: 64498 }],
  vantages: [vantage("Operator A", "64496", "dark"), vantage("Operator B", "64497", "dark"), vantage("Operator C", "64498", "reach")],
  sourceVerdicts: [{ source: "globalping", verdict: CountryVerdict.PARTIAL, eyeball: true, atUnix: NOW - 9 * 60, reason: "" }],
};

/** What GetChecks answers: a row a node, the two profiles of the board, the country column from Russia and the TSPU probe's run. */
export function checksOf(f: Flags) {
  const first = f.first;
  const out = f.off;
  const row = (n: string, cc: string, cells: object[]) => ({ nodeId: nid(n), nodeName: n, countryCode: cc, nodeStatus: out && n === "nl1" ? NodeStatus.DOWN : NodeStatus.ONLINE, cells });
  const c = (id: string, ms: number, connect = 0) => (first ? noResult(id) : okCell(id, ms, connect));
  const rows = [
    row("de1", "DE", [c("inb_vl_de1", 29), c("inb_hy_de1", 32)]),
    row("fi1", "FI", [first ? noResult("inb_vl_fi1") : badCell("inb_vl_fi1"), c("inb_hy_fi1", 57)]),
    row("nl1", "NL", out ? [offCell("inb_vl_nl1"), offCell("inb_hy_nl1")] : [c("inb_vl_nl1", 50), c("inb_hy_nl1", 81)]),
    row("msk1", "RU", [c("inb_vl_msk1", 40), c("inb_hy_msk1", 81)]),
  ];
  return {
    nowUnix: NOW,
    intervalS: 300,
    columns: [
      { profileId: "prf_vl", profileName: "VLESS · Reality", protocol: "vless" },
      { profileId: "prf_hy", profileName: "Hysteria2 · 8443", protocol: "hysteria2" },
    ],
    rows: f.nonodes ? [] : rows,
    countryChecks: first || f.edge || f.nonodes ? [] : [country("de1", CountryVerdict.OK), country("fi1", CountryVerdict.PARTIAL, partial), country("nl1", CountryVerdict.OK), country("msk1", CountryVerdict.OK)].filter((x) => !(out && x.nodeId === nid("nl1"))),
    tspuRun: first || f.edge ? undefined : { status: "ok", atUnix: NOW - 48 * 60, nodeId: nid("msk1"), dpiHop: 0, blockedRef: "", nodesChecked: 4, detail: "" },
    ...(f.loss ? { udpLoss: { nodeIds: [nid("de1"), nid("nl1")], sharePercent: 38, lostPermille: 31, nodeLostPermille: 55 } } : {}),
  };
}

const ids = ["disk_space", "journald_size", "dstate_tasks", "memory_pressure", "cpu_softirq", "cpu_steal", "agent_fds", "time_sync", "resolver", "ipv6", "port_conflicts", "conntrack_fill", "cert_expiry", "unit_generation"];
const detail: Record<string, string> = { disk_space: "used 10% of 40 GB", journald_size: "journal 48 MB", time_sync: "chrony, offset 3 ms", resolver: "192.0.2.53, 4 ms", cert_expiry: "70 days left", conntrack_fill: "1.2% of 262144" };
const item = (id: string, bad: boolean) => ({
  id,
  status: bad ? DoctorStatus.WARN : DoctorStatus.OK,
  titleKey: `doctor.${id}.title`,
  detail: bad ? "journal 3870 MB, cap 500 MB" : (detail[id] ?? ""),
  detailCode: "",
  params: bad ? { journal_mb: "3870", cap_mb: "500" } : ({} as Record<string, string>),
  whyKey: `health.doctor.${id}.why`,
  fixId: bad ? "journald_vacuum" : "",
  measuredUnix: NOW - 300,
  acceptedUnix: 0,
  acceptedBy: "",
  acceptedByName: "",
});
/** What GetDoctor answers: the report of each node, de1's journal the one finding (not before the first round, nor once it is fixed). */
export function doctorOf(f: Flags) {
  const found = !f.calm && !f.first && !f.nonodes;
  const node = (n: string, count: number, bad: boolean) => ({
    nodeId: nid(n),
    nodeName: n,
    nodeStatus: f.off && n === "nl1" ? NodeStatus.DOWN : NodeStatus.ONLINE,
    agentSupported: true,
    agentVersion: "0.1.71",
    hasReport: !f.first,
    receivedUnix: NOW - 300,
    lastSeenUnix: NOW - 30,
    ageS: 300,
    stale: false,
    items: f.first ? [] : ids.slice(0, count).map((id) => item(id, bad && id === "journald_size")),
  });
  return { nowUnix: NOW, nodes: f.nonodes ? [] : [node("de1", 14, found), node("fi1", 14, false), node("nl1", 13, false), node("msk1", 13, false)] };
}

/** The cards of the fleet the shell and the page's own "By node" read. */
export function overviewOf(f: Flags) {
  const alerts = alertsOf(f).active.filter((a) => a.nodeId && a.severity >= AlertSeverity.WARNING && a.mutedUntilUnix === 0);
  const broken = new Set(f.crit ? [nid("nl1")] : []);
  return {
    nodesTotal: f.nonodes ? 0 : FLEET.length,
    nodes: f.nonodes
      ? []
      : FLEET.map((n) => ({
          id: nid(n.n),
          name: n.n,
          countryCode: n.cc,
          location: n.city,
          status: broken.has(nid(n.n)) ? NodeStatus.DOWN : NodeStatus.ONLINE,
          online: [],
          sparkBytes: [],
          reason: f.nodeproblems && n.n === "msk1" ? { code: "no_profiles", params: {} } : undefined,
        })),
    alertsActive: alerts.length,
    alertsCritical: alerts.filter((a) => a.severity === AlertSeverity.CRITICAL).length,
  };
}
