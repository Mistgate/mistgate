import { queryOptions, useQuery } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import {
  AlertKind,
  AlertSeverity,
  CheckStatus,
  CountryVerdict,
  DoctorStatus,
  type Alert as AlertMsg,
  type CheckBucket as BucketMsg,
  type CheckCell as CellMsg,
  type CountryCheck as CountryCheckMsg,
  type DoctorItem as DoctorItemMsg,
  type NodeDoctor as NodeDoctorMsg,
} from "@/gen/mistgate/admin/v1/health_pb";
import type { T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { health } from "./api";
import { itemParams } from "./doctor-detail";
import type { Fmt } from "./format";
import { plain, type Plain } from "./plain";
import { pollMs } from "./queries";
import { protocolName } from "./series";
import { meQuery } from "./session";
import { lossPct, senderLabel } from "./port-check";
import { torrentEvidenceText } from "./torrent";
import { pauseReasonText } from "./updates";

// Everything the Health screens read and the small pure helpers they share (alert wording, matrix cells, bars,
// doctor grouping). The server sends keys and params, never prose (health.proto); the wording is in i18n/health.ts.

export type Alert = Plain<AlertMsg>;
export type CheckCell = Plain<CellMsg>;
export type CheckBucket = Plain<BucketMsg>;
export type DoctorItem = Plain<DoctorItemMsg>;
export type NodeDoctor = Plain<NodeDoctorMsg>;
type Params = Record<string, string | number>;

// ---------------------------------------------------------------------------------------------------
// Queries. Alerts, checks and the doctor poll like the rest of the admin (10 s); the server does the heavy work.

export const alertsQuery = queryOptions({
  queryKey: ["health", "alerts"],
  queryFn: async ({ signal }) => plain(await health.listAlerts({}, { signal })),
  refetchInterval: pollMs,
});

/** Empty nodeId = every node (the Checks tab); a node id = only that node (the pills of its Overview). */
export const checksQuery = (nodeId = "") =>
  queryOptions({
    queryKey: ["health", "checks", nodeId],
    queryFn: async ({ signal }) => plain(await health.getChecks({ nodeId }, { signal })),
    refetchInterval: pollMs,
  });

/** The stored doctor reports, never a call to a node. Empty nodeId = the whole fleet. */
export const doctorQuery = (nodeId = "") =>
  queryOptions({
    queryKey: ["health", "doctor", nodeId],
    queryFn: async ({ signal }) => plain(await health.getDoctor({ nodeId }, { signal })),
    refetchInterval: pollMs,
  });

/** What the signed-in role may do here (policy.go: mute, check now and run doctor need owner or helper; a fix needs the owner). */
export function useCan() {
  const role = useQuery(meQuery).data?.admin?.role;
  return { run: role === Role.OWNER || role === Role.HELPER, fix: role === Role.OWNER };
}

// ---------------------------------------------------------------------------------------------------
// Keys from the server

export const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);

/**
 * Wording for a server key. A key with a variant the SPA does not know ("...why.some_new_code") falls back to
 * "...why.unknown", then to the key without the variant; null when nothing fits, so the caller decides what to show.
 */
export function lookup(t: T, key: string, params: Params = {}): string | null {
  if (!key) return null;
  const i = key.lastIndexOf(".");
  const tries = i > 0 ? [key, `${key.slice(0, i)}.unknown`, key.slice(0, i)] : [key];
  for (const k of tries) if (hasKey(k)) return t(k, params);
  return null;
}

// ---------------------------------------------------------------------------------------------------
// Alerts

export function severityKind(s: AlertSeverity): StatusKind {
  if (s === AlertSeverity.CRITICAL) return "bad";
  if (s === AlertSeverity.WARNING) return "warn";
  return "blip"; // info: grey-blue, never red
}

export const severityWord = (s: AlertSeverity): MessageKey =>
  s === AlertSeverity.CRITICAL ? "hl.sev.critical" : s === AlertSeverity.WARNING ? "hl.sev.warning" : "hl.sev.info";

export function checkTitle(t: T, id: string): string {
  return lookup(t, `doctor.${id}.title`) ?? id;
}

export function alertTitle(t: T, a: Alert): string {
  const params: Record<string, string> = { ...a.params };
  let key = a.titleKey;
  // the blind-panel alerts are fleet-wide and have no profile to name
  if (a.kind === AlertKind.CHECK_FAILED && (a.subject === "panel_egress" || a.subject === "probe_path")) key += "." + a.subject;
  if (a.kind === AlertKind.NODE_DOWN && a.subject === "panel_link") key += ".panel_link"; // fleet-wide, no node to name
  if (a.kind === AlertKind.HOST_BLIP && a.subject === "flapping") key += ".flapping"; // the quiet blip records keep "Host blip"
  if ((a.kind === AlertKind.DOCTOR_WARN || a.kind === AlertKind.DOCTOR_FAIL) && params.check) params.check_title = checkTitle(t, params.check);
  if (a.kind === AlertKind.COUNTRY_BLOCKED) {
    params.country = countryIn(t, params.country ?? "");
    if (a.subject === "partial") key += ".partial"; // some networks only: its own key next to the full block's (subject "")
  }
  return lookup(t, key, params) ?? a.titleKey;
}

