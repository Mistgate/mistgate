import { brandName, logoSrc } from "./brand";
import { h } from "./dom";
import { localeOf } from "./i18n";
import { icon } from "./icons";
import { appList, keyAppName, keyAvailability, usableApps, ways, type KeyAvailability } from "./logic";
import type { Ctx } from "./state";
import type { AppEntry, MgData } from "./types";

// The first step of "new device": how the device will connect, as two cards of the board (.a-way): with an app and the
// subscription link (recommended), or with an AmneziaVPN key. A way the person cannot use is a quiet card that says why. The
// line above the cards says what the device takes: one place, and how many are free.

const tones = ["sky", "lav", "mint"] as const;

/** The app is the instance's own (the page wears its logo): named as the instance is. */
const isHost = (d: MgData, a: AppEntry) => a.name.trim().toLowerCase() === brandName(d).toLowerCase();

/** The letters on an app's tile: its first letter, and the sign in its name ("kl!ck" is "k!"). */
const mark = (name: string) => {
  const first = [...name.trim()][0] ?? "";
  const sign = name.match(/[^\p{L}\p{N}\s]/u)?.[0] ?? "";
  return first && first !== sign ? first + sign : first;
};

/** The tile of the app at `i` in `apps`: the instance's logo for its own app, else its letters in a tone (sky, lav, mint, in order). */
export function appTile(d: MgData, apps: AppEntry[], i: number): HTMLElement {
  const x = apps[i]!;
  if (isHost(d, x)) return h("img", { class: "a-app", src: logoSrc(d), alt: "", draggable: "false" });
  const n = apps.slice(0, i).filter((y) => !isHost(d, y)).length;
  return h("span", { class: `a-app ${tones[n % tones.length]}`, "aria-hidden": "true" }, mark(x.name));
}

/** The apps of the link way, one for each name: the platform's, else every one the person may use. */
export function linkApps(c: Pick<Ctx, "d" | "s">): AppEntry[] {
  const { d, s } = c;
  const all = appList(d, s.platform, "happ");
  const any = usableApps(d).filter((x) => x.kind === "happ");
  const list = all.length > 0 ? all : [...any.filter((x) => x.recommended), ...any.filter((x) => !x.recommended)];
  return list.filter((x, i) => x.name !== "" && list.findIndex((y) => y.name === x.name) === i);
}

const points = (items: string[]) => h("span", { class: "pts" }, ...items.map((x) => h("span", null, icon("check"), x)));
const go = (t: Ctx["t"]) => h("span", { class: "go" }, t.adChoose, icon("next"));

/** A way that cannot be used right now: why, and the way to write where writing helps. */
function offCard(c: Ctx, why: Exclude<KeyAvailability, "ok" | "none">): HTMLElement {
  const { d, t, support } = c;
  const app = keyAppName(d, null);
  const text = why === "limit" ? t.limit(d.user.devices_used, d.user.device_limit, support !== "") : why === "admin" ? t.keysByAdminD(app) : why === "preview" ? t.wayKeyPreview : t.noProfile;
  return h(
    "div",
    { class: "a-way off", "data-way": "key" },
    h("span", { class: "top" }, h("span", { class: "a-apps" }, h("span", { class: "a-app mint", "aria-hidden": "true" }, icon("key")))),
    h("span", { class: "t" }, t.wayKeyT(app)),
    h("span", { class: "d" }, text),
    why !== "preview" && support && h("a", { class: "a-lnk", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "add-way-write" }, icon("send"), t.write),
  );
}

/** The line above the cards: "the device takes one place", the bar of the places and what is free (no bar for a page without a limit). */
function slots(c: Ctx): HTMLElement {
  const { d, t } = c;
  const limit = d.user.device_limit;
  const used = Math.min(d.user.devices_used, limit);
  const full = limit > 0 && used >= limit;
  return h(
    "div",
    { class: "a-slots" },
    h("p", null, t.adSlotNote),
    limit > 0 && h("div", { class: "u-prog", role: "img", "aria-label": t.adSlotsAria(used, limit) }, h("i", { class: full ? "full" : undefined, style: { width: `${Math.round((used / limit) * 1000) / 10}%` } })),
    limit > 0 && h("p", { class: "r" }, t.adSlotLeft(limit - used, limit)),
  );
}

/** The step "way": the places and the cards. A card goes on to its branch (`pane`). */
export function wayBody(c: Ctx): HTMLElement {
  const { d, s, a, t } = c;
  const have = ways(d);
  const apps = linkApps(c);
  const shown = apps.slice(0, 3).map((x) => x.name);
  const names = typeof Intl.ListFormat === "function" ? new Intl.ListFormat(localeOf(s.lang), { type: "disjunction", style: "long" }).format(shown) : shown.join(", ");
  const link = h(
    "button",
    { class: "a-way rec", type: "button", "data-k": "add-way-link", on: { click: () => a.amz.pane("link") } },
    h("span", { class: "top" }, h("span", { class: "a-apps" }, ...apps.slice(0, 3).map((_, i) => appTile(d, apps.slice(0, 3), i))), h("span", { class: "tag" }, t.recommended)),
    h("span", { class: "t" }, t.wayAppT),
    h("span", { class: "d" }, apps.length > 0 ? t.adLinkD(names) : t.linkD),
    points(t.adLinkPts),
    go(t),
  );
  const app = keyAppName(d, null);
  const avail = keyAvailability(d);
  const key =
    avail === "ok"
      ? h(
          "button",
          { class: "a-way", type: "button", "data-k": "add-way-key", on: { click: () => a.amz.pane("key") } },
          h("span", { class: "top" }, h("span", { class: "a-apps" }, h("span", { class: "a-app mint", "aria-hidden": "true" }, icon("key")))),
          h("span", { class: "t" }, t.wayKeyT(app)),
          h("span", { class: "d" }, t.adKeyD(app)),
          points(t.adKeyPts),
          go(t),
        )
      : avail === "none"
        ? null
        : offCard(c, avail);
  return h("div", { class: "a-b" }, slots(c), h("div", { class: "a-ways" }, have.includes("link") && link, key));
}
