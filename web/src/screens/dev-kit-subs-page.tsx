import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { HappPing, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { accessQuery } from "@/lib/assistant";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { Shot } from "@/screens/dev-kit-redesign";
import { groupsQuery, protocolsQuery } from "@/screens/users/rpc";
import { SubscriptionsScreen } from "./subscriptions";
import type { Settings } from "./subscriptions/model";
import { clientsQuery, pickerUsersQuery, settingsQuery } from "./subscriptions/queries";
import type { SubsTab } from "./subscriptions/tabs";
import { demoNodes } from "./dev-kit-decoys-data";
import { dnsScenes } from "./dev-kit-subs-dns";
import { hxScenes } from "./dev-kit-subs-hx";
import { baseScenes } from "./dev-kit-subs-scenes";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=subs-page&state=<state>[&tab=<tab>][&lang=en|ru|zh|fa|tr][&theme=light|dark][&fx=1]: the Subscriptions
// screen of the redesign in the real shell, on made-up data of the board's PnSubs (alice, bob, carol, dave; the groups Семья and
// Друзья; the nodes de1, nl1, fi1, msk1). Every state of the board's switch is a state here; a scene starts the screen on its tab and
// keeps it there (a click on the strip does not leave the page). `state` names a scene (see the three lists), `tab` overrides its tab.
// The scenes of the tabs are in dev-kit-subs-scenes.tsx (apps, rules, versions, texts, page), -hx.tsx (headers, Mihomo templates) and
// -dns.tsx (DNS presets). The panel's calls are answered here (the scene patches the clients of the services), nothing leaves the page.

/** What a scene is built on: its private query cache and the answers of the panel, which the scene's setup changes. */
export type World = {
  qc: QueryClient;
  role: Role;
  edge: boolean;
  settings: Settings;
  /** The rest of GetSubscriptionSettings besides the settings themselves. */
  extra: { reservedHeaderNames: string[]; samples: { node: string; countryCode: string; profile: string; loadPercent: number }[]; sampleGroup: string; effectiveTitle: string; namesLanguage: string; sharedLinkUsers: number; linkAwgKeys: number };
  /** Seeds one query of the screen. */
  seed: (key: readonly unknown[], data: unknown) => void;
  /** Answers calls of a service client (the generated one the screens use) with the given functions. */
  patch: (client: object, fns: Record<string, (req: never) => unknown>) => void;
};

export type SceneDef = {
  state: string;
  tab: SubsTab;
  title: string;
  w?: number;
  h?: number;
  /** Extra URL parameters of the frame (&theme=light, &lang=fa). */
  params?: string;
  /** What the scene changes in the world before the screen draws. */
  setup?: (w: World) => void;
  /** The `user` of the App versions tab (?user=). */
  user?: string;
};

export const allScenes: SceneDef[] = [...baseScenes, ...hxScenes, ...dnsScenes];

const clients = [
  { id: "happ", name: "Happ", protocols: ["vless", "hysteria2"], formats: [SubFormat.BASE64_URIS, SubFormat.XRAY_JSON] },
  { id: "klick", name: "kl!ck", protocols: ["vless", "hysteria2", "awg"], formats: [SubFormat.MIHOMO_YAML, SubFormat.BASE64_URIS] },
  { id: "mihomo", name: "Mihomo", protocols: ["vless", "hysteria2"], formats: [SubFormat.MIHOMO_YAML] },
  { id: "singbox", name: "sing-box", protocols: ["vless", "hysteria2"], formats: [SubFormat.SINGBOX_JSON] },
  { id: "v2rayng", name: "v2rayNG", protocols: ["vless"], formats: [SubFormat.XRAY_JSON, SubFormat.BASE64_URIS] },
  { id: "amnezia", name: "AmneziaVPN", protocols: ["awg"], formats: [] },
];
const protocols = [
  { id: "vless", displayName: "VLESS" },
  { id: "hysteria2", displayName: "Hysteria2" },
  { id: "awg", displayName: "AmneziaWG" },
];
export const demoGroups = [
  { id: "grp_fm", name: "Семья", color: "sand" },
  { id: "grp_fr", name: "Друзья", color: "sky" },
];
export const demoPeople = [
  { id: "usr_alice", name: "alice", groupId: "grp_fm" },
  { id: "usr_bob", name: "bob", groupId: "grp_fr" },
  { id: "usr_carol", name: "carol", groupId: "grp_fm" },
  { id: "usr_dave", name: "dave", groupId: "grp_fr" },
];

/** The settings of a fresh made-up panel: the board's rules, the three apps' texts, the page switches. */
const baseSettings = (): Settings =>
  ({
    title: "Mistgate",
    announcement: "В воскресенье с 03:00 до 03:30 UTC перезагрузим de1 — подключение вернётся само",
    supportUrl: "https://t.me/example",
    updateIntervalHours: 12,
    serverNameTemplate: "{flag} {country} · {profile}",
    rules: [
      { uaContains: "mihomo", format: SubFormat.MIHOMO_YAML },
      { uaContains: "clash", format: SubFormat.MIHOMO_YAML },
      { uaContains: "klick", format: SubFormat.MIHOMO_YAML },
      { uaContains: "sing-box", format: SubFormat.SINGBOX_JSON },
      { uaContains: "v2rayng", format: SubFormat.XRAY_JSON },
      { uaContains: "happ", format: SubFormat.BASE64_URIS },
      { uaContains: "curl", format: SubFormat.BASE64_URIS },
    ],
    apps: [],
    defaultDnsPresetId: "",
    userPage: { showAnnouncement: true, showSupport: true, showQr: true },
    happPing: HappPing.TCP,
    happPingOnOpen: false,
    responseHeaders: [],
    mihomoTemplates: [],
  }) as unknown as Settings;

function worldOf(def: SceneDef, role: Role, edge: boolean): World {
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  const w: World = {
    qc,
    role,
    edge,
    settings: baseSettings(),
    extra: { reservedHeaderNames: [], samples: [], sampleGroup: "", effectiveTitle: "Mistgate", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 0 },
    seed: (key, data) => qc.setQueryData(key, data as never),
    patch: (client, fns) => Object.assign(client, fns),
  };
  def.setup?.(w);
  return w;
}

const nodeRow = (n: (typeof demoNodes)[number]) => ({
  id: n.id, name: n.name, countryCode: n.cc, location: "", provider: "Example Cloud", notes: "", address: n.address, status: NodeStatus.ONLINE, protocols: ["hysteria2", "vless"], online: [],
  decoyMode: "builtin", decoySiteId: "", decoySupported: true, decoyReasonCode: "", decoyBundleSha256: "", decoyPanelAvailable: true, decoyOwnAvailable: true, agentVersion: "0.1.71", warp: { state: 0 },
});

/** What a scene starts the screen with (exported for its smoke test): the world drawn on, the tab, the person of the versions tab. */
export function sceneOf(state: string, tab?: string) {
  const def = allScenes.find((s) => s.state === state) ?? allScenes[0]!;
  const role = state.endsWith("-helper") ? Role.HELPER : Role.OWNER;
  const w = worldOf(def, role, state.endsWith("-edge"));
  const c = w.qc;
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: w.role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(w.edge ? { edition: "edge", unavailable: [] } : {}) } as never);
  c.setQueryData(accessQuery.queryKey, { available: true, unread: 0, readOnly: false, model: "", provider: undefined } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: demoNodes.map(nodeRow), expectedAgentVersion: "0.1.71" } as never);
  // what a scene did not seed itself is the board's base
  // (a query a scene left loading or failed, with prefetchQuery, is not seeded over)
  const has = (key: readonly unknown[]) => c.getQueryData(key) !== undefined || c.getQueryState(key) !== undefined;
  c.setQueryData(settingsQuery.queryKey, { settings: w.settings, ...w.extra });
  if (!has(clientsQuery.queryKey)) c.setQueryData(clientsQuery.queryKey, clients as never);
  if (!has(protocolsQuery.queryKey)) c.setQueryData(protocolsQuery.queryKey, protocols as never);
  if (!has(groupsQuery.queryKey)) c.setQueryData(groupsQuery.queryKey, demoGroups as never);
  if (!has(pickerUsersQuery.queryKey)) c.setQueryData(pickerUsersQuery.queryKey, demoPeople);
  const t = (tab && isTab(tab) ? tab : def.tab) as SubsTab;
  return { qc: c, def, tab: t, user: def.user };
}
const isTab = (s: string) => ["formats", "rules", "clients", "texts", "headers", "mihomo", "page", "dns"].includes(s);

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function SubsPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? allScenes[0]!.state;
  const [scene] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return sceneOf(state, params.get("tab") ?? undefined);
  });
  return (
    <QueryClientProvider client={scene.qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="subscriptions" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <SubscriptionsScreen demo={{ tab: scene.tab, user: scene.user }} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

/** The gallery: every scene in a frame of its own width (an iframe, so the screen's breakpoints answer to the frame). */
export function SubsScenesKit() {
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Subscriptions</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {allScenes.map((f) => (
        <Shot key={f.state + (f.params ?? "")} id={`subs-${f.state}${f.params ? f.params.replace(/\W+/g, "-") : ""}`} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=subs-page&state=${f.state}${f.params ?? ""}`} width={f.w ?? 1440} height={f.h ?? 1000} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
