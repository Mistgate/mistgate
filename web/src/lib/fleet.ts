import { useQuery } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { isProblem, nodeKind } from "./node-status";
import { overviewQuery, userCountQuery } from "./queries";

// What the shell needs to know about the fleet: counts for the sidebar and the health pill. They come from
// the same Overview query the Overview screen uses (24 h), so the header costs no extra request there.
export type FleetSummary = {
  nodes: number;
  users: number;
  /** Nodes that count as a problem (isProblem): broken ones and those that need attention; not host blips, updates or pending nodes. */
  problems: number;
  /** Of those, the broken ones (red). */
  broken: number;
  /** Nodes waiting for their install (PENDING), and how many nodes ever connected. */
  pending: number;
  connected: number;
  /** Active alerts that are not muted and not info: what the health module counts. */
  alerts: number;
  /** How many of those are critical. */
  critical: number;
  /** The first answer has not arrived yet. */
  loading: boolean;
};

type Card = { status: NodeStatus; reason?: { code: string; params: Record<string, string> } | undefined };

/** The counts of a list of node cards (the Overview's), pure for the tests. */
export function countNodes(cards: readonly Card[]): Pick<FleetSummary, "problems" | "broken" | "pending" | "connected"> {
  const problems = cards.filter(isProblem);
  return {
    problems: problems.length,
    broken: problems.filter((n) => nodeKind(n) === "bad").length,
    pending: cards.filter((n) => n.status === NodeStatus.PENDING).length,
    connected: cards.filter((n) => n.status !== NodeStatus.PENDING && n.status !== NodeStatus.RETIRED).length,
  };
}

export function useFleetSummary(): FleetSummary {
  const overview = useQuery(overviewQuery());
  const users = useQuery(userCountQuery);
  return {
    nodes: overview.data?.nodesTotal ?? 0,
    users: users.data?.counts?.all ?? 0,
    ...countNodes(overview.data?.nodes ?? []),
    alerts: overview.data?.alertsActive ?? 0,
    critical: overview.data?.alertsCritical ?? 0,
    loading: overview.isPending,
  };
}

/**
 * The one number of problems, the same in the header, the menu, the Overview strip and on Health: the active alerts or the
 * problem nodes, whichever is more, so one fault (a broken node and its alert) is not counted twice.
 */
export const problemCount = (s: Pick<FleetSummary, "problems" | "alerts">) => Math.max(s.problems, s.alerts);

/** The colour of that number: red for a broken node or a critical alert, amber for the other problems, green when quiet. */
export function healthKind(broken: number, problems: number, critical: number): "ok" | "warn" | "bad" {
  if (critical > 0 || broken > 0) return "bad";
  return problems > 0 ? "warn" : "ok";
}

/** The tile that folds the rest of the nodes: the problems' colour, grey while some of them wait (an install, a blip), green only when every one works. */
export function foldView(cards: readonly Card[]): { kind: StatusKind; problems: number; pending: number } {
  const c = countNodes(cards);
  const kind = c.problems ? healthKind(c.broken, c.problems, 0) : cards.every((n) => nodeKind(n) === "ok") ? "ok" : "off";
  return { kind, problems: c.problems, pending: c.pending };
}

/**
 * The fleet health pill: a long label for the top bar, a short one for the phone header, and where a click leads (Health
 * when there are alerts to read, else the Overview, whose strip names the nodes). A fleet whose every node still waits for
 * its install is grey, never "healthy".
 */
export function useFleetHealth(): { kind: StatusKind; label: string; short: string; problems: number; to: "/" | "/health" } {
  const t = useT();
  const s = useFleetSummary();
  const n = problemCount(s);
  const to = s.alerts > 0 ? "/health" : "/";
  if (s.loading) return { kind: "off", label: t("common.loading"), short: "…", problems: n, to };
  if (s.nodes === 0) return { kind: "off", label: t("health.none"), short: t("health.none"), problems: n, to };
  const kind = healthKind(s.broken, n, s.critical);
  if (kind !== "ok") return { kind, label: t.n("health.problems", n), short: String(n), problems: n, to };
  if (s.connected === 0 && s.pending > 0) return { kind: "off", label: t.n("ov.tail.pending", s.pending), short: t("ov.pill.pendingShort"), problems: n, to };
  return { kind, label: t("health.ok"), short: t("health.okShort"), problems: n, to };
}
