import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { SubscriptionHostState } from "@/gen/mistgate/admin/v1/subscription_host_pb";
import { en } from "@/i18n/en";
import { DomainsPage } from "./domains";

// Settings -> Addresses: the subscription host card in each of its four states.

const auth = vi.hoisted(() => ({ me: vi.fn() }));
const instance = vi.hoisted(() => ({ getInstance: vi.fn() }));
const nodes = vi.hoisted(() => ({ listNodes: vi.fn() }));
const subHost = vi.hoisted(() => ({
  getSubscriptionHost: vi.fn(),
  setSubscriptionHost: vi.fn(),
  startSubscriptionHostMove: vi.fn(),
  finishSubscriptionHostChange: vi.fn(),
  cancelSubscriptionHostChange: vi.fn(),
  disableSubscriptionHost: vi.fn(),
  checkSubscriptionHostDomain: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({ ...(await orig<typeof import("@/lib/api")>()), auth, instance, nodes, subHost }));
// the select as a native one: what matters here is what a pick does
vi.mock("@/components/ui/select", () => ({
  Select: ({ value, onValueChange, options, ...rest }: { value: string; onValueChange: (v: string) => void; options: { value: string; label: string }[]; "aria-label": string }) => (
    <select aria-label={rest["aria-label"]} value={value} onChange={(e) => onValueChange(e.target.value)}>
      {options.map((o) => (
        <option key={o.value} value={o.value}>
          {o.label}
        </option>
      ))}
    </select>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const base = { domain: "", nodeId: "", nextNodeId: "", publicUrl: "", tunnels: [], pending: [], caaRecord: "", caaStatus: "", lackingLink: [] };
const withState = (state: SubscriptionHostState, more: object = {}) => subHost.getSubscriptionHost.mockResolvedValue({ host: { ...base, state, ...more } });

beforeEach(() => {
  auth.me.mockResolvedValue({ admin: { id: "adm_1", displayName: "Alice", role: Role.OWNER }, unavailable: [] });
  instance.getInstance.mockResolvedValue({ instance: { adminUrl: "https://admin.example/x/", subscriptionBase: "https://sub.example.com/s/" } });
  nodes.listNodes.mockResolvedValue({
    nodes: [
      { id: "nod_a", name: "de1", address: "192.0.2.1" },
      { id: "nod_b", name: "nl1", address: "192.0.2.2" },
    ],
  });
  subHost.checkSubscriptionHostDomain.mockResolvedValue({ matches: false, found: ["198.51.100.9"], expected: ["192.0.2.2"] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [auth, instance, nodes, subHost]) Object.values(m).forEach((f) => f.mockReset());
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DomainsPage />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 5; i++) await settle();
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const click = async (b: Element | undefined | null) => {
  await act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 3; i++) await settle();
};

describe("Settings → Addresses → subscription host", () => {
  it("off: asks for a node and a subdomain", async () => {
    withState(SubscriptionHostState.OFF);
    await mount();
    expect(text()).toContain(en["set.domains.subHost.title"]);
    expect(button(en["set.domains.subHost.enable"])).toBeDefined();
    expect(button(en["set.domains.subHost.saveAnyway"])).toBeUndefined();
    expect(text()).not.toContain(en["set.domains.subHost.via"]);
  });

  it("on: the domain, the node, its tunnel, the CAA record, move and turn off", async () => {
    withState(SubscriptionHostState.ON, { domain: "sub.example.com", nodeId: "nod_a", tunnels: [{ nodeId: "nod_a", up: true, sinceUnix: 0n }], caaRecord: '0 issue "letsencrypt.org"', caaStatus: "absent" });
    await mount();
    expect(text()).toContain("sub.example.com");
    expect(text()).toContain(en["set.domains.subHost.onNode"].replace("{node}", "de1"));
    expect(text()).toContain(en["set.domains.subHost.caa.absent"]);
    expect(text()).toContain(en["set.domains.subHost.via"]);
    expect(button(en["set.domains.subHost.move"])).toBeDefined();
    expect(button(en["set.domains.subHost.turnOff"])).toBeDefined();
  });

  it("moving: both nodes, the record to change and where it points now", async () => {
    withState(SubscriptionHostState.MOVING, { domain: "sub.example.com", nodeId: "nod_a", nextNodeId: "nod_b" });
    await mount();
    expect(text()).toContain(en["set.domains.subHost.moving"].replace("{from}", "de1").replace("{to}", "nl1"));
    expect(text()).toContain("198.51.100.9");
    expect(subHost.checkSubscriptionHostDomain).toHaveBeenCalledWith({ nodeId: "nod_b", domain: "sub.example.com" });
    expect(button(en["set.domains.subHost.finish"])).toBeDefined();
    expect(button(en["set.domains.subHost.cancel"])).toBeDefined();
  });

  it("leaving: the nodes still on the old address, and a forced finish only after the panel says they are pending", async () => {
    withState(SubscriptionHostState.LEAVING, { domain: "sub.example.com", nodeId: "nod_a", publicUrl: "https://panel.example.com", pending: [{ nodeId: "nod_b", name: "nl1", via: "sub.example.com:443" }] });
    subHost.finishSubscriptionHostChange.mockRejectedValueOnce(new ConnectError("sub_host_nodes_pending: nodes=nod_b", Code.FailedPrecondition));
    await mount();
    expect(text()).toContain("nl1");
    expect(button(en["set.domains.subHost.forceYes"])).toBeUndefined();
    await click(button(en["set.domains.subHost.finish"]));
    subHost.finishSubscriptionHostChange.mockResolvedValue({ host: { ...base, state: SubscriptionHostState.OFF } });
    await click(button(en["set.domains.subHost.forceYes"]));
    expect(subHost.finishSubscriptionHostChange).toHaveBeenLastCalledWith({ force: true });
  });

  it("offers Save anyway only after the panel refuses the DNS record", async () => {
    withState(SubscriptionHostState.OFF);
    await mount();
    // the form's own state: fill it through the card's handlers
    const field = document.querySelector<HTMLInputElement>('input[placeholder="sub.example.com"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "sub.example.com");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    const select = document.querySelector<HTMLSelectElement>(`select[aria-label="${en["set.domains.subHost.node"]}"]`)!;
    await act(async () => {
      select.value = "nod_a";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    subHost.setSubscriptionHost.mockRejectedValueOnce(new ConnectError("sub_host_dns_mismatch: expected=192.0.2.1&found=198.51.100.9", Code.FailedPrecondition));
    await click(button(en["set.domains.subHost.enable"]));
    expect(button(en["set.domains.subHost.saveAnyway"])).toBeDefined();
    subHost.setSubscriptionHost.mockResolvedValue({ host: { ...base, state: SubscriptionHostState.ON, domain: "sub.example.com", nodeId: "nod_a" } });
    await click(button(en["set.domains.subHost.saveAnyway"]));
    expect(subHost.setSubscriptionHost).toHaveBeenLastCalledWith({ nodeId: "nod_a", domain: "sub.example.com", skipDnsCheck: true });
  });
});
