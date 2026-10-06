import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { appNames, appsFor, keyAppName, keyAvailability, linkInUse, type Way, type KeyAvailability } from "./logic";
import type { Ctx } from "./state";
import { tile } from "./ui";

// The two ways to connect a device as cards: with an app and the subscription link (recommended), or with an AmneziaVPN key.
// The same cards stand in the "add a device" sheet (a card is a button that goes on) and in the first-visit steps (the cards
// are a radio group, one is chosen), so the page reads as one design. A way the person cannot use is a quiet card that says
// why. The words under a way say what it takes: a square for a slot, hollow when the way takes none.

/** The words under a way: a square for a slot, hollow when the way takes none. */
const slotLine = (tone: "sky" | "mint", free: boolean, text: string) => h("span", { class: `wayslot ${tone}` }, h("i", { class: `sq${free ? " o" : ""}`, "aria-hidden": "true" }), h("span", null, text));

/** A way that cannot be used right now: why, and the way to write where writing helps. */
function offCard(c: Ctx, why: Exclude<KeyAvailability, "ok" | "none">): HTMLElement {
  const { d, t, support } = c;
  const app = keyAppName(d, null);
  const text = why === "limit" ? t.limit(d.user.devices_used, d.user.device_limit, support !== "") : why === "admin" ? t.keysByAdminD(app) : why === "preview" ? t.wayKeyPreview : t.noProfile;
  return h(
    "div",
    { class: "opt off", "data-way": "key" },
    tile("neu", "key"),
    h(
      "div",
      { class: "opt-b" },
      h("span", { class: "opt-t" }, t.wayKeyT(app)),
      h("span", { class: "opt-d" }, text),
      why !== "preview" && support && h("a", { class: "tlink", href: support, target: "_blank", rel: "noopener noreferrer", style: { "margin-top": "4px" } }, icon("send", 16), t.write),
    ),
  );
}

/**
 * The cards of the two ways. `k` prefixes the keys; `pick` is what a card does. With `selected` the cards are a radio group
 * (the chosen one is marked, a round mark stands where the sheet has its arrow); without it they are buttons that go on.
 */
export function wayCards(c: Ctx, o: { k: string; pick: (w: Way) => void; selected?: Way }): Kid[] {
  const { d, s, t } = c;
  const app = keyAppName(d, null);
  const radio = o.selected !== undefined;
  const end = (on: boolean) => (radio ? h("span", { class: `radio${on ? " on" : ""}` }, on && icon("check", 13)) : icon("next", 18));
  const attrs = (w: Way) => ({ class: `opt go${o.selected === w ? " on" : ""}`, type: "button", role: radio ? "radio" : false, "aria-checked": radio ? o.selected === w : false, "data-k": `${o.k}way-${w}`, on: { click: () => o.selected !== w && o.pick(w) } });

  const names = appNames(appsFor(d, s.platform, "happ")).slice(0, 3);
  const used = linkInUse(d);
  const link = h(
    "button",
    attrs("link"),
    tile("sky", "link"),
    h(
      "span",
      { class: "opt-b" },
      h("span", { class: "opt-t" }, t.wayAppT, h("span", { class: "badge" }, t.recommended)),
      h("span", { class: "opt-d" }, t.wayAppD),
      names.length > 0 && h("span", { class: "chips" }, ...names.map((n) => h("span", { class: "chip sm" }, n))),
      slotLine("sky", used, used ? t.wayAppSlotShared : t.wayAppSlotFirst),
    ),
    end(o.selected === "link"),
  );

  const avail = keyAvailability(d);
  const limit = d.user.device_limit;
  const key =
    avail === "ok"
      ? h(
          "button",
          attrs("key"),
          tile("mint", "key"),
          h("span", { class: "opt-b" }, h("span", { class: "opt-t" }, t.wayKeyT(app)), h("span", { class: "opt-d" }, t.wayKeyD(app)), slotLine("mint", false, t.wayKeySlot(limit > 0 ? limit - d.user.devices_used : -1))),
          end(o.selected === "key"),
        )
      : avail === "none"
        ? null
        : offCard(c, avail);
  return [link, key];
}