/**
 * The plain-language reason; empty when the SPA has no wording for it. A doctor or certificate alert carries the doctor
 * row's params, worded like the row itself (doctor-detail.ts: "down" as "not working", profiles by name); node_down
 * says how long in the coarsest unit.
 */
export function alertWhy(t: T, fmt: Fmt, a: Alert): string {
  const doctor = a.kind === AlertKind.DOCTOR_WARN || a.kind === AlertKind.DOCTOR_FAIL || a.kind === AlertKind.CERT_EXPIRY;
  const params: Params = doctor ? itemParams(t, fmt, a) : { ...a.params };
  // a paused rollout names the step error code of its node: say it in words, the short form of the pause
  if (a.kind === AlertKind.UPDATE_FAILED && a.params.reason) params.reason = pauseReasonText(t, a.params.reason);
  if (a.kind === AlertKind.NODE_DOWN && a.params.minutes) params.duration = fmt.duration(Number(a.params.minutes) * 60);
  if (a.kind === AlertKind.ACCESS_ENDED && Number(a.params.since) > 0) params.date = fmt.stamp(Number(a.params.since));
  if (a.kind === AlertKind.USERS_IMPACTED) params.protocol = protocolName(a.subject);
  if (a.kind === AlertKind.TORRENT) return lookup(t, a.whyKey, torrentParams(t, params)) ?? "";
  if (a.kind === AlertKind.COUNTRY_BLOCKED) {
    // "RU" and "443, 8443" as the sentence needs them: "from Russia", "ports 443 and 8443"
    params.country = countryIn(t, a.params.country ?? "");
    const ports = (a.params.ports ?? "").split(",").map((p) => p.trim()).filter(Boolean);
    params.ports = ports.length ? t.n("cr.alert.ports", ports.length, { ports: listOf(fmt.locale, ports) }) : "";
    // the server's key assumes Globalping's probes; the TSPU probe alone, and two sources that disagree, have their own
    const sources = (a.params.sources ?? "").split(",");
    const own =
      sources.includes("tspu") && !sources.includes("globalping")
        ? "health.alert.country_blocked.why.tspu"
        : a.params.confidence === "conflict"
          ? `health.alert.country_blocked.why.conflict.${a.params.verdict ?? ""}`
          : "";
    if (own && hasKey(own)) return t(own, params);
  }
  if (a.kind === AlertKind.PORT_LOSSY) {
    params.lost = String(lossPct(Number(a.params.sent), Number(a.params.got)));
    params.sender = senderLabel(t, a.params.sender ?? "");
  }
  return lookup(t, a.whyKey, params) ?? "";
}

/** The sentence of a TORRENT alert: "N attempts", then the evidence of the last one and the ports, when the agent said so. */
function torrentParams(t: T, params: Params): Params {
  const count = Number(params.count) || 1;
  const evidence = String(params.evidence ?? "");
  const ports = String(params.ports ?? "");
  const nPorts = ports ? ports.split(",").length : 0;
  const proof = !evidence
    ? ""
    : nPorts
      ? t.n("health.alert.torrent.proofPorts", nPorts, { evidence: torrentEvidenceText(t, evidence), ports })
      : t("health.alert.torrent.proof", { evidence: torrentEvidenceText(t, evidence) });
  return { ...params, attempts: t.n("health.alert.torrent.attempts", count), proof };
}

export type FixRequest = { fixId: string; params: Record<string, string> };

/** The "apply_fix:<id>" action of an alert, with the parameters the fix takes (restart_inbound names its inbound). */
export function alertFix(a: Alert): FixRequest | null {
  const act = a.actions.find((x) => x.startsWith("apply_fix:"));
  if (!act) return null;
  const fixId = act.slice("apply_fix:".length);
  // a doctor row names it inbound_id, a check alert inbound
  const inbound = a.params.inbound_id || a.params.inbound;
  return { fixId, params: fixId === "restart_inbound" && inbound ? { inbound_id: inbound } : {} };
}

/** The fix's button; a restart says which profile it restarts when the name is known. */
export const fixLabel = (t: T, fixId: string, profile = "") =>
  fixId === "restart_inbound" && profile ? t("hl.fix.restartNamed", { profile }) : (lookup(t, `health.fix.${fixId}.label`) ?? fixId);

/**
 * What a restart does to the people on it, said before the click: one profile, several named ones or every profile of
 * the node, with the connections open now (params.online). Null when the panel did not count them (an older panel).
 */
export function restartConsequence(t: T, scope: "one" | "many" | "all", online: string | undefined): string | null {
  const n = Number(online);
  if (online === undefined || online === "" || !Number.isFinite(n)) return null;
  return t(`hl.restart.${scope}${n > 0 ? "" : "Idle"}` as MessageKey, { online: n });
}

/** The mute choices of an alert: an hour, until 08:00 local time, a day, a week (the panel's longest). */
export function muteChoices(now: Date): { key: MessageKey; seconds: number }[] {
  const morning = new Date(now);
  morning.setHours(8, 0, 0, 0);
  if (morning.getTime() <= now.getTime()) morning.setDate(morning.getDate() + 1);
  return [
    { key: "hl.mute.hour", seconds: 3600 },
    { key: "hl.mute.morning", seconds: Math.round((morning.getTime() - now.getTime()) / 1000) },
    { key: "hl.mute.day", seconds: 86_400 },
    { key: "hl.mute.week", seconds: 7 * 86_400 },
  ];
}

