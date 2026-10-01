import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { PrepareAwgOutcome } from "@/gen/mistgate/admin/v1/node_pb";
import { ProfilesTab } from "./profiles";

const listProfiles = vi.fn();
const createInbound = vi.fn();
const updateInbound = vi.fn();
const deleteInbound = vi.fn();
const createProfile = vi.fn();
const updateProfile = vi.fn();
const updateNode = vi.fn();
const prepareAwgKernel = vi.fn();
const restartInbounds = vi.fn();
const getDoctor = vi.fn();
const listGroups = vi.fn();
const updateGroup = vi.fn();
vi.mock("@/lib/api", () => ({
  profiles: {
    listProfiles: (...a: unknown[]) => listProfiles(...a),
    createInbound: (...a: unknown[]) => createInbound(...a),
    updateInbound: (...a: unknown[]) => updateInbound(...a),
    deleteInbound: (...a: unknown[]) => deleteInbound(...a),
    createProfile: (...a: unknown[]) => createProfile(...a),
    updateProfile: (...a: unknown[]) => updateProfile(...a),
  },
  nodes: {
    updateNode: (...a: unknown[]) => updateNode(...a),
    prepareAwgKernel: (...a: unknown[]) => prepareAwgKernel(...a),
    restartInbounds: (...a: unknown[]) => restartInbounds(...a),
  },
  groups: { listGroups: (...a: unknown[]) => listGroups(...a), updateGroup: (...a: unknown[]) => updateGroup(...a) },
  health: { getDoctor: (...a: unknown[]) => getDoctor(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  fleet: {},
  users: {},
  assetUrl: (p: string) => p,
  isUnauthenticated: () => false,
}));
// no router in the test: a link is an anchor that carries its target
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, hash, className, onClick }: { children?: ReactNode; to: string; params?: object; search?: object; hash?: string; className?: string; onClick?: () => void }) => (
    <a href={to + (hash ? `#${hash}` : "")} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className} onClick={onClick}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  updateNode.mockResolvedValue({});
  createInbound.mockResolvedValue({});
  updateInbound.mockResolvedValue({});
  deleteInbound.mockResolvedValue({});
  restartInbounds.mockResolvedValue({ restarted: 1 });
  listGroups.mockResolvedValue({ groups: [] });
  updateGroup.mockResolvedValue({});
  getDoctor.mockResolvedValue({ nowUnix: 1n, nodes: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listProfiles, createInbound, updateInbound, deleteInbound, createProfile, updateProfile, updateNode, prepareAwgKernel, restartInbounds, getDoctor, listGroups, updateGroup]) m.mockReset();
});

const node = (over: Record<string, unknown> = {}) => ({
  id: "nod_1",
  name: "de1",
  address: "de1.example.com",
  status: NodeStatus.ONLINE,
  awgBackend: "auto",
  online: [],
  ...over,
});
const data = (over: Record<string, unknown> = {}) =>
  ({ node: node(), inbounds: [], facts: { virt: "kvm" }, ...over }) as never;

