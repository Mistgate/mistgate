import { h, type Kid } from "./dom";
import type { Dict } from "./i18n";
import { icon } from "./icons";
import {
  addPlatforms,
  appsFor,
  atDeviceLimit,
  canAddDevice,
  deviceIcon,
  fmtAgo,
  isDesktop,
  isPhone,
  keyAppName,
  nodeLabels,
  platformWord,
  profileChoices,
  safeUrl,
  selfServe,
  storeLabel,
  versionsFor,
} from "./logic";
import { qrSvg } from "./qr";
import type { AwgConfig, AwgDevice, Lang, MgData, Platform } from "./types";

// The AmneziaVPN way of the user page: the key devices (one key pair each), the "add a device" dialog, the key of a device
// (its QR code, file and vpn:// text) and the "new key needed" card. State and actions are the page's (view.ts, main.ts);
// nothing here talks to the network. Keys hold a private key: they exist only in `AmzState.configs`, in memory, and are
// asked for one device at a time.

export type AmzState = {
  /** The device whose key is open in its row. */
  open: string | null;
  /** The "add a device" dialog is open. */
  adding: boolean;
  /** The device this dialog just created: the dialog then shows its key instead of the form. */
  created: string | null;
  /** The stale device whose new key the dialog shows ("new key needed"). */
  renew: string | null;
  /** "add", a device id, or "renew:<id>" while a call is in flight; "" otherwise. */
  busy: string;
  /** The last failure, as the panel's code; "" when none. `errorAt` is the busy key of the call that failed. */
  error: string;
  errorAt: string;
  retryMin: number;
  confirm: { id: string; kind: "remove" | "rotate" } | null;
  configs: Record<string, AwgConfig[]>;
  node: Record<string, number>;
  form: { profile: string; platform: string; label: string };
  /** The platform this page is open on ("" unknown): a key for this very device is copied or downloaded, not scanned. */
  here: string;
};

export const newAmzState = (platform: string, profile: string, here = ""): AmzState => ({
  open: null,
  adding: false,
  created: null,
  renew: null,
  busy: "",
  error: "",
  errorAt: "",
  retryMin: 0,
  confirm: null,
  configs: {},
  node: {},
  form: { profile, platform, label: "" },
  here,
});

export type AmzActions = {
  /** Opens (true) the "add a device" dialog or closes (false) whichever dialog is open. */
  add(open: boolean): void;
  /** Form fields change without a re-render: the page would drop what is being typed. */
  form(patch: Partial<AmzState["form"]>): void;
  create(): void;
  show(id: string | null): void;
  /** "New key needed": fetches the key of a stale device and shows it in the dialog. */
  renew(id: string): void;
  node(id: string, i: number): void;
  ask(c: AmzState["confirm"]): void;
  rotate(id: string): void;
  remove(id: string): void;
};

/** What the page offers its parts: copy (with a callback for the button's own "copied" state) and download. */
export type Tools = { copy(text: string, message: string, done?: () => void): void; download(filename: string, text: string): void };
type Ctx = { d: MgData; s: { lang: Lang; amz: AmzState }; a: { amz: AmzActions } & Tools; t: Dict };

const dot = () => h("i", { class: "dot" });
const chev = () => h("i", { class: "chev" }, "›");
/** Windows has no flag emoji (it draws "DE"): the choices there are the country's name alone. */
const flags = (s: { amz: AmzState }) => s.amz.here !== "windows";

/** The name of a device: its label, else its platform's word, else "Device". */
export const label = (x: AwgDevice, t: Dict) => x.label || platformWord(x.platform, t) || t.devGeneric;

function whenText(x: AwgDevice, c: Ctx): string {
  const { t, s } = c;
  return x.online ? t.online : x.last_handshake_unix ? t.awgHandshake(fmtAgo(x.last_handshake_unix, s.lang)) : t.awgNever;
}

function select(key: string, aria: string, value: string, options: [string, string][], onChange: (v: string) => void, first = false) {
  return h(
    "select",
    { class: "sel", "data-k": key, "data-autofocus": first, "aria-label": aria, on: { change: (e) => onChange((e.target as HTMLSelectElement).value) } },
    ...options.map(([v, text]) => h("option", { value: v, selected: v === value }, text)),
  );
}

/**
 * A button that copies `text` and says so on itself: its label turns into `done` with a tick for a moment. `toast` is the
 * page's toast text. The page's copy() calls back only when the clipboard took the text.
 */
