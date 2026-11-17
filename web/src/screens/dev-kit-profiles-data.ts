import { App, InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";

// Development only. The made-up fleet and the profiles of the redesign boards (PnProfiles, PnProfile), shared by the scenes of the list
// and of the profile page: twelve nodes (de1, de2, de3, nl1, nl2, nl3, fi1, fi2, fi3, msk1, msk2, spb1; documentation addresses),
// ten profiles of the five protocols (the board's eleventh, the DNS tunnel, has no protocol in the product), two groups (family,
// friends). What the panel would answer to GetChecks, GetFleetConnections and ListRelays for them, in the state a scene asks for.

export const NOW = Math.floor(Date.now() / 1000);
export const nid = (n: string) => `nod_${n}`;

/** Node: name, country, city, what the country check says of it from Russia, and whether a relay enters through it. */
type NodeSpec = { n: string; cc: string; city: string; ru: CountryVerdict; entry?: boolean; ip: string };
export const NODES: NodeSpec[] = [
  { n: "de1", cc: "DE", city: "Frankfurt", ru: CountryVerdict.BLOCKED, ip: "203.0.113.7" },
  { n: "de2", cc: "DE", city: "Nuremberg", ru: CountryVerdict.BLOCKED, ip: "203.0.113.67" },
  { n: "de3", cc: "DE", city: "Falkenstein", ru: CountryVerdict.BLOCKED, ip: "203.0.113.88" },
  { n: "nl1", cc: "NL", city: "Amsterdam", ru: CountryVerdict.PARTIAL, ip: "203.0.113.41" },
  { n: "nl2", cc: "NL", city: "Amsterdam", ru: CountryVerdict.BLOCKED, ip: "203.0.113.74" },
  { n: "nl3", cc: "NL", city: "Rotterdam", ru: CountryVerdict.UNKNOWN, ip: "203.0.113.52" },
  { n: "fi1", cc: "FI", city: "Helsinki", ru: CountryVerdict.OK, ip: "203.0.113.15" },
  { n: "fi2", cc: "FI", city: "Helsinki", ru: CountryVerdict.OK, ip: "203.0.113.81" },
  { n: "fi3", cc: "FI", city: "Helsinki", ru: CountryVerdict.OK, ip: "203.0.113.93" },
  { n: "msk1", cc: "RU", city: "Moscow", ru: CountryVerdict.OK, entry: true, ip: "203.0.113.20" },
  { n: "msk2", cc: "RU", city: "Moscow", ru: CountryVerdict.OK, entry: true, ip: "203.0.113.95" },
  { n: "spb1", cc: "RU", city: "Saint Petersburg", ru: CountryVerdict.PARTIAL, entry: true, ip: "203.0.113.30" },
];

export type Sv = "ok" | "bad" | "start" | "off";
type ProfileSpec = {
  id: string;
  name: string;
  protocol: string;
  summary: string;
  port: number;
  sv: Record<string, Sv>;
  /** Rounds in a row that failed on a node, the server working otherwise. */
  rounds?: Record<string, number>;
  err?: Record<string, string>;
  groups: ("family" | "friends")[];
  people: number;
  warnings?: { code: string; params: Record<string, string> }[];
  /** What the formats leave out, as the server names it: [format index, note]. */
  drops?: [number, string][];
  relay?: Record<string, string>;
  warp?: boolean;
};

const SUBS = [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON];
export const PROFILES: ProfileSpec[] = [
  { id: "prf_hy1", name: "Main · 443", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", port: 443, sv: { de1: "ok", nl1: "ok", fi1: "bad" }, err: { fi1: "listen udp [::]:443: bind: address already in use" }, groups: ["family", "friends"], people: 23, warnings: [{ code: "inbound_failed", params: { node: "fi1", inbound: "inb_hy1_fi1", error: "address already in use" } }], relay: { de1: "msk1" } },
  { id: "prf_hy2", name: "Main · WARP", protocol: "hysteria2", summary: "UDP 8443 · Salamander · WARP", port: 8443, sv: { de1: "ok", nl1: "ok" }, groups: ["family", "friends"], people: 23, warp: true },
  { id: "prf_hy3", name: "Mihomo · Gecko", protocol: "hysteria2", summary: "UDP 2053 · Gecko · Self-signed", port: 2053, sv: {}, groups: ["friends"], people: 0, drops: [[0, "gecko"], [2, "gecko"], [3, "gecko"]] },
  { id: "prf_vl1", name: "VLESS · 443", protocol: "vless", summary: "XHTTP 443 · REALITY", port: 443, sv: { de1: "ok", nl1: "ok", fi1: "ok", fi2: "ok" }, groups: ["friends"], people: 18, drops: [[3, "xhttp"]], relay: { de1: "msk1" } },
  { id: "prf_vl2", name: "VLESS · TCP", protocol: "vless", summary: "TCP 8443 · Vision · REALITY", port: 8443, sv: { de1: "ok", de3: "ok", nl1: "ok", nl2: "start", fi1: "ok" }, rounds: { nl1: 2 }, groups: ["family", "friends"], people: 23 },
  { id: "prf_vl3", name: "VLESS · CDN", protocol: "vless", summary: "WebSocket 2083 · TLS", port: 2083, sv: { nl1: "ok" }, groups: [], people: 0 },
  { id: "prf_aw1", name: "AWG 3.1", protocol: "awg", summary: "UDP 47213 · AWG 3.1 · DNS", port: 47213, sv: { de1: "ok", nl1: "ok" }, groups: ["family"], people: 9, warnings: [{ code: "udp_blocked_on_node", params: { node: "nl1" } }], drops: [[0, "awg"], [2, "awg"], [3, "awg"]], relay: { de1: "msk1" } },
  { id: "prf_aw2", name: "AWG 2.0", protocol: "awg", summary: "UDP 33183 · AWG 2.0 · QUIC", port: 33183, sv: { de1: "ok", nl3: "off" }, groups: ["family"], people: 3, drops: [[0, "awg"], [2, "awg"], [3, "awg"]] },
  { id: "prf_tt1", name: "TrustTunnel · 443", protocol: "trusttunnel", summary: "TCP 443 · HTTP/2 · Let's Encrypt", port: 443, sv: { de1: "ok", nl1: "ok" }, groups: ["friends"], people: 4, drops: [[0, "unsupported"], [1, "unsupported"], [2, "unsupported"], [3, "unsupported"]] },
  { id: "prf_tg1", name: "Telegram · 8443", protocol: "mtproto", summary: "TCP 8443 · Fake TLS", port: 8443, sv: {}, groups: [], people: 0, drops: [[0, "unsupported"], [1, "unsupported"], [2, "unsupported"], [3, "unsupported"]] },
];

export const PROTOCOLS = [
  { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP], experimental: false },
  { id: "vless", displayName: "VLESS", apps: [App.HAPP], experimental: false },
  { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA], experimental: false },
  { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM], experimental: true },
  { id: "trusttunnel", displayName: "TrustTunnel", apps: [App.TRUST_TUNNEL], experimental: true },
];

