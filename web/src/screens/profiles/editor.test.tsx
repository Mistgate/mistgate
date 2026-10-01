import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { hy2 } from "./schema.fixtures";
import { ProfileEditorScreen } from "./editor";

const createProfile = vi.fn();
const updateProfile = vi.fn();
const createInbound = vi.fn();
const updateGroup = vi.fn();
const navigate = vi.fn();
let params: Record<string, string> = {};
let search: Record<string, string> = {};

// the plugin's schema with the domain field (the trimmed fixture has none)
const schema = JSON.stringify({ ...JSON.parse(hy2), properties: { ...JSON.parse(hy2).properties, sni: { type: "string", title: "SNI", "x-group": "basics", "x-order": 30, "x-critical": true } } });
const defaults = JSON.stringify({ port: 443, tls_mode: "acme_domain", sni: "", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true });

vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  profiles: {
    listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: schema, defaultSettingsJson: defaults, apps: [App.HAPP] }] }),
    listProfiles: () => Promise.resolve({ profiles: [{ id: "p_old", name: "hy2 · 443", protocol: "hysteria2" }] }),
    previewProfile: () => Promise.resolve({ errors: [], warnings: [], clientPreview: "hysteria2://…", clientLabel: "Happ · URI list" }),
    getProfile: () =>
      Promise.resolve({
        profile: { id: "p_old", name: "hy2 · 443", protocol: "hysteria2", version: 1, nodeCount: 2, userCount: 12, summary: "", warnings: [] },
        settingsJson: defaults,
        inbounds: [],
      }),
    createProfile: (...a: unknown[]) => createProfile(...a),
    updateProfile: (...a: unknown[]) => updateProfile(...a),
    createInbound: (...a: unknown[]) => createInbound(...a),
  },
  groups: {
    listGroups: () =>
      Promise.resolve({
        groups: [
          { id: "grp_all", name: "Все", profileIds: ["p_old"], userCount: 9, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 },
          { id: "grp_t", name: "test", profileIds: [], userCount: 1, dnsPresetId: "", happNodes: 0, amneziaNodes: 0 },
        ],
      }),
    updateGroup: (...a: unknown[]) => updateGroup(...a),
  },
  nodes: {
    listNodes: () =>
      Promise.resolve({
        nodes: [
          { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2"], awgBackend: "auto" },
          { id: "nod_2", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", address: "203.0.113.10", protocols: [], awgBackend: "auto" },
        ],
      }),
  },
  subscriptions: { getSubscriptionSettings: () => Promise.resolve({ settings: { updateIntervalHours: 6, apps: [{ kind: App.HAPP, name: "Happ" }, { kind: App.AMNEZIA, name: "AmneziaVPN" }] }, effectiveTitle: "" }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useParams: () => params,
  useSearch: () => search,
  useNavigate: () => navigate,
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
beforeEach(() => {
  params = {};
  search = {};
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [createProfile, updateProfile, createInbound, updateGroup, navigate]) m.mockReset();
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ProfileEditorScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
const nameField = () => document.querySelector<HTMLInputElement>("input[placeholder='E.g. Main · 443']")!;
const portField = () => document.querySelector<HTMLInputElement>("input[aria-label='Port']")!;
const pressed = (label: string) => button(label)?.getAttribute("aria-pressed");

describe("a new profile", () => {
  it("is named after the protocol and its port until the name is typed, and asks for a name rather than greying the button", async () => {
    await mount();
    expect(nameField().value).toBe("Hysteria2 · 443");
    await type(portField(), "8443");
    expect(nameField().value).toBe("Hysteria2 · 8443");
    await type(nameField(), "");
    expect((button("Create profile") as HTMLButtonElement).disabled).toBe(false);
    await click(button("Create profile"));
    expect(text()).toContain("Give the profile a name");
    expect(createProfile).not.toHaveBeenCalled();
    // the domain field says what it is for, with an example
    expect(document.querySelector<HTMLInputElement>("input[aria-label='Domain (SNI)']")?.placeholder).toBe("vpn.example.com");
    expect(text()).toContain("Let's Encrypt needs a domain and open ports 80 and 443");
  });

  it("goes on every node and into the groups that have no Hysteria2 yet; the ones that have it are left alone, and both are said", async () => {
    await mount();
    expect(pressed("All nodes2")).toBe("true");
    expect(text()).toContain("fi1: the address is an IP, Let's Encrypt does not work there.");
    expect(pressed("test")).toBe("true");
    expect(pressed("Все")).toBe("false");
    expect(text()).toContain("«test»: no Hysteria2 profile yet. Without one its people get nothing in the subscription apps (Happ), so it is ticked.");
    expect(text()).toContain("«Все» already gets Hysteria2: a second profile is usually a test or a variant, so it is not ticked.");
  });

  it("is created, joins the groups, goes on the nodes in the same window, and opens its page when all went well", async () => {
    createProfile.mockResolvedValue({ profile: { id: "prf_new", name: "Hysteria2 · 443", protocol: "hysteria2" } });
    updateGroup.mockResolvedValue({});
    createInbound.mockResolvedValue({ inbound: {} });
    await mount();
    await click(button("Create profile"));
    await settle();
    expect(createProfile).toHaveBeenCalledWith(expect.objectContaining({ protocol: "hysteria2", name: "Hysteria2 · 443" }));
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup).toHaveBeenCalledWith({ groupId: "grp_t", profileIds: { values: ["prf_new"] } });
    expect(createInbound.mock.calls.map((c) => (c[0] as { nodeId: string }).nodeId)).toEqual(["nod_1", "nod_2"]);
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ to: "/profiles/$id", params: { id: "prf_new" } }));
  });

  it("made from a node's page goes on that node and returns there", async () => {
    search = { node: "nod_1" };
    createProfile.mockResolvedValue({ profile: { id: "prf_new", name: "Hysteria2 · 443", protocol: "hysteria2" } });
    createInbound.mockResolvedValue({ inbound: {} });
    await mount();
    expect(pressed("de1")).toBe("true");
    expect(pressed("fi1")).toBe("false");
    await click(button("Create profile"));
    await settle();
    expect(createInbound).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenLastCalledWith(expect.objectContaining({ to: "/nodes/$id", params: { id: "nod_1" }, search: { tab: "profiles" } }));
  });
});

describe("critical changes of a Hysteria2 profile", () => {
  it("say that the server stops working for everyone who has it until the app refreshes the subscription", async () => {
    params = { id: "p_old" };
    updateProfile.mockResolvedValue({ impact: { inboundsRestarted: 2, usersOnline: 1, devicesNeedReissue: 0, criticalFields: ["/port"], affectedUserNames: ["anna", "boris"] } });
    await mount();
    await type(portField(), "9443");
    await click(button("Save"));
    await settle();
    const dialog = [...document.querySelectorAll("[role=dialog]")].at(-1)!.textContent!;
    expect(dialog).toContain("This server stops working for 12 users until their app refreshes the subscription (up to 6 h).");
    expect(dialog).toContain("Ask them to refresh the subscription by hand, or make a copy of the profile on a new port");
    expect(dialog).toContain("The profile restarts on 2 nodes.");
  });
});