/** A node's doctor item as a fix request; restart_inbound names its inbound when the check does. */
export function itemFix(item: DoctorItem): FixRequest | null {
  if (!item.fixId) return null;
  const inbound = item.params.inbound_id;
  return { fixId: item.fixId, params: item.fixId === "restart_inbound" && inbound ? { inbound_id: inbound } : {} };
}

/** Text of the agent's short error code from a failed ApplyFix ("busy", "failed: <message>", ...). */
export function fixErrorText(t: T, error: string): string {
  const code = error.split(":")[0]!.trim();
  const key = `hl.fix.err.${code}`;
  if (hasKey(key)) return t(key);
  if (code === "failed") return error.replace(/^failed:\s*/, "") || t("hl.fix.err.generic");
  return error || t("hl.fix.err.generic");
}

export const resolutionWord = (r: string): MessageKey => {
  const k = `hl.res.${r}`;
  return hasKey(k) ? k : "hl.res.unknown";
};

/**
 * An alert about a person (the panel derives it from its own database), not about a node: it has its own list (Health ▸
 * People) and the user's page, and stays out of the node alerts, their counts and the strip. USERS_IMPACTED is about a
 * node and is not one of these.
 */
export const isPeopleAlert = (a: Pick<Alert, "kind">) =>
  a.kind === AlertKind.ACCESS_ENDED || a.kind === AlertKind.USER_CONNECTION || a.kind === AlertKind.TORRENT || a.kind === AlertKind.DEVICE_LIMIT;

/** Whether an alert about a person is about this user: params.user_id, or the subject for the alerts keyed by user (a device alert's subject is the device). */
export const alertIsAbout = (a: Alert, userId: string) => isPeopleAlert(a) && (a.params.user_id === userId || a.subject === userId);

/** Warnings first, then info; each group keeps the server's order (newest first). */
export const peopleOrder = (alerts: readonly Alert[]): Alert[] => alerts.filter(isPeopleAlert).sort((a, b) => b.severity - a.severity);

/** Alerts that count for the badge and the strip: not muted, not info. */
export function isLoud(a: Alert, now: number): boolean {
  return a.severity >= AlertSeverity.WARNING && a.mutedUntilUnix <= now;
}

// ---------------------------------------------------------------------------------------------------
// Synthetic checks

export type CellLook = {
  kind: StatusKind | "none";
  /** What the 44 px cell says: "42 ms", "✕ failed", "upd.", "—". */
  label: string;
  /** Longer form for the screen reader: the cell's numbers or its state. */
  state: string;
  /** The hour's connect median ("2.6 s") when it is slow, for the cell's second line; empty otherwise. */
  slow: string;
  /** The tooltip, one line each. */
  tip: string[];
};

/**
 * A connect median at or over this is a handshake that waited for a retry (QUIC about 2.5 s, WireGuard 5 s): the same
 * line as udpRetryMS of the server's UDP-loss notice (internal/panel/health/rpc.go).
 */
export const SLOW_CONNECT_MS = 2000;

/**
 * The cell's number and where it comes from: the hour's median of the response through an open tunnel, else the last
 * round's response, else the old cold time (rounds from before the panel measured the response have only that).
 */
export function cellMs(cell: CheckCell): { ms: number; from: "hour" | "last" | "cold" } {
  if (cell.responseHourMs > 0) return { ms: cell.responseHourMs, from: "hour" };
  const last = cell.last?.responseMs ?? 0;
  if (last > 0) return { ms: last, from: "last" };
  return { ms: cell.latencyHourMs || (cell.last?.latencyMs ?? 0), from: "cold" };
}

/** "48 ms" under a second, "2.6 s" from there on. */
export const msText = (t: T, fmt: Fmt, ms: number) => (ms < 1000 ? `${ms} ${t("hl.ms")}` : `${fmt.num(ms / 1000, 1)} ${t("unit.s")}`);

const tipFrom = { hour: "hl.tip.response", last: "hl.tip.responseLast", cold: "hl.tip.cold" } as const;