export const GROUPS = [
  { id: "grp_family", name: "Family", color: "sand", userCount: 9, profileIds: PROFILES.filter((p) => p.groups.includes("family")).map((p) => p.id) },
  { id: "grp_friends", name: "Friends", color: "sky", userCount: 14, profileIds: PROFILES.filter((p) => p.groups.includes("friends")).map((p) => p.id) },
].map((g) => ({ ...g, happNodes: 4, amneziaNodes: 2, telegramNodes: 0, disabledNodeIds: [], dnsPresetId: "" }));

const hash = (s: string) => {
  let h = 7;
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 9973;
  return h;
};

/** What a scene changes in the fleet: one node out of touch (nl3), the checks that have not run yet. */
export type Fleet = { out?: boolean; noChecks?: boolean; count?: number };

export const summaries = (n = PROFILES.length) =>
  PROFILES.slice(0, n).map((p) => ({
    id: p.id,
    name: p.name,
    protocol: p.protocol,
    summary: p.summary,
    nodeCount: Object.keys(p.sv).length,
    userCount: p.people,
    warnings: p.warnings ?? [],
    version: 1,
    formats: SUBS.map((format, i) => {
      const d = p.drops?.find((x) => x[0] === i);
      return { format, ok: !d, note: d?.[1] ?? "" };
    }),
  }));

const history = (bad: number, part: number, none = false) =>
  Array.from({ length: 48 }, (_, k) => {
    const from = 48 - k;
    return { startUnix: NOW - from * 1800, ok: none ? 0 : from <= bad ? 0 : from <= part ? 3 : 6, failed: none ? 0 : from <= bad ? 4 : from <= part ? 2 : 0, latencyMs: none ? 0 : 60 + (hash(`${k}`) % 40) };
  });

