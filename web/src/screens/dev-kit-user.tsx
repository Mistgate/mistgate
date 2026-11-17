import { QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { AddNodeProvider } from "@/components/add-node";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { LinkDeviceLimit, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { DeviceKind, QuotaReset, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { clientApiQuery } from "@/lib/clientapi";
import { clientLogsQuery } from "@/lib/client-debug";
import { setEffectsEnabled } from "@/lib/effects";
import { alertsQuery } from "@/lib/health";
import { userConnectionQuery } from "@/lib/connections";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { seeded } from "./dev-kit-nodes";
import { Shot } from "./dev-kit-redesign";
import { ShellLayout } from "./shell";
import type { DetailN } from "./users/model";
import { groupsQuery, protocolsQuery } from "./users/rpc";
import { UserDetail, userTabs, type UserTab } from "./users/user-detail";

// Development only. /dev-kit?only=pn-user: the person's page of the redesign in every state of the board, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the side-by-side:
//   /dev-kit?only=user-page&state=<state>[&tab=overview|devices|access|logs|settings][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1][&ui=<what is open>]
// state: ok (alice: active, 38 GB of 100) | over (the quota is used up) | exp (the term ended) | off (disabled) | new (never connected) | full (every place
//   is taken) | groups (ten groups) | tt-off | tt-none | tt-nol (a key and no link) | tt-paused (a key on hold) | track (over the limit, only counted) |
//   alert (an open alert about alice) | logs (devices of the Mistgate app and what came of their logs)
// ui: dirty (a limit changed: the save bar) | group | newgroup | dns | menu | qr (the TrustTunnel QR) | link-qr | pw (the page password shown) | rotate |
//   reset | delete | add (add a key) | hover (a day of the chart) | tgqr
// Made-up data of the board: alice, family and friends, de1, fi1, nl1 and msk1, documentation addresses.

const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86_400;
const id = (n: string) => `nod_${n}`;
const GB = 1e9;

const dev = (over: object) => ({
  id: "", platform: "", model: "", firstSeenUnix: 0, lastSeenUnix: 0, online: false, protocols: ["vless"], awgProfileId: "", awgProfileName: "", awgVersion: "", stale: false, address: "", lastHandshakeUnix: 0,
  kind: DeviceKind.LINK, osVersion: "", client: "", usedBytes: 0, onlineNodeIds: [] as string[], fingerprint: "", enrolled: false, enrolLinkStale: false, idle: false, idleSinceUnix: 0, ...over,
});
const fmts = (...rows: [boolean, string][]) => rows.map(([ok, note], i) => ({ format: [SubFormat.BASE64_URIS, SubFormat.MIHOMO_YAML, SubFormat.XRAY_JSON, SubFormat.SINGBOX_JSON][i]!, ok, note }));
const allOk = fmts([true, ""], [true, ""], [true, ""], [true, ""]);

const nodeAccess = [
  { nodeId: id("de1"), nodeName: "de1", countryCode: "DE", location: "Frankfurt", provider: "Example Cloud", protocols: ["vless", "hysteria2", "awg", "mtproto"], nodeProtocols: ["vless", "hysteria2", "awg", "mtproto"], selected: true },
  { nodeId: id("fi1"), nodeName: "fi1", countryCode: "FI", location: "Helsinki", provider: "Example Cloud", protocols: ["vless", "awg", "mtproto"], nodeProtocols: ["vless", "awg", "mtproto"], selected: true },
  { nodeId: id("nl1"), nodeName: "nl1", countryCode: "NL", location: "Amsterdam", provider: "Example Cloud", protocols: ["vless", "hysteria2"], nodeProtocols: ["vless", "hysteria2"], selected: true },
  { nodeId: id("msk1"), nodeName: "msk1", countryCode: "RU", location: "Moscow", provider: "Example Cloud", protocols: ["vless", "mtproto"], nodeProtocols: ["vless", "mtproto"], selected: true },
];
const profiles = [
  { id: "p_vl", name: "VLESS · Reality", protocol: "vless", formats: allOk },
  { id: "p_hy", name: "Hysteria2 · 443", protocol: "hysteria2", formats: allOk },
  { id: "p_awg", name: "AmneziaWG 3.1", protocol: "awg", formats: fmts([false, "awg"], [true, ""], [false, "awg"], [false, "awg"]) },
  { id: "p_tg", name: "MTProto · 443", protocol: "mtproto", formats: [] },
  { id: "p_tt", name: "TrustTunnel", protocol: "trusttunnel", formats: [] },
];
const protocols = [
  { id: "vless", displayName: "VLESS · Reality", apps: [App.HAPP] },
  { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP, App.AMNEZIA] },
  { id: "awg", displayName: "AmneziaWG 3.1", apps: [App.AMNEZIA] },
  { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM] },
  { id: "trusttunnel", displayName: "TrustTunnel", apps: [App.TRUST_TUNNEL] },
];
const groupSpecs: [string, string, string, number, number][] = [
  ["grp_family", "family", "sand", 4, 5],
  ["grp_friends", "friends", "sky", 6, 4],
  ["grp_colleagues", "colleagues", "lavender", 12, 3],
  ["grp_neighbours", "neighbours", "mint", 3, 3],
  ["grp_parents", "parents", "rose", 2, 4],
  ["grp_guests", "guests", "sage", 0, 2],
  ["grp_gaming", "gaming", "sand", 9, 3],
  ["grp_testers", "testers", "sky", 1, 5],
  ["grp_team", "team", "lavender", 14, 3],
  ["grp_old", "old-friends", "rose", 5, 3],
];
const group = (g: (typeof groupSpecs)[number]) => ({ id: g[0], name: g[1], color: g[2], userCount: g[3], profileIds: profiles.slice(0, g[4]).map((p) => p.id), dnsPresetId: "", happNodes: 3, amneziaNodes: 2, telegramNodes: 2, disabledNodeIds: [] as string[] });

const preset = (pid: string, name: string, over: object = {}) => ({ id: pid, name, description: "", builtin: true, servers: [], split: [], splitDirect: false, ipv4Only: false, users: 0, isDefault: false, ...over });
const dnsPresets = {
  presets: [
    preset("dns_builtin_standard", "Cloudflare + Google", { isDefault: true }),
    preset("dns_builtin_quad9", "Quad9: защита от вредоносных"),
    preset("dns_builtin_adblock", "AdGuard: без рекламы"),
    preset("dns_builtin_ru_split", "Россия: .ru напрямую"),
  ],
  providers: [],
  clientSupport: [],
};
const settings = (track: boolean) => ({
  settings: {
    title: "", announcement: "", supportUrl: "", updateIntervalHours: 12, serverNameTemplate: "{flag} {country} · {profile}", rules: [], defaultDnsPresetId: "",
    apps: [
      { platform: 0, kind: App.HAPP, name: "Happ" },
      { platform: 0, kind: App.HAPP, name: "Klick" },
      { platform: 0, kind: App.HAPP, name: "Mihomo Party" },
      { platform: 0, kind: App.AMNEZIA, name: "AmneziaVPN" },
    ],
    userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true, requirePagePassword: true, allowDnsChoice: true },
    linkDns: { presetIds: [], defaultPresetId: "" },
    linkDeviceLimit: track ? LinkDeviceLimit.TRACK : LinkDeviceLimit.ENFORCE,
  },
  reservedHeaderNames: [], effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 0,
});