export function cellLook(t: T, fmt: Fmt, cell: CheckCell, nodeStatus: NodeStatus): CellLook {
  const only = (kind: CellLook["kind"], label: string, state: string): CellLook => ({ kind, label, state, slow: "", tip: [state] });
  if (!cell.deployed) return only("none", t("hl.cell.none"), t("hl.cell.notDeployed"));
  const last = cell.last;
  if (!last || last.status === CheckStatus.UNSPECIFIED) return only("off", t("hl.cell.wait"), t("hl.cell.noResult"));
  const err = checkError(t, last.errorCode);
  const ok = cell.history.reduce((n, b) => n + b.ok, 0);
  const failed = cell.history.reduce((n, b) => n + b.failed, 0);
  const day = t("hl.tip.day", { ok, failed });
  const where = t("hl.tip.where");
  switch (last.status) {
    case CheckStatus.OK:
    case CheckStatus.DEGRADED: {
      const { ms, from } = cellMs(cell);
      const main = t(tipFrom[from], { ms: `${ms} ${t("hl.ms")}` });
      const connect = cell.connectHourMs > 0 ? t("hl.tip.connect", { time: msText(t, fmt, cell.connectHourMs) }) : "";
      const slow = cell.connectHourMs >= SLOW_CONNECT_MS ? msText(t, fmt, cell.connectHourMs) : "";
      const dash = t("hl.cell.none");
      const round = t("hl.tip.last", {
        time: fmt.clock(last.atUnix),
        response: last.responseMs > 0 ? msText(t, fmt, last.responseMs) : dash,
        connect: last.connectMs > 0 ? msText(t, fmt, last.connectMs) : dash,
      });
      return {
        kind: last.status === CheckStatus.OK ? "ok" : "warn",
        label: `${ms} ${t("hl.ms")}`,
        state: [main, slow && connect, err].filter(Boolean).join(" · "),
        slow,
        tip: [main, connect, round, err, day, where].filter(Boolean),
      };
    }
    case CheckStatus.FAILED:
      return { ...only("bad", t("hl.cell.fail"), err || t("hl.cell.fail")), tip: [err || t("hl.cell.fail"), day, where] };
    default: // skipped on purpose: a profile that failed to start must not look like one switched off
      if (nodeStatus === NodeStatus.UPDATING) return only("busy", t("hl.cell.upd"), err);
      switch (last.errorCode) {
        case "inbound_failed":
          return only("bad", t("hl.cell.failed"), err);
        case "inbound_pending":
          return only("busy", t("hl.cell.starting"), err);
        case "node_offline":
          return only("blip", t("hl.cell.offline"), err);
        case "client_unsupported":
          return only("off", t("hl.cell.na"), err);
        default: // inbound_disabled, and inbound_not_active of an older panel
          return only("off", t("hl.cell.off"), err);
      }
  }
}

// ---------------------------------------------------------------------------------------------------
// Reachability from a country (GetChecksResponse.country_checks): what Globalping's probes inside one country saw of each node,
// what the TSPU probe (one of the owner's nodes in the country) saw, and what the block lists say of its address, combined by
// the server into one verdict. The server sends only codes and numbers; empty while every source is off.

export type CountryCheck = Plain<CountryCheckMsg>;
type CountryProbe = CountryCheck["probes"][number];

/** A row with a Globalping round (it has probes, or a reason why it has none); a row of the TSPU probe or the lists alone has neither. */
export const isLive = (c: Pick<CountryCheck, "probes" | "reason">) => c.probes.length > 0 || c.reason !== "";

const portOrder = ["blocked", "open", "refused", "timeout"];

/**
 * What the TSPU probe saw on a node's TCP ports, the ports of a verdict together: "TCP 443, 8443: blocked · 2053: open". A
 * verdict this page does not know is shown as sent. Empty without ports.
 */
export function tspuPortsText(t: T, ports: readonly { port: number; verdict: string }[]): string {
  if (!ports.length) return "";
  const verdicts = [...new Set(ports.map((p) => p.verdict))].sort((a, b) => (portOrder.indexOf(a) + 1 || 9) - (portOrder.indexOf(b) + 1 || 9));
  const groups = verdicts.map((v) => {
    const key = `cr.port.${v}`;
    return t("cr.port.group", { ports: ports.filter((p) => p.verdict === v).map((p) => p.port).join(", "), word: hasKey(key) ? t(key) : v });
  });
  return `TCP ${groups.join(" · ")}`;
}

/** A block list by its name ("RKN registry (antifilter ipsum)"); a code the dictionary does not know stays a code. */
export function listName(t: T, code: string): string {
  const key = `cr.list.${code}`;
  return hasKey(key) ? t(key) : code;
}

/** The CDN or hosting range of the node's address, "Hetzner subnet, DE"; empty when the lists know none. */
export function cdnOf(t: T, c: Pick<CountryCheck, "cdnProvider" | "cdnRegion">): string {
  if (!c.cdnProvider) return "";
  return c.cdnRegion ? t("cr.cdn.region", { provider: c.cdnProvider, region: c.cdnRegion }) : t("cr.cdn", { provider: c.cdnProvider });
}

const capital = (s: string) => s.charAt(0).toLocaleUpperCase() + s.slice(1);

/** A country as it stands after "from" ("from Russia", "из России"); a code the dictionary does not know stays a code. */
export function countryIn(t: T, code: string): string {
  const key = `cr.in.${code}`;
  return hasKey(key) ? t(key) : t("cr.in", { code });
}

/** Why a round has no verdict ("too few probes answered"); empty for a code the SPA does not know. */
export function countryReason(t: T, code: string): string {
  const key = `cr.reason.${code}`;
  return code && hasKey(key) ? t(key) : "";
}

