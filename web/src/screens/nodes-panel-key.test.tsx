import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { PanelKeyBulk } from "./nodes-panel-key";

const listNodeServerAccess = vi.fn();
const addNodeServerKey = vi.fn();
const stepUps = vi.fn();

vi.mock("@/lib/api", () => ({
  provisioning: {
    listNodeServerAccess: (...a: unknown[]) => listNodeServerAccess(...a),
    addNodeServerKey: (...a: unknown[]) => addNodeServerKey(...a),
  },
}));

vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => {
    stepUps();
    return call();
  },
  isStepUpCancelled: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const node = (nodeId: string, nodeName: string, over: object = {}) => ({
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
  node("nod_de1", "de1"),
  node("nod_nl1", "nl1"),
  node("nod_fi1", "fi1", { panelKeyFingerprint: "SHA256:panel" }), // already by key
  node("nod_old", "old1", { nodeRetired: true }), // retired: never touched
  node("nod_key", "de2", { passwordSaved: false, panelKeyFingerprint: "SHA256:panel" }),
];

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  listNodeServerAccess.mockReset();
  addNodeServerKey.mockReset();
  stepUps.mockReset();
  localStorage.clear();
});

async function mount(access = fleet) {
  listNodeServerAccess.mockResolvedValue({ access });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={client}>
        <ToastProvider>
          <PanelKeyBulk />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await flush();
}

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((resolve) => setTimeout(resolve, 0))));
}

const text = () => host?.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const rowOf = (name: string) => [...document.querySelectorAll("li")].find((li) => li.querySelector("b")?.textContent === name)!;

describe("the panel key on every password-only node", () => {
  it("counts only live nodes that log in with a password alone", async () => {
    await mount();
    expect(text()).toContain("2 nodes log in with a password only. Install the panel key on all?");
  });

  it("runs only on the owner's click, one node at a time, and says per node how it went", async () => {
    let finishFirst!: () => void;
    addNodeServerKey
      .mockImplementationOnce(() => new Promise((resolve) => (finishFirst = () => resolve({ key: {} }))))
      .mockRejectedValueOnce(new ConnectError("authorized_keys_unsafe", Code.FailedPrecondition));
    await mount();
    expect(addNodeServerKey).not.toHaveBeenCalled();

    await act(async () => button(en["nodes.panelKey.action"])!.click());
    await flush();
    expect(addNodeServerKey).toHaveBeenCalledTimes(1); // nl1 waits for de1
    expect(addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_de1", panelKey: true, confirm: true });
    expect(text()).toContain("Installing the panel key: 0 of 2");
    expect(rowOf("de1").textContent).toContain(en["nodes.panelKey.installing"]);
    expect(rowOf("nl1").textContent).toContain(en["nodes.panelKey.waiting"]);

    await act(async () => finishFirst());
    await flush();
    expect(addNodeServerKey).toHaveBeenCalledTimes(2);
    expect(addNodeServerKey).toHaveBeenLastCalledWith({ nodeId: "nod_nl1", panelKey: true, confirm: true });
    expect(rowOf("de1").textContent).toContain(en["nodes.panelKey.installed"]);
    expect(rowOf("nl1").textContent).toContain(en["nodes.panelKey.failed"]);
    expect(rowOf("nl1").textContent).toContain(en["node.sshKeys.error.unsafe"]);
    expect(text()).toContain("Installed on 1 of 2 nodes");
    expect(stepUps).toHaveBeenCalledTimes(2); // every call goes through the guard; the panel asks once in its window
    expect(listNodeServerAccess).toHaveBeenCalledTimes(2); // the access list is read again at the end
  });

  it("words a failed login with a node's saved access by its real cause", async () => {
    addNodeServerKey.mockRejectedValue(new ConnectError("ssh_host_key_changed", Code.FailedPrecondition));
    await mount([node("nod_de1", "de1")]);
    await act(async () => button(en["nodes.panelKey.action"])!.click());
    await flush();
    expect(rowOf("de1").textContent).toContain(en["node.ssh.error.hostKey"]);
  });

  it("stays away with nothing to do, and once hidden until another node logs in with a password only", async () => {
    await mount([node("nod_fi1", "fi1", { panelKeyFingerprint: "SHA256:panel" })]);
    expect(host!.querySelector("section")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount();
    await act(async () => button(en["nodes.panelKey.hide"])!.click());
    expect(host!.querySelector("section")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount();
    expect(host!.querySelector("section")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount([...fleet, node("nod_se1", "se1")]);
    expect(text()).toContain("3 nodes log in with a password only.");
  });
});
