import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { UserScreen } from "./user-detail";

const getUser = vi.fn();
const updateUser = vi.fn();
const setUsersEnabled = vi.fn();
const getUserDnsChoices = vi.fn();
const resetUserDnsChoices = vi.fn();
const me = vi.fn();
const listAlerts = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  health: { listAlerts: (...a: unknown[]) => listAlerts(...a) },
  users: { getUser: (...a: unknown[]) => getUser(...a), updateUser: (...a: unknown[]) => updateUser(...a), setUsersEnabled: (...a: unknown[]) => setUsersEnabled(...a) },
  groups: {
    listGroups: () =>
      Promise.resolve({
        groups: [
          { id: "grp_all", name: "Everyone", profileIds: ["p_hy", "p_awg"], userCount: 4, dnsPresetId: "", happNodes: 1, amneziaNodes: 1 },
          { id: "grp_f", name: "friends", profileIds: ["p_hy"], userCount: 2, dnsPresetId: "", happNodes: 1, amneziaNodes: 0 },
        ],
      }),
  },
  profiles: { listProfiles: () => Promise.resolve({ profiles: [] }), listProtocols: () => Promise.resolve({ protocols: [{ id: "hysteria2", apps: [App.HAPP], displayName: "Hysteria2" }, { id: "awg", apps: [App.AMNEZIA], displayName: "AmneziaWG" }] }) },
  dns: {
    listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }),
    getUserDnsChoices: (...a: unknown[]) => getUserDnsChoices(...a),
    resetUserDnsChoices: (...a: unknown[]) => resetUserDnsChoices(...a),
  },
  auth: { me: () => me() },
  subscriptions: {
    getSubscriptionSettings: () => Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }, { kind: App.HAPP, name: "FlClash" }, { kind: App.AMNEZIA, name: "AmneziaVPN" }] }, effectiveTitle: "" }),
  },
}));
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
vi.mock("@tanstack/react-router", () => ({
  useParams: () => ({ id: "usr_1" }),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
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
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getUser, updateUser, setUsersEnabled, getUserDnsChoices, resetUserDnsChoices, me, listAlerts]) m.mockReset();
});

const user = {
  id: "usr_1",
  name: "Marina",
  subscriptionName: "",
  groupId: "grp_all",
  groupName: "Everyone",
  status: UserStatus.ACTIVE,
  apps: { happ: true, amnezia: true },
  via: [],
  devicesUsed: 2,
  deviceLimit: 5,
  usedBytes: 0n,
  quotaBytes: 0n,
  expiresUnix: 0n,
  lastSeenUnix: 0n,
  nextResetUnix: 0n,
  speedLimitBps: 0n,
  createdUnix: 0n,
  nodes: { all: true, nodeIds: [] },
  dnsPresetId: "",
  accessHapp: true,
  accessAmnezia: true,
};
const detail = {
  user,
  devices: [
    { id: "dev_i", platform: "", model: "", firstSeenUnix: 0n, lastSeenUnix: 0n, online: true, protocols: ["hysteria2"], awgProfileId: "", awgProfileName: "", awgVersion: "", stale: false, address: "", lastHandshakeUnix: 0n },
    { id: "dev_k", platform: "android", model: "Pixel", firstSeenUnix: 0n, lastSeenUnix: 0n, online: false, protocols: ["awg"], awgProfileId: "p_awg", awgProfileName: "AWG 3.1", awgVersion: "3.1", stale: false, address: "10.66.4.2", lastHandshakeUnix: 0n },
  ],
  dailyTraffic: [],
  nodeTraffic: [],
  profiles: [
    { id: "p_hy", name: "hy2 · 443", protocol: "hysteria2" },
    { id: "p_awg", name: "AWG 3.1", protocol: "awg" },
  ],
  nodeAccess: [{ nodeId: "nod_1", nodeName: "de1", countryCode: "DE", location: "", provider: "", protocols: ["hysteria2", "awg"], nodeProtocols: ["hysteria2", "awg"], selected: true }],
};

