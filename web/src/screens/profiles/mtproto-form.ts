// What the MTProto form does that the generic schema form does not know: the site is asked for only when the Fake TLS name is
// "another site" (the node's own name needs nothing typed), as the REALITY target is for VLESS (vless-form.ts).

import { getAt, type Field, type Settings } from "./schema";

/** Another site on TCP 443: the node's own site and the TLS-ALPN challenge are not served there, so its certificate renews over port 80. */
export function mtprotoOn443(s: Settings): boolean {
  return String(getAt(s, ["fake_tls", "kind"]) ?? "") === "site" && Number(getAt(s, ["port"])) === 443;
}

/** The fields of the form that mean nothing for these settings. */
export function mtprotoHidden(fields: Field[], s: Settings): Set<string> {
  const site = String(getAt(s, ["fake_tls", "kind"]) ?? "") === "site";
  return new Set(fields.filter((f) => f.id === "fake_tls.domain" && !site).map((f) => f.id));
}
