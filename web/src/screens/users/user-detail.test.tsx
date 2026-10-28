import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { Code, ConnectError } from "@connectrpc/connect";
import { LinkDeviceLimit, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { DeviceKind, QuotaReset, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { dateTimeOf } from "@/lib/format";
import { UserScreen } from "./user-detail";

const getUser = vi.fn();
const updateUser = vi.fn();
const setUsersEnabled = vi.fn();
const getUserDnsChoices = vi.fn();
const resetUserDnsChoices = vi.fn();
const me = vi.fn();
const listAlerts = vi.fn();
const revokeDevice = vi.fn();
const unblockDevice = vi.fn();
const renameDevice = vi.fn();
const getUserConnection = vi.fn();
const subsMode = { v: LinkDeviceLimit.ENFORCE as LinkDeviceLimit };
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  health: { listAlerts: (...a: unknown[]) => listAlerts(...a) },
  users: {
    getUser: (...a: unknown[]) => getUser(...a),
    updateUser: (...a: unknown[]) => updateUser(...a),
    setUsersEnabled: (...a: unknown[]) => setUsersEnabled(...a),
    revokeDevice: (...a: unknown[]) => revokeDevice(...a),
    unblockDevice: (...a: unknown[]) => unblockDevice(...a),
    getUserConnection: (...a: unknown[]) => getUserConnection(...a),
  },
  devices: { renameDevice: (...a: unknown[]) => renameDevice(...a) },
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
    getSubscriptionSettings: () =>
      Promise.resolve({ settings: { apps: [{ kind: App.HAPP, name: "Happ" }, { kind: App.HAPP, name: "FlClash" }, { kind: App.AMNEZIA, name: "AmneziaVPN" }], linkDeviceLimit: subsMode.v }, effectiveTitle: "" }),
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
  for (const m of [getUser, updateUser, setUsersEnabled, getUserDnsChoices, resetUserDnsChoices, me, listAlerts, revokeDevice, unblockDevice, renameDevice, getUserConnection]) m.mockReset();
  subsMode.v = LinkDeviceLimit.ENFORCE;
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
  deviceSpeedLimitBps: 0n,
  createdUnix: 0n,
  nodes: { all: true, nodeIds: [] },
  dnsPresetId: "",
  accessHapp: true,
  accessAmnezia: true,
};
// a device as GetUser sends it: every field there, the ones a test cares about set
const dev = (over: object) => ({
  id: "", platform: "", model: "", firstSeenUnix: 0n, lastSeenUnix: 0n, online: false, protocols: ["hysteria2"], awgProfileId: "", awgProfileName: "", awgVersion: "", stale: false, address: "", lastHandshakeUnix: 0n,
  kind: DeviceKind.LINK, osVersion: "", client: "", usedBytes: 0n, onlineNodeIds: [] as string[], fingerprint: "", ...over,
});
/** A profile's four formats in the order of the API, each [ok, note]. */
const fmts = (...rows: [boolean, string][]) =>
  rows.map(([ok, note], i) => ({ format: [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON][i]!, ok, note }));
const detail = {
  user,
  devices: [
    dev({ id: "dev_i", kind: DeviceKind.LINK_SHARED, online: true, client: "Hiddify/2.5" }),
    dev({ id: "dev_k", kind: DeviceKind.KEY, platform: "android", model: "Pixel", protocols: ["awg"], awgProfileId: "p_awg", awgProfileName: "AWG 3.1", awgVersion: "3.1", address: "10.66.4.2" }),
  ],
  blockedDevices: [] as object[],
  dailyTraffic: [],
  nodeTraffic: [],
  profiles: [
    { id: "p_hy", name: "hy2 · 443", protocol: "hysteria2", formats: fmts([true, ""], [true, ""], [true, ""], [true, ""]) },
    { id: "p_vl", name: "vless · xhttp", protocol: "vless", formats: fmts([true, ""], [true, ""], [true, ""], [false, "xhttp"]) },
    { id: "p_awg", name: "AWG 3.1", protocol: "awg", formats: fmts([false, "awg"], [true, ""], [false, "awg"], [false, "awg"]) },
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

async function mount(role = 1, choices: object[] | { choices: object[]; link?: object } = [], alerts: object[] = [], data: object = detail) {
  me.mockResolvedValue({ admin: { id: "adm_1", role } });
  listAlerts.mockResolvedValue({ nowUnix: NOW, active: alerts, history: [] });
  const picks = Array.isArray(choices) ? { choices } : choices;
  getUserDnsChoices.mockResolvedValue(picks);
  resetUserDnsChoices.mockResolvedValue({ removed: picks.choices.length + (picks.link ? 1 : 0) });
  getUser.mockResolvedValue(data);
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

  it("lists a torrent alert of this person in the Connection block", async () => {
    await mount(1, [], [alert({ id: "alt_t", kind: AlertKind.TORRENT, subject: "usr_1", titleKey: "health.alert.torrent.title", whyKey: "health.alert.torrent.why",
      params: { user_name: "Marina", user_id: "usr_1", nodes: "EE", count: "3", evidence: "dht_query", ports: "6881" } })]);
    expect(text()).toContain("Torrent attempts");
    expect(text()).toContain("Marina is trying to use torrents on EE: 3 attempts in a day, blocked. Evidence: DHT query, port 6881.");
  });

  it("shows no Connection block when this person has no such alert", async () => {
    await mount(1, [], [alert({ id: "alt_3", subject: "usr_2", params: { user_name: "Other", user_id: "usr_2" } })]);
    expect(text()).not.toContain("Connection");
    expect(text()).not.toContain("Access ended, but the person keeps trying");
  });

  it("adds what the nodes counted per device and profile to the Connection block, trouble in the warning colour", async () => {
    getUserConnection.mockResolvedValue({
      sinceUnix: NOW - 86_400, nowUnix: NOW, nodesWithoutCounters: [],
      rows: [
        { deviceId: "dev_i", nodeId: "nod_1", nodeName: "de1", inboundId: "inb_hy", profileName: "hy2 · 443", protocol: "hysteria2", connects: 3, rejects: 0, drops: 2, stalls: 0, onlineS: 4500n, lastHourUnix: NOW - 3600 },
        { deviceId: "dev_k", nodeId: "nod_1", nodeName: "de1", inboundId: "inb_awg", profileName: "AWG 3.1", protocol: "awg", connects: 0, rejects: 0, drops: 0, stalls: 7, onlineS: 0n, lastHourUnix: NOW - 7200 },
        { deviceId: "dev_gone", nodeId: "nod_1", nodeName: "de1", inboundId: "inb_hy", profileName: "hy2 · 443", protocol: "hysteria2", connects: 1, rejects: 1, drops: 0, stalls: 0, onlineS: 60n, lastHourUnix: NOW - 3600 },
      ],
    });
    await mount();
    expect(text()).toContain("Connection");
    const rows = [...document.querySelectorAll("[data-testid=conn-row]")].map((r) => r.textContent);
    expect(rows[0]).toContain("Apps without a device ID");
    expect(rows[0]).toContain("de1 · hy2 · 443");
    expect(rows[0]).toContain("3 connections · dropped at once: 2 · online 1 h");
    expect(rows[1]).toContain("Pixel");
    expect(rows[1]).toContain("0 connections · handshake did not complete: 7");
    expect(rows[2]).toContain("A removed device");
    expect(rows[2]).toContain("key refused: 1");
    const warned = [...document.querySelectorAll("[data-testid=conn-counts] .text-warn-text")].map((e) => e.textContent);
    expect(warned).toEqual(["dropped at once: 2", "handshake did not complete: 7", "key refused: 1"]);
  });

  it("says which nodes do not count yet, by name, and keeps the block hidden when nothing is counted at all", async () => {
    getUserConnection.mockResolvedValue({ sinceUnix: NOW - 86_400, nowUnix: NOW, rows: [], nodesWithoutCounters: ["nod_1"] });
    await mount();
    expect(text()).toContain("Not counted on de1: the node's agent does not send connection counters yet.");
    act(() => root?.unmount());
    host?.remove();
    getUserConnection.mockResolvedValue({ sinceUnix: NOW - 86_400, nowUnix: NOW, rows: [], nodesWithoutCounters: [] });
    await mount();
    expect(text()).not.toContain("Connection");
  });

  it("puts the Connection block at the foot of the left column, under the devices, and pages its counts six at a time", async () => {
    getUserConnection.mockResolvedValue({
      sinceUnix: NOW - 86_400, nowUnix: NOW, nodesWithoutCounters: [],
      rows: Array.from({ length: 8 }, (_, i) => ({
        deviceId: "dev_k", nodeId: "nod_1", nodeName: `n${i}`, inboundId: `inb_${i}`, profileName: "AWG 3.1", protocol: "awg", connects: 1, rejects: 0, drops: 0, stalls: 0, onlineS: 60n, lastHourUnix: NOW - 3600,
      })),
    });
    await mount();
    const counts = document.querySelector("[data-testid=conn-counts]")!;
    const column = [...document.querySelectorAll(".grid > div")].find((c) => c.contains(counts))!; // one of the page's two columns
    expect(column).toBe(column.parentElement!.firstElementChild); // the left one
    expect(column.lastElementChild!.contains(counts)).toBe(true); // at its foot
    expect(column.lastElementChild!.previousElementSibling!.textContent).toContain("Devices"); // right under the devices
    const shown = () => [...document.querySelectorAll("[data-testid=conn-row]")].map((r) => r.textContent?.match(/n\d/)?.[0]);
    expect(shown()).toEqual(["n0", "n1", "n2", "n3", "n4", "n5"]);
    expect(text()).toContain("Showing 1–6 of 8");
    await click(document.querySelector("[aria-label='Next page']"));
    expect(shown()).toEqual(["n6", "n7"]);
  });

  it("folds the traffic per node and protocol after five rows", async () => {
    const nodeTraffic = Array.from({ length: 8 }, (_, i) => ({ nodeId: `nod_${i}`, nodeName: `t${i}`, protocol: "hysteria2", bytes: BigInt(1000 - i) }));
    await mount(1, [], [], { ...detail, nodeTraffic });
    const rows = () => document.querySelectorAll("[data-testid=traffic-row]").length;
    expect(rows()).toBe(5);
    await click(button("Show all 8"));
    expect(rows()).toBe(8);
    await click(button("Show less"));
    expect(rows()).toBe(5);
    act(() => root?.unmount());
    host?.remove();
    await mount(1, [], [], { ...detail, nodeTraffic: nodeTraffic.slice(0, 6) }); // one more row is shown, not folded
    expect(rows()).toBe(6);
    expect(button("Show all 6")).toBeUndefined();
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
    expect(text()).toContain("Apps on the link");
    expect(text()).toContain("Apps without a device ID");
    expect(text()).toContain("Hiddify 2.5 · share one place");
    expect(text()).toContain("AmneziaVPN keys");
    expect(text()).toContain("Pixel");
    expect(text()).toContain("never connected yet");
    expect(text()).not.toContain("10.66.4.2"); // the address is in the key's window, not the row
    expect(button("Show key")).toBeDefined();
    expect(button("Disconnect")).toBeUndefined(); // in the row's menu
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

  it("marks a link profile that a subscription format leaves out, and only that one", async () => {
    await mount();
    const vl = [...document.querySelectorAll<HTMLAnchorElement>("a[href='/profiles/$id']")].find((a) => a.textContent === "vless · xhttp")!;
    expect(vl.parentElement!.textContent).toBe("vless · xhttpnot in sing-box: XHTTP");
    // one mark in the card: hy2 works everywhere, and AmneziaWG's own way already says it goes by keys
    expect(text().match(/not in /g)).toHaveLength(1);
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
    expect(dialog.textContent).toContain("Marina loses: “AWG 3.1” (1 AmneziaVPN key stops working)");
    expect(updateUser).toHaveBeenCalledTimes(1);
    await click(button("Change group"));
    await settle();
    expect(updateUser).toHaveBeenLastCalledWith({ userId: "usr_1", groupId: "grp_f" });
  });

  describe("DNS picked on the page", () => {
    const picks = [choice("de1", "Family"), choice("nl1", "Family", { offered: false, effectivePresetId: "dns_Plain", effectivePresetName: "Plain" })];
    // the card has another "Reset" (traffic): this one is inside the DNS panel
    const resetButtons = () => [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === "Reset" && b.closest("section, div")?.parentElement?.textContent?.includes("DNS picked on the page"));

    it("is not shown for a person who picked nothing", async () => {
      await mount(1, []);
      expect(text()).not.toContain("DNS picked on the page");
    });

    it("lists the picks per server and says what applies when the node stopped offering one", async () => {
      await mount(1, picks);
      expect(getUserDnsChoices).toHaveBeenCalledWith({ userId: "usr_1" }, expect.anything());
      expect(text()).toContain("DNS picked on the page");
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
      expect(text()).not.toContain("DNS picked on the page"); // refetched: nothing picked any more
    });

    it("shows a read-only admin the picks but not the button", async () => {
      await mount(3, picks);
      expect(text()).toContain("DNS picked on the page");
      expect(resetButtons()).toHaveLength(0);
    });

    // the panel: its hint's card (the card lists devices too, one of them "Apps on the link"-like)
    const panel = () => [...document.querySelectorAll("p")].find((p) => p.textContent === "What the person picked on their page: for the apps on the link and for each server.")?.parentElement ?? null;

    it("shows the pick for the apps on the link first", async () => {
      const link = { ...choice("", "AdGuard"), nodeId: "", nodeName: "" };
      await mount(1, { choices: picks, link });
      const rows = [...panel()!.querySelectorAll("li")].map((li) => li.textContent);
      expect(rows[0]).toBe("Apps on the linkAdGuard");
      expect(rows.slice(1).map((r) => r?.slice(0, 3))).toEqual(["de1", "nl1"]);
    });

    it("a pick for the apps on the link that is no longer offered says what applies", async () => {
      const link = { ...choice("", "AdGuard"), nodeId: "", nodeName: "", offered: false, effectivePresetId: "dns_Plain", effectivePresetName: "Plain" };
      await mount(1, { choices: [], link });
      expect(panel()?.textContent).toContain("Apps on the link");
      expect(panel()?.textContent).toContain("no longer offered, Plain applies");
    });

    it("a person who picked only for the apps on the link has the block and the reset, which clears it", async () => {
      await mount(1, { choices: [], link: { ...choice("", "AdGuard"), nodeId: "", nodeName: "" } });
      expect(text()).toContain("DNS picked on the page");
      await click(resetButtons()[0]);
      const dialog = document.querySelector("[role=dialog]")!;
      expect(dialog.textContent).toContain("for the apps on the link and for every server");
      getUserDnsChoices.mockResolvedValue({ choices: [] });
      await click([...dialog.querySelectorAll("button")].find((b) => b.textContent?.trim() === "Reset"));
      await settle();
      expect(resetUserDnsChoices).toHaveBeenCalledWith({ userId: "usr_1" });
      expect(panel()).toBeNull();
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

describe("the devices of the user card", () => {
  const T = Math.floor(Date.now() / 1000);
  const many = {
    ...detail,
    user: { ...user, devicesUsed: 4, deviceLimit: 3 },
    devices: [
      dev({ id: "dev_a", platform: "ios", model: "iPhone 15", osVersion: "iOS 18.1", client: "Happ/3.4.2", online: true, onlineNodeIds: ["nod_1", "nod_2"], fingerprint: "a1b2c3", usedBytes: 2_400_000_000n, firstSeenUnix: BigInt(T - 30 * 86_400), lastSeenUnix: BigInt(T - 20) }),
      dev({ id: "dev_b", platform: "android", model: "Pixel 8", osVersion: "Android 15", client: "Happ/3.4.0", online: true, onlineNodeIds: ["nod_1"], fingerprint: "d4e5f6" }),
      dev({ id: "dev_c", platform: "macos", model: "MacBook", osVersion: "macOS 15", client: "FlClash/0.8", lastSeenUnix: BigInt(T - 300), fingerprint: "0a0b0c" }),
      dev({ id: "dev_s", kind: DeviceKind.LINK_SHARED, client: "Hiddify/2.5", lastSeenUnix: BigInt(T - 7200) }),
      dev({ id: "dev_k", kind: DeviceKind.KEY, platform: "android", model: "Router", protocols: ["awg"], awgProfileId: "p_awg", awgProfileName: "AWG 3.1", awgVersion: "3.1" }),
    ],
    nodeAccess: [...detail.nodeAccess, { nodeId: "nod_2", nodeName: "nl1", countryCode: "NL", location: "", provider: "", protocols: ["hysteria2"], nodeProtocols: ["hysteria2"], selected: true }],
    blockedDevices: [{ id: "dev_x", platform: "windows", model: "Work PC", client: "v2rayN/7.1", blockedUnix: BigInt(T - 86_400), fingerprint: "778899" }],
  };
  /** Base UI opens a menu on the press, not on the click. */
  const press = async (el: Element | null | undefined) => {
    expect(el).toBeTruthy();
    await act(async () => {
      for (const type of ["pointerdown", "mousedown"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
      for (const type of ["pointerup", "mouseup", "click"]) el!.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    });
    await settle();
  };
  const row = (name: string) => [...document.querySelectorAll("li")].find((li) => li.textContent?.startsWith(name))!;
  const menu = (name: string) => press(document.querySelector(`button[aria-label='Actions for ${name}']`));
  const items = () => [...document.querySelectorAll('[role="menuitem"]')].map((i) => i.textContent);
  const item = (label: string) => click([...document.querySelectorAll('[role="menuitem"]')].find((i) => i.textContent === label));
  const dialog = () => [...document.querySelectorAll("[role=dialog]")].at(-1)!;
  const inDialog = (label: string) => click([...dialog().querySelectorAll("button")].find((b) => b.textContent?.trim() === label));

  it("shows a calm row per app on the link with its tile, two servers explained only where they are, the shared place and the keys", async () => {
    await mount(1, [], [], many);
    const iphone = row("iPhone 15");
    expect(iphone.textContent).toContain("de1, nl1");
    expect(iphone.textContent).toContain("iOS 18.1 · Happ 3.4.2");
    // the two servers say it themselves; the tooltip (and the status's name) explains what that means
    expect(iphone.querySelector("[aria-label*='The same keys are connected to 2 servers right now']")).not.toBeNull();
    expect(iphone.querySelector("[data-tone='sky']")).not.toBeNull(); // the tile of the way, as on the person's page
    // since when, all-time traffic and the fingerprint wait in the tooltip, named for a screen reader
    expect(iphone.textContent).not.toContain("a1b2c3");
    expect(iphone.textContent).not.toContain("2.4 GB");
    const details = iphone.querySelector("[aria-label^='iOS 18.1']")!.getAttribute("aria-label")!;
    expect(details).toContain("All-time traffic: 2.4 GB");
    expect(details).toContain("Fingerprint a1b2c3: tells two identical phones apart");
    expect(details).toContain("updated just now");
    expect(row("Pixel 8").textContent).toContain("online· de1");
    expect(row("Pixel 8").textContent).not.toContain("servers");
    expect(row("MacBook").textContent).toContain("5 min ago");
    expect(document.querySelectorAll("[aria-label*='The same keys are connected']")).toHaveLength(1);
    expect(row("Apps without a device ID").textContent).toContain("Hiddify 2.5 · share one place");
    expect(row("Router").querySelector("[data-tone='mint']")).not.toBeNull(); // a key: the same row, the mint tile
    // the header counts with the limit, red when over it
    const count = [...document.querySelectorAll("span")].find((s) => s.textContent === "4 of 3")!;
    expect(count.className).toContain("text-danger-text");
  });

  it("folds the blocked devices and unblocks one from its menu", async () => {
    unblockDevice.mockResolvedValue({ user });
    await mount(1, [], [], many);
    const fold = button("Blocked (1)")!;
    expect(fold.getAttribute("aria-expanded")).toBe("false");
    expect(text()).not.toContain("Work PC");
    await click(fold);
    expect(row("Work PC").textContent).toContain("v2rayN 7.1 · blocked");
    await menu("Work PC");
    expect(items()).toEqual(["Unblock"]);
    await item("Unblock");
    await settle();
    expect(unblockDevice).toHaveBeenCalledWith({ deviceId: "dev_x" });
    expect(text()).toContain("Unblocked. The app gets new keys at its next subscription refresh.");
  });

  it("renames, deletes and blocks from the row menu with the right calls; blocking asks first", async () => {
    renameDevice.mockResolvedValue({ device: {} });
    revokeDevice.mockResolvedValue({ user });
    await mount(1, [], [], many);
    await menu("Pixel 8");
    expect(items()).toEqual(["Rename…", "Delete…", "Block…"]);
    expect(text()).toContain("Fingerprint d4e5f6"); // the details, for a touch screen

    await item("Rename…");
    const field = dialog().querySelector("input")!;
    expect(field.value).toBe("Pixel 8");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field, "  work phone ");
      field.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await inDialog("Save");
    await settle();
    expect(renameDevice).toHaveBeenCalledWith({ deviceId: "dev_b", label: "work phone" });

    await menu("Pixel 8");
    await item("Delete…");
    expect(dialog().textContent).toContain("Delete “Pixel 8”?");
    await inDialog("Delete");
    await settle();
    expect(revokeDevice).toHaveBeenCalledWith({ deviceId: "dev_b" });

    await menu("Pixel 8");
    await item("Block…");
    expect(dialog().textContent).toContain("Block “Pixel 8”?");
    expect(dialog().textContent).toContain("cannot connect with this device ID again until you unblock it");
    expect(dialog().textContent).toContain("The person’s other devices keep working.");
    expect(revokeDevice).toHaveBeenCalledTimes(1); // nothing before the confirmation
    await inDialog("Block");
    await settle();
    expect(revokeDevice).toHaveBeenLastCalledWith({ deviceId: "dev_b", block: true });
    expect(text()).toContain("Device blocked");
  });

  it("disconnects the shared place from its menu", async () => {
    revokeDevice.mockResolvedValue({ user });
    await mount(1, [], [], many);
    await menu("Apps without a device ID");
    expect(items()).toEqual(["Disconnect…"]);
    await item("Disconnect…");
    expect(dialog().textContent).toContain("Disconnect the apps without a device ID?");
    await inDialog("Disconnect");
    await settle();
    expect(revokeDevice).toHaveBeenCalledWith({ deviceId: "dev_s" });
  });

  it("says in the panel's words why a device cannot be blocked", async () => {
    revokeDevice.mockRejectedValue(new ConnectError("cannot_block", Code.FailedPrecondition));
    await mount(1, [], [], many);
    await menu("iPhone 15");
    await item("Block…");
    await inDialog("Block");
    await settle();
    expect(text()).toContain("Only the device of an app that reports a device ID can be blocked.");
  });

  it("in the “only count” mode gives a person over the limit one quiet line to the setting instead of the alert", async () => {
    subsMode.v = LinkDeviceLimit.TRACK;
    await mount(1, [], [], many);
    expect(text()).toContain("The limit only counts for now: apps over it still connect.");
    expect(anchor("Limit mode")?.dataset.search).toBe(JSON.stringify({ tab: "rules" }));
    expect(button("+1 device")).toBeUndefined();
  });

  it("keeps the alert and drops the line when the limit refuses", async () => {
    await mount(1, [], [], many);
    expect(text()).not.toContain("The limit only counts for now");
    expect(button("+1 device")).toBeDefined();
  });

  it("sets the two speed limits on their own steppers, 0 = no limit", async () => {
    updateUser.mockResolvedValue({ user });
    await mount(1, [], [], { ...many, user: { ...many.user, speedLimitBps: 20_000_000n } });
    expect(text()).toContain("Speed per person");
    expect(text()).toContain("20 Mbit/s");
    expect(text()).toContain("Speed per device");
    await click(document.querySelector("[aria-label='Increase Speed per device']"));
    expect(text()).toContain("5 Mbit/s");
    await act(async () => void (await new Promise((r) => setTimeout(r, 600))));
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_1", deviceSpeedLimitBps: 5_000_000n });
  });
});

describe("the quota period on the user card", () => {
  const pick = (sel: HTMLSelectElement, value: QuotaReset) =>
    act(async () => {
      sel.value = String(value);
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
  const select = () => document.querySelector<HTMLSelectElement>("select[aria-label='Resets']")!;
  const labels = () => [...select().options].map((o) => o.textContent);

  it("says when the quota starts again and changes the period with one pick", async () => {
    updateUser.mockResolvedValue({ user });
    const next = Date.UTC(2027, 0, 4) / 1000; // a Monday, 00:00 UTC
    await mount(1, [], [], { ...detail, user: { ...user, quotaBytes: 100_000_000_000n, quotaReset: QuotaReset.WEEK, nextResetUnix: BigInt(next) } });
    expect(text()).toContain("resets every week");
    expect(text()).toContain(`Next reset ${dateTimeOf(next, "en")}`);
    expect(labels()).toEqual(["Daily", "Weekly", "Monthly", "Never"]);
    expect(select().value).toBe(String(QuotaReset.WEEK));
    await pick(select(), QuotaReset.DAY);
    await settle();
    expect(updateUser).toHaveBeenCalledWith({ userId: "usr_1", quotaReset: QuotaReset.DAY });
  });

  it("keeps a 30-day period it was given, and names no next reset when there is none", async () => {
    await mount(1, [], [], { ...detail, user: { ...user, quotaReset: QuotaReset.ROLLING_MONTH, nextResetUnix: 1_900_000_000n } });
    expect(labels()).toEqual(["Daily", "Weekly", "Monthly", "Every 30 days", "Never"]);
    expect(select().value).toBe(String(QuotaReset.ROLLING_MONTH));
    act(() => root?.unmount());
    host?.remove();
    await mount(1, [], [], { ...detail, user: { ...user, quotaReset: QuotaReset.NONE, nextResetUnix: 0n } });
    expect(text()).toContain("no reset");
    expect(text()).not.toContain("Next reset");
    expect(updateUser).not.toHaveBeenCalled();
  });
});
