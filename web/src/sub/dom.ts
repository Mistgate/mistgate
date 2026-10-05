// A tiny element builder. Text only ever goes in as text nodes (never innerHTML), so nothing the server or an
// admin typed can become markup.

export type Kid = Node | string | false | null | undefined;
export type Props = {
  class?: string;
  on?: Record<string, (e: Event) => void>;
  style?: Record<string, string>;
  [attr: string]: unknown;
};

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Props | null,
  ...kids: Kid[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(props ?? {})) {
    // aria-pressed / aria-checked / aria-expanded mean something as "false": the state is written, not left out
    if (typeof v === "boolean" && /^aria-(pressed|checked|expanded|selected|busy)$/.test(k)) {
      el.setAttribute(k, String(v));
      continue;
    }
    if (v === false || v == null) continue;
    if (k === "class") el.className = String(v);
    else if (k === "on") for (const [ev, fn] of Object.entries(v as Record<string, (e: Event) => void>)) el.addEventListener(ev, fn);
    else if (k === "style") for (const [p, val] of Object.entries(v as Record<string, string>)) el.style.setProperty(p, val);
    else el.setAttribute(k, v === true ? "" : String(v));
  }
  el.append(...kids.filter((x): x is Node | string => x !== false && x != null));
  return el;
}
