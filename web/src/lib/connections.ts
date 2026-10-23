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

/** One fact of a line: its words, and whether it is trouble (shown in the warning colour). */
export type Part = { text: string; warn?: boolean };

/** A device on one profile of a node: connections, early drops, refusals, handshakes that never completed, time online. */
export function deviceConnParts(t: T, fmt: Fmt, x: DeviceConn): Part[] {
  const parts: Part[] = [{ text: t.n("conn.connects", x.connects) }];
  if (x.drops) parts.push({ text: t("conn.drops", { n: x.drops }), warn: true });
  if (x.rejects) parts.push({ text: t("conn.rejects", { n: x.rejects }), warn: true });
  if (x.stalls) parts.push({ text: t("conn.stalls", { n: x.stalls }), warn: true });
  if (x.onlineS) parts.push({ text: t("conn.online", { time: fmt.duration(x.onlineS) }) });
  return parts;
}

/**
 * A profile on the node: the same counts summed over its keys, then what only the inbound knows. Hysteria2: the share of
 * started handshakes that never reached authentication (trouble from half of at least ten). AmneziaWG: the devices whose
 * handshake never completed out of those that tried. Unknown keys are context, not trouble: scanners send them too.
 */
export function inboundConnParts(t: T, fmt: Fmt, x: InboundConn): Part[] {
  const parts: Part[] = [];
  if (x.connects) parts.push({ text: t.n("conn.connects", x.connects) });
  if (x.drops) parts.push({ text: t("conn.drops", { n: x.drops }), warn: true });
  if (x.rejects) parts.push({ text: t("conn.rejects", { n: x.rejects }), warn: true });
  if (x.hellos > 0) {
    const failed = Math.max(0, x.hellos - x.handshakes);
    const pct = Math.round((100 * failed) / x.hellos);
    if (failed > 0) parts.push({ text: t("conn.hellos", { pct, n: x.hellos }), warn: x.hellos >= 10 && pct >= 50 });
  }
  if (x.keysTried > 0 && x.keysStalled > 0) parts.push({ text: t("conn.keysStalled", { n: x.keysStalled, tried: x.keysTried }), warn: true });
  if (x.unknownKeys) parts.push({ text: t("conn.unknown", { n: x.unknownKeys }) });
  if (x.onlineS) parts.push({ text: t("conn.online", { time: fmt.duration(x.onlineS) }) });
  return parts;
}