async function mount(d: never, props: { addProfile?: string; onAddClosed?: () => void } = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProfilesTab data={d} {...props} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
/** The check before the click waits 300 ms after the last change. */
const checked = async () => {
  await act(async () => void (await new Promise((r) => setTimeout(r, 350))));
  await settle();
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.textContent?.trim() === `+ ${label}`);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.trim() === label);
const submit = () => click(document.querySelector("[role=dialog] button[type=submit]"));
const input = (label: string) => [...document.querySelectorAll<HTMLInputElement>("[role=dialog] input")].find((i) => i.labels?.[0]?.textContent === label);
async function type(el: HTMLInputElement | undefined, value: string) {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(el, value);
    el!.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
/** The real calls, not the checks before the click. */
const adds = () => createInbound.mock.calls.filter(([r]) => !(r as { validateOnly?: boolean }).validateOnly);
const refuse = (msg: string, code = Code.InvalidArgument) => new ConnectError(msg, code);

const doctorWith = (detailCode: string, params: Record<string, string> = {}) => ({
  nowUnix: 1n,
  nodes: [
    {
      nodeId: "nod_1",
      nodeName: "de1",
      nodeStatus: NodeStatus.ONLINE,
      agentSupported: true,
      hasReport: true,
      receivedUnix: 1n,
      ageS: 1,
      stale: false,
      items: [{ id: "kernel_headers", status: DoctorStatus.OK, titleKey: "", detail: "", params, whyKey: "", fixId: "", measuredUnix: 1n, detailCode }],
    },
  ],
});

describe("adding a profile to a node", () => {
  it("asks which AmneziaWG backend to use, right in the dialog, when the profile is AmneziaWG", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_awg", name: "Amnezia 3.1 test", protocol: "awg" }] });
    getDoctor.mockResolvedValue(doctorWith("kernel_headers.no_awg"));
    await mount(data());
    await click(button("Add profile"));
    await settle();

    expect(text()).toContain("AmneziaWG on this node");
    expect([...document.querySelectorAll("[role=radio]")].map((r) => r.textContent?.trim())).toEqual(["Auto", "Kernel module", "Userspace"]);
    expect(text()).toContain("nothing yet"); // what runs now: no AmneziaWG on the node
    expect(text()).toContain("Not checked yet"); // the doctor skips the kernel check until the node serves AmneziaWG
    expect(text()).not.toContain("mistgate-node awg prepare-kernel");
    // no domain for a protocol without TLS
    expect(input("Domain (SNI)")).toBeUndefined();

    await click(radio("Kernel module"));
    expect(text()).toContain("mistgate-node awg prepare-kernel"); // the command that makes the kernel mode possible

    await submit();
    await settle();
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "kernel" });
    expect(adds()).toHaveLength(1);
    const add = createInbound.mock.calls.findIndex(([r]) => !(r as { validateOnly?: boolean }).validateOnly);
    expect(updateNode.mock.invocationCallOrder[0]).toBeLessThan(createInbound.mock.invocationCallOrder[add]!); // the backend first
  });

  it("does not touch the node's backend when it was left as it is, and says what runs now and whether the kernel is ready", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_awg", name: "Amnezia 3.1 test", protocol: "awg" }] });
    getDoctor.mockResolvedValue(doctorWith("kernel_headers.userspace", { missing: "headers,dkms" }));
    const inbounds = [{ id: "inb_9", profileId: "prf_other", profileName: "other", protocol: "awg", state: InboundState.ACTIVE, port: 51820, awg: { backend: "userspace", backendVersion: "amneziawg-go v3.1", peers: 3, peersOnline: 1 } }];
    await mount(data({ inbounds }));
    await click(button("Add profile"));
    await settle();

    expect(text()).toContain("userspace · amneziawg-go v3.1");
    expect(text()).toContain("Not ready: missing headers, dkms");
    await submit();
    await settle();
    expect(updateNode).not.toHaveBeenCalled();
    expect(adds()).toHaveLength(1);
  });

  // A node whose agent builds the module itself: the dialog never saves "kernel" for a module that is not there, and never
  // adds the profile with a mode that would stop it.
  const capable = (over: Record<string, unknown> = {}) =>
    data({ node: node({ awgPrepare: { supported: true, state: 0, sinceUnix: 0, reasonCode: "", reason: "" }, ...over }) });

  it("on a node that prepares the module itself, choosing the kernel with no module ready adds nothing and says what to do", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_awg", name: "Amnezia 3.1 test", protocol: "awg" }] });
    getDoctor.mockResolvedValue(doctorWith("kernel_headers.userspace", { missing: "headers,dkms" }));
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.NEEDS_PREPARE });
    await mount(capable());
    await click(button("Add profile"));
    await settle();
    await click(radio("Kernel module"));
    expect(text()).not.toContain("Run the command first"); // the command is not what this node needs
    expect(text()).toContain("Prepare it first in the node’s settings");
    await submit();
    await settle();
    expect(prepareAwgKernel).toHaveBeenCalledWith({ nodeId: "nod_1", confirm: false });
    expect(updateNode).not.toHaveBeenCalled();
    expect(adds()).toHaveLength(0);
    // the refusal stays in the open dialog, above its buttons
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toContain("The kernel module is not ready on this node");
  });

  it("on a node that prepares the module itself, the kernel is saved with the profile when the module is ready", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_awg", name: "Amnezia 3.1 test", protocol: "awg" }] });
    getDoctor.mockResolvedValue(doctorWith("kernel_headers.ready"));
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.READY });
    await mount(capable());
    await click(button("Add profile"));
    await settle();
    await click(radio("Kernel module"));
    await submit();
    await settle();
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "kernel" });
    expect(adds()).toHaveLength(1);
  });

  it("opens with the profile the profile page sent us for, and tells when the dialog closes", async () => {
    listProfiles.mockResolvedValue({
      profiles: [
        { id: "prf_a", name: "first", protocol: "hysteria2" },
        { id: "prf_awg", name: "Amnezia 3.1 test", protocol: "awg" },
      ],
    });
    getDoctor.mockResolvedValue(doctorWith("kernel_headers.userspace"));
    const onAddClosed = vi.fn();
    await mount(data(), { addProfile: "prf_awg", onAddClosed });
    await settle();
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    expect(text()).toContain("AmneziaWG on this node"); // the second profile is the chosen one, not the first in the list

    await submit();
    await settle();
    expect(createInbound).toHaveBeenCalledWith(expect.objectContaining({ profileId: "prf_awg", nodeId: "nod_1" }));
    expect(onAddClosed).toHaveBeenCalled();
  });

  it("names the field Domain (SNI), says what it is, and shows the name the node will really use", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_hy", name: "test", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt · WARP" }] });
    createInbound.mockResolvedValue({ inbound: { port: 443, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [], freePort: 8443 });
    await mount(data());
    await click(button("Add profile"));
    await settle();
    expect(text()).not.toContain("AmneziaWG on this node");
    expect(getDoctor).not.toHaveBeenCalled();
    // the line that tells profiles apart, in words
    expect(text()).toContain("Hysteria2 · UDP 443 · Salamander · Let's Encrypt · exit via WARP");
    const sni = input("Domain (SNI)");
    expect(sni?.placeholder).toBe("de1.example.com");
    expect(text()).toContain("The server name in the certificate, the one the app sees");
    expect(input("Port")?.placeholder).toBe("443 — as in the profile");
    // the check before the click asked, and wrote nothing
    expect(createInbound).toHaveBeenCalledWith(expect.objectContaining({ profileId: "prf_hy", nodeId: "nod_1", validateOnly: true }), expect.anything());
    expect(adds()).toHaveLength(0);
  });
});

