import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useEffect, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { setLang, useT } from "@/i18n";
import { en } from "@/i18n/en";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "./nodes";
import { availability, sshKeyInfo } from "./nodes-bulk-actions";

// The bulk bar of the Nodes page: it shows with the first tick (owner only), counts in words, says what the filter hides,
// clears on Esc, and its quick picks replace the selection. Checks start at once (ports one at a time, the doctor four);
// the SSH actions ask first, go one node at a time, never bring a password to the browser and never send a private key;
// the agent update hands the selected outdated nodes to the Updates page's rollout.

const api = vi.hoisted(() => ({
  checkPorts: vi.fn(),
  runDoctor: vi.fn(),
  listNodeServerAccess: vi.fn(),
  rotateNodeServerPassword: vi.fn(),
  revealNodeServerPassword: vi.fn(),
  addNodeServerKey: vi.fn(),
  removeNodeServerKey: vi.fn(),
  getPanelSSHKey: vi.fn(),
  startRollout: vi.fn(),
  navigate: vi.fn(),
}));
vi.mock("@/lib/api", () => ({
  basepath: "/",
  auth: { me: () => Promise.resolve({}) },
  nodes: { checkPorts: api.checkPorts },
  health: { runDoctor: api.runDoctor },
  provisioning: {
    listNodeServerAccess: api.listNodeServerAccess,
    rotateNodeServerPassword: api.rotateNodeServerPassword,
    revealNodeServerPassword: api.revealNodeServerPassword,
    addNodeServerKey: api.addNodeServerKey,
    removeNodeServerKey: api.removeNodeServerKey,
    getPanelSSHKey: api.getPanelSSHKey,
  },
  updates: { startRollout: api.startRollout },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className }: { children?: ReactNode; to: string; params?: { id: string }; search?: Record<string, string>; className?: string }) => (
    <a href={`${to.replace("$id", params?.id ?? "")}${search ? `?${new URLSearchParams(search)}` : ""}`} className={className}>
      {children}
    </a>
  ),
  useBlocker: () => ({ status: "idle" }),
  useNavigate: () => api.navigate,
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => vi.fn() }));

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
  for (const f of Object.values(api)) f.mockReset();
});

// made-up fleet: three online, lv1 down, msk1 online without profiles
const node = (name: string, over: Record<string, unknown> = {}) => ({
  id: `nod_${name}`,
  name,
  countryCode: "DE",
  location: "",
  provider: "",
  address: `${name}.example.com`,
  status: NodeStatus.ONLINE,
  protocols: ["hysteria2"],
  online: [],
  trafficTodayBytes: 0,
  hasMetrics: false,
  cpuPct: 0,
  uptimeS: 0,
  agentVersion: "0.3.1",
  decoyMode: "builtin",
  decoyReasonCode: "",
  decoyBundleSha256: "",
  decoyPanelAvailable: true,
  decoyOwnAvailable: true,
  ...over,
});
const fleet = [node("de1"), node("nl1"), node("fi1"), node("lv1", { status: NodeStatus.DOWN }), node("msk1", { protocols: [] })];

const panelFp = "SHA256:3kQz7mVb1xN8cR2tY5pL0sW9hD4gF6aJ8uE1oK7iM3n";
const access = (name: string, ip: string, over: Record<string, unknown> = {}) => ({
  nodeId: `nod_${name}`,
  nodeName: name,
  host: ip,
  port: 22,
  username: "root",
  passwordSaved: true,
  passwordLogin: true,
  panelKeyFingerprint: "",
  nodeRetired: false,
  rotationPending: false,
  ...over,
});
// fi1 logs in by the panel key only; lv1 has no saved access
const fleetAccess = [access("de1", "203.0.113.7"), access("nl1", "203.0.113.24", { panelKeyFingerprint: panelFp }), access("fi1", "198.51.100.12", { passwordSaved: false, panelKeyFingerprint: panelFp }), access("msk1", "192.0.2.40")];

