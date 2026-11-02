// How a protocol is shown wherever the profiles screens name it: its mark, its place in lists, and what its one-line
// summary says. Nothing here changes what a protocol can do; a protocol this file does not know still works everywhere.

import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { IconName, Tone } from "@/components/ui/icons";

/**
 * One icon and one pastel per protocol, a hue apart: Hysteria2 mint (fast), VLESS sky (looks like a web site), AmneziaWG
 * lavender (a key per device), MTProto rose (Telegram's plane). Sand stays out: next to the warnings' yellow it would read as
 * one. The order is the guide's advice: start with Hysteria2, add VLESS, then the keys, then Telegram.
 */
// `badge`: the one honest word on its card in the New profile choice: where the guide says to start, and what is still new.
type Look = { icon: IconName; tone: Tone; badge?: "start" | "experimental" };
const looks: Record<string, Look> = {
  hysteria2: { icon: "bolt", tone: "mint", badge: "start" },
  vless: { icon: "globe", tone: "sky" },
  awg: { icon: "key", tone: "lavender" },
  mtproto: { icon: "plane", tone: "rose", badge: "experimental" },
};
const order = Object.keys(looks);

/** A protocol this file does not know (a new plugin) wears the mark of the profiles section. */
export const lookOf = (id: string): Look => looks[id] ?? { icon: "layers", tone: "sage" };

/** Sort protocol ids: the known ones in their order, the others after them by id. */
export function byProtocol(a: string, b: string): number {
  const rank = (id: string) => (order.includes(id) ? order.indexOf(id) : order.length);
  return rank(a) - rank(b) || a.localeCompare(b);
}

/** A protocol whose only app is Telegram (MTProto): its tg:// links reach people outside the subscription formats. */
export const telegramOnly = (apps: readonly App[]) => apps.length > 0 && apps.every((a) => a === App.TELEGRAM);

/**
 * The server's one-line summary as its parts: "UDP 443 · Salamander · Let's Encrypt · WARP" (MTProto writes "TCP 443 - Fake
 * TLS"). A range like "hop 20000-30000" has no spaces around its dash and stays whole.
 */
export const summaryParts = (summary: string) =>
  summary
    .split(/\s+[·-]\s+/)
    .map((s) => s.trim())
    .filter(Boolean);

/** Whether the profile leaves through WARP: the summary names it as a part of its own. */
export const viaWarp = (summary: string) => summaryParts(summary).includes("WARP");
