import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { awg as awgApi, profiles as profilesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { checksQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { noteServerVersion, setLoadedVersion } from "@/lib/stale";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "@/screens/dev-kit-redesign";
import { checksOf, connectionsOf, GROUPS, NODES, NOW, nid, nodeRows, PROFILES, PROTOCOLS, relaysOf, summaries, type Fleet } from "@/screens/dev-kit-profiles-data";
import { ProfilePage, type PageScene } from "@/screens/profiles/page";
import { setAt, type Settings } from "@/screens/profiles/schema";
import type { PageData } from "@/screens/profiles/page-types";
import { fleetConnectionsQuery } from "@/screens/profiles/profiles-data";
import * as fixture from "@/screens/profiles/schema.fixtures";
import { seedTabs, tabFrames, tabsData } from "@/screens/dev-kit-profile-tabs";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-profile: the profile page of the redesign (PnProfile) in every state of the board, each in a frame of its own
// width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the
// side-by-side with the board:
//   /dev-kit?only=profile-page&state=<profile>.<tab>[.<variant>][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// <profile>: hy1 (Hysteria2, one server did not start) | hy2 (through WARP) | hy3 (on no node) | vl1 (VLESS XHTTP) | vl2 (VLESS TCP, a check fails,
// one server starts) | vl3 (behind a CDN, in no group) | aw1 (AmneziaWG 3.1) | aw2 (2.0, one node off) | tt1 (TrustTunnel) | tg1 (MTProto, on no node).
// <tab>: settings | servers | people. <variant> is what a scene adds: dirty (changes typed) | panelold (the panel was updated, the page is old) | stale (saved elsewhere) | gone (deleted) |
// load | err | nf (the page's own load states) | the windows put, take, twin, del and what the tabs name (dev-kit-profile-tabs.tsx).
// Made-up data of the board: de1 … spb1, the groups family and friends, documentation addresses.

const SCHEMAS: Record<string, string> = {
  hysteria2: fixture.hy2,
  vless: fixture.vless,
  trusttunnel: fixture.trusttunnel,
  awg: fixture.awg,
  mtproto: JSON.stringify({
    type: "object",
    properties: {
      port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10, "x-critical": true },
      fake_tls: { type: "object", title: "Fake TLS", "x-group": "obfuscation", "x-order": 20, properties: { kind: { type: "string", enum: ["own", "site"], title: "Name in the handshake", "x-critical": true }, domain: { type: "string", title: "Site", "x-critical": true } } },
    },
  }),
};

/** AmneziaWG's settings as the panel hands them out (the values are made up: the header protection key is the mask the panel sends instead of a secret). */
function awgSettings(version: "3.1" | "2.0") {
  const v31 = version === "3.1";
  return {
    version,
    port: v31 ? 47213 : 33183,
    mtu: 1280,
    egress: "direct",
    subnet4: "10.66.4.0/22",
    subnet6: "fd66:66:0:1::/64",
    obfuscation: {
      preset: "dns",
      domain: "",
      jc: 5,
      jmin: 24,
      jmax: 71,
      s1: 21,
      s2: 21,
      s3: v31 ? 21 : 0,
      s4: v31 ? 21 : 0,
      h1: v31 ? "1" : "1180000-1190000",
      h2: v31 ? "2" : "1200000-1210000",
      h3: v31 ? "3" : "1220000-1230000",
      h4: v31 ? "4" : "1240000-1250000",
      i1: "<b 0x1a2b3c4d><r 16><t>",
      i2: "",
      i3: "",
      i4: "",
      i5: "",
      per_device_signature: true,
      signature_seed: "9f2c",
      header_protection_key: v31 ? "••••" : "",
      random_trailers: v31,
      disable_cookies: false,
      content_padding_addition: v31 ? "16-64" : "",
      rekey_after_time: v31 ? "100-140" : "",
      rekey_timeout: v31 ? "4-6" : "",
      reject_after_time: v31 ? "170-190" : "",
      keepalive_timeout: v31 ? "9-11" : "",
      max_handshake_attempts: v31 ? "18-22" : "",
      persistent_keepalive: v31 ? "31" : "25",
    },
  };
}

const SETTINGS: Record<string, string> = {
  hy1: JSON.stringify({ port: 443, hop: { from: 0, to: 0 }, tls_mode: "acme_domain", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  hy2: JSON.stringify({ port: 8443, hop: { from: 0, to: 0 }, tls_mode: "acme_domain", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  hy3: JSON.stringify({ port: 2053, hop: { from: 0, to: 0 }, tls_mode: "self_signed", obfs: { type: "none", password: "" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  aw1: JSON.stringify(awgSettings("3.1")),
  aw2: JSON.stringify(awgSettings("2.0")),
  tg1: JSON.stringify({ port: 8443, fake_tls: { kind: "own", domain: "" } }),
};

const kid = (id: string) => id.replace("prf_", "");

/** The profile a scene shows, as the panel answers GetProfile for it. */
export function pageDataOf(profileKey: string): PageData {
  const spec = PROFILES.find((p) => kid(p.id) === profileKey) ?? PROFILES[0]!;
  const info = PROTOCOLS.find((p) => p.id === spec.protocol)!;
  const sum = summaries().find((s) => s.id === spec.id)!;
  const inbounds = Object.entries(spec.sv).map(([node, st]) => ({
    id: `inb_${kid(spec.id)}_${node}`,
    profileId: spec.id,
    profileName: spec.name,
    protocol: spec.protocol,
    nodeId: nid(node),
    nodeName: node,
    port: spec.port,
    tlsServerName: "",
    state: st === "ok" ? InboundState.ACTIVE : st === "bad" ? InboundState.FAILED : st === "start" ? InboundState.PENDING : InboundState.DISABLED,
    lastError: st === "bad" ? (spec.err?.[node] ?? "") : "",
    certPinSha256: "",
    certNotAfterUnix: spec.protocol === "hysteria2" || spec.protocol === "trusttunnel" ? NOW + 60 * 86400 : 0,
    egress: spec.warp ? "warp" : "direct",
    portOverride: 0,
    tlsServerNameOverride: "",
    enabled: st !== "off",
    directInSubscriptions: true,
    cdn: false,
  }));
  const settings = SETTINGS[profileKey] ?? (spec.protocol === "vless" ? JSON.stringify({ security: "reality", port: spec.port }) : spec.protocol === "trusttunnel" ? JSON.stringify({ port: 443 }) : "{}");
  return {
    profile: sum as never,
    info: { ...info, settingsSchemaJson: SCHEMAS[spec.protocol] ?? "{}", defaultSettingsJson: settings } as never,
    inbounds: inbounds as never,
    initial: { name: spec.name, settingsJson: settings },
  };
}

type Parts = { profile: string; tab: "settings" | "servers" | "people"; variant: string };
export const partsOf = (state: string): Parts => {
  const [profile = "hy1", tab = "settings", ...rest] = state.split(".");
  return { profile, tab: tab === "servers" || tab === "people" ? tab : "settings", variant: rest.join(".") };
};

/** The scene's own part of the page: the tab, the window and the page's states. */
export function sceneOf(state: string): PageScene {
  const { tab, variant } = partsOf(state);
  const dlg = (["put", "take", "twin", "del"] as const).find((d) => variant.split(".").includes(d));
  const v = variant.split(".");
  const bumped = (b: Settings) => setAt(b, ["port"], 9443);
  return {
    tab,
    ...(dlg && { dlg }),
    ...(v.includes("stale") && { stale: true }),
    ...(v.includes("gone") && { gone: true }),
    ...(v.includes("saved") && { saved: true }),
    ...((v.includes("dirty") || v.includes("impact")) && { seed: { settings: bumped, ...(v.includes("impact") && { impact: { criticalFields: ["/port"], inboundsRestarted: 2, usersOnline: 3, devicesNeedReissue: 0, affectedUserNames: ["alice", "bob", "carol"] } as never }) } }),
    ...(v.includes("errors") && { seed: { settings: (b: Settings) => setAt(b, ["port"], 0), name: "" } }),
    variant,
  };
}

const fleetOf = (variant: string): Fleet => ({ out: variant.split(".").includes("out"), noChecks: variant.split(".").includes("nochecks") });

/** The panel's calls of the page's own load states (the scene is its own page, so patching the shared client is safe). */
function fakeCalls(variant: string) {
  const p = profilesApi as unknown as Record<string, unknown>;
  // the panel was updated while this page stayed open: the page is the old version, the API answers as the new one
  setLoadedVersion(variant.split(".").includes("panelold") ? "0.1.70" : "");
  if (variant.split(".").includes("panelold")) noteServerVersion(new Headers({ "x-mistgate-version": "0.1.71" }));
  const errors = variant.split(".").includes("errors") ? [{ pointer: "/port", code: "invalid", message: "Port must be from 1 to 65535", params: {} }] : [];
  const warnings = variant.split(".").includes("warns") ? [{ pointer: "/port", code: "preset_port", message: "The look of DNS is usually on port 53", params: { preset: "dns" } }] : [];
  // AmneziaWG's preview is the .conf of a device, the remarks and the score of what the form holds now (the panel computes them; here the board's rules)
  const awgView = (json: string) => {
    const st = JSON.parse(json) as Record<string, unknown>;
    const ob = (st.obfuscation ?? {}) as Record<string, unknown>;
    const v31 = st.version !== "2.0";
    const ka = Number(String(ob.persistent_keepalive ?? "").split("-")[0]) || 0;
    const dnsOff = ob.preset === "dns" && Number(st.port) !== 53;
    const warns = [
      ...(ka > 30 ? [{ pointer: "/obfuscation/persistent_keepalive", code: "keepalive_over_nat", message: "A keepalive of 30 s is longer than the UDP timeout of many NATs", params: { max: String(ka) } }] : []),
      ...(dnsOff ? [{ pointer: "/port", code: "preset_port", message: "DNS traffic is normally on port 53", params: { preset: "dns", ports: "53" } }] : []),
    ];
    const items = [
      ...(ob.per_device_signature === true ? [{ code: "per_device_signature", delta: 3, pointer: "/obfuscation/per_device_signature", params: {} }] : []),
      ...(ka > 30 ? [{ code: "keepalive_over_nat", delta: -5, pointer: "/obfuscation/persistent_keepalive", params: {} }] : []),
      ...(dnsOff ? [{ code: "preset_port", delta: -5, pointer: "/port", params: { preset: "dns", ports: "53" } }] : []),
    ];
    const base = v31 ? 95 : 65;
    const conf = `[Interface]
PrivateKey = ••••
Address = 10.66.4.x/32
MTU = ${String(st.mtu)}
Jc = ••••
Jmin … I5 = ••••

[Peer]
PublicKey = ••••
Endpoint = de1.example.org:${String(st.port)}
PersistentKeepalive = ${String(ob.persistent_keepalive ?? "")}`;
    return {
      errors,
      warnings: warns,
      clientPreview: conf,
      clientLabel: "AmneziaVPN · .conf",
      formats: [{ format: SubFormat.MIHOMO_YAML, ok: true, note: "" }],
      obfuscationScore: { value: items.reduce((n, i) => n + i.delta, base), base, tier: v31 ? "header_protection" : "ranges", items },
    };
  };
  p.previewProfile = async (req: { protocol: string; settingsJson: string }) =>
    req.protocol === "awg"
      ? awgView(req.settingsJson)
      : { errors, warnings, clientPreview: "hysteria2://••••@de1.example.com:443/\n?sni=de1.example.com&obfs=salamander&obfs-password=••••\n#de1 · hy2 · 443", clientLabel: "de1", formats: [], obfuscationScore: undefined };
  p.updateProfile = async () => ({ profile: undefined, impact: undefined });
  // AmneziaWG's panel asks for the mimicry cards (without an answer the page says it cannot reach the panel)
  const a = awgApi as unknown as Record<string, unknown>;
  const preset = (id: string, name: string, usesDomain: boolean, ports: number[], varies: boolean) => ({ id, name, versions: ["3.1", "2.0"], usesDomain, naturalPorts: ports, variesPerDevice: varies });
  a.listMimicryPresets = async () => ({
    presets: [
      preset("dns", "DNS", true, [53], true),
      preset("quic", "QUIC", true, [443], true),
      preset("curl_quic", "QUIC (curl)", true, [443], true),
      preset("stun", "STUN", false, [], false),
      preset("webrtc", "WebRTC", false, [], false),
      preset("sip", "SIP", true, [5060], true),
      preset("ntp", "NTP", false, [], false),
      preset("rtp", "RTP", false, [], false),
      preset("ssdp", "SSDP", false, [], false),
      preset("dtls", "DTLS", false, [], false),
      preset("custom", "Custom", false, [], false),
    ],
    domains: ["cdn.example.org", "static.example.net", "www.example.com"],
  });
  a.generateObfuscation = async () => ({ obfuscationJson: JSON.stringify({ preset: "dns", per_device_signature: true, jc: 7, jmin: 30, jmax: 80, s1: 24, s2: 24, s3: 24, s4: 24, i1: "<b 0x5e6f7a8b><r 16><t>", signature_seed: "4d1e" }) });
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  const { variant } = partsOf(state);
  fakeCalls(variant);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0 } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: nodeRows(), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: NODES.length, nodes: nodeRows().map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: NodeStatus.ONLINE, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, { nowUnix: NOW, panel: { version: "v0.1.71", built: NOW, update: { version: "v0.1.71", url: "", checkedUnix: NOW, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: NOW }, nodes: [] } as never);
  c.setQueryData(profileListQuery.queryKey, summaries() as never);
  c.setQueryData(protocolsQuery.queryKey, PROTOCOLS as never);
  c.setQueryData(groupsQuery.queryKey, GROUPS as never);
  c.setQueryData(checksQuery("").queryKey, checksOf(fleetOf(variant)) as never);
  c.setQueryData(fleetConnectionsQuery.queryKey, connectionsOf() as never);
  c.setQueryData(relaysQuery("").queryKey, (role === Role.OWNER ? relaysOf() : []) as never);
  seedTabs(c, state, role);
  return c;
}

/** One whole page of the scene: the page in the real shell, at the window's own width. */
export function ProfileScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "hy1.settings";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    const role = params.get("role");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="profiles" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <ProfilePage data={tabsData(pageDataOf(partsOf(state).profile), state)} scene={sceneOf(state)} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
export const frames: Frame[] = [
  { id: "profile-hy1", state: "hy1.settings", title: "Hysteria2 · 443: a server did not start (1440)", w: 1440, h: 1100 },
  { id: "profile-hy1-light", state: "hy1.settings", title: "The same on the light theme", w: 1440, h: 1100, params: "&theme=light" },
  { id: "profile-hy1-1000", state: "hy1.settings", title: "A 1000 window: the rail", w: 1000, h: 1100 },
  { id: "profile-hy1-phone", state: "hy1.settings", title: "A phone, 400 wide", w: 400, h: 1800 },
  { id: "profile-hy1-fa", state: "hy1.settings", title: "Right to left (fa)", w: 1440, h: 1100, params: "&lang=fa" },
  { id: "profile-hy1-dirty", state: "hy1.settings.dirty", title: "Changes typed: the bar, the dot on the tab", w: 1440, h: 1100 },
  { id: "profile-hy1-impact", state: "hy1.settings.impact", title: "\"Save changes?\": what the critical change does to people", w: 1440, h: 1100 },
  { id: "profile-hy1-impact-phone", state: "hy1.settings.impact", title: "The same window on a phone (a sheet)", w: 400, h: 900 },
  { id: "profile-hy1-errors", state: "hy1.settings.errors", title: "A field the server refuses: the remark, the red row, Save cannot be pressed", w: 1440, h: 1100 },
  { id: "profile-aw1-warns", state: "aw1.settings.warns", title: "A warning of the server: the remark with \"To the field\"", w: 1440, h: 1400 },
  { id: "profile-hy1-saved", state: "hy1.settings.saved", title: "Saved: the bar says so", w: 1440, h: 1100 },
  { id: "profile-hy1-panelold", state: "hy1.settings.panelold", title: "The panel was updated while the page was open: the bar says so and offers Reload", w: 1440, h: 1100 },
  { id: "profile-hy1-panelold-phone", state: "hy1.settings.panelold", title: "The same on a phone", w: 400, h: 1700 },
  { id: "profile-hy1-stale", state: "hy1.settings.stale", title: "Saved in another window", w: 1440, h: 1100 },
  { id: "profile-hy1-readonly", state: "hy1.settings", title: "An observer: the form reads, nothing changes", w: 1440, h: 1100, params: "&role=readonly" },
  { id: "profile-hy3", state: "hy3.settings", title: "On no node: the verdict says so", w: 1440, h: 1000 },
  { id: "profile-vl3", state: "vl3.settings", title: "In no group: the verdict says so", w: 1440, h: 1200 },
  { id: "profile-vl2", state: "vl2.settings", title: "VLESS TCP", w: 1440, h: 1400 },
  { id: "profile-aw1", state: "aw1.settings", title: "AmneziaWG 3.1: Основное, Маскировка и обход, Сеть; the remarks have their buttons, the score is a pill in the second card", w: 1440, h: 2000 },
  { id: "profile-aw1-light", state: "aw1.settings", title: "AmneziaWG on the light theme", w: 1440, h: 2000, params: "&theme=light" },
  { id: "profile-aw1-phone", state: "aw1.settings", title: "AmneziaWG on a phone, 400 wide", w: 400, h: 3400 },
  { id: "profile-aw1-fa", state: "aw1.settings", title: "AmneziaWG right to left (fa)", w: 1440, h: 2000, params: "&lang=fa" },
  { id: "profile-aw2", state: "aw2.settings", title: "AmneziaWG 2.0: the version's card is picked and says what 2.0 is for, the score is lower, the oldest clients are other ones", w: 1440, h: 2000 },
  { id: "profile-aw1-score", state: "aw1.settings.score", title: "AmneziaWG: what the score is made of (the pill opens the card over the form)", w: 1440, h: 1500 },
  { id: "profile-aw1-adv", state: "aw1.settings.adv", title: "AmneziaWG: the fold of the block, the values are dots", w: 1440, h: 2600 },
  { id: "profile-aw1-show", state: "aw1.settings.adv.show", title: "AmneziaWG: \"Show values\" brings the values into boxes", w: 1440, h: 2600 },
  { id: "profile-aw1-conf", state: "aw1.settings.adv.conf", title: "AmneziaWG: \"Fill from .conf\"", w: 1440, h: 2700 },
  { id: "profile-aw1-net", state: "aw1.settings.net", title: "AmneziaWG: the fold of the network, the client networks are kept", w: 1440, h: 1700 },
  { id: "profile-aw-free", state: "aw1.settings.net.free", title: "AmneziaWG on no node: the client networks can be typed", w: 1440, h: 1700 },
  { id: "profile-aw1-warp", state: "aw1.settings.warp", title: "AmneziaWG through WARP: the nodes without it are named, the copy is \"without WARP\"", w: 1440, h: 1700 },
  { id: "profile-aw1-dirty", state: "aw1.settings.dirty", title: "AmneziaWG: a change typed", w: 1440, h: 2000 },
  { id: "profile-aw1-readonly", state: "aw1.settings", title: "AmneziaWG seen by an observer", w: 1440, h: 2000, params: "&role=readonly" },
  { id: "profile-tt1", state: "tt1.settings", title: "TrustTunnel", w: 1440, h: 1200 },
  { id: "profile-tg1", state: "tg1.settings", title: "MTProto", w: 1440, h: 1000 },
  ...tabFrames,
];

export function PnProfileKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Profile</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=profile-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
