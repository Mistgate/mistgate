import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { awaitingQuery } from "@/lib/integrations";
import { AddNodeProvider } from "@/components/add-node";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { Shot } from "@/screens/dev-kit-redesign";
import { ShellLayout } from "./shell";
import type { GroupDemo } from "./users/group-bits";
import { groupCountsQuery, groupPeopleKey } from "./users/group-people";
import { GroupScreen } from "./users/group-page";
import { groupsQuery, profileListQuery, protocolsQuery } from "./users/rpc";

// Development only. /dev-kit?only=pn-group: the group's page of the redesign in every state, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the
// side-by-side with the board (PnGroup):
//   /dev-kit?only=group-page&state=<state>[&tab=access|settings][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// states: people (friends: bob, carol, dave) | empty (nobody in the group) | pick (the panel "Who to move in", one ticked) | find (a
// search that finds nobody) | menu (the menu of carol's row) | access | access-dirty (a profile turned off: the save bar) |
// access-saving | access-saved | access-held (a node the owner holds back, locked) | nothing (a group with no profile: gives nothing) | settings | settings-dirty (a name typed) |
// settings-ask-mv | settings-ask-del (a button pressed once; the empty group) | deleted | missing.
// Made-up data of the board: the groups family and friends, alice to erin, the nodes de1, fi1, nl1 and msk1.

const now = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86400;

const person = (name: string, group: "friends" | "family", over: Record<string, unknown>) => ({
  id: `usr_${name}`,
  name,
  subscriptionName: "",
  groupId: `grp_${group}`,
  groupName: group,
  groupColor: group === "friends" ? "sand" : "lavender",
  status: UserStatus.ACTIVE,
  apps: { happ: true, amnezia: false, telegram: false },
  via: [],
  devicesUsed: 1,
  deviceLimit: 5,
  usedBytes: 0,
  quotaBytes: 100e9,
  expiresUnix: now + 30 * D,
  lastSeenUnix: now,
  online: false,
  currentNodeId: "",
  currentNodeName: "",
  nextResetUnix: 0,
  speedLimitBps: 0,
  deviceSpeedLimitBps: 0,
  createdUnix: now - 60 * D,
  nodes: { all: true, nodeIds: [] },
  dnsPresetId: "",
  accessHapp: true,
  accessAmnezia: false,
  accessTelegram: false,
  ...over,
});
const bob = person("bob", "friends", { devicesUsed: 2, usedBytes: 12e9, expiresUnix: now + 18 * D, online: true, currentNodeName: "nl1", lastSeenUnix: now - 30 });
const carol = person("carol", "friends", { devicesUsed: 1, usedBytes: 3e9, expiresUnix: now + 74 * D, lastSeenUnix: now - 5 * H });
const dave = person("dave", "friends", { devicesUsed: 2, usedBytes: 91e9, expiresUnix: now + 5 * D, online: true, currentNodeName: "de1", lastSeenUnix: now - 90 });
const alice = person("alice", "family", { devicesUsed: 4, usedBytes: 38e9, expiresUnix: now + 41 * D, online: true, currentNodeName: "de1", lastSeenUnix: now - 60, apps: { happ: true, amnezia: true, telegram: false } });
const erin = person("erin", "family", { devicesUsed: 3, usedBytes: 52e9, expiresUnix: now + 22 * D, lastSeenUnix: now - D });
const members = [bob, carol, dave];

const counts = (all: number, online: number) => ({ all, online, expiring: all > 0 ? 1 : 0, overQuota: 0, devicesUsed: all > 0 ? 5 : 0, devicesOnline: online > 0 ? 3 : 0, overDeviceLimit: 0, telegram: 0 });

const profile = (id: string, name: string, protocol: string) => ({ id, name, protocol, summary: "", nodeCount: 3, userCount: 5, warnings: [], version: 1, formats: [] });
const profileList = [profile("p_vless", "VLESS Reality", "vless"), profile("p_hy", "Hysteria2 main", "hysteria2"), profile("p_awg", "AWG 3.1", "awg"), profile("p_mt", "MTProto", "mtproto")];

