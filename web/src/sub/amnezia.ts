import { h, type Kid } from "./dom";
import type { Dict } from "./i18n";
import { icon, type IconName } from "./icons";
import {
  addPlatforms,
  countryName,
  deviceIcon,
  isDesktop,
  isPhone,
  keyAppName,
  nodeLabels,
  platformWord,
  profileChoices,
  selfServe,
  storeLabel,
  versionsFor,
  ways,
} from "./logic";
import { qrSvg } from "./qr";
import type { AmzActions, AmzState, Tools } from "./state";
import { dot, note, rich, tile } from "./ui";
import type { AwgDevice, Lang, MgData, Platform } from "./types";

export { newAmzState } from "./state";
export type { AmzActions, AmzState, Tools } from "./state";

// The AmneziaVPN way of the user page, the parts that are dialogs and cards of their own: the "add a device" sheet, the
// key of a device (its QR code, file and vpn:// text), the "new key needed" card. The list of devices is devices.ts. State
// and actions are the page's (view.ts, main.ts); nothing here talks to the network. Keys hold a private key: they exist
// only in `AmzState.configs`, in memory, and are asked for one device at a time.

/** What these parts need of the page: the data, the state they read, what they may do, the words, the support link. */
export type AmzCtx = { d: MgData; s: { lang: Lang; amz: AmzState }; a: { amz: AmzActions } & Tools; t: Dict; support?: string };

/** Windows has no flag emoji (it draws "DE"): the choices there are the country's name alone. */
export const flagsOn = (s: { amz: AmzState }) => s.amz.here !== "windows";

/** The name of a device: its label, else its platform's word, else "Device". */
export const label = (x: AwgDevice, t: Dict) => x.label || platformWord(x.platform, t) || t.devGeneric;

/**
 * A button that copies `text` and says so on itself: its label turns into `done` with a tick for a moment. `toast` is the
 * page's toast text. The page's copy() calls back only when the clipboard took the text. `after` runs when it did.
 */
export function copyButton(a: Tools, o: { text: string; label: string; done: string; toast: string; cls?: string; key?: string; after?: () => void; ico?: IconName }): HTMLButtonElement {
  const text = h("span", null, o.label);
  const own = o.cls?.split(" ") ?? [];
  const link = own.includes("tlink");
  const ico = o.ico ?? "copy";
  const btn = h("button", { class: `${link ? "" : "btn "}${o.cls ?? ""}`.trim(), type: "button", "data-k": o.key, on: { click: () => a.copy(o.text, o.toast, flash) } }, icon(ico, link ? 16 : 18), text);
  let timer = 0;
  function flash() {
    o.after?.();
    btn.classList.add("ok");
    btn.replaceChildren(icon("check", link ? 16 : 18), text);
    text.textContent = o.done;
    clearTimeout(timer);
    timer = window.setTimeout(() => {
      btn.classList.remove("ok");
      btn.replaceChildren(icon(ico, link ? 16 : 18), text);
      text.textContent = o.label;
    }, 1800);
  }
  return btn;
}

/** A download link with the store under the word: "Download / App Store". Never cut short. */
export function downloadButton(url: string, t: Dict, key: string, cls = ""): HTMLAnchorElement {
  return h(
    "a",
    { class: `btn sec dl${cls ? ` ${cls}` : ""}`, href: url, target: "_blank", rel: "noopener noreferrer", "data-k": key },
    icon("download"),
    h("span", { class: "dl-t" }, h("b", null, t.download), h("small", null, storeLabel(url, t))),
    icon("external", 16),
  );
}

/** The head of a sheet: ("Back" when the sheet is a step of a choice,) a tinted icon, the title, one line under it, the cross. */
export function sheetHead(t: Dict, o: { tone: string; ico: IconName; id: string; title: string; sub?: Kid | Kid[]; flagRow?: boolean }, close: () => void, back?: () => void): HTMLElement {
  return h(
    "div",
    { class: "sh-head" },
    back && h("button", { class: "ibtn round", type: "button", "data-k": "add-back", "aria-label": t.back, on: { click: back } }, icon("back", 18)),
    tile(o.tone, o.ico),
    h("div", { class: "grow" }, h("h3", { id: o.id }, o.title), o.sub && h("p", { class: `sub${o.flagRow ? " flagrow" : ""}` }, ...(Array.isArray(o.sub) ? o.sub : [o.sub]))),
    h("button", { class: "ibtn round", type: "button", "data-k": "modal-x", "aria-label": t.close, on: { click: close } }, icon("close", 16)),
  );
}

