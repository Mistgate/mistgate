import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { QuotaReset, UserFilter, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { groups as groupsApi, users as usersApi } from "@/lib/api";
import type { Job, Row } from "@/lib/batch";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { awaitingQuery } from "@/lib/integrations";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { clientApiQuery } from "@/lib/clientapi";
import { AddNodeProvider } from "@/components/add-node";
import { Shot } from "@/screens/dev-kit-redesign";
import { UsersBulkBar } from "@/screens/users/bulk";
import type { Avail } from "@/components/bulk";
import { panelFor, type ActionId } from "@/screens/users/bulk-actions";
import { UsersScreen, type UsersDemo } from "@/screens/users/list";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { GroupEditModal, type GroupEditDemo } from "@/screens/users/groups";
import { useTx } from "@/screens/users/t";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-users: the People screen of the redesign in every state, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for
// the side-by-side with the board:
//   /dev-kit?only=users-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// states: list (the board's three people) | many (its ten) | none (nobody yet) | search (a search that finds alice) | nomatch (one that
// finds nobody) | open (alice's row open) | open-erin (a person over the quota) | select (selecting, two ticked: the bulk bar) |
// select-ask (the bar asks "Sure?") | job-running | job-done | job-failed (the bar's batch states) | groups (the board's two groups) |
// ten (ten places each, bars) | sorted (&sort=traffic&desc=1) | filter-expiring | filter-over | open-heidi (no devices) | open-ten (ten devices behind Show all) | groups-many (ten) | groups-none (a search that finds no group) | groups-empty (no group at all) | group-new (the window) |
// group-edit (the edit window of a group; -lose: the panel says who loses a profile, -held: it refuses a node the owner holds back) |
// person-new | person-name (a name typed) | person-done | person-done-pw (with a page password).
// Made-up data of the board: alice, bob, carol, dave, erin, frank, grace, heidi, ivan, judy; the groups family and friends; the nodes de1,
// fi1, nl1, msk1; documentation addresses.

const now = Math.floor(Date.now() / 1000);
const DAY = 86400;
const GB = 1e9;

type Spec = { n: string; g: "family" | "friends"; du: number; dm: number; gb: number; q: number; days: number; node?: string; last: number; st?: "over" | "soon" | "exp" | "off" | "new"; via?: App[]; devs: [string, string, boolean?][] };

const phone = (n: string, app = "Happ"): [string, string] => [n, app];
const crowd: Spec[] = [
  { n: "alice", g: "family", du: 4, dm: 5, gb: 38, q: 100, days: 41, node: "de1", last: 0, via: [App.HAPP, App.AMNEZIA], devs: [["Desktop-Alice", "Klick", true], phone("iPhone 15"), phone("Pixel 8"), ["iPad (alice)", "AmneziaVPN"]] },
  { n: "bob", g: "friends", du: 2, dm: 3, gb: 12, q: 50, days: 18, node: "nl1", last: 0, via: [App.HAPP], devs: [["Laptop", "Mistgate for Windows", true], phone("Phone")] },
  { n: "carol", g: "friends", du: 1, dm: 3, gb: 3, q: 50, days: 74, last: 5 * 3600, via: [App.HAPP], devs: [phone("Phone")] },
  { n: "dave", g: "friends", du: 2, dm: 3, gb: 1.2, q: 50, days: 9, node: "de1", last: 0, via: [App.HAPP], devs: [["TV", "AmneziaVPN", true], phone("Tablet")] },
  { n: "erin", g: "friends", du: 3, dm: 3, gb: 52, q: 50, days: 22, last: DAY, st: "over", via: [App.HAPP], devs: [phone("Phone"), phone("Laptop"), phone("Router")] },
  { n: "frank", g: "family", du: 1, dm: 5, gb: 7, q: 100, days: 3, node: "fi1", last: 0, st: "soon", via: [App.HAPP], devs: [["Phone", "Klick", true]] },
  { n: "grace", g: "family", du: 2, dm: 5, gb: 64, q: 0, days: 0, last: 3 * DAY, via: [App.AMNEZIA], devs: [phone("Phone"), phone("Old laptop", "Mihomo Party")] },
  { n: "heidi", g: "friends", du: 0, dm: 3, gb: 0, q: 50, days: 30, last: -1, st: "new", devs: [] },
  { n: "ivan", g: "friends", du: 2, dm: 3, gb: 31, q: 50, days: -2, last: 2 * DAY, st: "exp", via: [App.HAPP], devs: [phone("Phone"), phone("Laptop")] },
  { n: "judy", g: "family", du: 3, dm: 5, gb: 12, q: 100, days: 60, last: 7 * DAY, st: "off", via: [App.HAPP], devs: [phone("Phone"), phone("Tablet"), phone("TV", "AmneziaVPN")] },
];
// the board's switch "Устройств": every person has ten places (the list draws a bar instead of dots), with the devices the board gives them
const TEN: Record<string, number> = { alice: 10, bob: 7, carol: 3, dave: 10, erin: 9, frank: 4, grace: 6, heidi: 0, ivan: 5, judy: 8 };
const MORE_DEVS: [string, string][] = [["Phone", "Happ"], ["Laptop", "Mistgate for Windows"], ["Tablet", "Happ"], ["TV", "AmneziaVPN"], ["Work PC", "Mistgate for Windows"], ["Router", "AmneziaVPN"], ["Second phone", "Klick"], ["Set-top box", "AmneziaVPN"], ["Old laptop", "Mihomo Party"], ["Spare phone", "Happ"]];
/** States that show the ten people of the board (more than the three of the list). */
const BIG = new Set(["many", "open-erin", "open-heidi", "ten", "open-ten", "sorted", "filter-expiring", "filter-over"]);
const tenOf = (state: string, s: Spec): Spec => {
  if (state !== "ten" && state !== "open-ten") return s;
  const du = TEN[s.n] ?? s.du;
  return { ...s, du, dm: 10, devs: du === 0 ? [] : s.n === "alice" ? [...s.devs, ...MORE_DEVS.slice(0, du - s.devs.length)] : MORE_DEVS.slice(0, du).map(([m, c], i) => [m, c, i === 0 && !!s.node] as [string, string, boolean]) };
};
const nodeIds = { de1: "nod_de1", fi1: "nod_fi1", nl1: "nod_nl1", msk1: "nod_msk1" } as const;
const nodes = [
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
  { id: "nod_fi1", name: "fi1", countryCode: "FI", status: NodeStatus.ONLINE },
  { id: "nod_nl1", name: "nl1", countryCode: "NL", status: NodeStatus.ONLINE },
  { id: "nod_msk1", name: "msk1", countryCode: "RU", status: NodeStatus.ONLINE },
];

const profiles = [
  { id: "p_vless", name: "VLESS Reality", protocol: "vless" },
  { id: "p_hy", name: "Hysteria2", protocol: "hysteria2" },
  { id: "p_awg", name: "AmneziaWG 3.1", protocol: "awg" },
  { id: "p_mt", name: "MTProto 443", protocol: "mtproto" },
];
const protocols = [
  { id: "vless", displayName: "VLESS", apps: [App.HAPP] },
  { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP] },
  { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA] },
  { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM] },
];