/** GetChecks: a column per profile, a row per node, the cell of a server by what the profile's `sv` says of it. */
export function checksOf(f: Fleet = {}) {
  const profiles = PROFILES.slice(0, f.count ?? PROFILES.length);
  return {
    nowUnix: NOW,
    intervalS: 300,
    columns: profiles.map((p) => ({ profileId: p.id, profileName: p.name, protocol: p.protocol })),
    rows: NODES.map((n) => ({
      nodeId: nid(n.n),
      nodeName: n.n,
      countryCode: n.cc,
      nodeStatus: f.out && n.n === "nl3" ? NodeStatus.DOWN : NodeStatus.ONLINE,
      cells: profiles.map((p) => {
        const st = p.sv[n.n];
        if (!st) return { deployed: false, inboundId: "", failStreak: 0, history: [] };
        const inboundId = `inb_${p.id.replace("prf_", "")}_${n.n}`;
        const ms = 90 + (hash(p.id + n.n) % 170);
        const rounds = p.rounds?.[n.n] ?? 0;
        const last = f.noChecks
          ? undefined
          : st === "bad" || st === "start"
            ? { status: CheckStatus.SKIPPED, atUnix: NOW - 240, latencyMs: 0, responseMs: 0, errorCode: "inbound_not_active", errorDetail: "the profile is not running on the node" }
            : st === "off"
              ? { status: CheckStatus.SKIPPED, atUnix: NOW - 240, latencyMs: 0, responseMs: 0, errorCode: "inbound_disabled", errorDetail: "" }
              : rounds
                ? { status: CheckStatus.FAILED, atUnix: NOW - 240, latencyMs: 0, responseMs: 0, errorCode: "timeout", errorDetail: "handshake timeout after 8s" }
                : { status: CheckStatus.OK, atUnix: NOW - 240, latencyMs: ms + 60, responseMs: ms, errorCode: "", errorDetail: "" };
        return {
          deployed: true,
          inboundId,
          last,
          failStreak: rounds,
          history: f.noChecks ? history(0, 0, true) : history(st === "bad" ? 8 : 0, rounds ? 2 : 0),
          responseHourMs: f.noChecks || st !== "ok" || rounds ? 0 : ms,
          latencyHourMs: f.noChecks || st !== "ok" || rounds ? 0 : ms + 60,
          connectHourMs: 0,
        };
      }),
    })),
    countryChecks: NODES.map((n) => ({
      nodeId: nid(n.n), country: "RU", verdict: n.ru, atUnix: NOW - 600, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
      globalpingVerdict: n.ru, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
      vantages: [], sourceVerdicts: [],
    })),
  };
}

/** GetFleetConnections: the people who connected over the last day, by server. */
export function connectionsOf() {
  return {
    sinceUnix: NOW - 86400,
    nowUnix: NOW,
    nodes: NODES.map((n) => ({
      nodeId: nid(n.n),
      nodeName: n.n,
      countryCode: n.cc,
      supported: true,
      inbounds: PROFILES.filter((p) => p.sv[n.n] === "ok").map((p) => ({ inboundId: `inb_${p.id.replace("prf_", "")}_${n.n}`, profileId: p.id, profileName: p.name, protocol: p.protocol, connects: 2 + (hash(n.n + p.id) % 42), rejects: 0, drops: 0, stalls: 0, onlineS: 0, unknownKeys: 0, hellos: 0, handshakes: 0, keysStalled: 0, keysTried: 0, unsupported: ["hellos", "handshakes"], hours: [], clients: [] })),
    })),
  };
}

/** ListRelays: the board's relays, each leaves a server through a Russian entry. */
export function relaysOf() {
  return PROFILES.flatMap((p) =>
    Object.entries(p.relay ?? {}).map(([exit, via]) => ({
      id: `rel_${p.id}_${exit}`, inboundId: `inb_${p.id.replace("prf_", "")}_${exit}`, exitNodeId: nid(exit), exitNodeName: exit, profileId: p.id, profileName: p.name, protocol: p.protocol, viaNodeId: nid(via), viaNodeName: via,
      network: p.protocol === "hysteria2" || p.protocol === "awg" ? "udp" : "tcp", port: p.port, exitPort: p.port, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.40", appliedUnix: NOW - 3600, exitLive: true, agentSupported: true,
      createdUnix: NOW - 7200, bytes: 0, packets: 0, countedUnix: 0,
    })),
  );
}

export const nodeRows = () =>
  NODES.map((n) => ({
    id: nid(n.n), name: n.n, countryCode: n.cc, location: n.city, provider: "Example Cloud", notes: "", address: n.ip, status: NodeStatus.ONLINE, protocols: [], online: [], trafficTodayBytes: 0, hasMetrics: false, cpuPct: 0, uptimeS: 0, lastSeenUnix: NOW,
  }));
