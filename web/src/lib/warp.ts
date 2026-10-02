import { WarpSource, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { MessageKey } from "@/i18n/en";
import type { StatusKind } from "@/components/ui/status";

// What the WARP card of a node shows: the state as a status pill, the source and the links. No server data is made up here.

export const warpKind: Record<WarpState, StatusKind> = {
  [WarpState.UNSPECIFIED]: "off",
  [WarpState.NOT_CONFIGURED]: "off",
  [WarpState.UNKNOWN]: "off",
  [WarpState.UNAVAILABLE]: "bad",
  [WarpState.STARTING]: "busy",
  [WarpState.UP]: "ok",
  [WarpState.DOWN]: "bad",
  [WarpState.DISABLED]: "off",
};

export const warpWord: Record<WarpState, MessageKey> = {
  [WarpState.UNSPECIFIED]: "warp.state.none",
  [WarpState.NOT_CONFIGURED]: "warp.state.none",
  [WarpState.UNKNOWN]: "warp.state.unknown",
  [WarpState.UNAVAILABLE]: "warp.state.unavailable",
  [WarpState.STARTING]: "warp.state.starting",
  [WarpState.UP]: "warp.state.up",
  [WarpState.DOWN]: "warp.state.down",
  [WarpState.DISABLED]: "warp.state.paused",
};

/** The pill of the card. The node's own state decides, except that a failing latest check must not hide behind a spinner
 * ("Starting" while every other check fails) or a green "Working": those two say so in words and take the warn tone. */
export function warpPill(state: WarpState, checkFailing: boolean): { kind: StatusKind; word: MessageKey } {
  if (checkFailing && state === WarpState.STARTING) return { kind: "warn", word: "warp.state.startingFailing" };
  if (checkFailing && state === WarpState.UP) return { kind: "warn", word: "warp.state.upFailing" };
  return { kind: warpKind[state], word: warpWord[state] };
}

/**
 * What is wrong with an account that one button fixes, so that button leads the card: Cloudflare revoked the account
 * ("register again", one step), or the account is alive but the tunnel does not come up ("restart WARP", pause and resume
 * in one go). `since` is the last handshake (0 = never), `checks` the failed checks in a row. Null for everything else: a
 * paused or working account, and host conflicts a restart cannot clear, which keep their own words.
 */
export type WarpTrouble = { kind: "revoked" } | { kind: "down"; since: number; checks: number } | null;

export function warpTrouble(d: {
  account?: { enabled: boolean };
  health?: { state: WarpState; lastHandshakeUnix: number; consecutiveFailures: number };
  attentionReason: string;
}): WarpTrouble {
  if (!d.account) return null;
  if (d.attentionReason === "revoked") return { kind: "revoked" };
  if (!d.account.enabled || !d.health) return null;
  if (d.health.state === WarpState.DOWN || d.attentionReason === "down_after_ladder") {
    return { kind: "down", since: d.health.lastHandshakeUnix, checks: d.health.consecutiveFailures };
  }
  return null;
}

/** A probe that passes but takes longer than this is "slow". */
export const slowProbeMs = 2000;

export type ProbeLook = { kind: StatusKind; word: MessageKey | null; ms: number | null; failure: MessageKey | null; httpStatus: number | null };

const probeFailureWords: Record<string, MessageKey> = {
  timeout: "warp.probe.error.timeout",
  cancelled: "warp.probe.error.other",
  dns: "warp.probe.error.dns",
  connection: "warp.probe.error.connection",
  network: "warp.probe.error.network",
  invalid_trace: "warp.probe.error.trace",
  warp_off: "warp.probe.error.warpOff",
  other: "warp.probe.error.other",
};

function probeFailure(code: string | undefined): Pick<ProbeLook, "failure" | "httpStatus"> {
  const status = /^http_(\d{3})$/.exec(code ?? "");
  if (status) return { failure: "warp.probe.httpStatus", httpStatus: Number(status[1]) };
  return { failure: code ? probeFailureWords[code] ?? "warp.probe.error.other" : null, httpStatus: null };
}

/**
 * One probe dot of the card, from the LATEST check: failed = red "no answer", passed but slow = warn, passed = ok, each with
 * its latency. `p` is the per-probe result of a node that sends it; an agent that predates it gives only `legacyOk` (the
 * flag of the last check, no timing); `checked` says whether the node sent any check time (a new agent that did not run
 * the probe, e.g. the link was down, has `checked` and no `p`: nothing to claim, "—").
 */
export function probeLook(p: { ok: boolean; latencyMs: number; failureCode?: string } | undefined, legacyOk: boolean | undefined, checked: boolean): ProbeLook {
  if (p) {
    if (!p.ok) return { kind: "bad", word: "warp.probe.fail", ms: null, ...probeFailure(p.failureCode) };
    return p.latencyMs > slowProbeMs
      ? { kind: "warn", word: "warp.probe.slow", ms: p.latencyMs, failure: null, httpStatus: null }
      : { kind: "ok", word: "warp.probe.ok", ms: p.latencyMs, failure: null, httpStatus: null };
  }
  if (checked || legacyOk === undefined) return { kind: "off", word: null, ms: null, failure: null, httpStatus: null };
  return legacyOk
    ? { kind: "ok", word: "warp.probe.ok", ms: null, failure: null, httpStatus: null }
    : { kind: "bad", word: "warp.probe.fail", ms: null, failure: null, httpStatus: null };
}

export const sourceWord: Record<WarpSource, MessageKey> = {
  [WarpSource.UNSPECIFIED]: "warp.source.none",
  [WarpSource.REGISTERED]: "warp.source.registered",
  [WarpSource.IMPORTED]: "warp.source.imported",
};

/** The terms link only when it is a plain http(s) URL (it comes from the panel, but a link in the UI is never trusted blind). */
export const safeHttpUrl = (u: string) => (/^https?:\/\/[^\s]+$/i.test(u.trim()) ? u.trim() : "");

/** A wgcf profile, as far as the dialog can tell before the panel looks at it: it has an [Interface] and a [Peer]. */
export const looksLikeWgcfProfile = (text: string) => /\[Interface\]/i.test(text) && /\[Peer\]/i.test(text) && /PrivateKey\s*=/i.test(text);

/** "3 min ago" style age of a unix time for the card (seconds, minutes, hours, days); "" for never. */
export function ageParts(unix: number, now: number): { unit: "s" | "m" | "h" | "d"; n: number } | null {
  if (unix <= 0) return null;
  const s = Math.max(0, now - unix);
  if (s < 60) return { unit: "s", n: Math.floor(s) };
  if (s < 3600) return { unit: "m", n: Math.floor(s / 60) };
  if (s < 86400) return { unit: "h", n: Math.floor(s / 3600) };
  return { unit: "d", n: Math.floor(s / 86400) };
}
