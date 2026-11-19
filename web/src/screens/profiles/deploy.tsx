import { ConnectError } from "@connectrpc/connect";
import { codedError } from "@/lib/coded-error";
import { errorCode, errorVars, shareRefusalText } from "@/lib/errors";
import { lossyVars } from "@/lib/port-check";
import { isShareCode, shareFreePort, shareOffer, type ShareCode, type ShareOffer } from "@/lib/tcpshare";
import type { Tx } from "@/screens/users/t";

// Why a node refused a profile (CreateInbound's coded errors), sorted by what the window that asked can offer.

/** Why a node refused the profile, sorted by what the window can offer: a port field, a domain field, or just the reason. */
export type Refusal =
  | { kind: "port"; text: string; free?: string; /** port_lossy: the port that loses packets, which "put on anyway" keeps */ lossy?: string }
  | { kind: "domain"; text: string }
  /** A TCP port the node shares by name (sni_taken, share_needs_site, share_unsupported, share_agent_old): `offer` when the profile can stand beside the other under a name of its own. */
  | { kind: "share"; text: string; code: ShareCode; offer: ShareOffer | null; free?: string }
  | { kind: "already"; text: string }
  | { kind: "other"; text: string };

export const isIp = (address: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(address) || address.includes(":");

/**
 * Reads a CreateInbound refusal. The coded forms ("port_taken: port=443&profile=Main&free=8443", "acme_needs_domain:
 * address=…", "already_on_node") come first; the panel's older English sentences of the same refusals are recognised too.
 * Anything else reads as errorText does.
 */
export function refusalOf(e: unknown, t: Tx, node: { name: string; address: string }, protocol?: string): Refusal {
  const msg = ConnectError.from(e).rawMessage;
  const code = errorCode(msg);
  const v = errorVars(msg);
  if (code === "port_lossy") {
    // the UDP delivery check found the port losing packets on this node; free is a port the same run proved
    const free = v.free && v.free !== "0" ? v.free : undefined;
    const text = [t("err.port_lossy", lossyVars(t, { ...v, node: v.node || node.name })), free ? t("ports.deploy.free", { free }) : ""].filter(Boolean).join(" ");
    return { kind: "port", free, lossy: v.port || "", text };
  }
  const old = /UDP port (\d+) is already used by profile "(.*)" on this node/.exec(msg);
  if (code === "port_taken" || old) {
    const port = v.port ?? old?.[1] ?? "";
    const profile = v.profile ?? old?.[2] ?? "";
    const free = v.free || undefined;
    // TrustTunnel holds TCP and, with HTTP/3, the UDP port of the same number, so it says which one may be the clash
    const key = protocol === "trusttunnel" ? (free ? "where.portTakenTtFree" : "where.portTakenTt") : free ? "where.portTakenFree" : "where.portTaken";
    return { kind: "port", free, text: t(key, { port, node: node.name, profile, free: free ?? "" }) };
  }
  if (isShareCode(code)) {
    const free = shareFreePort(v);
    return { kind: "share", code, offer: shareOffer(code, v), free: free > 0 ? String(free) : undefined, text: shareRefusalText(t, code, { node: node.name, ...v }) };
  }
  if (code === "acme_needs_domain" || /host name is required for a Let's Encrypt certificate/.test(msg)) {
    return { kind: "domain", text: t("where.domainNeed", { node: node.name, address: v.address || node.address }) };
  }
  // the node's own site behind VLESS REALITY is Let's Encrypt too, and its other way out is another site, not a self-signed certificate
  if (code === "reality_needs_domain") return { kind: "domain", text: t("err.reality_needs_domain", { node: v.node || node.name, address: v.address || node.address }) };
  if (code === "mtproto_needs_domain") return { kind: "domain", text: t("err.mtproto_needs_domain", { node: v.node || node.name, address: v.address || node.address }) };
  if (code === "already_on_node" || /already deployed on the node/.test(msg)) return { kind: "already", text: t("where.already") };
  if (/port override lies inside the hop range/.test(msg)) return { kind: "port", text: t("where.portPrompt", { node: node.name }) };
  return { kind: "other", text: codedError(e, t, "awg.err") };
}

export type DeployProfile = {
  id: string;
  name: string;
  protocol: string;
  /** The certificate settings the window checks before the click (Hysteria2: tls_mode, sni). */
  tlsMode?: string;
  sni?: string;
};
