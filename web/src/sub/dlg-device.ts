import { label } from "./amnezia";
import { frame, errLine, foot, spin } from "./dlg-kit";
import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { deviceAbout, keyAppName, platformWord, selfServe } from "./logic";
import type { Ctx } from "./state";
import type { AwgDevice, Device, Platform } from "./types";

// The windows of one device, after the boards V2DlgMenu / V2DlgRename / V2DlgDelete / V2DlgStale (and, for "replace the key",
// the delete window's twin): the menu of what can be done with it, its name, the question before removing it or replacing its
// key, and the question of a key that is no longer valid. The state is the page's (s.amz.menu, rename, confirm, stale); a call in
// flight and its failure are read from s.amz.busy / error / errorAt, which name the device (or "rename:<id>", "renew:<id>").

export type Who = { id: string; name: string; key: AwgDevice | null; link: Device | null };

/** The device an id names: a key (AmneziaVPN) or a device of an app on the link; none for a shared place or an id the page does not have. */
export function who(c: Ctx, id: string | null): Who | null {
  if (!id) return null;
  const { d, t } = c;
  const key = d.amnezia?.devices.find((x) => x.id === id) ?? null;
  if (key) return { id, name: label(key, t), key, link: null };
  const link = d.devices.find((x) => x.id === id && x.kind !== "shared") ?? null;
  return link ? { id, name: link.model || platformWord(link.platform, t) || t.devGeneric, key: null, link } : null;
}

const busyAt = (c: Ctx, key: string) => c.s.amz.busy === key;
const errorAt = (c: Ctx, key: string) => (c.s.amz.error && c.s.amz.errorAt === key ? errLine(c.t.err(c.s.amz.error, c.s.amz.retryMin), "dlg-err") : null);

// ---- the menu ----

type Act = { k: string; ico: IconName; text: string; bad?: boolean; run: () => void };

/** What the menu offers: a key shows its key and can have it replaced while the subscription is active; any device is renamed and removed as far as the page allows. */
function acts(c: Ctx, w: Who): Act[] {
  const { d, a, t } = c;
  const id = w.id;
  const out: Act[] = [];
  const ren = () => out.push({ k: "ren", ico: "pencil", text: t.rename, run: () => a.amz.renameStart(id) });
  const del = () => out.push({ k: "del", ico: "bin", text: t.dgDelete, bad: true, run: () => a.amz.ask({ id, kind: "remove" }) });
  if (w.key) {
    if (!selfServe(d)) return out;
    const live = d.user.status === "active";
    if (live) out.push({ k: "show", ico: "eye", text: t.showKey, run: () => a.amz.show(id) });
    ren();
    if (live) out.push({ k: "rot", ico: "refresh", text: t.rotateKey, run: () => a.amz.ask({ id, kind: "rotate" }) });
    del();
  } else {
    if (w.link!.can_rename) ren();
    if (w.link!.can_remove) del();
  }
  return out;
}

/** The menu can be shown: the device is there and there is something to do. */
export const menuHas = (c: Ctx): boolean => {
  const w = who(c, c.s.amz.menu);
  return !!w && acts(c, w).length > 0;
};

export function menuWindow(c: Ctx): Kid[] {
  const { a, t } = c;
  const w = who(c, c.s.amz.menu)!;
  return frame(c, {
    label: t.dgMenuAria,
    title: w.name,
    small: true,
    close: () => a.amz.menu(null),
    body: h(
      "div",
      { class: "a-acts" },
      ...acts(c, w).map((x, i) =>
        h("button", { class: `a-act${x.bad ? " bad" : ""}`, type: "button", "data-k": `dlg-m-${x.k}`, "data-autofocus": i === 0 ? "" : false, on: { click: x.run } }, icon(x.ico), x.text),
      ),
    ),
  });
}

// ---- rename ----

export const renameHas = (c: Ctx): boolean => !!c.s.amz.rename && !!who(c, c.s.amz.rename.id);

