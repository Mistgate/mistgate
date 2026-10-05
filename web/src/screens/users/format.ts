import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Tone } from "@/components/ui/icons";
import type { StatusKind } from "@/components/ui/status";
import { agoOf, scaleBytes, type Fmt } from "@/lib/format";
import type { UserN as User } from "./model";
import type { Key, Tx } from "./t";

// Traffic is decimal across the panel (lib/format.ts), so a quota of "100 GB" is 100e9 bytes.
export const GB = 1e9;
export const DAY = 86400;

export const nowSec = () => Math.floor(Date.now() / 1000);

/** Whole days until `expiresUnix` (rounded up; <= 0 once it has passed); null = never expires. */
export function daysLeft(expiresUnix: number, now = nowSec()): number | null {
  return expiresUnix === 0 ? null : Math.ceil((expiresUnix - now) / DAY);
}

/** Stable colour slot for an avatar, so a user keeps their colour in the list and on their page. */
export function avatarIndex(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return h % 9;
}

const groupTones = ["lavender", "sky", "sand", "sage", "mint", "rose"] as const satisfies readonly Tone[];

/** The tone of a group's chip, picked from its id: the same group has the same colour on every page and in every session. */
export function groupTone(id: string): Tone {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return groupTones[h % groupTones.length]!;
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

export const shownKind: Record<Shown, StatusKind> = { active: "ok", expired: "bad", quota: "warn", devices: "warn", disabled: "off" };
export const shownKey: Record<Shown, Key> = {
  active: "users.status.active",
  expired: "users.status.expired",
  quota: "users.status.quota",
  devices: "users.status.devices",
  disabled: "users.status.disabled",
};
export const shownText: Record<Shown, string> = {
  active: "text-muted",
  expired: "text-danger-text",
  quota: "text-warn-text",
  devices: "text-warn-text",
  disabled: "text-faint",
};

/** "12.3 / 100 GB" (the number shares the quota's unit) and the bar fill; unlimited quota has no bar. */
export function usage(used: number, quota: number, fmt: Fmt) {
  if (quota <= 0) return { text: fmt.bytes(used), pct: 0, tone: "ok" as const };
  const { unit } = scaleBytes(quota);
  const n = fmt.num(used / 1000 ** unit, 1, true);
  const pct = used / quota;
  return { text: `${n} / ${fmt.bytes(quota)}`, pct, tone: pct > 1 ? ("over" as const) : pct >= 0.8 ? ("high" as const) : ("ok" as const) };
}

export const barTone = {
  ok: "bg-[color-mix(in_oklch,var(--accent)_70%,var(--surface))]",
  high: "bg-warn",
  over: "bg-danger",
} as const;

/** The panel's short "5 min ago / 3 h ago / 2 d ago" (fmt.ago) for a Unix time; "—" when it is 0 (never). */
export function agoText(unix: number, t: Tx, now = nowSec()): string {
  return unix === 0 ? "—" : agoOf(t, unix, now);
}

/** Last-seen column: "now" while online, else how long ago. */
export function seenText(u: User, t: Tx, now = nowSec()): string {
  return u.online ? t("users.now") : agoText(u.lastSeenUnix, t, now);
}

/** Term column: days left ("0" once expired: the status pill already says so), or "∞" for no expiry. */
export function termText(u: User, t: Tx, now = nowSec()): { text: string; tone: "muted" | "fg" | "bad" } {
  const d = daysLeft(u.expiresUnix, now);
  if (d === null) return { text: "∞", tone: "muted" };
  if (u.expiresUnix <= now) return { text: "0", tone: "bad" };
  return { text: t("users.days", { n: d }), tone: d <= 7 ? "fg" : "muted" };
}

/** What the user actually connected with in the last 7 days: by the subscription link, by an AmneziaVPN key, or both. */
export function viaOf(u: User): { link: boolean; keys: boolean } {
  return { link: u.via.includes(App.HAPP), keys: u.via.includes(App.AMNEZIA) };
}

/** "Link + AmneziaVPN keys", "Subscription link" or "AmneziaVPN keys": the ways the admin switched on. */
export function appsText(u: User, t: Tx): string {
  if (u.apps?.happ && u.apps.amnezia) return t("users.appsBoth");
  return u.apps?.amnezia ? t("users.appsAwg") : t("users.appsHapp");
}
