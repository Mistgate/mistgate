import { describe, expect, it } from "vitest";
import { dictionaries, fill, langs } from "@/i18n";
import { en } from "@/i18n/en";
import type { HostHour, NodeMetrics } from "@/gen/mistgate/admin/v1/node_pb";
import type { Plain } from "./plain";
import { bottleneckKey, conntrackFill, cpuSplit, fdFill, fillTone, fitKey, hourSeries, linkUse, loadStatusKind, memSplit, recKey } from "./resources";

type M = Plain<NodeMetrics>;
const base: M = {
  cpuPct: 60, softirqPct: 10, load1: 1, ramUsedBytes: 1000, ramTotalBytes: 4000, diskUsedBytes: 0, diskTotalBytes: 0, netRxBps: 50e6, netTxBps: 20e6, atUnix: 1,
  stealPct: 5, iowaitPct: 2, swapUsedBytes: 10, swapTotalBytes: 100, conntrackCount: 900, conntrackMax: 1000, tcpSockets: 1, udpSockets: 1, nicSpeedMbps: 1000,
  agent: { cpuPct: 20, rssBytes: 400, goTotalBytes: 0, goHeapBytes: 0, goroutines: 1, threads: 1, openFds: 81, fdLimit: 100 },
};
const old: M = { ...base, agent: undefined, stealPct: 0, conntrackCount: 0, conntrackMax: 0, nicSpeedMbps: 0 };

describe("the machine's CPU and memory, split", () => {
  it("adds up to the busy share: Mistgate, softirq, steal, everything else", () => {
    expect(cpuSplit(base)).toEqual({ agent: 20, softirq: 10, steal: 5, other: 25, busy: 60, iowait: 2 });
    // the agent's share never pushes the parts past the busy share (its ticks and softirq can overlap)
    const s = cpuSplit({ ...base, cpuPct: 30, agent: { ...base.agent!, cpuPct: 50 } })!;
    expect(s.agent + s.softirq + s.steal + s.other).toBeCloseTo(30);
    expect(s.other).toBe(0);
    // garbage is clamped, never NaN
    const g = cpuSplit({ ...base, cpuPct: Number.NaN, stealPct: 500 })!;
    expect(Object.values(g).every(Number.isFinite)).toBe(true);
  });
  it("says nothing for an agent that predates the fields", () => {
    expect(cpuSplit(old)).toBeNull();
    expect(memSplit(old)).toBeNull();
    expect(conntrackFill(old)).toBeNull();
    expect(fdFill(old)).toBeNull();
    expect(linkUse(old)).toBeNull();
    expect(cpuSplit(undefined)).toBeNull();
  });
  it("reads memory, swap, conntrack, open files and the link", () => {
    expect(memSplit(base)).toEqual({ agent: 10, other: 15, used: 25, swap: 10 });
    expect(memSplit({ ...base, swapTotalBytes: 0 })!.swap).toBeNull();
    expect(conntrackFill(base)).toBe(90);
    expect(conntrackFill({ ...base, conntrackMax: 0 })).toBeNull(); // not loaded
    expect(fdFill(base)).toBe(81);
    expect(fdFill({ ...base, agent: { ...base.agent!, fdLimit: 0 } })).toBeNull();
    expect(linkUse(base)).toBe(5); // the busier direction, 50 of 1000 Mbit/s
    expect([fillTone(79), fillTone(80), fillTone(95)]).toEqual(["ok", "warn", "bad"]);
  });
});