const node = (name: string, cc: string, city: string, protocols: string[], sharing?: object) => ({ id: `nod_${name}`, name, countryCode: cc, location: city, provider: "Example Cloud", status: NodeStatus.ONLINE, protocols, online: [], notes: "", sharing });
const nodeList = (held: boolean) => [node("de1", "DE", "Frankfurt", ["vless", "hysteria2", "awg", "mtproto"]), node("fi1", "FI", "Helsinki", ["vless", "awg", "mtproto"]), node("nl1", "NL", "Amsterdam", ["vless", "hysteria2"]), node("msk1", "RU", "Moscow", ["vless", "mtproto"], held ? { mode: NodeShareMode.NONE, groupsReceiving: 0, groupsTotal: 2, changedUnix: now - 3 * D, idle: false } : undefined)];

const group = (id: "friends" | "family", over: Record<string, unknown> = {}) => ({
  id: `grp_${id}`,
  name: id,
  profileIds: id === "friends" ? ["p_vless", "p_hy"] : ["p_vless", "p_hy", "p_awg"],
  userCount: id === "friends" ? 3 : 2,
  dnsPresetId: "",
  happNodes: id === "friends" ? 4 : 3,
  amneziaNodes: id === "friends" ? 0 : 2,
  telegramNodes: 0,
  color: id === "friends" ? "sand" : "lavender",
  disabledNodeIds: [],
  ...over,
});

const preset = (id: string, name: string) => ({ id, name, description: "", builtin: true, servers: [], split: [], splitDirect: false, ipv4Only: false, users: 0, isDefault: id === "dns_builtin_standard" });

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  const empty = state === "empty" || state === "settings-ask-del";
  const nothing = state === "nothing";
  // msk1 is marked "nobody gets it": it is on the group's disabled list and the panel would refuse to switch it on
  const held = state === "access-held";
  const nodes = nodeList(held);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0 } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes, expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: 4, nodes: nodes.map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: n.status, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 5, online: 3 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(groupsQuery.queryKey, [
    group("friends", { ...(empty && { userCount: 0 }), ...(nothing && { profileIds: [], happNodes: 0, amneziaNodes: 0 }), ...(held && { disabledNodeIds: ["nod_msk1"], happNodes: 3 }) }),
    group("family"),
  ] as never);
  c.setQueryData(profileListQuery.queryKey, profileList as never);
  c.setQueryData(protocolsQuery.queryKey, [
    { id: "vless", apps: [App.HAPP] },
    { id: "hysteria2", apps: [App.HAPP] },
    { id: "awg", apps: [App.AMNEZIA] },
    { id: "mtproto", apps: [App.TELEGRAM] },
  ] as never);
  c.setQueryData(dnsPresetsQuery.queryKey, { presets: [preset("dns_builtin_standard", "Cloudflare + Google"), preset("dns_builtin_adblock", "AdGuard: no ads"), preset("dns_builtin_ru_split", "Russia: .ru direct")], providers: [], clientSupport: [] } as never);
  c.setQueryData(settingsQuery.queryKey, { settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, reservedHeaderNames: [], effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 0 } as never);
  const list = empty ? [] : members;
  c.setQueryData(groupCountsQuery("grp_friends").queryKey, (empty ? counts(0, 0) : counts(3, 2)) as never);
  c.setQueryData(groupPeopleKey("grp_friends", "", 1, 50), { users: list, nextPageToken: "", counts: empty ? counts(0, 0) : counts(3, 2) } as never);
  c.setQueryData(groupPeopleKey("grp_friends", "zz", 1, 50), { users: [], nextPageToken: "", counts: counts(0, 0) } as never);
  c.setQueryData(["users", "group-pick", ""], { users: [alice, erin, ...members], nextPageToken: "", counts: counts(5, 3) } as never);
  return c;
}

