import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";

// Small parts the page's sections share. Text only ever goes in as text nodes.

export const dot = (cls = "") => h("i", { class: `dot${cls ? ` ${cls}` : ""}` });

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
