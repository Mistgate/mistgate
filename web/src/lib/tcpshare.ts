// Services of one node that share a TCP port by the name in the connection (SNI): what the dialogs that put a profile on a node, or
// change its port or name, ask of the admin when the panel says the port is held (internal/panel/access/share.go), and the words of
// the DNS record the new name needs. Pure: the screens only draw it.

/** The panel's refusals about a shared TCP port (plugin.TCPShareProblems), which the dialogs word before the click. */
export const shareCodes = ["sni_taken", "share_needs_site", "share_unsupported", "share_agent_old"] as const;
export type ShareCode = (typeof shareCodes)[number];

export const isShareCode = (code: string): code is ShareCode => (shareCodes as readonly string[]).includes(code);

/**
 * What the panel offers for a name that is held (sni_taken with share=1): the profile stands beside the other one on the same port
 * under a name of its own. `suggest` is the name it proposes ("" for a node known by an IP address: the admin brings a domain),
 * `free` the free port the old answer ("take 8443") would use, "" when there is none.
 */
export type ShareOffer = { port: string; profile: string; held: string; suggest: string; free: string };

export function shareOffer(code: string, vars: Record<string, string | undefined>): ShareOffer | null {
  if (code !== "sni_taken" || vars.share !== "1") return null;
  const free = vars.free && Number(vars.free) > 0 ? vars.free : "";
  return { port: vars.port ?? "", profile: vars.profile ?? "", held: vars.name ?? "", suggest: vars.suggest ?? "", free };
}

/** The free port of a share refusal (the one the panel found for "take it instead"), 0 when there is none. */
export const shareFreePort = (vars: Record<string, string | undefined>): number => {
  const n = Number(vars.free);
  return Number.isInteger(n) && n > 0 ? n : 0;
};

const label = "[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?";
const hostRe = new RegExp(`^(${label}\\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`);

/** An IPv4 or IPv6 address (a name a client sends in SNI is never one). */
export const isIpAddress = (s: string) => /^\d{1,3}(\.\d{1,3}){3}$/.test(s) || (s.includes(":") && /^[0-9a-f:.]+$/i.test(s.replace(/^\[|\]$/g, "")));

/** A name the node can answer to: a host name with a dot, in ASCII, no wildcard, not an IP address (what the panel accepts, protocols.IsHostname). */
export function validShareName(raw: string): boolean {
  const n = raw.trim().toLowerCase().replace(/\.$/, "");
  return n.length <= 253 && hostRe.test(n) && !isIpAddress(n);
}

/** The name as the node reads it: lower case, no trailing dot. */
export const shareName = (raw: string) => raw.trim().toLowerCase().replace(/\.$/, "");

/** The record that makes `name` lead to the node: A for an IPv4 address, AAAA for IPv6, CNAME to the node's host name otherwise. */
export type DnsRecord = { type: "A" | "AAAA" | "CNAME"; name: string; value: string };

export function dnsRecord(name: string, nodeAddress: string): DnsRecord | null {
  const value = nodeAddress.trim().replace(/^\[|\]$/g, "").replace(/\.$/, "");
  if (!name || !value) return null;
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(value)) return { type: "A", name, value };
  if (value.includes(":")) return { type: "AAAA", name, value };
  return { type: "CNAME", name, value };
}

/** The record as one line, the way a zone file or a DNS panel takes it: "cdn.de1.example.com A 203.0.113.7". */
export const dnsLine = (r: DnsRecord) => `${r.name} ${r.type} ${r.value}`;

/** What the node page needs of an inbound to tell who shares a TCP port. */
export type PortInbound = { id: string; profileId: string; profileName: string; protocol: string; port: number; tlsServerName: string; on: boolean; lastError: string };

/** What the node reported of one shared port (Plain<SharedPort> of the admin API, the parts the card reads). */
export type PortSnapshot = {
  port: number;
  defaultInboundId: string;
  accepted: number;
  routes: { inboundId: string; delivered: number }[];
  defaultNoSni: number;
  defaultUnknownName: number;
  defaultNotTls: number;
  defaultBadHello: number;
  defaultHelloTooLarge: number;
  defaultRouteDown: number;
  timeouts: number;
  evicted: number;
  refused: number;
  noRoute: number;
  unknownNames: { name: string; count: number }[];
  bindError: string;
};

export type ShareRow = { inbound: PortInbound; isDefault: boolean; delivered: number | null };

/** One TCP port that services share, as the node's card draws it. */
export type SharedPortView = {
  port: number;
  rows: ShareRow[];
  snap: PortSnapshot | null;
  /** The agent cannot share (the panel held the others back, share_agent_old): only `running` runs, `waiting` are the ones that wait. */
  old: boolean;
  running: string;
  waiting: string[];
  /** Two or more services are set up on the port and the node has not reported on it yet. */
  noReport: boolean;
};

/** The panel held this inbound back because the node's agent cannot share a TCP port (fleet withheldShareReason). */
export const heldBack = (lastError: string) => /^share_agent_old\b/i.test(lastError);

/**
 * The TCP ports that two services or more stand on (switched-on inbounds), with what the node reported of each: who gets what, who
 * is the default. A port the agent reports as shared stays in the list while its snapshot is fresh even when the inbounds are gone
 * from the page. A hop range and UDP ports are never in it (`isTcp`).
 */
export function sharedPortViews(inbounds: readonly PortInbound[], snaps: readonly PortSnapshot[], isTcp: (protocol: string) => boolean): SharedPortView[] {
  const by = new Map<number, PortInbound[]>();
  for (const i of inbounds) {
    if (!i.port || !i.on || !isTcp(i.protocol)) continue;
    by.set(i.port, [...(by.get(i.port) ?? []), i]);
  }
  const ports = new Set<number>([...[...by].filter(([, l]) => l.length >= 2).map(([p]) => p), ...snaps.map((s) => s.port)]);
  const out: SharedPortView[] = [];
  for (const port of [...ports].sort((a, b) => a - b)) {
    const snap = snaps.find((s) => s.port === port) ?? null;
    const here = by.get(port) ?? [];
    const ids = new Set(here.map((i) => i.id));
    const extra = (snap?.routes ?? []).map((r) => inbounds.find((i) => i.id === r.inboundId)).filter((i): i is PortInbound => !!i && !ids.has(i.id));
    const list = [...here, ...extra].sort((a, b) => a.id.localeCompare(b.id));
    if (list.length < 2 && !snap) continue;
    const old = list.some((i) => heldBack(i.lastError));
    const running = list.find((i) => !heldBack(i.lastError))?.id ?? "";
    const delivered = new Map((snap?.routes ?? []).map((r) => [r.inboundId, r.delivered]));
    out.push({
      port,
      snap,
      old,
      running,
      waiting: list.filter((i) => heldBack(i.lastError)).map((i) => i.profileName),
      noReport: !snap && !old,
      rows: list.map((inbound) => ({ inbound, isDefault: snap ? snap.defaultInboundId === inbound.id : old && inbound.id === running, delivered: snap ? (delivered.get(inbound.id) ?? 0) : null })),
    });
  }
  return out;
}
