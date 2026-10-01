import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeDoctorTab } from "@/screens/node/doctor";
import { DoctorTab } from "./doctor";
import { useFixFlow } from "./fix";

const applyFix = vi.fn();
const getDoctor = vi.fn();
const runDoctor = vi.fn();
const acceptDoctorItem = vi.fn();
const unacceptDoctorItem = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  health: {
    applyFix: (...a: unknown[]) => applyFix(...a),
    getDoctor: (...a: unknown[]) => getDoctor(...a),
    runDoctor: (...a: unknown[]) => runDoctor(...a),
    acceptDoctorItem: (...a: unknown[]) => acceptDoctorItem(...a),
    unacceptDoctorItem: (...a: unknown[]) => unacceptDoctorItem(...a),
  },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
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
      createElement("a", { className: p.className, href: `${p.to.replace("$id", p.params?.id ?? "")}${p.search ? `?tab=${p.search.tab}` : ""}${p.hash ? `#${p.hash}` : ""}` }, p.children as never),
  };
});

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const f of [applyFix, getDoctor, runDoctor, acceptDoctorItem, unacceptDoctorItem]) f.mockReset();
});

const item = (over: Record<string, unknown>) => ({
  id: "net_baseline",
  status: DoctorStatus.OK,
  titleKey: "doctor.net_baseline.title",
  detail: "",
  params: {},
  whyKey: "health.doctor.net_baseline.why",
  fixId: "",
  measuredUnix: 1n,
  ...over,
});
const node = (over: Record<string, unknown>) => ({
  nodeId: "nod_1",
  nodeName: "de1",
  nodeStatus: NodeStatus.ONLINE,
  agentSupported: true,
  hasReport: true,
  receivedUnix: 1n,
  ageS: 30,
  stale: false,
  items: [],
  ...over,
});

async function mount(ui: React.ReactElement) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>));
  // the role and the doctor queries resolve
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));

