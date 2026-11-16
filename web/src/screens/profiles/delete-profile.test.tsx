import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { InboundState, NodeStatus, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { DeleteSection } from "./editor";

const deleteProfile = vi.fn();
const removeFrom = vi.fn();
const listNodes = vi.fn();
const navigate = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: { deleteProfile: (...a: unknown[]) => deleteProfile(...a), removeProfileFromNodes: (...a: unknown[]) => removeFrom(...a) },
  nodes: { listNodes: (...a: unknown[]) => listNodes(...a) },
  relays: { listRelays: () => Promise.resolve({ relays: [] }) },
}));
vi.mock("@tanstack/react-router", async (orig) => ({ ...(await orig<object>()), useNavigate: () => navigate }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => listNodes.mockResolvedValue({ nodes: [] }));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [deleteProfile, removeFrom, listNodes, navigate]) m.mockReset();
});

const profile = { id: "prf_1", name: "Main 443", protocol: "hysteria2" } as unknown as ProfileSummary;
const inbound = (n: number) => ({ id: `inb_${n}`, profileId: "prf_1", nodeId: `nod_${n}`, nodeName: `n${n}`, port: 443, state: InboundState.ACTIVE, lastError: "" }) as unknown as Inbound;
const eleven = Array.from({ length: 11 }, (_, i) => inbound(i + 1));

async function mount(inbounds: Inbound[]) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = (list: Inbound[]) => (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <DeleteSection profile={profile} inbounds={list} />
      </ToastProvider>
    </QueryClientProvider>
  );
  await act(async () => root!.render(view(inbounds)));
  await settle();
  return (list: Inbound[]) => act(async () => root!.render(view(list)));
}
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
// the toast is a role=dialog too, with aria-modal=false
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]:not([aria-modal=false])");
const buttonIn = (el: ParentNode, label: string) => [...el.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const open = () => click(buttonIn(document.body, "Delete"));
const boxes = () => [...dialog()!.querySelectorAll<HTMLElement>("li [role=checkbox]")];
const ticked = () => boxes().filter((b) => b.getAttribute("aria-checked") === "true");

describe("deleting a profile that is on no node", () => {
  it("is the plain confirmation, and one DeleteProfile call", async () => {
    deleteProfile.mockResolvedValue({});
    await mount([]);
    await open();
    expect(dialog()!.textContent).toContain("Delete “Main 443”?");
    expect(boxes()).toHaveLength(0);
    await click(buttonIn(dialog()!, "Delete"));
    await settle();
    expect(deleteProfile).toHaveBeenCalledWith({ profileId: "prf_1" });
    expect(removeFrom).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/profiles", replace: true });
  });

  it("says why when the panel refuses, and keeps the window", async () => {
    deleteProfile.mockRejectedValue(new ConnectError("boom", Code.Unavailable));
    await mount([]);
    await open();
    await click(buttonIn(dialog()!, "Delete"));
    await settle();
    expect(dialog()).not.toBeNull();
    expect(navigate).not.toHaveBeenCalled();
  });
});

describe("deleting a profile that is on 11 nodes", () => {
  it("lists every node ticked, and takes it off all of them and deletes in one call once the name is typed", async () => {
    removeFrom.mockResolvedValue({ removed: 11, profileDeleted: true, relaysRemoved: 0 });
    await mount(eleven);
    await open();
    expect(dialog()!.textContent).toContain("It is on 11 nodes");
    expect(ticked()).toHaveLength(11);
    expect(dialog()!.textContent).toContain("deleted for good");
    expect(buttonIn(dialog()!, "Take off all nodes and delete")!.hasAttribute("disabled")).toBe(true); // the name is not typed yet
    await type(dialog()!.querySelector<HTMLInputElement>("input:not([type=checkbox])")!, "Main 443");
    await click(buttonIn(dialog()!, "Take off all nodes and delete"));
    await settle();
    expect(removeFrom).toHaveBeenCalledTimes(1);
    expect(removeFrom).toHaveBeenCalledWith({ profileId: "prf_1", nodeIds: eleven.map((i) => i.nodeId), deleteProfile: true, confirmName: "Main 443" });
    expect(deleteProfile).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith({ to: "/profiles", replace: true });
  });

  it("with some unticked only takes it off those, keeps the profile and shows the nodes that are left", async () => {
    removeFrom.mockResolvedValue({ removed: 2, profileDeleted: false, relaysRemoved: 0 });
    const rerender = await mount(eleven);
    await open();
    await click(dialog()!.querySelector('[role=checkbox][aria-label="All nodes"]')); // all off
    expect(buttonIn(dialog()!, "Take off")!.hasAttribute("disabled")).toBe(true);
    await click(dialog()!.querySelector('li [role=checkbox][aria-label="n1"]'));
    await click(dialog()!.querySelector('li [role=checkbox][aria-label="n2"]'));
    expect(dialog()!.querySelector("input:not([type=checkbox])")).toBeNull(); // no name to type: the profile stays
    await click(buttonIn(dialog()!, "Take off 2 nodes"));
    await settle();
    expect(removeFrom).toHaveBeenCalledWith({ profileId: "prf_1", nodeIds: ["nod_1", "nod_2"], deleteProfile: false, confirmName: "" });
    expect(navigate).not.toHaveBeenCalled();
    await rerender(eleven.slice(2)); // the page read the profile again
    expect(dialog()!.textContent).toContain("It is on 9 nodes");
    expect(boxes()).toHaveLength(9);
    expect(ticked()).toHaveLength(9);
    expect(buttonIn(dialog()!, "Take off all nodes and delete")).toBeDefined();
  });

  it("says why when the panel refuses, and keeps the window and the ticks", async () => {
    removeFrom.mockRejectedValue(new ConnectError("profile_deployed: nodes=n12", Code.FailedPrecondition));
    await mount(eleven);
    await open();
    await type(dialog()!.querySelector<HTMLInputElement>("input:not([type=checkbox])")!, "Main 443");
    await click(buttonIn(dialog()!, "Take off all nodes and delete"));
    await settle();
    expect(dialog()!.textContent).toContain("The profile is on nodes n12");
    expect(ticked()).toHaveLength(11);
    expect(navigate).not.toHaveBeenCalled();
    expect(buttonIn(dialog()!, "Take off all nodes and delete")!.hasAttribute("disabled")).toBe(false); // it can be tried again
  });

  it("names the nodes that are not connected in the same window", async () => {
    listNodes.mockResolvedValue({ nodes: [{ id: "nod_1", name: "n1", status: NodeStatus.DOWN, countryCode: "", address: "n1.example.com", protocols: [] }] });
    await mount(eleven.slice(0, 3));
    await open();
    expect(dialog()!.textContent).toContain("1 of them is not connected now");
  });
});