export function renameWindow(c: Ctx): Kid[] {
  const { s, a, t } = c;
  const r = s.amz.rename!;
  const busy = busyAt(c, `rename:${r.id}`);
  const err = errorAt(c, `rename:${r.id}`);
  const input = h("input", {
    class: "a-inp",
    id: "dlg-nm",
    "data-k": "dlg-ren",
    "data-autofocus": "",
    type: "text",
    dir: "auto",
    maxlength: 40,
    autocomplete: "off",
    spellcheck: "false",
    value: r.value,
    "aria-invalid": err ? "true" : false,
    "aria-describedby": err ? "dlg-err" : false,
    on: {
      input: (e) => a.amz.renameInput((e.target as HTMLInputElement).value),
      keydown: (e) => {
        const k = e as KeyboardEvent;
        if (k.key === "Enter" && !k.isComposing) {
          k.preventDefault();
          if (!busy) a.amz.renameSave();
        }
      },
    },
  });
  return frame(c, {
    label: t.rename,
    title: t.renameT,
    small: true,
    close: () => a.amz.renameCancel(),
    body: h("div", { class: "a-b" }, h("div", { class: "a-fld" }, h("label", { class: "a-lbl", for: "dlg-nm" }, t.awgName, h("span", null, t.dgNameMax)), input, err)),
    foot: foot(
      h("button", { class: "u-btn", type: "button", "data-k": "dlg-no", on: { click: () => a.amz.renameCancel() } }, t.awgCancel),
      h("button", { class: "u-btn pri", type: "button", "data-k": "dlg-yes", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => !busy && a.amz.renameSave() } }, busy && spin(), busy ? t.awgBusy : t.save),
    ),
  });
}

// ---- delete, replace the key ----

export const confirmHas = (c: Ctx): boolean => !!c.s.amz.confirm && !!who(c, c.s.amz.confirm.id);

export function confirmWindow(c: Ctx): Kid[] {
  const { d, a, t } = c;
  const q = c.s.amz.confirm!;
  const w = who(c, q.id)!;
  const rotate = q.kind === "rotate";
  const busy = busyAt(c, q.id);
  const err = errorAt(c, q.id);
  const about = w.link ? deviceAbout(w.link) : "";
  // a key: the board's words; a device of an app on the link: the same sentence by meaning (the key is the app's connection)
  const lead = rotate ? t.dgRotD(keyAppName(d, ((w.key?.platform || null) as Platform | null))) : w.key ? t.dgDelD : t.dgDelLinkD;
  const go = rotate
    ? h("button", { class: "u-btn pri", type: "button", "data-k": "dlg-yes", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => !busy && a.amz.rotate(q.id) } }, busy ? spin() : icon("refresh"), busy ? t.awgBusy : t.dgRotBtn)
    : h("button", { class: "u-btn bad", type: "button", "data-k": "dlg-yes", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => !busy && a.amz.remove(q.id) } }, busy ? spin() : icon("bin"), busy ? t.awgBusy : t.removeKey);
  const title = rotate ? t.dgRotT(w.name) : t.dgDelT(w.name);
  return frame(c, {
    label: rotate ? t.rotateKey : t.dgDelete,
    title: title,
    small: true,
    close: () => a.amz.ask(null),
    body: h("div", { class: "a-b" }, h("p", { class: "a-lead" }, lead), about && h("p", { class: "u-hint" }, about), err),
    // the safe answer takes the keyboard
    foot: foot(h("button", { class: "u-btn", type: "button", "data-k": "dlg-no", "data-autofocus": "", on: { click: () => a.amz.ask(null) } }, t.awgCancel), go),
  });
}

// ---- "new key needed" ----

export const staleHas = (c: Ctx): boolean => !!c.s.amz.stale && !!c.d.amnezia?.devices.some((x) => x.id === c.s.amz.stale);

export function staleWindow(c: Ctx): Kid[] {
  const { d, a, t } = c;
  const id = c.s.amz.stale!;
  const x = d.amnezia!.devices.find((y) => y.id === id)!;
  const busy = busyAt(c, `renew:${id}`);
  const err = errorAt(c, `renew:${id}`);
  const steps = t.dgStaleSteps(keyAppName(d, ((x.platform || null) as Platform | null)));
  return frame(c, {
    label: t.dgStaleT(label(x, t)),
    title: t.dgStaleT(label(x, t)),
    close: () => a.amz.stale(null),
    body: h(
      "div",
      { class: "a-b" },
      h("p", { class: "a-lead" }, x.stale_reason === "dns" ? t.dgStaleDnsD : x.stale_reason === "routes" ? t.dgStaleRuD : t.dgStaleD),
      h("ol", { class: "a-steps" }, ...steps.map((s, i) => h("li", { class: "a-st" }, h("span", { class: "a-n" }, String(i + 1)), h("div", { class: "tx" }, s)))),
      err,
    ),
    foot: foot(
      h("button", { class: "u-btn", type: "button", "data-k": "dlg-no", on: { click: () => a.amz.stale(null) } }, t.dgLater),
      h("button", { class: "u-btn pri", type: "button", "data-k": "dlg-yes", "data-autofocus": "", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => !busy && a.amz.renew(id) } }, busy ? spin() : icon("refresh"), busy ? t.awgBusy : t.newKey),
    ),
  });
}
