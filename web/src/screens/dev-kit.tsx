import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { groupsQuery } from "@/screens/users/rpc";
import { Avatar, Card, Chip, Kbd, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Modal } from "@/components/ui/modal";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { Stepper } from "@/components/ui/stepper";
import { Switch } from "@/components/ui/switch";
import { Tabs } from "@/components/ui/tabs";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { NavIcon, navPaths, type NavIconName } from "@/components/ui/icons";
import { AnimatedMark, Brand, DefaultMark } from "@/components/brand";
import { Turnstile, TurnstileFrame, type TurnstileHandle } from "@/components/turnstile";
import { setBrand } from "@/lib/brand";
import { ConfLine } from "@/screens/profiles/conf-preview";
import type { Settings as SubSettings } from "@/screens/subscriptions/model";
import { TextsTab } from "@/screens/subscriptions/texts";
import { LinkModal } from "@/screens/users/link-modal";
import { NodeDemos } from "@/screens/dev-kit-node";
import { PortsKit } from "@/screens/dev-kit-ports";
import { useT } from "@/i18n";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { GroupsTab } from "@/screens/users/groups";
import { profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { DevicesPanel } from "@/screens/users/user-devices";
import { dnsPresetsQuery } from "@/screens/subscriptions/queries";
import { HaloKit } from "./dev-kit-halo";
import { FleetKit } from "./dev-kit-fleet";
import { ProfileApprovalsKit, SettingsDemos } from "./dev-kit-settings";
import { PolishKit } from "./dev-kit-polish";
import { DnsAdsKit, DnsLinkKit } from "./dev-kit-dns";
import { EdgeKit } from "./dev-kit-edge";
import { UsersKit } from "./dev-kit-users";
import { DecoyKit } from "./dev-kit-decoy";
import { SSHKit } from "./dev-kit-ssh";
import { OsKit, OsPageKit } from "./dev-kit-os";
import { OsFleetKit } from "./dev-kit-os-fleet";
import { ClientLogsKit } from "./dev-kit-clientlogs";
import { ProbesKit } from "./dev-kit-probes";
import { BulkKit, BulkPage } from "./dev-kit-bulk";
import { UsersBulkKit, UsersBulkPage } from "./dev-kit-users-bulk";
import { HealthWarpKit } from "@/screens/dev-kit-health-warp";
import { Ipv6Kit } from "./dev-kit-ipv6";
import { PagingKit, UpdatesKit } from "./dev-kit-redesign";
import { ReleaseKit } from "./dev-kit-release";
import { RestoreKit } from "./dev-kit-restore";
import { AiKit } from "./dev-kit-ai";
import { FxKit } from "./dev-kit-fx";
import { GraphKit } from "./dev-kit-graph";
import { AwgRelayKit } from "./dev-kit-awg-relay";
import { BridgeKit } from "./dev-kit-bridge";
import { FoundationKit } from "./dev-kit-foundation";
import { NodePageFrame, NodePageKit } from "./dev-kit-node-page";
import { NodeShareKit, NodeSharePage } from "./dev-kit-node-share";
import { OverviewKit, OverviewPage } from "./dev-kit-overview";
import { ShellKit, ShellPage } from "./dev-kit-shell";
import { DecoysKit, DecoysPage } from "./dev-kit-decoys";
import { SubsPage, SubsScenesKit } from "./dev-kit-subs-page";
import { NodesKit, NodesPage } from "./dev-kit-nodes";
import { PnUsersKit, UsersPage } from "./dev-kit-users-page";
import { PnProfilesKit, ProfilesPage } from "./dev-kit-profiles";
import { PnProfileKit, ProfileScenePage } from "./dev-kit-profile";
import { PnProfileNewKit, ProfileNewPage } from "./dev-kit-profile-new";
import { HealthScenePage, PnHealthKit } from "./dev-kit-health";
import { PnReachKit, ReachScenePage } from "./dev-kit-reach";
import { SettingsKit, SettingsScenePage } from "./dev-kit-settings-page";
import { AiScenePage, PnAiKit } from "./dev-kit-ai-page";
import { IntegrationsScenePage, PnIntegrationsKit } from "./dev-kit-integrations-page";
import { AssistantScenePage, PnAssistantKit } from "./dev-kit-assistant";
import { PnUpdatesKit, UpdatesScenePage } from "./dev-kit-updates";
import { UserKit, UserPage } from "./dev-kit-user";
import { GroupKit, GroupScenePage } from "./dev-kit-group";
import { WindowsKit, WindowsPage } from "./dev-kit-windows";

// Development only (see router.tsx): every primitive in one place, to look at in both themes and at
// 390 and 1280 px. Plain English on purpose: this page never ships.
const kinds: StatusKind[] = ["ok", "warn", "bad", "off", "busy", "blip"];

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <Card className="flex flex-wrap items-center gap-3 p-4">{children}</Card>
    </section>
  );
}

