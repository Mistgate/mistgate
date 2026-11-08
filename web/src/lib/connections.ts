import { queryOptions } from "@tanstack/react-query";
import type { GetNodeConnectionsResponse, InboundConnections } from "@/gen/mistgate/admin/v1/node_pb";
import type { DeviceConnection, GetUserConnectionResponse } from "@/gen/mistgate/admin/v1/user_pb";
import type { T } from "@/i18n";
import { nodes, users } from "./api";
import type { Fmt } from "./format";
import { plain, type Plain } from "./plain";

// What the nodes count of connection attempts (agent.proto LinkCounters, design/connection-diagnostics.md): the Connection
// block of the person's card and a line under every profile on the node's Profiles tab. Counts only, never an address.
// The words are in i18n/connections.ts.

export type UserConnection = Plain<GetUserConnectionResponse>;
export type DeviceConn = Plain<DeviceConnection>;
export type NodeConnections = Plain<GetNodeConnectionsResponse>;
export type InboundConn = Plain<InboundConnections>;

export const userConnectionQuery = (userId: string) =>
  queryOptions({
    queryKey: ["users", "connection", userId],
    queryFn: async ({ signal }) => plain(await users.getUserConnection({ userId }, { signal })),
    refetchInterval: 60_000,
  });

export const nodeConnectionsQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["nodes", "connections", nodeId],
    queryFn: async ({ signal }) => plain(await nodes.getNodeConnections({ nodeId }, { signal })),
    refetchInterval: 60_000,
  });

/** One fact of a line: its words, whether it is trouble (shown in the warning colour), and a short explanation on hover. */
export type Part = { text: string; warn?: boolean; hint?: string };

/** The links/3 counters of a key (agent.proto LinkCounters): the device's line and the profile's line share them. */
type KeyReasons = {
  connects: number;
  tcpStalls: number;
  tcpStalls16k: number;
  initiations: number;
  responses: number;
  responsesUnmatched: number;
  unsupported: string[];
};

/**
 * Why handshakes fail, where the node sees it (an agent with links/3): VLESS connections frozen after the first 16 KB, AmneziaWG
 * answers lost on the way back or not sent at all. A counter in unsupported is not known and says nothing.
 */
function keyReasonParts(t: T, x: KeyReasons): Part[] {
  const parts: Part[] = [];
  const unsupported = x.unsupported ?? []; // a row of a panel before links/3 has no list
  const known = (f: string) => !unsupported.includes(f);
  if (known("tcp_stalls_16k") && x.tcpStalls16k > 0) {
    parts.push({ text: t("conn.freeze16k", { n: x.tcpStalls16k }), warn: x.tcpStalls16k >= 2, hint: t("conn.freeze16k.hint") });
  }
  const later = x.tcpStalls - Math.min(x.tcpStalls, x.tcpStalls16k);
  if (known("tcp_stalls") && later > 0) parts.push({ text: t("conn.tcpStalls", { n: later }), hint: t("conn.tcpStalls.hint") });
  if (known("responses") && x.initiations >= 3 && x.responses * 5 < x.initiations) {
    parts.push({ text: t("conn.unanswered", { n: x.initiations - x.responses }), warn: true, hint: t("conn.unanswered.hint") });
  }
  if (known("responses_unmatched") && x.responsesUnmatched > 0) {
    const lost = x.responses >= 3 && x.responsesUnmatched * 10 >= x.responses * 8;
    parts.push({ text: t("conn.answersLost", { n: x.responsesUnmatched, total: x.responses }), warn: lost, hint: t("conn.answersLost.hint") });
  }
  return parts;
}

/** The inbound-only reasons: handshakes the node answered in vain, alerts either way, REALITY's refusals. */
function inboundReasonParts(t: T, x: InboundConn): Part[] {
  const parts: Part[] = [];
  const known = (f: string) => !x.unsupported.includes(f);
  const ours = Math.max(0, x.hellos - x.hellosForeign);
  if (known("hs_unfinished") && x.hsUnfinished > 0) {
    parts.push({ text: t("conn.noReply", { n: x.hsUnfinished }), warn: x.hsUnfinished >= 10 && x.hsUnfinished * 2 >= ours, hint: t("conn.noReply.hint") });
  }
  if (known("tls_cert_refused") && x.tlsCertRefused > 0) {
    parts.push({ text: t("conn.certRefused", { n: x.tlsCertRefused }), warn: true, hint: t("conn.certRefused.hint") });
  }
  if (known("tls_alerts_sent") && x.tlsAlertsSent > 0) parts.push({ text: t("conn.alertsSent", { n: x.tlsAlertsSent }), hint: t("conn.alertsSent.hint") });
  if (known("auth_failed") && x.authFailed > 0) {
    parts.push({ text: t("conn.realityKey", { n: x.authFailed }), warn: x.authFailed >= 5 && x.authFailed >= x.connects, hint: t("conn.realityKey.hint") });
  }
  const decoy = x.realityFallback - Math.min(x.realityFallback, x.authFailed);
  if (known("reality_fallback") && decoy > 0) parts.push({ text: t("conn.decoy", { n: decoy }), hint: t("conn.decoy.hint") });
  return parts;
}

