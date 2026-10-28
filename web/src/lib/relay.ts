import { queryOptions } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import type { Relay, RelayCandidate, RelayCountryHint, RelayReachHint, RelayStage } from "@/gen/mistgate/admin/v1/relay_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { relays as relaysApi } from "./api";
import { plain, type Plain } from "./plain";
import { pollMs } from "./queries";

// Relays (relay.proto): a port of an entry node that passes a server of another node (the exit) on, unchanged. Owner only.
// The node page shows them twice: on the entry, the list with its checks and switches (screens/node/relays.tsx); on the
// exit, a quiet line. Everything worded here is what that page says; the codes come from the panel, the words are
// i18n/relay.ts.

export type RelayMsg = Plain<Relay>;
export type Candidate = Plain<RelayCandidate>;
export type Stage = Plain<RelayStage>;
export type CountryHint = Plain<RelayCountryHint>;
export type ReachHint = Plain<RelayReachHint>;

/** The relays with this node at either end, oldest first. Under ["relays"], so a change refreshes every node page. */
export const relaysQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["relays", nodeId],
    queryFn: async ({ signal }) => plain(await relaysApi.listRelays({ nodeId }, { signal })).relays,
    refetchInterval: pollMs,
  });

/** What the entry could pass on, with the hints. Read once a minute at most: it builds every server of the fleet. */
export const candidatesQuery = (viaNodeId: string) =>
  queryOptions({
    queryKey: ["relays", "candidates", viaNodeId],
    queryFn: async ({ signal }) => plain(await relaysApi.listRelayCandidates({ viaNodeId }, { signal })),
    staleTime: 60_000,
  });

/** How fresh a passing leg check must be to switch a relay on (relay_check.go relayCheckTTL). */
export const checkTtlS = 15 * 60;

/** Failed rounds in a row that make a relay that is on "down", as they open its alert. */
export const monitorStreak = 2;

/** The row's state: what the agent, the last check and the panel's rounds through it say. "checking" is the page's own, while a call runs. */
export type RelayState = "on" | "off" | "failed" | "applying" | "checking";

export function relayState(r: Pick<RelayMsg, "enabled" | "state" | "check" | "monitor">): Exclude<RelayState, "checking"> {
  if (r.enabled && r.state === InboundState.FAILED) return "failed"; // the entry could not install the forward
  if (r.check && !r.check.ok) return "failed"; // the last check failed: switched off by it, or found broken while on
  // the panel's rounds through it failed twice running: what opens the relay_down alert (relay_round.go failStreakMin)
  if (r.enabled && r.monitor && !r.monitor.ok && r.monitor.failStreak >= monitorStreak) return "failed";
  if (r.enabled) return r.state === InboundState.ACTIVE ? "on" : "applying";
  return "off";
}

export const statePill: Record<RelayState, { kind: StatusKind; word: MessageKey }> = {
  on: { kind: "ok", word: "relay.state.on" },
  off: { kind: "off", word: "relay.state.off" },
  failed: { kind: "bad", word: "relay.state.failed" },
  applying: { kind: "busy", word: "relay.state.applying" },
  checking: { kind: "busy", word: "relay.state.checking" },
};

/** Why "Turn on" is not offered yet, or "" when it is: the panel's gate (a passing leg check under 15 min), said before the click. */
export type Gate = "" | "agent_old" | "exit_down" | "unchecked" | "failed" | "stale";

export function enableGate(r: Pick<RelayMsg, "agentSupported" | "exitLive" | "check" | "network">, nowS: number): Gate {
  if (!r.agentSupported) return "agent_old";
  if (!r.exitLive) return "exit_down";
  const leg = r.check?.leg;
  if (!leg?.status) return "unchecked";
  if (leg.status !== "ok") return "failed";
  if (nowS - leg.atUnix > checkTtlS || r.check!.network !== r.network) return "stale";
  return "";
}

/** The names a reason sentence fills in. */
export type RelayNames = { entry: string; exit: string; exitPort: number; port: number; network: string };

export const namesOf = (r: Pick<RelayMsg, "viaNodeName" | "exitNodeName" | "exitPort" | "port" | "network">): RelayNames => ({
  entry: r.viaNodeName,
  exit: r.exitNodeName,
  exitPort: r.exitPort,
  port: r.port,
  network: r.network,
});

// Codes of a failed stage with a sentence of their own; the rest ("refused", "timeout", "no_route", "failed", a new one) read
// as the stage's plain reason.
const ownWords = ["node_offline", "exit_offline", "agent_too_old", "no_sender", "same_host", "busy", "unverifiable", "spec", "apply_failed", "apply_timeout", "switched_off"] as const;

/**
 * A failed stage in words: "msk1 cannot reach de1 on 443/tcp" (leg), "8443/tcp on msk1 is not reachable from outside" (path),
 * or the code's own sentence. The leg is about the exit's port, the path about the entry's.
 */