const url = (kind: string) => `${kind}://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEGFsaWNlMjM0NTY3MjM0NTY3`;
const tgLink = (node: string, over: object = {}) => ({ nodeId: id(node), nodeName: node, profileId: "p_tg", profileName: "MTProto · 443", url: `tg://proxy?server=203.0.113.7&port=443&secret=ee00${node}`, ipv6: false, relay: false, via: "", ...over });
const ttLink = (node: string, over: object = {}) => ({ nodeId: id(node), nodeName: node, profileId: "p_tt", profileName: "TrustTunnel", url: url("tt") + node, hasIpv6: false, relay: false, via: "", note: "", ...over });

const DAY = [1.2, 2.8, 3.1, 0.9, 2.2, 4.6, 5.1, 1.8, 2.4, 3.3, 2.9, 1.1, 3.7, 2.9];
const row = (over: object) => ({
  deviceId: "dev_pc", nodeId: id("de1"), nodeName: "de1", inboundId: "inb_vl", profileName: "VLESS · Reality", protocol: "vless", connects: 14, rejects: 0, drops: 1, stalls: 0, onlineS: 7 * H, lastHourUnix: NOW - H,
  platform: "windows", client: "Klick/0.8.2", kind: "link", hours: [], clientCloses: 0, freezes: 0, quiet: 0, rttMs: 38, lossPermille: 4, rttSamples: 12, pktsSent: 5000, tcpStalls: 0, tcpStalls16k: 0, initiations: 0, responses: 0, responsesUnmatched: 0,
  unsupported: [], viaCountry: "", viaNodeId: "", viaNodeName: "", ...over,
});

