import { addModal, grab, modalLabel, sheetHead } from "./amnezia";
import { appStep, deviceStep, howStep, qrOther } from "./connect";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { appNames, appsFor, chosenApp, keyAppName, keyAvailability, linkInUse, ways, type KeyAvailability } from "./logic";
import type { Ctx } from "./state";
import { tile } from "./ui";

// "Add a device": one sheet for both ways. It asks how the device will connect: with an app and the subscription link (what
// the page used to keep in a block of its own, "Connect another device") or with an AmneziaVPN key (the form that was the
// whole of "Add a device"). A way the person cannot use stays on the sheet as a quiet card that says why. With only one way
// the question is skipped (amz-actions defaultPane). The key's own sheets (the form, the key, "new key needed") are amnezia.ts.

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

function pickSheet(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const app = keyAppName(d, null);
  const names = appNames(appsFor(d, s.platform, "happ")).slice(0, 3);
  const used = linkInUse(d);
  const linkWay = h(
    "button",
    { class: "opt go", type: "button", "data-k": "add-way-link", "data-autofocus": "", on: { click: () => a.amz.pane("link") } },
    tile("sky", "link"),
    h(
      "span",
      { class: "opt-b" },
      h("span", { class: "opt-t" }, t.wayAppT, h("span", { class: "badge" }, t.recommended)),
      h("span", { class: "opt-d" }, t.wayAppD),
      names.length > 0 && h("span", { class: "chips" }, ...names.map((n) => h("span", { class: "chip sm" }, n))),
      slotLine("sky", used, used ? t.wayAppSlotShared : t.wayAppSlotFirst),
    ),
    icon("next", 18),
  );
  const avail = keyAvailability(d);
  const limit = d.user.device_limit;
  const keyWay =
    avail === "ok"
      ? h(
          "button",
          { class: "opt go", type: "button", "data-k": "add-way-key", on: { click: () => a.amz.pane("key") } },
          tile("mint", "key"),
          h("span", { class: "opt-b" }, h("span", { class: "opt-t" }, t.wayKeyT(app)), h("span", { class: "opt-d" }, t.wayKeyD(app)), slotLine("mint", false, t.wayKeySlot(limit > 0 ? limit - d.user.devices_used : -1))),
          icon("next", 18),
        )
      : avail === "none"
        ? null
        : offCard(c, avail);
  return [sheetHead(t, { tone: "lav", ico: "plus", id: "dlg-t", title: t.awgAdd, sub: t.addS }, () => a.amz.add(false)), h("div", { class: "stack g12" }, linkWay, keyWay)];
}

/** The link branch: the three steps of connecting with an app (this device, the app, how), then the QR code for another device. */
function linkSheet(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const app = chosenApp(d, s.platform, s.app, "happ");
  const both = ways(d).length > 1; // after a choice the sheet is named as the choice was, and has "Back"
  return [
    sheetHead(t, { tone: "sky", ico: "link", id: "dlg-t", title: both ? t.wayAppT : t.linkSheetT, sub: t.linkSheetS }, () => a.amz.add(false), both ? () => a.amz.pane("pick") : undefined),
    h("div", { class: "stack g20 lbody" }, deviceStep(c, "add-"), appStep(c, s.platform, "add-", "happ"), howStep(c, app, "add-", false), qrOther(c, "add-")),
  ];
}

const keyShown = (c: Ctx) => {
  const { d, s } = c;
  const has = (id: string | null) => !!id && !!d.amnezia?.devices.some((x) => x.id === id);
  return has(s.amz.renew) || has(s.amz.created) || has(s.amz.open);
};

/** What the dialog of "add a device" holds: the choice of the way, the link branch, or (the key form and the key's own sheets) amnezia.ts. */
export function deviceModal(c: Ctx): Kid[] {
  const pane = c.s.amz.pane;
  if (!keyShown(c) && pane === "pick") return [grab(), ...pickSheet(c)];
  if (!keyShown(c) && pane === "link") return [grab(), ...linkSheet(c)];
  return addModal(c);
}

/** The accessible name of that dialog. */
export function deviceModalLabel(c: Ctx): string {
  const pane = c.s.amz.pane;
  if (!keyShown(c) && pane === "pick") return c.t.awgAdd;
  if (!keyShown(c) && pane === "link") return ways(c.d).length > 1 ? c.t.wayAppT : c.t.linkSheetT;
  return modalLabel(c);
}