export type CountryLook = {
  /** OK and UNKNOWN stay quiet: only the blocks (on some networks, of TCP, of everything) and a node in a block list take the problem colours. */
  kind: "ok" | "partial" | "warn" | "bad" | "listed" | "unknown";
  /** What the cell says: "reachable", "some networks", "TCP blocked", "unreachable", "listed", "no data". */
  label: string;
  /**
   * The cell's small second line: "ICMP passes" (Globalping saw TCP blocked), "sources disagree", the networks a partial block
   * cuts off, or the TSPU probe's name for a block it alone saw.
   */
  note: string;
  /** The verdict in a sentence: the tooltip's first line. */
  head: string;
  /** What a screen reader says: the verdict, the disagreement or the source, and that a live verdict's address is in the registry too. */
  state: string;
  /** A live verdict whose address is in a block list as well: the cell and the row carry the list mark beside the verdict. */
  alsoListed: boolean;
  /** The quiet line of a row, where a phone has no tooltip: who saw it, the lists, and when, or why there is no verdict. */
  line: string;
  /** Two live sources that disagree: what each said ("Globalping: reachable · probe: TCP blocked"); empty when they do not. */
  conflict: string;
  /** A partial block: "Blocked on some networks: MTS (AS8359, Novosibirsk)". Empty on any other verdict: a full block marks every probe dark. */
  dark: string;
  /** Named operators the round found no home or mobile probe for: "No home/mobile probe this round: MegaFon, T2". */
  noProbe: string;
  /**
   * The tooltip under the verdict: each Globalping probe (where it is, what it got, whether it counted, whether it is one of the
   * networks a partial block cuts off) and the round's time; empty without a round.
   */
  probes: { where: string; result: string; counted: boolean; dark: boolean }[];
  at: string;
  /** A Globalping round without a verdict while the TSPU probe decided: why the round said nothing. */
  gpNone: string;
  /** What the TSPU probe saw: the probe node and the hop of the box, the ports, the time of the run; null without a run for the node. */
  tspu: { where: string; ports: string; at: string } | null;
  /** What the block lists add: why the live round said nothing, the lists, the CDN range, when the lists were compared. */
  lists: string[];
  /** Where the verdict comes from: Globalping from home and mobile networks or data centers, the TSPU probe, both, or the block lists only. */
  via: string;
  /**
   * A partial block in full, for the lines under its verdict where there is room and no hover (a node's card, the phone): the
   * networks that do not see the node, the probes that reach it, the operators the round had no probe for, the round's age.
   * Null on any other verdict, and on a partial row that names no dark network.
   */
  breakdown: { dark: BreakdownNet[]; seen: BreakdownNet[]; noProbe: string; at: string } | null;
};

/** A network of the breakdown: "MTS" and "AS8359 · Novosibirsk", with the time of its answer when it reached the node. */
export type BreakdownNet = { name: string; tag: string };

/**
 * A probe for the tooltip: "Rostelecom, Moscow" and "ICMP 42 ms · TCP 443, 8443 ✕" (ports that differ get a mark each). On a
 * partial block a dark probe says that it reaches the other nodes and not this one.
 */
function probeOf(t: T, p: CountryProbe, partial: boolean): CountryLook["probes"][number] {
  const icmp = `ICMP ${p.icmpOk ? (p.icmpMs > 0 ? `${p.icmpMs} ${t("hl.ms")}` : "✓") : "✕"}`;
  const mark = (ok: boolean) => (ok ? "✓" : "✕");
  const same = p.tcp.every((x) => x.ok === p.tcp[0]?.ok);
  const ports = same ? `${p.tcp.map((x) => x.port).join(", ")} ${mark(!!p.tcp[0]?.ok)}` : p.tcp.map((x) => `${x.port} ${mark(x.ok)}`).join(", ");
  const where = [p.network, p.city].filter(Boolean).join(", ") || "?";
  const dark = partial && p.valid && p.dark;
  return {
    where: !p.valid ? t("cr.tip.skipped", { line: where }) : dark ? t("cr.tip.darkProbe", { line: where }) : where,
    result: p.tcp.length ? `${icmp} · TCP ${ports}` : icmp,
    counted: p.valid,
    dark,
  };
}

/** A probe's network as the partial alert names it: "MTS (AS8359, Novosibirsk)". The names come from Globalping: text, never markup. */
function networkOf(p: CountryProbe): string {
  const tag = [p.asn ? `AS${p.asn}` : "", p.city].filter(Boolean).join(", ");
  return tag ? `${p.network || "?"} (${tag})` : p.network || "?";
}

/** "MTS" and "AS8359 · Novosibirsk · 41 ms" (ms: the TCP connect, else the ping, when the probe got one; the unit never wraps alone). */
function breakdownNet(t: T, p: CountryProbe, ms: number): BreakdownNet {
  return { name: p.network || "?", tag: [p.asn ? `AS${p.asn}` : "", p.city, ms > 0 ? `${ms}\u00a0${t("hl.ms")}` : ""].filter(Boolean).join(" · ") };
}

const viaKeys: Record<string, MessageKey> = { eyeball: "cr.tip.eyeball", dc_only: "cr.tip.dc", listed: "cr.tip.listsOnly" };

/** A verdict of one source in a word, for "Globalping: reachable · probe: TCP blocked". */
const verdictWord = (t: T, v: CountryVerdict) =>
  v === CountryVerdict.OK
    ? t("cr.v.ok")
    : v === CountryVerdict.PARTIAL
      ? t("cr.v.partialLong")
      : v === CountryVerdict.TCP_BLOCKED
        ? t("cr.v.tcp")
        : v === CountryVerdict.BLOCKED
          ? t("cr.v.blocked")
          : t("cr.v.unknown");

