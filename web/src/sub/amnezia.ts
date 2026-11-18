import { logoSrc } from "./brand";
import { chip } from "./connect";
import { h, type Kid } from "./dom";
import type { Dict } from "./i18n";
import { icon, type IconName } from "./icons";
import { addPlatforms, countryName, deviceIcon, isDesktop, isPhone, keyAppName, platformWord, profileChoices, versionsFor } from "./logic";
import { qrCard } from "./qr";
import type { AmzActions, AmzState, Tools } from "./state";
import { rich, tile } from "./ui";
import type { AwgDevice, Lang, MgData, Platform } from "./types";

export { newAmzState } from "./state";
export type { AmzActions, AmzState, Tools } from "./state";

// The AmneziaVPN way of the user page: the form that makes a device (step "key" of the "new device" window) and the key of a
// device (step "ready": its QR code, file and vpn:// text). The list of devices is devices.ts. State and actions are the page's
// (view.ts, main.ts); nothing here talks to the network. Keys hold a private key: they exist only in `AmzState.configs`, in
// memory, and are asked for one device at a time.

/** What these parts need of the page: the data, the state they read, what they may do, the words, the support link. */
export type AmzCtx = { d: MgData; s: { lang: Lang; amz: AmzState }; a: { amz: AmzActions } & Tools; t: Dict; support?: string };

/** The name of a device: its label, else its platform's word, else "Device". */
export const label = (x: AwgDevice, t: Dict) => x.label || platformWord(x.platform, t) || t.devGeneric;

import { copyButton } from "./ui";
export { copyButton };

/** The head of a sheet of the old design (a tinted icon, the title, the cross); the DNS window still stands on it. */
export function sheetHead(t: Dict, o: { tone: string; ico: IconName; id: string; title: string; sub?: Kid | Kid[]; flagRow?: boolean; focus?: boolean }, close: () => void, back?: () => void): HTMLElement {
  return h(
    "div",
    { class: "sh-head" },
    back && h("button", { class: "ibtn round", type: "button", "data-k": "add-back", "aria-label": t.back, on: { click: back } }, icon("back", 18)),
    tile(o.tone, o.ico),
    h("div", { class: "grow" }, h("h3", { id: o.id, tabindex: o.focus ? "-1" : false, "data-autofocus": o.focus ? "" : false }, o.title), o.sub && h("p", { class: `sub${o.flagRow ? " flagrow" : ""}` }, ...(Array.isArray(o.sub) ? o.sub : [o.sub]))),
    h("button", { class: "ibtn round", type: "button", "data-k": "modal-x", "aria-label": t.close, on: { click: close } }, icon("close", 16)),
  );
}

export const grab = () => h("div", { class: "grab" });

// ---- step "key": the form of a new device ----

/** The flags the board draws in CSS (sub-a.css); any other country shows its two letters on the same box. */
const cssFlags = new Set(["de", "nl", "ru", "ee", "pl", "ro", "by"]);
const flagBox = (cc: string) => {
  const l = cc.trim().toLowerCase();
  return cssFlags.has(l) ? h("span", { class: `fl2 ${l}`, "aria-hidden": "true" }) : h("span", { class: "fl2 cc", "aria-hidden": "true" }, l.toUpperCase());
};

