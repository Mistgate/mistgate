import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { TwinPanel, twinPlanKey, type Egress } from "@/screens/profiles/twin";
import { WherePanel } from "@/screens/profiles/where";
import { groupsQuery } from "@/screens/users/rpc";
import { AccentPicker } from "@/components/accent-picker";
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
import { useT } from "@/i18n";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { profiles as profilesApi } from "@/lib/api";
import { ConnectError, Code } from "@connectrpc/connect";
import { AfterCreate, useAfterCreate } from "@/screens/profiles/after-create";
import { DeployDialog } from "@/screens/profiles/deploy";
import { GroupsTab, ReachLine } from "@/screens/users/groups";
import { profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { DevicesPanel } from "@/screens/users/user-detail";
import { dnsPresetsQuery } from "@/screens/subscriptions/queries";
import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { FleetKit } from "./dev-kit-fleet";
import { SettingsDemos } from "./dev-kit-settings";
import { PolishKit } from "./dev-kit-polish";
import { HealthWarpKit } from "@/screens/dev-kit-health-warp";
import { PagingKit, UpdatesKit } from "./dev-kit-redesign";

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

// "Where it runs" on the profile page, on a private query cache so it needs no server: three states, one per verdict
const demoGroups = [
  { id: "grp_1", name: "family", profileIds: ["prf_1"], userCount: 5, dnsPresetId: "" },
  { id: "grp_2", name: "friends", profileIds: ["prf_1", "prf_2"], userCount: 2, dnsPresetId: "" },
  { id: "grp_3", name: "test", profileIds: ["prf_2"], userCount: 1, dnsPresetId: "" },
];
const demoNodes = [
  { id: "nod_1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE" },
  { id: "nod_2", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI" },
];
const demoProfile = (over: object) => ({ id: "prf_1", name: "Amnezia 3.1 test", protocol: "awg", nodeCount: 1, userCount: 7, version: 1, ...over }) as unknown as ProfileSummary;
const demoInbound = (over: object) => ({ id: "inb_1", profileId: "prf_1", nodeId: "nod_1", nodeName: "de1", port: 51820, state: InboundState.ACTIVE, lastError: "", ...over }) as unknown as Inbound;

function WhereDemo({ profile, inbounds, groups, role = Role.OWNER, twin }: { profile: ProfileSummary; inbounds: Inbound[]; groups: typeof demoGroups; role?: Role; twin?: boolean }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(groupsQuery.queryKey, groups as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: demoNodes } as never);
    c.setQueryData(meQuery.queryKey, { admin: { role } } as never);
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <WherePanel profile={profile} inbounds={inbounds} twin={twin ? { egress: "direct", dirty: false } : undefined} />
    </QueryClientProvider>
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

/**
 * Dev only: the window's calls answered here, refusing the way the panel does: nl1 has the port taken (and names a free
 * one), fi1 sits on an IP, where Let's Encrypt needs a domain.
 */
function fakeDeploy() {
  (profilesApi as unknown as { createInbound: unknown }).createInbound = async (r: { profileId: string; nodeId: string; portOverride: number; tlsServerNameOverride: string }) => {
    await new Promise((res) => setTimeout(res, 400));
    if (r.nodeId === "nod_2" && !r.portOverride) throw new ConnectError("port_taken: port=443&profile=hy2 · WARP · 8443&free=8444", Code.AlreadyExists);
    if (r.nodeId === "nod_3" && r.profileId === "p_hy" && !r.tlsServerNameOverride) throw new ConnectError("acme_needs_domain: address=203.0.113.10", Code.FailedPrecondition);
    return { inbound: {} };
  };
}

function DeployDemo() {
  const [open, setOpen] = useState<"" | "hy2" | "awg">("");
  const profile =
    open === "awg" ? { id: "p_awg", name: "AWG 3.1 · QUIC", protocol: "awg" } : { id: "p_hy", name: "hy2 · 443 · Salamander", protocol: "hysteria2", tlsMode: "acme_domain", sni: "" };
  return (
    <KitCache>
      <Button
        onClick={() => {
          fakeDeploy();
          setOpen("hy2");
        }}
      >
        Hysteria2 + Let's Encrypt
      </Button>
      <Button
        onClick={() => {
          fakeDeploy();
          setOpen("awg");
        }}
      >
        AmneziaWG, first on a node
      </Button>
      <DeployDialog open={open !== ""} onOpenChange={(o) => !o && setOpen("")} profile={profile} inbounds={[{ nodeId: "nod_1" }]} />
    </KitCache>
  );
}

function AfterDemo() {
  const info = kitProtocols[0] as unknown as ProtocolInfo;
  const a = useAfterCreate(info, "");
  return <AfterCreate a={a} info={info} settings={{ port: 443, tls_mode: "acme_domain", sni: "" }} />;
}

const kitUser = { id: "usr_demo", name: "Марина", groupName: "Все", deviceLimit: 5, devicesUsed: 2, apps: { happ: true, amnezia: true } } as never;
const kitDevices = [
  { id: "dev_i", platform: "", model: "", awgProfileId: "", awgProfileName: "", awgVersion: "", protocols: ["hysteria2"], online: true, lastSeenUnix: 0, firstSeenUnix: 0, lastHandshakeUnix: 0, stale: false, address: "" },
  {
    id: "dev_k",
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

// "The same server with and without WARP": the action and its dialog, the plan filled in the cache (the server's dry run)
function TwinDemo({ profile, egress, plan }: { profile: ProfileSummary; egress: Egress; plan: object }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(groupsQuery.queryKey, demoGroups as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: demoNodes.map((n, i) => ({ ...n, warp: { state: i === 0 ? WarpState.UP : WarpState.NOT_CONFIGURED } })) } as never);
    c.setQueryData(meQuery.queryKey, { admin: { role: Role.OWNER } } as never);
    c.setQueryData(twinPlanKey(profile.id, egress === "warp" ? "direct" : "warp", profile.version), plan as never);
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <TwinPanel profile={profile} egress={egress} dirty={false} />
    </QueryClientProvider>
  );
}

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
        <IconButton variant="close" aria-label="Close">×</IconButton>
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

      <Block title="Accent">
        <AccentPicker />
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
        <SectionLabel>Where it runs (profile page): works / on no node / in no group / read-only</SectionLabel>
        <WhereDemo profile={demoProfile({ protocol: "hysteria2" })} inbounds={[demoInbound({}), demoInbound({ id: "inb_2", nodeId: "nod_2", nodeName: "fi1", state: InboundState.FAILED, lastError: "bind: address already in use" })]} groups={demoGroups} twin />
        <WhereDemo profile={demoProfile({ userCount: 0 })} inbounds={[]} groups={demoGroups.slice(2)} />
        <WhereDemo profile={demoProfile({ userCount: 0, protocol: "hysteria2" })} inbounds={[demoInbound({})]} groups={demoGroups.slice(2)} />
        <WhereDemo profile={demoProfile({})} inbounds={[demoInbound({})]} groups={demoGroups} role={Role.READONLY} />
      </section>

      <section className="flex flex-col gap-3">
        <SectionLabel>Same server with and without WARP (profile page): direct profile / WARP profile (AmneziaWG)</SectionLabel>
        <TwinDemo profile={demoProfile({ protocol: "hysteria2", name: "files" })} egress="direct" plan={{ name: "files · WARP", egress: "warp", port: 8443, nodeIds: ["nod_1", "nod_2"], groupIds: ["grp_1", "grp_2"], hopDropped: true }} />
        <TwinDemo profile={demoProfile({ name: "Amnezia 3.1 test · WARP" })} egress="warp" plan={{ name: "Amnezia 3.1 test", egress: "direct", port: 31877, nodeIds: ["nod_1"], groupIds: ["grp_1"], hopDropped: false }} />
      </section>

      <section className="flex flex-col gap-3">
        <SectionLabel>Groups tab (users screen) and what a person of a group gets (create-user form)</SectionLabel>
        <KitCache>
          <GroupsTab creating={false} onCreatingChange={() => {}} />
          <Card className="flex flex-col divide-y divide-line px-4">
            {kitGroups.map((g) => (
              <ReachLine key={g.id} group={g} />
            ))}
          </Card>
        </KitCache>
      </section>

      <Block title="Put on nodes (profile page): results per node; nl1 has the port taken, fi1 is an IP (Let's Encrypt needs a domain); the AWG backend of a node's first AWG profile">
        <DeployDemo />
      </Block>

      <section className="flex flex-col gap-3">
        <SectionLabel>New profile: right after it is created (a group with Hysteria2 is left alone, one without gets it)</SectionLabel>
        <KitCache>
          <AfterDemo />
        </KitCache>
      </section>

      <section className="flex flex-col gap-3">
        <SectionLabel>User card: devices in the two ways</SectionLabel>
        <KitCache>
          <DevicesPanel user={kitUser} devices={kitDevices} profiles={[{ id: "p_awg", name: "AWG 3.1 · QUIC", protocol: "awg" } as never]} actions={{ refresh: async () => {}, revoke: async () => {} } as never} onEditGroup={() => {}} />
        </KitCache>
      </section>

      <NodeDemos />

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
