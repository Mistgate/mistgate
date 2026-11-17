import { CheckStatus, type GetChecksResponse } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { cellMs } from "@/lib/health";
import type { Plain } from "@/lib/plain";

// What the profile screens know of a server (a profile on a node), from the panel's own checks (GetChecks): the list's chips, the
// matrix and the page's "Servers on nodes" read the same cells, so a server looks the same everywhere. The words are in
// i18n/profilespage.ts.

export type Checks = Plain<GetChecksResponse>;
type CheckCell = Checks["rows"][number]["cells"][number];

/** The shape of a state mark: ok, half (part), crossed (bad), spinning (busy), empty ring (none). */
export type Gk = "ok" | "part" | "bad" | "busy" | "none";
/** A server's state as the board names it: works, the checks fail, did not start, starting, off, and its node is out of touch. */
export type CellKind = "ok" | "part" | "bad" | "busy" | "off" | "gone";
export type Hist = "ok" | "part" | "bad" | "none";

export type PCell = {
  profileId: string;
  nodeId: string;
  nodeName: string;
  countryCode: string;
  inboundId: string;
  kind: CellKind;
  gl: Gk;
  /** The median answer of the hour, ms; 0 = no check yet or none that came up. */
  ms: number;
  /** Rounds in a row that failed (the board's "2 rounds"). */
  streak: number;
  /** A round has run: the cell has a number to show. */
  checked: boolean;
  /** The last 48 half-hours, oldest first. */
  hist: Hist[];
  /** The cell the panel last saw: its code ("timeout") and the short fact behind it. */
  errorCode: string;
  errorDetail: string;
  /** The text of the profile's own warning for this node ("did not start"), when it has one. */
  failed: string;
  atUnix: number;
};

const glOf: Record<CellKind, Gk> = { ok: "ok", part: "part", bad: "bad", busy: "busy", off: "none", gone: "none" };

const histOf = (cell: CheckCell): Hist[] =>
  cell.history.map((b) => (b.ok > 0 && b.failed === 0 ? "ok" : b.failed > 0 && b.ok > 0 ? "part" : b.failed > 0 ? "bad" : "none"));

/**
 * One server as a cell: the node out of touch first, then a start that failed (the profile's warning names the node), then what the
 * last round said. A cell with no round yet is a working one that has not been looked at ("works, no check").
 */
export function pcellOf(profileId: string, row: Checks["rows"][number], cell: CheckCell, failedOn: ReadonlyMap<string, string>): PCell {
  const last = cell.last;
  const failed = failedOn.get(row.nodeName) ?? "";
  const base = { profileId, nodeId: row.nodeId, nodeName: row.nodeName, countryCode: row.countryCode, inboundId: cell.inboundId, streak: cell.failStreak, hist: histOf(cell), errorCode: last?.errorCode ?? "", errorDetail: last?.errorDetail ?? "", failed, atUnix: last?.atUnix ?? 0 };
  const done = (kind: CellKind, ms = 0, checked = false): PCell => ({ ...base, kind, gl: glOf[kind], ms, checked });
  if (row.nodeStatus === NodeStatus.DOWN || last?.errorCode === "node_offline") return done("gone");
  if (failed) return done("bad");
  if (row.nodeStatus === NodeStatus.UPDATING) return done("busy");
  if (!last || last.status === CheckStatus.UNSPECIFIED) return done("ok");
  if (last.status === CheckStatus.SKIPPED) return done(last.errorCode === "inbound_not_active" ? "busy" : "off");
  if (last.status === CheckStatus.FAILED) return done(cell.failStreak >= 1 ? "part" : "ok", cellMs(cell).ms, true);
  return done("ok", cellMs(cell).ms, true);
}

/** The server warnings of a profile by node name: "Did not start on fi1" says which node (the list's amber mark has the words). */
export const failedNodes = (p: Pick<ProfileSummary, "warnings">): Map<string, string> =>
  new Map(p.warnings.filter((w) => w.code === "inbound_failed" && w.params.node).map((w) => [w.params.node!, w.code]));

/** Every profile's servers, by profile id, nodes in the order the checks give them (by name). */
export function cellsByProfile(checks: Checks | undefined, profiles: readonly ProfileSummary[]): Map<string, PCell[]> {
  const out = new Map<string, PCell[]>();
  if (!checks) return out;
  const by = new Map(profiles.map((p) => [p.id, failedNodes(p)]));
  checks.columns.forEach((col, ci) => {
    const list: PCell[] = [];
    for (const row of checks.rows) {
      const cell = row.cells[ci];
      if (cell?.deployed) list.push(pcellOf(col.profileId, row, cell, by.get(col.profileId) ?? new Map()));
    }
    out.set(col.profileId, [...(out.get(col.profileId) ?? []), ...list]);
  });
  return out;
}

export type PState = "none" | "off" | "bad" | "part" | "busy" | "nogr" | "ok";

/**
 * The profile's state as the page says it ("Works", "Partly · 2 of 3", "Not on any node", "In no group"): from its servers and
 * whether a group carries it. `on` counts the servers that are not switched off, `up` the ones that work.
 */
export function stateOf(cells: readonly PCell[], inGroups: number, deployed = cells.length): { kind: PState; gl: Gk; on: number; up: number } {
  const on = cells.filter((c) => c.kind !== "off");
  const bad = on.filter((c) => c.kind === "bad" || c.kind === "gone");
  const starting = on.filter((c) => c.kind === "busy");
  const up = on.length - bad.length;
  const done = (kind: PState, gl: Gk) => ({ kind, gl, on: on.length, up });
  if (deployed === 0) return done("none", "none");
  if (on.length === 0) return done("off", "none");
  if (bad.length === on.length) return done("bad", "bad");
  if (bad.length > 0) return done("part", "part");
  if (starting.length > 0) return done("busy", "busy");
  if (inGroups === 0) return done("nogr", "part");
  return done("ok", "ok");
}

/** The worst glyph of a profile's cells (what a count of remarks takes its colour from). */
export const worstOf = (gls: readonly Gk[]): "" | "part" | "bad" => (gls.includes("bad") ? "bad" : gls.includes("part") ? "part" : "");