export function copyButton(a: Tools, o: { text: string; label: string; done: string; toast: string; cls?: string; key?: string }): HTMLButtonElement {
  const text = h("span", null, o.label);
  const own = o.cls?.split(" ") ?? [];
  const look = own.includes("pri") || own.includes("tlink") ? "" : "sec ";
  const btn = h("button", { class: `${own.includes("tlink") ? "" : "btn "}${look}${o.cls ?? ""}`.trim(), type: "button", "data-k": o.key, on: { click: () => a.copy(o.text, o.toast, flash) } }, icon("copy"), text);
  let timer = 0;
  function flash() {
    btn.classList.add("ok");
    btn.replaceChildren(icon("check"), text);
    text.textContent = o.done;
    clearTimeout(timer);
    timer = window.setTimeout(() => {
      btn.classList.remove("ok");
      btn.replaceChildren(icon("copy"), text);
      text.textContent = o.label;
    }, 1800);
  }
  return btn;
}

/** A download link with the store under the word: "Download / App Store". Never cut short. */
export function downloadButton(url: string, t: Dict, key: string): HTMLAnchorElement {
  return h(
    "a",
    { class: "btn sec dl", href: url, target: "_blank", rel: "noopener noreferrer", "data-k": key },
    icon("download"),
    h("span", { class: "dl-t" }, h("b", null, t.download), h("small", null, storeLabel(url, t))),
  );
}

/** Numbered steps of a way ("1 Install…", "2 Add…"); the number is left out when there is only one. */
export function steps(items: { title: Kid; body: Kid[] }[]): HTMLElement {
  const numbered = items.length > 1;
  return h(
    "ol",
    { class: `steps${numbered ? "" : " single"}` },
    ...items.map((it, i) =>
      h("li", { class: "step" }, numbered && h("span", { class: "n mono", "aria-hidden": "true" }, String(i + 1)), h("div", { class: "step-b" }, h("b", { class: "step-t" }, it.title), ...it.body)),
    ),
  );
}

// ---- the "add a device" dialog (and the new key of a stale device) ----

/** What the dialog holds: the form, then (after "Create") the new device's key; or the new key of a stale device. */
export function addModal(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const am = d.amnezia;
  if (!am) return [];
  const app = keyAppName(d, null);
  const head = (title: string) =>
    h(
      "div",
      { class: "mhead" },
      h("b", { class: "mt" }, title),
      h("button", { class: "mx", type: "button", "data-k": "modal-x", "aria-label": t.close, on: { click: () => a.amz.add(false) } }, icon("close")),
    );
  const done = h("div", { class: "mfoot" }, h("button", { class: "btn sec", type: "button", "data-k": "modal-done", on: { click: () => a.amz.add(false) } }, t.awgDone));

  const renewing = s.amz.renew ? am.devices.find((x) => x.id === s.amz.renew) : undefined;
  if (renewing) {
    return [
      head(t.renewT(label(renewing, t))),
      h("p", { class: "mut sm mlead" }, t.renewH(app)),
      h("p", { class: "hintbox calm" }, t.renewOld(app)),
      configBlock(renewing, c),
      done,
    ];
  }
  const made = s.amz.created ? am.devices.find((x) => x.id === s.amz.created) : undefined;
  const err = s.amz.error && s.amz.errorAt === "add" && h("p", { class: "err", role: "alert" }, t.err(s.amz.error, s.amz.retryMin));
  if (made) return [head(t.awgReadyT(label(made, t))), h("p", { class: "mut sm mlead" }, t.awgReadyH), configBlock(made, c), done];

  const f = s.amz.form;
  const busy = s.amz.busy === "add";
  const choices = profileChoices(am.profiles, t, s.lang, app, flags(s));
  return [
    head(t.awgAddT),
    h(
      "form",
      {
        class: "addf",
        on: {
          submit: (e) => {
            e.preventDefault();
            if (!busy) a.amz.create();
          },
        },
      },
      h("p", { class: "mut sm" }, t.awgAddH),
      choices.length > 1 &&
        h("label", { class: "fld" }, h("span", { class: "eyebrow" }, t.profile.toUpperCase()), select("amz-profile", t.profile, f.profile, choices.map((p) => [p.id, p.label]), (v) => a.amz.form({ profile: v }), true)),
      h("label", { class: "fld" }, h("span", { class: "eyebrow" }, t.awgPlatform.toUpperCase()), select("amz-platform", t.awgPlatform, f.platform, addPlatforms.map((p) => [p, t.platforms[p]]), (v) => a.amz.form({ platform: v }), choices.length <= 1)),
      h(
        "label",
        { class: "fld" },
        h("span", { class: "eyebrow" }, t.awgName.toUpperCase()),
        h("input", { class: "inp", "data-k": "amz-label", type: "text", maxlength: 40, autocomplete: "off", placeholder: t.awgNamePh[f.platform as keyof typeof t.awgNamePh], value: f.label, on: { input: (e) => a.amz.form({ label: (e.target as HTMLInputElement).value }) } }),
      ),
      err,
      h(
        "div",
        { class: "mfoot split" },
        h("button", { class: "btn sec", type: "button", "data-k": "modal-cancel", disabled: busy, on: { click: () => a.amz.add(false) } }, t.awgCancel),
        h("button", { class: "btn pri", type: "submit", "data-k": "modal-create", disabled: busy }, busy ? t.awgBusy : t.awgCreate),
      ),
    ),
  ];
}

