import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { ChecksTab } from "./checks";

vi.mock("@/lib/api", () => ({
  health: { runChecksNow: () => Promise.resolve({ scheduled: 1, skipped: 0 }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  nodes: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

const history = Array.from({ length: 48 }, (_, i) => ({ startUnix: i * 1800, ok: 1, failed: 0, latencyMs: 40 }));
const cell = (status: CheckStatus, errorCode = "", latencyMs = 0) => ({
  deployed: true,
  inboundId: `inb_${errorCode || status}`,
  failStreak: 0,
  history,
  last: { status, atUnix: 100, latencyMs, exitIp: "", exitCountry: "", errorCode, errorDetail: "" },
});
const checks = {
  nowUnix: 200,
  intervalS: 300,
  columns: [
    { profileId: "prf_1", profileName: "hy2 · 443 · Salamander", protocol: "hysteria2" },
    { profileId: "prf_2", profileName: "hy2 · WARP · 8443", protocol: "hysteria2" },
  ],
  rows: [
    { nodeId: "nod_1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.OK, "", 42), cell(CheckStatus.SKIPPED, "inbound_failed")] },
    { nodeId: "nod_2", nodeName: "de2", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.SKIPPED, "inbound_disabled"), cell(CheckStatus.FAILED, "timeout")] },
    { nodeId: "nod_3", nodeName: "nl1", countryCode: "NL", nodeStatus: NodeStatus.DOWN, cells: [cell(CheckStatus.SKIPPED, "node_offline"), { deployed: false, inboundId: "", failStreak: 0, history: [] }] },
  ],
};

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><ChecksTab data={checks as never} /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const cellButton = (aria: string) => [...document.querySelectorAll<HTMLButtonElement>("button[aria-label]")].find((b) => b.getAttribute("aria-label")?.startsWith(aria));

describe("the checks matrix", () => {
  it("shows a profile that failed to start in red, apart from one switched off, and a node without link apart from both", async () => {
    await mount();
    const failed = cellButton("de1, hy2 · WARP · 8443");
    expect(failed?.textContent).toBe("✕ not started");
    expect(failed?.className).toContain("tone-bad");
    const off = cellButton("de2, hy2 · 443 · Salamander");
    expect(off?.textContent).toBe("off");
    expect(off?.className).not.toContain("tone-bad");
    const offline = cellButton("nl1, hy2 · 443 · Salamander");
    expect(offline?.textContent).toBe("no link");
    expect(offline?.className).toContain("tone-blip");
    expect(cellButton("de2, hy2 · WARP · 8443")?.textContent).toBe("✕ fails");
  });

  it("explains its words under the matrix, for a phone that has no hover", async () => {
    await mount();
    const legend = document.body.textContent ?? "";
    for (const words of ["ms — latency", "✕ — fails the check", "✕ not started — the node could not start it", "off — switched off by you", "no link — the node is offline", "not checked — no test client"]) {
      expect(legend).toContain(words);
    }
  });

  it("gives a phone a card per node, the history opening right under the row it belongs to", async () => {
    await mount();
    const rows = [...document.querySelectorAll<HTMLButtonElement>("button[aria-expanded]")];
    expect(rows.map((r) => r.textContent)).toContain("hy2 · WARP · 8443✕ not startedThe node could not start the profile");
    const row = rows.find((r) => r.textContent?.startsWith("hy2 · 443 · Salamander42 ms"))!;
    await act(async () => void row.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(row.getAttribute("aria-expanded")).toBe("true");
    expect(row.nextElementSibling?.textContent).toContain("24 h"); // the history card follows its row
  });
});
