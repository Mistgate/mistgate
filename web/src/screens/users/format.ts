import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Tone } from "@/components/ui/icons";
import { agoOf, scaleBytes, type Fmt } from "@/lib/format";
import type { UserN as User } from "./model";
import type { Key, Tx } from "./t";

export { avatarIndex } from "@/components/ui/bits";

// Traffic is decimal across the panel (lib/format.ts), so a quota of "100 GB" is 100e9 bytes.
export const GB = 1e9;
export const DAY = 86400;

export const nowSec = () => Math.floor(Date.now() / 1000);

/** Whole days until `expiresUnix` (rounded up; <= 0 once it has passed); null = never expires. */
export function daysLeft(expiresUnix: number, now = nowSec()): number | null {
  return expiresUnix === 0 ? null : Math.ceil((expiresUnix - now) / DAY);
}

/**
 * The palette of a group's colour, in the order the server offers it and a new group takes the least used tone in (the
 * server's store.GroupTones). Sky and mint come last: they are also the Link and Keys chips.
 */
export const groupTones = ["lavender", "sand", "sage", "rose", "sky", "mint"] as const satisfies readonly Tone[];

/** The tone of a group's chip: the colour stored on the group, or, while it has none (""), one picked from its id, the same everywhere. */
export function groupTone(id: string, color = ""): Tone {
  const stored = groupTones.find((t) => t === color);
  if (stored) return stored;
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return groupTones[h % groupTones.length]!;
}

/** The tone fewest groups wear (the earliest of the palette on a tie): what a new group gets, as the server decides it. */
export function leastUsedTone(groups: readonly { color: string }[]): Tone {
  const used = (t: Tone) => groups.filter((g) => g.color === t).length;
  return groupTones.reduce((best, t) => (used(t) < used(best) ? t : best));
}

// What the status column shows. The API has four statuses; "devices full" is derived (devices_used >= limit)
// and only shown for an otherwise active user.
export type Shown = "active" | "expired" | "quota" | "devices" | "disabled";

export function shownStatus(u: User): Shown {
  switch (u.status) {
    case UserStatus.DISABLED:
      return "disabled";
    case UserStatus.EXPIRED:
      return "expired";
    case UserStatus.LIMITED:
      return "quota";
    default:
      return u.deviceLimit > 0 && u.devicesUsed >= u.deviceLimit ? "devices" : "active";
  }
}

/** Active in the API's eyes, yet the person's page would give nothing: no subscription link, no AmneziaVPN keys, no Telegram. */
export const noAccess = (u: Pick<User, "accessHapp" | "accessAmnezia" | "accessTelegram">) => !u.accessHapp && !u.accessAmnezia && !u.accessTelegram;

export const shownKey: Record<Shown, Key> = {
  active: "users.status.active",
  expired: "users.status.expired",
  quota: "users.status.quota",
  devices: "users.status.devices",
  disabled: "users.status.disabled",
};

/**
 * Traffic against the quota: `used` in the quota's unit ("182", "3.4") and `of` the quota ("500 GB"), or `used` with its
 * own unit and `of` "" when there is no quota. One decimal under 10 only: a list reads "405 / 500 GB", not "405.0".
 * The bar is quiet while there is room, amber from 80%, red once the quota is used up.
 */
export function usage(used: number, quota: number, fmt: Fmt) {
  if (quota <= 0) return { used: fmt.bytes(used), of: "", text: fmt.bytes(used), pct: 0, tone: "ok" as const };
  const { unit } = scaleBytes(quota);
  const v = used / 1000 ** unit;
  const n = fmt.num(v, v > 0 && v < 10 ? 1 : 0);
  const of = fmt.bytes(quota);
  const pct = used / quota;
  return { used: n, of, text: `${n} / ${of}`, pct, tone: pct >= 1 ? ("over" as const) : pct >= 0.8 ? ("high" as const) : ("ok" as const) };
}

/** The panel's short "5 min ago / 3 h ago / 2 d ago" (fmt.ago) for a Unix time; "—" when it is 0 (never). */
export function agoText(unix: number, t: Tx, now = nowSec()): string {
  return unix === 0 ? "—" : agoOf(t, unix, now);
}

