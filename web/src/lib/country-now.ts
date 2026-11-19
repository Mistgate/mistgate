import type { CountryNow as CountryNowMsg } from "@/gen/mistgate/admin/v1/health_pb";
import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { hasKey, sourceName } from "./health";
import type { Plain } from "./plain";

// "Check now" of a node also asks the country check ("From Russia") for a round out of turn (health.proto CountryNow). The panel runs it in
// the background and keeps what each source answered; these helpers turn that into the words the node's Overview, its Ways tab and the
// Reachability page say under their "From Russia" line: what is being rechecked, what is not and why, when the next recheck may be asked and
// what Globalping has left. The radar is for a round that is going on, nothing else.

export type CountryNow = Plain<CountryNowMsg>;
export type NowSource = CountryNow["sources"][number];

export const nowOf = (list: readonly CountryNow[] | undefined, nodeId: string): CountryNow | undefined => list?.find((c) => c.nodeId === nodeId);

/** What the panel keeps of a round is shown for this long after it started; an older one is no news. */
const stale = 6 * 3600;

export type NowLook = {
  /** A round is going on now: the radar. */
  running: boolean;
  /** The sources the round asked and that are running or have answered. */
  asked: NowSource[];
  /** The ones given a job whose answer comes by itself later (the owner's own probes). */
  queued: NowSource[];
  /** The ones that took part and did not work. */
  failed: NowSource[];
  /** The ones that were left out, with the reason. */
  skipped: NowSource[];
  /** The ones whose stored result was read again. */
  reread: NowSource[];
  /** Seconds until the node may be asked again (0 = now). */
  nextIn: number;
  startedUnix: number;
  /** Globalping's tests left for a round by hand, what such a round costs and when that was read; none until it was asked. */
  gp: { left: number; cost: number; atUnix: number } | null;
  /** A round of the last press has a word to say. */
  any: boolean;
};

export function nowLook(now: CountryNow | undefined, nowSec: number): NowLook | null {
  if (!now) return null;
  const fresh = now.running || nowSec - now.askedUnix < stale;
  const by = (f: (s: NowSource) => boolean) => now.sources.filter(f);
  return {
    running: now.running,
    asked: by((s) => s.state === "running" || s.state === "done"),
    queued: by((s) => s.state === "queued"),
    failed: by((s) => s.state === "failed"),
    skipped: by((s) => s.state === "skipped"),
    reread: by((s) => s.state === "reread"),
    nextIn: Math.max(0, now.nextUnix - nowSec),
    startedUnix: now.startedUnix,
    gp: now.globalpingLeft === undefined ? null : { left: now.globalpingLeft, cost: now.globalpingCost, atUnix: now.globalpingAtUnix },
    any: fresh && now.sources.length > 0,
  };
}

/** The name of a source in the lines of a round: the sources of the combined verdict and the kl!ck reports. */
export function nowSourceName(t: T, source: string): string {
  return source === "klick" ? t("cn.src.klick") : sourceName(t, source);
}

/** Why a source was left out or did not work, in words; a code this page does not know says only that it did not work. */
export function nowReason(t: T, reason: string): string {
  const key = `cn.reason.${reason}`;
  return hasKey(key) ? t(key as MessageKey) : t("cn.reason.unknown");
}

/** "bsbord: on its schedule only": a source and why it is not rechecked. */
export const nowSkip = (t: T, s: NowSource) => t("cn.skip.item", { source: nowSourceName(t, s.source), why: nowReason(t, s.reason) });

/** What the press said, as one toast: a round was asked (and for how many sources), or when the next one may be. */
export function nowToast(t: T, now: CountryNow | undefined): string {
  if (!now) return "";
  if (now.retryAfterS > 0) return t("cn.toast.early", { min: Math.max(1, Math.ceil(now.retryAfterS / 60)) });
  const look = nowLook(now, Math.floor(Date.now() / 1000));
  if (look && look.asked.length + look.queued.length > 0) return t.n("cn.toast.asked", look.asked.length + look.queued.length);
  return t("cn.toast.none");
}
