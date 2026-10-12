import type { T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { agoOf } from "./format";

// The UDP delivery check (design/udp-port-check.md) in words: the percentage a verdict stands for, where the packets came
// from, how long ago, and why a check could not run. Shared by the node's "UDP ports" block, the refusals and warnings of
// CreateInbound / UpdateInbound / UpdateProfile, the twin and the events list.

/** Percent of the packets that did not arrive (0 when nothing was sent). */
export const lossPct = (sent: number, got: number) => (sent > 0 ? Math.max(0, Math.min(100, Math.round((1 - got / sent) * 100))) : 0);

/** "the panel" for the panel host, else the name of the node that sent; "" when unknown. */
export const senderLabel = (t: T, sender: string) => (sender === "panel" ? t("ports.senderPanel") : sender);

/**
 * The values of a port_lossy refusal, warning or event ({port, node, sent, got, at, sender}) plus the words the sentences
 * need: {lost} (percent), {sender} ("the panel" or a node name) and {when} ("5 min ago").
 */
export function lossyVars(t: T, v: Record<string, string | undefined>, nowMs = Date.now()): Record<string, string | number> {
  const at = Number(v.at);
  return {
    ...Object.fromEntries(Object.entries(v).map(([k, x]) => [k, x ?? ""])),
    lost: lossPct(Number(v.sent), Number(v.got)),
    sender: senderLabel(t, v.sender ?? ""),
    when: at > 0 ? agoOf(t, at, Math.floor(nowMs / 1000)) : t("ports.whenUnknown"),
  };
}

export const reasons = ["node_offline", "agent_too_old", "no_sender", "busy", "inconclusive", "same_host", "no_route", "failed"] as const;
export type PortReason = (typeof reasons)[number];

const known = (reason: string): PortReason => (reasons as readonly string[]).includes(reason) ? (reason as PortReason) : "failed";

/** The one-line explanation of a reason; a reason the page does not know reads as "failed". */
export function reasonText(t: T, reason: string, vars: { best?: number } = {}): string {
  return t(`ports.why.${known(reason)}` as MessageKey, { best: vars.best ?? 0 });
}

/** The reason in a few words, for a badge ("no sender"). */
export const reasonShort = (t: T, reason: string): string => t(`ports.short.${known(reason)}` as MessageKey);

/** The best share of delivered packets (percent) among the ports of a run: how inconclusive it was. */
export const bestDelivered = (ports: readonly { sent: number; got: number }[]) => Math.max(0, ...ports.map((p) => (p.sent > 0 ? Math.round((p.got / p.sent) * 100) : 0)));

/** "Saved" and what the check had to say about the port, as one toast: "Saved. Port 8443 on de1 was not checked …". */
export const withNotes = (base: string, notes: readonly string[]) => (notes.length > 0 ? `${base}. ${notes.join(" ")}` : base);

type Warning = { code: string; params: Record<string, string> };

/**
 * hy2_decoy_paused in words ({node, port, hy2, vless, via, target, port80}): who takes the TCP port and what becomes of the
 * Hysteria2 site (by `via`: self, tls, site), then what that means for its certificate (by `port80`, when sent).
 */
export function decoyNote(t: T, p: Record<string, string | undefined>): string {
  const vars = Object.fromEntries(Object.entries(p).map(([k, v]) => [k, v ?? ""]));
  const via = p.via === "self" || p.via === "tls" ? p.via : "site";
  const port80 = p.port80 === "open" || p.port80 === "closed" || p.port80 === "unchecked" ? t(`node.check.decoy.port80.${p.port80}`, vars) : "";
  return [t(`node.check.decoy.${via}`, vars), port80].filter(Boolean).join(" ");
}

/**
 * The sentences a saved inbound owes the admin about its port: the check could not run (port_unchecked), the port loses
 * packets and was saved anyway or did not change (port_lossy), or a VLESS profile took the Hysteria2 site's TCP port
 * (hy2_decoy_paused). Empty when there is nothing to say.
 */
export function portNotes(t: T, warnings: readonly Warning[] | undefined): string[] {
  const out: string[] = [];
  for (const w of warnings ?? []) {
    if (w.code === "port_unchecked") out.push(t("ports.unchecked.saved", { port: w.params.port ?? "", node: w.params.node ?? "", why: reasonText(t, w.params.reason ?? "") }));
    if (w.code === "port_lossy") out.push(t("err.port_lossy", lossyVars(t, w.params)));
    if (w.code === "hy2_decoy_paused") out.push(decoyNote(t, w.params));
  }
  return out;
}

/** The fields of a stored TCP row that the renewal line reads. */
type TcpRow = { port: number; role: string; alpn: boolean; profile: string; verdict: string };

export type Renewal = { kind: "ok80" } | { kind: "ok443" } | { kind: "risk"; profile: string };

/**
 * Whether the node's Let's Encrypt certificates renew (design/vless.md section 7.4): through port 80 when it is ours, else
 * through TLS-ALPN on 443 when an alpn listener there is ours, else not (risk; `profile` is the VLESS profile that holds
 * 443 with another site's REALITY, "" when there is none). null when there is nothing to say: no row for port 80 (no ACME
 * name), or port 80 was not probed (no verdict), where a warning would be a guess.
 */
export function renewalOf(rows: readonly TcpRow[]): Renewal | null {
  const p80 = rows.find((r) => r.port === 80);
  if (!p80?.verdict) return null;
  if (p80.verdict === "ours") return { kind: "ok80" };
  if (rows.some((r) => r.port === 443 && r.alpn && r.verdict === "ours")) return { kind: "ok443" };
  return { kind: "risk", profile: rows.find((r) => r.port === 443 && r.role === "vless" && !r.alpn)?.profile ?? "" };
}

/** The renewal line in words. */
export function renewalText(t: T, r: Renewal): string {
  if (r.kind !== "risk") return t(`ports.renewal.${r.kind}`);
  return t("ports.renewal.risk", { why: r.profile ? t("ports.renewal.risk.vless", { profile: r.profile }) : "" });
}
