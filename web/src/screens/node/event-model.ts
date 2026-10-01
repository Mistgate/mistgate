import type { StatusKind } from "@/components/ui/status";
import { EventSeverity, type Event } from "@/gen/mistgate/admin/v1/fleet_pb";
import { eventKind } from "@/lib/events";
import type { Plain } from "@/lib/plain";

// The node's event list as the owner reads it: one line per thing that happened. The raw rows stay the truth (they are
// the "details" of a line); this file only decides which rows belong together. Pure, so the folding is unit-tested.

export type NodeEvent = Plain<Event>;

export type EventFilter = "all" | "problems" | "profiles" | "agent";
export const eventFilters: readonly EventFilter[] = ["all", "problems", "profiles", "agent"];

/** Profile starts that come this long after the thing that caused them belong to it (an agent comes up in seconds). */
export const foldWindowS = 120;

/**
 * The rows of one agent update come over this long: the start of the new build, its commit after the 5-minute window
 * (update_committed) and the panel's verdict after its gate (update_step_passed); a rollback likewise.
 */
export const updateWindowS = 15 * 60;

export type LineKind =
  | "agent_started"
  | "update"
  | "rollback"
  | "profile_added"
  | "profile_removed"
  | "profiles_restarted"
  | "profiles_started"
  | "event";

export type EventLine = {
  /** Id of the line's first row (the cause, else the oldest of the group): a stable React key and paging cursor. */
  id: number;
  timeUnix: number;
  kind: LineKind;
  /** The row that gives the line its wording; for a group of profile starts the first of them. */
  head: NodeEvent;
  status: StatusKind;
  /** Rows folded into the head, oldest first (profile starts, the configuration apply that followed). A "profiles
   *  started" group has no cause row: its head is the first start, and the starts, head included, are the children. */
  children: NodeEvent[];
  /** Profile names of the folded starts / restarts, in order, without repeats. */
  profiles: string[];
  /** Counts of what happened to the profiles: how many started and how many restarted. */
  started: number;
  restarted: number;
};

const profileCodes = new Set([
  "profile_added",
  "profile_removed",
  "profiles_restarted",
  "awg_backend_unavailable",
  "hop_failed",
  "hop_rejected",
  "credential_expired",
]);

/**
 * "profiles" for everything about a profile on this node, "agent" for the rest (the agent, the host, the link to the
 * panel). The server filters by the same split (ListEventsRequest.family, internal/panel/fleet/admin_fleet.go).
 */
export const eventFamily = (code: string): "profiles" | "agent" => (code.startsWith("engine_") || profileCodes.has(code) ? "profiles" : "agent");

export const isProblem = (e: Pick<NodeEvent, "severity">) => e.severity === EventSeverity.WARNING || e.severity === EventSeverity.ERROR;

export function matchesFilter(e: NodeEvent, f: EventFilter): boolean {
  switch (f) {
    case "problems":
      return isProblem(e);
    case "profiles":
      return eventFamily(e.code) === "profiles";
    case "agent":
      return eventFamily(e.code) === "agent";
    default:
      return true;
  }
}

const profileName = (e: NodeEvent) => e.profileName || e.params.profile || "";
const isStart = (e: NodeEvent) => e.code === "engine_started" || e.code === "engine_restarted";

/** Worst of two kinds, so a folded failure is not hidden behind its cause. */
const rank: Record<StatusKind, number> = { ok: 0, off: 0, blip: 1, busy: 1, warn: 2, bad: 3 };
const worse = (a: StatusKind, b: StatusKind) => (rank[b] > rank[a] ? b : a);

/** Colour of the head of an agent line: an update that went through is fine, a plain restart is a short interruption. */
function headKind(e: NodeEvent): StatusKind {
  if (e.code === "agent_started") return e.params.reason === "update" ? "ok" : "blip";
  if (e.code === "profile_added" || e.code === "profile_removed" || e.code === "profiles_restarted") return "ok";
  if (e.code === "update_step_passed") return "ok";
  if (e.code === "update_step_failed") return "bad";
  if (e.code === "update_step_rolled_back") return "warn";
  return eventKind(e);
}

type Open = { line: EventLine; until: number };
/** An update or rollback line open for its other rows: they name the same target version. */
type OpenVersion = Open & { version: string };

/** The version an update row is about: the new build an agent started, committed or the panel let through. */
const targetVersion = (e: NodeEvent) => (e.code === "agent_started" ? e.params.version : e.params.to_version) ?? "";

/**
 * Lines for the events of a node, newest first. `events` is in any order. Rows are filtered first, then folded:
 *  - profile starts after agent_started (reason agent_start, or no reason on a row older than the field) fold into it;
 *  - a start with reason profile_added folds into the profile_added of the same profile, restart_command into the
 *    profiles_restarted (of one profile, or of all of them), within foldWindowS;
 *  - starts nothing explains (older rows) that follow each other within the window become one "profiles started" line;
 *  - the full configuration apply right after an agent start folds into the agent line;
 *  - one update is one line: the start of the new build, its commit and the panel's verdict (updateWindowS); one rollback
 *    is one line too: the agent's and the panel's row, and the old build starting again.
 */
