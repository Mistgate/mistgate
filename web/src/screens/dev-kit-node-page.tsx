import { QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CheckStatus, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { useLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { checksQuery, doctorQuery } from "@/lib/health";
import { nodeQuery, nodesQuery, overviewQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { seeded } from "./dev-kit-overview";
import { Shot } from "./dev-kit-redesign";
import { ShellLayout } from "./shell";
import { NodeDetail } from "./node";
import { hostHistoryQuery } from "./node/load-history";
import { nodeAlertsQuery } from "./node/overview-parts";
import { OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { WarpSource } from "@/gen/mistgate/admin/v1/common_pb";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { LogLevel } from "@/gen/mistgate/admin/v1/node_pb";
import { fleet as fleetApi, nodes as nodesApi, warp as warpApi } from "@/lib/api";
import { warpQuery } from "./node/warp-model";
import { nodeDnsOptionsQuery } from "./node/dns-options";
import { dnsPresetsQuery } from "./subscriptions/queries";
import { debugListQuery } from "@/lib/debug-capture";
import { osUpdatesQuery } from "@/lib/os-updates";

// Development only. /dev-kit?only=nodepage: the node page of the redesign, a tab at a time (?tab=overview|profiles|server|doctor|...),
// in every state of the board's switch (load low / high / at the limit, alerts none / a few / many, the node answering / silent / new)
// and the states of the product the board does not draw (retired, read-only role, the Cloudflare edition, the agent behind an entry),
// each in a frame of its own width (an iframe, so the window's breakpoints answer to the frame), inside the real shell. Each frame is
// a page of its own: /dev-kit?only=nodepage-frame&tab=<tab>&state=<state>
//   calm | alerts | many | high | limit | down | new | retired | viewer | edge | via | entry
// `fx=1` turns the reveal animation on. Made-up data: nodes de1, nl1, fi1, msk1, people alice, dave, bob, a panel called owner.

const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86_400;
const GB = 1e9;

export type NState = "calm" | "alerts" | "many" | "high" | "limit" | "down" | "new" | "retired" | "viewer" | "edge" | "via" | "entry";
type Spec = { load: 0 | 1 | 2; alerts: number; down?: boolean; fresh?: boolean; retired?: boolean; role?: Role; edge?: boolean; via?: boolean; entry?: boolean };
const specs: Record<NState, Spec> = {
  calm: { load: 0, alerts: 0 },
  alerts: { load: 0, alerts: 3 },
  many: { load: 1, alerts: 6 },
  high: { load: 1, alerts: 1 },
  limit: { load: 2, alerts: 2 },
  down: { load: 0, alerts: 1, down: true },
  new: { load: 0, alerts: 0, fresh: true },
  retired: { load: 0, alerts: 0, retired: true },
  viewer: { load: 0, alerts: 3, role: Role.READONLY },
  edge: { load: 0, alerts: 1, edge: true },
  via: { load: 0, alerts: 1, via: true },
  entry: { load: 0, alerts: 0, entry: true },
};
export const nodeStates = Object.keys(specs) as NState[];

const rnd = (k: number) => {
  const x = Math.sin(k * 127.1 + 311.7) * 43758.5453;
  return x - Math.floor(x);
};
const day = (hr: number) => 0.16 + 0.55 * Math.max(0, Math.cos(((hr - 21) / 24) * 2 * Math.PI)) ** 2 + 0.3 * Math.max(0, Math.cos(((hr - 13) / 24) * 2 * Math.PI)) ** 3;

const alertOf = (over: Record<string, unknown>) => ({
  id: `alt_${Math.random().toString(36).slice(2, 8)}`,
  severity: AlertSeverity.WARNING,
  kind: AlertKind.DOCTOR_WARN,
  nodeId: "nod_de1",
  nodeName: "de1",
  subject: "",
  titleKey: "health.alert.doctor_warn.title",
  params: {},
  whyKey: "",
  firstSeenUnix: NOW - 1800,
  openedUnix: NOW - 1800,
  lastSeenUnix: NOW,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["open_node", "mute"],
  ...over,
});

/** The open alerts of the board's pool for this many warnings. */
function alertPool(n: number) {
  const pool = [
    alertOf({ subject: "journald_size", whyKey: "health.doctor.journald_size.why", params: { check: "journald_size", journal_mb: "3891", cap_mb: "500" }, actions: ["apply_fix:journald_vacuum", "open_node", "accept", "mute"], openedUnix: NOW - 4 * H }),
    alertOf({ kind: AlertKind.CERT_EXPIRY, subject: "inb_1", titleKey: "health.alert.cert_expiry.title", whyKey: "health.alert.cert_expiry.why", params: { profile: "hy2 · 443", days_left: "9", server_part: "" }, actions: ["open_profiles", "mute"], openedUnix: NOW - 2 * D }),
    alertOf({ kind: AlertKind.CHECK_FAILED, severity: AlertSeverity.CRITICAL, subject: "inb_2", titleKey: "health.alert.check_failed.title", params: { profile: "vless · 443", inbound: "inb_2", port: "443" }, actions: ["restart_inbound", "open_node", "mute"], openedUnix: NOW - 25 * 60 }),
    alertOf({ subject: "unit_generation", params: { check: "unit_generation" }, actions: ["apply_fix:refresh_unit", "open_node", "mute"], openedUnix: NOW - 5 * H }),
    alertOf({ kind: AlertKind.CERT_EXPIRY, subject: "inb_3", titleKey: "health.alert.cert_expiry.title", whyKey: "health.alert.cert_expiry.why", params: { profile: "awg · 51820", days_left: "12", server_part: "" }, actions: ["open_profiles", "mute"], openedUnix: NOW - 3 * D }),
    alertOf({ subject: "time_sync", params: { check: "time_sync" }, actions: ["open_node", "accept", "mute"], openedUnix: NOW - 9 * H }),
  ];
  return pool.slice(0, n);
}


// the doctor's report of the board: fourteen checks of the host; a state decides which of them are not fine
const checkIds = ["disk_space", "journald_size", "dstate_tasks", "memory_pressure", "cpu_softirq", "cpu_steal", "agent_fds", "time_sync", "resolver", "ipv6", "port_conflicts", "conntrack_fill", "cert_expiry", "unit_generation"];
const details: Record<string, string> = { disk_space: "used 10% of 40 GB", journald_size: "journal 48 MB", time_sync: "chrony, offset 3 ms", resolver: "1.1.1.1, 4 ms", cert_expiry: "70 days left", conntrack_fill: "1.2% of 262144" };
function doctorItems(state: NState) {
  const bad = (id: string) => (state === "limit" ? ["disk_space", "journald_size", "time_sync"].includes(id) : state === "alerts" || state === "many" || state === "viewer" || state === "high" ? ["journald_size", "time_sync"].includes(id) : state === "down" ? id === "time_sync" : false);
  const crit = (id: string) => state === "limit" && id === "disk_space";
  return checkIds.map((id) => ({
    id,
    status: crit(id) ? DoctorStatus.FAIL : bad(id) || (state === "calm" && id === "cpu_steal") ? DoctorStatus.WARN : id === "dstate_tasks" && state === "down" ? DoctorStatus.SKIP : DoctorStatus.OK,
    titleKey: `doctor.${id}.title`,
    detail: details[id] ?? "",
    detailCode: "",
    params: id === "journald_size" ? { journal_mb: "3891", cap_mb: "500" } : id === "disk_space" ? { used_pct: "88", free_gb: "4.8" } : {},
    whyKey: `health.doctor.${id}.why`,
    fixId: id === "journald_size" && bad(id) ? "journald_vacuum" : "",
    measuredUnix: NOW - 300,
    acceptedUnix: state === "calm" && id === "cpu_steal" ? NOW - 2 * D : 0,
    acceptedBy: "adm_1",
    acceptedByName: "owner",
  }));
}

const closed = [
  { ...alertOf({ resolvedAtUnix: NOW - 3 * H, openedUnix: NOW - 3 * H - 2 * 60, firstSeenUnix: NOW - 3 * H - 2 * 60, resolution: "cleared", kind: AlertKind.NODE_DOWN, titleKey: "health.alert.node_down.title", params: { minutes: "2" } }) },
  { ...alertOf({ resolvedAtUnix: NOW - 5 * H, openedUnix: NOW - 5 * H - 60, firstSeenUnix: NOW - 5 * H - 60, resolution: "cleared", kind: AlertKind.NODE_DOWN, titleKey: "health.alert.node_down.title", params: { minutes: "1" } }) },
  { ...alertOf({ resolvedAtUnix: NOW - 6 * D, openedUnix: NOW - 6 * D - 60, firstSeenUnix: NOW - 6 * D - 60, resolution: "cleared", kind: AlertKind.NODE_DOWN, titleKey: "health.alert.node_down.title", params: { minutes: "1" } }) },
];

const history48 = (fail: (i: number) => boolean) => Array.from({ length: 48 }, (_, i) => ({ startUnix: NOW - (47 - i) * 1800, ok: fail(i) ? 0 : 6, failed: fail(i) ? 6 : 0, latencyMs: 28 + Math.round(rnd(i) * 20) }));
const cell = (id: string, ms: number, fail: (i: number) => boolean = () => false, status = CheckStatus.OK) => ({
  deployed: true,
  inboundId: id,
  failStreak: status === CheckStatus.FAILED ? 3 : 0,
  history: history48(fail),
  latencyHourMs: ms,
  responseHourMs: ms,
  connectHourMs: ms,
  last: { status, atUnix: NOW - 120, latencyMs: ms, responseMs: ms, connectMs: ms, exitIp: "", exitCountry: "", errorCode: status === CheckStatus.FAILED ? "timeout" : "", errorDetail: "" },
});

const columns = [
  { profileId: "prf_hy", profileName: "Hysteria2 · 443", protocol: "hysteria2" },
  { profileId: "prf_vl", profileName: "VLESS · Reality", protocol: "vless" },
  { profileId: "prf_aw", profileName: "AmneziaWG 3.1", protocol: "amneziawg" },
];

/** What the Server tab's system card reads: fine, security updates waiting, or a reboot due (?os=ok|upd|boot; by default a state's own). */
export type OsKind = "ok" | "upd" | "boot";
const osOf: Record<NState, OsKind> = { calm: "ok", alerts: "upd", many: "boot", high: "ok", limit: "boot", down: "ok", new: "ok", retired: "ok", viewer: "upd", edge: "ok", via: "ok", entry: "ok" };

/** The node page's data (what GetNode says) and the query cache around it, for a state. */
/**
 * The History tab's calls, answered here: the node's events (the board's list: an outage, a blip, a certificate, an update, a restart),
 * the journal of warnings, and an agent log that grows a line every second or so. The state decides what the node has been through.
 */
function fakeHistory(state: NState) {
  const fresh = state === "new";
  const down = state === "down";
  const e = (id: number, ago: number, code: string, params: Record<string, string> = {}, over: Record<string, unknown> = {}) => ({
    id: BigInt(id), timeUnix: BigInt(NOW - ago), severity: EventSeverity.INFO, code, params, nodeId: "nod_de1", nodeName: "de1", userId: "", userName: "", inboundId: "", profileName: "", protocol: "", source: "agent", ...over,
  });
  const warn = { severity: EventSeverity.WARNING };
  const events = fresh
    ? [e(1, 600, "node_enrolled", {}, { source: "panel" })]
    : [
        ...(down ? [e(30, 14 * 60, "node_down", { minutes: "14" }, { ...warn, source: "panel" })] : []),
        e(20, 7 * H, "config_applied", { revision: "418" }),
        e(19, 9 * H + 600, "node_down", { minutes: "2" }, { ...warn, source: "panel" }),
        e(18, 9 * H + 480, "node_recovered", {}, { source: "panel" }),
        e(17, 10 * H, "node_blip", {}, { ...warn, source: "panel" }),
        e(16, 11 * H, "cert_renewed", { domain: "de1.example.com" }),
        e(15, 26 * H, "agent_started", { version: "0.1.71", prev_version: "0.1.70", reason: "update" }),
        e(14, 30 * H, "engine_restarted", { protocol: "hysteria2", reason: "restart_command" }, { source: "admin", profileName: "Hysteria2 · 443", inboundId: "inb_1", protocol: "hysteria2" }),
        e(13, 32 * H, "profile_added", { protocol: "awg" }, { source: "admin", profileName: "AmneziaWG 3.1", inboundId: "inb_3", protocol: "awg" }),
        e(12, 6 * D, "node_blip", {}, { ...warn, source: "panel" }),
      ];
  (fleetApi as unknown as Record<string, unknown>).listEvents = async (req: { family?: string; minSeverity?: number; limit?: number }) => ({
    events: events.filter((x) => (!req.minSeverity || x.severity >= req.minSeverity) && (req.family !== "agent" || !x.code.startsWith("engine_") && !x.code.startsWith("profile_"))).slice(0, req.limit ?? 50),
    hasMore: false,
  });
  const entry = (ago: number, level: number, source: string, message: string, count: number, attrs: Record<string, string> = {}) => ({ nodeId: "nod_de1", hourStartUnix: BigInt(NOW - (NOW % H) - ago), lastUnix: BigInt(NOW - ago), level, source, message, inboundId: "", profileName: "", count, attrs });
  (nodesApi as unknown as Record<string, unknown>).getNodeJournal = async () => ({
    entries: fresh ? [] : [entry(1200, EventSeverity.ERROR, "xray", "failed to dial upstream: i/o timeout", 6, { code: "dial_timeout" }), entry(3 * H, EventSeverity.WARNING, "agent", "journald size above the limit", 1, { journal_mb: "3891", cap_mb: "500" }), entry(5 * H, EventSeverity.WARNING, "hysteria", "client rate limited", 14, { user: "bob" })],
    truncated: false,
    nodesWithoutJournal: [],
  });
  const lines = [
    [LogLevel.INFO, "agent", "heartbeat sent seq=48211 rtt=31ms"], [LogLevel.INFO, "agent", "probe hysteria2 ok 31ms"], [LogLevel.INFO, "xray", "accepted tcp reality from 203.0.113.50"],
    [LogLevel.WARNING, "agent", "journald size above limit 3870MB > 500MB"], [LogLevel.INFO, "agent", "probe awg-3.1 ok 34ms"], [LogLevel.ERROR, "agent", "port check tcp/80 refused from panel"],
    [LogLevel.INFO, "hysteria", "client connected id=alice addr=203.0.113.50"], [LogLevel.WARNING, "hysteria", "client rate limited id=bob"], [LogLevel.INFO, "agent", "stats pushed online=3 rx=64Mbit tx=67Mbit"],
  ] as const;
  const line = (i: number, ago: number) => ({ timeUnixMs: BigInt(Date.now() - ago), level: lines[i % lines.length]![0], source: lines[i % lines.length]![1], message: lines[i % lines.length]![2] });
  (nodesApi as unknown as Record<string, unknown>).streamLogs = async function* (_req: unknown, opts?: { signal?: AbortSignal }) {
    yield { lines: Array.from({ length: 12 }, (_, i) => line(i, (12 - i) * 3000)), eof: false, error: "" };
    for (let i = 12; !opts?.signal?.aborted; i++) {
      await new Promise((r) => setTimeout(r, 1100));
      yield { lines: [line(i, 0)], eof: false, error: "" };
    }
  };
}

export type WarpKind = "ok" | "pause" | "bad" | "none" | "revoked";

/** The WARP card of the board's states: working, paused, not answering, none connected (the registration then plays on a press), revoked. */
function warpOf(kind: WarpKind) {
  const shake = kind === "ok" ? NOW - 20 : NOW - 3 * H;
  return {
    account: kind === "none" ? undefined : { enabled: kind !== "pause", source: WarpSource.REGISTERED, accountType: "free", hasToken: true, useReserved: true, endpointV4: "198.51.100.1" },
    health:
      kind === "none"
        ? undefined
        : {
            state: kind === "ok" ? WarpState.UP : kind === "pause" ? WarpState.DISABLED : WarpState.DOWN,
            backend: "wireguard-go", endpoint: "198.51.100.1:2408", lastHandshakeUnix: shake, warpFlag: "on", colo: "FRA", probeCloudflareOk: kind === "ok", probeOtherOk: kind === "ok",
            probeCloudflare: kind === "ok" ? { ok: true, latencyMs: 70, atUnix: NOW - 40 } : { ok: false, latencyMs: 0, atUnix: NOW - 40, errorCode: "timeout" },
            probeOther: kind === "ok" ? { ok: true, latencyMs: 90, atUnix: NOW - 40 } : { ok: false, latencyMs: 0, atUnix: NOW - 40, errorCode: "timeout" },
            consecutiveFailures: kind === "bad" || kind === "revoked" ? 12 : 0, rxBytes: 19.8 * GB, txBytes: 3.1 * GB, lastError: kind === "bad" ? "handshake_stale" : kind === "revoked" ? "warp_flag_off" : "", reportedUnix: NOW - 5, checkedUnix: NOW - 40,
          },
    pendingApply: false, applyError: "", agentSupports: true, tosUrl: "https://www.cloudflare.com/application/terms/", needsAttention: kind === "revoked", attentionReason: kind === "revoked" ? "revoked" : "",
    inbounds: kind === "none" ? [] : [{ inboundId: "inb_3", profileName: "AmneziaWG 3.1", online: 1 }],
  };
}

export function nodePageData(state: NState, extra: { os?: OsKind; dbg?: "rec" | "done"; wp?: WarpKind } = {}) {
  const sp = specs[state];
  const lo = sp.load;
  const status = sp.retired ? NodeStatus.RETIRED : sp.down ? NodeStatus.DOWN : sp.fresh ? NodeStatus.ONLINE : NodeStatus.ONLINE;
  const people = sp.down || sp.fresh ? 0 : 2;
  const cpu = [9, 64, 93][lo]!;
  const ram = [23, 71, 92][lo]!;
  const disk = [10, 58, 88][lo]!;
  const rx = [64, 318, 466][lo]! * 1e6;
  const tx = [67, 322, 474][lo]! * 1e6;
  const metrics = {
    cpuPct: cpu,
    softirqPct: [4.1, 41, 58][lo]!,
    load1: [0.31, 1.42, 3.87][lo]!,
    stealPct: [0.1, 1.8, 9][lo]!,
    iowaitPct: 0.2,
    ramUsedBytes: (ram / 100) * 4 * 2 ** 30,
    ramTotalBytes: 4 * 2 ** 30,
    swapUsedBytes: [0, 48, 410][lo]! * 2 ** 20,
    swapTotalBytes: 512 * 2 ** 20,
    diskUsedBytes: (disk / 100) * 40 * 2 ** 30,
    diskTotalBytes: 40 * 2 ** 30,
    netRxBps: rx,
    netTxBps: tx,
    nicSpeedMbps: 1000,
    conntrackCount: [3120, 48200, 231000][lo]!,
    conntrackMax: 262144,
    tcpSockets: [9, 96, 310][lo]!,
    udpSockets: [7, 41, 122][lo]!,
    agent: { cpuPct: [0.6, 6.2, 9.4][lo]!, rssBytes: [75, 212, 340][lo]! * 1e6, goTotalBytes: 90e6, goHeapBytes: 4.9e6, goroutines: 42, threads: 13, openFds: [215, 1840, 4100][lo]!, fdLimit: 65536 },
    atUnix: sp.down ? NOW - 14 * 60 : NOW - 5,
  };
  const node = {
    id: "nod_de1",
    name: "de1",
    countryCode: "DE",
    location: "Frankfurt",
    publicLabel: "Fast route",
    provider: "Example Cloud",
    address: "203.0.113.7",
    status,
    protocols: ["hysteria2", "vless", "amneziawg"],
    online: sp.down || sp.fresh ? [] : [{ protocol: "hysteria2", users: 1 }, { protocol: "amneziawg", users: 1 }],
    agentVersion: "0.1.71",
    lastSeenUnix: sp.down ? NOW - 14 * 60 : NOW - 5,
    awgBackend: "auto",
    bandwidthMbps: 1000,
    decoyMode: "builtin",
    decoySupported: true,
    decoyReasonCode: "",
    decoyBundleSha256: "",
    decoyPanelAvailable: true,
    decoyOwnAvailable: true,
    decoyAgentMode: "builtin",
    decoyAgentSha256: "",
    warp: { state: WarpState.UP, source: 1, colo: "FRA", accountType: "free" },
    linkViaNodeId: sp.via ? "nod_msk1" : "",
    reason: sp.down ? { code: "agent_silent", params: { minutes: "14" } } : undefined,
  };
  const inbound = (id: string, protocol: string, name: string, port: number, istate = InboundState.ACTIVE) => ({
    id, profileId: `prf_${id}`, profileName: name, protocol, nodeId: "nod_de1", nodeName: "de1", state: istate, port, tlsServerName: "de1.example.com", certNotAfterUnix: NOW + 70 * D, certPinSha256: "", lastError: "", egress: "direct", enabled: true, portOverride: 0, directInSubscriptions: true, cdn: false,
  });
  const broken = state === "many";
  const inbounds = sp.fresh
    ? []
    : [
        inbound("inb_1", "hysteria2", "Hysteria2 · 443", 443),
        { ...inbound("inb_2", "vless", "VLESS · Reality", 443, broken ? InboundState.FAILED : InboundState.ACTIVE), lastError: broken ? "listen tcp :443: bind: address already in use" : "", certNotAfterUnix: 0 },
        { ...inbound("inb_3", "amneziawg", "AmneziaWG 3.1", 51820), certNotAfterUnix: 0, tlsServerName: "", egress: "warp", awg: { backend: "kernel", backendVersion: "amneziawg kernel module", ifaceUp: true, peers: 3, peersOnline: 1, peersHandshaken: 2, newestHandshakeUnix: NOW - 70, udpRxPackets: 900 } },
      ];
  const data = {
    node,
    metrics: sp.fresh ? undefined : metrics,
    facts: { hostname: "de1", os: "Ubuntu 24.04", kernel: "6.8.0-45", arch: "amd64", cpuCount: 2, ramTotalBytes: 4 * 2 ** 30, diskTotalBytes: 40 * 2 ** 30, virt: "kvm", hasIpv6: true, bootUnix: NOW - 31 * D, engines: ["hysteria2: hysteria core v2.6.1"] },
    inbounds,
    onlineUsers: people ? [{ userId: "usr_alice", userName: "alice", protocol: "hysteria2", deviceModel: "Desktop-Alice", connectedAtUnix: NOW - 3600, downBps: 31e6 }, { userId: "usr_alice", userName: "alice", protocol: "hysteria2", deviceModel: "iPad", connectedAtUnix: NOW - 3000, downBps: 31e6 }, { userId: "usr_dave", userName: "dave", protocol: "amneziawg", deviceModel: "Pixel 9", connectedAtUnix: NOW - 900, downBps: 2e6 }] : [],
    topToday: sp.fresh ? [] : [{ userId: "usr_alice", userName: "alice", bytes: 5.0 * GB }, { userId: "usr_dave", userName: "dave", bytes: 1.2 * GB }, { userId: "usr_bob", userName: "bob", bytes: 322e6 }],
    notes: "",
    dnsResolvers: [],
    createdUnix: NOW - 40 * D,
    enrollmentExpiresUnix: 0,
    portChecks: [],
    tcpChecks: [],
  };

  const qc = seeded("calm");
  const hostHours = (n: number) =>
    Array.from({ length: n }, (_, i) => {
      const hrOf = new Date((NOW - (n - 1 - i) * H) * 1000).getHours();
      const d = day(hrOf) * (0.85 + rnd(i) * 0.3);
      const base = [0.3, 1, 1.4][lo]!;
      return { hourStartUnix: NOW - (NOW % H) - (n - 1 - i) * H, samples: 360, cpuAvgPct: Math.min(99, Math.max(2, d * 22 * base + 4)), cpuMaxPct: Math.min(100, d * 30 * base + 12), sessionsAvg: d * 2.4, sessionsMax: Math.ceil(d * 3.2), extSamples: 360, stealAvgPct: [0.1, 1.8, 6][lo]!, stealMaxPct: [0.4, 3, 9][lo]!, agentCpuAvgPct: [0.5, 5, 8][lo]!, agentCpuMaxPct: [1, 8, 12][lo]! };
    });
  const live = !sp.retired;
  const failing = state === "many" || state === "limit" || state === "alerts";
  qc.setQueryData(nodeQuery("nod_de1").queryKey, data as never);
  qc.setQueryData(nodeAlertsQuery("nod_de1").queryKey, { nowUnix: NOW, active: sp.fresh ? [] : alertPool(sp.alerts), history: sp.fresh ? [] : closed } as never);
  qc.setQueryData(
    checksQuery("nod_de1").queryKey,
    {
      nowUnix: NOW,
      intervalS: 300,
      columns: sp.fresh ? [] : columns,
      rows: sp.fresh
        ? []
        : [
            {
              nodeId: "nod_de1",
              nodeName: "de1",
              countryCode: "DE",
              nodeStatus: status,
              cells: [cell("inb_1", 31), cell("inb_2", 29, (i) => (failing ? i > 38 : false), failing ? CheckStatus.FAILED : CheckStatus.OK), cell("inb_3", 34)],
            },
          ],
      countryChecks: [],
    } as never,
  );
  qc.setQueryData(checksQuery("").queryKey, { nowUnix: NOW, intervalS: 300, columns, rows: [], countryChecks: [] } as never);
  qc.setQueryData(doctorQuery("nod_de1").queryKey, { nowUnix: NOW, nodes: [{ nodeId: "nod_de1", nodeName: "de1", nodeStatus: status, agentSupported: true, hasReport: !sp.fresh, receivedUnix: sp.down ? NOW - 14 * 60 : NOW - 5 * 60, ageS: sp.down ? 14 * 60 : 5 * 60, stale: false, items: sp.fresh ? [] : doctorItems(state) }] } as never);
  qc.setQueryData(overviewQuery().queryKey, { nowUnix: NOW, nodes: [{ id: "nod_de1", name: "de1", sparkBytes: Array.from({ length: 24 }, (_, i) => Math.round((0.4 + Math.sin(i / 3.2) * 0.3 + (i > 17 ? 0.5 : 0)) * 2.1e9)) }], traffic: [], online: [], events: [], topConsumers: [] } as never);
  qc.setQueryData(overviewQuery(OverviewRange.OVERVIEW_RANGE_24H).queryKey, qc.getQueryData(overviewQuery().queryKey) as never);
  if (live && !sp.fresh && !sp.edge) {
    qc.setQueryData(hostHistoryQuery("nod_de1", 24).queryKey, { nodeId: "nod_de1", nodeName: "de1", nowUnix: NOW, sinceUnix: NOW - 31 * D, hours: hostHours(24) } as never);
    qc.setQueryData(hostHistoryQuery("nod_de1", 168).queryKey, { nodeId: "nod_de1", nodeName: "de1", nowUnix: NOW, sinceUnix: NOW - 31 * D, hours: hostHours(168) } as never);
  }
  if (sp.fresh) qc.setQueryData(hostHistoryQuery("nod_de1", 24).queryKey, { nodeId: "nod_de1", nodeName: "de1", nowUnix: NOW, sinceUnix: NOW - 300, hours: [] } as never);
  qc.setQueryData(relaysQuery("nod_de1").queryKey, (sp.entry ? [] : []) as never);
  // the Exit tab: the WARP account, the DNS presets of the people's page
  const wp = extra.wp ?? "ok";
  qc.setQueryData(warpQuery("nod_de1").queryKey, warpOf(wp) as never);
  const dnsPreset = (id: string, name: string, over: object = {}) => ({ id, name, servers: [], split: [], splitDirect: false, isDefault: false, blockAds: false, ...over });
  qc.setQueryData(dnsPresetsQuery.queryKey, { presets: [dnsPreset("dns_cf", "Cloudflare"), dnsPreset("dns_q9", "Quad9"), dnsPreset("dns_ag", "AdGuard", { blockAds: true }), dnsPreset("dns_fm", "Family"), dnsPreset("dns_ru", "Russia: Yandex + split")], providers: [], clientSupport: [] } as never);
  qc.setQueryData(nodeDnsOptionsQuery.queryKey, [{ nodeId: "nod_de1", presetIds: ["dns_cf", "dns_q9", "dns_ag"], defaultPresetId: "dns_cf" }] as never);
  // the Server tab: the SSH access and its keys, the system of the node and a debug recording
  const osKind = extra.os ?? osOf[state];
  const fp = "SHA256:Qm4Vw7dJ1xN8cR2tY5pL0sW9hD4gF6aJ8NvK2Ws";
  qc.setQueryData(["node-server-access", "nod_de1"], {
    nodeId: "nod_de1", nodeName: "de1", host: "203.0.113.7", port: 22, username: "root", fingerprint: fp, configuredUnix: NOW - 12 * D, passwordSaved: true, passwordLogin: true, passwordGenerated: true,
    panelKeyFingerprint: "SHA256:Zp8Lr1NvTc5KJd3Fq7YwB2mXa9RbE1c", nodeRetired: !!sp.retired, rotationPending: false,
  } as never);
  const key = (type: string, bits: number, f: string, comment: string, over: object = {}) => ({ type, bits, fingerprint: f, comment, panelKey: false, restricted: false, lines: 1, ...over });
  qc.setQueryData(["node-server-keys", "nod_de1"], { keys: [key("ssh-ed25519", 256, "SHA256:Zp8Lr1NvTc5KJd3Fq7YwB2mXa9RbE1c", "mistgate-panel", { panelKey: true }), key("ssh-ed25519", 256, "SHA256:Hb3Jk7QwXn1TrPz5Lm8Vc2Ys6DfGa9KtN2e", "owner@laptop", { lines: 2 }), key("ssh-rsa", 4096, "SHA256:Dv6Cx2MbPq8NhZr4Wt7Ly1Ks3FgJa5BrE8g", "backup-key", { restricted: true })], otherLines: 0 } as never);
  const osStatus = { measuredUnix: NOW - 3600, distroId: "debian", distroVersion: "13", pending: osKind === "upd" ? 12 : 0, security: osKind === "upd" ? 6 : 0, notable: [], kernelRunning: "6.12.48+deb13-amd64", kernelNewest: osKind === "boot" ? "6.12.57+deb13-amd64" : "6.12.48+deb13-amd64", rebootRequired: osKind === "boot", rebootReasons: osKind === "boot" ? ["kernel"] : [], held: [], apt: "ok", halfConfigured: [], listsUnix: NOW - 3600, dpkgUnix: NOW - 7200, autoUpdates: "on", autoReboot: false, awgModuleNext: "", container: "", bootIdHash: "", error: "" };
  qc.setQueryData(osUpdatesQuery.queryKey, {
    nowUnix: NOW, scheduleTimezoneOffsetMinutes: 0, panelHost: { ...osStatus, pending: 0, security: 0, rebootRequired: false, rebootReasons: [] },
    nodes: [{ nodeId: "nod_de1", name: "de1", connected: !sp.down, supported: true, onlineUsers: people, onPanelServer: false, state: osKind === "boot" ? OsUpdateState.REBOOT_REQUIRED : osKind === "upd" ? OsUpdateState.UPDATES : OsUpdateState.UP_TO_DATE, status: osStatus, updateCapable: true, updateBlock: "", updateBlockAll: "", inbounds: 3, rebootBlock: "" }],
  } as never);
  qc.setQueryData(debugListQuery("nod_de1").queryKey, {
    captures: extra.dbg === "rec" ? [{ captureId: "req_1", nodeId: "nod_de1", nodeName: "de1", state: "running", level: "debug", sources: ["xray", "hysteria"], minutes: 5, startedUnix: NOW - 100, endsUnix: NOW + 200, records: 0, dropped: 0, actor: "owner" }] : [],
  } as never);
  const nodes = (qc.getQueryData(nodesQuery.queryKey) as { nodes: Record<string, unknown>[] }).nodes;
  qc.setQueryData(nodesQuery.queryKey, { ...(qc.getQueryData(nodesQuery.queryKey) as object), nodes: [...nodes, { ...nodes[0], id: "nod_msk1", name: "msk1", countryCode: "RU", location: "Moscow" }] } as never);
  qc.setQueryData(meQuery.queryKey, {
    admin: { id: "adm_1", displayName: "owner", role: sp.role ?? Role.OWNER },
    version: "0.1.71",
    sourceUrl: "https://github.com/example/mistgate",
    sourceStars: 298,
    stepUpUntilUnix: 0,
    edition: sp.edge ? "edge" : "vps",
    unavailable: sp.edge ? ["health_checks"] : [],
  } as never);
  return { qc, data };
}

/** One whole page of the scene, for a frame or a screenshot at the window's own width. */
export function NodePageFrame() {
  const params = new URLSearchParams(location.search);
  const state = (params.get("state") ?? "calm") as NState;
  const [scene] = useState(() => {
    if (params.get("fx") === "1") setEffectsEnabled(true);
    const os = params.get("os");
    const dbg = params.get("dbg");
    const wp = params.get("wp");
    const kind = wp === "ok" || wp === "pause" || wp === "bad" || wp === "none" || wp === "revoked" ? wp : undefined;
    if (params.get("tab") === "history") fakeHistory(state);
    const made = nodePageData(state, { os: os === "ok" || os === "upd" || os === "boot" ? os : undefined, dbg: dbg === "rec" || dbg === "done" ? dbg : undefined, wp: kind });
    // no WARP yet: the registration the scene plays takes nine seconds (four steps), then the account is there
    if (kind === "none") {
      (warpApi as unknown as Record<string, unknown>).registerWarp = async () => {
        await new Promise((r) => setTimeout(r, 9000));
        made.qc.setQueryData(warpQuery("nod_de1").queryKey, warpOf("ok") as never);
        return {};
      };
    }
    return made;
  });
  return (
    <QueryClientProvider client={scene.qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="nodes" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <NodeDetail data={scene.data as never} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

const frames: { id: string; state: NState; tab: string; title: string; w: number; h: number; /** More of the frame own params (?sv=, ?os=, ...). */ q?: string }[] = [
  { id: "node-overview-calm", state: "calm", tab: "overview", title: "Overview: load low, no alerts", w: 1440, h: 1700 },
  { id: "node-overview-alerts", state: "alerts", tab: "overview", title: "Overview: three alerts, one of them failing a check", w: 1440, h: 1800 },
  { id: "node-overview-many", state: "many", tab: "overview", title: "Overview: six alerts fold into 'show more', load high", w: 1440, h: 1900 },
  { id: "node-overview-limit", state: "limit", tab: "overview", title: "Overview: load at the limit, the meters turn red", w: 1440, h: 1800 },
  { id: "node-overview-down", state: "down", tab: "overview", title: "Overview: the node is silent, the last readings are dimmed", w: 1440, h: 1700 },
  { id: "node-overview-new", state: "new", tab: "overview", title: "Overview: a node just added, no history yet", w: 1440, h: 1500 },
  { id: "node-overview-retired", state: "retired", tab: "overview", title: "Overview: a retired node", w: 1440, h: 1200 },
  { id: "node-overview-viewer", state: "viewer", tab: "overview", title: "Overview: a read-only role, no fix buttons", w: 1440, h: 1800 },
  { id: "node-overview-via", state: "via", tab: "overview", title: "Overview: the agent reaches the panel through msk1", w: 1440, h: 1800 },
  { id: "node-overview-edge", state: "edge", tab: "overview", title: "Overview: the Cloudflare edition, no checks, no load hours", w: 1440, h: 1500 },
  { id: "node-profiles-calm", state: "calm", tab: "profiles", title: "Profiles: three servers running, a day of checks each", w: 1440, h: 1100 },
  { id: "node-profiles-many", state: "many", tab: "profiles", title: "Profiles: one did not start (the port is taken), its fix and the error", w: 1440, h: 1300 },
  { id: "node-profiles-down", state: "down", tab: "profiles", title: "Profiles: the node is silent, the day of checks is dimmed", w: 1440, h: 1100 },
  { id: "node-profiles-new", state: "new", tab: "profiles", title: "Profiles: none on the node yet", w: 1440, h: 700 },
  { id: "node-profiles-viewer", state: "viewer", tab: "profiles", title: "Profiles: a read-only role, no buttons", w: 1440, h: 1100 },
  { id: "node-profiles-1100", state: "calm", tab: "profiles", title: "Profiles at 1100 wide: the cells take a row of their own", w: 1100, h: 1300 },
  { id: "node-profiles-phone", state: "calm", tab: "profiles", title: "Profiles on a phone, 400 wide", w: 400, h: 1900 },
  { id: "node-doctor-calm", state: "calm", tab: "doctor", title: "Doctor: the node is healthy, one warning accepted as normal", w: 1440, h: 1500 },
  { id: "node-doctor-alerts", state: "alerts", tab: "doctor", title: "Doctor: two checks need attention", w: 1440, h: 1700 },
  { id: "node-doctor-limit", state: "limit", tab: "doctor", title: "Doctor: a check failed, the card is red", w: 1440, h: 1700 },
  { id: "node-doctor-new", state: "new", tab: "doctor", title: "Doctor: the node has no report yet", w: 1440, h: 800 },
  { id: "node-doctor-down", state: "down", tab: "doctor", title: "Doctor: the node does not answer, the last report", w: 1440, h: 1500 },
  { id: "node-doctor-viewer", state: "viewer", tab: "doctor", title: "Doctor: a read-only role, no buttons", w: 1440, h: 1700 },
  { id: "node-doctor-phone", state: "alerts", tab: "doctor", title: "Doctor on a phone, 400 wide", w: 400, h: 2600 },
  { id: "node-server-node", state: "calm", tab: "server", title: "Server: the node", w: 1440, h: 1000 },
  { id: "node-server-ch", state: "calm", tab: "server", q: "sv=ch", title: "Server: bandwidth", w: 1440, h: 700 },
  { id: "node-server-ag", state: "calm", tab: "server", q: "sv=ag", title: "Server: the agent and the link", w: 1440, h: 900 },
  { id: "node-server-ssh", state: "calm", tab: "server", q: "sv=ssh", title: "Server: SSH access and the keys on the server", w: 1440, h: 1400 },
  { id: "node-server-site", state: "calm", tab: "server", q: "sv=site", title: "Server: the decoy site", w: 1440, h: 900 },
  { id: "node-server-awg", state: "calm", tab: "server", q: "sv=awg", title: "Server: the AmneziaWG backend", w: 1440, h: 800 },
  { id: "node-server-sys-ok", state: "calm", tab: "server", q: "sv=sys&os=ok", title: "Server: the system is up to date, and the host", w: 1440, h: 1200 },
  { id: "node-server-sys-upd", state: "alerts", tab: "server", q: "sv=sys&os=upd", title: "Server: updates wait", w: 1440, h: 1200 },
  { id: "node-server-sys-boot", state: "many", tab: "server", q: "sv=sys&os=boot", title: "Server: a reboot is due", w: 1440, h: 1200 },
  { id: "node-server-dbg", state: "calm", tab: "server", q: "sv=dbg", title: "Server: the debug recording, idle", w: 1440, h: 800 },
  { id: "node-server-dbg-rec", state: "calm", tab: "server", q: "sv=dbg&dbg=rec", title: "Server: the debug recording runs", w: 1440, h: 700 },
  { id: "node-server-del", state: "calm", tab: "server", q: "sv=del", title: "Server: retiring the node", w: 1440, h: 500 },
  { id: "node-server-retired", state: "retired", tab: "server", q: "sv=ssh", title: "Server: a retired node keeps four sections", w: 1440, h: 800 },
  { id: "node-server-viewer", state: "viewer", tab: "server", title: "Server: a read-only role", w: 1440, h: 1000 },
  { id: "node-server-edge", state: "edge", tab: "server", q: "sv=sys", title: "Server: the Cloudflare edition has no system card", w: 1440, h: 700 },
  { id: "node-server-phone", state: "calm", tab: "server", title: "Server on a phone, 400 wide", w: 400, h: 2000 },
  { id: "node-server-phone-ssh", state: "calm", tab: "server", q: "sv=ssh", title: "Server: SSH access on a phone, 400 wide", w: 400, h: 2400 },
  { id: "node-exit-ok", state: "calm", tab: "exit", q: "wp=ok", title: "Exit: WARP works, three rows, the traffic and the DNS cards", w: 1440, h: 1500 },
  { id: "node-exit-pause", state: "calm", tab: "exit", q: "wp=pause", title: "Exit: WARP is paused", w: 1440, h: 1500 },
  { id: "node-exit-bad", state: "alerts", tab: "exit", q: "wp=bad", title: "Exit: WARP does not answer, the tunnel is down", w: 1440, h: 1600 },
  { id: "node-exit-revoked", state: "calm", tab: "exit", q: "wp=revoked", title: "Exit: Cloudflare revoked the account, Register again leads", w: 1440, h: 1600 },
  { id: "node-exit-dead", state: "down", tab: "exit", q: "wp=ok", title: "Exit: the node is silent, WARP is unknown", w: 1440, h: 1500 },
  { id: "node-exit-none", state: "calm", tab: "exit", q: "wp=none", title: "Exit: no WARP yet (Enable WARP plays the registration, nine seconds)", w: 1440, h: 1300 },
  { id: "node-exit-viewer", state: "viewer", tab: "exit", q: "wp=ok", title: "Exit: a read-only role", w: 1440, h: 1500 },
  { id: "node-exit-1100", state: "calm", tab: "exit", q: "wp=ok", title: "Exit at 1100 wide: one column", w: 1100, h: 1900 },
  { id: "node-exit-phone", state: "calm", tab: "exit", q: "wp=ok", title: "Exit on a phone, 400 wide", w: 400, h: 3000 },
  { id: "node-history-events", state: "calm", tab: "history", title: "History: the week at a glance and the timeline of events", w: 1440, h: 1200 },
  { id: "node-history-down", state: "down", tab: "history", title: "History: the node is down, the last cell is red", w: 1440, h: 1200 },
  { id: "node-history-new", state: "new", tab: "history", title: "History: a node added today, one event", w: 1440, h: 700 },
  { id: "node-history-journal", state: "calm", tab: "history", q: "view=journal", title: "History: the journal of warnings and errors", w: 1440, h: 800 },
  { id: "node-history-logs", state: "calm", tab: "history", q: "view=logs", title: "History: the log of the agent, a line comes every second", w: 1440, h: 900 },
  { id: "node-history-phone", state: "calm", tab: "history", title: "History on a phone, 400 wide", w: 400, h: 2000 },
  { id: "node-history-logs-phone", state: "calm", tab: "history", q: "view=logs", title: "History: the log on a phone, 400 wide", w: 400, h: 900 },
  { id: "node-overview-1100", state: "alerts", tab: "overview", title: "Overview at 1100 wide: one column", w: 1100, h: 2600 },
  { id: "node-overview-phone", state: "alerts", tab: "overview", title: "Overview on a phone, 400 wide", w: 400, h: 3400 },
];

export function NodePageKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  const only = new URLSearchParams(location.search).get("tab");
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Node page</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames
        .filter((f) => !only || f.tab === only)
        .map((f) => (
          <Shot key={f.id} id={f.id} title={`${f.title} · ${f.w} px`}>
            <div className="max-w-full overflow-x-auto rounded-card border border-line">
              {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
              {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
              <iframe key={key} title={f.title} src={`${path}?only=nodepage-frame&tab=${f.tab}&state=${f.state}${f.q ? `&${f.q}` : ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
            </div>
          </Shot>
        ))}
    </div>
  );
}
