import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { hy2, vless } from "./schema.fixtures";
import { validateNewProfileSearch } from "./search";
import { ProfileNewScreen } from "./wizard";

// The new-profile wizard: Task, Settings, Nodes, People, Done. What it calls is what the old form called (CreateProfile, the groups'
// own calls, CreateInbound per node); what it asks before the click is CreateInbound with validate_only and no profile yet.

const createProfile = vi.fn();
const createInbound = vi.fn();
const updateGroup = vi.fn();
const createGroup = vi.fn();
const getProfile = vi.fn();
let role = Role.OWNER;
let nodeList: unknown[] = [];
let groupList: unknown[] = [];

const awgSchema = JSON.stringify({ type: "object", properties: { version: { type: "string", enum: ["3.1", "2.0"], title: "Protocol version", "x-group": "basics", "x-order": 10 }, port: { type: "integer", title: "UDP port", "x-group": "basics", "x-order": 20 } } });
const generateObfuscation = vi.fn();
const mtSchema = JSON.stringify({ type: "object", properties: { port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10 } } });
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    listProtocols: () =>
      Promise.resolve({
        protocols: [
          { id: "awg", displayName: "AmneziaWG", settingsSchemaJson: awgSchema, defaultSettingsJson: JSON.stringify({ version: "3.1", port: 47213, mtu: 1280, subnet4: "10.66.0.0/22", subnet6: "fd00::/64", obfuscation: { preset: "dns", per_device_signature: true, s1: 10 } }), apps: [App.AMNEZIA] },
          { id: "mtproto", displayName: "MTProto", settingsSchemaJson: mtSchema, defaultSettingsJson: JSON.stringify({ port: 443 }), apps: [App.TELEGRAM] },
          { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: hy2, defaultSettingsJson: JSON.stringify({ port: 443, tls_mode: "acme_domain", obfs: { type: "salamander", password: "demo-secret" } }), apps: [App.HAPP] },
          { id: "vless", displayName: "VLESS", settingsSchemaJson: vless, defaultSettingsJson: JSON.stringify({ port: 443, transport: "xhttp", security: "reality", reality: { target_kind: "self", server_names: [], short_ids: [], fingerprint: "chrome" }, xhttp: { mode: "auto", x_padding_bytes: "100-1000" }, egress: "direct" }), apps: [App.HAPP] },
        ],
      }),
    listProfiles: () => Promise.resolve({ profiles: [{ id: "prf_old", name: "Hysteria2 · 8443", protocol: "hysteria2" }] }),
    previewProfile: () => Promise.resolve({ errors: [], warnings: [], clientPreview: "hysteria2://••••@node.example.org:443/", clientLabel: "", formats: [] }),
    createProfile: (...a: unknown[]) => createProfile(...a),
    createInbound: (...a: unknown[]) => createInbound(...a),
    getProfile: (...a: unknown[]) => getProfile(...a),
    updateInbound: () => Promise.resolve({}),
  },
  groups: { listGroups: () => Promise.resolve({ groups: groupList }), updateGroup: (...a: unknown[]) => updateGroup(...a), createGroup: (...a: unknown[]) => createGroup(...a) },
  nodes: { listNodes: () => Promise.resolve({ nodes: nodeList }) },
  relays: { listRelays: () => Promise.resolve({ relays: [] }) },
  health: { getChecks: () => Promise.resolve({ nowUnix: 0, intervalS: 300, columns: [], rows: [], countryChecks: [] }) },
  awg: { generateObfuscation: (...a: unknown[]) => generateObfuscation(...a), listMimicryPresets: () => Promise.resolve({ presets: [], domains: [] }) },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 12, apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
}));

// the router: navigate moves the URL of /profiles/new (and re-renders); anything else is only recorded
let search: Record<string, unknown> = {};
let rerender = () => {};
const away = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useBlocker: () => ({ status: "idle" }),
  useParams: () => ({}),
  useSearch: () => search,
  useNavigate: () => (o: { to?: string; search?: Record<string, unknown> }) => {
    if (o.to === "/profiles/new") {
      search = validateNewProfileSearch(o.search ?? {});
      rerender();
    } else away(o);
    return Promise.resolve();
  },
  Link: ({ children, to, className, onClick }: { children?: ReactNode; to: string; className?: string; onClick?: (e: unknown) => void }) => (
    <a href={to} className={className} onClick={onClick}>
      {children}
    </a>
  ),
}));