describe("the node's Doctor tab", () => {
  it("says so when the agent is too old for the doctor", async () => {
    getDoctor.mockResolvedValue({ nowUnix: 1n, nodes: [node({ agentSupported: false, hasReport: false })] });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(text()).toContain("agent is too old for the doctor");
    expect(button("Run again")).toBeUndefined();
  });

  it("offers to ask for the first report while there is none", async () => {
    getDoctor.mockResolvedValue({ nowUnix: 1n, nodes: [node({ hasReport: false })] });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(text()).toContain("No report yet");
    expect(button("Run again")).toBeDefined();
  });

  it("groups the checks and runs a fix in two steps: the dry run first, then the confirmed call with its plan id", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({ id: "net_baseline", status: DoctorStatus.WARN, detail: "baseline differs: journald_file", fixId: "apply_baseline", params: { differs: "journald_file" } }),
            item({ id: "ipv6", status: DoctorStatus.OK, titleKey: "doctor.ipv6.title", detail: "no global IPv6 address" }),
          ],
        }),
      ],
    });
    applyFix
      .mockResolvedValueOnce({ plan: { fixId: "apply_baseline", titleKey: "health.fix.apply_baseline.plan", params: { changes: "journald_file" }, detail: "would set: journald_file", disruptive: false }, planId: "pln_1" })
      .mockResolvedValueOnce({ applied: true, affected: 1, resultParams: {} });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);

    expect(text()).toContain("Needs attention");
    expect(text()).toContain("All good");
    expect(text()).toContain("baseline differs: journald_file"); // the agent's fact line

    await click(button("Restore the base settings"));
    expect(applyFix).toHaveBeenCalledTimes(1);
    expect(applyFix.mock.calls[0]![0]).toMatchObject({ nodeId: "nod_1", fixId: "apply_baseline", dryRun: true });
    const dialog = document.querySelector("[role=dialog]")!;
    expect(dialog.textContent).toContain("Set the network and journal baseline again: journald_file.");
    expect(dialog.textContent).toContain("would set: journald_file");

    await click(button("Apply"));
    expect(applyFix).toHaveBeenCalledTimes(2);
    expect(applyFix.mock.calls[1]![0]).toMatchObject({ nodeId: "nod_1", fixId: "apply_baseline", dryRun: false, planId: "pln_1" });
  });

  it("does not offer a fix to a helper (the owner alone changes the host) but still explains the problem", async () => {
    role = Role.HELPER;
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [node({ items: [item({ status: DoctorStatus.WARN, fixId: "apply_baseline", params: { differs: "x" } })] })],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(button("Restore the base settings")).toBeUndefined();
    expect(button("Run again")).toBeDefined();
    expect(text()).toContain("differs from the baseline");
  });

  it("words the node's fact line from its code, and shows the agent's own line for a code it does not know", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({ id: "disk_space", titleKey: "doctor.disk_space.title", detail: "/ 50% used, 4.6 GB free, inodes 21%", detailCode: "disk_space.usage", params: { mount: "/", used_pct: "50", free_mb: "4700", inode_pct: "21" } }),
            item({ id: "ipv6", titleKey: "doctor.ipv6.title", detail: "brand new english line", detailCode: "ipv6.brand_new" }),
            item({ id: "resolver", titleKey: "doctor.resolver.title", detail: "all 3 control domains resolve, median 4 ms" }), // an agent that predates the codes
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(text()).toContain("/: 50% used, 4.6 GB free, inodes 21%");
    expect(text()).not.toContain("/ 50% used, 4.6 GB free");
    expect(text()).toContain("brand new english line");
    expect(text()).toContain("all 3 control domains resolve, median 4 ms");
  });

  it("lists what is fine as a compact list, problems staying on top with their text", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({ id: "time_sync", status: DoctorStatus.WARN, titleKey: "doctor.time_sync.title", whyKey: "health.doctor.time_sync.why", params: { offset_s: "5", ntp_synced: "no" } }),
            item({ id: "ipv6", titleKey: "doctor.ipv6.title", detail: "a" }),
            item({ id: "resolver", titleKey: "doctor.resolver.title", detail: "b" }),
            item({ id: "memory_pressure", titleKey: "doctor.memory_pressure.title", detail: "c" }),
            item({ id: "foreign_nft", status: DoctorStatus.SKIP, titleKey: "doctor.foreign_nft.title", detail: "nft is not installed" }),
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    const lists = [...document.querySelectorAll("ul")];
    expect(lists.map((l) => l.querySelectorAll("li").length)).toEqual([3, 1]); // fine, then not checked
    expect(lists[0]!.textContent).toContain("IPv6");
    expect(lists[0]!.textContent).not.toContain("Clock"); // the problem is not in the compact list
    expect(text()).toContain("The clock is 5 s off the panel"); // and has its full explanation
  });

  it("gives the manual kernel_headers fix as an exact command with a copy button", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({
              id: "kernel_headers",
              status: DoctorStatus.WARN,
              titleKey: "doctor.kernel_headers.title",
              whyKey: "health.doctor.kernel_headers.why",
              detailCode: "kernel_headers.missing",
              params: { kernel: "6.8.0-142-generic", missing: "headers,dkms", mode: "kernel" },
            }),
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(document.querySelector("code")?.textContent).toBe("mistgate-node awg prepare-kernel");
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === "Copy")).toBe(true);
    expect(text()).toContain("Kernel mode is requested, but missing: kernel headers, dkms (kernel 6.8.0-142-generic)");
    expect(text()).not.toContain("Manual action");
  });

  it("says Problem or Attention next to the title of a problem", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [node({ items: [item({ id: "disk_space", status: DoctorStatus.FAIL, titleKey: "doctor.disk_space.title" }), item({ id: "foreign_vpn", status: DoctorStatus.WARN, titleKey: "doctor.foreign_vpn.title" })] })],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    const pills = [...document.querySelectorAll(".pill")].map((p) => p.textContent);
    expect(pills).toEqual(["×Problem", "!Attention"]);
  });

  it("opens the steps by hand of a check the panel cannot fix: the exact command with a copy button", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [node({ items: [item({ id: "time_sync", status: DoctorStatus.WARN, titleKey: "doctor.time_sync.title", whyKey: "health.doctor.time_sync.why", params: { offset_s: "5", ntp_synced: "no" } })] })],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    const manual = button("Manual")!;
    expect(manual.getAttribute("aria-expanded")).toBe("false");
    expect(document.querySelector("code")).toBeNull();
    await click(manual);
    expect(manual.getAttribute("aria-expanded")).toBe("true");
    expect(text()).toContain("Turn on time sync on the server (as root):");
    expect(document.querySelector("code")?.textContent).toBe("timedatectl set-ntp true");
    expect(button("Copy")).toBeDefined();
  });

  it("a port conflict by hand: who holds the port, and the way to the node's profiles to move it", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({
              id: "port_conflicts",
              status: DoctorStatus.FAIL,
              titleKey: "doctor.port_conflicts.title",
              whyKey: "health.doctor.port_conflicts.why",
              detailCode: "port_conflicts.held",
              params: { inbound_id: "inb_1", profile: "hy2 · 8443", network: "udp", port: "8443", process: "caddy(812)" },
            }),
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(text()).toContain("udp/8443 of “hy2 · 8443” is held by caddy(812)"); // the profile by name, not inb_1
    await click(button("Manual"));
    expect(document.querySelector("code")?.textContent).toBe("ss -lupn 'sport = :8443'");
    const move = [...document.querySelectorAll("a")].find((a) => a.textContent === "Change the profile’s port");
    expect(move?.getAttribute("href")).toBe("/nodes/nod_1?tab=profiles");
  });

  it("accepts a warning as normal for the node, and an accepted one says who and when, with the way back", async () => {
    acceptDoctorItem.mockResolvedValue({ doctor: {} });
    unacceptDoctorItem.mockResolvedValue({ doctor: {} });
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({ id: "foreign_vpn", status: DoctorStatus.WARN, titleKey: "doctor.foreign_vpn.title", whyKey: "health.doctor.foreign_vpn.why", params: { names: "unit:x-ui" } }),
            item({ id: "ipv6", status: DoctorStatus.WARN, titleKey: "doctor.ipv6.title", acceptedUnix: 1_790_000_000n, acceptedBy: "adm_1", acceptedByName: "Owner" }),
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(text()).toContain("Needs attention: 1"); // the accepted warning is not a problem any more
    expect(text()).toContain("Accepted as normal");
    expect(text()).toMatch(/Accepted by you on [A-Z][a-z]{2} \d{1,2}\b/);
    await click(button("This is normal for this node"));
    expect(acceptDoctorItem).toHaveBeenCalledWith({ nodeId: "nod_1", checkId: "foreign_vpn" });
    await click(button("Count it again"));
    expect(unacceptDoctorItem).toHaveBeenCalledWith({ nodeId: "nod_1", checkId: "ipv6" });
  });

  it("does not offer to accept a failure, a certificate, or anything to a read-only admin", async () => {
    role = Role.READONLY;
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [node({ items: [item({ id: "foreign_vpn", status: DoctorStatus.WARN, titleKey: "doctor.foreign_vpn.title" })] })],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(button("This is normal for this node")).toBeUndefined();
    act(() => root?.unmount());
    role = Role.OWNER;
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({ id: "disk_space", status: DoctorStatus.FAIL, titleKey: "doctor.disk_space.title" }),
            item({ id: "cert_expiry", status: DoctorStatus.WARN, titleKey: "doctor.cert_expiry.title", params: { inbound_id: "inb_1", reason: "expiring", days_left: "9" } }),
          ],
        }),
      ],
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    expect(button("This is normal for this node")).toBeUndefined();
  });

  it("restarts a profile by name: the question names it, says who drops now, and the node's words wait under Details", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [
        node({
          items: [
            item({
              id: "port_conflicts",
              status: DoctorStatus.FAIL,
              titleKey: "doctor.port_conflicts.title",
              detailCode: "port_conflicts.bind_failed",
              fixId: "restart_inbound",
              params: { inbound_id: "inb_1", profile: "hy2 · WARP · 8443", network: "udp", port: "8443" },
            }),
          ],
        }),
      ],
    });
    applyFix.mockResolvedValueOnce({
      plan: { fixId: "restart_inbound", titleKey: "health.fix.restart_inbound.plan", params: { inbounds: "inb_1", profiles: "hy2 · WARP · 8443", online: "3" }, detail: "would restart inb_1", disruptive: true },
      planId: "pln_2",
    });
    await mount(<NodeDoctorTab nodeId="nod_1" nodeName="de1" />);
    await click(button("Restart “hy2 · WARP · 8443”"));
    expect(applyFix.mock.calls[0]![0]).toMatchObject({ nodeId: "nod_1", fixId: "restart_inbound", params: { inbound_id: "inb_1" }, dryRun: true });
    const dialog = document.querySelector("[role=dialog]")!;
    expect(dialog.textContent).toContain("Restart “hy2 · WARP · 8443” on de1?");
    expect(dialog.textContent).toContain("Connections through this profile (now 3) drop for a couple of seconds and come back by themselves.");
    expect(dialog.querySelector("details summary")?.textContent).toContain("Details");
    expect(dialog.querySelector("details")?.textContent).toContain("would restart inb_1");
    expect(button("Restart")).toBeDefined();
  });
});