export type Scene = { state: string; tab: UserTab; role: Role; ui: string };

/** The made-up detail of alice by the scene's state. */
export function detailOf(state: string): DetailN {
  const over = state === "over";
  const exp = state === "exp";
  const off = state === "off";
  const isNew = state === "new";
  const full = state === "full" || state === "track";
  const logs = state === "logs";
  const ttKey = !isNew && !["tt-off", "tt-none", "tt-nol"].includes(state);
  const days = exp ? -2 : 41;
  const used = isNew ? 0 : over ? 104 : 38;
  const status = off || state === "tt-paused" ? UserStatus.DISABLED : exp ? UserStatus.EXPIRED : over ? UserStatus.LIMITED : UserStatus.ACTIVE;
  const devices = isNew
    ? []
    : [
        dev({ id: "dev_pc", platform: "windows", model: "Desktop-Alice", osVersion: "Windows 11 24H2", client: "Klick/0.8.2", online: !off && !exp && !over, onlineNodeIds: !off && !exp && !over ? [id("de1")] : [], fingerprint: "3ef1d8", usedBytes: 21.4 * GB, firstSeenUnix: NOW - 28 * D, lastSeenUnix: NOW - 60, enrolled: logs }),
        dev({ id: "dev_ph", platform: "ios", model: "iPhone 15", osVersion: "iOS 19.1", client: "Happ/3.4.2", fingerprint: "a07c42", usedBytes: 11.9 * GB, firstSeenUnix: NOW - 27 * D, lastSeenUnix: NOW - 3 * H, enrolled: logs }),
        ...(full ? [] : [dev({ id: "dev_px", platform: "android", model: "Pixel 8", osVersion: "Android 16", client: "Happ/3.4.1", fingerprint: "5b9e10", usedBytes: 3.1 * GB, firstSeenUnix: NOW - 24 * D, lastSeenUnix: NOW - D, enrolled: logs })]),
        ...(full ? [dev({ id: "dev_sh", kind: DeviceKind.LINK_SHARED, platform: "", model: "", client: "Happ/3.3.0", lastSeenUnix: NOW - 5 * D })] : []),
        dev({ id: "dev_ipad", kind: DeviceKind.KEY, platform: "ios", model: "iPad Alice", osVersion: "iPadOS 19", protocols: ["awg"], awgProfileId: "p_awg", awgProfileName: "AmneziaWG 3.1", awgVersion: "3.1", address: "10.66.4.2", lastHandshakeUnix: NOW - 420, firstSeenUnix: NOW - 20 * D }),
        ...(ttKey ? [dev({ id: "dev_tt", kind: DeviceKind.KEY, protocols: ["trusttunnel"], model: "TrustTunnel key", firstSeenUnix: NOW - 5 * D })] : []),
      ];
  const used2 = devices.length;
  const user = {
    id: "usr_alice", name: "alice", subscriptionName: "Alice", groupId: "grp_family", groupName: "family", groupColor: "sand", status,
    apps: { happ: true, amnezia: true, telegram: true, mistgate: true, trustTunnel: state !== "tt-off" },
    via: [], devicesUsed: isNew ? 0 : full ? 5 : used2, deviceLimit: 5, devicesIdle: 0, usedBytes: used * GB, quotaBytes: 100 * GB, quotaReset: QuotaReset.MONTH, nextResetUnix: NOW + 17 * D,
    expiresUnix: NOW + days * D, lastSeenUnix: isNew ? 0 : NOW - (off || exp || over ? 2 * D : 60), online: !isNew && !off && !exp && !over, currentNodeId: id("de1"), currentNodeName: "de1", speedLimitBps: 0, deviceSpeedLimitBps: 0,
    nodes: { all: true, nodeIds: [] as string[] }, createdUnix: NOW - 28 * D, dnsPresetId: "", effectiveDnsPresetId: "dns_builtin_standard", effectiveDnsPresetName: "Cloudflare + Google", dnsSource: 3,
    accessHapp: true, accessAmnezia: true, accessTelegram: true, telegramIssued: true,
  };
  return {
    user: user as never,
    devices: devices as never,
    dailyTraffic: DAY.map((v, i) => ({ dayUnix: Math.floor((NOW - (13 - i) * D) / D) * D, bytes: isNew ? 0 : Math.round(((v * used) / 38) * GB) })),
    nodeTraffic: isNew
      ? []
      : [
          { nodeId: id("de1"), nodeName: "de1", protocol: "vless", bytes: Math.round(((18.3 * used) / 38) * GB) },
          { nodeId: id("de1"), nodeName: "de1", protocol: "hysteria2", bytes: Math.round(((9.8 * used) / 38) * GB) },
          { nodeId: id("nl1"), nodeName: "nl1", protocol: "hysteria2", bytes: Math.round(((6.4 * used) / 38) * GB) },
          { nodeId: id("fi1"), nodeName: "fi1", protocol: "vless", bytes: Math.round(((3.5 * used) / 38) * GB) },
        ],
    profiles: profiles as never,
    nodeAccess: nodeAccess as never,
    blocked: isNew ? [] : [{ id: "dev_blk", platform: "android", model: "Galaxy S21", osVersion: "Android 14", client: "Happ/3.2.0", blockedUnix: NOW - 2 * D, fingerprint: "778899" } as never],
  };
}

