import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { CreateUserModal } from "./create-user";

const listGroups = vi.fn();
const listProfiles = vi.fn();
const createUser = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: { listGroups: (...a: unknown[]) => listGroups(...a), createGroup: vi.fn(), updateGroup: vi.fn() },
  profiles: { listProfiles: (...a: unknown[]) => listProfiles(...a), listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", apps: [App.HAPP] }, { id: "awg", apps: [App.AMNEZIA] }] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  users: { createUser: (...a: unknown[]) => createUser(...a) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }) },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listGroups, listProfiles, createUser]) m.mockReset();
});

const group = (over: Record<string, unknown> = {}) =>
  ({ id: "grp_all", name: "Все", profileIds: ["p_hy"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0, ...over }) as unknown as Group;
const profiles = [{ id: "p_hy", name: "hy2 · 443", protocol: "hysteria2" }];

async function mount(groups: Group[], ps = profiles) {
  listGroups.mockResolvedValue({ groups });
  listProfiles.mockResolvedValue({ profiles: ps });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <CreateUserModal open onOpenChange={() => {}} onCreated={() => {}} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const switches = () => [...document.querySelectorAll("[role=switch]")].map((s) => s.getAttribute("aria-checked"));
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the new user form", () => {
  it("starts in “Everyone”, says what the person gets, and switches off the way the group gives nothing for", async () => {
    createUser.mockResolvedValue({});
    await mount([group({ id: "grp_f", name: "friends", amneziaNodes: 1 }), group()]);
    expect(document.querySelector("[role=combobox]")?.textContent).toContain("Все");
    expect(text()).toContain("Subscription (Happ) — 2 nodes");
    expect(text()).toContain("AmneziaVPN keys — no: no AmneziaWG of the group runs on a node");
    // Happ on, AmneziaVPN off with the reason, "all nodes" on
    expect(switches()).toEqual(["true", "false", "true"]);
    expect(text()).toContain("Off: no AmneziaWG of the group runs on a node");
    expect(text()).not.toContain("gets nothing yet");

    await type(document.querySelector<HTMLInputElement>("input[placeholder='E.g. Marina']")!, "Marina");
    await click(button("Create"));
    await settle();
    expect(createUser).toHaveBeenCalledWith(expect.objectContaining({ name: "Marina", groupId: "grp_all", apps: { happ: true, amnezia: false } }));
  });

  it("says in red when the group gives nothing at all, and leaves Happ on", async () => {
    await mount([group({ happNodes: 0, amneziaNodes: 0 })]);
    expect(text()).toContain("This person gets nothing yet: no profile of the group “Все” runs on a node.");
    expect(document.querySelector("[role=alert]")).not.toBeNull();
    expect(switches().slice(0, 2)).toEqual(["true", "false"]);
  });

  it("shows the group form inline only when there is no group at all, without a DNS of the group, and its button is not the main one", async () => {
    await mount([]);
    expect(text()).toContain("Create a group first");
    const add = button("Add group")!;
    expect(add.className).not.toContain("bg-accent");
    // one DNS choice, the person's own, waiting under "More" (the group's is not asked here)
    expect(document.querySelectorAll("[aria-label='DNS']")).toHaveLength(1);
    expect(document.querySelector("details summary")?.textContent).toContain("More");
  });

  it("without a profile says to make one first, with the way to Profiles", async () => {
    await mount([group({ profileIds: [], happNodes: 0 })], []);
    expect(text()).toContain("Create a profile first and put it on a node.");
    expect(document.querySelector("a[href='/profiles']")?.textContent).toContain("Profiles");
  });
});