const node = (n: number, over: Record<string, unknown> = {}) => ({ id: `nod_${n}`, name: `de${n}`, countryCode: "DE", address: `de${n}.example.com`, status: NodeStatus.ONLINE, protocols: [], awgBackend: "auto", awgPrepare: { supported: true }, ...over });
const group = (id: string, name: string, userCount: number, profileIds: string[] = []) => ({ id, name, color: "sky", profileIds, userCount, dnsPresetId: "", happNodes: 0, amneziaNodes: 0 });

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  search = {};
  role = Role.OWNER;
  nodeList = [node(1), node(2)];
  groupList = [group("grp_f", "friends", 14), group("grp_h", "family", 9, ["prf_old"])];
  createProfile.mockResolvedValue({ profile: { id: "prf_new", name: "Hysteria2 · 443", protocol: "hysteria2" } });
  createInbound.mockImplementation((r: { nodeId: string; validateOnly?: boolean; portOverride?: number }) => Promise.resolve({ inbound: { id: `inb_${r.nodeId}`, port: r.portOverride || 443 }, warnings: [], freePort: 0 }));
  updateGroup.mockResolvedValue({});
  createGroup.mockResolvedValue({});
  getProfile.mockResolvedValue({ profile: { id: "prf_new" }, settingsJson: "{}", inbounds: [{ id: "inb_nod_1", nodeId: "nod_1", state: InboundState.ACTIVE, port: 443 }, { id: "inb_nod_2", nodeId: "nod_2", state: InboundState.ACTIVE, port: 443 }] });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [createProfile, createInbound, updateGroup, createGroup, getProfile, generateObfuscation, away]) m.mockReset();
});

const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const draw = () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <ProfileNewScreen />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    );
  rerender = () => act(draw);
  await act(async () => draw());
  await settle();
}
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll<HTMLElement>("button, a")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (el: Element | undefined | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const type = (box: HTMLInputElement | null | undefined, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(box, value);
    box!.dispatchEvent(new Event("input", { bubbles: true }));
  });
const next = async () => {
  await click(button("Next"));
  await settle();
};
const cards = () => [...document.querySelectorAll<HTMLElement>("[role=radiogroup] [role=radio]")];
const card = (name: string) => cards().find((c) => c.textContent!.includes(name))!;
const nameBox = () => document.querySelector<HTMLInputElement>("input[data-w=name]");
const portBox = () => document.querySelector<HTMLInputElement>("input[data-w=port]");
const nodeRow = (n: string) => [...document.querySelectorAll(".pw-nodes .pw-pn")].find((r) => r.querySelector("b")?.textContent === n)!;
const ticked = (n: string) => nodeRow(n).querySelector("[role=checkbox]")!.getAttribute("aria-checked");
const group3 = (n: string) => [...document.querySelectorAll(".pw-gr")].find((r) => r.textContent!.includes(n))!;

