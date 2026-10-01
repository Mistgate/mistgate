import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { SettingsTab } from "./settings";

// Node → Settings: what retiring says when the node could not be told.

const retireNode = vi.fn();
const me = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { retireNode: (...a: unknown[]) => retireNode(...a), updateNode: vi.fn() },
  auth: { me: (...a: unknown[]) => me(...a) },
}));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("./warp", () => ({ WarpCard: () => null }));
vi.mock("./awg-backend", () => ({ AwgBackendCard: () => null }));
vi.mock("./ssh-access", () => ({ SSHAccessCard: () => null }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  me.mockResolvedValue({ admin: { id: "adm_1", role: 1 } });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [retireNode, me, navigate]) m.mockReset();
});

const data = (status = NodeStatus.DOWN) =>
  ({
    node: { id: "nod_1", name: "de1", address: "de1.example.com", countryCode: "DE", status, location: "", provider: "" },
    notes: "",
    dnsResolvers: [],
    inbounds: [],
  }) as never;

async function mount(status?: NodeStatus) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <SettingsTab data={data(status)} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function retire() {
  await click(button(en["node.retire"]));
  await type(dialog()!.querySelector<HTMLInputElement>('input[placeholder="de1"]')!, "de1");
  await click(inDialog(en["node.retire"]));
}

describe("retiring a node", () => {
  it("goes back to the list when the agent got the order", async () => {
    retireNode.mockResolvedValue({ agentNotified: true });
    await mount(NodeStatus.ONLINE);
    await retire();
    expect(retireNode).toHaveBeenCalledWith({ nodeId: "nod_1", confirmName: "de1" });
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes" });
    expect(text()).not.toContain("did not get the order");
  });

  it("says an offline node was not reached and gives the cleanup to run on the server", async () => {
    retireNode.mockResolvedValue({ agentNotified: false });
    await mount();
    await retire();
    expect(navigate).not.toHaveBeenCalled();
    expect(dialog()?.textContent).toContain("de1 did not get the order");
    expect(dialog()?.textContent).toContain(en["node.retiredUnreachedBody"]);
    expect(dialog()?.textContent).toContain("systemctl disable --now mistgate-node");
    expect(dialog()?.textContent).toContain("rm -rf /var/lib/mistgate-node");
    await click(inDialog(en["common.done"]));
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes" });
  });
});
