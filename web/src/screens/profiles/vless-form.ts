// What the VLESS form does that the generic schema form does not know: REALITY and TLS are two different sets of fields,
// transports belong to one of them, and a CDN host changes the XHTTP default. Pure functions over the settings document, so the
// rules can be tested without drawing the form.

import { getAt, setAt, type Field, type Settings } from "./schema";

const text = (s: Settings, path: string[]) => String(getAt(s, path) ?? "");
const isTLS = (s: Settings) => text(s, ["security"]) === "tls";
const cdnHost = (s: Settings) => text(s, ["cdn", "host"]).trim();

/**
 * The document after an edit, with what the new choice makes necessary: a transport the other security does not offer is
 * replaced, a certificate mode is chosen for TLS, and what only one security accepts is dropped when the other one is picked
 * (the server refuses a REALITY profile that still carries a CDN host). Behind a CDN, XHTTP starts as packet-up.
 */
export function vlessEdited(prev: Settings, next: Settings): Settings {
  let out = next;
  if (text(prev, ["security"]) !== text(next, ["security"])) {
    if (isTLS(next)) {
      if (text(out, ["transport"]) === "tcp") out = setAt(out, ["transport"], "xhttp");
      if (!text(out, ["tls_mode"])) out = setAt(out, ["tls_mode"], "acme_domain");
      if (text(out, ["xhttp", "host"]) !== "") out = setAt(out, ["xhttp", "host"], "");
    } else {
      if (text(out, ["transport"]) === "ws") out = setAt(out, ["transport"], "xhttp");
      for (const key of ["tls_mode", "sni", "cdn", "ws"]) out = { ...out, [key]: undefined };
    }
  }
  if (cdnHost(prev) === "" && cdnHost(next) !== "") {
    if (getAt(out, ["cdn", "port"]) === undefined) out = setAt(out, ["cdn", "port"], 443);
    const mode = text(out, ["xhttp", "mode"]);
    if (mode === "" || mode === "auto") out = setAt(out, ["xhttp", "mode"], "packet-up");
  }
  return out;
}

/** The fields of the form that mean nothing for these settings. `ownSite`: REALITY shows this node's own site. */
export function vlessHidden(fields: Field[], s: Settings): Set<string> {
  const tls = isTLS(s);
  const transport = text(s, ["transport"]);
  const cdn = cdnHost(s) !== "";
  const hidden = new Set<string>();
  for (const f of fields) {
    const [head] = f.path;
    if (head === "xhttp" && (transport !== "xhttp" || (tls && f.id === "xhttp.host"))) hidden.add(f.id);
    else if (head === "ws" && (!tls || transport !== "ws" || (cdn && f.id === "ws.host"))) hidden.add(f.id);
    else if (head === "reality" && tls) hidden.add(f.id);
    else if (!tls && (f.id === "tls_mode" || f.id === "sni" || head === "cdn")) hidden.add(f.id);
    else if (f.id === "cdn.port" && !cdn) hidden.add(f.id);
    else if (head === "reality" && text(s, ["reality", "target_kind"]) === "self" && (f.id === "reality.target" || f.id === "reality.server_names")) hidden.add(f.id);
  }
  return hidden;
}

/** Whether a choice is offered: TCP with Vision belongs to REALITY, WebSocket to TLS, a self-signed origin to a CDN. */
export function vlessOffers(f: Field, value: string, s: Settings): boolean {
  if (f.id === "transport") return value === "tcp" ? !isTLS(s) : value === "ws" ? isTLS(s) : true;
  if (f.id === "tls_mode" && value === "self_signed") return cdnHost(s) !== "" || text(s, ["tls_mode"]) === "self_signed";
  return true;
}