export function stageReason(t: T, stage: "leg" | "path", s: Pick<Stage, "code" | "verdict" | "sent" | "got">, n: RelayNames): string {
  const port = stage === "leg" ? n.exitPort || n.port : n.port;
  const vars = { entry: n.entry, exit: n.exit, port, net: n.network || "tcp", got: s.got, sent: s.sent };
  if ((ownWords as readonly string[]).includes(s.code)) return t(`relay.why.${s.code as (typeof ownWords)[number]}`, vars);
  if (s.code === "other" || s.verdict === "other") return t(`relay.why.other.${stage}`, vars);
  if ((s.code === "lossy" || s.code === "broken") && s.sent > 0) return t(`relay.why.udp.${stage}`, vars);
  return t(`relay.why.${stage}`, vars);
}

/** The first failed stage of the relay's last check in words, or "" when none failed. */
export function checkReason(t: T, r: Pick<RelayMsg, "check" | "viaNodeName" | "exitNodeName" | "exitPort" | "port" | "network">): string {
  const c = r.check;
  if (!c || c.ok) return "";
  const stage = c.stage === "path" ? "path" : "leg";
  const s = c[stage];
  return s ? stageReason(t, stage, s, namesOf(r)) : "";
}

/** What the entry's agent reported when it could not install the forward ("forward_drop", "resolve: …", "refused: …"). */
export function agentError(t: T, lastError: string, n: Pick<RelayNames, "entry" | "exit">): string {
  const head = lastError.split(":")[0]!.trim();
  const key = ({ forward_drop: "relay.err.forward_drop", resolve: "relay.err.resolve", refused: "relay.err.refused", unsupported_host: "relay.err.unsupported_host" } as const)[head];
  return t(key ?? "relay.err.other", n);
}

/** The exits a relay is for: some networks of the audience country see them dark. */
export const isCut = (h?: Pick<CountryHint, "verdict">) => h?.verdict === "blocked" || h?.verdict === "partial";

export type Hint = { tone: StatusKind; text: string };

/** "From RU: blocked on MTS, Tele2": the exit's country check. */
export function countryHint(t: T, cc: string, h?: CountryHint): Hint {
  const networks = (h?.dark ?? []).join(", ");
  switch (h?.verdict) {
    case "blocked":
    case "partial":
      return { tone: h.verdict === "blocked" ? "bad" : "warn", text: networks ? t("relay.hint.country.cut", { cc, networks }) : t("relay.hint.country.cutAll", { cc }) };
    case "ok":
      return { tone: "ok", text: t("relay.hint.country.ok", { cc }) };
    case "listed":
      return { tone: "warn", text: t("relay.hint.country.listed", { cc }) };
  }
  return { tone: "off", text: t("relay.hint.country.no_data", { cc }) };
}

/** Does the entry reach the exit: the TSPU probe's row for it ("msk1 → de1 passes"), or "no TSPU on the entry's path", or no data. */
export function reachHint(t: T, entry: string, exit: string, h?: ReachHint): Hint {
  switch (h?.verdict) {
    case "reach":
      return h.source === "refs_pass" ? { tone: "ok", text: t("relay.hint.reach.refs", { entry }) } : { tone: "ok", text: t("relay.hint.reach.tspu", { entry, exit }) };
    case "partial":
      return { tone: "warn", text: t("relay.hint.reach.partial", { entry, exit }) };
    case "blocked":
      return { tone: "bad", text: t("relay.hint.reach.blocked", { entry, exit }) };
  }
  return { tone: "off", text: t("relay.hint.reach.no_data", { entry }) };
}

/**
 * Do people reach the entry on the networks that see the exit dark? Only for an exit that is dark somewhere (else there is
 * nothing to compare): the entry dark on some of them too means the relay does not help there.
 */
export function seenHint(t: T, entry: string, exit?: CountryHint, entryHint?: CountryHint): Hint | null {
  const dark = exit?.dark ?? [];
  if (!isCut(exit) || dark.length === 0) return null;
  if (!entryHint || entryHint.verdict === "no_data" || !entryHint.verdict) return { tone: "off", text: t("relay.hint.seen.no_data", { entry }) };
  const both = dark.filter((x) => entryHint.dark.includes(x));
  if (both.length > 0) return { tone: "bad", text: t("relay.hint.seen.no", { entry, networks: both.join(", ") }) };
  return { tone: "ok", text: t("relay.hint.seen.yes", { entry, networks: dark.join(", ") }) };
}

export type CandidateNode = { nodeId: string; nodeName: string; servers: Candidate[]; suggested: boolean };

/**
 * The candidates by exit node, in the panel's order (the ones that can be relayed first, the cut exits among them first):
 * the suggested nodes (cut, with a server that can be relayed), then the others.
 */
export function groupCandidates(list: Candidate[]): { suggested: CandidateNode[]; others: CandidateNode[] } {
  const byNode = new Map<string, CandidateNode>();
  for (const c of list) {
    const g = byNode.get(c.nodeId) ?? { nodeId: c.nodeId, nodeName: c.nodeName, servers: [], suggested: false };
    g.servers.push(c);
    byNode.set(c.nodeId, g);
  }
  const all = [...byNode.values()];
  // what can be picked first in each node: then the ones relayed already, then the ones that cannot be
  const rank = (c: Candidate) => (c.unsupported ? 2 : c.relayId ? 1 : 0);
  for (const g of all) {
    g.servers.sort((a, b) => rank(a) - rank(b));
    g.suggested = g.servers.some((c) => !c.unsupported && isCut(c.country));
  }
  return { suggested: all.filter((g) => g.suggested), others: all.filter((g) => !g.suggested) };
}
