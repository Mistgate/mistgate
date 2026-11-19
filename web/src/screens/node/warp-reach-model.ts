import type { StatusKind } from "@/components/ui/status";
import { WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { WarpData } from "./warp-model";

// What the reach card says about a node: the panel's answer (GetWarpResponse.reach / reach_state, see proto warp.proto) read as one
// look. No server data is made up here: the card shows what the node reported and says plainly when there is none.

/** One site's answer. A verdict the page does not know reads as "error": the panel may grow one before the page does. */
export type ReachVerdict = "ok" | "blocked" | "unreachable" | "error";
export type ReachRow = { domain: string; verdict: ReachVerdict; status: number; ms: number; streak: number };

/** The status pill and the word of a verdict (a block page is the only red one: the other two are "look at it by hand"). */
export const verdictKind: Record<ReachVerdict, StatusKind> = { ok: "ok", blocked: "bad", unreachable: "warn", error: "warn" };

/** How old a result may be before the card calls it old: the panel itself drops an alert on a result older than this. */
export const reachStaleS = 3 * 3600;

/**
 * The look of the card. `old`: the node's agent lacks "warp-reach/1"; `off`: the owner's list is empty (or the edition runs no check);
 * `wait`: nothing yet, the tunnel is up so the first round is on its way; `nodata`: nothing to show and nothing coming (the tunnel is not
 * up, WARP is paused, the node is silent); `ready`: a result. `paused` and `dead` say why a result is not renewed, `stale` that it is old.
 */
export type ReachLook = {
  state: "old" | "off" | "wait" | "nodata" | "ready";
  rows: ReachRow[];
  colo: string;
  checked: number;
  paused: boolean;
  dead: boolean;
  stale: boolean;
  /** The node is running a check of the sites now (asked by "Check now" or by the schedule). */
  running: boolean;
  /** How the newest check ended when it did not end well (busy, too_soon, warp_down, failed) and when; none when it did, or when the result that came since is newer. */
  failed: { code: string; atUnix: number } | null;
};

/** How long the card keeps saying that the newest check did not end well. */
export const reachFailedS = 10 * 60;

const known = new Set<string>(["ok", "blocked", "unreachable"]);

export function reachLook(d: WarpData, down: boolean, now: number): ReachLook {
  const paused = !!d.account && !d.account.enabled;
  const r = d.reach;
  const rows: ReachRow[] = (r?.domains ?? []).map((x) => ({
    domain: x.domain,
    verdict: known.has(x.verdict) ? (x.verdict as ReachVerdict) : "error",
    status: x.httpStatus,
    ms: x.latencyMs,
    streak: x.blockedStreak,
  }));
  const checked = r?.checkedUnix ?? 0;
  const rc = d.reachCheck;
  const failed = rc && rc.last && rc.last !== "ok" && rc.lastUnix >= checked && now - rc.lastUnix < reachFailedS ? { code: rc.last, atUnix: rc.lastUnix } : null;
  const base = { rows, colo: r?.colo ?? "", checked, paused, dead: down, stale: checked > 0 && now - checked > reachStaleS, running: !!rc?.running, failed };
  if (d.reachState === "agent_too_old") return { ...base, state: "old" };
  if (d.reachState === "disabled") return { ...base, state: "off" };
  if (d.reachState === "ready" && r) return { ...base, state: "ready" };
  // "waiting" (and a state this page does not know): the first round comes while the tunnel is up and the node answers
  const up = d.health?.state === WarpState.UP;
  return { ...base, state: up && !paused && !down ? "wait" : "nodata" };
}

/** How many sites answered with a block page, and how many with anything else but a page (error, no answer). */
export function reachCounts(rows: readonly ReachRow[]) {
  const blocked = rows.filter((x) => x.verdict === "blocked").length;
  return { blocked, trouble: rows.filter((x) => x.verdict === "error" || x.verdict === "unreachable").length, total: rows.length };
}