// ---- the key of one device (in its row, and in the dialog) ----

/** How a key is set up: on this very phone it is copied, on this very computer the file is downloaded, else scanned. */
export function keyMode(platform: string, here: string): "phone" | "desktop" | "other" {
  if (!here || platform !== here) return "other";
  return isPhone(here) ? "phone" : isDesktop(here) ? "desktop" : "other";
}

function configBlock(x: AwgDevice, c: Ctx): HTMLElement {
  const { d, s, a, t } = c;
  const list = s.amz.configs[x.id];
  if (!list) return h("div", { class: "cfg" }, h("p", { class: "mut sm" }, t.awgBusy));
  if (list.length === 0) return h("div", { class: "cfg" }, h("p", { class: "mut sm" }, t.noProfile));
  const i = Math.min(s.amz.node[x.id] ?? 0, list.length - 1);
  const cfg = list[i]!;
  const app = keyAppName(d, x.platform as Platform);
  const filename = cfg.filename || "amnezia.conf";
  const dl = (cls: string) => h("button", { class: `btn ${cls}`, type: "button", "data-k": `amz-dl-${x.id}`, on: { click: () => a.download(filename, cfg.conf) } }, icon("download"), h("span", null, t.downloadFile));
  const cp = (cls: string) => copyButton(a, { text: cfg.vpn_key, label: t.copyKey, done: t.copiedShort, toast: t.keyCopied, cls, key: `amz-key-${x.id}` });
  const qr = (big: boolean) => {
    const svg = qrSvg(cfg.conf, t.qrScan(app));
    return svg ? h("div", { class: `qr${big ? " big" : ""}` }, svg) : h("p", { class: "mut sm" }, t.qrTooBig);
  };
  const qrFold = (extra: HTMLElement) =>
    h("details", { class: "qrx" }, h("summary", null, h("span", null, t.qrOtherDev), chev()), h("div", { class: "qrbox" }, qr(false), h("span", { class: "hint c" }, t.qrHowKey(app)), extra));
  const fileOnly = cfg.warnings.includes("amnezia_desktop_mtu") && h("p", { class: "hintbox" }, t.desktopFileOnly(app));

  // the one accent button of the key; a dialog that opens on it puts the keyboard there
  const first = <T extends HTMLElement>(el: T): T => {
    el.dataset.autofocus = "";
    return el;
  };
  let body: Kid[];
  switch (keyMode(x.platform, s.amz.here)) {
    case "phone":
      body = [first(cp("pri")), h("ol", { class: "ksteps" }, ...t.phoneSteps(app).map((step) => h("li", null, step))), qrFold(dl("sec sm"))];
      break;
    case "desktop":
      body = [first(dl("pri")), h("p", { class: "how" }, t.desktopSteps(app)), fileOnly, qrFold(cp("sec sm"))];
      break;
    default:
      // a computer cannot scan a code: open the page there; a phone (or anything else) scans it
      body = isDesktop(x.platform)
        ? [h("p", { class: "hintbox calm" }, t.openThere), h("div", { class: "cfg-actions" }, dl("sec"), cp("sec")), fileOnly]
        : [
            h("div", { class: "cfg-main" }, qr(true), h("div", { class: "cfg-side" }, h("b", null, t.qrScan(app)), h("p", { class: "mut sm" }, t.qrHowKey(app)))),
            h("div", { class: "cfg-actions" }, dl("sec"), cp("sec")),
          ];
  }
  const versions = versionsFor(x.platform, cfg.min_clients.length ? cfg.min_clients : x.min_clients);
  return h(
    "div",
    { class: "cfg" },
    list.length > 1 &&
      h(
        "label",
        { class: "fld" },
        h("span", { class: "eyebrow" }, t.country.toUpperCase()),
        select(`amz-node-${x.id}`, t.country, String(i), nodeLabels(list, flags(s)).map((l, k) => [String(k), l]), (v) => a.amz.node(x.id, Number(v))),
        h("span", { class: "hint" }, t.countryH),
      ),
    ...body,
    versions.length > 0 && h("p", { class: "vers mut" }, t.needApp(versions)),
    h("p", { class: "secret", role: "note" }, icon("shield"), h("span", null, t.awgSecret)),
  );
}