const sampleConf = ["[Interface]", "Address = 10.66.0.2/32", "PrivateKey = ••••••••••••", "Jc = 6", "S1 = 52", "I1 = <b 0x00>", "", "[Peer]", "Endpoint = node.example.com:443"];

// an uploaded logo as the contract has it: white, "light" and "dark" fills, re-tinted from the accent
const sampleLogo =
  '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><circle cx="32" cy="32" r="30" fill="#693fc2"/><path d="M32 12 50 46H14z" fill="#cba5fa"/><circle cx="32" cy="38" r="6" fill="#ffffff"/></svg>';

function SyntaxSample() {
  return (
    <div className="w-full rounded-xl border border-line bg-canvas p-3 font-mono text-[11px] leading-[1.65]">
      {sampleConf.map((l, i) => (
        <ConfLine key={i} text={l} />
      ))}
    </div>
  );
}

// Groups, the two ways and "what a person gets", the "put on nodes" window, the new-profile block and the card's devices:
// mock data on a private cache that never asks the server (queries off by default).
const kitProfiles = [
  { id: "p_hy", name: "hy2 · 443 · Salamander", protocol: "hysteria2", nodeCount: 2, userCount: 9 },
  { id: "p_warp", name: "hy2 · WARP · 8443", protocol: "hysteria2", nodeCount: 1, userCount: 9 },
  { id: "p_awg", name: "AWG 3.1 · QUIC", protocol: "awg", nodeCount: 0, userCount: 2 },
];
const kitProtocols = [
  { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP] },
  { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA] },
];
const kitGroups = [
  { id: "grp_all", name: "Все", profileIds: ["p_hy", "p_warp", "p_awg"], userCount: 9, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 },
  { id: "grp_f", name: "Друзья", profileIds: ["p_hy"], userCount: 3, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 },
  { id: "grp_t", name: "тест 2", profileIds: [], userCount: 1, dnsPresetId: "", happNodes: 0, amneziaNodes: 0 },
] as unknown as Group[];
const kitNodes = [
  { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2"], awgBackend: "auto" },
  { id: "nod_2", name: "nl1", status: NodeStatus.ONLINE, countryCode: "NL", address: "nl1.example.com", protocols: [], awgBackend: "auto" },
  { id: "nod_3", name: "fi1", status: NodeStatus.PENDING, countryCode: "FI", address: "203.0.113.10", protocols: [], awgBackend: "auto" },
];

function KitCache({ children }: { children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, enabled: false } } });
    c.setQueryData(groupsQuery.queryKey, kitGroups as never);
    c.setQueryData(profileListQuery.queryKey, kitProfiles as never);
    c.setQueryData(protocolsQuery.queryKey, kitProtocols as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: kitNodes } as never);
    c.setQueryData(meQuery.queryKey, { admin: { role: Role.OWNER } } as never);
    c.setQueryData(dnsPresetsQuery.queryKey, { presets: [], providers: [], clientSupport: [] } as never);
    return c;
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const kitUser = { id: "usr_demo", name: "Марина", groupName: "Все", deviceLimit: 5, devicesUsed: 2, apps: { happ: true, amnezia: true } } as never;
const kitDevices = [
  // kind: 1 an app on the link with its device ID, 2 the place the apps without one share, 3 an AmneziaVPN key
  { id: "dev_l", kind: 1, platform: "ios", model: "iPhone 15", osVersion: "iOS 18.1", client: "Happ/3.4.2", fingerprint: "a1b2c3", usedBytes: 4.8e9, onlineNodeIds: ["de1"], awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"], online: true, lastSeenUnix: Math.floor(Date.now() / 1000) - 60, firstSeenUnix: Math.floor(Date.now() / 1000) - 9 * 86400, lastHandshakeUnix: 0, stale: false, address: "" },
  { id: "dev_i", kind: 2, platform: "", model: "", osVersion: "", client: "Hiddify/2.5.7", fingerprint: "", usedBytes: 0, onlineNodeIds: [], awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"], online: false, lastSeenUnix: Math.floor(Date.now() / 1000) - 7200, firstSeenUnix: 0, lastHandshakeUnix: 0, stale: false, address: "" },
  // an app that connected through the Mistgate API by its own key ("By key"), and one whose link changed since ("Link changed")
  { id: "dev_e", kind: 1, platform: "windows", model: "Office PC", osVersion: "Windows 11", client: "Mistgate/1.0", fingerprint: "c3d4e5", usedBytes: 1.2e9, onlineNodeIds: ["de1"], awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"], online: true, lastSeenUnix: Math.floor(Date.now() / 1000) - 30, firstSeenUnix: Math.floor(Date.now() / 1000) - 3 * 86400, lastHandshakeUnix: 0, stale: false, address: "", enrolled: true, enrolLinkStale: false },
  { id: "dev_r", kind: 1, platform: "macos", model: "MacBook Air", osVersion: "macOS 15", client: "Mistgate/1.0", fingerprint: "f6a7b8", usedBytes: 3e8, onlineNodeIds: [], awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"], online: false, lastSeenUnix: Math.floor(Date.now() / 1000) - 3600, firstSeenUnix: Math.floor(Date.now() / 1000) - 5 * 86400, lastHandshakeUnix: 0, stale: false, address: "", enrolled: true, enrolLinkStale: true },
  {
    id: "dev_k",
    kind: 3,
    osVersion: "",
    client: "",
    fingerprint: "",
    usedBytes: 0,
    onlineNodeIds: [],
    platform: "android",
    model: "Pixel 7",
    awgProfileId: "p_awg",
    awgProfileName: "AWG 3.1 · QUIC",
    awgVersion: "3.1",
    protocols: ["awg"],
    online: false,
    lastSeenUnix: 0,
    firstSeenUnix: 0,
    lastHandshakeUnix: Math.floor(Date.now() / 1000) - 3 * 86400,
    stale: true,
    address: "10.66.4.3",
  },
] as never;

// Subscriptions -> "Names & texts": the servers of the busiest group as GetSubscriptionSettings sends them (two in one
// country, to show the number), and an announcement longer than Happ shows (the counter and the cut)
const demoSubSettings = {
  title: "",
  announcement: "В субботу с 02:00 до 03:00 обновляю серверы в Германии и Финляндии — может моргнуть на пару минут. Если после этого не подключается, обновите подписку в Happ: потяните список вниз. Спасибо, что терпите, и хороших выходных!",
  supportUrl: "https://t.me/example_support",
  updateIntervalHours: 12,
  serverNameTemplate: "{flag} {country} · {profile}",
  rules: [],
  defaultDnsPresetId: "",
  apps: [],
  userPage: { showAnnouncement: true, showSupport: true, showQr: true },
} as unknown as SubSettings;
const demoSamples = [
  { node: "de1", countryCode: "DE", profile: "hy2 · 443" },
  { node: "de1", countryCode: "DE", profile: "hy2 · 443 · WARP" },
  { node: "fi1", countryCode: "FI", profile: "hy2 · 443" },
  { node: "nl1", countryCode: "NL", profile: "hy2 · 443" },
];

function TextsDemo() {
  const [qc] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } }));
  return (
    <QueryClientProvider client={qc}>
      <TextsTab data={{ settings: demoSubSettings, effectiveTitle: "Mistgate", samples: demoSamples, sampleGroup: "Все", namesLanguage: "ru" }} />
    </QueryClientProvider>
  );
}