export function countryLook(t: T, fmt: Fmt, c: CountryCheck): CountryLook {
  const live = isLive(c);
  const probed = c.tspuAtUnix > 0; // the TSPU probe has a run for this node, whatever it decided
  // the sources the verdict rests on; a row from before the server named them is Globalping's when it has a round
  const fromGp = c.sources.includes("globalping") || (c.sources.length === 0 && live);
  const fromTspu = c.sources.includes("tspu");
  const conflict = c.confidence === "conflict" ? t("cr.tip.said", { gp: verdictWord(t, c.globalpingVerdict), tspu: verdictWord(t, c.tspuVerdict) }) : "";
  const reason = live ? countryReason(t, c.reason) : probed ? t("cr.reason.tspu") : "";
  const listed = c.listedBy.length > 0;
  const cdn = cdnOf(t, c);
  const names = c.listedBy.map((l) => listName(t, l));
  // a partial block names the networks that get nothing from the node; a full block marks every probe dark, so only PARTIAL does
  const partial = c.verdict === CountryVerdict.PARTIAL;
  const darkProbes = partial ? c.probes.filter((p) => p.valid && p.dark) : [];
  const dark = darkProbes.length ? t("cr.tip.dark", { networks: [...new Set(darkProbes.map(networkOf))].join(t("cr.tip.sep")) }) : "";
  const darkNames = [...new Set(darkProbes.map((p) => p.network).filter(Boolean))].join(", ");
  const networks = dark || (fromGp ? [...new Set(c.probes.filter((p) => p.valid && p.network).map((p) => p.network))].join(", ") : "");
  const ports = tspuPortsText(t, c.tspuPorts);
  const vantage = c.tspuVantageName || c.tspuVantageId;
  const ago = fmt.ago(c.atUnix);
  // only Globalping looks at ICMP: "ICMP passes" is its word for a TCP block, never the probe node's
  const icmp = c.verdict === CountryVerdict.TCP_BLOCKED && fromGp && (c.globalpingVerdict === CountryVerdict.TCP_BLOCKED || c.sources.length === 0);
  const tspuOnly = fromTspu && !fromGp;
  // the phone's line: what the sources saw (or that they disagree), then what the lists add, then when
  const seen = conflict
    ? [t("cr.note.conflict"), conflict]
    : [icmp ? t("cr.icmpPasses") : "", networks, tspuOnly ? ports : "", fromTspu ? t("cr.line.probe", { node: vantage }) : ""];
  const lineOf = (...first: string[]) => [...first, cdn, ago].filter(Boolean).join(" · ");
  const look = (kind: CountryLook["kind"], label: string, head: string, line: string): CountryLook => {
    const note = conflict ? t("cr.note.conflict") : icmp ? t("cr.icmpPasses") : partial ? darkNames : tspuOnly && kind !== "ok" ? t("tp.title") : "";
    return {
      kind,
      label,
      note,
      head,
      // a partial block reads its networks out in full, where the cell shows only their names
      state: [head, conflict ? `${t("cr.tip.conflict")} · ${conflict}` : icmp ? "" : dark || note, listed && kind !== "listed" ? t("cr.line.listed") : ""].filter(Boolean).join(" · "),
      alsoListed: listed && kind !== "listed",
      line,
      conflict,
      dark,
      noProbe: c.noProbe.length ? t("cr.tip.noProbe", { networks: c.noProbe.join(t("cr.tip.sep")) }) : "",
      breakdown: darkProbes.length
        ? {
            // one line per dark network; every valid probe that reached the node (the verdict's carrier: TCP when the node has ports)
            dark: [...new Map(darkProbes.map((p) => breakdownNet(t, p, 0)).map((n) => [`${n.name}|${n.tag}`, n])).values()],
            seen: c.probes
              .filter((p) => p.valid && !p.dark && (p.tcp.length ? p.tcp.some((x) => x.ok) : p.icmpOk))
              .map((p) => breakdownNet(t, p, p.tcp.find((x) => x.ok)?.ms || (p.icmpOk ? p.icmpMs : 0))),
            noProbe: c.noProbe.length ? t("cr.part.noProbe", { networks: c.noProbe.join(t("cr.tip.sep")) }) : "",
            at: t("cr.tip.at", { time: fmt.ago(c.atUnix) }),
          }
        : null,
      probes: c.probes.map((p) => probeOf(t, p, partial)),
      at: live ? t("cr.tip.at", { time: fmt.stamp(c.atUnix) }) : "",
      gpNone: live && !fromGp && fromTspu && c.reason ? t("cr.tip.gpNone", { reason: countryReason(t, c.reason) || c.reason }) : "",
      tspu: probed
        ? {
            where: c.dpiHop > 0 ? t("cr.tip.tspuWhere", { node: vantage, hop: c.dpiHop }) : t("cr.tip.tspuNode", { node: vantage }),
            ports,
            at: t("cr.tip.tspuAt", { time: fmt.stamp(c.tspuAtUnix) }),
          }
        : null,
      lists: [
        kind === "listed" && reason ? t("cr.tip.liveNone", { reason }) : "",
        listed ? t("cr.tip.listedBy", { lists: names.join(t("cr.tip.sep")) }) : "",
        capital(cdn),
        live || probed ? "" : t("cr.tip.listsAt", { time: fmt.stamp(c.atUnix) }),
      ].filter(Boolean),
      via:
        fromGp && fromTspu
          ? t("cr.tip.both", { gp: t(c.eyeball ? "cr.tip.gpHome" : "cr.tip.gpDc") })
          : fromTspu
            ? t("cr.tip.tspu")
            : viaKeys[c.confidence]
              ? t(viaKeys[c.confidence]!)
              : // an unknown round has no confidence of its own: its probes still say where it looked from
                c.probes.length
                ? t(c.eyeball ? "cr.tip.eyeball" : "cr.tip.dc")
                : "",
    };
  };
  const inList = listed ? t("cr.line.listed") : "";
  switch (c.verdict) {
    case CountryVerdict.OK:
      return look("ok", t("cr.v.ok"), t("cr.tip.ok", { country: countryIn(t, c.country) }), lineOf(...seen, inList));
    case CountryVerdict.PARTIAL:
      return look("partial", t("cr.v.partial"), t("cr.tip.partial", { country: countryIn(t, c.country) }), lineOf(...seen, inList));
    case CountryVerdict.TCP_BLOCKED:
      return look("warn", t("cr.v.tcp"), t(icmp ? "cr.tip.tcp" : "cr.tip.tcpOnly"), lineOf(...seen, inList));
    case CountryVerdict.BLOCKED:
      return look("bad", t("cr.v.blocked"), t("cr.tip.blocked"), lineOf(...seen, inList));
    case CountryVerdict.LISTED:
      return look("listed", t("cr.v.listed"), t("cr.tip.listed"), lineOf(names.join(t("cr.tip.sep"))));
    default:
      // with neither a round nor a run the row is the lists' alone, and they do not hold the address
      if (!live && !probed) return look("unknown", t("cr.v.unknown"), t("cr.tip.notListed"), lineOf(t("cr.line.notListed")));
      return look("unknown", t("cr.v.unknown"), reason ? t("cr.tip.unknown", { reason }) : t("cr.tip.unknownBare"), lineOf(reason));
  }
}