type G = { id: string; name: string; color: string; profileIds: string[]; userCount: number; happNodes: number; amneziaNodes: number; telegramNodes: number; disabledNodeIds: string[]; dnsPresetId: string };
const group = (id: string, name: string, color: string, userCount: number, profileIds = ["p_vless", "p_hy", "p_awg", "p_mt"]): G => ({
  id: `grp_${id}`, name, color, profileIds, userCount, happNodes: profileIds.some((p) => p === "p_vless" || p === "p_hy") ? 4 : 0, amneziaNodes: profileIds.includes("p_awg") ? 4 : 0, telegramNodes: profileIds.includes("p_mt") ? 2 : 0, disabledNodeIds: [], dnsPresetId: "",
});
const twoGroups = [group("friends", "friends", "sky", 6), group("family", "family", "sand", 4)];
const moreGroups = [
  ...twoGroups,
  group("colleagues", "colleagues", "lavender", 12, ["p_vless", "p_hy"]),
  group("neighbors", "neighbors", "sage", 3, ["p_vless"]),
  group("parents", "parents", "rose", 2, ["p_vless", "p_awg"]),
  group("guests", "guests", "mint", 0, ["p_hy"]),
  group("gaming", "gaming club", "sand", 9, ["p_vless", "p_hy", "p_awg"]),
  group("testers", "testers", "sky", 1, ["p_vless", "p_hy", "p_awg", "p_mt"]),
  group("team", "example.com team", "lavender", 14, ["p_vless", "p_hy"]),
  group("old", "old friends", "rose", 5, ["p_vless"]),
];

