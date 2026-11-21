import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import type { Tools } from "./state";

// Small parts the page's sections share. Text only ever goes in as text nodes.

export const dot = (cls = "") => h("i", { class: `dot${cls ? ` ${cls}` : ""}` });

/** The space after a sentence before the next one: none after a full-width stop (Chinese sets "。" solid). */
export const gap = (sentence: string) => (/[\u3000-\u303f\uff00-\uffef]$/.test(sentence) ? "" : " ");

/** Text with every «quoted» piece kept on one line (the buttons and menu names the person must find). */
export function rich(text: string): Kid[] {
  return text.split(/(«[^»]*»)/).filter(Boolean).map((p) => (p.startsWith("«") ? h("span", { class: "q" }, p) : p));
}

/** A note: neutral, "warn", "bad" (an error: role=alert) or "ok" (role=status), with an icon and its text. */
export function note(tone: "" | "warn" | "bad" | "ok", ic: IconName, body: Kid[] | Kid, extra: { cls?: string; id?: string; sm?: boolean } = {}): HTMLElement {
  const kids = Array.isArray(body) ? body : [body];
  return h(
    "div",
    { class: `note${tone ? ` ${tone}` : ""}${extra.sm === false ? "" : " sm"}${extra.cls ? ` ${extra.cls}` : ""}`, id: extra.id, role: tone === "bad" ? "alert" : tone === "ok" ? "status" : false },
    icon(ic, 16),
    h("p", null, ...kids),
  );
}

/** A tinted square with an icon. */
export const tile = (tone: string, ic: IconName, o: { size?: 36 | 40; iconSize?: number } = {}) =>
  h("span", { class: `tile${o.size === 36 ? " s36" : ""} ${tone}`, "aria-hidden": "true" }, icon(ic, o.iconSize ?? (o.size === 36 ? 18 : 20)));

/** A progress bar; `warn` paints it as a warning. */
export const bar = (pct: number, warn: boolean, label: string) =>
  h("div", { class: "bar", role: "img", "aria-label": label }, h("i", { class: warn ? "warn" : "", style: { width: `${Math.round(pct * 10) / 10}%` } }));

/** How long a control that did its work says so in place (the copy button's tick, a key window tile's done word), in ms. */
export const DONE_MS = 2000;

/**
 * The board's copy button (.u-cpb): the icon turns into a tick and the label rolls up into `done` for a moment (the CSS reads
 * data-cp="done"). `cls` is the look of the button (default the board's .u-btn; .u-ic for an icon alone, .a-lnk for the link in
 * a window). `toast` is the page's toast text; the page's copy() calls back only when the clipboard took the text, and puts
 * the toast above this button. `after` runs when it did. Without `label` the button is only the icon (give it `aria`).
 */
export function copyButton(a: Tools, o: { text: string; label?: string; done?: string; toast: string; cls?: string; key?: string; after?: () => void; aria?: string; live?: boolean; textCls?: string }): HTMLButtonElement {
  const tick = icon("check", 20);
  tick.classList.add("ck");
  const btn = h(
    "button",
    { class: `${o.cls ?? "u-btn"} u-cpb`, type: "button", "data-cp": "", "data-k": o.key, "aria-label": o.aria, "aria-live": o.live ? "polite" : false },
    h("span", { class: "u-cpi", "aria-hidden": "true" }, icon("copy", 20), tick),
    o.label !== undefined && h("span", { class: `u-cpt${o.textCls ? ` ${o.textCls}` : ""}` }, h("span", null, o.label), h("span", { "aria-hidden": "true" }, o.done ?? o.label)),
  );
  let timer = 0;
  btn.addEventListener("click", () =>
    a.copy(
      o.text,
      () => {
        o.after?.();
        btn.dataset.cp = "done";
        clearTimeout(timer);
        timer = window.setTimeout(() => (btn.dataset.cp = ""), DONE_MS);
      },
      o.toast,
      btn,
    ),
  );
  return btn;
}

/**
 * Where the page's toast stands: 56 px above the control that was pressed (the top of the page when there is none). An open
 * window is in the top layer, above everything in the body, so the toast of a control inside it lives in the window and is
 * measured from the screen (.u-toast.in).
 */
export function placeToast(toast: HTMLElement, from?: HTMLElement) {
  const win = from?.closest("dialog[open]") ?? null;
  const host = win ?? document.body;
  if (toast.parentElement !== host) host.append(toast);
  toast.classList.toggle("in", !!win);
  const y = from?.isConnected ? from.getBoundingClientRect().top + (win ? 0 : window.scrollY) - 56 : 16;
  toast.style.top = `${Math.max(16, Math.round(y))}px`;
}