describe("step 1, the task", () => {
  it("offers the tasks as cards of one radio group, the first chosen, and goes on with the chosen one: the URL keeps the step", async () => {
    search = { node: "nod_1" };
    await mount();
    expect(document.querySelector("[role=radiogroup]")?.getAttribute("aria-label")).toBe("Task");
    // the tasks of the board that have a protocol here, and "I'll pick it myself" (the DNS tunnel has no backend: no card)
    expect(cards().map((c) => c.querySelector("b")?.textContent)).toEqual(["Fast, for most people", "For phones in Russia", "For a router and AmneziaVPN", "Via a CDN", "Telegram without a VPN", "I’ll pick the protocol myself"]);
    expect(text()).not.toContain("DNS tunnel");
    expect(card("Fast, for most people").getAttribute("aria-checked")).toBe("true");
    expect(card("Fast, for most people").textContent).toContain("Start with this one");
    expect(card("Telegram without a VPN").textContent).toContain("Experimental");
    await click(card("Telegram without a VPN"));
    expect(card("Telegram without a VPN").getAttribute("aria-checked")).toBe("true");
    expect(card("Fast, for most people").getAttribute("aria-checked")).toBe("false");
    await next();
    expect(search).toEqual({ node: "nod_1", protocol: "mtproto" });
    expect(cards()).toHaveLength(0);
    expect(nameBox()!.value).toBe("Telegram · 443");
    expect(text()).toContain("Telegram without a VPN · MTProto");
    expect(document.querySelectorAll(".pw-steps li.pw-done")).toHaveLength(1);
  });

  it("skips the task when the URL names a protocol, and 'Change' goes back to the tasks", async () => {
    search = { protocol: "vless" };
    await mount();
    expect(cards()).toHaveLength(0);
    expect(nameBox()!.value).toBe("VLESS · 443");
    expect(text()).toContain("For phones in Russia · VLESS");
    await click(button("Change"));
    await settle();
    expect(search).toEqual({});
    expect(cards()).toHaveLength(6);
    expect(card("For phones in Russia").getAttribute("aria-checked")).toBe("true");
  });

  it("opens the CDN task for ?protocol=vless&task=cdn (the node page's \"Add a CDN\") and keeps it in the URL", async () => {
    search = { protocol: "vless", task: "cdn", node: "nod_1" };
    await mount();
    expect(nameBox()!.value).toBe("VLESS · CDN · 443");
    expect(document.querySelector("input[aria-label='CDN host']")).not.toBeNull();
    await click(button("Change"));
    await settle();
    expect(search).toEqual({ node: "nod_1" });
    expect(card("Via a CDN").getAttribute("aria-checked")).toBe("true");
    await next();
    expect(search).toEqual({ node: "nod_1", protocol: "vless", task: "cdn" });
  });

  it("opens on the tasks when the URL names a protocol there is none of", async () => {
    search = { protocol: "nope" };
    await mount();
    expect(cards()).toHaveLength(6);
  });

  it("is the owner's: the others get the reason and the way back", async () => {
    role = Role.HELPER;
    await mount();
    expect(text()).toContain("Only the owner can change profiles.");
    expect(cards()).toHaveLength(0);
    await click(button("Back to profiles"));
    expect(away).toHaveBeenCalledWith(expect.objectContaining({ to: "/profiles" }));
  });
});

describe("step 2, the settings", () => {
  beforeEach(() => {
    search = { protocol: "hysteria2" };
  });

  it("names the profile after the port until a name is typed, and stops at a port that is no port or a name that is taken", async () => {
    await mount();
    expect(nameBox()!.value).toBe("Hysteria2 · 443");
    await type(portBox(), "9443");
    expect(nameBox()!.value).toBe("Hysteria2 · 9443");
    await click(button("8443"));
    expect(nameBox()!.value).toBe("Hysteria2 · 8443");
    // the name is taken (a profile of that name exists): the bar is off and the field says why
    expect(text()).toContain("The name is taken: pick another");
    expect(button("Next")!.getAttribute("aria-disabled")).toBe("true");
    await type(nameBox(), "Mine");
    expect(button("Next")!.getAttribute("aria-disabled")).toBe("false");
    await type(portBox(), "70000");
    expect(text()).toContain("The port is a number from 1 to 65535");
    expect(button("Next")!.getAttribute("aria-disabled")).toBe("true");
    expect(document.querySelector(".pw-bar span")?.textContent).toBe("Fix the marked fields");
    await next();
    expect(document.querySelectorAll(".pw-steps li.pw-cur")[0]?.textContent).toContain("Settings");
    await type(nameBox(), "");
    expect(text()).toContain("Give the profile a name");
  });

  it("shows what the client will see and where it works (the server's preview), and opens All settings with the whole form", async () => {
    await mount();
    expect(text()).toContain("hysteria2://••••@node.example.org:443/");
    expect(document.querySelector(".pw-def")).toBeNull();
    await click(button("All settings"));
    expect(document.querySelector(".pw-def")?.textContent).toContain("Certificate");
    // the protocol's own form, with its groups (what the task does not ask for is here)
    expect(text()).toContain("Obfuscation password");
    await click(button("All settings"));
    expect(document.querySelector(".pw-def")).toBeNull();
  });

  it("asks the server for a whole new block when AmneziaWG goes to 2.0, and names the profile after the version", async () => {
    search = {};
    generateObfuscation.mockResolvedValue({ obfuscationJson: JSON.stringify({ s1: 15, jc: 4 }) });
    await mount();
    await click(card("For a router and AmneziaVPN"));
    await next();
    expect(nameBox()!.value).toBe("AWG 3.1");
    expect(document.querySelector("input[aria-label='UDP port']")).not.toBeNull();
    await click(button("Yes — version 2.0"));
    await settle();
    expect(generateObfuscation).toHaveBeenCalledWith(expect.objectContaining({ version: "2.0", preset: "dns" }));
    expect(nameBox()!.value).toBe("AWG 2.0");
    expect(document.querySelectorAll("[role=radio][aria-checked=true]")[0]?.textContent).toBe("Yes — version 2.0");
  });

  it("leaves AmneziaWG's client networks to the server", async () => {
    search = { protocol: "awg" };
    await mount();
    await next();
    await next();
    await click(button("Create and put on 2 nodes"));
    await settle();
    const settings = JSON.parse((createProfile.mock.calls[0]![0] as { settingsJson: string }).settingsJson) as Record<string, unknown>;
    expect(settings).not.toHaveProperty("subnet4");
    expect(settings).not.toHaveProperty("subnet6");
    expect(settings.version).toBe("3.1");
  });

  it("asks for the CDN host on the CDN task and puts it into the settings", async () => {
    search = {};
    await mount();
    await click(card("Via a CDN"));
    await next();
    expect(nameBox()!.value).toBe("VLESS · CDN · 443");
    expect(button("Next")!.getAttribute("aria-disabled")).toBe("true");
    expect(text()).toContain("Enter the CDN host");
    await type(document.querySelector("input[aria-label='CDN host']"), "cdn.example.org");
    expect(button("Next")!.getAttribute("aria-disabled")).toBe("false");
  });
});