const userOf = (s: Spec, over: Record<string, unknown> = {}) => {
  const g = s.g === "family" ? twoGroups[1]! : twoGroups[0]!;
  const status = s.st === "off" ? UserStatus.DISABLED : s.st === "exp" ? UserStatus.EXPIRED : s.st === "over" ? UserStatus.LIMITED : UserStatus.ACTIVE;
  const online = !!s.node;
  return {
    id: `usr_${s.n}`, name: s.n, subscriptionName: "", groupId: g.id, groupName: g.name, groupColor: g.color, status, apps: { happ: true, amnezia: !!s.via?.includes(App.AMNEZIA), telegram: false, mistgate: true },
    via: s.via ?? [], devicesUsed: s.du, deviceLimit: s.dm, usedBytes: BigInt(Math.round(s.gb * GB)), quotaBytes: BigInt(s.q * GB), quotaReset: QuotaReset.MONTH, nextResetUnix: BigInt(now + 14 * DAY),
    expiresUnix: BigInt(s.days === 0 ? 0 : now + s.days * DAY), lastSeenUnix: BigInt(online ? now - 60 : s.last < 0 ? 0 : now - s.last), online, currentNodeId: s.node ? nodeIds[s.node as keyof typeof nodeIds] : "", currentNodeName: s.node ?? "",
    speedLimitBps: 0n, deviceSpeedLimitBps: 0n, createdUnix: BigInt(now - 60 * DAY), nodes: { all: true, nodeIds: [] }, dnsPresetId: "", accessHapp: true, accessAmnezia: !!s.via?.includes(App.AMNEZIA), accessTelegram: false, telegramIssued: false, devicesIdle: 0, ...over,
  };
};

const pw = "lake-2718-moss";

