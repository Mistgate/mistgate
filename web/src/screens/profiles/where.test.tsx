import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { WherePanel } from "./where";

const listGroups = vi.fn();
const updateGroup = vi.fn();
const createGroup = vi.fn();
const listNodes = vi.fn();
const createInbound = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  groups: { listGroups: (...a: unknown[]) => listGroups(...a), updateGroup: (...a: unknown[]) => updateGroup(...a), createGroup: (...a: unknown[]) => createGroup(...a) },
  nodes: { listNodes: (...a: unknown[]) => listNodes(...a) },
  profiles: {
    listProfiles: () => Promise.resolve({ profiles: [{ id: "prf_1", name: "Amnezia 3.1 test", protocol: "awg" }] }),
    listProtocols: () => Promise.resolve({ protocols: [] }),
    createInbound: (...a: unknown[]) => createInbound(...a),
    twinProfile: () => Promise.resolve({ name: "x", egress: "warp", port: 8443, nodeIds: [], groupIds: [], hopDropped: false }),
  },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));
// no router in the test: a link is an anchor that carries its target
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(search ?? {})} className={className} aria-label={(rest as { "aria-label"?: string })["aria-label"]}>
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
  role = Role.OWNER;
  listNodes.mockResolvedValue({ nodes: [] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listGroups, updateGroup, createGroup, listNodes, createInbound]) m.mockReset();
});

const profile = (over: Record<string, unknown> = {}) => ({ id: "prf_1", name: "Amnezia 3.1 test", protocol: "awg", nodeCount: 1, userCount: 7, version: 1, ...over }) as unknown as ProfileSummary;
const inbound = (over: Record<string, unknown> = {}) => ({ id: "inb_1", profileId: "prf_1", nodeId: "nod_1", nodeName: "de1", port: 51820, state: InboundState.ACTIVE, lastError: "", ...over }) as unknown as Inbound;
const group = (over: Record<string, unknown> = {}) => ({ id: "grp_1", name: "family", profileIds: ["prf_1"], userCount: 5, dnsPresetId: "", happNodes: 0, amneziaNodes: 1, ...over }) as unknown as Group;
const node = (id: string, name: string, over: Record<string, unknown> = {}) => ({ id, name, status: NodeStatus.ONLINE, countryCode: "DE", address: `${name}.example.com`, protocols: [], awgBackend: "auto", ...over });

async function mount(p: ProfileSummary, inbounds: Inbound[], groups: Group[], extra: { cert?: { tlsMode?: string; sni?: string }; twin?: { egress: "direct" | "warp"; dirty: boolean } } = {}) {
  listGroups.mockResolvedValue({ groups });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <WherePanel profile={p} inbounds={inbounds} {...extra} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = async () => {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const dialog = () => document.querySelector("[role=dialog]");
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the status line and the three columns", () => {
  it("says what works, and every name is a link: the node to its profiles, the group to its editor, the users to the list", async () => {
    await mount(profile(), [inbound()], [group(), group({ id: "grp_2", name: "friends", userCount: 2 })]);
    expect(text()).toContain("Working: 1 node, 2 groups, 7 users");
    const nodeLink = [...document.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent === "de1")!;
    expect(nodeLink.dataset.search).toBe(JSON.stringify({ tab: "profiles" }));
    expect(text()).toContain(":51820");
    const groupLink = document.querySelector<HTMLAnchorElement>('a[aria-label="Open group “friends”"]')!;
    expect(groupLink.dataset.search).toBe(JSON.stringify({ tab: "groups", group: "grp_2" }));
    // users per group link to the users list filtered by that group
    const users = document.querySelector<HTMLAnchorElement>('a[aria-label="Users of group “family”"]');
    expect(users?.dataset.search).toBe(JSON.stringify({ group: "grp_1" }));
    expect(users?.textContent).toContain("5 users");
  });

  it("warns that there is nothing to give when the profile is on no node, with the action right there", async () => {
    await mount(profile({ userCount: 0 }), [], [group()]);
    expect(text()).toContain("Not on any node: there is nothing to give users");
    expect(text()).toContain("The profile starts working once it is put on a node.");
    expect(button("Put on nodes")).toBeDefined();
  });

  it("warns that nobody gets it when the profile is in no group", async () => {
    await mount(profile({ userCount: 0 }), [inbound()], [group({ profileIds: ["prf_other"] })]);
    expect(text()).toContain("In no group: nobody will get it");
    expect(text()).toContain("Nobody: the profile is in no group");
    expect(button("Add to a group")).toBeDefined();
  });

  it("does not say it works while no inbound is up, and shows the error", async () => {
    await mount(profile(), [inbound({ state: InboundState.FAILED, lastError: "bind: address in use" })], [group()]);
    expect(text()).toContain("Put on nodes, but not running yet");
    expect(text()).toContain("bind: address in use");
    expect(text()).not.toContain("Working:");
  });

  it("keeps the WARP copy as the last line of the block", async () => {
    await mount(profile({ protocol: "hysteria2" }), [inbound()], [group()], { twin: { egress: "direct", dirty: false } });
    expect(button("WARP copy")).toBeDefined();
    expect(text()).toContain("The same server leaving through WARP");
  });
});