export const grab = () => h("div", { class: "grab" });

// ---- the "add a device" dialog (and the key of a device) ----

/** What the dialog holds: the new device's form, then (after "Create") its key; the key of a device that is shown; or the new key of a stale one. */
export function addModal(c: AmzCtx): Kid[] {
  const { d, s } = c;
  const am = d.amnezia;
  if (!am) return [];
  const find = (id: string | null) => (id ? am.devices.find((x) => x.id === id) : undefined);
  const renewing = find(s.amz.renew);
  if (renewing) return keySheet(c, renewing, "renew");
  const made = find(s.amz.created);
  if (made) return keySheet(c, made, "created");
  const shown = find(s.amz.open);
  if (shown) return keySheet(c, shown, "shown");
  return [grab(), ...addForm(c)];
}

/** The accessible name of the dialog. */
export function modalLabel(c: AmzCtx): string {
  const { d, s, t } = c;
  const x = (id: string | null) => (id ? d.amnezia?.devices.find((y) => y.id === id) : undefined);
  const r = x(s.amz.renew);
  if (r) return t.renewT(label(r, t));
  const k = x(s.amz.created) ?? x(s.amz.open);
  return k ? t.keyFor(label(k, t)) : t.newDevT;
}

function addForm(c: AmzCtx): Kid[] {
  const { d, s, a, t, support } = c;
  const am = d.amnezia!;
  const app = keyAppName(d, null);
  const f = s.amz.form;
  const busy = s.amz.busy === "add";
  const choices = profileChoices(am.profiles, t, s.lang, app, flagsOn(s));
  const chosen = choices.find((p) => p.id === f.profile) ?? choices[0];
  const err = s.amz.error && s.amz.errorAt === "add" && s.amz.error !== "device_limit";
  const full = s.amz.error === "device_limit" && s.amz.errorAt === "add";
  const plat = (p: (typeof addPlatforms)[number]) =>
    h(
      "button",
      { class: `plat${f.platform === p ? " on" : ""}`, type: "button", role: "radio", "aria-checked": f.platform === p, "data-k": `amz-platform-${p}`, "data-autofocus": f.platform === p, on: { click: () => a.amz.form({ platform: p }, true) } },
      icon(p === "other" ? "other" : deviceIcon(p), 20),
      t.platforms[p],
    );
  return [
    sheetHead(t, { tone: "mint", ico: "plus", id: "dlg-t", title: t.newDevT, sub: t.newDevS(app) }, () => a.amz.add(false), ways(d).length > 1 ? () => a.amz.pane("pick") : undefined),
    h(
      "form",
      {
        class: "stack g20",
        on: {
          submit: (e) => {
            e.preventDefault();
            if (!busy) a.amz.create();
          },
        },
      },
      h("div", { class: "fld" }, h("label", { id: "nd-p" }, t.awgPlatform), h("div", { class: "plats six compact", role: "radiogroup", "aria-labelledby": "nd-p" }, ...addPlatforms.map(plat))),
      h(
        "div",
        { class: "fld" },
        h("div", { class: "fl-h" }, h("label", { for: "nd-n" }, t.awgName), h("span", { class: "hint" }, t.optional)),
        h("input", { class: "inp", id: "nd-n", "data-k": "amz-label", type: "text", maxlength: 40, autocomplete: "off", placeholder: t.awgNamePh[f.platform as keyof typeof t.awgNamePh], value: f.label, on: { input: (e) => a.amz.form({ label: (e.target as HTMLInputElement).value }) } }),
      ),
      choices.length > 1 &&
        chosen &&
        h(
          "div",
          { class: "fld" },
          h("label", { id: "nd-v" }, t.profile),
          h(
            "div",
            { class: "pick" },
            h("span", { class: "stack g4 grow" }, h("span", { class: "t" }, chosen.label), h("span", { class: "s" }, t.profileSub[chosen.kind])),
            icon("chev", 16),
            h(
              "select",
              { "data-k": "amz-profile", "aria-labelledby": "nd-v", on: { change: (e) => a.amz.form({ profile: (e.target as HTMLSelectElement).value }, true) } },
              ...choices.map((p) => h("option", { value: p.id, selected: p.id === chosen.id }, p.label)),
            ),
          ),
        ),
      err && note("bad", "warn", t.err(s.amz.error, s.amz.retryMin)),
      full &&
        h(
          "div",
          { class: "note bad sm", role: "alert" },
          icon("warn", 16),
          h("div", { class: "stack g8" }, h("p", null, h("b", null, t.fullT), " ", t.fullD), support && h("a", { class: "tlink", href: support, target: "_blank", rel: "noopener noreferrer", style: { "min-height": "28px" } }, t.writeInTg)),
        ),
      h("button", { class: "btn pri", type: "submit", "data-k": "modal-create", disabled: busy }, busy ? t.awgBusy : t.awgCreate),
    ),
  ];
}