export type CountryColumn = { code: string; byNode: ReadonlyMap<string, CountryCheck> };

/**
 * The country column of the checks: the country of the newest round (it names the column) and each node's latest check.
 * Null when there is nothing to show: the integration is off (the server then sends no checks) or no round has run yet.
 */
export function countryColumn(checks: readonly CountryCheck[] | undefined): CountryColumn | null {
  if (!checks?.length) return null;
  const newest = checks.reduce((a, c) => (c.atUnix > a.atUnix ? c : a));
  return { code: newest.country, byNode: new Map(checks.map((c) => [c.nodeId, c])) };
}

/** "de1, nl1 and fi1" in the UI language; zh gets the half-width spaces of its glossary around 和 between Latin names. */
export const listOf = (locale: string, items: readonly string[]) =>
  new Intl.ListFormat(locale, { type: "conjunction" })
    .formatToParts(items)
    .map((p) => (p.type === "literal" && /^\p{Script=Han}+$/u.test(p.value) ? ` ${p.value} ` : p.value))
    .join("");

/** The sentence for a check error code; empty for none. An unknown code is shown as it is. */
export function checkError(t: T, code: string): string {
  if (!code) return "";
  return lookup(t, `health.check.err.${code}`) ?? code;
}

export type Bar = {
  /** 8..100, percent of the bar area. */
  height: number;
  tone: "ok" | "warn" | "bad" | "none";
  bucket: CheckBucket;
};

/**
 * The 24 h strip of a cell: one bar per half hour, as tall as its median latency (a failed bucket is full height in
 * red, a bucket with both results in amber, an empty one a faint stub).
 */
export function barsOf(history: readonly CheckBucket[]): Bar[] {
  const top = Math.max(1, ...history.map((b) => b.latencyMs));
  return history.map((bucket) => {
    if (bucket.ok + bucket.failed === 0) return { height: 8, tone: "none", bucket };
    if (bucket.ok === 0) return { height: 100, tone: "bad", bucket };
    const height = Math.round(20 + (bucket.latencyMs / top) * 80);
    return { height, tone: bucket.failed > 0 ? "warn" : "ok", bucket };
  });
}

// ---------------------------------------------------------------------------------------------------
// Doctor

export function doctorKind(s: DoctorStatus): StatusKind {
  switch (s) {
    case DoctorStatus.FAIL:
      return "bad";
    case DoctorStatus.WARN:
      return "warn";
    case DoctorStatus.OK:
      return "ok";
    default:
      return "off";
  }
}

export const doctorWord = (s: DoctorStatus): MessageKey =>
  s === DoctorStatus.FAIL ? "hl.doctor.status.fail" : s === DoctorStatus.WARN ? "hl.doctor.status.warn" : s === DoctorStatus.OK ? "hl.doctor.status.ok" : "hl.doctor.status.skip";

