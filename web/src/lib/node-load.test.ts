import { describe, expect, it } from "vitest";
import { nodeLoad } from "./node-load";

const now = 1_800_000_000;
const m = (cpuPct: number, rx: number, tx: number, at = now - 10) => ({ cpuPct, netRxBps: rx, netTxBps: tx, atUnix: at });

describe("nodeLoad", () => {
  it("takes the busier of the network against the capacity and the CPU", () => {
    expect(nodeLoad(m(17, 26e6, 28e6), 100, now)).toEqual({ percent: 28, level: "low" });
    expect(nodeLoad(m(55, 1e6, 1e6), 100, now)).toEqual({ percent: 55, level: "medium" });
    expect(nodeLoad(m(10, 900e6, 10e6), 1000, now)).toEqual({ percent: 90, level: "high" });
  });

  it("uses the CPU alone without a capacity, as the user's page does", () => {
    expect(nodeLoad(m(81, 999e6, 999e6), 0, now)).toEqual({ percent: 81, level: "high" });
  });

  it("clamps to 100 and switches at 50 and 80", () => {
    expect(nodeLoad(m(0, 5e9, 0), 100, now)?.percent).toBe(100);
    expect(nodeLoad(m(49.6, 0, 0), 0, now)?.level).toBe("medium");
    expect(nodeLoad(m(79.4, 0, 0), 0, now)?.level).toBe("medium");
    expect(nodeLoad(m(79.5, 0, 0), 0, now)?.level).toBe("high");
  });

  it("shows nothing without a fresh sample", () => {
    expect(nodeLoad(undefined, 100, now)).toBeNull();
    expect(nodeLoad(m(17, 0, 0, 0), 100, now)).toBeNull();
    expect(nodeLoad(m(17, 0, 0, now - 91), 100, now)).toBeNull();
    expect(nodeLoad(m(17, 0, 0, now - 90), 100, now)).not.toBeNull();
  });
});
