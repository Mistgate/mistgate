import { describe, expect, it } from "vitest";
import { nodeProtocols, nodeTicks } from "./overview-checks";

// The 24 cells of a node's checks: an hour is empty, clean, partly failed or all failed.

const END = 1_790_000_000 - (1_790_000_000 % 1800); // the start of the newest half hour
const cell = (deployed: boolean, buckets: (k: number) => { ok: number; failed: number; latencyMs?: number }) => ({
  deployed,
  inboundId: "",
  failStreak: 0,
  history: Array.from({ length: 48 }, (_, k) => ({ startUnix: END - (47 - k) * 1800, latencyMs: 40, ...buckets(k) })),
});

describe("the checks of a node", () => {
  it("fold the half hours of every deployed profile into 24 hours, oldest first", () => {
    const row = {
      cells: [
        cell(true, (k) => (k === 10 || k === 11 ? { ok: 0, failed: 3 } : k === 20 ? { ok: 2, failed: 1 } : k < 4 ? { ok: 0, failed: 0 } : { ok: 6, failed: 0, latencyMs: 30 + k })),
        cell(false, () => ({ ok: 9, failed: 9 })), // not deployed: not counted
      ],
    };
    const ticks = nodeTicks(row as never);
    expect(ticks).toHaveLength(24);
    expect(ticks.map((t) => t.state).slice(0, 3)).toEqual(["none", "none", "ok"]); // the first four half hours had no round
    expect(ticks[5]).toMatchObject({ state: "bad", ok: 0, failed: 6 });
    expect(ticks[10]).toMatchObject({ state: "warn", ok: 8, failed: 1 }); // bucket 20 lost one of three, 21 is clean
    expect(ticks[23]!.state).toBe("ok");
    // the hours count back from the end of the newest bucket
    expect(ticks[23]).toMatchObject({ from: END + 1800 - 3600, to: END + 1800 });
    expect(ticks[22]!.from).toBe(ticks[23]!.from - 3600);
    // the time of an hour is the average of its rounds that came up
    expect(ticks[12]!.ms).toBe(Math.round((6 * (30 + 24) + 6 * (30 + 25)) / 12));
  });

  it("are all empty for a node with no profile", () => {
    expect(nodeTicks(undefined).every((t) => t.state === "none" && t.from === 0)).toBe(true);
    expect(nodeTicks({ cells: [cell(false, () => ({ ok: 1, failed: 0 }))] } as never).every((t) => t.state === "none")).toBe(true);
  });

  it("name the protocols of the deployed profiles, once each, in the order of the tags", () => {
    const columns = [{ protocol: "amneziawg" }, { protocol: "hysteria2" }, { protocol: "vless" }, { protocol: "hysteria2" }] as never;
    const row = { cells: [{ deployed: true }, { deployed: true }, { deployed: false }, { deployed: true }] };
    expect(nodeProtocols(row as never, columns)).toEqual(["hysteria2", "amneziawg"]);
    expect(nodeProtocols(undefined, columns)).toEqual([]);
  });
});
