import { copyButton } from "./amnezia";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { serverName } from "./logic";
import { qrSvg } from "./qr";
import { flag } from "./servers";
import type { Ctx } from "./state";
import type { MgData } from "./types";

type TtLink = NonNullable<MgData["trust_tunnel"]>["links"][number];

/** The official page of the TrustTunnel apps (the clients in the project's README): where a person who has none gets one. */
export const ttAppsUrl = "https://github.com/TrustTunnel/TrustTunnel#clients";

/**
 * The TrustTunnel key of the person: one card per tt:// link, in the look of the Telegram ones: the country (and the owner's place)
 * with "IPv6" or "via RU" beside it, "Open in the app", Copy, and on a computer the QR code to point a phone at. Under the heading,
 * the steps in one line (install the app, scan the code or open the link) and where to get the app. A server with no link (its
 * certificate is self-signed) is a card that says so and offers nothing to open.
 */
export function trustTunnelSection(c: Ctx): Kid[] {
  const { d, t } = c;
  if (!d.trust_tunnel) return [];
  const links = d.trust_tunnel.links;
  const how = links.some((l) => l.url) ? [h("span", { class: "only-m" }, t.ttHowPhone), h("span", { class: "only-w" }, t.ttHowDesk)] : [t.ttS];
  return [
    h(
      "div",
      { class: "shead" },
      h("h2", { class: "h2" }, t.ttT),
      h("p", { class: "hint sec-sub" }, ...how, " ", h("a", { class: "tlink", href: ttAppsUrl, target: "_blank", rel: "noopener noreferrer", "data-k": "tt-get" }, t.ttGet, icon("external", 14))),
    ),
    links.length === 0
      ? h("section", { class: "card tg-empty", "aria-label": t.ttT }, h("p", { class: "hint" }, t.ttNoLinks))
      : h("div", { class: "srv-grid tg-grid" }, ...links.map((l, i) => card(c, l, i, links.filter((x) => x.url).length === 1))),
  ];
}

/** One card; `only`: the person's one link, whose "Open in the app" is the page's main action (with several, none leads). */
function card(c: Ctx, l: TtLink, i: number, only: boolean): HTMLElement {
  const { t, s, a } = c;
  const name = serverName(l, s.lang);
  const qr = l.url ? qrSvg(l.url, t.ttQr(name.country)) : null;
  return h(
    "article",
    { class: `card srv nodns tg tt${l.url ? "" : " off"}` },
    h(
      "div",
      { class: "srv-top" },
      flag(c, l),
      h("h3", { class: "srv-name" }, name.country, name.place && h("span", null, " · ", h("bdi", null, name.place))),
      l.has_ipv6 && h("span", { class: "chip sm sky" }, icon("globe", 13), "IPv6"),
      l.relay && h("span", { class: "chip sm lav", title: t.viaHint(l.via) }, icon("next", 13), t.chipVia(l.via)),
    ),
    l.url
      ? h(
          "div",
          { class: "tg-body" },
          qr && h("div", { class: "qr tg-qr tt-qr only-w" }, qr),
          h(
            "div",
            { class: "tg-acts" },
            h("a", { class: only ? "btn pri" : "btn sec", href: l.url, "data-k": `tt-open-${i}` }, icon("external", 18), t.ttOpen),
            copyButton(a, { text: l.url, label: t.ttCopy, done: t.copiedShort, toast: t.copied, cls: "tlink", key: `tt-copy-${i}` }),
          ),
        )
      : h("p", { class: "hint", "data-k": `tt-nolink-${i}` }, t.ttNoLink),
  );
}
