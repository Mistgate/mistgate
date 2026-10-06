import { copyButton, downloadButton } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import {
  appKey,
  appList,
  atDeviceLimit,
  canAddDevice,
  chosenApp,
  deviceIcon,
  fetchedUnix,
  fmtAgo,
  hasLinkApp,
  isPhone,
  keyAppName,
  keyAvailability,
  platformOrder,
  platformWord,
  qrApp,
  safeAddUrl,
  safeUrl,
  ways,
  type Way,
} from "./logic";
import { qrSvg } from "./qr";
import type { Ctx } from "./state";
import { note, rich, tile } from "./ui";
import type { AppEntry, Platform } from "./types";
import { wayCards } from "./ways";

// The steps of connecting (a first visit, and the link branch of the "add a device" sheet): the way (when there are two), the device,
// the app, how. The link way: install, add the subscription with one tap or by copying the link, turn the VPN on. The key way:
// install, add this device (its key is made), paste the key. No key app is ever mixed into the list of link apps.

const platIcon: Record<Platform, IconName> = { ios: "phone", android: "android", windows: "monitor", macos: "laptop", linux: "terminal" };

const sendLink = (c: Ctx, cls = "sec") => c.support && h("a", { class: `btn ${cls}`, href: c.support, target: "_blank", rel: "noopener noreferrer" }, icon("send"), c.t.write);

/** The QR code of the link can be shown: the page has the option, a link and some link app. */
export const qrOn = ({ d }: Pick<Ctx, "d">) => d.options.show_qr && d.subscription_url !== "" && hasLinkApp(d);

function qrBox(c: Ctx): HTMLElement | null {
  const { d, t } = c;
  const svg = qrOn(c) ? qrSvg(d.subscription_url, t.qrAlt) : null;
  return svg ? h("div", { class: "qr s" }, svg) : null;
}

/** Which device it is for. Tiles on a phone, pills on a computer. */
export function deviceStep(c: Ctx, k = "", n = 1): HTMLElement {
  const { s, a, t } = c;
  const detected = s.detected;
  const same = detected !== null && detected === s.platform;
  const back =
    detected && !same
      ? (() => {
          const [q, link] = t.notYours(isPhone(detected), platformWord(detected, t));
          return h("p", { class: "hint" }, q, " ", h("button", { class: "tlink b", type: "button", "data-k": `${k}plat-back`, style: { "min-height": "44px" }, on: { click: () => a.platform(detected) } }, link));
        })()
      : null;
  return h(
    "div",
    { class: "step" },
    h("div", { class: "step-h" }, h("span", { class: "sn" }, String(n)), h("h3", { class: "step-t" }, t.yourDevice), same && h("span", { class: "hint only-w" }, icon("check", 14), t.detectedShort)),
    h(
      "div",
      { class: "plats", role: "radiogroup", "aria-label": t.pickDev },
      ...platformOrder.map((p) =>
        h("button", { class: `plat${p === s.platform ? " on" : ""}`, type: "button", role: "radio", "aria-checked": p === s.platform, "data-k": `${k}plat-${p}`, on: { click: () => a.platform(p) } }, icon(platIcon[p], 24), t.platforms[p]),
      ),
    ),
    same && h("p", { class: "hint row g6 only-m" }, icon("check", 14), t.detected),
    back,
  );
}

function appCard(c: Ctx, x: AppEntry, o: { chosen: boolean; big: boolean; many: boolean }, k: string): HTMLElement {
  const { a, t } = c;
  const key = appKey(x);
  const desc = x.description || t.linkD;
  return h(
    "button",
    { class: `app${o.big ? "" : " min"}${o.chosen ? " on" : ""}`, type: "button", role: "radio", "aria-checked": o.chosen, "data-k": `${k}app-${key}`, on: { click: () => !o.chosen && a.app(key) } },
    tile("sky", "link", { size: o.big ? undefined : 36 }),
    h(
      "span",
      { class: "app-b" },
      h("span", { class: "app-n" }, x.name, o.many && x.recommended && h("span", { class: "badge" }, t.recommended)),
      h("span", { class: "desc" }, desc),
    ),
    h("span", { class: `radio${o.chosen ? " on" : ""}` }, o.chosen && icon("check", 13)),
  );
}

