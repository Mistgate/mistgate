import { qrLogoSrc } from "./brand";
import { h, type Kid } from "./dom";
import { tilt } from "./fx";
import { icon } from "./icons";
import { fetchedUnix, fmtAgo, fmtDate, hasLinkApp, hero, safeUrl } from "./logic";
import { qrCard } from "./qr";
import type { Ctx } from "./state";
import type { MgData } from "./types";
import { copyButton } from "./ui";

// The top of the page after the boards: the owner's announcement, the subscription card (the state, the name, three figures,
// and the card of a new device), and the note under it that says what to do (a first visit, a term that ends soon, traffic used
// up, a term that ended, a subscription that is off).

/** There is something to connect with: a link or a key. */
export const connectNeeded = (d: MgData) => d.access.happ || (d.access.amnezia && d.amnezia !== null) || d.apps.length > 0;

/** The QR code of this page (the link) can be shown. */
export const qrOk = (d: MgData) => d.options.show_qr && d.subscription_url !== "" && hasLinkApp(d);

type Tone = "" | "warn" | "off";
type Fig = { v: string; n: string; tone: Tone };

/** The three figures of the card: the term, the traffic, the places of the devices. */
export function figures(c: Pick<Ctx, "d" | "s" | "t">, now = Date.now() / 1000): { term: Fig; traffic: Fig; devices: Fig } {
  const { d, s, t } = c;
  const u = d.user;
  const hr = hero(d, s.lang, now);
  const noExpiry = !u.expires_unix;
  const date = noExpiry ? "" : fmtDate(u.expires_unix, s.lang, now);

  let term: Fig;
  if (u.status === "expired") term = { v: t.chip.expired, n: date, tone: "off" };
  else if (u.status === "disabled") term = { v: t.chip.disabled, n: t.byOwner, tone: "off" };
  else if (noExpiry) term = { v: t.noTerm, n: t.noTermSub, tone: "" };
  else term = { v: `${hr.days} ${t.dayWord(hr.days)}`, n: t.until(date), tone: hr.soon ? "warn" : "" };

  const quota = hr.quotaN ? t.ofQ(hr.quotaN) : t.unlimited;
  const extra = u.status === "limited" ? t.limitSpent : u.status === "active" ? hr.reset : "";
  const traffic: Fig = { v: hr.usedQ, n: [quota, extra].filter(Boolean).join(t.listSep), tone: hr.low ? "warn" : "" };

  const free = Math.max(0, u.device_limit - u.devices_used);
  const devices: Fig = { v: t.devUsed(u.devices_used, u.device_limit), n: u.device_limit > 0 ? (free > 0 ? t.devFree(free) : t.devNoFree) : t.devNoLimit, tone: "" };
  return { term, traffic, devices };
}

// a figure that is written in letter by letter on a computer (fx.ts); the text is there for everything else
const fig = (key: string, text: string, cls = "v") => h("span", { class: cls, "data-scr": text, "data-scr-k": key, "aria-label": text }, text);

/** The announcement of the owner (the board's .u-ann: a tile, the text, the cross). The first line is the title, the rest the line under it. */
export function announcement(c: Ctx): HTMLElement | null {
  const { d, s, a, t } = c;
  // the board shows it above a subscription that works only (.u-ann is not drawn in the states without tabs)
  if (d.user.status !== "active" || !d.options.show_announcement || !d.announcement || s.annClosed) return null;
  const [first = "", ...rest] = d.announcement.split("\n");
  const more = rest.join("\n").trim();
  return h(
    "section",
    { class: "u-ann", role: "status", "aria-label": t.annAria },
    h("span", { class: "tl", "aria-hidden": "true" }, icon("megaphone", 20)),
    h("div", { class: "tx" }, h("p", { class: "t" }, h("b", null, first)), more && h("p", { class: "m" }, more)),
    h("button", { class: "u-ic", type: "button", "data-k": "ann-x", "aria-label": t.hideAnn, on: { click: () => a.closeAnn() } }, icon("close", 16)),
  );
}

function statusLine(c: Ctx, now: number): { tone: Tone; st: string; sy: string } {
  const { d, s, t } = c;
  const u = d.user;
  const hr = hero(d, s.lang, now);
  const got = fetchedUnix(d);
  switch (u.status) {
    case "limited":
      return { tone: "warn", st: t.chip.limited, sy: u.quota_reset !== "none" && u.next_reset_unix ? t.syResets(fmtDate(u.next_reset_unix, s.lang, now)) : "" };
    case "expired":
      return { tone: "off", st: t.chip.expired, sy: u.expires_unix ? fmtDate(u.expires_unix, s.lang, now) : "" };
    case "disabled":
      return { tone: "off", st: t.chip.disabled, sy: t.syOff };
    default:
      return { tone: hr.soon ? "warn" : "", st: hr.soon ? t.endsIn(hr.days) : t.chip.active, sy: got ? t.syGot(fmtAgo(got, s.lang, now)) : t.syNever };
  }
}

