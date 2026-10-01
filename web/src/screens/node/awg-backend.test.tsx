import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AwgPrepareState, PrepareAwgOutcome } from "@/gen/mistgate/admin/v1/node_pb";
import { AwgBackendCard } from "./awg-backend";

const updateNode = vi.fn();
const prepareAwgKernel = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { updateNode: (...a: unknown[]) => updateNode(...a), prepareAwgKernel: (...a: unknown[]) => prepareAwgKernel(...a) },
  health: { getDoctor: () => Promise.resolve({ nodes: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
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
beforeEach(() => {
  updateNode.mockResolvedValue({});
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [updateNode, prepareAwgKernel]) m.mockReset();
});

type Prep = { supported: boolean; state?: AwgPrepareState; sinceUnix?: number; reasonCode?: string; reason?: string };
const nowS = () => Math.floor(Date.now() / 1000);

function data(prep: Prep | undefined, awgBackend = "auto") {
  return {
    node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, awgBackend, awgPrepare: prep && { state: AwgPrepareState.NONE, sinceUnix: 0, reasonCode: "", reason: "", ...prep } },
    inbounds: [],
    facts: { virt: "kvm" },
  } as never;
}

async function mount(d: never) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AwgBackendCard data={d} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const settle = async () => {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.trim() === label);
// the toast is a role=dialog too (a non-modal one): the confirm window is the modal one
const dialog = () => document.querySelector('[role=dialog]:not([aria-modal="false"])');
const manualCommand = "mistgate-node awg prepare-kernel";

async function pickKernelAndSave() {
  await click(radio("Kernel module"));
  await click(button("Save"));
  await settle();
}

describe("the kernel module on a node whose agent is too old to build it", () => {
  it("keeps the manual command and just saves the setting", async () => {
    await mount(data({ supported: false }));
    await click(radio("Kernel module"));
    expect(text()).toContain(manualCommand);
    expect(text()).toContain("Run this on the node as root");
    expect(text()).not.toContain("Do it by hand");
    await click(button("Save"));
    await settle();
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "kernel" });
    expect(prepareAwgKernel).not.toHaveBeenCalled();
    expect(dialog()).toBeNull();
  });

  it("does the same when the node says nothing about preparing at all", async () => {
    await mount(data(undefined));
    await pickKernelAndSave();
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "kernel" });
    expect(prepareAwgKernel).not.toHaveBeenCalled();
  });
});

describe("the kernel module on a node whose agent builds it itself", () => {
  it("asks the node, and when the module is already ready just switches, without a dialog", async () => {
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.READY });
    await mount(data({ supported: true }));
    await pickKernelAndSave();
    expect(prepareAwgKernel).toHaveBeenCalledWith({ nodeId: "nod_1", confirm: false });
    expect(dialog()).toBeNull();
    expect(updateNode).toHaveBeenCalledWith({ nodeId: "nod_1", awgBackend: "kernel" });
  });

  it("says plainly what will happen before it starts anything, and starts it only on confirm; the setting is not touched", async () => {
    prepareAwgKernel.mockResolvedValueOnce({ outcome: PrepareAwgOutcome.NEEDS_PREPARE }).mockResolvedValueOnce({ outcome: PrepareAwgOutcome.STARTED });
    await mount(data({ supported: true }));
    await click(radio("Kernel module"));
    // the manual way is tucked into a closed disclosure
    const manual = document.querySelector("details");
    expect(manual?.textContent).toContain("Do it by hand");
    expect(manual?.textContent).toContain(manualCommand);
    expect(manual?.hasAttribute("open")).toBe(false);
    expect(document.querySelectorAll("code")).toHaveLength(1); // nowhere else
    await click(button("Save"));
    await settle();

    const d = dialog()?.textContent ?? "";
    expect(d).toContain("Build the kernel module on de1?");
    for (const part of ["dkms", "make", "gcc", "kernel headers".replace("kernel headers", "headers"), "a few hundred MB", "1–5 minutes", "userspace", "reconnect once", "nothing changes"]) {
      expect(d, part).toContain(part);
    }
    expect(prepareAwgKernel).toHaveBeenCalledTimes(1); // only the question so far
    expect(updateNode).not.toHaveBeenCalled();

    await click(button("Build the module"));
    await settle();
    expect(prepareAwgKernel).toHaveBeenLastCalledWith({ nodeId: "nod_1", confirm: true });
    expect(updateNode).not.toHaveBeenCalled(); // the panel switches the node itself, after the module is built
    expect(dialog()).toBeNull();
    expect(text()).toContain("The node is preparing the module");
  });

  it("starts nothing when the dialog is cancelled", async () => {
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.NEEDS_PREPARE });
    await mount(data({ supported: true }));
    await pickKernelAndSave();
    await click(button("Cancel"));
    await settle();
    expect(dialog()).toBeNull();
    expect(prepareAwgKernel).toHaveBeenCalledTimes(1);
    expect(updateNode).not.toHaveBeenCalled();
  });

  it("shows why the node cannot do it and the command to run by hand, without a dialog", async () => {
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.UNSUPPORTED, reasonCode: "container", reason: "this is a lxc container" });
    await mount(data({ supported: true }));
    await pickKernelAndSave();
    expect(dialog()).toBeNull();
    expect(text()).toContain("this is a container, it cannot load a kernel module");
    expect(text()).toContain(manualCommand);
    expect(updateNode).not.toHaveBeenCalled();
  });

  it("does not switch or start twice while it builds", async () => {
    await mount(data({ supported: true, state: AwgPrepareState.RUNNING, sinceUnix: nowS() - 200 }));
    await click(radio("Kernel module"));
    expect(button("Save")?.hasAttribute("disabled")).toBe(true);
    // another backend can still be chosen and saved
    await click(radio("Userspace"));
    expect(button("Save")?.hasAttribute("disabled")).toBe(false);
  });

  it("the panel's refusal (the node went away, the agent is old) is a sentence, and nothing is saved", async () => {
    prepareAwgKernel.mockRejectedValue(new ConnectError("agent too old", Code.FailedPrecondition));
    await mount(data({ supported: true }));
    await pickKernelAndSave();
    expect(dialog()).toBeNull();
    expect(text()).toContain("The node’s agent is too old for AmneziaWG");
    expect(updateNode).not.toHaveBeenCalled();
  });
});