/** The app, of the link way: the recommended one big, the others under "Other apps", never folded. */
export function appStep(c: Ctx, platform: Platform, k = "", n = 2): HTMLElement {
  const { d, s, a, t } = c;
  const all = appList(d, platform, "happ");
  const chosen = chosenApp(d, platform, s.app, "happ");
  let body: Kid;
  if (all.length === 0) {
    // nothing for this device: still the link (it fits any app that takes subscriptions) and the way to write
    const copy = d.access.happ && d.subscription_url !== "";
    body = h(
      "div",
      { class: "noapps" },
      tile("neu", deviceIcon(platform)),
      h("div", { class: "stack g6" }, h("p", { class: "h3" }, copy ? t.noAppsT(platformWord(platform, t)) : t.noApps), copy && h("p", { class: "sm mut" }, t.noAppsD)),
      copy && copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls: "pri", key: `${k}link-copy`, after: a.mark }),
      sendLink(c),
    );
  } else {
    const [first, ...rest] = all as [AppEntry, ...AppEntry[]];
    body = h(
      "div",
      { class: "stack g8", role: "radiogroup", "aria-label": t.appAria },
      appCard(c, first, { chosen: first === chosen, big: true, many: all.length > 1 }, k),
      rest.length > 0 && h("p", { class: "lbl", style: { padding: "8px 2px 0" } }, t.otherApps),
      rest.length > 0 && h("div", { class: "app-grid" }, ...rest.map((x) => appCard(c, x, { chosen: x === chosen, big: false, many: all.length > 1 }, k))),
    );
  }
  return h(
    "div",
    { class: "step" },
    h("div", { class: "step-h" }, h("span", { class: "sn" }, String(n)), h("h3", { class: "step-t" }, t.appT)),
    body,
  );
}

const hint = (kids: Kid[]) => h("p", { class: "hint" }, ...kids);

/** A step of the ladder: its icon, its title and what goes under it. */
function how(items: { ico: IconName; title: string; body: Kid[] }[]): HTMLElement {
  return h(
    "ol",
    { class: "how" },
    ...items.map((it) => h("li", { class: "how-i" }, h("span", { class: "how-k" }, icon(it.ico, 16)), h("div", { class: "how-b" }, h("p", { class: "how-t" }, it.title), ...it.body))),
  );
}

/** Step 3 for a link app. */
function linkSteps(c: Ctx, app: AppEntry, k: string): HTMLElement {
  const { d, a, t } = c;
  const download = safeUrl(app.download_url);
  const addUrl = safeAddUrl(app.add_url);
  const copy = (cls: string, label: string, key: string) => copyButton(a, { text: d.subscription_url, label, done: t.copiedShort, toast: t.copied, cls, key, after: a.mark });
  const items: { ico: IconName; title: string; body: Kid[] }[] = [];
  if (download) items.push({ ico: "download", title: t.stepInstall(app.name), body: [downloadButton(download, t, `${k}link-get`, "fit-w")] });
  if (addUrl) {
    items.push({
      ico: "plus",
      title: t.stepAddSub,
      body: [
        h("p", { class: "how-sub" }, t.addHint(app.name)),
        h("div", { class: "row addrow", style: { "flex-wrap": "wrap", gap: "8px 24px" } }, h("a", { class: "btn pri fit", href: addUrl, "data-k": `${k}link-add`, on: { click: () => a.mark() } }, icon("plus"), t.addOne), h("p", { class: "row g8 sm mut only-w" }, t.noOpenQ, copy("tlink", t.copyLink, `${k}link-copy-w`))),
        h("div", { class: "fb only-m" }, h("div", { class: "fb-h" }, h("b", null, t.noOpenQ), copy("tlink", t.copyShort, `${k}link-copy`)), hint(rich(t.pasteHow(app.name)))),
      ],
    });
  } else {
    items.push({ ico: "copy", title: t.copyPasteT, body: [copy("pri fit", t.copyLink, `${k}link-copy`), h("p", { class: "how-sub", style: { "margin-top": "0" } }, t.copyHow(app.name))] });
  }
  items.push({ ico: "power", title: addUrl ? t.stepVpn(app.name) : t.stepVpnPlain, body: d.server_count > 1 ? [hint(rich(t.allServers(d.server_count)))] : [] });
  return how(items);
}

