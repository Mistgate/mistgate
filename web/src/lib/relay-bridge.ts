import type { StatusKind } from "@/components/ui/status";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Fmt } from "./format";
import type { RelayMsg, Stage } from "./relay";

// The bridge of a relay (relay.proto, the reverse-relay plan §7-§8): the way a relay works is "direct" (the entry connects to
// the exit's server: dnat) or "reverse" (the exit connects to the entry and keeps the line open: the bridge). The owner picks
// the mode (auto, direct, bridge); the panel keeps `active_mode`, and says why in `mode_reason`. Here are the pure parts the
// screens share; the words are in i18n/relay.ts.

export type Mode = "auto" | "dnat" | "reverse";
export type Active = "dnat" | "reverse";
export const modes: readonly Mode[] = ["auto", "dnat", "reverse"];

/** The owner's choice; an empty mode (a panel before the bridge) is auto. */
export const modeOf = (r: { mode?: string }): Mode => (r.mode === "dnat" || r.mode === "reverse" ? r.mode : "auto");

/** The way that stands now; an empty one (a panel before the bridge) is direct. */
export const activeOf = (r: { activeMode?: string }): Active => (r.activeMode === "reverse" ? "reverse" : "dnat");

/** Why the bridge is not possible for a server through this entry ("" = it is). A UDP server is blocked even for a panel that did not say. */
export type Blocked = "" | "udp" | "entry_agent_old" | "exit_agent_old";

export function blockedOf(x: { reverseBlocked?: string; network?: string }): Blocked {
  if (x.reverseBlocked === "udp" || x.reverseBlocked === "entry_agent_old" || x.reverseBlocked === "exit_agent_old") return x.reverseBlocked;
  return x.network === "udp" ? "udp" : "";
}

/** The sentence for a blocked bridge: UDP, or the agent that needs the update. */
export function blockedText(t: T, blocked: Blocked, n: { entry: string; exit: string }): string {
  if (blocked === "udp") return t("relay.way.udp");
  if (blocked === "") return "";
  return t("relay.way.update", { nodes: blocked === "entry_agent_old" ? n.entry : n.exit });
}

/** The reasons the panel gives for the way it stands in (Relay.mode_reason); "" is a relay that never changed. */
export const modeReasons = ["leg_failed", "direct_back", "bridge_failed", "owner", "reverse_unsupported", "udp"] as const;
export type ModeReason = (typeof modeReasons)[number];

/** The reason in words: why the relay is in the way it is, or "" when there is nothing to say (never changed, a UDP server). */
export function reasonText(t: T, r: Pick<RelayMsg, "modeReason" | "mode" | "activeMode" | "viaNodeName" | "exitNodeName" | "reverseBlocked" | "network">): string {
  const n = { entry: r.viaNodeName, exit: r.exitNodeName };
  switch (r.modeReason) {
    case "leg_failed":
    case "direct_back":
    case "bridge_failed":
      return t(`relay.way.why.${r.modeReason}`, n);
    case "owner":
      return t(activeOf(r) === "reverse" ? "relay.way.why.owner.reverse" : "relay.way.why.owner.dnat");
    case "reverse_unsupported": {
      const blocked = blockedOf(r);
      return blocked === "" ? t("relay.way.update", { nodes: `${n.entry}, ${n.exit}` }) : blockedText(t, blocked, n);
    }
    case "udp":
      return t("relay.way.udp");
  }
  return "";
}

/** Checks of the direct leg in a row that send a relay back to the direct way (the controller's streak). */
export const directStreakGoal = 3;

// ---- the load of the bridge ----

export type BridgeLoad = { clients: number; capacity: number; percent: number; nearFull: boolean; atLimit: boolean; pool: number; segments: number };

/** People on the bridge against what its pool carries; null when the panel gave no capacity (nothing to compare). */
export function bridgeLoad(b?: Pick<NonNullable<RelayMsg["bridge"]>, "clients" | "capacity" | "nearFull" | "pool" | "error">): BridgeLoad | null {
  if (!b || b.capacity <= 0) return null;
  const percent = Math.min(100, Math.round((b.clients / b.capacity) * 100));
  const atLimit = b.clients >= b.capacity || limitErrors.has(b.error);
  return { clients: b.clients, capacity: b.capacity, percent, nearFull: !atLimit && (b.nearFull || b.clients * 5 >= b.capacity * 4), atLimit, pool: b.pool, segments: Math.min(10, Math.ceil(percent / 10)) };
}

/** What an agent says when the bridge turns a person away: the bridge is at its limit. */
const limitErrors = new Set(["stream_unavailable", "queue_full", "stream_limit"]);