// ---- one key device ----

function row(x: AwgDevice, c: Ctx, canAct: boolean): HTMLElement {
  const { d, s, a, t } = c;
  const open = s.amz.open === x.id;
  const busy = s.amz.busy === x.id;
  const ask = s.amz.confirm?.id === x.id ? s.amz.confirm : null;
  const name = label(x, t);
  const word = platformWord(x.platform, t);
  const meta = [word && word !== name ? word : "", whenText(x, c)].filter(Boolean).join(" · ");
  const lnk = (key: string, text: string, onClick: () => void, cls = "") =>
    h("button", { class: `lnk${cls ? ` ${cls}` : ""}`, type: "button", "data-k": key, disabled: busy, on: { click: onClick } }, text);
  const removing = ask?.kind === "remove";
  return h(
    "div",
    { class: `krow${open ? " open" : ""}${x.stale ? " stale-row" : ""}`, id: `amz-row-${x.id}` },
    h("span", { class: "kico", "aria-hidden": "true" }, icon(deviceIcon(x.platform))),
    h("div", { class: "ktxt" }, h("b", null, h("span", { class: "kname" }, name), x.stale && h("span", { class: "tag" }, t.awgStale)), h("span", { class: x.online ? "meta on" : "meta" }, meta)),
    canAct &&
      h(
        "div",
        { class: "kact" },
        lnk(`amz-show-${x.id}`, open ? t.hideKey : t.showKey, () => a.amz.show(open ? null : x.id), "pri"),
        lnk(`amz-rot-${x.id}`, t.rotateKey, () => a.amz.ask({ id: x.id, kind: "rotate" })),
        lnk(`amz-del-${x.id}`, t.removeKey, () => a.amz.ask({ id: x.id, kind: "remove" }), "bad"),
      ),
    ask &&
      h(
        "div",
        { class: `ask${removing ? " bad" : ""}`, role: "alertdialog", "aria-label": name },
        h("p", null, removing ? t.removeQ(name) : t.rotateQ(name, keyAppName(d, x.platform as Platform))),
        h(
          "div",
          { class: "btnrow" },
          h("button", { class: "btn sec sm", type: "button", "data-k": `amz-no-${x.id}`, disabled: busy, on: { click: () => a.amz.ask(null) } }, t.awgCancel),
          h(
            "button",
            { class: `btn sm ${removing ? "bad" : "pri"}`, type: "button", "data-k": `amz-yes-${x.id}`, disabled: busy, on: { click: () => (removing ? a.amz.remove(x.id) : a.amz.rotate(x.id)) } },
            busy ? t.awgBusy : removing ? t.removeYes : t.rotateYes,
          ),
        ),
      ),
    open && configBlock(x, c),
  );
}

// ---- the AmneziaVPN way ----

/**
 * The key way, a card of its own beside the subscription: install the app, add a device
 * (it gets its own key), then the user's keys with their actions. Without self-service the rows have no buttons and the
 * card says the admin issues keys; the admin's preview has no address to call and shows no buttons either.
 */