/** Step 3 for a key app: install, add this device (its key is made), paste the key. */
function keySteps(c: Ctx, app: AppEntry, k: string): HTMLElement {
  const { d, s, a, t, support } = c;
  const am = d.amnezia!;
  const download = safeUrl(app.download_url);
  const items: { ico: IconName; title: string; body: Kid[] }[] = [];
  if (download) items.push({ ico: "download", title: t.stepInstall(app.name), body: [downloadButton(download, t, `${k}key-get`, "fit-w")] });
  if (!am.self_service) {
    items.push({ ico: "key", title: t.keysByAdminT, body: [h("p", { class: "how-sub" }, t.keysByAdminD(app.name)), sendLink(c)] });
  } else if (am.profiles.length === 0) {
    items.push({ ico: "clock", title: t.noProfileT, body: [h("p", { class: "how-sub" }, t.noProfile), sendLink(c)] });
  } else {
    const full = atDeviceLimit(d);
    items.push({
      ico: "plus",
      title: t.stepAddDevT,
      body: [
        h("p", { class: "how-sub" }, t.stepAddDevS),
        h("button", { class: "btn pri fit", type: "button", "data-k": "amz-add", disabled: !canAddDevice(d), on: { click: () => a.amz.add(true, "key") } }, icon("plus"), t.awgAdd),
        full && note("warn", "warn", t.limit(d.user.devices_used, d.user.device_limit, support !== "")),
      ],
    });
  }
  items.push({ ico: "key", title: t.stepKeyT(app.name), body: [h("p", { class: "how-sub" }, ...rich(isPhone(s.platform) ? t.stepKeyPhone(app.name) : t.stepKeyDesktop(app.name)))] });
  return how(items);
}

/** Step 3: the way to connect with the chosen app; or, once the app has fetched the subscription, "Done" (`done` off: the steps, always). */
export function howStep(c: Ctx, app: AppEntry | undefined, k = "", done = true, n = 3): HTMLElement | null {
  const { d, s, a, t } = c;
  if (!app) return null;
  const at = fetchedUnix(d);
  const finished = done && app.kind === "happ" && s.marked && at > 0 && !s.stepsAgain;
  return h(
    "div",
    { class: "step" },
    h("div", { class: "step-h" }, h("span", { class: `sn${finished ? " done" : ""}` }, finished ? icon("check", 14) : String(n)), h("h3", { class: "step-t" }, t.howT)),
    finished
      ? h(
          "div",
          { class: "donebox" },
          note("ok", "check", [h("b", null, t.doneT), " ", t.doneD(fmtAgo(at, s.lang))]),
          h("button", { class: "tlink", type: "button", "data-k": `${k}steps-again`, on: { click: () => a.stepsAgain() } }, t.showSteps),
        )
      : app.kind === "happ"
        ? linkSteps(c, app, k)
        : keySteps(c, app, k),
  );
}

/** "Connect another device": the QR code of the link, which opens this page on that device. */
function qrContent(c: Ctx, key: string): HTMLElement | null {
  const { d, a, t } = c;
  const box = qrBox(c);
  if (!box) return null;
  return h("div", { class: "qrbox" }, box, h("p", { class: "hint", style: { "text-align": "center" } }, ...rich(t.qrHow(qrApp(d)))), copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls: "tlink", key, after: a.mark }));
}