/** The panel's calls of this screen, answered here (the scene is its own page, so patching the shared client is safe). */
function fakeCalls(state: string, count: number) {
  const api = usersApi as unknown as Record<string, unknown>;
  const everyone = state === "none" ? [] : crowd.slice(0, count).map((s) => tenOf(state, s));
  api.listUsers = async (req: { filter?: UserFilter; query?: string; groupId?: string; pageSize: number; offset?: number; sort?: number; descending?: boolean }) => {
    const q = (req.query ?? "").toLowerCase();
    let list = everyone.filter((s) => !q || s.n.includes(q) || s.g.includes(q)).filter((s) => !req.groupId || `grp_${s.g}` === req.groupId);
    const f = req.filter;
    list = f === UserFilter.ONLINE ? list.filter((s) => s.node) : f === UserFilter.EXPIRING ? list.filter((s) => s.st === "soon") : f === UserFilter.OVER_QUOTA ? list.filter((s) => s.st === "over") : list;
    const dir = req.descending ? -1 : 1;
    const key: Record<number, (s: Spec) => number | string> = { 1: (s) => s.n, 2: (s) => s.gb, 3: (s) => s.days, 4: (s) => (s.node ? 1e12 : -s.last), 5: (s) => s.du * 100 + s.dm, 6: (s) => s.g };
    const by = key[req.sort ?? 1] ?? key[1]!;
    list = [...list].sort((a, b) => (by(a) > by(b) ? 1 : by(a) < by(b) ? -1 : 0) * dir);
    return {
      users: list.slice(req.offset ?? 0, (req.offset ?? 0) + req.pageSize).map((s) => userOf(s)),
      nextPageToken: "",
      counts: { all: list.length === everyone.length ? everyone.length : everyone.length, online: everyone.filter((s) => s.node).length, expiring: everyone.filter((s) => s.st === "soon").length, overQuota: everyone.filter((s) => s.st === "over").length, devicesUsed: everyone.reduce((a, s) => a + s.du, 0), devicesOnline: everyone.filter((s) => s.node).length, overDeviceLimit: 0, telegram: 0 },
    };
  };
  api.getUser = async ({ userId }: { userId: string }) => {
    const s = tenOf(state, crowd.find((x) => `usr_${x.n}` === userId) ?? crowd[0]!);
    return {
      user: userOf(s),
      devices: s.devs.map(([model, client, online], i) => ({ id: `dev_${s.n}_${i}`, platform: "other", model, firstSeenUnix: BigInt(now - 30 * DAY), lastSeenUnix: BigInt(online ? now - 30 : now - (i + 1) * 5 * 3600), lastOnlineUnix: BigInt(online ? now - 30 : now - (i + 1) * 5 * 3600), online: !!online, protocols: [], awgProfileId: "", awgProfileName: "", awgVersion: "", stale: false, address: "", lastHandshakeUnix: 0n, kind: 1, osVersion: "", client, usedBytes: 0n, onlineNodeIds: [], fingerprint: "", enrolled: false, enrolLinkStale: false, idle: false, idleSinceUnix: 0n })),
      dailyTraffic: [], nodeTraffic: [], profiles: [], nodeAccess: [], blockedDevices: [],
    };
  };
  api.getSubscriptionLink = async ({ userId }: { userId: string }) => ({ url: `https://sub.example.com/s/${userId.replace("usr_", "")}-7f3k9q`, pagePassword: userId === "usr_alice" ? pw : "" });
  const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
  for (const name of ["extendUsers", "resetUserTraffic", "setUsersEnabled", "deleteUsers"]) api[name] = async () => (await wait(300), { users: [] });
  api.createUser = async ({ name }: { name: string }) => ({ user: userOf({ n: name, g: "friends", du: 0, dm: 3, gb: 0, q: 50, days: 30, last: -1, st: "new", devs: [] }), subscriptionUrl: `https://sub.example.com/s/${name}-7f3k9q`, pagePassword: state === "person-done-pw" ? "tide-8132-pine" : "" });
  (groupsApi as unknown as Record<string, unknown>).listGroups = async () => ({ groups: [] });
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  const count = BIG.has(state) || state.startsWith("select") ? (state === "select" || state === "select-ask" ? 3 : 10) : 3;
  fakeCalls(state, BIG.has(state) ? 10 : count);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  const list = state === "none" ? [] : crowd.slice(0, BIG.has(state) ? 10 : 3).map((s) => tenOf(state, s));
  const gs = state === "groups-empty" ? [] : state === "groups-many" ? moreGroups : twoGroups;
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0 } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: nodes.map((n) => ({ ...n, location: "", provider: "", notes: "", address: "203.0.113.7", reason: undefined, protocols: [], online: [], trafficTodayBytes: 0, hasMetrics: false, cpuPct: 0, uptimeS: 0, lastSeenUnix: now })), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: 4, nodes: nodes.map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: "", status: n.status, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: list.length, online: list.filter((s) => s.node).length, devicesUsed: list.reduce((a, s) => a + s.du, 0), devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, { nowUnix: now, panel: { version: "v0.1.71", built: now, update: { version: "v0.1.71", url: "", checkedUnix: now, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: now }, nodes: [] } as never);
  c.setQueryData(groupsQuery.queryKey, gs as never);
  c.setQueryData(profileListQuery.queryKey, profiles as never);
  c.setQueryData(protocolsQuery.queryKey, protocols as never);
  c.setQueryData(dnsPresetsQuery.queryKey, { presets: [], providers: [], clientSupport: [] } as never);
  c.setQueryData(settingsQuery.queryKey, { settings: { apps: [{ kind: App.HAPP, name: "Happ" }, { kind: App.HAPP, name: "Klick" }] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0 } as never);
  c.setQueryData(clientApiQuery.queryKey, { enabled: true, stats: { people: 10, peopleOn: 4, devices: 20 } } as never);
  return c;
}

