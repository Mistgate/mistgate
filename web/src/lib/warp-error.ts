import type { T } from "@/i18n";
import { hasKey } from "./health";

// WARP's last_error in words. The agent (internal/node/warp/manager.go) sends the reason of the LATEST failed check as a
// short English code, then "; ladder: <note>" with the last recovery step it took:
//   reasons: probe_cloudflare_failed, probe_other_failed, warp_flag_<flag>, handshake_stale, handshake_never, link_down,
//            stat_failed, backend_unavailable, up_failed: <message>
//   notes:   reassert, reassert failed, rotated to <ip:port>, rotate <ip:port> failed: <err>, refresh requested,
//            owner action needed, every endpoint tried
// and the panel records why the owner has to decide (warp_account.attention): revoked, down_after_ladder, table_in_use,
// rule_pref_in_use, apply_failed. The card (screens/node/warp.tsx) and the doctor's warp_path line both read them through
// this file, so the two never word the same thing differently. Anything unknown falls back to the raw code.

const LADDER = "; ladder: ";

/** "probe_cloudflare_failed; ladder: reassert" -> reason "probe_cloudflare_failed", note "reassert"; a bare "ladder: x" has no reason. */
export function splitWarpError(raw: string): { reason: string; note: string } {
  const s = raw.trim();
  const i = s.indexOf(LADDER);
  if (i >= 0) return { reason: s.slice(0, i).trim(), note: s.slice(i + LADDER.length).trim() };
  if (s.startsWith("ladder: ")) return { reason: "", note: s.slice("ladder: ".length).trim() };
  return { reason: s, note: "" };
}

export type WarpErrorWords = {
  /** What happened, one sentence ("" when the code is unknown). */
  head: string;
  /** What it means for the profiles and what to do (may be empty). */
  more: string;
  /** The recovery step the node is on, as a sentence ("" when none or unknown). */
  ladder: string;
  /** The parts this file has no wording for, as the agent sent them (shown small, in mono). */
  raw: string;
};

function reasonWords(t: T, reason: string): { head: string; more: string } | null {
  if (!reason) return null;
  const code = reason.split(":")[0]!.trim();
  let key = `warp.e.${code}`;
  const params: Record<string, string> = {};
  if (code.startsWith("warp_flag_")) {
    key = "warp.e.warp_flag";
    params.flag = code.slice("warp_flag_".length);
  } else if (code === "up_failed") {
    params.detail = reason.slice(reason.indexOf(":") + 1).trim();
  }
  if (!hasKey(key)) return null;
  const more = `${key}.more`;
  return { head: t(key, params), more: hasKey(more) ? t(more, params) : "" };
}

function ladderWords(t: T, note: string): string | null {
  if (!note) return null;
  const rotated = /^rotated to (\S+)$/.exec(note);
  if (rotated) return t("warp.ladder.rotated", { ep: rotated[1]! });
  const key =
    note === "reassert" ? "warp.ladder.reassert"
    : note === "reassert failed" ? "warp.ladder.reassert_failed"
    : /^rotate \S+ failed/.test(note) ? "warp.ladder.rotate_failed"
    : note === "refresh requested" ? "warp.ladder.refresh"
    : note === "owner action needed" ? "warp.ladder.owner"
    : note === "every endpoint tried" ? "warp.ladder.exhausted"
    : null;
  return key && hasKey(key) ? t(key) : null;
}

/** The words for the agent's last_error; null when there is none. */
export function describeWarpError(t: T, lastError: string): WarpErrorWords | null {
  if (!lastError.trim()) return null;
  const { reason, note } = splitWarpError(lastError);
  const r = reasonWords(t, reason);
  const ladder = ladderWords(t, note);
  const raw = [r ? "" : reason, ladder === null ? note : ""].filter(Boolean).join("; ");
  return { head: r?.head ?? "", more: r?.more ?? "", ladder: ladder ?? "", raw };
}

/** One line for the doctor: the head (or the raw code) and the recovery step. */
export function warpErrorLine(t: T, lastError: string): string {
  const w = describeWarpError(t, lastError);
  return w ? [w.head || w.raw, w.ladder].filter(Boolean).join(" ") : "";
}

/** True when the last check failed: a reason is present (the ladder note alone says only what the node did). */
export const warpCheckFailed = (lastError: string) => splitWarpError(lastError).reason !== "";

/** Why the owner has to decide (the panel's attention code): the sentence, or null for a code without its own wording. */
export function attentionWords(t: T, code: string): string | null {
  const key = `warp.att.${code}`;
  return code && hasKey(key) ? t(key) : null;
}
