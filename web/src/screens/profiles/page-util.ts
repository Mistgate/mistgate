import type { DeployProfile } from "./deploy";
import { getAt, parseSettings, type Settings } from "./schema";

/**
 * What "Put on nodes" must know to ask for a domain before the click, where Let's Encrypt cannot work on an IP: Hysteria2's
 * certificate mode and domain, or the node's own site behind VLESS REALITY, which always has a Let's Encrypt certificate.
 */
export function certOfSettings(protocol: string, settings: Settings): Pick<DeployProfile, "tlsMode" | "sni"> {
  if (protocol === "vless") {
    if (getAt(settings, ["security"]) === "tls") return { tlsMode: String(getAt(settings, ["tls_mode"]) ?? ""), sni: String(getAt(settings, ["sni"]) ?? "") };
    return { tlsMode: getAt(settings, ["reality", "target_kind"]) === "self" ? "acme_domain" : "", sni: "" };
  }
  // MTProto: the node's own name in the Fake TLS handshake needs a host name, as the node's own site behind REALITY does
  if (protocol === "mtproto") return { tlsMode: getAt(settings, ["fake_tls", "kind"]) === "site" ? "" : "acme_domain", sni: "" };
  return { tlsMode: String(getAt(settings, ["tls_mode"]) ?? ""), sni: String(getAt(settings, ["sni"]) ?? "") };
}

/** The profile's own exit as the saved settings say it, or "" for a protocol that has none (the schema has no `egress`). */
export function egressOf(settingsJson: string, hasEgress: boolean): "" | "direct" | "warp" {
  if (!hasEgress) return "";
  return getAt(parseSettings(settingsJson), ["egress"]) === "warp" ? "warp" : "direct";
}