export function keyWay(c: Ctx, platform: Platform | null, support: string): HTMLElement {
  const { d, s, a, t } = c;
  const am = d.amnezia!;
  const apps = appsFor(d, platform, "amnezia");
  const app = keyAppName(d, platform);
  const main = apps[0];
  const download = main ? safeUrl(main.download_url) : "";
  const serve = selfServe(d);
  const full = atDeviceLimit(d);
  const used = d.user.devices_used;
  const items: { title: Kid; body: Kid[] }[] = [];
  if (main && download) items.push({ title: t.stepInstall(main.name), body: [main.description && h("p", { class: "step-d mut" }, main.description), downloadButton(download, t, "key-get")] });

  if (!am.self_service) {
    // keys come from the admin: the step says whom to ask, and the rows below have no buttons
    items.push({ title: t.stepAskKey, body: [h("p", { class: "mut step-d" }, t.keysByAdmin), support && h("a", { class: "btn sec", href: support, target: "_blank", rel: "noopener noreferrer" }, t.write)] });
  } else if (am.profiles.length === 0) {
    items.push({ title: t.stepAddDev, body: [h("p", { class: "mut step-d" }, t.noProfile)] });
  } else {
    items.push({
      title: t.stepAddDev,
      body: [
        h("button", { class: "btn pri", type: "button", "data-k": "amz-add", disabled: !canAddDevice(d), on: { click: () => a.amz.add(true) } }, icon("plus"), h("span", null, t.awgAdd)),
        full && h("p", { class: "limit", role: "note" }, t.limit(used, d.user.device_limit, serve && am.devices.length > 0)),
      ],
    });
  }

  const rowErr = s.amz.error && !s.amz.adding && !s.amz.renew && s.amz.errorAt !== "add" && !s.amz.errorAt.startsWith("renew:");
  const counter = d.user.device_limit > 0 ? t.used(used, d.user.device_limit) : String(am.devices.length);
  return h(
    "section",
    { class: "card way", "aria-labelledby": "way-key-t", "data-way": "key" },
    wayHead("way-key-t", "key", "mint", t.keyT(app), t.keyD),
    steps(items),
    apps.length > 1 && alts(apps.slice(1), t, (x) => [safeUrl(x.download_url) && downloadButton(safeUrl(x.download_url), t, `key-get-${x.name}`)]),
    h(
      "div",
      { class: "sub-list" },
      h("div", { class: "sub-h" }, h("b", { class: "sub-t" }, t.yourKeys), am.devices.length > 0 && h("span", { class: `cnt${full ? " warn" : ""}` }, counter)),
      rowErr && h("p", { class: "err", role: "alert" }, t.err(s.amz.error, s.amz.retryMin)),
      am.devices.length > 0 ? h("div", { class: "krows" }, ...am.devices.map((x) => row(x, c, serve))) : h("p", { class: "mut pad" }, t.keysNone),
    ),
  );
}

/** The head of a way: its tinted icon, its name and the one line that says what it is. */
export function wayHead(id: string, glyph: "link" | "key", tone: string, title: string, line: string): HTMLElement {
  return h(
    "header",
    { class: "way-h" },
    h("span", { class: "way-ico", "data-tone": tone, "aria-hidden": "true" }, icon(glyph)),
    h("div", { class: "way-ht" }, h("b", { class: "way-t", id }, title), h("span", { class: "mut" }, line)),
  );
}

/** The other apps of a way on the platform: compact, with their own (secondary) buttons, never folded away. */
export function alts(apps: MgData["apps"], t: Dict, actions: (a: MgData["apps"][number]) => Kid[]): HTMLElement {
  return h(
    "div",
    { class: "alts" },
    h("b", { class: "sub-t" }, t.otherApps),
    ...apps.map((x) => h("div", { class: "alt" }, h("div", { class: "alt-t" }, h("b", null, x.name), x.description && h("span", { class: "mut" }, x.description)), h("div", { class: "alt-a" }, ...actions(x)))),
  );
}

// ---- "new key needed" ----

/** The devices that need a new key: the card has a button for each. */
export const staleDevices = (d: MgData): AwgDevice[] => d.amnezia?.devices.filter((x) => x.stale) ?? [];

export function staleCard(c: Ctx): HTMLElement | null {
  const { d, s, a, t } = c;
  const stale = staleDevices(d);
  if (stale.length === 0 || !d.access.amnezia) return null;
  const app = keyAppName(d, null);
  const one = stale.length === 1;
  const serve = selfServe(d);
  const err = s.amz.error && s.amz.errorAt.startsWith("renew:") && h("p", { class: "err", role: "alert" }, t.err(s.amz.error, s.amz.retryMin));
  return h(
    "section",
    { class: "card stale" },
    h(
      "div",
      { class: "stale-main" },
      h("div", { class: "stale-t" }, dot(), h("b", null, one ? t.staleT(label(stale[0]!, t)) : t.staleTs)),
      h("p", { class: "mut" }, serve ? t.staleD : t.keysByAdmin),
      serve && h("ol", { class: "steps3" }, ...t.staleSteps(app).map((step, i) => h("li", null, h("span", { class: "n mono" }, String(i + 1)), h("span", null, step)))),
      err,
    ),
    serve &&
      h(
        "div",
        { class: "stale-act" },
        ...stale.map((x) =>
          h(
            "button",
            { class: "btn pri", type: "button", "data-k": `amz-stale-${x.id}`, disabled: s.amz.busy !== "", on: { click: () => a.amz.renew(x.id) } },
            s.amz.busy === `renew:${x.id}` ? t.awgBusy : one ? t.newKey : t.newKeyFor(label(x, t)),
          ),
        ),
      ),
  );
}