const logInfo = (over: Record<string, unknown>) => ({
  id: "dbr_1", userId: "usr_alice", userName: "alice", deviceId: "dev_pc", devicePlatform: "windows", deviceModel: "Desktop-Alice", state: "open", level: "info", note: "", unasked: false, actor: "owner",
  requestedUnix: NOW - 1800, expiresUnix: NOW + 70 * H, closedUnix: 0, records: 0, bytes: 0, droppedAttrs: 0, firstUnixMs: 0, lastUnixMs: 0, app: "", readUnix: 0, ...over,
});

/** The private query cache of a scene: the shell of the Nodes scene (the same fleet) and everything the person's page reads. */
export function cacheOf(s: Scene) {
  const edge = s.state === "edge";
  const c = seeded("calm", s.role, edge);
  const d = detailOf(s.state);
  const many = s.state === "groups" || s.ui === "group" || s.ui === "newgroup";
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: s.role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(edge ? { edition: "edge" } : {}) } as never);
  c.setQueryData(groupsQuery.queryKey, (many || s.state === "groups" ? groupSpecs : groupSpecs.slice(0, 2)).map(group) as never);
  c.setQueryData(protocolsQuery.queryKey, protocols as never);
  c.setQueryData(settingsQuery.queryKey, settings(s.state === "track") as never);
  c.setQueryData(dnsPresetsQuery.queryKey, dnsPresets as never);
  c.setQueryData(clientApiQuery.queryKey, { enabled: true } as never);
  c.setQueryData(["users", "link", "usr_alice"], { url: "https://sub.example.com/s/alice-7f3k9q", password: "lake-2718-moss" });
  c.setQueryData(["users", "dns-choices", "usr_alice"], {
    choices: [{ nodeId: id("de1"), nodeName: "de1", presetId: "dns_builtin_standard", presetName: "Cloudflare + Google", offered: true, effectivePresetId: "dns_builtin_standard", effectivePresetName: "Cloudflare + Google", updatedUnix: 0 }],
    link: { nodeId: "", nodeName: "", presetId: "dns_builtin_standard", presetName: "Cloudflare + Google", offered: true, effectivePresetId: "dns_builtin_standard", effectivePresetName: "Cloudflare + Google", updatedUnix: 0 },
  });
  c.setQueryData(alertsQuery.queryKey, {
    nowUnix: NOW,
    history: [],
    active:
      s.state === "alert"
        ? [{ id: "alt_1", severity: AlertSeverity.WARNING, kind: AlertKind.USER_CONNECTION, nodeId: "", nodeName: "", subject: "dev_ph", titleKey: "health.alert.user_connection.title", whyKey: "health.alert.user_connection.why.stale_key", params: { user_name: "alice", user_id: "usr_alice" }, firstSeenUnix: NOW - 7200, openedUnix: NOW - 3600, lastSeenUnix: NOW, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["open_user", "mute"] }]
        : [],
  } as never);
  c.setQueryData(userConnectionQuery("usr_alice").queryKey, {
    sinceUnix: NOW - D, nowUnix: NOW, nodesWithoutCounters: [],
    rows:
      s.state === "new"
        ? []
        : [
            row({}),
            row({ inboundId: "inb_hy", profileName: "Hysteria2", protocol: "hysteria2", connects: 6, drops: 1, rttMs: 31 }),
            row({ deviceId: "dev_ph", nodeId: id("nl1"), nodeName: "nl1", connects: 22, drops: 3, rttMs: 57, lossPermille: 9, onlineS: 7 * H, platform: "ios", client: "Happ/3.4.2" }),
            row({ deviceId: "dev_px", nodeId: id("fi1"), nodeName: "fi1", connects: 5, drops: 1, rttMs: 64, onlineS: 7 * H }),
            row({ deviceId: "dev_ipad", nodeId: id("de1"), nodeName: "de1", inboundId: "inb_awg", profileName: "AmneziaWG 3.1", protocol: "awg", connects: 0, drops: 0, rttSamples: 0, pktsSent: 0, onlineS: 2 * H, kind: "key" }),
          ],
  } as never);
  const key = !["tt-off", "tt-none", "tt-nol", "new"].includes(s.state);
  c.setQueryData(["users", "telegram", "usr_alice", true], { hasKey: true, keyDeviceId: "dev_tg", keyIssuedUnix: NOW - 28 * D, links: [tgLink("de1"), tgLink("de1", { ipv6: true, url: "tg://proxy?server=[2001:db8::7]&port=443&secret=ee00de1" }), tgLink("fi1"), tgLink("msk1")] });
  c.setQueryData(["users", "trusttunnel", "usr_alice", s.state !== "tt-off"], {
    hasKey: key, keyDeviceId: "dev_tt", keyIssuedUnix: NOW - 5 * D,
    links: !key || s.state === "tt-nol" ? [] : [ttLink("de1"), ttLink("de1", { hasIpv6: true, url: url("tt") + "6" }), ttLink("nl1", { relay: true, via: "RU", url: url("tt") + "r" }), ttLink("fi1", { url: "", note: "self_signed", profileId: "p_tt" })],
  });
  c.setQueryData(clientLogsQuery("usr_alice").queryKey, {
    items:
      s.state === "logs"
        ? [
            logInfo({}),
            logInfo({ id: "dbr_2", deviceId: "dev_ph", deviceModel: "iPhone 15", devicePlatform: "ios", state: "received", records: 412, closedUnix: NOW - 30 * H, app: "Happ/3.4.2" }),
            logInfo({ id: "dbr_3", deviceId: "dev_px", deviceModel: "Pixel 8", devicePlatform: "android", state: "received", unasked: true, actor: "", records: 128, closedUnix: NOW - 6 * D, app: "Happ/3.4.1" }),
          ]
        : [],
  } as never);
  void d;
  return c;
}