describe("the check before the click", () => {
  const hy = (over: Record<string, unknown> = {}) => ({ id: "prf_hy", name: "Main", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 0, version: 3, ...over });
  const ipNode = data({ node: node({ name: "ip1", address: "203.0.113.10" }) });

  it("says before the click that Let's Encrypt needs a domain on an IP node, and offers the self-signed certificate honestly", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy()] });
    createInbound.mockImplementation((r: { validateOnly?: boolean; tlsServerNameOverride: string }) =>
      r.tlsServerNameOverride ? Promise.resolve({ inbound: { port: 443, tlsServerName: r.tlsServerNameOverride }, warnings: [] }) : Promise.reject(refuse("acme_needs_domain: address=203.0.113.10&node=ip1")),
    );
    updateProfile.mockResolvedValue({});
    await mount(ipNode);
    await click(button("Add profile"));
    await settle();

    expect(text()).toContain("Let’s Encrypt will not issue a certificate for an IP");
    expect(text()).toContain("The address of ip1 is the IP 203.0.113.10");
    expect(document.activeElement).toBe(input("Domain (SNI)")); // the place where it gets fixed
    expect(text()).toContain("Happ is not verified yet: check it on your phone");
    expect((document.querySelector("[role=dialog] button[type=submit]") as HTMLButtonElement).disabled).toBe(true);

    // the profile runs nowhere yet: it can switch itself
    await click(button("Switch “Main” to self-signed"));
    await settle();
    expect(updateProfile).toHaveBeenCalledWith({ profileId: "prf_hy", settingsJson: JSON.stringify({ tls_mode: "self_signed" }), expectedVersion: 3 });

    // or a domain: the check passes and the button works
    await type(input("Domain (SNI)"), "vpn.example.com");
    await checked();
    expect(text()).not.toContain("will not issue a certificate");
    await submit();
    await settle();
    expect(adds()[0]?.[0]).toMatchObject({ profileId: "prf_hy", tlsServerNameOverride: "vpn.example.com" });
  });

  it("on a profile that already runs elsewhere, offers a self-signed profile for this node instead of switching it", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy({ nodeCount: 2 })] });
    createInbound.mockRejectedValue(refuse("acme_needs_domain: address=203.0.113.10&node=ip1"));
    await mount(ipNode);
    await click(button("Add profile"));
    await settle();
    expect(button("Switch “Main” to self-signed")).toBeUndefined();
    expect(text()).toContain("“Main” stays with Let’s Encrypt on the nodes it already runs on.");
    await click(button("Create a self-signed profile for ip1"));
    await settle();
    // the maker opens on the self-signed certificate, said honestly, and the way back
    expect(text()).toContain("Happ is not verified yet");
    expect(radio("Self-signed")?.getAttribute("aria-checked")).toBe("true");
    expect(button("Create Hysteria2 · 443 and put it on ip1")).toBeDefined();
    await click(button("Back"));
    expect(text()).toContain("Let’s Encrypt will not issue a certificate for an IP");
  });

  it("fills in a free port when the profile's own is taken, and says why; the owner can type his own", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy({ name: "Second" })] });
    createInbound.mockImplementation((r: { validateOnly?: boolean; portOverride: number }) =>
      r.portOverride === 0
        ? Promise.reject(refuse("port_taken: port=443&profile=Main&node=de1&free=8443", Code.AlreadyExists))
        : r.portOverride === 2053
          ? Promise.reject(refuse("port_taken: port=2053&profile=Other&node=de1&free=4443", Code.AlreadyExists))
          : Promise.resolve({ inbound: { port: r.portOverride }, warnings: [] }),
    );
    await mount(data());
    await click(button("Add profile"));
    await settle();
    await checked();
    expect(input("Port")?.value).toBe("8443");
    expect(text()).toContain("443 is taken by “Main”: the free port 8443 is filled in. You can type your own.");

    await type(input("Port"), "2053");
    await checked();
    expect(text()).toContain("Port 2053 on de1 is already taken by the profile “Other”.");
    expect((document.querySelector("[role=dialog] button[type=submit]") as HTMLButtonElement).disabled).toBe(true);
    await click(button("Take 4443"));
    await checked();
    await submit();
    await settle();
    expect(adds()[0]?.[0]).toMatchObject({ portOverride: 4443 });
  });

  it("warns, without stopping, that a WARP exit has no WARP on this node, with the way to it", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy({ summary: "UDP 8443 · Salamander · Let's Encrypt · WARP" })] });
    createInbound.mockResolvedValue({ inbound: { port: 8443, nodeName: "de1" }, warnings: [{ code: "warp_missing", params: { state: "none" } }] });
    await mount(data());
    await click(button("Add profile"));
    await settle();
    expect(text()).toContain("de1 has no WARP — a profile with a WARP exit will not pass traffic there.");
    expect(document.querySelector('a[href="/nodes/$id#warp"]')?.textContent).toBe("Set up WARP on de1");
    expect((document.querySelector("[role=dialog] button[type=submit]") as HTMLButtonElement).disabled).toBe(false);
  });

  it("says when the node's WARP is there but down by its last report", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy({ summary: "UDP 8443 · Salamander · Let's Encrypt · WARP" })] });
    createInbound.mockResolvedValue({ inbound: { port: 8443, nodeName: "de1" }, warnings: [{ code: "warp_missing", params: { state: "down" } }] });
    await mount(data());
    await click(button("Add profile"));
    await settle();
    expect(text()).toContain("WARP on de1 does not work now");
    expect(document.querySelector('a[href="/nodes/$id#warp"]')?.textContent).toBe("Open WARP on de1");
  });

  it("keeps the dialog open with the reason when the add itself fails", async () => {
    listProfiles.mockResolvedValue({ profiles: [hy()] });
    createInbound.mockImplementation((r: { validateOnly?: boolean }) => (r.validateOnly ? Promise.resolve({ inbound: { port: 443 }, warnings: [] }) : Promise.reject(refuse("node_retired", Code.FailedPrecondition))));
    await mount(data());
    await click(button("Add profile"));
    await settle();
    await submit();
    await settle();
    expect(document.querySelector("[role=dialog]")).not.toBeNull();
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toContain("The node is retired from the fleet.");
  });
});