const NOW = 1_800_000_000;
const alert = (over: object) => ({
  id: "alt_1", severity: AlertSeverity.WARNING, kind: AlertKind.ACCESS_ENDED, nodeId: "", nodeName: "", subject: "usr_1", titleKey: "health.alert.access_ended.title",
  params: { user_name: "Marina", user_id: "usr_1", since: String(NOW - 86_400) }, whyKey: "health.alert.access_ended.why.expired",
  firstSeenUnix: NOW - 7200, openedUnix: NOW - 3600, lastSeenUnix: NOW, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["open_user", "mute"], ...over,
});
const choice = (nodeName: string, presetName: string, over: object = {}) => ({
  nodeId: `nod_${nodeName}`, nodeName, presetId: `dns_${presetName}`, presetName, offered: true, effectivePresetId: `dns_${presetName}`, effectivePresetName: presetName, updatedUnix: 0n, ...over,
});

async function mount(role = 1, choices: object[] = [], alerts: object[] = []) {
  me.mockResolvedValue({ admin: { id: "adm_1", role } });
  listAlerts.mockResolvedValue({ nowUnix: NOW, active: alerts, history: [] });
  getUserDnsChoices.mockResolvedValue({ choices });
  resetUserDnsChoices.mockResolvedValue({ removed: choices.length });
  getUser.mockResolvedValue(detail);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <UserScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const anchor = (label: string) => [...document.querySelectorAll<HTMLAnchorElement>("a")].find((a) => a.textContent === label);

describe("the user card", () => {
  it("shows the open alerts about this person in a Connection block: title, explanation, since when", async () => {
    await mount(
      1,
      [],
      [
        alert({}),
        alert({ id: "alt_2", severity: AlertSeverity.INFO, kind: AlertKind.USER_CONNECTION, subject: "dev_k", titleKey: "health.alert.user_connection.title", whyKey: "health.alert.user_connection.why.stale_key", params: { user_name: "Marina", user_id: "usr_1" } }),
        alert({ id: "alt_3", subject: "usr_2", params: { user_name: "Other", user_id: "usr_2" } }), // somebody else
        alert({ id: "alt_4", kind: AlertKind.USERS_IMPACTED, nodeId: "nod_1", nodeName: "de1", subject: "hysteria2", params: {}, titleKey: "health.alert.users_impacted.title", whyKey: "" }), // a node
      ],
    );
    const list = [...document.querySelectorAll("ul")].find((ul) => ul.textContent?.includes("Access ended"));
    expect(list).toBeTruthy();
    expect(list!.querySelectorAll("li")).toHaveLength(2);
    expect(list!.textContent).toContain("Access ended, but the person keeps trying");
    expect(list!.textContent).toContain("Marina’s subscription ended on");
    expect(list!.textContent).toContain("for 1 h"); // from the start of the episode
    expect(list!.textContent).toContain("Someone may not be able to connect");
    expect(list!.textContent).toContain("AmneziaWG key is older than the last profile change");
    expect(text()).toContain("Connection");
    expect(text()).not.toContain("Other");
    expect(text()).not.toContain("Connections dropped");
  });

  it("shows no Connection block when this person has no such alert", async () => {
    await mount(1, [], [alert({ id: "alt_3", subject: "usr_2", params: { user_name: "Other", user_id: "usr_2" } })]);
    expect(text()).not.toContain("Connection");
    expect(text()).not.toContain("Access ended, but the person keeps trying");
  });

  it("saves a separate public name while the account name stays unchanged", async () => {
    await mount();
    const field = document.querySelector<HTMLInputElement>('input[aria-label="Name shown on the subscription page"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "Nastya");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("Page greeting: Nastya");
    await click(button("Save name"));
    await settle();
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_1", subscriptionName: "Nastya" });
  });

  it("splits the devices into the subscription's apps and the AmneziaVPN keys, and lists both", async () => {
    await mount();
    expect(text()).toContain("Through the subscription");
    expect(text()).toContain("Apps on the link");
    expect(text()).toContain("Every app on the link (Happ, FlClash), on every device: one place in the limit · online");
    expect(text()).toContain("AmneziaVPN keys");
    expect(text()).toContain("Pixel");
    expect(text()).toContain("never connected yet");
    expect(text()).not.toContain("10.66.4.2"); // the address is in the key's window, not the row
    expect(button("Show key")).toBeDefined();
    expect(button("Disconnect")).toBeDefined();
  });

  it("shows the group's profiles in the two ways as links, and the group as a link in the hint", async () => {
    await mount();
    expect(text()).toContain("Subscription (Happ, FlClash)");
    const profiles = [...document.querySelectorAll<HTMLAnchorElement>("a[href='/profiles/$id']")].map((a) => a.dataset.params);
    expect(profiles).toContain(JSON.stringify({ id: "p_hy" }));
    expect(profiles).toContain(JSON.stringify({ id: "p_awg" }));
    const group = document.querySelector<HTMLAnchorElement>('a[aria-label="Open group “Everyone”"]')!;
    expect(group.dataset.search).toBe(JSON.stringify({ tab: "groups", group: "grp_all" }));
    expect(anchor("de1")?.dataset.search).toBe(JSON.stringify({ tab: "profiles" }));
  });

  it("asks before a new group, with what the person loses, and only the confirmation moves them", async () => {
    updateUser.mockImplementation(async (r: { dryRun?: boolean }) =>
      r.dryRun ? { user, impact: { users: 1, lost: [{ id: "p_awg", name: "AWG 3.1", protocol: "awg", awgDevices: 1 }], gained: [] } } : { user },
    );
    await mount();
    const select = document.querySelector<HTMLSelectElement>("select[aria-label='Group']")!;
    await act(async () => {
      select.value = "grp_f";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_1", groupId: "grp_f", dryRun: true });
    const dialog = [...document.querySelectorAll("[role=dialog]")].at(-1)!;
    expect(dialog.textContent).toContain("Marina: move to “friends”?");
    expect(dialog.textContent).toContain("Marina loses: «AWG 3.1» (1 AmneziaVPN key stops working)");
    expect(updateUser).toHaveBeenCalledTimes(1);
    await click(button("Change group"));
    await settle();
    expect(updateUser).toHaveBeenLastCalledWith({ userId: "usr_1", groupId: "grp_f" });
  });

  describe("DNS per server", () => {
    const picks = [choice("de1", "Family"), choice("nl1", "Family", { offered: false, effectivePresetId: "dns_Plain", effectivePresetName: "Plain" })];
    // the card has another "Reset" (traffic): this one is inside the DNS panel
    const resetButtons = () => [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "Reset" && b.closest("section, div")?.parentElement?.textContent?.includes("DNS per server"));

    it("is not shown for a person who picked nothing", async () => {
      await mount(1, []);
      expect(text()).not.toContain("DNS per server");
    });

    it("lists the picks per server and says what applies when the node stopped offering one", async () => {
      await mount(1, picks);
      expect(getUserDnsChoices).toHaveBeenCalledWith({ userId: "usr_1" }, expect.anything());
      expect(text()).toContain("DNS per server");
      expect(text()).toContain("de1");
      expect(text()).toContain("nl1");
      expect(text()).toContain("the node no longer offers it, Plain applies");
      expect(text().match(/no longer offers it/g)).toHaveLength(1); // only the one that is not offered
    });

    it("resets after a confirmation and refreshes", async () => {
      await mount(1, picks);
      await click(resetButtons()[0]);
      expect(resetUserDnsChoices).not.toHaveBeenCalled();
      const dialog = document.querySelector("[role=dialog]")!;
      expect(dialog.textContent).toContain("Reset the DNS choices");
      getUserDnsChoices.mockResolvedValue({ choices: [] });
      await click([...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Reset"));
      await settle();
      expect(resetUserDnsChoices).toHaveBeenCalledWith({ userId: "usr_1" });
      expect(text()).not.toContain("DNS per server"); // refetched: nothing picked any more
    });

    it("shows a read-only admin the picks but not the button", async () => {
      await mount(3, picks);
      expect(text()).toContain("DNS per server");
      expect(resetButtons()).toHaveLength(0);
    });
  });

  it("disables in red, and the toast can undo it", async () => {
    setUsersEnabled.mockResolvedValue({ users: [] });
    await mount();
    const off = button("Disable")!;
    expect(off.className).toContain("text-danger-text");
    await click(off);
    await settle();
    expect(setUsersEnabled).toHaveBeenCalledWith({ userIds: ["usr_1"], enabled: false });
    expect(text()).toContain("Marina disabled");
    await click(button("Undo"));
    await settle();
    expect(setUsersEnabled).toHaveBeenLastCalledWith({ userIds: ["usr_1"], enabled: true });
  });
});