describe("how the preparation is shown", () => {
  it("running: how many minutes", async () => {
    await mount(data({ supported: true, state: AwgPrepareState.RUNNING, sinceUnix: nowS() - 200 }));
    expect(text()).toContain("Preparing the module… (running 3 min)");
    expect(text()).toContain("AmneziaWG keeps running as before");
  });

  it("running for less than a minute", async () => {
    await mount(data({ supported: true, state: AwgPrepareState.RUNNING, sinceUnix: nowS() - 5 }));
    expect(text()).toContain("running <1 min");
  });

  it("done: ready, and whether the node uses it", async () => {
    await mount(data({ supported: true, state: AwgPrepareState.DONE, sinceUnix: nowS() }, "kernel"));
    expect(text()).toContain("Module ready");
    expect(text()).toContain("The node runs AmneziaWG on the kernel module.");
    act(() => root?.unmount());
    host?.remove();
    await mount(data({ supported: true, state: AwgPrepareState.DONE, sinceUnix: nowS() }, "auto"));
    expect(text()).toContain("The module is built and loaded. Pick “Kernel module” to use it.");
  });

  it("failed: the reason in plain words, no log dump, the node's state, and Retry that asks again", async () => {
    prepareAwgKernel.mockResolvedValue({ outcome: PrepareAwgOutcome.NEEDS_PREPARE });
    await mount(
      data({ supported: true, state: AwgPrepareState.FAILED, sinceUnix: nowS(), reasonCode: "step_failed", reason: "step 2 of 5 failed: install the build tools (exit status 100)" }),
    );
    expect(text()).toContain("Failed: one of the installation steps failed");
    expect(text()).toContain("step 2 of 5 failed: install the build tools (exit status 100)"); // the short fact
    expect(text()).toContain("The node kept the backend it had");
    expect(text()).toContain("journalctl -u mistgate-awg-prepare");
    await click(button("Retry"));
    await settle();
    expect(prepareAwgKernel).toHaveBeenCalledWith({ nodeId: "nod_1", confirm: false });
    expect(dialog()?.textContent).toContain("Build the kernel module on de1?"); // it installs again: confirmed again
  });

  it("failed with a timeout: only the sentence, there is no detail to add", async () => {
    await mount(data({ supported: true, state: AwgPrepareState.FAILED, sinceUnix: nowS(), reasonCode: "timeout", reason: "it did not finish within 15 minutes and was stopped" }));
    expect(text()).toContain("Failed: the build did not finish in 15 minutes and was stopped");
    expect(text()).not.toContain("it did not finish within 15 minutes");
  });

  it("an old agent shows no preparation status at all", async () => {
    await mount(data({ supported: false, state: AwgPrepareState.FAILED, reasonCode: "timeout" }));
    expect(text()).not.toContain("Failed:");
  });
});