describe("a node with no profile and a panel with none", () => {
  it("says what the empty node means and puts the main button there", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_x", name: "x", protocol: "hysteria2" }] });
    await mount(data());
    expect(text()).toContain("No profiles on de1 yet — users will not get it");
    expect(button("Add profile")?.className).toContain("bg-accent"); // the main action
  });

  it("makes a Hysteria2 profile and puts it on the node in one go, into «Все» while it has no Hysteria2", async () => {
    listProfiles.mockResolvedValue({ profiles: [] });
    listGroups.mockResolvedValue({ groups: [{ id: "grp_all", name: "Все", profileIds: [], userCount: 4 }] });
    createProfile.mockResolvedValue({ profile: { id: "prf_new", version: 1 } });
    await mount(data({ node: node({ name: "ip1", address: "203.0.113.10" }) }));
    await click(button("Add profile"));
    await settle();
    expect(text()).toContain("There are no profiles yet");
    expect(text()).toContain("The address of ip1 is an IP.");
    // Let's Encrypt stays the default: it needs a domain here, so the button waits for one
    expect(button("Create Hysteria2 · 443 and put it on ip1")?.hasAttribute("disabled") || button("Create Hysteria2 · 443 and put it on ip1")?.getAttribute("data-disabled") !== null).toBe(true);
    expect(text()).toContain("“Все” has no Hysteria2 profile yet");
    expect(document.querySelector('button[aria-pressed="true"]')?.textContent).toContain("Все");

    await click(radio("Self-signed"));
    expect(text()).toContain("Happ is not verified yet");
    await click(button("Create Hysteria2 · 443 and put it on ip1"));
    await settle();
    expect(createProfile).toHaveBeenCalledWith({ protocol: "hysteria2", name: "Hysteria2 · 443 · self-signed", settingsJson: JSON.stringify({ tls_mode: "self_signed" }) });
    expect(adds()[0]?.[0]).toMatchObject({ profileId: "prf_new", nodeId: "nod_1" });
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_all", profileIds: { values: ["prf_new"] } });
  });
});