export default function DevKit() {
  const toast = useToast();
  const t = useT();
  const [open, setOpen] = useState(false);
  const [refused, setRefused] = useState(false);
  const [on, setOn] = useState(true);
  const [n, setN] = useState(100);
  const [sel, setSel] = useState("a");
  const [seg, setSeg] = useState("x");
  const [tab, setTab] = useState("one");
  const [widget, setWidget] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const turnstile = useRef<TurnstileHandle>(null);
  const [replay, setReplay] = useState(0);
  const [linkDemo, setLinkDemo] = useState<"pw" | "plain" | "awg" | "nothing" | null>(null);
  // ?only=profile-delete|dns|ads|edge|node|health|users|subs|approvals|ipv6|ports|ssh|os|os-fleet|os-page|client-logs|bulk|bulk-users|graph|awg-relay|probes|trusttunnel|bridge|foundation|shell|overview|nodes|windows|nodepage|node-share|pn-group|decoys|pn-profiles|pn-health|pn-reach|pn-settings|pn-updates|pn-ai|pn-assistant|pn-integrations: one kit alone, for its screenshots;
  // ?only=bulk-page and bulk-users-page are whole pages (the Nodes screen, the People tab, in the shell's frame), drawn without the kit's column
  const which = new URLSearchParams(location.search).get("only") ?? "";
  if (which === "halo") return <HaloKit />;
  if (which === "shell-page") return <ShellPage />;
  if (which === "overview-page") return <OverviewPage />;
  if (which === "nodes-page") return <NodesPage />;
  if (which === "users-page") return <UsersPage />;
  if (which === "profiles-page") return <ProfilesPage />;
  if (which === "profile-page") return <ProfileScenePage />;
  if (which === "profile-new-page") return <ProfileNewPage />;
  if (which === "health-page") return <HealthScenePage />;
  if (which === "reach-page") return <ReachScenePage />;
  if (which === "settings-page") return <SettingsScenePage />;
  if (which === "pn-updates-page") return <UpdatesScenePage />;
  if (which === "pn-ai-page") return <AiScenePage />;
  if (which === "pn-integrations-page") return <IntegrationsScenePage />;
  if (which === "pn-assistant-page") return <AssistantScenePage />;
  if (which === "user-page") return <UserPage />;
  if (which === "group-page") return <GroupScenePage />;
  if (which === "decoys-page") return <DecoysPage />;
  if (which === "subs-page") return <SubsPage />;
  if (which === "windows-page") return <WindowsPage />;
  if (which === "nodepage-frame") return <NodePageFrame />;
  if (which === "node-share-page") return <NodeSharePage />;
  if (which === "bulk-page") return <BulkPage />;
  if (which === "bulk-users-page") return <UsersBulkPage />;
  const only = { dns: DnsLinkKit, ads: DnsAdsKit, edge: EdgeKit, node: NodeDemos, health: HealthWarpKit, users: UsersKit, approvals: ProfileApprovalsKit, fleet: FleetKit, ipv6: Ipv6Kit, ports: PortsKit, ssh: SSHKit, os: OsKit, "os-fleet": OsFleetKit, "os-page": OsPageKit, "client-logs": ClientLogsKit, bulk: BulkKit, "bulk-users": UsersBulkKit, release: ReleaseKit, restore: RestoreKit, ai: AiKit, fx: FxKit, graph: GraphKit, "awg-relay": AwgRelayKit, probes: ProbesKit, bridge: BridgeKit, foundation: FoundationKit, shell: ShellKit, overview: OverviewKit, nodes: NodesKit, windows: WindowsKit, nodepage: NodePageKit, "node-share": NodeShareKit, "pn-users": PnUsersKit, "pn-user": UserKit, "pn-group": GroupKit, decoys: DecoysKit, "pn-profiles": PnProfilesKit, "pn-profile": PnProfileKit, "pn-profile-new": PnProfileNewKit, "pn-health": PnHealthKit, "pn-reach": PnReachKit, subs: SubsScenesKit, "subs-scenes": SubsScenesKit, "pn-settings": SettingsKit, "pn-updates": PnUpdatesKit, "pn-ai": PnAiKit, "pn-assistant": PnAssistantKit, "pn-integrations": PnIntegrationsKit }[which];
  if (only) {
    const Kit = only;
    return (
      <div className="mx-auto flex max-w-5xl flex-col gap-6 px-4 py-8">
        <Kit />
      </div>
    );
  }
  return (
    <div className="mx-auto flex max-w-3xl flex-col gap-6 px-4 py-8">
      <div className="flex items-center gap-3">
        <Brand />
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      <PageTitle>UI kit</PageTitle>

      <Block title="Buttons">
        <Button variant="primary" size="lg">Primary 44</Button>
        <Button variant="secondary" size="lg">Secondary 44</Button>
        <Button variant="primary">Primary 36</Button>
        <Button>Secondary 36</Button>
        <Button variant="outline">Outline</Button>
        <Button variant="ghost">Ghost</Button>
        <Button variant="danger">Danger</Button>
        <Button variant="secondary" size="sm">Small 28</Button>
        <Button variant="secondary" size="xs">XS 22</Button>
        <Button variant="primary" disabled>Disabled</Button>
        <IconButton aria-label="Back">‹</IconButton>
        <IconButton variant="compact" aria-label="Close">×</IconButton>
        <Stepper decrementLabel="Less" incrementLabel="More" onDecrement={() => setN(n - 10)} onIncrement={() => setN(n + 10)}>
          {n} GB
        </Stepper>
      </Block>

      <Block title="Fields">
        <div className="grid w-full gap-3 md:grid-cols-2">
          <TextField label="Login" defaultValue="admin" />
          <TextField label="With error" defaultValue="oops" error="That value did not work." />
          <TextField search placeholder="Search nodes" aria-label="Search" />
          <TextField label="Mono" mono defaultValue="203.0.113.10" hint="A hint under the field." />
          <Select
            aria-label="Select"
            value={sel}
            onValueChange={setSel}
            options={[
              { value: "a", label: "Family" },
              { value: "b", label: "Friends" },
              { value: "c", label: "Work" },
            ]}
          />
        </div>
        <label className="flex items-center gap-3 text-[13px] font-semibold">
          <Switch checked={on} onCheckedChange={setOn} /> Switch
        </label>
        <Switch aria-label="Off switch" checked={false} />
      </Block>

      <Block title="Segmented, chips, kbd">
        <Segmented variant="thumb" aria-label="thumb" value={seg} onValueChange={setSeg} options={[{ value: "x", label: "24h" }, { value: "y", label: "7d" }]} />
        <Segmented variant="inset" aria-label="inset" value={seg} onValueChange={setSeg} options={[{ value: "x", label: "Dark" }, { value: "y", label: "Light" }]} />
        <Segmented variant="flat" aria-label="flat" value={seg} onValueChange={setSeg} options={[{ value: "x", label: <>All <span className="font-mono text-[11px] text-muted">5</span></> }, { value: "y", label: "Problems" }]} />
        <Chip>HY2</Chip>
        <Chip mono>AWG 3.1</Chip>
        <Kbd keys={["⌘", "K"]} />
      </Block>

      <Block title="Status">
        {kinds.map((k) => (
          <StatusPill key={k} kind={k} />
        ))}
        {kinds.map((k) => (
          <StatusPill key={k + "sm"} kind={k} sm />
        ))}
        {kinds.map((k) => (
          <StatusDot key={k + "dot"} kind={k} />
        ))}
      </Block>

      <Block title="Notices, avatars">
        <Notice title="Heads up.">Something needs a look.</Notice>
        <Notice tone="danger" title="Broken.">Something failed.</Notice>
        {["Zarina", "Artyom", "Lena", "Misha", "Katya", "Dima", "Olya", "Sasha", "Igor"].map((nm, i) => (
          <Avatar key={nm} name={nm} index={i} size={32} />
        ))}
      </Block>

      <Block title="Nav icons">
        {(Object.keys(navPaths) as NavIconName[]).map((k) => (
          <NavIcon key={k} name={k} size={20} />
        ))}
      </Block>

      <Block title="Section chips: one tone per meaning">
        {(["lavender", "sky", "sand", "sage", "mint", "rose"] as const).map((tone, i) => (
          <SectionLabel key={tone} icon={(["person", "layers", "sliders", "mask", "code", "warn"] as const)[i]} tone={tone}>
            {tone}
          </SectionLabel>
        ))}
        <SyntaxSample />
      </Block>

      <Block title="Toast, modal, brand">
        <Button onClick={() => toast("Saved")}>Toast</Button>
        <Button onClick={() => toast("User removed", { undo: () => toast("Restored") })}>Toast with undo</Button>
        <Button onClick={() => toast.error(t("err.group_not_empty", { users: 3 }))}>Toast: error</Button>
        <Button variant="primary" onClick={() => setOpen(true)}>Modal / sheet (Create is refused)</Button>
        <Button onClick={() => setBrand({ wordmark: ["north", "star"] })}>Brand: two-part wordmark</Button>
        <Button onClick={() => setBrand({ wordmark: ["mist", "gate"] })}>Brand: default</Button>
      </Block>

      <Block title="User link modal (mock link, no API calls until you press New link)">
        <Button onClick={() => setLinkDemo("pw")}>With page password</Button>
        <Button onClick={() => setLinkDemo("plain")}>Link only (password off)</Button>
        <Button onClick={() => setLinkDemo("awg")}>Password + Amnezia</Button>
        <Button onClick={() => setLinkDemo("nothing")} title="the group has no profile on a node">
          Gets nothing yet
        </Button>
      </Block>

      <section id="subs-texts" className="flex flex-col gap-3">
        <SectionLabel>Subscriptions, names and texts: real servers of the group “Все”, the announcement past what Happ shows</SectionLabel>
        <TextsDemo />
      </section>

      <Block title="Brand mark (16, 24, 30, 41, 64, 128; follows the accent)">
        {[16, 24, 30, 41, 64, 128].map((px) => (
          <DefaultMark key={px} size={px} />
        ))}
      </Block>

      <Block title="Animated mark (intro once, loading loop; an uploaded logo only fades and breathes)">
        <Button onClick={() => setReplay((r) => r + 1)}>Replay the intro</Button>
        <Button onClick={() => setBrand({ logoSvg: sampleLogo })}>Logo: uploaded</Button>
        <Button onClick={() => setBrand({ logoSvg: null })}>Logo: built-in</Button>
        <div className="flex w-full flex-wrap items-center gap-6">
          <AnimatedMark key={`i${replay}`} size={128} mode="intro" />
          <AnimatedMark key={`j${replay}`} size={41} mode="intro" />
          <AnimatedMark size={128} mode="loading" />
          <AnimatedMark size={56} mode="loading" />
          <AnimatedMark size={30} mode="loading" />
        </div>
      </Block>
      <Block title="Turnstile frame (idle, busy, ok, error)">
        <div className="grid w-full gap-3 md:grid-cols-2">
          {(["idle", "busy", "ok", "error"] as const).map((st) => (
            <TurnstileFrame key={st} status={st} label={st} onClick={st === "idle" || st === "error" ? () => {} : undefined} />
          ))}
        </div>
      </Block>

      <Block title="Turnstile widget (the public always-pass test key from Cloudflare; their script loads only when started)">
        <div className="flex w-full flex-col gap-3">
          {widget ? <Turnstile siteKey="1x00000000000000000000AA" onToken={setToken} ref={turnstile} /> : <Button onClick={() => setWidget(true)}>Start the widget</Button>}
          <div className="flex items-center gap-3 text-xs text-muted">
            <span className="min-w-0 flex-1 truncate font-mono">token: {token ? `${token.slice(0, 18)}…` : "none"}</span>
            <Button size="sm" onClick={() => turnstile.current?.reset()}>Reset</Button>
          </div>
        </div>
      </Block>

      <Tabs
        aria-label="Tabs"
        value={tab}
        onValueChange={setTab}
        items={[
          { value: "one", label: "Overview", content: <p className="text-muted">First panel</p> },
          { value: "two", label: "Doctor", badge: 2, content: <p className="text-muted">Second panel</p> },
          { value: "three", label: "Settings", content: <p className="text-muted">Third panel</p> },
        ]}
      />

      <section className="flex flex-col gap-3">
        <SectionLabel>Groups tab (users screen)</SectionLabel>
        <KitCache>
          <GroupsTab creating={false} onCreatingChange={() => {}} />
        </KitCache>
      </section>

      <section className="flex flex-col gap-3">
        <SectionLabel>User card: devices in the two ways (Office PC: "By key"; MacBook Air: "Link changed")</SectionLabel>
        <KitCache>
          <DevicesPanel user={kitUser} devices={kitDevices} profiles={[{ id: "p_awg", name: "AWG 3.1 · QUIC", protocol: "awg" } as never]} actions={{ refresh: async () => {}, revoke: async () => {} } as never} onEditGroup={() => {}} />
        </KitCache>
      </section>

      <NodeDemos />
      <PortsKit />

      <HealthWarpKit />
      <PagingKit />
      <UpdatesKit />

      <Card className="border-dashed">
        <EmptyState icon={<NavIcon name="nodes" size={20} />} title="Nothing here yet" action={<Button variant="primary">Add node</Button>}>
          An empty state: heavy title, one muted line, one action.
        </EmptyState>
      </Card>

      <FleetKit />
      <SettingsDemos />
      <PolishKit />
      <DnsLinkKit />
      <EdgeKit />
      <DecoyKit />
      <UsersKit />

      <LinkModal
        target={
          linkDemo
            ? {
                id: "usr_demo",
                name: "Marina",
                happ: linkDemo !== "awg",
                amnezia: linkDemo === "awg",
                ...(linkDemo === "nothing" && { access: { happ: false, amnezia: false }, active: true, group: { id: "grp_t", name: "тест 2", givesNothing: true } }),
              }
            : null
        }
        initialUrl="https://vpn.example.com/y4pt5Qm8ZrLw3VcXnB7d2Ke9HsAf"
        initialPassword={linkDemo === "plain" ? "" : "rrgu-k3m9"}
        onClose={() => setLinkDemo(null)}
      />

      <Modal
        open={open}
        onOpenChange={(o) => {
          setOpen(o);
          setRefused(false);
        }}
        title="Create user"
        description="A modal on the desktop, a bottom sheet on the phone."
        footer={
          <>
            <Button variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
            <Button variant="primary" onClick={() => setRefused(true)}>Create</Button>
          </>
        }
      >
        <TextField label="Name" placeholder="Lena" />
        {/* a refusal inside an open window stays in it, above the buttons; a toast is for when the window is gone */}
        {refused && <Notice tone="danger">{t("err.name_taken")}</Notice>}
      </Modal>
    </div>
  );
}