describe("AmneziaWG", () => {
  it("explains that devices are made per user, and only for AmneziaWG", async () => {
    await mount(profile(), [inbound()], [group()]);
    expect(text()).toContain("devices (keys) are made per user");
    act(() => root?.unmount());
    await mount(profile({ protocol: "hysteria2" }), [inbound()], [group()]);
    expect(text()).not.toContain("devices (keys) are made per user");
  });
});

describe("roles", () => {
  it("shows no actions to a read-only admin", async () => {
    role = Role.READONLY;
    await mount(profile({ userCount: 0 }), [], []);
    expect(text()).toContain("Not on a node and in no group");
    expect(document.querySelectorAll("button").length).toBe(0);
    act(() => root?.unmount());
    await mount(profile(), [inbound()], [group()]);
    expect(text()).toContain("Working:");
    expect(document.querySelectorAll("button").length).toBe(0);
  });

  it("lets a helper work with groups but not put the profile on a node", async () => {
    role = Role.HELPER;
    await mount(profile(), [inbound()], [group()]);
    expect(button("Edit")).toBeDefined();
    expect(button("Add to a group")).toBeDefined();
    expect(button("Put on nodes")).toBeUndefined();
  });
});

describe("adding to a group", () => {
  it("offers only the groups without the profile and calls UpdateGroup with the old set plus this profile", async () => {
    updateGroup.mockResolvedValue({ group: group() });
    await mount(profile({ userCount: 0 }), [inbound()], [group({ id: "grp_9", name: "has-it", profileIds: ["prf_1", "prf_2"] }), group({ id: "grp_2", name: "friends", profileIds: ["prf_2"], userCount: 3 })]);
    await click(button("Add to a group"));
    await settle();
    expect(dialog()!.textContent).toContain("friends");
    expect(dialog()!.textContent).not.toContain("has-it");

    await click(dialog()!.querySelector('button[aria-label="Add: friends"]'));
    await settle();
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_2", profileIds: { values: ["prf_2", "prf_1"] } });
  });

  it("with nowhere to add it, makes a new group with this profile already in it", async () => {
    createGroup.mockResolvedValue({ group: group({ id: "grp_new", name: "Friends" }) });
    await mount(profile({ userCount: 0 }), [inbound()], [group()]);
    await click(button("Add to a group"));
    await settle();
    expect(dialog()!.textContent).toContain("Nowhere to add it");
    await click(button("Create a group with this profile"));
    await settle();
    const name = document.querySelector<HTMLInputElement>("[role=dialog] input")!;
    expect(name.placeholder).toBe("E.g. Friends");
    expect(button("Amnezia 3.1 test")?.getAttribute("aria-pressed")).toBe("true");
    await type(name, "Friends");
    await click(button("Create group"));
    await settle();
    expect(createGroup).toHaveBeenCalledWith({ name: "Friends", profileIds: ["prf_1"], dnsPresetId: "" });
  });
});

