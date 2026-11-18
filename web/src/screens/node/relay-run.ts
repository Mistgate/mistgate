import { Code, ConnectError } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";
import { markFresh } from "@/components/fx/registry";
import type { T } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { errorCode, errorText, errorVars } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { namesOf, stageReason, viaLabel, type Candidate, type RelayMsg, type Stage } from "@/lib/relay";
import { activeOf, blockedOf, bridgeReason, modeOf, reachReason, wantsReach, type Mode } from "@/lib/relay-bridge";

// What it takes to make one relay and turn it on (relay.proto): create it switched off (or take the one that is there), check the leg from the
// entry to the exit's server and, where a bridge may carry it, the exit's way to the entry, then switch it on, which makes the panel check the
// bridge and the path from outside. The dialog "Add relay" and the wizard "Make reachable" both run their servers through this, one after another,
// and each says in words how the run went: the sentence is made while it runs, in the language of the moment.

export type StageLook = "ok" | "fail" | "skipped" | "running" | "waiting";
/** One server while a run works on it. */
export type Run = {
  leg: StageLook;
  reach?: StageLook | null;
  bridge?: StageLook | null;
  path: StageLook | null;
  /** The leg did not pass and the bridge did: no red mark on it. */
  soft?: boolean;
  tone?: "ok" | "bad";
  text?: string;
  /** A free port the panel named when the one asked for was taken. */
  free?: number;
};
export type Entry = { id: string; name: string; cc: string };

export type RunCtx = {
  t: T;
  qc: QueryClient;
  entry: Entry;
  /** How often the apps ask for the subscription (hours): the words say when people get the new server. */
  hours: number;
  /** The way asked for. A server that cannot use a bridge is made direct, or auto (it stays direct). */
  way: Mode;
  /** A relay of the run was switched on (the graph's "move" then offers to remove the old one). */
  onSuccess?: () => void;
};

export const lookOf = (s?: Stage): StageLook | null => (s?.status === "ok" ? "ok" : s?.status === "fail" ? "fail" : s?.status === "skipped" ? "skipped" : null);

/**
 * Makes the relay for `c` (off), checks it and, with `on`, switches it on. `existing` is a relay of the same server through this entry that is
 * already there (an earlier run was cut short): it is checked and turned on, nothing is made twice. `set` is told each step. True when the relay
 * ended up on.
 */