/** At pool 4 the bridge cannot grow: the way to more room is a second entry. */
export const bridgeMaxPool = 4;

// ---- the words of the bridge's errors and stages ----

const errorKeys = {
  disconnected: "relay.bridge.err.disconnected",
  stale: "relay.bridge.err.stale",
  warming: "relay.bridge.err.warming",
  no_verification: "relay.bridge.err.no_verification",
  pin_mismatch: "relay.bridge.err.pin_mismatch",
  connect: "relay.bridge.err.connect",
  tls_protocol: "relay.bridge.err.tls",
  tls_exporter: "relay.bridge.err.tls",
  http2: "relay.bridge.err.tls",
  connection_limit: "relay.bridge.err.connection_limit",
  stream_unavailable: "relay.bridge.err.limit",
  queue_full: "relay.bridge.err.limit",
  stream_limit: "relay.bridge.err.limit",
  pair_timeout: "relay.bridge.err.pair_timeout",
  target_connect: "relay.bridge.err.target_connect",
  identity_unavailable: "relay.bridge.err.identity_unavailable",
  site_listen_failed: "relay.bridge.err.site",
  site_certificate_unavailable: "relay.bridge.err.site",
  port_conflict: "relay.bridge.err.site",
  timeout_unsupported: "relay.bridge.err.site",
  bad_config: "relay.bridge.err.config",
  target_not_local: "relay.bridge.err.config",
} as const satisfies Record<string, MessageKey>;

/** An agent's "request_idle" is the bridge waiting for people, not an error. */
const quietErrors = new Set(["", "request_idle", "closed"]);

/** What an agent's word for the bridge says, as a fragment ("{exit} does not hold the bridge to {entry}"); "" for a word that is no error. */
export function bridgeErrorText(t: T, code: string, n: { entry: string; exit: string }): string {
  if (quietErrors.has(code)) return "";
  const key = (errorKeys as Record<string, MessageKey>)[code] ?? "relay.bridge.err.other";
  return t(key, { ...n, code });
}

// ---- what the bridge's problem is and what is done about it (the board's "Ошибка моста": one line, its tone and its button) ----

/** The button under a bridge problem: check again, check the speed, or open the page that holds the fix. */
export type BridgeAction = "check" | "recheck" | "speed" | "profilesEntry" | "profilesExit" | "server" | "doctorEntry";
/**
 * `crit`: the bridge does not hold, the line starts "The bridge is down:" (`head` false: the sentence says it itself); `warn` and
 * `mut` are a bridge that holds or will in a while (a limit, a pin to wait for, a restart): no head, no button, it passes on its own.
 */
export type BridgeProblem = { tone: "crit" | "warn" | "mut"; head: boolean; /** The sentence starts with "the bridge" in small letters, and stands on its own: it gets a capital. */ capital?: boolean; action?: BridgeAction };

/** The words of an agent that are no failure of the bridge: a pause, not a problem. */
const waiting: Record<string, BridgeProblem> = {
  no_verification: { tone: "mut", head: false },
  closed: { tone: "mut", head: false, capital: true },
  connection_limit: { tone: "warn", head: false },
};
const actions: Record<string, BridgeAction> = {
  pin_mismatch: "recheck",
  bad_config: "recheck",
  target_not_local: "recheck",
  tls_protocol: "profilesEntry",
  tls_exporter: "profilesEntry",
  http2: "profilesEntry",
  pair_timeout: "speed",
  target_connect: "profilesExit",
  identity_unavailable: "server",
  site_listen_failed: "doctorEntry",
  site_certificate_unavailable: "doctorEntry",
  port_conflict: "doctorEntry",
  timeout_unsupported: "doctorEntry",
};

/** Whether the code is one of the quiet waiting states: shown even when the bridge has not failed. */
export const bridgeWaits = (code: string): boolean => code in waiting;

/** The tone and the button of an agent's word for the bridge; a word that is not known is a bridge that is down, with "check". */
export function bridgeProblem(code: string): BridgeProblem {
  const w = waiting[code];
  if (w) return w;
  return { tone: "crit", head: code !== "pair_timeout", capital: code === "pair_timeout", action: actions[code] ?? "check" };
}

/** Why the exit's check towards the entry's bridge failed or was skipped ("reverse_reach"), as a fragment. */
export function reachReason(t: T, s: Pick<Stage, "code" | "port">, n: { entry: string; exit: string }): string {
  const vars = { ...n, port: s.port || 443 };
  switch (s.code) {
    case "timeout":
    case "other":
    case "entry_site_unavailable":
    case "unsupported":
      return t(`relay.why.reach.${s.code}`, vars);
    case "exit_offline":
      return t("relay.why.exit_offline", vars);
    case "node_offline":
      return t("relay.why.node_offline", vars);
  }
  return t("relay.why.reach", vars);
}

