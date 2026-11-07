import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import { FleetLoad } from "./nodes-load";
import { PanelResourcesCard } from "./settings-pages/panel-resources";

// Nodes -> Load: a row per node from GetFleetLoad, recommendations in place; and Settings -> System, the panel's own resources.

const ops = vi.hoisted(() => ({ getFleetLoad: vi.fn(), getPanelResources: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), ops }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  Object.values(ops).forEach((m) => m.mockReset());
  setLang("en");
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount(ui: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>));
  for (let i = 0; i < 3; i++) await settle();
}
const click = (el: Element) => act(async () => void el.dispatchEvent(new MouseEvent("click", { bubbles: true })));

const load = {
  nowUnix: 1_800_000_000n,
  hours: 168,
  nodes: [
    {
      nodeId: "nod_1", nodeName: "de1", connected: true, cpus: 2, status: "overloaded", bottleneck: "cpu", hoursWithData: 168, extHours: 100,
      busyHourUnix: 1_799_990_000n, busyHourCpuPct: 86, peopleBusyHour: 120, peoplePeak: 140, cpuPeakP95Pct: 99, stealP95Pct: 3, ramPeakPct: 60,
      rxPeakBps: 300e6, txPeakBps: 280e6, capacityMbps: 500, capacitySource: "configured", bandwidthPeakPct: 60, fit: "ok", cpuPctPerHundredPeople: 55,
      mbpsPerPerson: 1.2, headroomPeople: 0, agentCpuAvgPct: 12, agentCpuMaxPct: 30, agentRssMaxBytes: 300n * 1024n * 1024n, agentRssGrowthPctPerDay: 0.4,
      recommendations: [
        { code: "cpu_busy", severity: "crit", params: { cpu_pct: "86", peak_pct: "99", target: "nl1", target_headroom: "80" } },
        { code: "a_code_from_a_newer_panel", severity: "info", params: {} },
      ],
    },
    {
      nodeId: "nod_2", nodeName: "nl1", connected: true, cpus: 4, status: "ok", bottleneck: "", hoursWithData: 168, extHours: 0, busyHourUnix: 1_799_990_000n,
      busyHourCpuPct: 20, peopleBusyHour: 10, peoplePeak: 14, cpuPeakP95Pct: 40, rxPeakBps: 0n, txPeakBps: 0n, capacityMbps: 0, capacitySource: "", fit: "ok",
      cpuPctPerHundredPeople: 50, headroomPeople: 80, recommendations: [{ code: "update_agent", severity: "info", params: {} }],
    },
    {
      nodeId: "nod_3", nodeName: "fi1", connected: false, cpus: 0, status: "unknown", bottleneck: "", hoursWithData: 0, extHours: 0, busyHourUnix: 0n,
      busyHourCpuPct: 0, peopleBusyHour: 0, peoplePeak: 0, cpuPeakP95Pct: 0, rxPeakBps: 0n, txPeakBps: 0n, capacityMbps: 0, capacitySource: "", fit: "few_hours",
      recommendations: [],
    },
  ],
  summary: {
    ok: 1, busy: 0, overloaded: 1, unknown: 1, headroomPeople: 80, headroomNodes: 2,
    mostLoaded: [{ nodeId: "nod_1", nodeName: "de1", status: "overloaded", busyHourCpuPct: 86, headroomPeople: 0 }],
    mostFree: [{ nodeId: "nod_2", nodeName: "nl1", status: "ok", busyHourCpuPct: 20, headroomPeople: 80 }],
  },
};

