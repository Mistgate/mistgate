import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeDoctorTab } from "@/screens/node/doctor";

const getDoctor = vi.fn();
const runDoctor = vi.fn();
vi.mock("@/lib/api", () => ({
  health: { getDoctor: (...a: unknown[]) => getDoctor(...a), runDoctor: (...a: unknown[]) => runDoctor(...a), applyFix: vi.fn() },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
  fleet: {},
  nodes: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// no router here: a link is an anchor that says where it goes
vi.mock("@tanstack/react-router", async (orig) => {
  const { createElement } = await import("react");
  return {
    ...(await orig<typeof import("@tanstack/react-router")>()),
    Link: (p: { to: string; params?: { id: string }; search?: { tab: string }; hash?: string; className?: string; children?: unknown }) =>
      createElement("a", { className: p.className, href: `${p.to.replace("$id", p.params?.id ?? "")}?tab=${p.search?.tab}#${p.hash}` }, p.children as never),
  };
});

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getDoctor.mockReset();
  runDoctor.mockReset();
});

const warpItem = (over: Record<string, unknown>) => ({
  id: "warp_path",
  status: DoctorStatus.FAIL,
  titleKey: "doctor.warp_path.title",
  detail: "down: probe_cloudflare_failed; ladder: reassert",
  detailCode: "warp_path.down",
  params: { state: "down", backend: "kernel", colo: "ARN", error: "probe_cloudflare_failed; ladder: reassert" },
  whyKey: "health.doctor.warp_path.why",
  fixId: "",
  measuredUnix: 1n,
  ...over,
});
const report = (items: unknown[]) => ({
  nowUnix: 1n,
  nodes: [{ nodeId: "nod_1", nodeName: "de1", nodeStatus: NodeStatus.ONLINE, agentSupported: true, hasReport: true, receivedUnix: 1n, ageS: 30, stale: false, items }],
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><NodeDoctorTab nodeId="nod_1" nodeName="de1" /></QueryClientProvider>));
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);

describe("warp_path in the doctor", () => {
  it("words the reason like the WARP card does, and never shows the raw code", async () => {
    getDoctor.mockResolvedValue(report([warpItem({ fixId: "reconnect_warp" })]));
    await mount();
    expect(text()).toContain("WARP is down: The Cloudflare probe failed through WARP. Trying to recover: reconnecting.");
    expect(text()).not.toContain("probe_cloudflare_failed");
    // the explanation above it no longer claims that every WARP profile is dead
    expect(text()).toContain("if it is only slow they work slowly");
  });

  it("offers real actions instead of a grey 'Manual action' chip: open the WARP card, check again", async () => {
    getDoctor.mockResolvedValue(report([warpItem({ fixId: "reconnect_warp" })]));
    runDoctor.mockResolvedValue({ nowUnix: 2n, nodes: [] });
    await mount();
    expect(text()).not.toContain("Manual action");
    expect(button("Reconnect WARP")).toBeDefined();
    const open = [...document.querySelectorAll("a")].find((a) => a.textContent === "Open WARP");
    expect(open?.getAttribute("href")).toBe("/nodes/nod_1?tab=settings#warp");
    await act(async () => void button("Check again")?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(runDoctor).toHaveBeenCalledWith({ nodeId: "nod_1" });
  });

  it("an unknown code stays readable as it is", async () => {
    getDoctor.mockResolvedValue(report([warpItem({ params: { state: "down", error: "brand_new_failure" } })]));
    await mount();
    expect(text()).toContain("WARP is down: brand_new_failure");
  });
});
