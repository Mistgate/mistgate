import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { NodesScreen } from "./nodes";

// The offer above the Nodes list: live nodes that log in with a saved password only get the panel key, on the owner's click.
// The click selects them and runs "Panel key" in the bulk bar: one node at a time, each row saying how it went.

const api = vi.hoisted(() => ({ listNodeServerAccess: vi.fn(), addNodeServerKey: vi.fn(), stepUps: vi.fn(), declined: { on: false } }));
vi.mock("@/lib/api", () => ({
  basepath: "/",
  auth: { me: () => Promise.resolve({}) },
  provisioning: { listNodeServerAccess: api.listNodeServerAccess, addNodeServerKey: api.addNodeServerKey },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useNavigate: () => vi.fn(),
}));
vi.mock("@/components/add-node", () => ({ useAddNode: () => vi.fn() }));
// every call goes through the guard; the owner may decline the step-up at a given node
vi.mock("@/components/step-up", async (original) => {
  const real = await original<typeof import("@/components/step-up")>();
  return {
    ...real,
    useStepUp: () => async (call: () => Promise<unknown>) => {
      api.stepUps();
      if (api.declined.on) throw new real.StepUpCancelled();
      return call();
    },
  };
});

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
  api.listNodeServerAccess.mockReset();
  api.addNodeServerKey.mockReset();
  api.stepUps.mockReset();
  api.declined.on = false;
  localStorage.clear();
});

const access = (nodeId: string, nodeName: string, over: object = {}) => ({
  nodeId,
  nodeName,
  host: `${nodeName}.example.com`,
  port: 22,
  username: "root",
  passwordSaved: true,
  passwordLogin: true,
  panelKeyFingerprint: "",
  nodeRetired: false,
  ...over,
});
const fleet = [
  access("nod_de1", "de1"),
  access("nod_nl1", "nl1"),
  access("nod_fi1", "fi1", { panelKeyFingerprint: "SHA256:panel" }), // already by key
  access("nod_old", "old1", { nodeRetired: true }), // retired: never touched
  access("nod_key", "de2", { passwordSaved: false, panelKeyFingerprint: "SHA256:panel" }),
];
const node = (id: string, name: string, over: object = {}) => ({ id, name, countryCode: "DE", location: "", provider: "", address: `${name}.example.com`, status: NodeStatus.ONLINE, protocols: [], online: [], trafficTodayBytes: 0, hasMetrics: false, cpuPct: 0, uptimeS: 0, agentVersion: "0.3.1", ...over });

async function mount(list = fleet) {
  api.listNodeServerAccess.mockResolvedValue({ access: list });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER } } as never);
  qc.setQueryData(updatesQuery.queryKey, { nodes: [] } as never);
  const nodes = list.map((a) => node(a.nodeId, a.nodeName, a.nodeRetired ? { status: NodeStatus.RETIRED } : {}));
  qc.setQueryData(nodesQuery.queryKey, { nodes, expectedAgentVersion: "0.3.1" } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodesScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await flush();
}

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((resolve) => setTimeout(resolve, 0))));
}

const text = () => document.body.textContent ?? "";
const offer = () => document.querySelector("[data-panel-key-offer]");
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const rowOf = (name: string) => [...document.querySelectorAll("[data-batch] li")].find((li) => li.querySelector("b")?.textContent === name)!;
const ticked = (name: string) => document.querySelector<HTMLInputElement>(`input[aria-label="${en["bulk.select"].replace("{name}", name)}"]`)!.checked;

describe("the panel key on every password-only node", () => {
  it("counts only live nodes that log in with a password alone", async () => {
    await mount();
    expect(offer()?.textContent).toContain("2 nodes log in with a password only. Install the panel key on all?");
  });

  it("runs only on the owner's click: selects them, then one node at a time, each row saying how it went", async () => {
    let finishFirst!: () => void;
    api.addNodeServerKey
      .mockImplementationOnce(() => new Promise((resolve) => (finishFirst = () => resolve({ key: {} }))))
      .mockRejectedValueOnce(new ConnectError("authorized_keys_unsafe", Code.FailedPrecondition));
    await mount();
    expect(api.addNodeServerKey).not.toHaveBeenCalled();

    await act(async () => button(en["nodes.panelKey.action"])!.click());
    await flush();
    expect(["de1", "nl1", "fi1", "de2"].filter(ticked)).toEqual(["de1", "nl1"]);
    expect(api.addNodeServerKey).toHaveBeenCalledTimes(1); // nl1 waits for de1
    expect(api.addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_de1", panelKey: true, confirm: true });
    expect(text()).toContain(`${en["bulk.title.panelKey"]} · 0 of 2`);
    expect(rowOf("de1").textContent).toContain(en["bulk.state.running"]);
    expect(rowOf("nl1").textContent).toContain(en["bulk.state.waiting"]);

    await act(async () => finishFirst());
    await flush();
    expect(api.addNodeServerKey).toHaveBeenCalledTimes(2);
    expect(api.addNodeServerKey).toHaveBeenLastCalledWith({ nodeId: "nod_nl1", panelKey: true, confirm: true });
    expect(rowOf("de1").textContent).toContain(en["bulk.state.ok"]);
    expect(rowOf("nl1").textContent).toContain(en["bulk.state.bad"]);
    expect(rowOf("nl1").textContent).toContain(en["node.sshKeys.error.unsafe"]);
    expect(text()).toContain("Panel key installed on 1 of 2 nodes · 1 failed");
    expect(api.stepUps).toHaveBeenCalledTimes(2); // every call goes through the guard; the panel asks once in its window
    expect(api.listNodeServerAccess).toHaveBeenCalledTimes(3); // on the page's load, fresh before the batch, and again at its end
  });

  it("words a failed login with a node's saved access by its real cause", async () => {
    api.addNodeServerKey.mockRejectedValue(new ConnectError("ssh_host_key_changed", Code.FailedPrecondition));
    await mount([access("nod_de1", "de1")]);
    await act(async () => button(en["nodes.panelKey.action"])!.click());
    await flush();
    expect(rowOf("de1").textContent).toContain(en["node.ssh.error.hostKey"]);
  });

  it("a declined step-up starts nothing: every row reads Not started", async () => {
    api.declined.on = true;
    await mount();
    await act(async () => button(en["nodes.panelKey.action"])!.click());
    await flush();
    expect(api.addNodeServerKey).not.toHaveBeenCalled();
    expect(rowOf("de1").textContent).toContain(en["bulk.state.notStarted"]);
    expect(rowOf("nl1").textContent).toContain(en["bulk.state.notStarted"]);
    expect(text()).toContain("· 2 not started");
    expect(button(`${en["bulk.retry"].replace("{n}", "2")}`)).toBeTruthy();
  });

  it("stays away with nothing to do, and once hidden until another node logs in with a password only", async () => {
    await mount([access("nod_fi1", "fi1", { panelKeyFingerprint: "SHA256:panel" })]);
    expect(offer()).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount();
    await act(async () => button(en["nodes.panelKey.hide"])!.click());
    expect(offer()).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount();
    expect(offer()).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount([...fleet, access("nod_se1", "se1")]);
    expect(offer()?.textContent).toContain("3 nodes log in with a password only.");
  });
});