/** Last-seen column: "now" while online, else how long ago. */
export function seenText(u: User, t: Tx, now = nowSec()): string {
  return u.online ? t("users.now") : agoText(u.lastSeenUnix, t, now);
}

/** What the user actually connected with in the last 7 days: by the subscription link, by an AmneziaVPN key, by a TrustTunnel key, or several. */
export function viaOf(u: User): { link: boolean; keys: boolean; trustTunnel: boolean } {
  return { link: u.via.includes(App.HAPP), keys: u.via.includes(App.AMNEZIA), trustTunnel: u.via.includes(App.TRUST_TUNNEL) };
}

// ---------------------------------------------------------------------------------------------------
// The people table of the redesign (list.tsx, users-table.tsx): what each cell says and in which tone.

/** The tone of a cell of the table: nothing, amber, red, or quiet (the person is off). */
export type CellTone = "" | "warn" | "crit" | "off";

/** The term column: days left (amber in the last week), "expired" in red, "no expiry", or "off" for a person who is disabled. */
export function termView(u: User, t: Tx, now = nowSec()): { text: string; tone: CellTone } {
  if (u.status === UserStatus.DISABLED) return { text: t("ul.term.off"), tone: "off" };
  if (u.status === UserStatus.EXPIRED || (u.expiresUnix > 0 && u.expiresUnix <= now)) return { text: t("ul.term.expired"), tone: "crit" };
  const d = daysLeft(u.expiresUnix, now);
  if (d === null) return { text: t("ul.term.never"), tone: "" };
  return { text: t.n("ul.days", d), tone: d <= 7 ? "warn" : "" };
}

/** The devices cell: "2/5", how full it is (the bar), and whether the limit is reached; no limit has no bar. */
export function devicesView(u: Pick<User, "devicesUsed" | "deviceLimit">) {
  const limit = u.deviceLimit;
  return {
    text: `${u.devicesUsed}/${limit > 0 ? limit : "∞"}`,
    full: limit > 0 && u.devicesUsed >= limit,
    /** A limit of up to six is drawn as a dot per place, a bigger one as a bar; no limit is neither. */
    dots: limit > 0 && limit <= 6 ? limit : 0,
    pct: limit > 0 ? Math.min(100, Math.round((u.devicesUsed / limit) * 100)) : 0,
  };
}

/** The traffic cell's bar: the share of the quota, amber from 80 %, red once used up; no quota is a dashed line. */
export function trafficTone(u: Pick<User, "usedBytes" | "quotaBytes" | "status">): "" | "warn" | "crit" | "inf" {
  if (u.quotaBytes <= 0) return "inf";
  const pct = u.usedBytes / u.quotaBytes;
  return u.status === UserStatus.LIMITED || pct >= 1 ? "crit" : pct >= 0.8 ? "warn" : "";
}

/** The line at the head of an open row: what is wrong with the person (or what to do), and in which tone. `info` is the quiet one. */
export type RowWarn = { tone: "crit" | "warn" | "info"; text: string };

export function warnOf(u: User, group: { happNodes: number; amneziaNodes: number } | undefined, t: Tx, fmt: Fmt, now = nowSec()): RowWarn | null {
  const d = daysLeft(u.expiresUnix, now);
  if (u.status === UserStatus.DISABLED) return { tone: "crit", text: t("ul.warn.off") };
  if (u.status === UserStatus.EXPIRED) return { tone: "crit", text: t.n("ul.warn.expired", Math.max(0, -(d ?? 0)) || 0) };
  if (u.status === UserStatus.LIMITED) return { tone: "crit", text: u.nextResetUnix > 0 ? t("ul.warn.quota", { date: fmt.date(u.nextResetUnix) }) : t("ul.warn.quotaNoReset") };
  if (noAccess(u)) return { tone: "warn", text: t(!group || (group.happNodes === 0 && group.amneziaNodes === 0) ? "users.noAccessGroup" : "users.noAccessApps") };
  if (d !== null && d <= 7) return { tone: "warn", text: t.n("ul.warn.soon", Math.max(0, d)) };
  if (u.deviceLimit > 0 && u.devicesUsed >= u.deviceLimit) return { tone: "warn", text: t("ul.warn.full", { n: u.deviceLimit }) };
  if (u.lastSeenUnix === 0 && u.devicesUsed === 0) return { tone: "info", text: t("ul.warn.new") };
  return null;
}
