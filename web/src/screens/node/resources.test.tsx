import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import type { NodeMetrics } from "@/gen/mistgate/admin/v1/node_pb";
import { setLang } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { LoadHistory } from "./load-history";
import { ResourcesCard } from "./resources";

// The node's Resources card and load history: what took the machine, and the hours behind it. An agent that predates the
// resource fields gets a request to update it, never zeros.

const nodes = vi.hoisted(() => ({ getNodeHostHistory: vi.fn() }));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), nodes }));
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
  nodes.getNodeHostHistory.mockReset();
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

const metrics: Plain<NodeMetrics> = {
  cpuPct: 60, softirqPct: 10, load1: 1.2, ramUsedBytes: 2 * 2 ** 30, ramTotalBytes: 4 * 2 ** 30, diskUsedBytes: 0, diskTotalBytes: 0, netRxBps: 120e6, netTxBps: 40e6,
  atUnix: 1, stealPct: 5, iowaitPct: 3, swapUsedBytes: 64 << 20, swapTotalBytes: 1 << 30, conntrackCount: 900, conntrackMax: 1000, tcpSockets: 140, udpSockets: 12,
  nicSpeedMbps: 1000, agent: { cpuPct: 20, rssBytes: 256 << 20, goTotalBytes: 200 << 20, goHeapBytes: 90 << 20, goroutines: 420, threads: 14, openFds: 310, fdLimit: 65536 },
};

describe("the Resources card", () => {
  it("splits the CPU and the memory and shows the meters", async () => {
    await mount(<ResourcesCard metrics={metrics} cpus={2} />);
    const text = host!.textContent!;
    expect(text).toContain("60% busy · 2 vCPU");
    for (const part of ["Mistgate20%", "Kernel networking10%", "Steal5%", "Everything else25%"]) expect(text).toContain(part);
    expect(text).toContain("Waiting for the disk: 3%");
    expect(text).toContain("256 MB"); // the agent's resident memory
    expect(text).toContain("Swap: 64 MB of 1 GB");
    expect(text).toContain("900 of 1,000"); // connection tracking
    expect(text).toContain("310 of 65,536"); // open files
    expect(text).toContain("TCP 140 · UDP 12");
    expect(text).toContain("420 goroutines");
    expect(text).toContain("120 Mbit/s of 1,000 Mbit/s"); // the busier direction against the link
    // connection tracking at 90% is a warning meter
    const meter = host!.querySelector("[aria-label='Connection tracking: 90%'] span");
    expect(meter?.className).toContain("bg-warn");
  });

  it("asks for an agent update instead of zeros", async () => {
    await mount(<ResourcesCard metrics={{ ...metrics, agent: undefined, stealPct: 0, conntrackMax: 0 }} cpus={2} />);
    const text = host!.textContent!;
    expect(text).toContain("Update the agent to see");
    expect(text).not.toContain("0%");
    expect(text).not.toContain("Connection tracking");
  });

  it("is not drawn without readings, and leaves out what is not known", async () => {
    await mount(<ResourcesCard metrics={undefined} cpus={2} />);
    expect(host!.textContent).toBe("");
    act(() => root!.unmount());
    host!.remove();
    await mount(<ResourcesCard metrics={{ ...metrics, nicSpeedMbps: 0, conntrackMax: 0, conntrackCount: 0 }} cpus={0} />);
    const text = host!.textContent!;
    expect(text).not.toContain("Link");
    expect(text).toContain("Not loaded");
    expect(text).not.toContain("vCPU");
  });
});

describe("the load history", () => {
  const now = 1_800_000_000;
  const newest = now - (now % 3600);
  const hours = (ext: boolean) =>
    Array.from({ length: 24 }, (_, i) => ({
      hourStartUnix: BigInt(newest - (23 - i) * 3600), samples: 360, cpuAvgPct: 10 + i, cpuMaxPct: 30 + i, sessionsAvg: i, sessionsMax: i + 2,
      extSamples: ext ? 360 : 0, stealAvgPct: ext ? 2 : 0, stealMaxPct: ext ? 4 : 0, agentCpuAvgPct: ext ? 1 : 0, agentCpuMaxPct: ext ? 3 : 0,
    }));
  const radios = () => [...document.querySelectorAll<HTMLElement>("[role=radiogroup][aria-label='What to show'] [role=radio]")].map((r) => r.textContent);

  it("draws the busiest hour and offers steal and Mistgate only when an updated agent reported them", async () => {
    nodes.getNodeHostHistory.mockResolvedValue({ nodeId: "nod_1", nodeName: "de1", nowUnix: BigInt(now), sinceUnix: 0n, hours: hours(true) });
    await mount(<LoadHistory nodeId="nod_1" />);
    expect(nodes.getNodeHostHistory).toHaveBeenCalledWith({ nodeId: "nod_1", hours: 24 }, expect.anything());
    expect(host!.textContent).toContain("Load history");
    expect(host!.textContent).toContain("33%"); // the busiest hour's mean, 10 + 23
    expect(radios()).toEqual(["CPU", "People", "Steal", "Mistgate"]);

    act(() => root!.unmount());
    host!.remove();
    nodes.getNodeHostHistory.mockResolvedValue({ nodeId: "nod_1", nodeName: "de1", nowUnix: BigInt(now), sinceUnix: 0n, hours: hours(false) });
    await mount(<LoadHistory nodeId="nod_1" />);
    expect(radios()).toEqual(["CPU", "People"]);
  });

  it("is not drawn on the Cloudflare edition or without hours", async () => {
    nodes.getNodeHostHistory.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount(<LoadHistory nodeId="nod_1" />);
    expect(host!.textContent).toBe("");
    act(() => root!.unmount());
    host!.remove();
    nodes.getNodeHostHistory.mockResolvedValue({ nodeId: "nod_1", nodeName: "de1", nowUnix: BigInt(now), sinceUnix: 0n, hours: [] });
    await mount(<LoadHistory nodeId="nod_1" />);
    expect(host!.textContent).toBe("");
  });
});