describe("step 3, the nodes", () => {
  beforeEach(() => {
    search = { protocol: "hysteria2" };
  });
  const toNodes = async () => {
    await mount();
    await next();
  };

  it("ticks every node that can take the profile, and the check before the click is the one with no profile yet", async () => {
    await toNodes();
    expect(ticked("de1")).toBe("true");
    expect(ticked("de2")).toBe("true");
    expect(nodeRow("de1").textContent).toContain("OK · port 443 is free");
    expect(document.querySelector(".pw-all2 [role=checkbox]")?.getAttribute("aria-checked")).toBe("true");
    const asks = createInbound.mock.calls.map((c) => c[0] as { profileId: string; protocol: string; validateOnly: boolean; nodeId: string; settingsJson: string });
    expect(asks.length).toBeGreaterThanOrEqual(2);
    expect(asks.every((a) => a.profileId === "" && a.protocol === "hysteria2" && a.validateOnly)).toBe(true);
    expect(JSON.parse(asks[0]!.settingsJson)).toMatchObject({ port: 443, tls_mode: "acme_domain" });
    expect(document.querySelector(".pw-bar span")?.textContent).toBe("2 nodes ticked");
    // one tick off, "All suitable" follows it
    await click(nodeRow("de2").querySelector("[role=checkbox]"));
    expect(ticked("de2")).toBe("false");
    expect(document.querySelector(".pw-all2 [role=checkbox]")?.getAttribute("aria-checked")).toBe("false");
    await click(document.querySelector(".pw-all2 [role=checkbox]"));
    expect(ticked("de2")).toBe("true");
  });

  it("ticks only the node of ?node=", async () => {
    search = { protocol: "hysteria2", node: "nod_2" };
    await toNodes();
    expect(ticked("de1")).toBe("false");
    expect(ticked("de2")).toBe("true");
  });

  it("asks for a domain where the address is an IP, and puts it into the check", async () => {
    nodeList = [node(1), node(2, { address: "203.0.113.20" })];
    createInbound.mockImplementation((r: { nodeId: string; tlsServerNameOverride?: string }) => (r.nodeId === "nod_2" && !r.tlsServerNameOverride ? Promise.reject(new ConnectError("acme_needs_domain: address=203.0.113.20", Code.InvalidArgument)) : Promise.resolve({ inbound: { port: 443 }, warnings: [], freePort: 0 })));
    await toNodes();
    expect(ticked("de2")).toBe("false");
    expect(nodeRow("de2").className).toContain("need");
    const dom = nodeRow("de2").querySelector<HTMLInputElement>("input[aria-label='Domain (SNI)']")!;
    expect(dom.placeholder).toBe("vpn.example.com");
    expect(nodeRow("de2").textContent).toContain("…or a self-signed certificate");
    await type(dom, "vpn.example.com");
    await click(button("Enter domain"));
    await settle();
    expect(createInbound.mock.calls.some((c) => (c[0] as { tlsServerNameOverride?: string; nodeId: string }).tlsServerNameOverride === "vpn.example.com")).toBe(true);
    expect(nodeRow("de2").textContent).toContain("OK · domain entered");
    expect(ticked("de2")).toBe("true");
  });

  it("takes the port the server proved free where the profile's own is taken", async () => {
    createInbound.mockImplementation((r: { nodeId: string; validateOnly?: boolean; portOverride?: number }) =>
      r.nodeId === "nod_1" && !r.portOverride ? Promise.reject(new ConnectError("port_taken: port=443&profile=Site · 443&free=8443", Code.AlreadyExists)) : Promise.resolve({ inbound: { id: "inb", port: r.portOverride || 443 }, warnings: [], freePort: 0 }),
    );
    await toNodes();
    expect(ticked("de1")).toBe("true");
    expect(nodeRow("de1").querySelector<HTMLInputElement>("input[aria-label='Port on the node']")!.value).toBe("8443");
    await next();
    await next();
    await click(button("Create and put on 2 nodes"));
    await settle();
    const real = createInbound.mock.calls.map((c) => c[0] as { validateOnly?: boolean; nodeId: string; portOverride: number }).filter((a) => !a.validateOnly);
    expect(real.find((a) => a.nodeId === "nod_1")!.portOverride).toBe(8443);
    expect(real.find((a) => a.nodeId === "nod_2")!.portOverride).toBe(0);
  });

  it("with no node in the panel says so, and the profile is made without putting it anywhere", async () => {
    nodeList = [];
    await toNodes();
    expect(text()).toContain("There are no nodes yet");
    await next();
    expect(button("Create the profile")).toBeTruthy();
    await click(button("Create the profile"));
    await settle();
    expect(createProfile).toHaveBeenCalled();
    expect(text()).toContain("The profile is made; there is nowhere to put it yet");
    expect(createInbound.mock.calls.filter((c) => !(c[0] as { validateOnly?: boolean }).validateOnly)).toHaveLength(0);
  });
});