// ---- the key of one device (in the dialog) ----

export type KeyView = "this-phone" | "this-desktop" | "phone-qr" | "other-desktop";
export type Where = "this" | "phone" | "other";

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

function keySheet(c: AmzCtx, x: AwgDevice, mode: "shown" | "created" | "renew"): Kid[] {
  const { d, s, a, t } = c;
  const name = label(x, t);
  const app = keyAppName(d, x.platform as Platform);
  const renew = mode === "renew";
  const head = sheetHead(
    t,
    { tone: renew ? "sand" : "mint", ico: renew ? "refresh" : "key", id: "dlg-t", title: renew ? t.renewT(name) : t.keyFor(name), sub: renew ? t.renewS : mode === "created" ? t.awgReadyH : t.keyAgain },
    () => a.amz.add(false),
  );
  const list = s.amz.configs[x.id];
  if (!list || list.length === 0) return [grab(), head, h("p", { class: "hint cfg-wait" }, list ? t.noProfile : t.awgBusy)];

  const flags = flagsOn(s);
  const i = Math.min(s.amz.node[x.id] ?? 0, list.length - 1);
  const cfg = list[i]!;
  const filename = cfg.filename || "amnezia.conf";
  const opts = whereOptions(s.amz.here, t);
  const want = (s.amz.where[x.id] as Where | undefined) ?? defaultWhere(x.platform, s.amz.here);
  const where: Where = opts.some((o) => o.id === want) ? want : (opts[0]?.id ?? "other");
  const view = keyView(x.platform, s.amz.here, where);

  const first = <T extends HTMLElement>(el: T): T => {
    el.dataset.autofocus = "";
    return el;
  };
  const dl = (cls: string, k = "") => h("button", { class: `btn ${cls}`, type: "button", "data-k": `amz-dl-${x.id}${k}`, on: { click: () => a.download(filename, cfg.conf) } }, icon("file"), h("span", null, t.downloadFile));
  const cp = (cls: string) => copyButton(a, { text: cfg.vpn_key, label: t.copyKey, done: t.copiedShort, toast: t.keyCopied, cls, key: `amz-key-${x.id}` });
  const qr = () => {
    const svg = qrSvg(cfg.conf, t.qrKeyAlt(app));
    return svg ? h("div", { class: "qr l" }, svg) : note("", "qr", t.qrTooBig);
  };
  const fileOnly = cfg.warnings.includes("amnezia_desktop_mtu") && note("warn", "warn", t.desktopFileOnly);
  // the old connection in the app is called by the country (the mockup: "Germany"), as the new keys are named
  const oldConn = countryName(cfg.country_code, s.lang) || cfg.label;
  const [pasteStep, deleteStep] = t.renewSteps(app, oldConn);

  const body: Kid[] = [];
  if (view === "this-phone") {
    body.push(
      first(cp("pri")),
      h("ol", { class: "steps-box", "aria-label": t.stepsAria(app) }, ...(renew ? [pasteStep, deleteStep] : t.phoneSteps(app)).map((step, k) => h("li", null, h("span", { class: "n" }, String(k + 1)), h("span", null, ...rich(step))))),
    );
  } else if (view === "this-desktop") {
    body.push(
      h("div", { class: "row g16", style: { "flex-wrap": "wrap" } }, first(dl("pri fit")), h("span", { class: "mono keyfile" }, filename)),
      h("p", { class: "sm" }, ...rich(t.desktopSteps(app))),
      fileOnly,
    );
  } else if (view === "phone-qr") {
    body.push(
      h(
        "div",
        { class: "keyq" },
        qr(),
        h("div", { class: "keyq-t stack g12" }, h("p", { class: "b" }, t.qrScan(app)), h("p", { class: "sm mut" }, ...rich(t.qrScanHow)), h("p", { class: "lbl" }, t.orElse), first(dl("sec")), cp("sec")),
      ),
    );
  } else {
    body.push(
      h("div", { class: "stack g14", style: { "align-items": "flex-start", padding: "8px 0" } }, tile("lav", "laptop"), h("p", { class: "h3" }, t.openThereT), h("p", { class: "sm mut" }, t.openThereD)),
      h("div", { class: "div" }),
      h("div", { class: "stack g8" }, h("p", { class: "lbl" }, t.orFile), first(dl("sec"))),
    );
  }
  if (renew && view !== "this-phone") body.push(note("", "info", deleteStep));

  const labels = nodeLabels(list, flags);
  const versions = versionsFor(x.platform, cfg.min_clients.length ? cfg.min_clients : x.min_clients);
  return [
    grab(),
    head,
    h(
      "div",
      { class: "cfg" },
      h("div", { class: "seg", role: "radiogroup", "aria-label": t.where }, ...opts.map((o) => h("button", { class: where === o.id ? "on" : "", type: "button", role: "radio", "aria-checked": where === o.id, "data-k": `amz-where-${o.id}`, on: { click: () => a.amz.where(x.id, o.id) } }, o.label))),
      list.length > 1 &&
        h(
          "div",
          { class: "fld" },
          h("label", { id: `k-c-${x.id}` }, t.country),
          h(
            "div",
            { class: "pick one only-m" },
            h("span", { class: "t grow" }, labels[i]),
            h("span", { class: "hint" }, t.moreCountries(list.length - 1)),
            icon("chev", 16),
            h("select", { "data-k": `amz-node-${x.id}`, "aria-labelledby": `k-c-${x.id}`, on: { change: (e) => a.amz.node(x.id, Number((e.target as HTMLSelectElement).value)) } }, ...labels.map((l, k) => h("option", { value: String(k), selected: k === i }, l))),
          ),
          h("div", { class: "seg pills only-w", role: "radiogroup", "aria-labelledby": `k-c-${x.id}` }, ...labels.map((l, k) => h("button", { class: i === k ? "on" : "", type: "button", role: "radio", "aria-checked": i === k, "data-k": `amz-node-${x.id}-${k}`, on: { click: () => a.amz.node(x.id, k) } }, l))),
          h("p", { class: "hint only-w" }, t.countryH),
        ),
      ...body,
      h("div", { class: "div" }),
      h(
        "div",
        { class: "row g16", style: { "justify-content": "space-between", "align-items": "flex-end" } },
        h(
          "div",
          { class: "foot-note grow" },
          versions.length > 0 && h("p", { class: "hint row g8" }, icon("info", 16), t.needApp(versions)),
          h("p", { class: "priv" }, icon("shield", 16), t.awgSecret),
        ),
        h("button", { class: "btn sec sm only-w", type: "button", "data-k": "modal-done", on: { click: () => a.amz.add(false) } }, t.awgDone),
      ),
    ),
  ];
}