/** What a scene starts the page with (exported for its smoke test). */
export const demoOf: Record<string, GroupDemo> = {
  pick: { pick: true, tick: 1 },
  find: { find: "zz" },
  menu: { menu: true },
  "access-dirty": { dirty: true },
  "access-saving": { dirty: true, saving: true },
  "access-saved": { ok: true },
  "settings-dirty": { rename: "friends and family" },
  "settings-ask-mv": { ask: "mv" },
  "settings-ask-del": { ask: "del" },
  deleted: { deleted: true },
};

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function GroupScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "people";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    const role = params.get("role");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="users" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <GroupScreen id={state === "missing" ? "grp_gone" : "grp_friends"} demo={demoOf[state]} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "group-people", state: "people", title: "People: the figures, bob, carol and dave (1440)", w: 1440, h: 860 },
  { id: "group-people-light", state: "people", title: "The same on the light theme", w: 1440, h: 860, params: "&theme=light" },
  { id: "group-people-rail", state: "people", title: "A 900 window: the rail, the last visit goes", w: 900, h: 860 },
  { id: "group-people-phone", state: "people", title: "A phone, 400 wide", w: 400, h: 1000 },
  { id: "group-people-fa", state: "people", title: "Right to left (fa)", w: 1440, h: 860, params: "&lang=fa" },
  { id: "group-empty", state: "empty", title: "Nobody in the group", w: 1440, h: 780 },
  { id: "group-pick", state: "pick", title: "Who to move in (one ticked)", w: 1440, h: 1100 },
  { id: "group-pick-phone", state: "pick", title: "Who to move in on a phone", w: 400, h: 1200 },
  { id: "group-find", state: "find", title: "A search that finds nobody", w: 1440, h: 780 },
  { id: "group-menu", state: "menu", title: "The menu of a row", w: 1440, h: 860 },
  { id: "group-readonly", state: "people", title: "An observer: no buttons, no moving", w: 1440, h: 860, params: "&role=readonly" },
  { id: "group-access", state: "access", title: "Access: the profiles, the nodes, DNS", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-access-light", state: "access", title: "Access on the light theme", w: 1440, h: 1000, params: "&tab=access&theme=light" },
  { id: "group-access-narrow", state: "access", title: "Access, 900", w: 900, h: 1400, params: "&tab=access" },
  { id: "group-access-phone", state: "access", title: "Access on a phone", w: 400, h: 1700, params: "&tab=access" },
  { id: "group-access-dirty", state: "access-dirty", title: "A profile turned off: the bar slides out", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-access-saving", state: "access-saving", title: "The bar while the call runs", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-access-saved", state: "access-saved", title: "The bar after the save", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-access-held", state: "access-held", title: "A node the owner holds back (nobody gets it): off for the group and locked, with the reason in the row", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-access-held-light", state: "access-held", title: "The same on the light theme", w: 1440, h: 1000, params: "&tab=access&theme=light" },
  { id: "group-access-held-phone", state: "access-held", title: "The same on a phone", w: 400, h: 1700, params: "&tab=access" },
  { id: "group-nothing", state: "nothing", title: "A group that gives nothing", w: 1440, h: 1000, params: "&tab=access" },
  { id: "group-settings", state: "settings", title: "Settings: the name and the colour, the danger zone", w: 1440, h: 780, params: "&tab=settings" },
  { id: "group-settings-light", state: "settings", title: "Settings on the light theme", w: 1440, h: 780, params: "&tab=settings&theme=light" },
  { id: "group-settings-phone", state: "settings", title: "Settings on a phone", w: 400, h: 1100, params: "&tab=settings" },
  { id: "group-settings-dirty", state: "settings-dirty", title: "A name typed: the bar", w: 1440, h: 780, params: "&tab=settings" },
  { id: "group-settings-ask-mv", state: "settings-ask-mv", title: "Move everyone, pressed once", w: 1440, h: 780, params: "&tab=settings" },
  { id: "group-settings-ask-del", state: "settings-ask-del", title: "Delete the empty group, pressed once", w: 1440, h: 780, params: "&tab=settings" },
  { id: "group-deleted", state: "deleted", title: "The group is deleted", w: 1440, h: 600 },
  { id: "group-missing", state: "missing", title: "No such group", w: 1440, h: 600 },
];

export function GroupKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Group</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=group-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