describe("the node's hours as series", () => {
  const now = 1_800_000_000 - (1_800_000_000 % 3600) + 1200;
  const hour = (back: number, x: Partial<Plain<HostHour>>): Plain<HostHour> =>
    ({ hourStartUnix: now - (now % 3600) - back * 3600, samples: 360, cpuAvgPct: 10, cpuMaxPct: 20, sessionsAvg: 4, sessionsMax: 7, extSamples: 0, stealAvgPct: 0, stealMaxPct: 0, agentCpuAvgPct: 0, agentCpuMaxPct: 0, ...x }) as Plain<HostHour>;
  it("lays the hours on a dense axis ending now, a missing hour as zero with no samples", () => {
    const s = hourSeries([hour(0, { cpuAvgPct: 30 }), hour(2, { cpuAvgPct: 12, cpuMaxPct: 50 }), hour(99, {})], "cpu", 24, now);
    expect(s.times.length).toBe(24);
    expect(s.times[23]).toBe(now - (now % 3600));
    expect(s.mean[23]).toBe(30);
    expect([s.mean[21], s.peak[21]]).toEqual([12, 50]);
    expect([s.samples[22], s.mean[22]]).toEqual([0, 0]);
    expect(s.ext).toBe(false);
  });
  it("reads steal and Mistgate's CPU only from hours that carried them", () => {
    const rows = [hour(0, { extSamples: 360, stealAvgPct: 3, stealMaxPct: 9, agentCpuAvgPct: 2, agentCpuMaxPct: 6 }), hour(1, { stealAvgPct: 77 })];
    const steal = hourSeries(rows, "steal", 24, now);
    expect(steal.ext).toBe(true);
    expect([steal.mean[23], steal.peak[23], steal.mean[22]]).toEqual([3, 9, 0]);
    const agent = hourSeries(rows, "agent", 24, now);
    expect([agent.mean[23], agent.peak[23]]).toEqual([2, 6]);
    expect(hourSeries(rows, "people", 24, now).peak[23]).toBe(7);
  });
});

describe("the fleet's load in words", () => {
  // The recommendation codes and the params the panel sends with each (internal/panel/capacity: Rec* and recommend()).
  const sent: Record<string, Record<string, string>> = {
    update_agent: {},
    steal_high: { steal_pct: "31" },
    conntrack_near_limit: { fill_pct: "90", limit: "262144" },
    cpu_busy: { cpu_pct: "86", peak_pct: "99", target: "nl1", target_headroom: "80" },
    cpu_busy_add_node: { cpu_pct: "86", peak_pct: "99" },
    bandwidth_busy: { peak_pct: "92", link_mbps: "1000" },
    ram_high: { ram_pct: "96" },
    swap_in_use: { swap_mb: "300" },
    disk_filling: { disk_pct: "85" },
    agent_memory_growing: { growth_pct_day: "12", rss_mb: "640", days: "5" },
  };
  it("has a sentence in every language for every code, every placeholder filled", () => {
    const cases = [...Object.entries(sent), ["bandwidth_busy", { peak_pct: "92", link_mbps: "1000", target: "nl1", target_headroom: "40" }] as const];
    for (const [code, params] of cases) {
      const key = recKey({ code, params: { ...params } });
      expect(key, code).not.toBeNull();
      for (const lang of langs) {
        const text = fill(dictionaries[lang][key!], params);
        expect(text, `${lang} ${code}`).toBeTruthy();
        expect(text, `${lang} ${code}`).not.toMatch(/[{}]|undefined/);
      }
    }
    expect(recKey({ code: "bandwidth_busy", params: { target: "nl1" } })).toBe("load.rec.bandwidth_busy_to");
    expect(recKey({ code: "a_newer_code", params: {} })).toBeNull();
    // no wording for a code the panel never sends
    const worded = Object.keys(en).filter((k) => k.startsWith("load.rec.")).map((k) => k.slice("load.rec.".length));
    expect(worded.sort()).toEqual([...Object.keys(sent), "bandwidth_busy_to"].sort());
  });
  it("words the bottleneck, the status and why a fit is missing", () => {
    for (const b of ["cpu", "steal", "bandwidth", "ram", "conntrack", "disk"]) expect(bottleneckKey(b), b).toBe(`load.res.${b}`);
    expect(bottleneckKey("")).toBeNull();
    expect(bottleneckKey("gpu")).toBeNull();
    expect([loadStatusKind("ok"), loadStatusKind("busy"), loadStatusKind("overloaded"), loadStatusKind("unknown"), loadStatusKind("")]).toEqual(["ok", "warn", "bad", "off", "off"]);
    expect(fitKey("ok")).toBeNull();
    expect(fitKey("flat")).toBe("load.fit.flat");
    expect(fitKey("something_new")).toBe("load.fit.few_hours");
  });
});
