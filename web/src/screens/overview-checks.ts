import type { GetChecksResponse } from "@/gen/mistgate/admin/v1/health_pb";
import type { Plain } from "@/lib/plain";
import { sortProtocols } from "@/lib/series";

// The "Checks" cells of a node on the Overview: the panel's own tunnel checks (GetChecks) folded into 24 cells of an hour each.

type Checks = Plain<GetChecksResponse>;
type Row = Checks["rows"][number];

export type TickState = "ok" | "warn" | "bad" | "none";
export type Tick = {
  state: TickState;
  /** The hour the cell stands for, unix seconds (0 when the node has no history at all). */
  from: number;
  to: number;
  ok: number;
  failed: number;
  /** The time of the rounds that came up, averaged by how many came up; 0 when none did. */
  ms: number;
};

const HOUR = 3600;
const HALF = 1800;

/**
 * The last 24 hours of one node, oldest first, from the half-hour buckets of all of its deployed profiles (48 of each). An
 * hour with no round is "none"; one whose rounds all failed is "bad"; one with some failed is "warn"; else "ok".
 */
export function nodeTicks(row: Pick<Row, "cells"> | undefined): Tick[] {
  const cells = (row?.cells ?? []).filter((c) => c.deployed && c.history.length > 0);
  const end = cells.reduce((e, c) => Math.max(e, c.history[c.history.length - 1]!.startUnix + HALF), 0);
  const ticks: Tick[] = [];
  for (let h = 0; h < 24; h++) {
    let ok = 0;
    let failed = 0;
    let weighted = 0;
    for (const c of cells) {
      // the two buckets of hour h: the newest bucket of the cell is the last of hour 23
      for (const k of [2 * h, 2 * h + 1]) {
        const b = c.history[c.history.length - 48 + k];
        if (!b) continue;
        ok += b.ok;
        failed += b.failed;
        weighted += b.latencyMs * b.ok;
      }
    }
    const state: TickState = ok + failed === 0 ? "none" : failed === 0 ? "ok" : ok === 0 ? "bad" : "warn";
    const from = end ? end - (24 - h) * HOUR : 0;
    ticks.push({ state, from, to: from ? from + HOUR : 0, ok, failed, ms: ok ? Math.round(weighted / ok) : 0 });
  }
  return ticks;
}

/** The protocols a node runs, from its deployed profiles, in the order of the tags everywhere; empty when the node has none. */
export function nodeProtocols(row: Pick<Row, "cells"> | undefined, columns: Checks["columns"]): string[] {
  const out: string[] = [];
  row?.cells.forEach((c, i) => {
    const p = columns[i]?.protocol;
    if (c.deployed && p) out.push(p);
  });
  return sortProtocols(out);
}
