import { queryOptions, useQuery } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import {
  AlertKind,
  AlertSeverity,
  CheckStatus,
  DoctorStatus,
  type Alert as AlertMsg,
  type CheckBucket as BucketMsg,
  type CheckCell as CellMsg,
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
  // the blind-panel alert is fleet-wide and has no profile to name
  if (a.kind === AlertKind.CHECK_FAILED && a.subject === "panel_egress") key += ".panel_egress";
  if ((a.kind === AlertKind.DOCTOR_WARN || a.kind === AlertKind.DOCTOR_FAIL) && params.check) params.check_title = checkTitle(t, params.check);
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
  return lookup(t, a.whyKey, params) ?? "";
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
  /** Longer form for the tooltip and the screen reader. */
  state: string;
};

export function cellLook(t: T, cell: CheckCell, nodeStatus: NodeStatus): CellLook {
  if (!cell.deployed) return { kind: "none", label: t("hl.cell.none"), state: t("hl.cell.notDeployed") };
  const last = cell.last;
  if (!last || last.status === CheckStatus.UNSPECIFIED) return { kind: "off", label: t("hl.cell.wait"), state: t("hl.cell.noResult") };
  const ms = `${last.latencyMs} ${t("hl.ms")}`;
  const err = checkError(t, last.errorCode);
  switch (last.status) {
    case CheckStatus.OK:
      return { kind: "ok", label: ms, state: ms };
    case CheckStatus.DEGRADED:
      return { kind: "warn", label: ms, state: [ms, err].filter(Boolean).join(" · ") };
    case CheckStatus.FAILED:
      return { kind: "bad", label: t("hl.cell.fail"), state: err || t("hl.cell.fail") };
    default: // skipped on purpose: a profile that failed to start must not look like one switched off
      if (nodeStatus === NodeStatus.UPDATING) return { kind: "busy", label: t("hl.cell.upd"), state: err };
      switch (last.errorCode) {
        case "inbound_failed":
          return { kind: "bad", label: t("hl.cell.failed"), state: err };
        case "inbound_pending":
          return { kind: "busy", label: t("hl.cell.starting"), state: err };
        case "node_offline":
          return { kind: "blip", label: t("hl.cell.offline"), state: err };
        case "client_unsupported":
          return { kind: "off", label: t("hl.cell.na"), state: err };
        default: // inbound_disabled, and inbound_not_active of an older panel
          return { kind: "off", label: t("hl.cell.off"), state: err };
      }
  }
}

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
