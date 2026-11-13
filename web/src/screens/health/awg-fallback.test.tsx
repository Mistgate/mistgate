import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeDoctorTab } from "@/screens/node/doctor";

const getDoctor = vi.fn();
const updateNode = vi.fn();
vi.mock("@/lib/api", () => ({
  health: { getDoctor: (...a: unknown[]) => getDoctor(...a), runDoctor: vi.fn(), applyFix: vi.fn() },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
  nodes: { updateNode: (...a: unknown[]) => updateNode(...a) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
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
  getDoctor.mockReset();
  updateNode.mockReset();
});

const item = (over: Record<string, unknown>) => ({
  id: "awg_backend",
  status: DoctorStatus.FAIL,
  titleKey: "doctor.awg_backend.title",
  detail: "no AmneziaWG backend",
  detailCode: "awg_backend.unavailable",
  params: { mode: "kernel", reason: "the amneziawg kernel module is not loaded", hint: "no_module" },
  whyKey: "health.doctor.awg_backend.why.no_module",
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

describe("AmneziaWG backend in the doctor: the userspace fallback", () => {
  it("offers 'Switch this node to auto' only on the row the panel marked for an agent that cannot fall back", async () => {
    getDoctor.mockResolvedValue(report([item({ params: { mode: "kernel", reason: "the amneziawg kernel module is not loaded", hint: "no_module", switch_auto: "1" } })]));
    updateNode.mockResolvedValue({});
    await mount();
    const b = button("Switch this node to auto");
    expect(b).toBeDefined();
    await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "auto" });
  });

  it("does not offer it to an agent that can (no mark), nor for another reason", async () => {
    getDoctor.mockResolvedValue(report([item({})]));
    await mount();
    expect(button("Switch this node to auto")).toBeUndefined();
    act(() => root?.unmount());
    host?.remove();
    getDoctor.mockResolvedValue(report([item({ params: { mode: "auto", reason: "open /dev/net/tun: no such file or directory", hint: "no_tun" } })]));
    await mount();
    expect(button("Switch this node to auto")).toBeUndefined();
  });

  it("words the fallback as a warning with the repair button, in plain words", async () => {
    getDoctor.mockResolvedValue(
      report([
        item({
          status: DoctorStatus.WARN,
          detailCode: "awg_backend.fallback",
          params: { backend: "userspace", version: "amneziawg-go v3", mode: "kernel", kernel: "7.0.0-38-generic", reason: "the amneziawg kernel module is not loaded", hint: "fallback" },
          whyKey: "health.doctor.awg_backend.why.fallback",
          fixId: "awg_module_repair",
        }),
      ]),
    );
    await mount();
    expect(text()).toContain("AmneziaWG runs in userspace for now: the kernel module does not load on kernel 7.0.0-38-generic");
    expect(text()).toContain("Devices stay connected");
    expect(button("Switch back to the module")).toBeDefined();
    expect(button("Switch this node to auto")).toBeUndefined();
  });
});