describe("step 4 and 5, the people and the run", () => {
  beforeEach(() => {
    search = { protocol: "hysteria2" };
  });
  const toPeople = async () => {
    await mount();
    await next();
    await next();
  };

  it("ticks the groups that have no profile of this protocol yet, says what that adds up to, and how the profile reaches them", async () => {
    await toPeople();
    expect(group3("friends").querySelector("[role=checkbox]") ?? group3("friends")).toBeTruthy();
    expect(group3("friends").getAttribute("aria-checked")).toBe("true");
    expect(group3("family").getAttribute("aria-checked")).toBe("false");
    expect(group3("friends").textContent).toContain("14 people");
    expect(text()).toContain("14 people get it");
    expect(text()).toContain("“family” already gets Hysteria2");
    expect(text()).toContain("Link-based apps get the server on their own at the next subscription refresh (every 12 h)");
    expect(document.querySelector(".pw-bar span")?.textContent).toBe("14 people · 2 nodes");
    await click(group3("friends"));
    expect(text()).toContain("Nobody yet");
    expect(document.querySelector(".pw-bar span")?.textContent).toBe("2 nodes, no groups");
  });

  it("makes the profile, puts it into the groups and on the nodes, follows the nodes, and opens the profile's page", async () => {
    await toPeople();
    await click(button("Create and put on 2 nodes"));
    await settle();
    expect(createProfile).toHaveBeenCalledWith(expect.objectContaining({ protocol: "hysteria2", name: "Hysteria2 · 443" }));
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_f", profileIds: { values: ["prf_new"] } });
    const real = createInbound.mock.calls.map((c) => c[0] as { validateOnly?: boolean; nodeId: string; profileId: string }).filter((a) => !a.validateOnly);
    expect(real.map((a) => a.nodeId)).toEqual(["nod_1", "nod_2"]);
    expect(real.every((a) => a.profileId === "prf_new")).toBe(true);
    expect(text()).toContain("Runs on 2 nodes of 2");
    expect(document.querySelectorAll(".pw-apr.done")).toHaveLength(4);
    expect(document.querySelector(".pw-bar span")?.textContent).toContain("The profile already exists");
    expect(document.querySelectorAll(".pw-steps li.pw-done")).toHaveLength(5);
    expect((button("Open profile") as HTMLAnchorElement).getAttribute("href")).toBe("/profiles/$id");
    await click(button("Done"));
    expect(away).toHaveBeenCalledWith(expect.objectContaining({ to: "/profiles" }));
  });

  it("made from a node's page returns there", async () => {
    search = { protocol: "hysteria2", node: "nod_1" };
    await toPeople();
    await click(button("Create and put on 1 node"));
    await settle();
    expect(createInbound.mock.calls.filter((c) => !(c[0] as { validateOnly?: boolean }).validateOnly)).toHaveLength(1);
    await click(button("Done"));
    expect(away).toHaveBeenLastCalledWith(expect.objectContaining({ to: "/nodes/$id", params: { id: "nod_1" }, search: { tab: "profiles" } }));
  });

  it("makes the group 'Everyone' when the panel has none", async () => {
    groupList = [];
    await toPeople();
    expect(group3("Everyone").getAttribute("aria-checked")).toBe("true");
    await click(button("Create and put on 2 nodes"));
    await settle();
    expect(createGroup).toHaveBeenCalledWith({ name: "Everyone", profileIds: ["prf_new"] });
  });

  it("says which node did not start, with its port to try again", async () => {
    getProfile.mockResolvedValueOnce({ profile: { id: "prf_new" }, inbounds: [{ id: "inb_nod_1", nodeId: "nod_1", state: InboundState.ACTIVE, port: 443 }, { id: "inb_nod_2", nodeId: "nod_2", state: InboundState.FAILED, lastError: "listen udp :443: bind: address already in use", port: 443, protocol: "hysteria2" }] });
    getProfile.mockResolvedValue({ profile: { id: "prf_new" }, inbounds: [{ id: "inb_nod_1", nodeId: "nod_1", state: InboundState.ACTIVE, port: 443 }, { id: "inb_nod_2", nodeId: "nod_2", state: InboundState.ACTIVE, port: 8443 }] });
    await toPeople();
    await click(button("Create and put on 2 nodes"));
    await settle();
    expect(text()).toContain("Runs on 1 node of 2: de2 failed");
    expect(document.querySelector(".pw-apr.bad")?.textContent).toContain("Did not start");
    const port = document.querySelector<HTMLInputElement>(".pw-apf input")!;
    await type(port, "8443");
    await click(button("Repeat for de2"));
    await settle();
    expect(text()).toContain("Runs on 2 nodes of 2");
  });

  it("says that the profile itself was refused, and 'Fix' goes back to the settings", async () => {
    createProfile.mockRejectedValue(new ConnectError("name_taken", Code.AlreadyExists));
    await toPeople();
    await click(button("Create and put on 2 nodes"));
    await settle();
    expect(text()).toContain("Could not make “Hysteria2 · 443”");
    expect(createInbound.mock.calls.filter((c) => !(c[0] as { validateOnly?: boolean }).validateOnly)).toHaveLength(0);
    await click(button("Fix"));
    expect(document.querySelectorAll(".pw-steps li.pw-cur")[0]?.textContent).toContain("Settings");
  });
});

describe("leaving", () => {
  it("asks before leaving with something typed, and leaves at once with nothing", async () => {
    search = { protocol: "hysteria2" };
    await mount();
    await click(button("Profiles"));
    expect(text()).not.toContain("Leave without creating the profile?");
    await type(nameBox(), "x");
    await click(button("Profiles"));
    expect(text()).toContain("Leave without creating the profile?");
  });
});

describe("the new profile's URL", () => {
  it("keeps the node and a protocol id, and drops what is not one", () => {
    expect(validateNewProfileSearch({ node: "nod_1", protocol: "vless" })).toEqual({ node: "nod_1", protocol: "vless" });
    expect(validateNewProfileSearch({ protocol: "Robert'); DROP" })).toEqual({});
    expect(validateNewProfileSearch({ protocol: "vless", task: "cdn" })).toEqual({ protocol: "vless", task: "cdn" });
    expect(validateNewProfileSearch({ task: "cdn" })).toEqual({});
  });
});
