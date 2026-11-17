import { NodeUpdateState, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { canaryOf, isActive, outdatedNodes, stepInfo, type NodeUpdate, type Rollout, type Step, type Updates } from "@/lib/updates";

// What the cards of the nodes (the board's .uq-fleet) say: one card per node, in the colour of where it stands against the release,
// the word under its version, the canary marked, and a thin line while its agent is being installed. Pure, so a test can read it.

/** The board's states of a card: works, can be updated, being updated, went wrong, not reachable, waiting its turn ("" ). */
export type CardKind = "ok" | "part" | "now" | "bad" | "off" | "";

/** A word of the card: the key of its text (the page fills {version}). */
export type CardWord = "ok" | "can" | "sent" | "gate" | "back" | "off" | "wait" | "manual" | "failed";

export type FleetCard = {
  id: string;
  name: string;
  version: string;
  kind: CardKind;
  word: CardWord;
  /** The version the word names: the release to update to, or the one the node was put back on. */
  wordVersion: string;
  canary: boolean;
  /** A thin line of how far the agent got (0..100); undefined when nothing is being installed. */
  pg?: number;
  /** It finished a moment ago: the card flashes. */
  just: boolean;
};

const nodeCard = (n: NodeUpdate, to: string): Pick<FleetCard, "kind" | "word" | "wordVersion"> => {
  switch (n.state) {
    case NodeUpdateState.UP_TO_DATE:
      return { kind: "ok", word: "ok", wordVersion: "" };
    case NodeUpdateState.OUTDATED:
      return { kind: "part", word: "can", wordVersion: to };
    case NodeUpdateState.UPDATING:
      return { kind: "now", word: "sent", wordVersion: "" };
    case NodeUpdateState.ROLLED_BACK:
      return { kind: "bad", word: "back", wordVersion: n.version };
    case NodeUpdateState.FAILED:
      return { kind: "bad", word: "failed", wordVersion: "" };
    case NodeUpdateState.UNSUPPORTED:
      return { kind: "part", word: "manual", wordVersion: "" };
    default:
      return { kind: "off", word: "off", wordVersion: "" };
  }
};

/** How far the node's download got, when it reports one; else what the step is worth (sent, being checked). */
function stepProgress(s: Step): number {
  if (s.state === StepState.SENT && s.downloadPhase && s.totalBytes > 0) return Math.round((Math.min(s.downloadedBytes, s.totalBytes) / s.totalBytes) * 70);
  return stepInfo(s.state).pct;
}

const stepCard = (s: Step, n: NodeUpdate): Pick<FleetCard, "kind" | "word" | "wordVersion" | "pg"> | null => {
  switch (s.state) {
    case StepState.PENDING:
      return { kind: "", word: "wait", wordVersion: "" };
    case StepState.SENT:
      return { kind: "now", word: "sent", wordVersion: "", pg: stepProgress(s) };
    case StepState.GATING:
      return { kind: "now", word: "gate", wordVersion: "", pg: stepProgress(s) };
    case StepState.PASSED:
      return { kind: "ok", word: "ok", wordVersion: "" };
    case StepState.FAILED:
      return { kind: "bad", word: "failed", wordVersion: "" };
    case StepState.ROLLED_BACK:
      return { kind: "bad", word: "back", wordVersion: n.version };
    default:
      return null;
  }
};

/** The cards of the page: the canary first (the node a rollout starts with, or the one it started with), then the rest in the panel's order. */
export function fleetCards(d: Updates, rollout: Rollout | null | undefined = d.rollout): FleetCard[] {
  const to = d.bundle?.version ?? "";
  const active = isActive(rollout) ? rollout : null;
  const steps = new Map(active?.steps.map((s) => [s.nodeId, s] as const));
  // the card of a node that finished a moment ago flashes, in the last rollout as well as in the running one
  const finished = new Map(rollout?.steps.map((s) => [s.nodeId, s] as const));
  const first = active ? undefined : canaryOf(outdatedNodes(d.nodes));
  const cards = d.nodes.map((n): FleetCard => {
    const s = active ? steps.get(n.nodeId) : undefined;
    const look: Pick<FleetCard, "kind" | "word" | "wordVersion"> & { pg?: number } = (s && stepCard(s, n)) ?? nodeCard(n, to);
    return {
      id: n.nodeId,
      name: n.name,
      version: n.version || "—",
      ...look,
      pg: look.pg ?? (n.state === NodeUpdateState.UPDATING ? 35 : undefined),
      canary: (!!s && s.stage === 0) || n === first,
      just: finished.get(n.nodeId)?.state === StepState.PASSED && d.nowUnix - (finished.get(n.nodeId)?.finishedUnix ?? 0) < 60,
    };
  });
  // the canary leads, the others keep the panel's order
  return [...cards.filter((c) => c.canary), ...cards.filter((c) => !c.canary)];
}

/** The line of the bar "now": what the rollout is doing this moment, in the board's three forms. */
export function rolloutNow(r: Rollout): { kind: "canary" | "gate" | "batch" | "wait"; node: string; names: string } {
  const live = r.steps.filter((s) => s.state === StepState.SENT || s.state === StepState.GATING);
  const gating = live.find((s) => s.state === StepState.GATING);
  if (gating) return { kind: "gate", node: gating.nodeName, names: gating.nodeName };
  const sent = live.filter((s) => s.state === StepState.SENT);
  if (sent.length === 0) return { kind: "wait", node: "", names: "" };
  const first = sent[0]!;
  const names = sent.map((s) => s.nodeName).join(", ");
  return { kind: first.stage === 0 ? "canary" : "batch", node: first.nodeName, names };
}