// ---- "new key needed" ----

/** The devices that need a new key: the card has a button for each. */
export const staleDevices = (d: MgData): AwgDevice[] => d.amnezia?.devices.filter((x) => x.stale) ?? [];

export function staleCard(c: AmzCtx): HTMLElement | null {
  const { d, s, a, t } = c;
  const stale = staleDevices(d);
  if (stale.length === 0 || !d.access.amnezia || d.user.status !== "active") return null;
  const app = keyAppName(d, null);
  const one = stale.length === 1;
  const serve = selfServe(d);
  const dnsOnly = stale.every((x) => x.stale_reason === "dns");
  const err = s.amz.error && s.amz.errorAt.startsWith("renew:") && note("bad", "warn", t.err(s.amz.error, s.amz.retryMin));
  return h(
    "section",
    { class: "card stack g14 stale", style: { padding: "18px", "border-color": "var(--warning-line)" }, "aria-labelledby": "stale-t" },
    h("div", { class: "row g10" }, dot("warn"), h("p", { class: "h3 grow", id: "stale-t" }, one ? t.staleT(label(stale[0]!, t)) : t.staleTs)),
    h("p", { class: "sm mut", style: { "margin-top": "-6px" } }, serve ? (dnsOnly ? t.staleDns : t.staleD) : t.keysByAdmin),
    serve &&
      h(
        "ol",
        { class: "stack g10" },
        ...t.staleSteps(app).map((step, i) => h("li", { class: "row g10 sm", style: { "align-items": "flex-start" } }, h("span", { class: "stepno" }, String(i + 1)), h("span", null, ...rich(step)))),
      ),
    err,
    ...(serve
      ? stale.map((x) =>
          h("button", { class: "btn pri", type: "button", "data-k": `amz-stale-${x.id}`, disabled: s.amz.busy !== "", on: { click: () => a.amz.renew(x.id) } }, icon("refresh"), s.amz.busy === `renew:${x.id}` ? t.awgBusy : one ? t.newKey : t.newKeyFor(label(x, t))),
        )
      : []),
  );
}
