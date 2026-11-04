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

// the panel's udpCISCountryCodes (internal/panel/fleet/ports.go): a confirmation is sent from the other side of this line
const cis = new Set(["RU", "BY", "KZ", "KG", "AM", "AZ", "TJ", "UZ", "MD", "TM"]);

/** The region of a sender by its country: "cis", "other", or "" when the country is unknown (the panel, a gone node). */
export const udpRegion = (countryCode: string): string => (countryCode ? (cis.has(countryCode.toUpperCase()) ? "cis" : "other") : "");

/** "CIS" / "outside the CIS"; "" for anything else. */
export const regionWord = (t: T, region: string): string => (region === "cis" || region === "other" ? t(`ports.region.${region}`) : "");

/** One sender's run of a UDP check: what it sent, what arrived, and whether it lost packets ("warn" lossy, "bad" broken). */
export type PortRun = { sender: string; sent: number; got: number; loss: "" | "warn" | "bad" };

type Confirmable = { sender: string; sent: number; got: number; verdict: string; senderVerdict: string; confirmSender: string; confirmSent: number; confirmGot: number; confirmVerdict: string };

const lossOf = (verdict: string): PortRun["loss"] => (verdict === "broken" ? "bad" : verdict === "lossy" ? "warn" : "");

/**
 * The two runs of a port the panel checked from a second sender (blame "path": one lost, "node": both did); null for a
 * port checked from one sender only, which keeps its single line.
 */
export function portRuns(c: Confirmable): [PortRun, PortRun] | null {
  if (!c.confirmSender) return null;
  return [
    { sender: c.sender, sent: c.sent, got: c.got, loss: lossOf(c.senderVerdict || c.verdict) },
    { sender: c.confirmSender, sent: c.confirmSent, got: c.confirmGot, loss: lossOf(c.confirmVerdict) },
  ];
}

/** How long before a port check a passing protocol probe through the port still answers it (portProbeTrust in internal/panel/health/eval.go). */
const portProbeTrustSec = 15 * 60;

/**
 * A port whose test datagrams were lost (lossy, broken) while the panel's own protocol probe through it passed at most
 * 15 minutes before the check or any time after it (protocolOkUnix): the test is blocked, typically by a hoster's filter,
 * and people get through. The panel opens no port_lossy alert for it; the page reads it as calmly.
 */
export function testBlocked(c: { verdict: string; checkedUnix?: number; protocolOkUnix?: number }): boolean {
  const ok = c.protocolOkUnix ?? 0;
  return (c.verdict === "lossy" || c.verdict === "broken") && ok > 0 && ok >= (c.checkedUnix ?? 0) - portProbeTrustSec;
}

type Checked = Confirmable & { port: number; blame: string };

/**
 * The senders whose way to this node is filtered rather than lossy, as the panel tells them apart (combinePortCheckRuns in
 * internal/panel/fleet/ports.go): every port the sender lost while the other sender got through (blame "path") got at most
 * 1 % of its packets, and other ports arrived whole from that same sender. Typically a hoster's DDoS filter that admits only
 * UDP 443 from data-centre networks: the node is fine, port checks from that sender cannot pass. `open` is sorted.
 */
export function udpFilters(checks: readonly Checked[]): { sender: string; open: number[] }[] {
  const by = new Map<string, { lost: boolean; dropped: boolean; open: number[] }>();
  for (const c of checks) {
    if (!c.confirmSender) continue;
    const runs = [
      { sender: c.sender, verdict: c.senderVerdict || c.verdict, sent: c.sent, got: c.got },
      { sender: c.confirmSender, verdict: c.confirmVerdict, sent: c.confirmSent, got: c.confirmGot },
    ];
    for (const r of runs) {
      if (!r.verdict) continue;
      const s = by.get(r.sender) ?? { lost: false, dropped: true, open: [] };
      by.set(r.sender, s);
      if (!lossOf(r.verdict)) s.open.push(c.port);
      else if (c.blame === "path") {
        s.lost = true;
        s.dropped &&= r.got * 100 <= r.sent;
      }
    }
  }
  return [...by].filter(([, s]) => s.lost && s.dropped && s.open.length > 0).map(([sender, s]) => ({ sender, open: s.open.sort((a, b) => a - b) }));
}

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
  const port80 = p.port80 === "open" || p.port80 === "closed" || p.port80 === "busy" || p.port80 === "unchecked" ? t(`node.check.decoy.port80.${p.port80}`, vars) : "";
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
