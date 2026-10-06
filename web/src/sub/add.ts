import { addModal, grab, modalLabel, sheetHead } from "./amnezia";
import { appStep, deviceStep, howStep, qrOther } from "./connect";
import { h, type Kid } from "./dom";
import { chosenApp, keyAppName, ways } from "./logic";
import type { Ctx } from "./state";
import { wayCards } from "./ways";

// "Add a device": one sheet for both ways. It asks how the device will connect: with an app and the subscription link (what
// the page used to keep in a block of its own, "Connect another device") or with an AmneziaVPN key (the form that was the
// whole of "Add a device"). The cards are ways.ts, the same as in the first-visit steps. With only one way the question is
// skipped (amz-actions defaultPane). The key's own sheets (the form, the key, "new key needed") are amnezia.ts.

function pickSheet(c: Ctx): Kid[] {
  const { a, t } = c;
  return [sheetHead(t, { tone: "lav", ico: "plus", id: "dlg-t", title: t.awgAdd, sub: t.addS, focus: true }, () => a.amz.add(false)), h("div", { class: "stack g12" }, ...wayCards(c, { k: "add-", pick: (w) => a.amz.pane(w) }))];
}
/** The link branch: the three steps of connecting with an app (this device, the app, how), then the QR code for another device. */
function linkSheet(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const app = chosenApp(d, s.platform, s.app, "happ");
  const both = ways(d).length > 1; // after a choice the sheet is named as the choice was, and has "Back"
  return [
    sheetHead(t, { tone: "sky", ico: "link", id: "dlg-t", title: both ? t.wayAppT : t.linkSheetT, sub: t.linkSheetS, focus: true }, () => a.amz.add(false), both ? () => a.amz.pane("pick") : undefined),
    h("div", { class: "stack g20 lbody" }, deviceStep(c, "add-"), appStep(c, s.platform, "add-"), howStep(c, app, "add-", false), qrOther(c, "add-")),
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
  if (!keyShown(c) && pane === "key" && ways(c.d).length > 1) return c.t.wayKeyT(keyAppName(c.d, null));
  return modalLabel(c);
}