describe("the profiles list by protocol", () => {
  const hy = { id: "inb_1", profileId: "prf_1", profileName: "test", protocol: "hysteria2", state: InboundState.ACTIVE, port: 443, tlsServerName: "example.com", certNotAfterUnix: 1_900_000_000, certPinSha256: "", lastError: "" };
  const awg = {
    id: "inb_2",
    profileId: "prf_2",
    profileName: "Amnezia 3.1 test",
    protocol: "awg",
    state: InboundState.ACTIVE,
    port: 51820,
    tlsServerName: "ignored.example.com",
    egress: "warp",
    lastError: "",
    awg: { backend: "kernel", backendVersion: "amneziawg kernel module", ifaceUp: true, peers: 3, peersOnline: 1, peersHandshaken: 3, newestHandshakeUnix: 0, udpRxPackets: 5 },
  };

  it("shows port, domain and the certificate for a TLS protocol, and the profile's name is a link to it", async () => {
    await mount(data({ inbounds: [hy] }));
    expect(text()).toContain("Domain (SNI)");
    expect(text()).toContain("example.com");
    expect(text()).toContain("Certificate");
    expect(text()).toContain("until");
    expect(text()).not.toContain("Backend");
    expect(document.querySelector('a[href="/profiles/$id"]')?.getAttribute("data-params")).toBe(JSON.stringify({ id: "prf_1" }));
  });

  it("shows port, backend, egress and devices for AmneziaWG, and neither a domain nor a certificate", async () => {
    await mount(data({ inbounds: [awg] }));
    const t = text();
    expect(t).toContain("Backend");
    expect(t).toContain("kernel");
    expect(t).toContain("Egress");
    expect(t).toContain("WARP");
    expect(t).toContain("Devices");
    expect(t).toContain("3 · 1 online");
    expect(t).not.toContain("Domain (SNI)");
    expect(t).not.toContain("Certificate");
    expect(t).not.toContain("ignored.example.com");
  });

  it("marks a certificate that runs out soon, and says 'none' instead of a dash for a profile that is not running", async () => {
    const soon = Math.floor(Date.now() / 1000) + 5 * 86400 - 60;
    await mount(data({ inbounds: [{ ...hy, certNotAfterUnix: soon }, { ...hy, id: "inb_3", profileName: "down", state: InboundState.FAILED, certNotAfterUnix: 0 }] }));
    const cell = [...document.querySelectorAll("span")].find((s) => s.textContent?.includes("5 d left"));
    expect(cell?.className).toContain("text-warn-text");
    expect(text()).toContain("none: the profile is not running");
  });
});