/** Presses what a `ui=` names, once the page has drawn: a menu, a window, a changed limit (for a screenshot of that state). */
function useUi(ui: string) {
  useEffect(() => {
    if (!ui) return;
    const q = (sel: string) => document.querySelector<HTMLElement>(sel);
    const press = (el: HTMLElement | null | undefined) => {
      if (!el) return;
      for (const type of ["pointerdown", "mousedown"]) el.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
      for (const type of ["pointerup", "mouseup", "click"]) el.dispatchEvent(new MouseEvent(type, { bubbles: true, button: 0 }));
    };
    const act: Record<string, () => void> = {
      dirty: () => press([...document.querySelectorAll<HTMLElement>(".up-lim [role=radio]")][2]),
      group: () => press(q(".up-gsel")),
      newgroup: () => {
        press(q(".up-gsel"));
        window.setTimeout(() => press(q(".up-gnew")), 200);
      },
      dns: () => press(q(".up-dnsc .up-sel")),
      menu: () => press(q(".up-dv .up-dotsb")),
      qr: () => press([...document.querySelectorAll<HTMLElement>(".tq-sv > div:first-child button")].at(-1)),
      tgqr: () => press([...document.querySelectorAll<HTMLElement>(".up-tg > div:first-child button")].at(-1)),
      "link-qr": () => press([...document.querySelectorAll<HTMLElement>(".up-lkc .up-code button")].at(-1)),
      pw: () => press(q(".up-pw .a button")),
      rotate: () => press([...document.querySelectorAll<HTMLElement>(".up-pw .a button")].at(-1)),
      reset: () => press(q(".up-sv-row button")),
      delete: () => press([...document.querySelectorAll<HTMLElement>(".up-sv-row button")].at(-1)),
      add: () => press(q("[data-testid=add-key]")),
      hover: () => q(".up-ch > span:nth-child(7)")?.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })),
    };
    const t = window.setTimeout(() => act[ui]?.(), 700);
    return () => window.clearTimeout(t);
  }, [ui]);
}

