import "./sub-a.css";
import { keyBody, keyFoot, label, readyBody, readyFoot, shownKey } from "./amnezia";
import { appBody, appFoot, connBody, connFoot, linkApp } from "./connect";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { atDeviceLimit, keyAppName, keyAvailability, linkFull, ways } from "./logic";
import type { Ctx } from "./state";
import { wayBody } from "./ways";

// The window "New device" of the board (V2Add): one dialog, a step at a time. How the device connects (way), then either the
// link branch (the device and the app, connecting) or the key branch (the form, the key); "no free slot" instead of any step when
// there is none. "Show key" and "New key needed" open the key itself (ready) straight from the page's state. The page draws the
// whole dialog again on every change; deviceModalStage says when the step is a new one (the window plays its entrance then).

type Stage = { id: "way" | "app" | "conn" | "key" | "full" } | { id: "ready"; x: NonNullable<ReturnType<typeof shownKey>> };

/** There is no place left: the limit is reached and the link cannot take the device either, or the panel has just said so. */
function noPlace(c: Ctx): boolean {
  const { d, s } = c;
  if (d.user.device_limit <= 0) return false;
  if (s.amz.error === "device_limit" && s.amz.errorAt === "add") return true;
  const linkOk = ways(d).includes("link") && !linkFull(d);
  return atDeviceLimit(d) && !linkOk;
}

/** The step the state is on. A pane the person cannot go on (no key way, no app for the device) is the step of the way. */
function stage(c: Ctx): Stage {
  const { d, s } = c;
  const key = shownKey(c);
  if (key) return { id: "ready", x: key };
  if (noPlace(c)) return { id: "full" };
  const pane = s.amz.pane;
  const have = ways(d);
  if (pane === "key" && keyAvailability(d) === "ok") return { id: "key" };
  // connecting needs an app; without one (nothing for this device) the step of the app says so
  if ((pane === "conn" || pane === "link") && have.includes("link")) return { id: pane === "conn" && linkApp(c) ? "conn" : "app" };
  return { id: "way" };
}

/** "N of M": the place of the step on its branch (the way is the first step when there are two); none for what is not a step of one. */
function count(c: Ctx, st: Stage): string {
  const { d, t } = c;
  const both = ways(d).length > 1;
  const first = both ? 1 : 0;
  const at = st.id === "way" ? (both ? 1 : 0) : st.id === "app" || st.id === "key" ? first + 1 : st.id === "conn" || (st.id === "ready" && st.x.mode === "created") ? first + 2 : 0;
  return at > 0 ? t.adCount(at, first + 2) : "";
}

function title(c: Ctx, st: Stage): string {
  const { d, t } = c;
  switch (st.id) {
    case "way":
      return t.newDevT;
    case "app":
      return t.wayAppT;
    case "conn":
      return t.adConnT(linkApp(c)?.name ?? "");
    case "key":
      return t.adKeyT(keyAppName(d, null));
    case "full":
      return t.adFullT;
    case "ready":
      return (st.x.mode === "renew" ? t.adRenewFor : t.adKeyFor)(label(st.x.x, t));
  }
}

/** The accessible name of the window: its title. */
export const deviceModalLabel = (c: Ctx): string => title(c, stage(c));

/** The key of the step: it changes only when the window moves on (a step, or the key of another device), not when a choice is made inside one. */
export function deviceModalStage(c: Ctx): string {
  const st = stage(c);
  return st.id === "ready" ? `ready:${st.x.x.id}:${st.x.mode}` : st.id;
}

function fullStep(c: Ctx): { body: HTMLElement; foot: HTMLElement } {
  const { d, a, t, support } = c;
  const limit = d.user.device_limit;
  const n = Math.min(d.user.devices_used, limit);
  return {
    body: h(
      "div",
      { class: "a-b" },
      h("div", { class: "a-empty" }, h("p", { class: "u-fig" }, t.slots(n, limit), h("span", null, t.adFullOf)), h("div", { class: "u-prog", style: { width: "100%" }, role: "img", "aria-label": t.adSlotsAria(n, limit) }, h("i", { class: "full", style: { width: "100%" } }))),
      h("p", { class: "a-lead" }, t.adFullD(support !== "")),
    ),
    foot: h(
      "footer",
      { class: "a-f" },
      support && h("a", { class: "u-btn", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "add-full-write" }, icon("chat"), t.writeSupport),
      h(
        "button",
        {
          class: "u-btn pri",
          type: "button",
          "data-k": "add-full-devices",
          on: {
            click: () => {
              a.amz.add(false);
              a.tab("devices");
            },
          },
        },
        t.adOpenDevs,
      ),
    ),
  };
}

/** What the dialog holds: one <section class="a-dlg"> with the head ("Back", the title, "1 of 3", "Close"), the body of the step and its foot. */
export function deviceModal(c: Ctx): Kid[] {
  const { d, a, t } = c;
  const st = stage(c);
  const both = ways(d).length > 1;
  const q = (pane: "pick" | "link" | "key") => () => a.amz.pane(pane);
  let body: HTMLElement;
  let foot: HTMLElement | null = null;
  let back: (() => void) | undefined;
  switch (st.id) {
    case "way":
      body = wayBody(c);
      break;
    case "app":
      body = appBody(c);
      foot = appFoot(c);
      back = both ? q("pick") : undefined;
      break;
    case "conn": {
      body = connBody(c, linkApp(c)!);
      foot = connFoot(c);
      back = q("link");
      break;
    }
    case "key":
      body = keyBody(c);
      foot = keyFoot(c);
      back = both ? q("pick") : undefined;
      break;
    case "ready":
      body = readyBody(c, st.x.x);
      foot = readyFoot(c, st.x.mode);
      back = st.x.mode === "created" ? q("key") : undefined;
      break;
    case "full": {
      const f = fullStep(c);
      body = f.body;
      foot = f.foot;
      break;
    }
  }
  const name = title(c, st);
  return [
    h(
      "section",
      { class: `a-dlg${st.id === "key" ? " key" : ""}`, role: "dialog", "aria-modal": "true", "aria-label": name },
      h(
        "header",
        { class: "a-h" },
        back ? h("button", { class: "u-ic", type: "button", "data-k": "add-back", "aria-label": t.back, on: { click: back } }, icon("back")) : h("span", { class: "sp" }),
        h("h2", { class: "t", id: "dlg-t", tabindex: "-1", "data-autofocus": "" }, name),
        h("span", { class: "n" }, count(c, st)),
        h("button", { class: "u-ic", type: "button", "data-k": "modal-x", "aria-label": t.close, on: { click: () => a.amz.add(false) } }, icon("close")),
      ),
      body,
      foot,
    ),
  ];
}