export function buildLines(events: readonly NodeEvent[], filter: EventFilter = "all"): EventLine[] {
  const rows = events.filter((e) => matchesFilter(e, filter)).sort((a, b) => a.timeUnix - b.timeUnix || a.id - b.id);
  const lines: EventLine[] = [];
  let agent: Open | null = null;
  let restart: Open | null = null;
  const added = new Map<string, Open>(); // by inbound id
  let orphan: Open | null = null;
  let update: OpenVersion | null = null;
  let rollback: OpenVersion | null = null;

  const add = (head: NodeEvent, kind: LineKind): EventLine => {
    const line: EventLine = { id: head.id, timeUnix: head.timeUnix, kind, head, status: headKind(head), children: [], profiles: [], started: 0, restarted: 0 };
    lines.push(line);
    return line;
  };
  const within = (o: Open | null | undefined, t: number): o is Open => !!o && t <= o.until;
  const fold = (line: EventLine, e: NodeEvent) => {
    line.children.push(e);
    line.status = worse(line.status, isProblem(e) ? eventKind(e) : "ok");
    if (isStart(e)) {
      if (e.code === "engine_started") line.started++;
      else line.restarted++;
      const name = profileName(e);
      if (name && !line.profiles.includes(name)) line.profiles.push(name);
    }
  };

  for (const e of rows) {
    const t = e.timeUnix;
    switch (e.code) {
      case "agent_started":
        if (e.params.reason === "update" && within(rollback, t) && e.params.prev_version === rollback.version) {
          // the old build starting again is the end of the rollback, not "updated 0.3.1 -> 0.3.0"
          fold(rollback.line, e);
          agent = { line: rollback.line, until: t + foldWindowS };
          continue;
        }
        agent = { line: add(e, "agent_started"), until: t + foldWindowS };
        update = e.params.reason === "update" ? { line: agent.line, until: t + updateWindowS, version: targetVersion(e) } : null;
        continue;
      case "update_committed":
      case "update_step_passed":
        if (within(update, t) && targetVersion(e) === update.version) fold(update.line, e);
        else update = { line: add(e, "update"), until: t + updateWindowS, version: targetVersion(e) };
        continue;
      case "update_rolled_back":
      case "update_step_rolled_back":
        update = null;
        if (within(rollback, t) && targetVersion(e) === rollback.version) fold(rollback.line, e);
        else rollback = { line: add(e, "rollback"), until: t + updateWindowS, version: targetVersion(e) };
        continue;
      case "profile_added":
        added.set(e.inboundId, { line: add(e, "profile_added"), until: t + foldWindowS });
        continue;
      case "profile_removed":
        add(e, "profile_removed");
        continue;
      case "profiles_restarted":
        restart = { line: add(e, "profiles_restarted"), until: t + foldWindowS };
        continue;
    }
    if (isStart(e)) {
      const reason = e.params.reason ?? "";
      let target: Open | null = null;
      if ((reason === "agent_start" || reason === "") && within(agent, t) && e.code === "engine_started") target = agent;
      else if (reason === "profile_added" && within(added.get(e.inboundId), t)) target = added.get(e.inboundId)!;
      else if ((reason === "restart_command" || reason === "") && e.code === "engine_restarted" && within(restart, t)) {
        // a restart of one profile belongs to the press that named it; a press on all of them has no inbound
        if (!restart.line.head.inboundId || restart.line.head.inboundId === e.inboundId) target = restart;
      }
      if (target) {
        fold(target.line, e);
        continue;
      }
      // nothing explains it: starts that follow each other are one line, not a wall of identical rows
      if (within(orphan, t) && orphan.line.head.code === e.code) {
        fold(orphan.line, e);
        continue;
      }
      const line = add(e, "profiles_started");
      fold(line, e); // here the head is itself one of the starts, so it is among the children too
      orphan = { line, until: t + foldWindowS };
      continue;
    }
    if (e.code === "state_applied" && within(agent, t) && e.params.revision !== undefined) {
      fold(agent.line, e);
      continue;
    }
    add(e, "event");
  }
  return lines.sort((a, b) => b.timeUnix - a.timeUnix || b.id - a.id);
}

/** Every row behind a line, oldest first: what the "details" of the line list. */
export const lineRows = (l: EventLine): NodeEvent[] => (l.kind === "profiles_started" ? l.children : [l.head, ...l.children]);

/** Local calendar day of a line, for the day separators ("2026-09-28"). */
export function dayKey(unix: number): string {
  const d = new Date(unix * 1000);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** Splits lines (newest first) into days, keeping their order. */
export function byDay(lines: readonly EventLine[]): { day: string; unix: number; lines: EventLine[] }[] {
  const out: { day: string; unix: number; lines: EventLine[] }[] = [];
  for (const l of lines) {
    const day = dayKey(l.timeUnix);
    const last = out.at(-1);
    if (last && last.day === day) last.lines.push(l);
    else out.push({ day, unix: l.timeUnix, lines: [l] });
  }
  return out;
}