const upd = (name: string, state: NodeUpdateState, over: Record<string, unknown> = {}) => ({ nodeId: `nod_${name}`, name, state, supportsUpdate: true, built: 1, version: "0.3.0", onlineUsers: 0, inbounds: 0, scheduledUnix: 0, ...over });
const updates = (over: Record<string, unknown> = {}) => ({
  nodes: [upd("de1", NodeUpdateState.OUTDATED), upd("nl1", NodeUpdateState.OUTDATED), upd("fi1", NodeUpdateState.UP_TO_DATE), upd("lv1", NodeUpdateState.OFFLINE), upd("msk1", NodeUpdateState.UNSUPPORTED, { supportsUpdate: false })],
  bundle: { status: BundleStatus.TRUSTED, version: "0.3.1", built: 2 },
  ...over,
});

const owner = { admin: { id: "adm_1", role: Role.OWNER } };

async function mount({ nodes = fleet, me = owner, ups = updates() }: { nodes?: ReturnType<typeof node>[]; me?: object; ups?: object } = {}) {
  api.listNodeServerAccess.mockResolvedValue({ access: fleetAccess });
  api.getPanelSSHKey.mockResolvedValue({ publicKey: "ssh-ed25519 AAAA mistgate-panel", fingerprint: panelFp });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  qc.setQueryData(updatesQuery.queryKey, ups as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes, expectedAgentVersion: "0.3.1" } as never);
  qc.setQueryData(["node-server-access"], { access: fleetAccess });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodesScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}