describe("a profile that did not start", () => {
  const failed = {
    id: "inb_8",
    profileId: "prf_8",
    profileName: "hy2 · WARP · 8443",
    protocol: "hysteria2",
    state: InboundState.FAILED,
    port: 8443,
    tlsServerName: "de1.example.com",
    certNotAfterUnix: 0,
    certPinSha256: "",
    lastError: "listen udp :8443: bind: address already in use",
  };

  it("says why in words, names the program the doctor saw on the port, and keeps the agent's text under it", async () => {
    getDoctor.mockResolvedValue({
      nowUnix: 1n,
      nodes: [{ nodeId: "nod_1", items: [{ id: "port_conflicts", status: DoctorStatus.FAIL, params: { inbound_id: "inb_8", port: "8443", network: "udp", process: "xray(812)" } }] }],
    });
    await mount(data({ inbounds: [failed] }));
    await settle();
    expect(text()).toContain("Failed to start");
    expect(text()).toContain("Port udp/8443 is taken by another program (xray)");
    expect(text()).toContain("listen udp :8443: bind: address already in use");
  });

  it("changes the port from the row: the edit opens on a port that is free on the node", async () => {
    updateInbound.mockImplementation((r: { validateOnly?: boolean }) => Promise.resolve(r.validateOnly ? { inbound: { port: 8443 }, warnings: [], freePort: 4443 } : {}));
    await mount(data({ inbounds: [failed] }));
    await click(button("Change port"));
    await settle();
    expect(input("Port")?.value).toBe("4443");
    expect(document.activeElement).toBe(input("Port"));
    expect(text()).toContain("Port 8443 is taken by another program: the free port 4443 is filled in.");
    await submit();
    await settle();
    expect(updateInbound).toHaveBeenCalledWith({ inboundId: "inb_8", portOverride: 4443, tlsServerNameOverride: undefined, enabled: undefined });
  });

  it("restarts only this profile, after saying who notices", async () => {
    await mount(data({ inbounds: [failed] }));
    await click(button("Restart"));
    expect(text()).toContain("Restart “hy2 · WARP · 8443” on de1?");
    expect(text()).toContain("Connections through this profile drop for a couple of seconds");
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Restart"));
    await settle();
    expect(restartInbounds).toHaveBeenCalledWith({ nodeId: "nod_1", inboundId: "inb_8" });
  });

  it("offers no restart while the agent is not on the line", async () => {
    await mount(data({ node: node({ status: NodeStatus.DOWN }), inbounds: [failed] }));
    expect(button("Restart")).toBeUndefined();
  });
});

describe("removing a profile from the node", () => {
  it("says how many users lose the node at the next subscription update", async () => {
    listProfiles.mockResolvedValue({ profiles: [{ id: "prf_1", name: "test", protocol: "hysteria2", userCount: 12 }] });
    await mount(data({ inbounds: [{ id: "inb_1", profileId: "prf_1", profileName: "test", protocol: "hysteria2", state: InboundState.ACTIVE, port: 443, lastError: "" }] }));
    await click(button("Remove"));
    await settle();
    expect(text()).toContain("Remove “test” from de1?");
    expect(text()).toContain("12 users lose de1 at the next subscription update.");
  });

  it("says that AmneziaVPN keys stop and work again when the profile comes back", async () => {
    listProfiles.mockResolvedValue({ profiles: [] });
    await mount(data({ inbounds: [{ id: "inb_2", profileId: "prf_2", profileName: "awg", protocol: "awg", state: InboundState.ACTIVE, port: 51820, lastError: "", awg: { peers: 3, peersOnline: 0 } }] }));
    await click(button("Remove"));
    await settle();
    expect(text()).toContain("3 AmneziaVPN devices on de1 stop working; if you put the profile back on de1, their keys work again.");
  });

  it("keeps the dialog open with the reason when the removal fails", async () => {
    listProfiles.mockResolvedValue({ profiles: [] });
    deleteInbound.mockRejectedValue(new ConnectError("boom", Code.Unavailable));
    await mount(data({ inbounds: [{ id: "inb_1", profileId: "prf_1", profileName: "test", protocol: "hysteria2", state: InboundState.ACTIVE, port: 443, lastError: "" }] }));
    await click(button("Remove"));
    await settle();
    await click([...document.querySelectorAll("[role=dialog] button")].find((b) => b.textContent === "Remove"));
    await settle();
    expect(document.querySelector("[role=dialog] [role=alert]")?.textContent).toContain("Cannot reach the panel");
  });
});
