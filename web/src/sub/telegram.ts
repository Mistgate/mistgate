import { copyButton } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { safeUrl, serverName } from "./logic";
import { qrSvg } from "./qr";
import { flag } from "./servers";
import type { Ctx } from "./state";
import type { MgData } from "./types";

type TgLink = NonNullable<MgData["telegram"]>["links"][number];

/**
 * The Telegram proxy of the person: one card per tg://proxy link, in the look of the server cards: the country (and the
 * owner's place) with "IPv6" or "via RU" beside it on the twins of a server's link, "Open in Telegram", and on a computer the QR code to
 * point a phone at. One line under the heading says what happens: Telegram asks whether to connect the proxy.
 */
export function telegramSection(c: Ctx): Kid[] {
  const { d, t } = c;
  if (!d.telegram) return [];
  const links = d.telegram.links;
  const how =
    links.length > 0
      ? [h("span", { class: "only-m" }, t.telegramHowPhone), h("span", { class: "only-w" }, t.telegramHowDesk)]
      : [t.telegramS];
  return [
    h("div", { class: "shead" }, h("h2", { class: "h2" }, t.telegramT), h("p", { class: "hint sec-sub" }, ...how)),
    links.length === 0
      ? h("section", { class: "card tg-empty", "aria-label": t.telegramT }, h("p", { class: "hint" }, t.telegramNoLinks))
      : h("div", { class: "srv-grid tg-grid" }, ...links.map((l, i) => card(c, l, i, links.length === 1))),
  ];
}

/** One card; `only`: the person's one link, whose "Open in Telegram" is the page's main action (with several, none leads). */
function card(c: Ctx, l: TgLink, i: number, only: boolean): HTMLElement {
  const { t, s, a } = c;
  const name = serverName(l, s.lang);
  const url = safeUrl(l.url);
  const qr = url ? qrSvg(url, t.telegramQr(name.country)) : null;
  return h(
    "article",
    { class: "card srv nodns tg" },
    // the twin's mark rides in the heading row, so every card has the same rhythm with or without one
    h(
      "div",
      { class: "srv-top" },
      flag(c, l),
      h("h3", { class: "srv-name" }, name.country, name.place && h("span", null, ` · ${name.place}`)),
      l.ipv6 && h("span", { class: "chip sm sky" }, icon("globe", 13), "IPv6"),
      l.relay && h("span", { class: "chip sm lav", title: t.viaHint(l.via) }, icon("next", 13), t.chipVia(l.via)),
    ),
    h(
      "div",
      { class: "tg-body" },
      qr && h("div", { class: "qr tg-qr only-w" }, qr),
      h(
        "div",
        { class: "tg-acts" },
        url && h("a", { class: only ? "btn pri" : "btn sec", href: url, "data-k": `tg-open-${i}` }, icon("send", 18), t.telegramOpen),
        copyButton(a, { text: l.url, label: t.telegramCopy, done: t.copiedShort, toast: t.copied, cls: "tlink", key: `tg-copy-${i}` }),
      ),
    ),
  );
}