/** The row that opens the QR code (a phone's way to connect another device). */
function qrRow(c: Ctx, k = ""): HTMLElement[] {
  const { s, a, t } = c;
  if (!qrOn(c)) return [];
  const open = s.qrOpen;
  return [
    h("div", { class: "div" }),
    h(
      "button",
      { class: "xrow", type: "button", "aria-expanded": open, "data-k": `${k}qr-row`, on: { click: () => a.qrOpen(!open) } },
      tile("neu", "qr", { size: 36 }),
      h("span", { class: "stack g4 grow" }, h("span", { class: "t" }, t.qrOtherT), h("span", { class: "s" }, t.qrOtherS)),
      icon("chev"),
    ),
    ...(open ? [qrContent(c, `${k}qr-copy-m`)] : []),
  ].filter((x): x is HTMLElement => !!x);
}

/** Which way the first-visit steps are for: the chosen one when both can be used; else the only way there is. */
export function wizardWay(c: Pick<Ctx, "d" | "s">): Way {
  const w = ways(c.d);
  if (w.length > 1) return c.s.way === "key" && keyAvailability(c.d) === "ok" ? "key" : "link";
  return w[0] ?? "link";
}

/** The first step when there are two ways: how the device will connect (the cards of the "add a device" sheet, as a choice). */
function wayStep(c: Ctx): HTMLElement {
  const { a, t } = c;
  return h(
    "div",
    { class: "step" },
    h("div", { class: "step-h" }, h("span", { class: "sn" }, "1"), h("h3", { class: "step-t" }, t.chooseT)),
    h("div", { class: "stack g8", role: "radiogroup", "aria-label": t.chooseT }, ...wayCards(c, { k: "", pick: (w) => a.way(w), selected: wizardWay(c) })),
  );
}

/** The key app of the platform for the steps of the key way (the settings may name none: the page still says what to do). */
function keyApp(c: Ctx): AppEntry {
  const { d, s } = c;
  return chosenApp(d, s.platform, s.app, "amnezia") ?? { platform: s.platform, kind: "amnezia", name: keyAppName(d, null), download_url: "", add_url: "", description: "", recommended: false };
}

/** The steps of a first visit: how (with two ways), the device, then the steps of the way: a link app (app, how) or the key (install, add, paste). */
export function connectCard(c: Ctx): HTMLElement {
  const { d, s, t } = c;
  const choose = ways(d).length > 1;
  const first = choose ? 2 : 1;
  const steps: (HTMLElement | null)[] = [choose ? wayStep(c) : null, deviceStep(c, "", first)];
  if (wizardWay(c) === "key") steps.push(howStep(c, keyApp(c), "", true, first + 1));
  else steps.push(appStep(c, s.platform, "", first + 1), howStep(c, chosenApp(d, s.platform, s.app, "happ"), "", true, first + 2));
  return h("section", { class: "card", "aria-label": t.connectAria }, ...steps.filter((x): x is HTMLElement => !!x), ...(wizardWay(c) === "link" && qrOn(c) ? [h("div", { class: "only-m" }, ...qrRow(c))] : []));
}
/** "Connect another device" inside the "add a device" sheet: a folded row on a phone, the code itself on a computer. */
export function qrOther(c: Ctx, k: string): HTMLElement | null {
  if (!qrOn(c)) return null;
  return h("div", { class: "stack g12" }, h("section", { class: "card only-m" }, ...qrRow(c, k).slice(1)), h("div", { class: "only-w" }, qrContent(c, `${k}qr-copy-w`)));
}

/** The QR code beside the steps on a computer (a first visit, the link way). */
export function qrSide(c: Ctx): HTMLElement | null {
  const { d, a, t } = c;
  const box = wizardWay(c) === "link" ? qrBox(c) : null;
  if (!box) return null;
  return h(
    "section",
    { class: "card stack qrcard", "aria-labelledby": "qr-t" },
    h("div", { class: "stack g6" }, h("h3", { class: "h3", id: "qr-t" }, t.qrPhoneT), h("p", { class: "sm mut" }, ...rich(t.qrHow(qrApp(d))))),
    box,
    copyButton(a, { text: d.subscription_url, label: t.copyLink, done: t.copiedShort, toast: t.copied, cls: "sec", key: "qr-copy", after: a.mark }),
  );
}
