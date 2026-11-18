import { logoSrc } from "./brand";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { appKey, appList, chosenApp, hasLinkApp, isPhone, platformOrder, platformWord, safeAddUrl, safeUrl, storeLabel } from "./logic";
import { qrCard } from "./qr";
import type { Ctx } from "./state";
import { copyButton } from "./ui";
import type { AppEntry, Platform } from "./types";
import { appTile } from "./ways";

// The steps of the link way in the "new device" window: the device and the app (step "app"), then connecting (step "conn"): install,
// add the subscription with one tap or by copying the link, and, on a computer, the QR code that opens this page on a phone.

const platIcon: Record<Platform, IconName> = { ios: "phone", android: "android", windows: "monitor", macos: "laptop", linux: "terminal" };

/** A choice of the board (.a-chip): an icon and a word, one of a radio group. */
export function chip(o: { ico?: IconName; label: string; on: boolean; k: string; click: () => void }): HTMLButtonElement {
  return h("button", { class: `a-chip${o.on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": o.on, "data-k": o.k, on: { click: o.click } }, o.ico && icon(o.ico), o.label);
}

/** The QR code of the link can be shown: the page has the option, a link and some link app. */
export const qrOn = ({ d }: Pick<Ctx, "d">) => d.options.show_qr && d.subscription_url !== "" && hasLinkApp(d);

/** The link app the steps are about: the chosen one of the platform. */
export const linkApp = ({ d, s }: Pick<Ctx, "d" | "s">): AppEntry | undefined => chosenApp(d, s.platform, s.app, "happ");

const sendLink = (c: Ctx, k: string) => c.support && h("a", { class: "u-btn", href: c.support, target: "_blank", rel: "noopener noreferrer", "data-k": k }, icon("chat"), c.t.write);

// ---- step "app": which device, which app ----

/** Which device it is for: the platforms as chips, the browser's guess marked (and the way back to it when it was changed). */
function deviceField(c: Ctx): HTMLElement {
  const { s, a, t } = c;
  const detected = s.detected;
  const same = detected !== null && detected === s.platform;
  const back =
    detected && !same
      ? (() => {
          const [q, link] = t.notYours(isPhone(detected), platformWord(detected, t));
          return h("p", { class: "u-hint" }, q, " ", h("button", { class: "a-lnk", type: "button", "data-k": "add-plat-back", on: { click: () => a.platform(detected) } }, link));
        })()
      : null;
  return h(
    "div",
    { class: "a-fld" },
    h("p", { class: "a-lbl", id: "a-plat" }, t.adDevice, same && h("span", null, t.adBrowser)),
    h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": "a-plat" }, ...platformOrder.map((p) => chip({ ico: platIcon[p], label: t.platforms[p], on: p === s.platform, k: `add-plat-${p}`, click: () => a.platform(p) }))),
    back,
  );
}

/** The apps of the platform: the recommended one first, each a radio with its tile; nothing for this device: the link itself and the way to write. */
function appField(c: Ctx): HTMLElement {
  const { d, s, a, t } = c;
  const all = appList(d, s.platform, "happ");
  const chosen = linkApp(c);
  if (all.length === 0) {
    // still the link (it fits any app that takes subscriptions) and the way to write
    const copy = d.access.happ && d.subscription_url !== "";
    return h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl" }, t.appT),
      h(
        "div",
        { class: "a-nil" },
        h("p", { class: "t" }, copy ? t.noAppsT(platformWord(s.platform, t)) : t.noApps),
        copy && h("p", { class: "u-hint" }, t.noAppsD),
        h("div", { class: "u-acts" }, copy && copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls: "u-btn", key: "add-link-copy-plain", after: a.mark }), sendLink(c, "add-write")),
      ),
    );
  }
  return h(
    "div",
    { class: "a-fld" },
    h("p", { class: "a-lbl", id: "a-app" }, t.appT),
    h(
      "div",
      { class: "a-opts", role: "radiogroup", "aria-labelledby": "a-app" },
      ...all.map((x, i) => {
        const on = x === chosen;
        return h(
          "button",
          { class: `a-opt${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `add-app-${appKey(x)}`, on: { click: () => !on && a.app(appKey(x)) } },
          appTile(d, all, i),
          h("span", { class: "tx" }, h("span", { class: "t" }, h("bdi", null, x.name), all.length > 1 && x.recommended && h("span", { class: "tag" }, t.recommended)), h("span", { class: "d" }, x.description || t.linkD)),
          h("span", { class: "a-rd" }),
        );
      }),
    ),
  );
}