function Fleet({ data }: { data: Parameters<typeof DoctorTab>[0]["data"] }) {
  const flow = useFixFlow();
  return (
    <>
      <DoctorTab data={data} flow={flow} />
      {flow.modal}
    </>
  );
}

describe("the fleet Doctor tab", () => {
  it("lists the problems of every node, critical first, and says per node why one has no fresh report", async () => {
    const data = {
      nowUnix: 1,
      nodes: [
        node({ nodeName: "a1", items: [item({ id: "time_sync", status: DoctorStatus.WARN, titleKey: "doctor.time_sync.title", params: { offset_s: "4", ntp_synced: "no" } })] }),
        node({ nodeId: "nod_2", nodeName: "b2", items: [item({ id: "resolver", status: DoctorStatus.FAIL, titleKey: "doctor.resolver.title", params: { failed: "gosuslugi.ru", median_ms: "40" } })] }),
        node({ nodeId: "nod_3", nodeName: "de2", agentSupported: false, hasReport: false, items: [], agentVersion: "0.2.7" }),
        node({ nodeId: "nod_4", nodeName: "nl1", nodeStatus: NodeStatus.DOWN, lastSeenUnix: new Date(2026, 9, 1, 17, 26).getTime() / 1000, items: [item({})] }),
        node({ nodeId: "nod_5", nodeName: "fi1", hasReport: false, items: [] }),
      ],
    };
    await mount(<Fleet data={data as never} />);
    const t = text();
    expect(t.indexOf("Server resolver")).toBeGreaterThan(-1);
    expect(t.indexOf("Server resolver")).toBeLessThan(t.indexOf("Clock")); // FAIL before WARN, whatever the node order
    expect(t).toContain("found: 2");
    const lines = [...document.querySelectorAll("li")].map((li) => li.textContent);
    expect(lines).toContain("de2 — agent 0.2.7 is too old for the doctor · Updates");
    expect(lines.some((l) => /^nl1 — offline since .*17:26, its last report is shown$/.test(l ?? ""))).toBe(true);
    expect(lines).toContain("fi1 — the first report has not arrived yet");
    // every node name is the way to that node's doctor
    const hrefs = [...document.querySelectorAll("a")].map((a) => [a.textContent, a.getAttribute("href")]);
    expect(hrefs).toContainEqual(["de2", "/nodes/nod_3?tab=doctor"]);
    expect(hrefs).toContainEqual(["b2", "/nodes/nod_2?tab=doctor"]);
    expect(hrefs).toContainEqual(["Updates", "/updates"]);
  });

  it("says all is green when nothing is wrong", async () => {
    const data = { nowUnix: 1, nodes: [node({ items: [item({})] })] };
    await mount(<Fleet data={data as never} />);
    expect(text()).toContain("All items are green");
  });
});
