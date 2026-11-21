// What the MTProto form does that the generic schema form does not know: the site is asked for only when the Fake TLS name is
// "another site" (the node's own name needs nothing typed), as the REALITY target is for VLESS (vless-form.ts); and the mode Web
// (the proxy of Telegram Web) fixes the port to 443 and the name to the node's own, so those rows leave or stop being editable.

import { getAt, setAt, type Field, type Group, type Settings } from "./schema";

/** The Web Proxy of Telegram Web instead of the Fake TLS proxy of the apps. */
export function mtprotoWeb(s: Settings): boolean {
  return String(getAt(s, ["mode"]) ?? "") === "web";
}

/** Another site on TCP 443: the node's own site and the TLS-ALPN challenge are not served there, so its certificate renews over port 80. */
export function mtprotoOn443(s: Settings): boolean {
  return !mtprotoWeb(s) && String(getAt(s, ["fake_tls", "kind"]) ?? "") === "site" && Number(getAt(s, ["port"])) === 443;
}

/** The fields of the form that mean nothing for these settings: the whole Fake TLS name is the proxy of the apps'. */
export function mtprotoHidden(fields: Field[], s: Settings): Set<string> {
  const web = mtprotoWeb(s);
  const site = !web && String(getAt(s, ["fake_tls", "kind"]) ?? "") === "site";
  return new Set(fields.filter((f) => (f.id === "fake_tls.kind" && web) || (f.id === "fake_tls.domain" && !site)).map((f) => f.id));
}

/** The fields that show their value and cannot be edited: Web is https, which a browser reaches on 443 only. */
export function mtprotoLocked(s: Settings): Set<string> {
  return mtprotoWeb(s) ? new Set(["port"]) : new Set();
}

/** The document after an edit: Web takes the port 443 and the node's own name, which the panel requires of it. */
export function mtprotoEdited(prev: Settings, next: Settings): Settings {
  if (mtprotoWeb(prev) || !mtprotoWeb(next)) return next;
  return setAt(setAt(next, ["port"], 443), ["fake_tls", "kind"], "own");
}

/** The mode leads the first card ("basics"); the panel's schema leaves it without a group, which would put it in the fold of the advanced ones. */
export function mtprotoGroups(groups: Group[]): Group[] {
  const mode = groups.flatMap((g) => g.fields).find((f) => f.id === "mode");
  if (!mode || groups.some((g) => g.id === "basics" && g.fields.includes(mode))) return groups;
  const rest = groups.map((g) => ({ ...g, fields: g.fields.filter((f) => f !== mode) })).filter((g) => g.fields.length > 0 || g.id === "basics");
  const at = rest.findIndex((g) => g.id === "basics");
  if (at < 0) return [{ id: "basics", fields: [mode] }, ...rest];
  return rest.map((g, i) => (i === at ? { ...g, fields: [mode, ...g.fields] } : g));
}

/** The nodes where the profile has a port of its own that is not 443: in Web mode the inbound there would not be built. */
export function mtprotoWebOverrides(inbounds: readonly { nodeName: string; portOverride: number }[], s: Settings): { node: string; port: number }[] {
  if (!mtprotoWeb(s)) return [];
  return inbounds.filter((i) => i.portOverride !== 0 && i.portOverride !== 443).map((i) => ({ node: i.nodeName, port: i.portOverride }));
}

/** The nodes of those overrides as a sentence names them: "de1 (8443), nl1 (2053)". */
export const mtprotoNodesText = (list: readonly { node: string; port: number }[]): string => list.map((o) => `${o.node} (${o.port})`).join(", ");
