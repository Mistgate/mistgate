import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { countNodes, foldView, healthKind, problemCount, useFleetHealth } from "./fleet";

let overview: Record<string, unknown> = {};
vi.mock("@/lib/api", () => ({
  fleet: { overview: () => Promise.resolve(overview) },
  users: { listUsers: () => Promise.resolve({ counts: { all: 3 } }) },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const card = (status: NodeStatus, code?: string, params: Record<string, string> = {}) => ({ status, reason: code ? { code, params } : undefined });

describe("the one number of problems", () => {
  it("counts problem nodes, the broken among them, the waiting and the connected ones", () => {
    const cards = [
      card(NodeStatus.ONLINE),
      card(NodeStatus.ONLINE, "no_profiles"),
      card(NodeStatus.ONLINE, "inbound_failed", { failed: "1", total: "2" }),
      card(NodeStatus.BLIP, "host_blip"),
      card(NodeStatus.PENDING, "enrollment_pending"),
      card(NodeStatus.ONLINE, "clock_skew"),
    ];
    expect(countNodes(cards)).toEqual({ problems: 2, broken: 1, pending: 1, connected: 5 });
  });
  it("is the larger of the alerts and the problem nodes, so one fault is not counted twice", () => {
    expect(problemCount({ problems: 2, alerts: 0 })).toBe(2);
    expect(problemCount({ problems: 1, alerts: 3 })).toBe(3);
  });
  it("is red only for something broken or critical; a node without profiles alone is amber", () => {
    expect(healthKind(0, 1, 0)).toBe("warn");
    expect(healthKind(1, 1, 0)).toBe("bad");
    expect(healthKind(0, 2, 1)).toBe("bad");
    expect(healthKind(0, 0, 0)).toBe("ok");
  });
  it("folds the rest of the nodes into a tile that is green only when every one of them works", () => {
    expect(foldView([card(NodeStatus.ONLINE), card(NodeStatus.ONLINE)])).toEqual({ kind: "ok", problems: 0, pending: 0 });
    expect(foldView([card(NodeStatus.ONLINE), card(NodeStatus.PENDING, "enrollment_expired")])).toEqual({ kind: "off", problems: 0, pending: 1 });
    expect(foldView([card(NodeStatus.ONLINE), card(NodeStatus.BLIP, "host_blip")])).toMatchObject({ kind: "off", problems: 0 });
    expect(foldView([card(NodeStatus.ONLINE, "no_profiles"), card(NodeStatus.PENDING, "enrollment_pending")])).toEqual({ kind: "warn", problems: 1, pending: 1 });
    expect(foldView([card(NodeStatus.DOWN, "agent_silent"), card(NodeStatus.ONLINE, "no_profiles")])).toMatchObject({ kind: "bad", problems: 2 });
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

function Pill() {
  const h = useFleetHealth();
  return <span data-kind={h.kind} data-to={h.to}>{h.label}</span>;
}

async function pill(data: Record<string, unknown>) {
  overview = { nodesTotal: 0, nodes: [], alertsActive: 0, alertsCritical: 0, events: [], traffic: [], online: [], topConsumers: [], ...data };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><Pill /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
  const el = host.querySelector("span")!;
  const out = { kind: el.dataset.kind, to: el.dataset.to, label: el.textContent };
  act(() => root?.unmount());
  host.remove();
  root = host = null;
  return out;
}

describe("the header pill", () => {
  it("is grey and says so while the only node waits for its install, never 'healthy'", async () => {
    expect(await pill({ nodesTotal: 1, nodes: [card(NodeStatus.PENDING, "enrollment_pending")] })).toEqual({ kind: "off", to: "/", label: "1 node is waiting for install" });
  });
  it("names the problems with one word and number, and leads to Health only when there are alerts to read", async () => {
    expect(await pill({ nodesTotal: 2, nodes: [card(NodeStatus.ONLINE), card(NodeStatus.ONLINE, "no_profiles")] })).toEqual({ kind: "warn", to: "/", label: "1 problem" });
    expect(await pill({ nodesTotal: 1, nodes: [card(NodeStatus.DOWN, "agent_silent")], alertsActive: 2, alertsCritical: 1 })).toEqual({ kind: "bad", to: "/health", label: "2 problems" });
  });
  it("is green when everything that is connected works", async () => {
    expect(await pill({ nodesTotal: 2, nodes: [card(NodeStatus.ONLINE), card(NodeStatus.PENDING, "enrollment_pending")] })).toEqual({ kind: "ok", to: "/", label: "Fleet is healthy" });
  });
});