describe("putting it on nodes", () => {
  it("lists the nodes without the profile, puts it on the ticked ones in one go and says the result of each", async () => {
    listNodes.mockResolvedValue({ nodes: [node("nod_1", "de1"), node("nod_2", "fi1"), node("nod_3", "nl1"), node("nod_4", "old", { status: NodeStatus.RETIRED })] });
    createInbound.mockImplementation(async (r: { nodeId: string }) => {
      if (r.nodeId === "nod_3") throw new ConnectError('UDP port 51820 is already used by profile "Main" on this node', Code.AlreadyExists);
      return { inbound: inbound({ nodeId: r.nodeId }) };
    });
    await mount(profile({ userCount: 0 }), [inbound()], []);
    await click(button("Put on nodes"));
    await settle();
    // de1 has it already, "old" is retired
    const rows = [...dialog()!.querySelectorAll("li")].map((li) => li.textContent);
    expect(rows).toHaveLength(2);
    expect(rows.join()).not.toContain("de1");
    expect((button("Put on 0 nodes") as HTMLButtonElement).disabled).toBe(true);

    await click(document.querySelector('[role=checkbox][aria-label="All nodes"]'));
    await click(button("Put on 2 nodes"));
    await settle();
    expect(createInbound).toHaveBeenCalledTimes(2);
    expect(createInbound).toHaveBeenCalledWith({ profileId: "prf_1", nodeId: "nod_2", portOverride: 0, tlsServerNameOverride: "" });
    const d = dialog()!.textContent!;
    expect(d).toContain("Put on: it starts in a few seconds"); // fi1
    expect(d).toContain("Port 51820 on nl1 is taken by the profile “Main”."); // nl1, and fi1 is not undone

    // the refused node gets a port field; the retry goes for it alone, with the new port
    const port = dialog()!.querySelector<HTMLInputElement>('input[inputmode="numeric"]')!;
    await type(port, "51821");
    createInbound.mockResolvedValue({ inbound: inbound({ nodeId: "nod_3" }) });
    await click(button("Try 1 node again"));
    await settle();
    expect(createInbound).toHaveBeenLastCalledWith({ profileId: "prf_1", nodeId: "nod_3", portOverride: 51821, tlsServerNameOverride: "" });
    expect(button("Done")).toBeDefined();
  });

  it("asks for a domain before the click where Let's Encrypt cannot work on an IP, and sends it", async () => {
    listNodes.mockResolvedValue({ nodes: [node("nod_2", "fi1", { address: "203.0.113.10" })] });
    createInbound.mockResolvedValue({ inbound: inbound() });
    await mount(profile({ protocol: "hysteria2" }), [], [group()], { cert: { tlsMode: "acme_domain", sni: "" } });
    await click(button("Put on nodes"));
    await settle();
    // the only node is ticked from the start, and its row asks for the domain already
    expect(dialog()!.textContent).toContain("the address of fi1 is the IP 203.0.113.10");
    const domain = dialog()!.querySelector<HTMLInputElement>('input[aria-label="Domain (SNI) for fi1"]')!;
    await type(domain, "fi1.example.com");
    await click(button("Put on 1 node"));
    await settle();
    expect(createInbound).toHaveBeenCalledWith({ profileId: "prf_1", nodeId: "nod_2", portOverride: 0, tlsServerNameOverride: "fi1.example.com" });
  });

  it("takes the free port a coded refusal names", async () => {
    listNodes.mockResolvedValue({ nodes: [node("nod_2", "fi1")] });
    createInbound.mockRejectedValueOnce(new ConnectError("port_taken: port=443&profile=Main&free=8443", Code.AlreadyExists));
    await mount(profile({ protocol: "hysteria2" }), [], [group()]);
    await click(button("Put on nodes"));
    await settle();
    await click(button("Put on 1 node"));
    await settle();
    expect(dialog()!.textContent).toContain("Port 443 on fi1 is taken by the profile “Main”; 8443 is free.");
    expect(dialog()!.querySelector<HTMLInputElement>('input[inputmode="numeric"]')!.value).toBe("8443");
  });
});