/** What a scene starts the screen with (exported for its smoke test). */
export const demoOf: Record<string, UsersDemo> = {
  search: { find: "ali" },
  nomatch: { find: "zzz" },
  open: { open: "usr_alice" },
  // the send window ("QR and send") over alice's open row: her page asks for a password; bob's does not
  send: { open: "usr_alice", send: "usr_alice" },
  "send-plain": { open: "usr_bob", send: "usr_bob" },
  "open-erin": { open: "usr_erin" },
  // a person who never connected: no devices to list ("None yet")
  "open-heidi": { open: "usr_heidi" },
  // ten places each, alice with her ten devices: four are listed, the rest sit behind "Show all"
  "open-ten": { open: "usr_alice" },
  "filter-expiring": { filter: "expiring" },
  "filter-over": { filter: "over" },
  select: { selecting: true, ticked: ["usr_alice", "usr_bob"] },
  "select-ask": { selecting: true, ticked: ["usr_alice", "usr_bob"], ask: "disable" },
  "groups-none": { groupFind: "zzz" },
  "group-new": { newGroup: true },
  "person-new": { creating: true },
  "person-name": { creating: true, name: "carol" },
  "person-done": { creating: true, done: true },
  "person-done-pw": { creating: true, done: true },
};

// the batch the floating bar shows when a state asks for one (the bar's own states: running, done, failed)
const bar = (state: string) => {
  const rowOf = (n: string, st: Row["state"], over: Partial<Row> = {}): Row => ({ id: `usr_${n}`, name: n, host: "", state: st, ...over });
  const run: Job = { spec: { action: "extend", limit: 1, error: () => "" }, running: true, stopping: false, rows: [rowOf("alice", "ok"), rowOf("bob", "running"), rowOf("carol", "waiting")] };
  const done: Job = { ...run, spec: { ...run.spec, many: async () => {} }, running: false, rows: [rowOf("alice", "ok"), rowOf("bob", "ok"), rowOf("carol", "ok")] };
  const failed: Job = { ...done, rows: [done.rows[0]!, done.rows[1]!, rowOf("carol", "bad", { note: "Network error" })] };
  return state === "job-running" ? run : state === "job-done" ? done : failed;
};

/** The edit window of a group that a scene opens over the Groups tab (the app opens it from the user card, the profile page and "New person"). */
export const editOf: Record<string, { group: G; demo?: GroupEditDemo }> = {
  "group-edit": { group: twoGroups[0]! },
  "group-edit-lose": { group: twoGroups[0]!, demo: { impact: { users: 6, lost: [{ id: "p_awg", name: "AmneziaWG 3.1", protocol: "awg", awgDevices: 3 }], gained: [] } as never } },
  // msk1 is held back and this group was not given it: it is off here, and a save that switches it on is refused
  "group-edit-held": { group: { ...twoGroups[0]!, disabledNodeIds: ["nod_msk1"] }, demo: { held: "nod_msk1" } },
};