export const appBody = (c: Ctx): HTMLElement => h("div", { class: "a-b" }, deviceField(c), appField(c));

/** "Next" goes to connecting; with no app for the device there is nothing to go on to: "Done". */
export function appFoot(c: Ctx): HTMLElement {
  const { a, t } = c;
  return linkApp(c)
    ? h("footer", { class: "a-f" }, h("button", { class: "u-btn pri", type: "button", "data-k": "add-next", on: { click: () => a.amz.pane("conn") } }, t.adNext))
    : h("footer", { class: "a-f" }, h("button", { class: "u-btn pri", type: "button", "data-k": "add-done", on: { click: () => a.amz.add(false) } }, t.awgDone));
}

// ---- step "conn": install, add the subscription ----

const storeOf = (url: string, c: Ctx): "appStore" | "play" | "site" => {
  const word = storeLabel(url, c.t);
  return word === c.t.store.appStore ? "appStore" : word === c.t.store.play ? "play" : "site";
};

const step = (n: number, ...kids: Kid[]) => h("li", { class: "a-st" }, h("span", { class: "a-n" }, String(n)), h("div", { class: "tx" }, ...kids));

/** The steps: install the app (when it has a download), then add the subscription with one tap, or by the link when the app has no add address. */
function stepsOf(c: Ctx, app: AppEntry): HTMLElement {
  const { d, a, t } = c;
  const download = safeUrl(app.download_url);
  const addUrl = safeAddUrl(app.add_url);
  const copy = (cls: string, key: string) => copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls, key, after: a.mark });
  const items: Kid[][] = [];
  if (download) {
    items.push([
      t.stepInstall(app.name),
      h("a", { class: "u-btn", href: download, target: "_blank", rel: "noopener noreferrer", "data-k": "add-link-get" }, icon("file"), t.adGet[storeOf(download, c)]),
    ]);
  }
  if (addUrl) {
    items.push([
      t.stepAddSub,
      h("span", { class: "u-hint", style: { "margin-top": "-8px" } }, t.addHint(app.name)),
      h("a", { class: "u-btn pri", href: addUrl, "data-k": "add-link-add", on: { click: () => a.mark() } }, icon("plus"), t.addOne),
      h("span", { class: "a-alt" }, t.noOpenQ, copy("a-lnk", "add-link-copy")),
    ]);
  } else {
    items.push([t.copyPasteT, h("span", { class: "u-hint", style: { "margin-top": "-8px" } }, t.copyHow(app.name)), copy("u-btn", "add-link-copy")]);
  }
  return h("ol", { class: "a-steps" }, ...items.map((kids, i) => step(i + 1, ...kids)));
}

/** The QR code of this page: a phone's camera opens the page there. On a computer only (the phone is the device). */
function qrSide(c: Ctx): HTMLElement | null {
  const { d, t } = c;
  const card = qrOn(c) ? qrCard(d.qr, t.qrAlt, logoSrc(d)) : null;
  return card && h("div", { class: "a-side pg" }, card, h("p", { class: "u-hint" }, t.adQrHint));
}

export function connBody(c: Ctx, app: AppEntry): HTMLElement {
  const steps = stepsOf(c, app);
  const side = qrSide(c);
  return h("div", { class: "a-b" }, side ? h("div", { class: "a-two" }, steps, side) : steps);
}

export const connFoot = (c: Ctx): HTMLElement =>
  h("footer", { class: "a-f" }, h("p", { class: "u-hint" }, c.t.adAfterFirst), h("button", { class: "u-btn", type: "button", "data-k": "add-done", on: { click: () => c.a.amz.add(false) } }, c.t.awgDone));