/** One whole page of the scene: the person's page in the real shell, at the window's own width. */
export function UserPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "ok";
  const tab = (userTabs.includes(params.get("tab") as UserTab) ? params.get("tab") : "overview") as UserTab;
  const ui = params.get("ui") ?? "";
  const role = params.get("role") === "helper" ? Role.HELPER : params.get("role") === "readonly" ? Role.READONLY : Role.OWNER;
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return cacheOf({ state, tab, role, ui });
  });
  useUi(ui);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="users" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <UserDetail data={detailOf(state)} scene={{ tab }} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "user-ov", state: "ok", title: "Overview: alice, 38 GB of 100, online on de1 (1440)", w: 1440, h: 900 },
  { id: "user-ov-hover", state: "ok", title: "Overview: a day of the chart under the pointer", w: 1440, h: 900, params: "&ui=hover" },
  { id: "user-ov-light", state: "ok", title: "Overview on the light theme", w: 1440, h: 900, params: "&theme=light" },
  { id: "user-ov-fx", state: "ok", title: "Overview with the effects on (bars rise, the figure shuffles)", w: 1440, h: 900, params: "&fx=1" },
  { id: "user-ov-rail", state: "ok", title: "Overview, 1000: one column", w: 1000, h: 1100 },
  { id: "user-ov-phone", state: "ok", title: "Overview on a phone, 400 wide", w: 400, h: 1400 },
  { id: "user-ov-fa", state: "ok", title: "Overview right to left (fa)", w: 1440, h: 900, params: "&lang=fa" },
  { id: "user-ov-over", state: "over", title: "The quota is used up: the banner, the red figure", w: 1440, h: 900 },
  { id: "user-ov-exp", state: "exp", title: "The term ended", w: 1440, h: 900 },
  { id: "user-ov-off", state: "off", title: "Disabled by the owner", w: 1440, h: 900 },
  { id: "user-ov-new", state: "new", title: "Never connected: no traffic, nobody online", w: 1440, h: 900 },
  { id: "user-ov-alert", state: "alert", title: "An open alert about alice above the traffic", w: 1440, h: 900 },
  { id: "user-ov-qr", state: "ok", title: "The link and its QR window", w: 1440, h: 900, params: "&ui=link-qr" },
  { id: "user-ov-pw", state: "ok", title: "The page password shown", w: 1440, h: 900, params: "&ui=pw" },
  { id: "user-ov-rotate", state: "ok", title: "A new link and password: the question", w: 1440, h: 900, params: "&ui=rotate" },
  { id: "user-dv", state: "ok", title: "Devices (1440)", w: 1440, h: 1500, params: "&tab=devices" },
  { id: "user-dv-light", state: "ok", title: "Devices on the light theme", w: 1440, h: 1500, params: "&tab=devices&theme=light" },
  { id: "user-dv-phone", state: "ok", title: "Devices on a phone", w: 400, h: 2400, params: "&tab=devices" },
  { id: "user-dv-fa", state: "ok", title: "Devices right to left", w: 1440, h: 1500, params: "&tab=devices&lang=fa" },
  { id: "user-dv-menu", state: "ok", title: "The menu of a device", w: 1440, h: 900, params: "&tab=devices&ui=menu" },
  { id: "user-dv-full", state: "full", title: "Every place taken: the amber line, the shared place, the banner", w: 1440, h: 1500, params: "&tab=devices" },
  { id: "user-dv-track", state: "track", title: "Over the limit in the only-count mode: the quiet line, no banner", w: 1440, h: 900, params: "&tab=devices" },
  { id: "user-dv-new", state: "new", title: "No device yet", w: 1440, h: 900, params: "&tab=devices" },
  { id: "user-dv-add", state: "ok", title: "Add a key (the window of the product)", w: 1440, h: 900, params: "&tab=devices&ui=add" },
  { id: "user-dv-tt-off", state: "tt-off", title: "TrustTunnel off", w: 1440, h: 1100, params: "&tab=devices" },
  { id: "user-dv-tt-none", state: "tt-none", title: "TrustTunnel on, no key", w: 1440, h: 1100, params: "&tab=devices" },
  { id: "user-dv-tt-nol", state: "tt-nol", title: "TrustTunnel: a key and no link", w: 1440, h: 1100, params: "&tab=devices" },
  { id: "user-dv-tt-paused", state: "tt-paused", title: "TrustTunnel: a key on hold (disabled person)", w: 1440, h: 1100, params: "&tab=devices" },
  { id: "user-dv-tt-qr", state: "ok", title: "TrustTunnel: the QR window", w: 1440, h: 900, params: "&tab=devices&ui=qr" },
  { id: "user-dv-tt-helper", state: "ok", title: "TrustTunnel and the page for a helper", w: 1440, h: 1500, params: "&tab=devices&role=helper" },
  { id: "user-dv-tt-readonly", state: "ok", title: "An observer: the TrustTunnel line, no buttons", w: 1440, h: 1500, params: "&tab=devices&role=readonly" },
  { id: "user-dv-tg-qr", state: "ok", title: "Telegram link: the QR window", w: 1440, h: 900, params: "&tab=devices&ui=tgqr" },
  { id: "user-ac", state: "ok", title: "Access and limits (1440)", w: 1440, h: 1600, params: "&tab=access" },
  { id: "user-ac-light", state: "ok", title: "Access on the light theme", w: 1440, h: 1600, params: "&tab=access&theme=light" },
  { id: "user-ac-phone", state: "ok", title: "Access on a phone", w: 400, h: 3000, params: "&tab=access" },
  { id: "user-ac-fa", state: "ok", title: "Access right to left", w: 1440, h: 1600, params: "&tab=access&lang=fa" },
  { id: "user-ac-dirty", state: "ok", title: "A limit changed: the bar of the unsaved", w: 1440, h: 900, params: "&tab=access&ui=dirty" },
  { id: "user-ac-dns", state: "ok", title: "The DNS list open", w: 1440, h: 900, params: "&tab=access&ui=dns" },
  { id: "user-ac-group", state: "ok", title: "The group list (two groups)", w: 1440, h: 900, params: "&tab=access&ui=group" },
  { id: "user-ac-groups", state: "groups", title: "Ten groups: the search in the list", w: 1440, h: 1100, params: "&tab=access&ui=group" },
  { id: "user-ac-newgroup", state: "ok", title: "A new group from the list", w: 1440, h: 1100, params: "&tab=access&ui=newgroup" },
  { id: "user-ac-readonly", state: "ok", title: "An observer: nothing changes", w: 1440, h: 1600, params: "&tab=access&role=readonly" },
  { id: "user-lg", state: "logs", title: "Logs: a request, two bundles (the owner)", w: 1440, h: 900, params: "&tab=logs" },
  { id: "user-lg-phone", state: "logs", title: "Logs on a phone", w: 400, h: 1200, params: "&tab=logs" },
  { id: "user-lg-none", state: "ok", title: "Logs: no device of the app yet", w: 1440, h: 700, params: "&tab=logs" },
  { id: "user-lg-helper", state: "logs", title: "Logs for a helper: the owner reads them", w: 1440, h: 700, params: "&tab=logs&role=helper" },
  { id: "user-st", state: "ok", title: "Settings (1440)", w: 1440, h: 1000, params: "&tab=settings" },
  { id: "user-st-light", state: "ok", title: "Settings on the light theme", w: 1440, h: 1000, params: "&tab=settings&theme=light" },
  { id: "user-st-phone", state: "ok", title: "Settings on a phone", w: 400, h: 1500, params: "&tab=settings" },
  { id: "user-st-fa", state: "ok", title: "Settings right to left", w: 1440, h: 1000, params: "&tab=settings&lang=fa" },
  { id: "user-st-reset", state: "ok", title: "Reset the traffic: the question", w: 1440, h: 900, params: "&tab=settings&ui=reset" },
  { id: "user-st-delete", state: "ok", title: "Delete: type the name", w: 1440, h: 900, params: "&tab=settings&ui=delete" },
  { id: "user-st-off", state: "off", title: "Settings of a disabled person: Enable", w: 1440, h: 1000, params: "&tab=settings" },
  { id: "user-st-readonly", state: "ok", title: "An observer: the zone is locked", w: 1440, h: 1000, params: "&tab=settings&role=readonly" },
];

export function UserKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Person</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=user-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