/** The subscription card: the state, the name, the three figures; beside them (a subscription that works) the new device: the QR code and two buttons. */
export function heroCard(c: Ctx, now = Date.now() / 1000): HTMLElement {
  const { d, a, t } = c;
  const live = d.user.status === "active";
  const f = figures(c, now);
  const st = statusLine(c, now);
  const name = d.user.name.trim();
  const canAdd = live && connectNeeded(d);
  const copyAble = live && d.subscription_url !== "";
  const cell = (key: string, label: string, x: Fig, button?: () => void) => {
    const kids: Kid[] = [h("span", { class: "k" }, label), fig(key, x.v, `v${x.tone ? ` ${x.tone}` : ""}`), h("span", { class: "n" }, x.n)];
    return button ? h("button", { class: "u-stat", type: "button", "data-k": "stat-devices", on: { click: button } }, ...kids) : h("div", { class: "u-stat" }, ...kids);
  };
  const qr = live && qrOk(d) ? qrCard(d.qr, t.qrAlt, qrLogoSrc(d)) : null;
  if (qr) {
    qr.classList.add("u-tilt");
    qr.prepend(h("i", { class: "u-glare", "aria-hidden": "true" }));
    qr.addEventListener("mousemove", tilt.move);
    qr.addEventListener("mouseleave", tilt.leave);
  }
  const add = (cls = "u-btn pri") => h("button", { class: cls, type: "button", "data-k": "hero-add", on: { click: () => a.amz.add(true) } }, icon("plus"), t.awgAdd);
  // on a phone the board puts "Link" next to "Add device" in a row that is exactly full: the tick (and the toast) say it is copied, the word stays the same, so the button keeps its width and does not drop to a row of its own
  const copy = (label: string, cls = "u-btn", done = t.copied) => copyButton(a, { text: d.subscription_url, label, done, toast: t.copied, cls, key: "hero-copy", after: a.mark });
  const aside = canAdd || copyAble;
  return h(
    "section",
    { class: `u-card u-hero${aside ? "" : " solo"}`, "aria-label": t.statusAria },
    h(
      "div",
      { class: "u-hero-m" },
      h(
        "div",
        null,
        h("p", { class: "u-status" }, h("span", { class: `u-dot${st.tone ? ` ${st.tone}` : ""}` }), h("b", null, st.st), st.sy && h("span", { class: "sep" }, "·"), st.sy && h("span", { class: "sy" }, st.sy)),
        h("h1", { class: "u-h1" }, name ? t.subOf(name) : t.subAnon),
      ),
      h("div", { class: "u-stats" }, cell("term", t.termL, f.term), cell("traffic", t.trafficL, f.traffic), cell("devices", t.statDevices, f.devices, live ? () => a.tab("devices") : undefined)),
    ),
    aside && h("aside", { class: "u-hero-c only-w", "aria-label": t.newDevT }, qr && h("div", { class: "u-qrrow" }, qr, h("div", null, h("p", { class: "u-t" }, t.newDevT), h("p", { class: "u-hint" }, t.newDevH))), h("div", { class: "u-acts col" }, canAdd && add(), copyAble && copy(t.copySubLink))),
    aside && h("div", { class: "u-hero-c only-m" }, h("div", { class: "u-acts" }, canAdd && add("u-btn pri grow"), copyAble && copy(t.copySubShort, canAdd ? "u-btn" : "u-btn grow", t.copySubShort))),
  );
}

/** The note under the card: what to do now (a first visit, a term that ends soon, traffic used up, a term that ended, a subscription that is off). */
export function alertCard(c: Ctx, now = Date.now() / 1000): HTMLElement | null {
  const { d, s, t, support } = c;
  const u = d.user;
  const hr = hero(d, s.lang, now);
  const call = support !== "";
  let o: { tone: Tone; t: string; d: string } | null = null;
  if (u.status === "expired") o = { tone: "", t: t.alExpT(u.expires_unix ? fmtDate(u.expires_unix, s.lang, now) : ""), d: t.alExpD };
  else if (u.status === "disabled") o = { tone: "", t: t.alOffT, d: t.alOffD(call) };
  else if (u.status === "limited") o = { tone: "warn", t: t.alLimT, d: t.alLimD(u.quota_reset !== "none" && u.next_reset_unix ? fmtDate(u.next_reset_unix, s.lang, now) : "", call) };
  else if (hr.soon) o = { tone: "warn", t: t.alSoonT(fmtDate(u.expires_unix, s.lang, now)), d: t.alSoonD(call) };
  else if (!s.returning && connectNeeded(d)) o = { tone: "", t: t.alFirstT, d: t.alFirstD };
  if (!o) return null;
  const button = (u.status !== "active" || hr.soon) && call && safeUrl(support) !== "";
  return h(
    "section",
    { class: `u-card u-alert${o.tone ? ` ${o.tone}` : ""}`, role: "status" },
    h("div", { class: "tx" }, h("b", null, o.t), h("span", null, o.d)),
    button && h("a", { class: "u-btn pri", href: support, target: "_blank", rel: "noopener noreferrer", "data-k": "alert-write" }, t.writeSupport),
  );
}