export async function runOne(ctx: RunCtx, c: Candidate, on: boolean, port: string, set: (patch: Partial<Run>) => void, existing?: RelayMsg): Promise<boolean> {
  const { t, qc, entry, hours } = ctx;
  const names = { entry: entry.name, exit: c.nodeName, exitPort: c.port, port: Number(port) || c.port, network: c.network };
  const pair = { entry: entry.name, exit: c.nodeName };
  const asked: Mode = blockedOf(c) !== "" && ctx.way === "reverse" ? "auto" : ctx.way;
  set({ leg: "running", reach: null, bridge: null, path: on ? "waiting" : null, soft: false, tone: undefined, text: undefined, free: undefined });
  // the first relay between these two nodes makes their lane: only that one sweeps in (a second relay joins a lane that is there)
  const hadLane = qc.getQueryData<RelayMsg[]>(["relays", c.nodeId])?.some((x) => x.viaNodeId === entry.id && x.exitNodeId === c.nodeId) ?? false;
  let r: RelayMsg;
  try {
    r = existing ?? plain(await relaysApi.createRelay({ inboundId: c.inboundId, viaNodeId: entry.id, port: Number(port) || 0, ...(asked !== "auto" ? { mode: asked } : {}) })).relay!;
    // a relay that is new grows into its row when the list reads it (the board's nd-new), and sweeps
    if (!existing) markFresh("relay", r.id);
    r = plain(await relaysApi.checkRelay({ id: r.id, ...(wantsReach(r) ? { reverseReach: true } : {}) })).relay!;
  } catch (e) {
    const ce = ConnectError.from(e);
    const free = errorCode(ce.rawMessage) === "port_taken" ? Number(errorVars(ce.rawMessage).free) || 0 : 0;
    set({ leg: "skipped", path: null, tone: "bad", text: errorText(e, t), free });
    return false;
  }
  const leg = r.check?.leg;
  const reach = r.check?.reverseReach;
  const reachLook = reach?.status ? (lookOf(reach) ?? "skipped") : null;
  const legOk = leg?.status === "ok";
  const bridgeOk = blockedOf(r) === "" && reach?.status === "ok";
  const m = modeOf(r);
  // direct, bridge, or either: what the check found is enough to go on
  const usable = m === "dnat" ? legOk : m === "reverse" ? bridgeOk : legOk || bridgeOk;
  const viaBridge = m === "reverse" || (m === "auto" && !legOk && bridgeOk);
  // the panel's refusal of a switch-on names the bridge's code beside the leg's: both failed
  const withBridge = (reason: string, code?: string) => (code ? t("relay.why.both", { reason, bridge: reachReason(t, { code, port: 0 }, pair) }) : reason);
  if (!usable) {
    const direct = leg ? stageReason(t, "leg", leg, namesOf(r)) : t("relay.why.leg", { ...names, port: c.port, net: c.network });
    const reason = m === "reverse" ? (reach ? reachReason(t, reach, pair) : direct) : m === "auto" && reach?.status === "fail" ? t("relay.why.both", { reason: direct, bridge: reachReason(t, reach, pair) }) : direct;
    set({ leg: legOk ? "ok" : "fail", reach: reachLook, path: on ? "skipped" : null, tone: "bad", text: t(on ? "relay.dlg.result.fail" : "relay.dlg.result.failCheck", { reason }) });
    return false;
  }
  if (!on) {
    set({ leg: legOk ? "ok" : "fail", reach: reachLook, soft: !legOk, tone: "ok", text: viaBridge && !legOk ? t("relay.dlg.result.checkedBridge", pair) : t("relay.dlg.result.checked") });
    return false;
  }
  set({ leg: legOk ? "ok" : "fail", reach: reachLook, soft: !legOk, bridge: viaBridge ? "running" : null, path: viaBridge ? "waiting" : "running" });
  try {
    const done = plain(await relaysApi.updateRelay({ id: r.id, enabled: true })).relay;
    if (!hadLane) {
      markFresh("lane", `${c.nodeId}:via:${entry.id}`); // on the exit's page ...
      markFresh("lane", `${entry.id}:out:${c.nodeId}`); // ... and on the entry's
    }
    if (existing) markFresh("relay", r.id); // a relay that was there already: it sweeps when it comes on
    // what the panel settled on, else what the check said
    const bridged = done ? activeOf(done) === "reverse" : viaBridge;
    set({ bridge: bridged ? "ok" : null, path: "ok", tone: "ok", text: t(bridged ? "relay.dlg.result.onBridge" : "relay.dlg.result.on", { via: viaLabel(t, entry.cc), hours }) });
    ctx.onSuccess?.();
    return true;
  } catch (e) {
    const ce = ConnectError.from(e);
    const v = errorVars(ce.rawMessage);
    const failed = ce.code === Code.FailedPrecondition && errorCode(ce.rawMessage) === "relay_check_failed";
    // the stage that failed: the leg (and the bridge's reach beside it), the bridge, or the path from outside
    const stage = v.stage === "leg" || v.stage === "reverse_reach" || v.stage === "bridge" ? v.stage : "path";
    const reason = !failed
      ? errorText(e, t)
      : stage === "bridge"
        ? bridgeReason(t, { code: v.code ?? "" }, pair)
        : stage === "reverse_reach"
          ? reachReason(t, { code: v.code ?? "", port: 0 }, pair)
          : withBridge(stageReason(t, stage, { code: v.code ?? "", verdict: "", sent: 0, got: 0 }, namesOf(r)), stage === "leg" ? v.reverse_reach : undefined);
    set({ bridge: stage === "bridge" ? "fail" : viaBridge ? "ok" : null, path: stage === "bridge" ? "skipped" : "fail", tone: "bad", text: failed ? t("relay.dlg.result.fail", { reason }) : reason });
    return false;
  }
}