/** Why the bridge stage of a check failed, as a fragment. */
export const bridgeReason = (t: T, s: Pick<Stage, "code">, n: { entry: string; exit: string }): string => bridgeErrorText(t, s.code, n) || t("relay.bridge.err.disconnected", n);

/** The reason of a speed test that did not pass, as a fragment. `mb` is the size that was asked for. */
export function speedReason(t: T, s: Pick<Stage, "status" | "code" | "bytes" | "mb">, n: { entry: string; exit: string }): string {
  const mb = s.mb || 8;
  if (s.status === "skipped") return s.code === "busy" ? t("relay.speed.skip.busy") : s.code === "unsupported" ? t("relay.speed.skip.unsupported") : t("relay.speed.fail.failed");
  switch (s.code) {
    case "timeout":
      return t("relay.speed.fail.timeout", { ...n, mb, got: Math.floor((s.bytes || 0) / 1e6) });
    case "unreachable":
      return t("relay.speed.fail.unreachable", n);
    case "entry_offline":
      return t("relay.why.node_offline", n);
    case "exit_offline":
      return t("relay.why.exit_offline", n);
  }
  return t("relay.speed.fail.failed");
}

/** "31 Mbit/s · 8 MB in 2.1 s" for a speed test that passed. */
export function speedOk(t: T, fmt: Fmt, s: Pick<Stage, "mbitS" | "mb" | "durationMs">): string {
  return t("relay.speed.ok", { speed: fmt.mbit(s.mbitS * 1e6), mb: s.mb || 8, time: `${fmt.num(s.durationMs / 1000, 1)} ${t("unit.s")}` });
}

/** A stage's look: its colour and whether it ran. */
export const speedKind = (s?: Pick<Stage, "status">): StatusKind => (s?.status === "ok" ? "ok" : s?.status === "fail" ? "bad" : "off");

// ---- what a row shows ----

/** The way a candidate's note mentions: bridge possible, none, UDP, an agent to update; null when there is nothing to say. */
export type BridgeNote = { tone: StatusKind; text: string };

/**
 * The note under a server in the "Add relay" dialog about the bridge. The direct leg is "cut" when the TSPU probe says the entry
 * does not reach the exit (blocked, partly); only then is a bridge worth a word, and a UDP server is never offered one.
 */
export function bridgeNote(
  t: T,
  c: { reverseBlocked: string; network: string; reach?: { verdict: string }; exitReachesEntry?: { verdict: string } },
  n: { entry: string; exit: string },
): BridgeNote | null {
  const cut = c.reach?.verdict === "blocked" || c.reach?.verdict === "partial";
  const blocked = blockedOf(c);
  if (blocked === "udp") return cut ? { tone: "off", text: t("relay.hint.bridge.udp") } : null;
  if (blocked !== "") return cut ? { tone: "warn", text: blockedText(t, blocked, n) } : null;
  const back = c.exitReachesEntry?.verdict;
  if (cut && (back === "reach" || back === "partial")) return { tone: "ok", text: t("relay.hint.bridge.possible", n) };
  if (c.reach?.verdict === "blocked" && back === "blocked") return { tone: "bad", text: t("relay.hint.bridge.none", n) };
  return null;
}

// ---- the state a relay is in, by the way it works ----

/**
 * The direct leg failing is not a failure of the relay when the bridge carries it (it stands in the bridge: the failing leg is
 * why), or can (the exit reached the entry's bridge a moment ago and the owner did not choose direct only).
 */
export function bridgeCarries(r: Pick<RelayMsg, "enabled" | "check"> & { mode?: string; activeMode?: string; reverseBlocked?: string; network?: string }): boolean {
  const c = r.check;
  if (!c || c.ok || c.stage !== "leg") return false;
  if (r.enabled && activeOf(r) === "reverse") return true;
  return modeOf(r) !== "dnat" && blockedOf(r) === "" && c.reverseReach?.status === "ok";
}

/** A relay that stands in the bridge, switched on, whose exit does not hold it: people are turned away. */
export const bridgeFailed = (r: Pick<RelayMsg, "check"> & { enabled?: boolean; activeMode?: string }): boolean => !!r.enabled && activeOf(r) === "reverse" && r.check?.bridge?.status === "fail";

/** Ask for the exit's check towards the entry too: the relay may use a bridge, and the owner did not choose direct only. */
export const wantsReach = (r: { mode?: string; reverseBlocked?: string; network?: string }): boolean => !!r.mode && r.mode !== "dnat" && blockedOf(r) === "";