/** The form: what device, its name, the version of the app (when the profiles differ), the countries the key holds. */
export function keyBody(c: AmzCtx): HTMLElement {
  const { d, s, a, t } = c;
  const am = d.amnezia!;
  const app = keyAppName(d, null);
  const f = s.amz.form;
  const busy = s.amz.busy === "add";
  const choices = profileChoices(am.profiles, t, s.lang, app, false);
  const chosen = choices.find((p) => p.id === f.profile) ?? choices[0];
  const err = s.amz.error && s.amz.errorAt === "add" ? s.amz.error : "";
  const sameKind = (k: string) => choices.filter((p) => p.kind === k).length;
  const ver = (p: (typeof choices)[number]) => {
    const on = p.id === chosen?.id;
    const awg = am.profiles.find((x) => x.id === p.id)?.version ?? "";
    const word = p.kind === "old" ? t.adVerOld : p.kind === "warp" ? t.adVerWarp : t.adVerNew;
    // two profiles of one kind differ by their countries: those say which is which
    const about = sameKind(p.kind) > 1 ? p.label : p.kind === "old" ? t.adVerOldD : p.kind === "warp" ? t.profileSub.warp : t.adVerNewD;
    return h(
      "button",
      { class: `a-ver${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `amz-ver-${p.id}`, on: { click: () => a.amz.form({ profile: p.id }, true) } },
      h("span", { class: "a-rd" }),
      h("span", { class: "v" }, word, h("span", { class: `pr${p.kind === "main" ? " new" : ""}` }, p.kind === "warp" ? "WARP" : `AWG ${awg}`)),
      h("span", { class: "d" }, about),
    );
  };
  const countries = (chosen?.countries ?? []).map((cc) => ({ cc, name: countryName(cc, s.lang) })).filter((x) => x.name);
  return h(
    "form",
    {
      class: "a-b",
      id: "a-key-form",
      on: {
        submit: (e) => {
          e.preventDefault();
          if (!busy) a.amz.create();
        },
      },
    },
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: "nd-p" }, t.awgPlatform),
      h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": "nd-p" }, ...addPlatforms.map((p) => chip({ ico: p === "other" ? "router" : deviceIcon(p), label: t.platforms[p], on: f.platform === p, k: `amz-platform-${p}`, click: () => a.amz.form({ platform: p }, true) }))),
    ),
    h(
      "div",
      { class: "a-fld" },
      h("label", { class: "a-lbl", for: "a-nm" }, t.awgName, h("span", null, t.optional)),
      h("input", { class: "a-inp", id: "a-nm", "data-k": "amz-label", type: "text", dir: "auto", maxlength: 40, autocomplete: "off", placeholder: t.awgNamePh[f.platform as keyof typeof t.awgNamePh], value: f.label, on: { input: (e) => a.amz.form({ label: (e.target as HTMLInputElement).value }) } }),
    ),
    choices.length > 1 &&
      h(
        "div",
        { class: "a-fld" },
        h("p", { class: "a-lbl", id: "nd-v" }, t.adVerT(app), h("span", null, t.adVerH)),
        h("div", { class: "a-vers", role: "radiogroup", "aria-labelledby": "nd-v" }, ...choices.map(ver)),
      ),
    countries.length > 0 &&
      h(
        "div",
        { class: "a-fld" },
        h("p", { class: "a-lbl" }, t.adInclT(countries.length), h("span", null, t.adInclH(app))),
        h("ul", { class: "a-incl" }, ...countries.map((x) => h("li", null, flagBox(x.cc), h("span", null, x.name)))),
      ),
    err && h("p", { class: "a-err", role: "alert" }, icon("warn"), t.err(err, s.amz.retryMin)),
  );
}

export function keyFoot(c: AmzCtx): HTMLElement {
  const { s, a, t } = c;
  const busy = s.amz.busy === "add";
  return h(
    "footer",
    { class: "a-f" },
    h("button", { class: "u-btn pri", type: "button", "data-k": "modal-create", disabled: busy, on: { click: () => !busy && a.amz.create() } }, busy ? h("span", { class: "spin", "aria-hidden": "true" }) : icon("key"), busy ? t.awgBusy : t.awgCreate),
  );
}

// ---- step "ready": the key of one device ----

export type KeyView = "this-phone" | "this-desktop" | "phone-qr" | "other-desktop";
export type Where = "this" | "phone" | "other";
export type KeyMode = "shown" | "created" | "renew";

/** The device whose key the window shows: the new key of a stale one, the one just made, or the one asked for ("Show key"). */
export function shownKey(c: Pick<AmzCtx, "d" | "s">): { x: AwgDevice; mode: KeyMode } | null {
  const { d, s } = c;
  const find = (id: string | null) => (id ? d.amnezia?.devices.find((x) => x.id === id) : undefined);
  const r = find(s.amz.renew);
  if (r) return { x: r, mode: "renew" };
  const m = find(s.amz.created);
  if (m) return { x: m, mode: "created" };
  const o = find(s.amz.open);
  return o ? { x: o, mode: "shown" } : null;
}

/** What "where to add it" offers: on a phone this phone or another device; on a computer this computer, a phone or another computer. */
export function whereOptions(here: string, t: Dict): { id: Where; label: string }[] {
  if (isPhone(here)) return [{ id: "this", label: t.whereThisPhone }, { id: "other", label: t.whereOther }];
  if (isDesktop(here)) return [{ id: "this", label: t.whereThisPc }, { id: "phone", label: t.wherePhone }, { id: "other", label: t.whereOtherPc }];
  return [{ id: "phone", label: t.wherePhone }, { id: "other", label: t.whereOtherPc }];
}

/** Where a key is added by default: on this very device when the key is for it, on a phone when it is a phone's key, else on another computer. */
export function defaultWhere(platform: string, here: string): Where {
  if (here && platform === here) return "this";
  if (isPhone(here)) return "other";
  return isPhone(platform) ? "phone" : "other";
}

/** How a key is set up: on this very phone it is copied, on this very computer the file is downloaded, a phone scans a code, another computer is told to open the page there. */
export function keyView(platform: string, here: string, where: Where): KeyView {
  if (where === "this") return isPhone(here) ? "this-phone" : "this-desktop";
  if (where === "phone") return "phone-qr";
  return isPhone(here) ? (isDesktop(platform) ? "other-desktop" : "phone-qr") : "other-desktop";
}

const whereIcon = (id: Where, here: string): IconName => (id === "this" ? (isPhone(here) ? "phone" : "monitor") : id === "phone" ? "phone" : "laptop");

const numbered = (steps: Kid[][]) => h("ol", { class: "a-steps", style: { gap: "12px" } }, ...steps.map((kids, i) => h("li", { class: "a-st" }, h("span", { class: "a-n" }, String(i + 1)), h("div", { class: "tx" }, ...kids))));
/** A step's words as one flex item of the step (the board's .tx is a column: its pieces would stack): the «quoted» names stay whole. */
const say = (s: string): Kid[] => [h("span", null, ...rich(s))];
const metaRow = (ico: IconName, ...kids: Kid[]) => h("p", null, icon(ico), h("span", null, ...kids));

/** The key: where to add it, then (a country, the steps, the buttons, the QR code) as that place needs, and what the person must know. */
export function readyBody(c: AmzCtx, x: AwgDevice, mode: KeyMode): HTMLElement {
  const { d, s, a, t } = c;
  const app = keyAppName(d, x.platform as Platform);
  const renew = mode === "renew";
  const list = s.amz.configs[x.id];
  if (!list || list.length === 0) return h("div", { class: "a-b" }, h("p", { class: "u-hint" }, list ? t.noProfile : t.awgBusy));

  const i = Math.min(s.amz.node[x.id] ?? 0, list.length - 1);
  const cfg = list[i]!;
  const filename = cfg.filename || "amnezia.conf";
  const opts = whereOptions(s.amz.here, t);
  const want = (s.amz.where[x.id] as Where | undefined) ?? defaultWhere(x.platform, s.amz.here);
  const where: Where = opts.some((o) => o.id === want) ? want : (opts[0]?.id ?? "other");
  const view = keyView(x.platform, s.amz.here, where);
  // the old connection in the app is called by the country, as the new keys are named (a key through an entry is a connection
  // of its own, named as the page names it: "Germany · via RU")
  const oldConn = cfg.via_node_id ? cfg.label : countryName(cfg.country_code, s.lang) || cfg.label;
  const [pasteStep, deleteStep] = t.renewSteps(app, oldConn);

  const dl = h("button", { class: "u-btn", type: "button", "data-k": `amz-dl-${x.id}`, on: { click: () => a.download(filename, cfg.conf) } }, icon("file"), t.downloadFile);
  const cp = copyButton(a, { text: cfg.vpn_key, label: t.copyKey, done: t.copiedShort, toast: t.keyCopied, cls: "u-btn", key: `amz-key-${x.id}` });
  const acts = (...b: Kid[]) => h("div", { class: "u-acts" }, ...b);

  const labels = list.map((l, k) => l.label || String(k + 1));
  const countries =
    list.length > 1 &&
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: `k-c-${x.id}` }, t.country),
      h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": `k-c-${x.id}` }, ...labels.map((l, k) => chip({ label: l, on: i === k, k: `amz-node-${x.id}-${k}`, click: () => a.amz.node(x.id, k) }))),
      h("p", { class: "u-hint" }, t.countryH),
    );

  const meta: Kid[] = [metaRow("shield", t.adSecret)];
  const versions = versionsFor(x.platform, cfg.min_clients.length ? cfg.min_clients : x.min_clients);
  if (versions.length > 0) meta.push(metaRow("info", t.needApp(versions)));
  // the key through an entry is another server in the app: said once, where it is picked
  if (cfg.via_node_id) meta.push(metaRow("info", t.viaKeyNote(t.chipVia(cfg.via_country), app)));
  if (view === "this-desktop" && cfg.warnings.includes("amnezia_desktop_mtu")) meta.push(metaRow("warn", t.desktopFileOnly));
  if (renew && view !== "this-phone") meta.push(metaRow("info", deleteStep));

  let left: Kid[];
  let qr: HTMLElement | null = null;
  if (view === "this-phone") {
    left = [numbered((renew ? [pasteStep, deleteStep] : t.phoneSteps(app)).map(say)), acts(cp)];
  } else if (view === "this-desktop") {
    left = [numbered(t.adFileSteps(app).map(say)), acts(dl, h("span", { class: "a-fname" }, filename))];
  } else if (view === "phone-qr") {
    left = [numbered(t.adQrSteps(app).map(say)), acts(dl, cp)];
    qr = qrCard(cfg.conf, t.qrKeyAlt(app), logoSrc(d));
    if (!qr) meta.push(metaRow("info", t.qrTooBig));
  } else {
    left = [numbered([[t.openThereT, h("span", { class: "u-hint" }, t.openThereD)], [t.orFile, acts(dl)]])];
  }
  const col = h("div", { class: "a-steps" }, countries, ...left);
  return h(
    "div",
    { class: "a-b" },
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: `k-w-${x.id}` }, t.where),
      h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": `k-w-${x.id}` }, ...opts.map((o) => chip({ ico: whereIcon(o.id, s.amz.here), label: o.label, on: where === o.id, k: `amz-where-${o.id}`, click: () => a.amz.where(x.id, o.id) }))),
    ),
    qr ? h("div", { class: "a-two" }, col, h("div", { class: "a-side" }, qr)) : col,
    h("div", { class: "a-meta" }, ...meta),
  );
}

export const readyFoot = (c: AmzCtx, mode: KeyMode): HTMLElement =>
  h("footer", { class: "a-f" }, h("p", { class: "u-hint" }, mode === "renew" ? c.t.renewS : c.t.keyAgain), h("button", { class: "u-btn", type: "button", "data-k": "modal-done", on: { click: () => c.a.amz.add(false) } }, c.t.awgDone));

// ---- "new key needed" ----

/** The devices that need a new key. */
export const staleDevices = (d: MgData): AwgDevice[] => d.amnezia?.devices.filter((x) => x.stale) ?? [];