const settle = async () => {
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function press(el: Element | null | undefined) {
  expect(el).toBeTruthy();
  await act(async () => {
    for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
  });
  await settle();
}
const tick = (name: string) => document.querySelector<HTMLInputElement>(`input[type=checkbox][aria-label="${en["bulk.select"].replace("{name}", name)}"]`)!;
const tickAll = () => document.querySelector<HTMLInputElement>(`input[aria-label="${en["bulk.selectAll"]}"]`)!;
const bar = () => document.querySelector<HTMLElement>("[data-bulk-bar]");
const barButton = (label: string) => [...(bar()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim().startsWith(label));
const menuItem = (label: string) => [...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent?.startsWith(label));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const row = (name: string) => [...document.querySelectorAll("[data-batch] li")].find((li) => li.querySelector("b")?.textContent === name) as HTMLElement | undefined;
const batchText = () => document.querySelector("[data-batch]")?.textContent ?? "";
async function pick(...names: string[]) {
  for (const n of names) await press(tick(n));
}
async function enter(el: Element | null, value: string) {
  const area = el as HTMLTextAreaElement;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, value);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await settle();
}
/** A call per node that waits for the test. */
function held() {
  const waiting = new Map<string, (v: unknown) => void>();
  const failing = new Map<string, (e: unknown) => void>();
  const fn = ({ nodeId }: { nodeId: string }) =>
    new Promise((resolve, reject) => {
      waiting.set(nodeId, resolve);
      failing.set(nodeId, reject);
    });
  return {
    fn,
    pending: () => [...waiting.keys()],
    answer: async (id: string, value: unknown) => {
      const r = waiting.get(id)!;
      waiting.delete(id);
      await act(async () => r(value));
      await settle();
    },
    refuse: async (id: string, e: unknown) => {
      const r = failing.get(id)!;
      waiting.delete(id);
      await act(async () => r(e));
      await settle();
    },
  };
}

// public keys made for these tests (their private halves were thrown away); the fingerprints are what Go's
// ssh.FingerprintSHA256 prints for them (and ssh-keygen -l)
const goKeys = [
  ["ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIMGoUqi0IPZuTYCruTQPQixYNyZnOIQDf3Vn84RZIwfj alice@laptop", "SHA256:UdqQ+oCd5eVZPdP+SWiLJYNHQeAnWzLOHbE1p25asHU", "ED25519", "alice@laptop"],
  [
    "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC+bYmsOCkfmLqxIcgxkwb+n2j0OcN4JhqeCOVj77yCfiabsiK6BHWl3Jpmjuxh1+8Fr4WqbHF6hQSaPCWX2P+jGfzWywQfTTX47vqq4ls4H7TXSvZDoyHKwDgtwcAJWv8zxNS+H6XCOtscvgIhqqlPQ2gwyWVNrKyGMOoNB8gNT0MpC5YBNgNf4Mv/K8uWXLYB+u2u1ygbbrZhrncPgH5HoGpvABF+AE7WSMHl+w7GYrTlIQq6MSOLOnFY3H0wVHtxwbOQnQGo1IgteDpvXXrbk1a1TRs9rZ+XCcSIO2HMGxjIybJqOKeAGt2Smz0KblVnLJ/knC5bAQyxhiIc6xEl8+nBzVGdpNrYVmGnevhd/H5mrXZLHKQ37sAGadkXcQQ2pXoR6tdIRnLr9g79lxcRlePS2kutYs6fvF103wejl2Tn10YY5Ev3L7EiG/Hll7QQ2K2iJtLkjq8maipPN2MykLD4bQ227sDYxl+zRhDWiKAj9INP1eLIkL4z1GL3h+0= deploy@ci.example.com",
    "SHA256:lxjzVkoVYY5lnEfqumy84dSYGn5/zBLk8cQVUE6e5ys",
    "RSA 3072",
    "deploy@ci.example.com",
  ],
  ["ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBOWTh2eXnQh2OYLI0nefjYeEaPirqB1lBxVY74hklSU1T3PUjFqAQ3znO070bs1kYujWV+O0MjJs0zdmGvh79kA= bob@yubikey", "SHA256:7YouMGHcMPqS3CgOFCsWM325T6zmQh9UfoFOfL7h4CE", "ECDSA 256", "bob@yubikey"],
] as const;

describe("the bar", () => {
  it("shows with the first tick, counts in words, and is the owner's only", async () => {
    await mount();
    expect(bar()).toBeNull();
    await pick("de1");
    expect(bar()?.textContent).toContain("1node selected");
    await pick("nl1");
    expect(bar()?.textContent).toContain("2nodes selected");
    act(() => root?.unmount());
    host?.remove();

    await mount({ me: { admin: { id: "adm_2", role: Role.HELPER } } });
    expect(tick("de1")).toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.trim() === en["bulk.pick"])).toBe(false);
  });

  it("says how much of the selection the filter hides", async () => {
    await mount();
    await pick("de1", "nl1", "lv1");
    await press([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.startsWith(en["nodes.problems"])));
    expect(bar()?.textContent).toContain("3nodes selected");
    expect(bar()?.textContent).toContain("2 hidden by the filter");
  });

  it("clears on Esc, but not while a menu is open", async () => {
    await mount();
    await pick("de1");
    await press(barButton(en["bulk.check"]));
    expect(menuItem(en["bulk.ports"])).toBeTruthy();
    await act(async () => void document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await settle();
    expect(bar()).not.toBeNull();
    await act(async () => void document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await settle();
    expect(document.querySelector("[role=menu]")).toBeNull();
    await act(async () => void document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await settle();
    expect(bar()).toBeNull();
    expect(tick("de1").checked).toBe(false);
  });

  it("a quick pick replaces the selection", async () => {
    await mount();
    await pick("de1", "nl1");
    await press([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === en["bulk.pick"]));
    await press(menuItem(en["bulk.pick.problems"]));
    expect(tick("lv1").checked).toBe(true);
    expect(tick("de1").checked).toBe(false);
    expect(bar()?.textContent).toContain("1node selected");
    await press([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === en["bulk.pick"]));
    await press(menuItem(en["bulk.pick.password"]));
    expect(["de1", "nl1", "fi1", "lv1", "msk1"].filter((n) => tick(n).checked)).toEqual(["de1", "msk1"]);
    await press([...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === en["bulk.pick"]));
    await press(menuItem(en["bulk.clear"]));
    expect(bar()).toBeNull();
  });

  it("an action no selected node can take is off, and says why", async () => {
    await mount();
    await pick("lv1");
    await press(barButton(en["bulk.check"]));
    const ports = menuItem(en["bulk.ports"])!;
    expect(ports.getAttribute("aria-disabled")).toBe("true");
    expect(ports.textContent).toContain(en["bulk.skip.offline"]);
    expect(barButton(en["bulk.password"])?.disabled).toBe(true);
  });
});

describe("checks", () => {
  it("ports go one node at a time; offline and profile-less nodes are skipped with their reason", async () => {
    const calls = held();
    api.checkPorts.mockImplementation(calls.fn);
    await mount();
    await press(tickAll());
    await press(barButton(en["bulk.check"]));
    await press(menuItem(en["bulk.ports"]));
    expect(calls.pending()).toEqual(["nod_de1"]);
    expect(row("lv1")?.textContent).toContain(en["bulk.skip.offline"]);
    expect(row("msk1")?.textContent).toContain(en["bulk.skip.noProfiles"]);
    expect(row("nl1")?.textContent).toContain(en["bulk.state.waiting"]);

    await calls.answer("nod_de1", { errorCode: "", ports: [{ verdict: "ok" }, { verdict: "ok" }], tcp: [{ verdict: "ours" }] });
    expect(calls.pending()).toEqual(["nod_nl1"]);
    expect(row("de1")?.textContent).toContain(en["bulk.result.ok"]);
    expect(row("de1")?.textContent).toContain("3 ports fine");
    expect(row("de1")?.querySelector("a")?.getAttribute("href")).toBe("/nodes/nod_de1?tab=ways&way=direct");

    await calls.answer("nod_nl1", { errorCode: "busy", ports: [], tcp: [] });
    await calls.answer("nod_fi1", { errorCode: "", ports: [{ verdict: "ok" }, { verdict: "lossy" }], tcp: [] });
    expect(row("nl1")?.textContent).toContain(en["bulk.state.bad"]);
    expect(row("nl1")?.textContent).toContain(en["ports.why.busy"]);
    expect(row("fi1")?.textContent).toContain(en["bulk.result.warn"]);
    expect(row("fi1")?.textContent).toContain("1 port fine, 1 lossy");
    expect(batchText()).toContain("Ports checked on 2 of 5 nodes · 1 failed · 2 skipped");
    expect(api.checkPorts).toHaveBeenCalledTimes(3);
  });

  it("the doctor runs at most four nodes at once", async () => {
    const calls = held();
    api.runDoctor.mockImplementation(calls.fn);
    const six = ["de1", "de2", "nl1", "nl2", "fi1", "fi2"].map((n) => node(n));
    await mount({ nodes: six });
    await press(tickAll());
    await press(barButton(en["bulk.check"]));
    await press(menuItem(en["bulk.doctor"]));
    expect(calls.pending()).toEqual(["nod_de1", "nod_de2", "nod_nl1", "nod_nl2"]);
    const report = (items: { status: DoctorStatus; acceptedUnix?: number }[]) => ({ nodes: [{ agentSupported: true, hasReport: true, items }] });
    await calls.answer("nod_de2", report([{ status: DoctorStatus.FAIL }, { status: DoctorStatus.FAIL }, { status: DoctorStatus.WARN, acceptedUnix: 5 }]));
    expect(calls.pending()).toEqual(["nod_de1", "nod_nl1", "nod_nl2", "nod_fi1"]);
    expect(row("de2")?.textContent).toContain(en["bulk.result.bad"]);
    expect(row("de2")?.textContent).toContain("2 failing");
    expect(row("de2")?.textContent).not.toContain("warning"); // an accepted warning is normal for the node
  });
});

describe("SSH", () => {
  it("changes passwords only after the confirm, one node at a time, generated on the panel and never revealed", async () => {
    const calls = held();
    api.rotateNodeServerPassword.mockImplementation(calls.fn);
    await mount();
    await pick("de1", "nl1", "fi1");
    await press(barButton(en["bulk.password"]));
    expect(dialog()?.textContent).toContain("Change the SSH password on 2 nodes");
    expect(dialog()?.textContent).toContain(en["bulk.password.warn"]);
    expect(dialog()?.textContent).toContain(`fi1 — ${en["bulk.skip.keyOnly"]}`);
    expect(api.rotateNodeServerPassword).not.toHaveBeenCalled();

    api.listNodeServerAccess.mockClear();
    await press(inDialog(en["bulk.password.go"]));
    expect(api.listNodeServerAccess).toHaveBeenCalledTimes(1); // read fresh before an SSH batch
    expect(calls.pending()).toEqual(["nod_de1"]);
    expect(api.rotateNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_de1", newPassword: "", generate: true, confirm: true });
    await calls.answer("nod_de1", { rotated: true });
    expect(calls.pending()).toEqual(["nod_nl1"]);
    await calls.refuse("nod_nl1", new ConnectError("ssh_password_change_unverified", Code.Unavailable));

    expect(row("de1")?.textContent).toContain(en["bulk.state.ok"]);
    expect(row("de1")?.querySelector("a")?.getAttribute("href")).toBe("/nodes/nod_de1?tab=server");
    expect(row("de1")?.textContent).toContain("203.0.113.7"); // the host the panel logs in to
    expect(row("nl1")?.textContent).toContain(en["node.sshAccess.error.unverified"]);
    expect(row("fi1")?.textContent).toContain(en["bulk.state.skipped"]);
    expect(batchText()).toContain("Password changed on 1 of 3 nodes · 1 failed · 1 skipped");
    expect(api.revealNodeServerPassword).not.toHaveBeenCalled();
    expect(document.body.textContent).not.toMatch(/kV7#|N4QWZ/);

    // Retry takes only the failed one
    api.rotateNodeServerPassword.mockReset();
    api.rotateNodeServerPassword.mockResolvedValue({ rotated: true });
    await press([...document.querySelectorAll("[data-batch] button")].find((b) => b.textContent?.includes("Retry for 1")));
    expect(api.rotateNodeServerPassword).toHaveBeenCalledTimes(1);
    expect(api.rotateNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_nl1", newPassword: "", generate: true, confirm: true });
  });

  it("binds one public key line; a private key or two lines never leave the browser", async () => {
    api.addNodeServerKey.mockResolvedValue({ key: {} });
    await mount();
    await pick("de1", "lv1");
    await press(barButton(en["bulk.more"]));
    await press(menuItem(en["bulk.bindKey"]));
    const field = dialog()!.querySelector("textarea");
    await enter(field, "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ==\n-----END OPENSSH PRIVATE KEY-----");
    expect((field as HTMLTextAreaElement).value).toBe("");
    expect(dialog()?.textContent).toContain(en["node.sshKeys.privateRefused"]);
    expect(inDialog(en["bulk.bind.go"])?.disabled).toBe(true);
    await enter(field, `${goKeys[0][0]}\n${goKeys[2][0]}`);
    expect(dialog()?.textContent).toContain(en["node.sshKeys.error.invalid"]);
    expect(inDialog(en["bulk.bind.go"])?.disabled).toBe(true);

    await enter(field, goKeys[0][0]);
    expect(dialog()?.textContent).toContain("ED25519");
    expect(dialog()?.textContent).toContain(goKeys[0][1]);
    expect(dialog()?.textContent).toContain(`lv1 — ${en["bulk.skip.noAccess"]}`);
    await press(inDialog(en["bulk.bind.go"]));
    await settle();
    expect(api.addNodeServerKey).toHaveBeenCalledTimes(1);
    expect(api.addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_de1", publicKey: goKeys[0][0], confirm: true });
  });

  it("unbinds by fingerprint, refuses the panel key, and reads a missing key as not there", async () => {
    api.removeNodeServerKey.mockImplementation(async ({ nodeId }: { nodeId: string }) => {
      if (nodeId === "nod_de1") throw new ConnectError("ssh_public_key_not_found", Code.NotFound);
      return {};
    });
    await mount();
    await pick("de1", "nl1");
    await press(barButton(en["bulk.more"]));
    await press(menuItem(en["bulk.unbindKey"]));
    const field = dialog()!.querySelector("textarea");
    await enter(field, panelFp);
    expect(dialog()?.textContent).toContain(en["bulk.unbind.panel"]);
    expect(inDialog(en["bulk.unbind.go"])?.disabled).toBe(true);
    await enter(field, "SHA256:short");
    expect(dialog()?.textContent).toContain(en["bulk.unbind.badFp"]);
    await enter(field, goKeys[1][1]);
    expect(inDialog(en["bulk.unbind.go"])?.disabled).toBe(false);
    await press(inDialog(en["bulk.unbind.go"]));
    await settle();
    expect(api.removeNodeServerKey.mock.calls.map((c) => c[0])).toEqual([
      { nodeId: "nod_de1", fingerprint: goKeys[1][1], confirm: true, confirmPanelKey: false },
      { nodeId: "nod_nl1", fingerprint: goKeys[1][1], confirm: true, confirmPanelKey: false },
    ]);
    expect(row("de1")?.textContent).toContain(en["bulk.state.absent"]);
    expect(row("nl1")?.textContent).toContain(en["bulk.state.ok"]);
  });

  it("reads a key's fingerprint the way Go's x/crypto/ssh does", async () => {
    for (const [line, fingerprint, label, comment] of goKeys) expect(await sshKeyInfo(line)).toEqual({ type: line.split(" ")[0], label, comment, fingerprint });
    expect(await sshKeyInfo("ssh-ed25519 AAAAB3NzaC1yc2EAAAADAQABAAABAQ== x")).toBeNull(); // an RSA blob behind an ed25519 name
    expect(await sshKeyInfo("ssh-ed25519 AAAA")).toBeNull();
  });
});

describe("agent", () => {
  it("hands only the selected outdated nodes to a rollout, then opens Updates", async () => {
    api.startRollout.mockResolvedValue({});
    await mount();
    await pick("de1", "fi1", "msk1");
    const update = barButton(en["bulk.agent"])!;
    expect(update.textContent).toContain("· 1");
    await press(update);
    expect(dialog()?.querySelectorAll("input[type=checkbox]")).toHaveLength(1);
    expect(dialog()?.textContent).toContain(`fi1 — ${en["bulk.skip.current"]}`);
    expect(dialog()?.textContent).toContain(`msk1 — ${en["bulk.skip.manual"]}`);
    await press(inDialog(en["up.rollout.go"]));
    expect(api.startRollout).toHaveBeenCalledWith(expect.objectContaining({ nodeIds: ["nod_de1"], expectedVersion: "0.3.1" }));
    expect(api.navigate).toHaveBeenCalledWith({ to: "/updates" });
  });

  it("is off while a rollout runs, and says so", async () => {
    const rollout = { id: "rol_1", status: RolloutStatus.RUNNING, steps: [{ nodeId: "nod_nl1", state: StepState.PENDING, stage: 1 }] };
    await mount({ ups: updates({ rollout }) });
    await pick("de1");
    expect(barButton(en["bulk.agent"])?.disabled).toBe(true);
    let why: string | undefined;
    function Probe() {
      const t = useT();
      useEffect(() => {
        why = availability("agent", [node("de1")] as never, { updates: updates({ rollout }) as never }, t).why;
      });
      return null;
    }
    act(() => root!.render(<Probe />));
    expect(why).toBe(en["bulk.agent.rollout"]);
  });
});
