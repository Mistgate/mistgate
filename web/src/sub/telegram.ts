import { h, type Kid } from "./dom";
import { icon, type IconName } from "./icons";
import { qrLogoSrc } from "./brand";
import { safeUrl, serverName } from "./logic";
import { qrCard } from "./qr";
import { flag } from "./servers";
import type { Ctx } from "./state";
import type { Qr } from "./types";
import { copyButton } from "./ui";

// The Telegram proxy of the person, as the board draws it. A computer: the list of the proxies' countries on the left (one is
// chosen) and the pane of the chosen one on the right: the QR code, the name, one line of what happens, "Open in Telegram" and
// "Copy the link". A phone: one line of what happens and a row for each proxy with "Open" and a copy button. Only the list of a
// person with one proxy is left out (nothing to choose). TrustTunnel is the same panel with its own words (trusttunnel.ts).

/** What one proxy link of either kind is: the server as the page names it, its marks, the link ("" = none yet). */
export type ProxyLink = { label: string; country_code: string; place: string; ipv6: boolean; web?: boolean; relay: boolean; via: string; url: string; qr: Qr | null };

export type ProxySpec = {
  /** The prefix of every data-k ("tg", "tt"). */
  key: string;
  title: string;
  empty: string;
  links: ProxyLink[];
  cap: string;
  pane: string;
  /** What is said in the pane (a computer): kids, so the TrustTunnel one can end in a link. */
  hint: Kid[];
  hintPhone: Kid[];
  open: string;
  copy: string;
  icon: IconName;
  /** The words that name one proxy's picture and copy button. */
  qrAria: (country: string) => string;
  copyAria: (country: string) => string;
  /** The link as it may stand in an href (the page's own check of the scheme); "" when it is none. */
  href: (url: string) => string;
  /** A server that has no link yet (TrustTunnel with a self-signed certificate). */
  noLink: string;
};

// which proxy of the panel is chosen (a computer): kept while the page is open, it is not the page's state, so a drawing of the page keeps it
const chosen: Record<string, number> = {};

/** The dev server's `&proxy=<n>`: the n-th proxy of the panel ("tg", "tt") is the chosen one. */
export const pickProxy = (key: "tg" | "tt", n: number) => void (chosen[key] = n);

/** The marks of a proxy: IPv6, a twin through a node of another country, and the Web Proxy of Telegram Web (tg://webproxy). */
const marks = (c: Ctx, l: ProxyLink): HTMLElement[] => [l.web && h("span", { class: "u-tag", title: c.t.tgWebHint }, c.t.tgWeb), l.ipv6 && h("span", { class: "u-tag dir" }, "IPv6"), l.relay && h("span", { class: "u-tag ru", title: c.t.viaHint(l.via) }, c.t.chipVia(l.via))].filter((x): x is HTMLElement => !!x);

const nameOf = (c: Ctx, l: ProxyLink) => serverName(l, c.s.lang);

/** The panel: the empty note, or the computer's pane (and picker) with the phone's list beside it. */
export function proxyPanel(c: Ctx, o: ProxySpec): Kid[] {
  const { d, t, a } = c;
  const links = o.links;
  if (links.length === 0) return [h("section", { class: "u-card", "aria-label": o.title }, h("p", { class: "u-empty" }, o.empty))];
  const at = () => Math.min(Math.max(chosen[o.key] ?? 0, 0), links.length - 1);

  const pane = (l: ProxyLink, i: number) => {
    const n = nameOf(c, l);
    const url = o.href(l.url);
    const qr = url ? qrCard(l.qr, o.qrAria(n.country), qrLogoSrc(d), { big: true }) : null;
    return h(
      "section",
      { class: `u-card u-pane${qr ? "" : " noqr"}`, "aria-label": o.pane },
      qr,
      h(
        "div",
        { class: "u-pane-b" },
        h(
          "div",
          null,
          h("h3", { class: "u-h2" }, flag(c, l, true), h("span", null, h("bdi", null, n.country), n.place && h("small", null, h("bdi", null, n.place))), ...marks(c, l)),
          h("p", { class: "u-hint", style: { "margin-top": "8px" } }, ...(url ? o.hint : [o.noLink])),
        ),
        url &&
          h(
            "div",
            { class: "u-acts col" },
            h("a", { class: "u-btn pri", href: url, "data-k": `${o.key}-open-${i}` }, icon(o.icon), o.open),
            copyButton(a, { text: l.url, label: o.copy, done: t.copied, toast: t.copied, key: `${o.key}-copy-${i}` }),
          ),
      ),
    );
  };

  const picker = () =>
    h(
      "section",
      { class: "u-card u-list", "aria-label": o.cap },
      h("p", { class: "u-cap" }, o.cap),
      ...links.map((l, i) => {
        const on = i === at();
        const n = nameOf(c, l);
        const ck = icon("check");
        ck.classList.add("ck");
        return h(
          "button",
          {
            class: `u-pick${on ? " on" : ""}`,
            type: "button",
            "aria-pressed": on,
            "data-k": `${o.key}-pick-${i}`,
            on: {
              click: () => {
                chosen[o.key] = i;
                draw();
                desk.querySelector<HTMLElement>(`[data-k="${o.key}-pick-${i}"]`)?.focus({ preventScroll: true });
              },
            },
          },
          flag(c, l),
          h("span", null, h("bdi", null, n.country), n.place && h("small", null, h("bdi", null, n.place))),
          ...marks(c, l),
          ck,
        );
      }),
    );

  const desk = h("div", { class: `u-tg only-w${links.length === 1 ? " one" : ""}` });
  const draw = () => desk.replaceChildren(...(links.length > 1 ? [picker()] : []), pane(links[at()]!, at()));
  draw();

  const row = (l: ProxyLink, i: number) => {
    const n = nameOf(c, l);
    const url = o.href(l.url);
    return h(
      "div",
      { class: `u-prow${url ? "" : " nolink"}` },
      h("span", { class: "grow" }, flag(c, l), h("span", null, h("bdi", null, n.country), n.place && h("small", null, h("bdi", null, n.place))), ...marks(c, l)),
      url && h("a", { class: "u-btn", href: url, "data-k": `${o.key}-open-m-${i}` }, icon(o.icon), t.openShort),
      url && copyButton(a, { text: l.url, cls: "u-ic", aria: o.copyAria(n.country), toast: t.copied, key: `${o.key}-copy-m-${i}` }),
      !url && h("p", { class: "u-hint" }, o.noLink),
    );
  };

  return [desk, h("p", { class: "u-hint only-m", style: { padding: "4px 4px 0" } }, ...o.hintPhone), h("section", { class: "u-card u-list only-m", "aria-label": o.title }, ...links.map(row))];
}

/** The Telegram proxies: one per tg://proxy link. */
export function telegramSection(c: Ctx): Kid[] {
  const { d, t } = c;
  if (!d.telegram) return [];
  return proxyPanel(c, {
    key: "tg",
    title: t.telegramT,
    empty: t.telegramNoLinks,
    links: d.telegram.links,
    cap: t.tgCap,
    pane: t.tgPane,
    hint: [t.tgHintDesk],
    hintPhone: [t.tgHintPhone],
    open: t.telegramOpen,
    copy: t.telegramCopy,
    icon: "send",
    qrAria: t.telegramQr,
    copyAria: t.tgCopyAria,
    noLink: t.telegramNoLinks,
    href: (u) => (u ? safeUrl(u) : ""),
  });
}