describe("the Load view", () => {
  it("lists every node with its peak, busy hour, bottleneck, room and status, and opens what to do", async () => {
    ops.getFleetLoad.mockResolvedValue(load);
    await mount(<FleetLoad />);
    expect(ops.getFleetLoad).toHaveBeenCalledWith({ hours: 168 }, expect.anything());
    const text = host!.textContent!;
    for (const s of ["Overloaded1", "OK1", "No data1", "room for about 80 more people", "Most room: nl1 (+80)"]) expect(text).toContain(s);
    const rows = [...host!.querySelectorAll("a")].map((a) => a.textContent);
    expect(rows).toEqual(["de1", "nl1", "fi1"]);
    expect(text).toContain("86%");
    expect(text).toContain("No room"); // de1 is overloaded
    expect(text).toContain("80 more people"); // nl1
    expect(text).toContain("Nothing close"); // nl1 has no bottleneck near its limit

    const open = host!.querySelector<HTMLButtonElement>("button[aria-label='Show what to do about de1']")!;
    expect(open.getAttribute("aria-expanded")).toBe("false");
    await click(open);
    const after = host!.textContent!;
    expect(after).toContain("The busy hour runs at 86% CPU. Move people to nl1: it has room for about 80 more.");
    expect(after).toContain("a_code_from_a_newer_panel"); // a code this page does not know is shown as it is
    expect(after).toContain("55%"); // CPU per 100 people
    expect(after).toContain("60% of your 500 Mbit/s");
    expect(after).toContain("12% mean, 30% peak");
    expect(after).toContain("not known"); // de1's conntrack peak: absent, not zero

    // an old agent says what it lacks; a node without hours says it has none
    await click(host!.querySelector("button[aria-label='Show what to do about nl1']")!);
    expect(host!.textContent).toContain("Update the agent: it does not report steal");
    await click(host!.querySelector("button[aria-label='Show what to do about fi1']")!);
    expect(host!.textContent).toContain("No hour of host load in this period yet");

    // another window asks again
    await click([...host!.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => r.textContent === "30 d")!);
    await settle();
    expect(ops.getFleetLoad).toHaveBeenLastCalledWith({ hours: 720 }, expect.anything());
  });

  it("says what the Cloudflare edition lacks", async () => {
    ops.getFleetLoad.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount(<FleetLoad />);
    expect(host!.textContent).toContain("Not available on the Cloudflare edition");
  });

  it("speaks Russian with three plural forms", async () => {
    setLang("ru");
    ops.getFleetLoad.mockResolvedValue(load);
    await mount(<FleetLoad />);
    expect(host!.textContent).toContain("ещё 80 человек");
    expect(host!.textContent).toContain("Перегружена");
  });
});

describe("the panel's own resources", () => {
  const resources = {
    nowUnix: 1_800_000_000n,
    process: { cpuPct: 1.5, rssBytes: 90n * 1024n * 1024n, goTotalBytes: 60n * 1024n * 1024n, goHeapBytes: 30n * 1024n * 1024n, goroutines: 400, threads: 12, openFds: 80, fdLimit: 65536, uptimeS: 7200n },
    host: { cpus: 2, cpuBusyPct: 8, stealPct: 0.5, iowaitPct: 1, load1: 0.4, ramUsedBytes: 700n * 1024n * 1024n, ramTotalBytes: 2n * 1024n * 1024n * 1024n, swapUsedBytes: 0n, swapTotalBytes: 0n, diskUsedBytes: 5n * 1024n * 1024n * 1024n, diskTotalBytes: 20n * 1024n * 1024n * 1024n },
    database: { fileBytes: 64n * 1024n * 1024n, walBytes: 4n * 1024n * 1024n, pageCount: 16384n, freelistCount: 1638n, pageSize: 4096, measuredUnix: 1_799_999_990n },
    agentsConnected: 2,
    nodes: 3,
    rpc: { windowS: 300, requests: 420n, serverErrors: 1n, clientErrors: 3n, p95Ms: 38 },
  };
  it("shows the process, the server, the database and the admin API", async () => {
    ops.getPanelResources.mockResolvedValue(resources);
    await mount(<PanelResourcesCard />);
    const text = host!.textContent!;
    for (const s of ["Panel resources", "1.5%", "90 MB", "Go heap 30 MB of 60 MB", "80 of 65,536", "400 goroutines, 12 threads", "2 h",
      "700 MB of 2 GB", "No swap", "5 GB of 20 GB", "68 MB", "file 64 MB, write-ahead log 4 MB", "10% of the file is free pages", "2 of 3", "420", "3 refused or invalid", "38 ms"]) {
      expect(text, s).toContain(s);
    }
  });
  it("says when the server does not report its machine, and is not drawn on the Cloudflare edition", async () => {
    ops.getPanelResources.mockResolvedValue({ ...resources, host: { ...resources.host, ramTotalBytes: 0n } });
    await mount(<PanelResourcesCard />);
    expect(host!.textContent).toContain("does not report its CPU, memory and disk");
    act(() => root!.unmount());
    host!.remove();
    ops.getPanelResources.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount(<PanelResourcesCard />);
    expect(host!.textContent).toBe("");
  });
});
