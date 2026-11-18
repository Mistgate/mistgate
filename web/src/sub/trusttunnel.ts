import type { Kid } from "./dom";
import { h } from "./dom";
import { icon } from "./icons";
import type { Ctx } from "./state";
import { proxyPanel } from "./telegram";

/** The official page of the TrustTunnel apps (the clients in the project's README): where a person who has none gets one. */
export const ttAppsUrl = "https://github.com/TrustTunnel/TrustTunnel#clients";

/**
 * The TrustTunnel key of the person: the panel of the Telegram proxies (telegram.ts) with its own words. One row per tt:// link
 * (a computer: the list of the servers, the chosen one's QR code and "Open in the app"; a phone: a row each), the steps in one
 * line (install the app, scan the code or open the link) and where to get the app. A server with no link (its certificate is
 * self-signed) says so and offers nothing to open.
 */
export function trustTunnelSection(c: Ctx): Kid[] {
  const { d, t } = c;
  const getApp = (key: string) => h("a", { class: "a-lnk", href: ttAppsUrl, target: "_blank", rel: "noopener noreferrer", "data-k": key }, t.ttGet, icon("external", 14));
  if (!d.trust_tunnel) return [];
  return proxyPanel(c, {
    key: "tt",
    title: t.ttT,
    empty: t.ttNoLinks,
    links: d.trust_tunnel.links.map((l) => ({ ...l, ipv6: l.has_ipv6 })),
    cap: t.ttCap,
    pane: t.ttPane,
    hint: [t.ttHowDesk, " ", getApp("tt-get")],
    hintPhone: [t.ttHowPhone, " ", getApp("tt-get-m")],
    open: t.ttOpen,
    copy: t.ttCopy,
    icon: "external",
    qrAria: t.ttQr,
    copyAria: t.ttCopyAria,
    noLink: t.ttNoLink,
    href: (u) => u,
  });
}
