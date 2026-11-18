import { h, type Kid } from "./dom";
import { icon } from "./icons";
import type { Ctx } from "./state";

// What every small window shares (the boards' .a-dlg): the frame with its head (a space, the title, "Close"), a spinner, an error line.

/** One <section class="a-dlg"> with the head and the body (and the foot, when there is one). `small`: the board's .sm (440 px). `close` is what Esc does too. */
export function frame(c: Ctx, o: { label: string; title: Kid; small?: boolean; close: () => void; body: Kid; foot?: Kid }): Kid[] {
  return [
    h(
      "section",
      { class: `a-dlg${o.small ? " sm" : ""}`, role: "dialog", "aria-modal": "true", "aria-label": o.label },
      h(
        "header",
        { class: "a-h" },
        h("span", { class: "sp" }),
        h("h2", { class: "t", id: "dlg-t" }, o.title),
        h("button", { class: "u-ic", type: "button", "data-k": "modal-x", "aria-label": c.t.close, on: { click: o.close } }, icon("close")),
      ),
      o.body,
      o.foot,
    ),
  ];
}

export const spin = () => h("span", { class: "spin", "aria-hidden": "true" });

/** The board's error line (.a-err): a warning and the words, announced when it appears. */
export const errLine = (text: string, id?: string) => h("p", { class: "a-err", id, role: "alert" }, icon("warn"), text);

/** The foot of a window (.a-f). */
export const foot = (...kids: Kid[]) => h("footer", { class: "a-f" }, ...kids);
