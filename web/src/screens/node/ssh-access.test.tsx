import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { SSHAccessCard } from "./ssh-access";

const listNodeServerAccess = vi.fn();
const revealNodeServerPassword = vi.fn();
const forgetNodeServerAccess = vi.fn();

vi.mock("@/lib/api", () => ({
  provisioning: {
    listNodeServerAccess: (...args: unknown[]) => listNodeServerAccess(...args),
    revealNodeServerPassword: (...args: unknown[]) => revealNodeServerPassword(...args),
    forgetNodeServerAccess: (...args: unknown[]) => forgetNodeServerAccess(...args),
  },
}));

vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let client: QueryClient | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  client = null;
  listNodeServerAccess.mockReset();
  revealNodeServerPassword.mockReset();
  forgetNodeServerAccess.mockReset();
});

function tree(nodeId: string) {
  return (
    <QueryClientProvider client={client!}>
      <ToastProvider>
        <div role="tabpanel">
          <SSHAccessCard nodeId={nodeId} />
        </div>
      </ToastProvider>
    </QueryClientProvider>
  );
}

async function mount(nodeId = "nod_1") {
  listNodeServerAccess.mockResolvedValue({
    access: ["nod_1", "nod_2"].map((id) => ({
      nodeId: id,
      nodeName: id === "nod_1" ? "de1" : "de2",
      host: "node.example.com",
      port: 22,
      username: "root",
    })),
  });
  revealNodeServerPassword.mockResolvedValue({ password: "secret-generated-inside-panel" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(tree(nodeId)));
  await flush();
}

async function renderNode(nodeId: string) {
  await act(async () => root!.render(tree(nodeId)));
  await flush();
}

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((resolve) => setTimeout(resolve, 0))));
}

function deferReveal() {
  let finish!: (response: { password: string }) => void;
  revealNodeServerPassword.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
  return () => finish({ password: "late-secret" });
}

describe("node SSH access", () => {
  it("reveals on demand, then clears the secret when hidden without putting it in the query cache", async () => {
    await mount();
    expect(revealNodeServerPassword).not.toHaveBeenCalled();
    const show = document.querySelector("button");
    expect(show).toBeTruthy();
    await act(async () => show!.click());
    await flush();

    const input = document.querySelector<HTMLInputElement>("#ssh-password-nod_1");
    expect(input?.value).toBe("secret-generated-inside-panel");
    const cached = client?.getQueryData(["node-server-access", "nod_1"]);
    expect(cached).toBeTruthy();
    expect(cached).not.toHaveProperty("password");

    const hide = document.querySelector("button");
    expect(hide).toBeTruthy();
    await act(async () => hide!.click());
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("clears a revealed password when its settings tab becomes inactive", async () => {
    await mount();
    await act(async () => document.querySelector("button")!.click());
    await flush();
    expect(document.querySelector<HTMLInputElement>("#ssh-password-nod_1")?.value).toBe("secret-generated-inside-panel");

    await act(async () => {
      document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("discards a pending reveal when the browser tab is hidden", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    const priorVisibility = Object.getOwnPropertyDescriptor(document, "visibilityState");
    try {
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
      await act(async () => document.dispatchEvent(new Event("visibilitychange")));
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
      await act(async () => document.dispatchEvent(new Event("visibilitychange")));
      await act(async () => {
        finish();
        await Promise.resolve();
      });
      await flush();
      expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
    } finally {
      if (priorVisibility) Object.defineProperty(document, "visibilityState", priorVisibility);
      else Reflect.deleteProperty(document, "visibilityState");
    }
  });

  it("discards a pending reveal on pagehide", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await act(async () => window.dispatchEvent(new Event("pagehide")));
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("discards a pending reveal when its settings tab becomes inactive", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await act(async () => {
      document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("keeps a retired node's access until the owner forgets it, after a second click", async () => {
    await mount();
    expect(document.body.textContent).not.toContain("Forget saved access");
    act(() => root?.unmount());
    host?.remove();

    const retired = { nodeId: "nod_1", nodeName: "de1", host: "node.example.com", port: 22, username: "root", nodeRetired: true, passwordGenerated: true };
    listNodeServerAccess.mockReset();
    listNodeServerAccess.mockResolvedValueOnce({ access: [retired] }).mockResolvedValue({ access: [] });
    forgetNodeServerAccess.mockResolvedValue({});
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(tree("nod_1")));
    await flush();
    expect(document.body.textContent).toContain("only the panel knows it");
    expect(document.body.textContent).toContain("This node is retired");

    const forget = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Forget"))!;
    await act(async () => forget().click());
    expect(forgetNodeServerAccess).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("deleted for good");
    await act(async () => forget().click());
    await flush();
    expect(forgetNodeServerAccess).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(document.querySelector("section")).toBeNull(); // nothing saved any more: the card goes
  });

  it("does not show a previous node's password after nodeId changes during reveal", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await renderNode("nod_2");
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
    expect(document.querySelector("#ssh-password-nod_2")).toBeNull();
  });
});