type Quality = { freezes: number; rttMs: number; rttSamples: number; lossPermille: number; pktsSent: number };

/**
 * What a links/2 agent knows of how Hysteria2 connections went: frozen paths (what tells a freeze from an app check), the average RTT and the
 * loss. Loss is trouble from 5 % over at least a thousand packets.
 */
function qualityParts(t: T, x: Quality): Part[] {
  const parts: Part[] = [];
  if (x.freezes) parts.push({ text: t("conn.freezes", { n: x.freezes }), warn: true });
  if (x.rttSamples > 0) parts.push({ text: t("conn.rtt", { ms: x.rttMs }) });
  if (x.pktsSent >= 1000) parts.push({ text: t("conn.loss", { pct: (x.lossPermille / 10).toFixed(1) }), warn: x.lossPermille >= 50 });
  return parts;
}

/**
 * A device on one profile of a node: connections, early drops, refusals, handshakes that never completed, time online, and
 * with a links/2 agent frozen paths, RTT and loss. Where the agent tells frozen paths apart, an early drop is no longer
 * trouble by itself: most are short checks of the app.
 */
export function deviceConnParts(t: T, fmt: Fmt, x: DeviceConn): Part[] {
  const links2 = x.freezes + x.quiet + x.clientCloses + x.rttSamples > 0;
  const parts: Part[] = [{ text: t.n("conn.connects", x.connects) }];
  if (x.drops) parts.push({ text: t("conn.drops", { n: x.drops }), warn: !links2 });
  if (x.rejects) parts.push({ text: t("conn.rejects", { n: x.rejects }), warn: true });
  if (x.stalls) parts.push({ text: t("conn.stalls", { n: x.stalls }), warn: true });
  parts.push(...qualityParts(t, x));
  parts.push(...keyReasonParts(t, x));
  if (x.onlineS) parts.push({ text: t("conn.online", { time: fmt.duration(x.onlineS) }) });
  return parts;
}

/**
 * A profile on the node: the same counts summed over its keys, then what only the inbound knows. Hysteria2: the share of
 * started handshakes that never reached authentication (trouble from half of at least ten). AmneziaWG: the devices whose
 * handshake never completed out of those that tried. Unknown keys are context, not trouble: scanners send them too.
 */
export function inboundConnParts(t: T, fmt: Fmt, x: InboundConn): Part[] {
  const links2 = x.protocol === "hysteria2" && !x.unsupported.includes("freezes");
  const parts: Part[] = [];
  if (x.connects) parts.push({ text: t.n("conn.connects", x.connects) });
  if (x.drops) parts.push({ text: t("conn.drops", { n: x.drops }), warn: !links2 });
  if (x.rejects) parts.push({ text: t("conn.rejects", { n: x.rejects }), warn: true });
  if (links2) parts.push(...qualityParts(t, x));
  parts.push(...keyReasonParts(t, x), ...inboundReasonParts(t, x));
  if (x.protocol === "hysteria2" && x.hellos > 0) {
    // with links/2 the handshakes that never completed are known apart from pings that never authenticate
    const failed = links2 ? Math.max(0, x.hellos - x.accepted) : Math.max(0, x.hellos - x.handshakes);
    const pct = Math.round((100 * failed) / x.hellos);
    if (failed > 0) parts.push({ text: t("conn.hellos", { pct, n: x.hellos }), warn: x.hellos >= 10 && pct >= 50 });
  }
  if (x.keysTried > 0 && x.keysStalled > 0) parts.push({ text: t("conn.keysStalled", { n: x.keysStalled, tried: x.keysTried }), warn: true });
  if (x.unknownKeys) parts.push({ text: t("conn.unknown", { n: x.unknownKeys }) });
  if (x.onlineS) parts.push({ text: t("conn.online", { time: fmt.duration(x.onlineS) }) });
  return parts;
}
