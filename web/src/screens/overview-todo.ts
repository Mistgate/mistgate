import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import type { T } from "@/i18n";
import type { Fmt } from "@/lib/format";
import { alertFix, alertTitle, alertWhy, groupAlerts, groupSeverity, groupTitle, type Alert } from "@/lib/health";
import { toolTitle, type Approval } from "@/lib/integrations";
import { isProblem, nodeKind, reasonText, statusWord } from "@/lib/node-status";
import type { FixTarget } from "./health/fix";
import type { NodeCard } from "./overview-types";

// "Needs you" on the Overview: what asks for the owner's hand, as one list. A warning of the alerts, a node that is a problem
// without an alert yet (broken, or with no profile), and what an agent waits to have approved.

export type TodoAction =
  /** The two-step fix of the Health screen, started from the row. */
  | { kind: "fix"; target: FixTarget }
  | { kind: "node"; id: string; tab?: "profiles" | "exit"; label: string }
  | { kind: "health"; label: string }
  | { kind: "approvals"; label: string };

export type TodoItem = {
  id: string;
  /** bad = red (a fault), warn = amber, ai = sky (an agent asks). */
  tone: "bad" | "warn" | "ai";
  /** The node the row is about, set in front of the title. */
  node: string;
  title: string;
  sub: string;
  action: TodoAction;
};

function alertItem(t: T, fmt: Fmt, g: readonly Alert[], canFix: boolean): TodoItem {
  const a = g[0]!;
  const single = g.length === 1;
  const fix = single ? alertFix(a) : null;
  // the same key as the Health screen's row of this check, so a fix started from either shows its progress on both
  const inbound = a.params.inbound_id ?? "";
  const check = a.params.check ?? a.subject;
  let action: TodoAction;
  if (fix && canFix && a.nodeId) {
    action = { kind: "fix", target: { ...fix, nodeId: a.nodeId, nodeName: a.nodeName, key: `${a.nodeId}/${check}/${inbound}`, profile: a.params.profile } };
  } else if (a.nodeId && a.actions.includes("open_profiles")) {
    action = { kind: "node", id: a.nodeId, tab: "profiles", label: t("hl.alerts.openProfiles") };
  } else if (a.nodeId && a.actions.includes("open_warp")) {
    action = { kind: "node", id: a.nodeId, tab: "exit", label: t("warp.open") };
  } else if (a.nodeId) {
    action = { kind: "node", id: a.nodeId, label: t("hl.alerts.openNode") };
  } else {
    action = { kind: "health", label: t("ov.todo.open") };
  }
  return {
    id: `a-${a.id}`,
    tone: groupSeverity(g) === AlertSeverity.CRITICAL ? "bad" : "warn",
    node: a.nodeName,
    title: single ? alertTitle(t, a) : groupTitle(t, g),
    sub: alertWhy(t, fmt, a),
    action,
  };
}

function nodeItem(t: T, n: NodeCard): TodoItem {
  const tone = nodeKind(n) === "bad" ? "bad" : "warn";
  const action: TodoAction =
    n.reason?.code === "no_profiles" ? { kind: "node", id: n.id, tab: "profiles", label: t("ov.addProfile", { name: n.name }) } : { kind: "node", id: n.id, label: t("hl.alerts.openNode") };
  return { id: `n-${n.id}`, tone, node: n.name, title: statusWord(t, n), sub: reasonText(t, n.reason), action };
}

/**
 * The rows, red first, then amber, then what an agent asks; the order inside a group is the server's. `alerts` are the loud
 * alerts about nodes (not people's, not muted); a node the alerts already name has no row of its own. A fix is offered only to
 * a role that may apply it (`canFix`).
 */
export function todoItems(
  t: T,
  fmt: Fmt,
  { alerts, cards, approvals, canFix }: { alerts: readonly Alert[]; cards: readonly NodeCard[]; approvals: readonly Pick<Approval, "id" | "tool">[]; canFix: boolean },
): TodoItem[] {
  const named = new Set(alerts.map((a) => a.nodeId).filter(Boolean));
  const rows = [
    ...groupAlerts(alerts).map((g) => alertItem(t, fmt, g, canFix)),
    ...cards.filter((n) => isProblem(n) && !named.has(n.id)).map((n) => nodeItem(t, n)),
  ];
  const ask = approvals.map((a): TodoItem => ({ id: `p-${a.id}`, tone: "ai", node: "", title: t("ov.todo.approval"), sub: toolTitle(t, a.tool), action: { kind: "approvals", label: t("ov.todo.decide") } }));
  return [...rows.filter((r) => r.tone === "bad"), ...rows.filter((r) => r.tone === "warn"), ...ask];
}
