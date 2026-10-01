import { useMemo } from "react";
import type { StatusKind } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT, type T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { minutesText } from "./format";

// How a node's status and reason look in the UI. The server sends a NodeStatus and an optional reason
// {code, params}; an ONLINE node with a reason is shown as "attention", "works partly" or "broken" because the agent is
// fine but something on the node is not.

/** A StatusReason after plain(): the code the UI localizes and the values that fill its placeholders. */
export type Reason = { code: string; params: Record<string, string> };
type Reasoned = { status: NodeStatus; reason?: Reason | undefined };

export function nodeKind({ status, reason }: Reasoned): StatusKind {
  switch (status) {
    case NodeStatus.ONLINE:
      if (reason?.code === "inbound_failed" || reason?.code === "doctor_fail") return "bad";
      return reason ? "warn" : "ok"; // no_profiles, state_drift, clock_skew: the node runs, but needs a look
    case NodeStatus.DOWN:
    case NodeStatus.NO_TRAFFIC:
      return "bad";
    case NodeStatus.BLIP:
      return "blip";
    case NodeStatus.UPDATING:
      return "busy";
    default:
      return "off"; // pending, retired, unknown
  }
}

/**
 * The agent has a live session with the panel: what restarting profiles and streaming logs need. "No traffic" is such a
 * node too (the agent is there, the client-side check fails), so it must not lose its restart button or its logs.
 */
export function agentLinked(status: NodeStatus): boolean {
  return status === NodeStatus.ONLINE || status === NodeStatus.NO_TRAFFIC;
}

/** Counts towards the problems (the number in the header, the strip, the menu). A clock that drifts a little and a host blip do not. */
export function isProblem(n: Reasoned): boolean {
  const k = nodeKind(n);
  return k === "bad" || (k === "warn" && n.reason?.code !== "clock_skew");
}

/** The node has an agent that came at least once: anything but waiting for the install and retired. */
export const hasConnected = (n: Pick<Reasoned, "status">) =>
  n.status !== NodeStatus.PENDING && n.status !== NodeStatus.RETIRED && n.status !== NodeStatus.UNSPECIFIED;

/** Some of the node's profiles failed and some run: "works partly", still red. Without the counts (an old reason) it is "broken". */
function partly(r?: Reason): boolean {
  const failed = Number(r?.params.failed);
  const total = Number(r?.params.total);
  return r?.code === "inbound_failed" && failed > 0 && total > failed;
}

// Problems first: broken, attention, host blip, updating, waiting, healthy.
const rank: Record<StatusKind, number> = { bad: 0, warn: 1, blip: 2, busy: 3, off: 4, ok: 5 };
export const kindRank = (k: StatusKind) => rank[k];

export function sortByProblems<N extends Reasoned>(nodes: readonly N[]): N[] {
  return [...nodes].sort((a, b) => kindRank(nodeKind(a)) - kindRank(nodeKind(b)));
}

const statusKeys: Partial<Record<NodeStatus, MessageKey>> = {
  [NodeStatus.PENDING]: "node.status.pending",
  [NodeStatus.ONLINE]: "node.status.online",
  [NodeStatus.DOWN]: "node.status.down",
  [NodeStatus.BLIP]: "node.status.blip",
  [NodeStatus.NO_TRAFFIC]: "node.status.noTraffic",
  [NodeStatus.UPDATING]: "node.status.updating",
  [NodeStatus.RETIRED]: "node.status.retired",
};

const reasonKeys: Record<string, MessageKey> = {
  agent_silent: "node.reason.agent_silent",
  host_blip: "node.reason.host_blip",
  enrollment_pending: "node.reason.enrollment_pending",
  enrollment_expired: "node.reason.enrollment_expired",
  inbound_failed: "node.reason.inbound_failed",
  state_drift: "node.reason.state_drift",
  clock_skew: "node.reason.clock_skew",
  no_traffic: "node.reason.no_traffic",
  doctor_fail: "node.reason.doctor_fail",
  no_profiles: "node.reason.no_profiles",
};

/** Localized explanation of a status reason; empty when there is none. An unknown code is shown as it is. */
export function reasonText(t: T, reason?: Reason): string {
  if (!reason?.code) return "";
  const key = reasonKeys[reason.code];
  if (!key) return reason.code;
  const p = reason.params;
  switch (reason.code) {
    case "doctor_fail": {
      // a doctor check id: name it the way the Doctor tab does
      const title = `doctor.${p.check}.title`;
      return t(key, Object.hasOwn(en, title) ? { ...p, check: t(title as MessageKey) } : p);
    }
    case "agent_silent":
    case "host_blip":
      return t(key, { span: minutesText(t, Number(p.minutes)) });
    case "enrollment_pending":
      return t(key, { span: minutesText(t, Number(p.expires_in_minutes)) });
    case "inbound_failed":
      // The agent's error as it came; lib/inbound-error.ts could word it here too
      return p.profile ? t("node.reason.inbound_failed.named", { profile: p.profile, error: p.error ?? "" }) : t(key, p);
  }
  return t(key, p);
}

/** One word for the status ("Healthy", "Unreachable"); a reason on an ONLINE node turns it into attention, partly working or broken. */
export function statusWord(t: T, n: Reasoned): string {
  if (n.status === NodeStatus.ONLINE && n.reason) {
    if (partly(n.reason)) return t("node.status.partly");
    return t(nodeKind(n) === "bad" ? "node.status.broken" : "node.status.attention");
  }
  const key = statusKeys[n.status];
  return key ? t(key) : "";
}

/** The label under a node's name: the reason when there is one, else the status word. A pending node says both ("Waiting for install · …"). */
export function statusLine(t: T, n: Reasoned): string {
  const reason = reasonText(t, n.reason);
  if (n.status === NodeStatus.PENDING && reason) return `${statusWord(t, n)} · ${reason}`;
  return reason || statusWord(t, n);
}

export function useNodeStatus() {
  const t = useT();
  return useMemo(
    () => ({
      kind: nodeKind,
      word: (n: Reasoned) => statusWord(t, n),
      reason: (r?: Reason) => reasonText(t, r),
      line: (n: Reasoned) => statusLine(t, n),
    }),
    [t],
  );
}