/** A warning the owner accepted as normal for this node (until its fact changes): not a problem, not counted. */
export const isAccepted = (i: { status: DoctorStatus; acceptedUnix?: number }) => i.status === DoctorStatus.WARN && (i.acceptedUnix ?? 0) > 0;

export const isIssue = (i: DoctorItem) => (i.status === DoctorStatus.FAIL || i.status === DoctorStatus.WARN) && !isAccepted(i);

/** Problems first (FAIL before WARN), each group keeping the agent's check order. */
export function sortIssues<X extends { status: DoctorStatus; acceptedUnix?: number }>(items: readonly X[]): X[] {
  const rank = (s: DoctorStatus) => (s === DoctorStatus.FAIL ? 0 : 1);
  return items.filter((i) => (i.status === DoctorStatus.FAIL || i.status === DoctorStatus.WARN) && !isAccepted(i)).sort((a, b) => rank(a.status) - rank(b.status));
}

export type DoctorGroups = { issues: DoctorItem[]; accepted: DoctorItem[]; ok: DoctorItem[]; skipped: DoctorItem[] };

export function groupDoctor(items: readonly DoctorItem[]): DoctorGroups {
  return {
    issues: sortIssues(items),
    accepted: items.filter(isAccepted),
    ok: items.filter((i) => i.status === DoctorStatus.OK),
    skipped: items.filter((i) => i.status === DoctorStatus.SKIP || i.status === DoctorStatus.UNSPECIFIED),
  };
}

export type FleetIssue = { nodeId: string; nodeName: string; item: DoctorItem; stale: boolean; ageS: number };

/** Every WARN and FAIL of the fleet, FAIL first, then by node name (the server sends nodes by name). */
export function fleetIssues(nodes: readonly NodeDoctor[]): FleetIssue[] {
  const all = nodes.flatMap((n) => n.items.filter(isIssue).map((item) => ({ nodeId: n.nodeId, nodeName: n.nodeName, item, stale: n.stale, ageS: n.ageS })));
  const rank = (i: FleetIssue) => (i.item.status === DoctorStatus.FAIL ? 0 : 1);
  return all.sort((a, b) => rank(a) - rank(b));
}

/** What the fleet doctor summary counts: items actually evaluated (not skipped) and nodes that have a report. */
export function doctorTotals(nodes: readonly NodeDoctor[]) {
  const reported = nodes.filter((n) => n.hasReport && n.items.length > 0);
  return {
    nodes: reported.length,
    items: reported.reduce((a, n) => a + n.items.filter((i) => i.status !== DoctorStatus.SKIP).length, 0),
    issues: reported.reduce((a, n) => a + n.items.filter(isIssue).length, 0),
  };
}

export function itemTitle(t: T, i: DoctorItem): string {
  return lookup(t, i.titleKey || `doctor.${i.id}.title`, i.params) ?? (i.titleKey || i.id);
}

/** `params` are the item's params as the UI words them (doctor-detail.ts itemParams); the raw ones when omitted. */
export function itemWhy(t: T, i: DoctorItem, params: Params = i.params): string {
  return lookup(t, i.whyKey, params) ?? "";
}

/**
 * The one command that fixes a problem by hand, shown with a copy button where the panel cannot do it itself:
 * installing packages is not in the agent's safe set, so kernel_headers points at the explicit owner step.
 */
export function itemCommand(i: DoctorItem): string | null {
  return i.id === "kernel_headers" && isIssue(i) ? "mistgate-node awg prepare-kernel --yes" : null;
}

/** One step of "Manual ▾": what to do, the exact command to copy, or the node's tab where it is done (with its button). */
export type ManualStep = { text: MessageKey; params?: Params; command?: string; open?: { tab: "profiles"; label: MessageKey } };

/**
 * The steps for a problem the panel cannot fix itself, by check (empty: the explanation above says it all). The
 * commands are read-only or the documented one-liner: the owner runs them as root on the node.
 */
export function manualSteps(i: DoctorItem): ManualStep[] {
  const p = i.params;
  switch (i.id) {
    case "time_sync":
      return [{ text: "hl.manual.time_sync", command: "timedatectl set-ntp true" }];
    case "port_conflicts": {
      const steps: ManualStep[] = [];
      const flags = p.network === "tcp" ? "-ltpn" : "-lupn";
      if (p.port) steps.push({ text: "hl.manual.port_who", params: { port: p.port }, command: `ss ${flags} 'sport = :${p.port}'` });
      else if (p.hop_from && p.hop_to)
        steps.push({ text: "hl.manual.port_who", params: { port: `${p.hop_from}-${p.hop_to}` }, command: `ss ${flags} 'sport >= :${p.hop_from} and sport <= :${p.hop_to}'` });
      steps.push({ text: "hl.manual.port_move", open: { tab: "profiles", label: "hl.manual.port_moveBtn" } });
      return steps;
    }
    case "foreign_nft":
      return [{ text: "hl.manual.foreign_nft", command: "nft list ruleset" }];
    case "dstate_tasks":
      return [{ text: "hl.manual.reboot" }];
    case "cert_expiry":
      return p.inbound_id ? [{ text: "hl.manual.cert", open: { tab: "profiles", label: "hl.alerts.openProfiles" } }] : [];
  }
  return [];
}