export function SceneEdit({ state }: { state: string }) {
  const e = editOf[state];
  return e ? <GroupEditModal group={e.group as never} open onOpenChange={() => {}} demo={e.demo} /> : null;
}

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function UsersPage() {
  const tx = useTx();
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "list";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    const role = params.get("role");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  const job = state.startsWith("job-");
  const picked = ["alice", "bob", "carol"].map((n) => ({ id: `usr_${n}`, name: n }));
  const avail = Object.fromEntries((["extend", "disable", "enable", "reset", "delete"] as ActionId[]).map((a) => [a, { count: 3 }])) as Record<ActionId, Avail>;
  const noop = () => {};
  const grp = state.startsWith("groups") || state === "group-new" || state in editOf;
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="users" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <UsersScreen demo={job ? { selecting: true, ticked: ["usr_alice", "usr_bob", "usr_carol"] } : { ...demoOf[state], ...(grp && { tab: "groups" as const }) }} />
          </ShellLayout>
          <SceneEdit state={state} />
          {job && (
            <UsersBulkBar people={picked} offPage={0} allShown={false} total={3} avail={avail} toggle="disable" busy={state === "job-running"} job={bar(state)} panel={panelFor(tx, "extend")} onAction={noop} onClear={noop} onAll={noop} onStop={noop} onRetry={noop} onCloseJob={noop} />
          )}
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "users-list", state: "list", title: "The board's three people (1440)", w: 1440, h: 720 },
  { id: "users-list-light", state: "list", title: "The same on the light theme", w: 1440, h: 720, params: "&theme=light" },
  { id: "users-list-1000", state: "list", title: "A 1000 window: the rail, the card narrows (no last-online, no dots)", w: 1000, h: 720 },
  { id: "users-list-800", state: "list", title: "A 800 window: no group, no devices", w: 800, h: 720 },
  { id: "users-list-phone", state: "list", title: "A phone, 400 wide (the sort chips take the head's place)", w: 400, h: 780 },
  { id: "users-list-fa", state: "list", title: "Right to left (fa)", w: 1440, h: 720, params: "&lang=fa" },
  { id: "users-many", state: "many", title: "The board's ten people: expiring, over quota, expired, off, never seen", w: 1440, h: 1100 },
  { id: "users-ten", state: "ten", title: "The board's switch Devices, ten places each: a bar instead of the dots", w: 1440, h: 1100 },
  { id: "users-ten-phone", state: "ten", title: "Ten places each, a phone", w: 400, h: 1300 },
  { id: "users-sorted", state: "sorted", title: "Sorted by traffic, the most first: the head shows the arrow", w: 1440, h: 1100, params: "&sort=traffic&desc=1" },
  { id: "users-filter-expiring", state: "filter-expiring", title: "The Expiring chip pressed", w: 1440, h: 520 },
  { id: "users-filter-over", state: "filter-over", title: "The Over quota chip pressed", w: 1440, h: 520 },
  { id: "users-none", state: "none", title: "Nobody yet", w: 1440, h: 720 },
  { id: "users-none-phone", state: "none", title: "Nobody yet, a phone", w: 400, h: 900 },
  { id: "users-search", state: "search", title: "A search that finds alice", w: 1440, h: 520 },
  { id: "users-nomatch", state: "nomatch", title: "A search that finds nobody", w: 1440, h: 520 },
  { id: "users-open", state: "open", title: "alice's row open: the link, the password, four devices", w: 1440, h: 860 },
  { id: "users-send", state: "send", title: "The send window over alice's row: the QR, the link, the page password, Copy to send", w: 1440, h: 860 },
  { id: "users-send-light", state: "send", title: "The same on the light theme", w: 1440, h: 860, params: "&theme=light" },
  { id: "users-send-plain", state: "send-plain", title: "bob: no page password, the row says anyone with the link opens the page", w: 1440, h: 860 },
  { id: "users-send-phone", state: "send", title: "The send window on a phone (a sheet)", w: 400, h: 900 },
  { id: "users-send-fa", state: "send", title: "The send window, right to left (fa)", w: 1440, h: 860, params: "&lang=fa" },
  { id: "users-open-phone", state: "open", title: "alice's row open on a phone", w: 400, h: 1100 },
  { id: "users-open-heidi", state: "open-heidi", title: "heidi: never connected, no devices yet", w: 1440, h: 1100 },
  { id: "users-open-ten", state: "open-ten", title: "alice with ten devices: four listed, the rest behind Show all", w: 1440, h: 900 },
  { id: "users-open-erin", state: "open-erin", title: "erin: over the quota", w: 1440, h: 900 },
  { id: "users-select", state: "select", title: "Selecting: the check column and the bulk bar", w: 1440, h: 720 },
  { id: "users-select-ask", state: "select-ask", title: "The bar asks once before it switches people off", w: 1440, h: 720 },
  { id: "users-select-phone", state: "select", title: "Selecting on a phone", w: 400, h: 780 },
  { id: "users-job-running", state: "job-running", title: "The bar while a batch runs", w: 1440, h: 720 },
  { id: "users-job-done", state: "job-done", title: "The bar when a batch ended well", w: 1440, h: 720 },
  { id: "users-job-failed", state: "job-failed", title: "The bar when one person failed (the list opens by itself)", w: 1440, h: 780 },
  { id: "users-helper", state: "list", title: "A helper: Select and the bar, no owner-only things", w: 1440, h: 720, params: "&role=helper" },
  { id: "users-readonly", state: "list", title: "An observer: no Select, no buttons, no link", w: 1440, h: 720, params: "&role=readonly" },
  { id: "groups", state: "groups", title: "Groups: the board's two", w: 1440, h: 860 },
  { id: "groups-light", state: "groups", title: "Groups on the light theme", w: 1440, h: 860, params: "&theme=light" },
  { id: "groups-phone", state: "groups", title: "Groups on a phone", w: 400, h: 1200 },
  { id: "groups-many", state: "groups-many", title: "Ten groups", w: 1440, h: 1400 },
  { id: "groups-none", state: "groups-none", title: "A search that finds no group", w: 1440, h: 520 },
  { id: "groups-empty", state: "groups-empty", title: "No group at all", w: 1440, h: 520 },
  { id: "groups-readonly", state: "groups", title: "Groups for an observer: no tile, no Delete", w: 1440, h: 860, params: "&role=readonly" },
  { id: "group-new", state: "group-new", title: "The new-group window", w: 1440, h: 860 },
  { id: "group-new-phone", state: "group-new", title: "The new-group window on a phone", w: 400, h: 900 },
  { id: "group-edit", state: "group-edit", title: "The edit-group window: the group's own values in the new-group window", w: 1440, h: 900 },
  { id: "group-edit-light", state: "group-edit", title: "The edit-group window, light", w: 1440, h: 900, params: "&theme=light" },
  { id: "group-edit-phone", state: "group-edit", title: "The edit-group window on a phone (a sheet)", w: 400, h: 900 },
  { id: "group-edit-lose", state: "group-edit-lose", title: "The edit-group window: a profile taken away from six people, what they lose, the red button", w: 1440, h: 900 },
  { id: "group-edit-held", state: "group-edit-held", title: "The edit-group window: the panel refuses a node that is held back, and says where to give it", w: 1440, h: 900 },
  { id: "group-edit-held-phone", state: "group-edit-held", title: "The same on a phone", w: 400, h: 900 },
  { id: "person-new", state: "person-new", title: "The new-person window", w: 1440, h: 900 },
  { id: "person-name", state: "person-name", title: "The new-person window, a name typed", w: 1440, h: 900 },
  { id: "person-new-phone", state: "person-new", title: "The new-person window on a phone (a sheet)", w: 400, h: 900 },
  { id: "person-new-light", state: "person-new", title: "The new-person window, light", w: 1440, h: 900, params: "&theme=light" },
  { id: "person-done", state: "person-done", title: "The done screen", w: 1440, h: 780 },
  { id: "person-done-pw", state: "person-done-pw", title: "The done screen with a page password", w: 1440, h: 780 },
];

export function PnUsersKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Users</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=users-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
