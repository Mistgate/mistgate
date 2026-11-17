import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { profiles as profilesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { checksQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "@/screens/dev-kit-redesign";
import { checksOf, connectionsOf, GROUPS, NODES, NOW, nid, nodeRows, PROFILES, PROTOCOLS, relaysOf, summaries, type Fleet } from "@/screens/dev-kit-profiles-data";
import { ProfilePage, type PageScene } from "@/screens/profiles/page";
import type { PageData } from "@/screens/profiles/page-types";
import { fleetConnectionsQuery } from "@/screens/profiles/profiles-data";
import * as fixture from "@/screens/profiles/schema.fixtures";
import { seedTabs, tabFrames } from "@/screens/dev-kit-profile-tabs";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-profile: the profile page of the redesign (PnProfile) in every state of the board, each in a frame of its own
// width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the
// side-by-side with the board:
//   /dev-kit?only=profile-page&state=<profile>.<tab>[.<variant>][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// <profile>: hy1 (Hysteria2, one server did not start) | hy2 (through WARP) | hy3 (on no node) | vl1 (VLESS XHTTP) | vl2 (VLESS TCP, a check fails,
// one server starts) | vl3 (behind a CDN, in no group) | aw1 (AmneziaWG 3.1) | aw2 (2.0, one node off) | tt1 (TrustTunnel) | tg1 (MTProto, on no node).
// <tab>: settings | servers | people. <variant> is what a scene adds: dirty (changes typed) | stale (saved elsewhere) | gone (deleted) |
// load | err | nf (the page's own load states) | the windows put, take, twin, del and what the tabs name (dev-kit-profile-tabs.tsx).
// Made-up data of the board: de1 … spb1, the groups family and friends, documentation addresses.

const SCHEMAS: Record<string, string> = {
  hysteria2: fixture.hy2,
  vless: fixture.vless,
  trusttunnel: fixture.trusttunnel,
  awg: JSON.stringify({
    type: "object",
    properties: {
      name: { type: "string", title: "Name", "x-group": "basics", "x-order": 5 },
      version: { type: "string", enum: ["2.0", "3.1"], title: "Protocol version", "x-group": "basics", "x-widget": "segmented", "x-order": 10, "x-critical": true },
      port: { type: "integer", title: "UDP port", "x-group": "basics", "x-order": 20, "x-critical": true },
      mtu: { type: "integer", title: "MTU", "x-group": "advanced", "x-order": 30 },
      keepalive: { type: "integer", title: "Keepalive", "x-unit": "s", "x-group": "advanced", "x-order": 40 },
      egress: { type: "string", enum: ["direct", "warp"], "x-enum-labels": { direct: "From the node's address", warp: "Through WARP" }, title: "Exit", "x-group": "basics", "x-widget": "segmented", "x-order": 25 },
      obfuscation: { type: "object", title: "Obfuscation", "x-group": "obfuscation", "x-order": 50, properties: { preset: { type: "string", enum: ["dns", "quic"], title: "Mimicry" }, jc: { type: "integer", title: "Jc" } } },
    },
  }),
  mtproto: JSON.stringify({
    type: "object",
    properties: {
      port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10, "x-critical": true },
      fake_tls: { type: "object", title: "Fake TLS", "x-group": "obfuscation", "x-order": 20, properties: { kind: { type: "string", enum: ["own", "site"], title: "Name in the handshake", "x-critical": true }, domain: { type: "string", title: "Site", "x-critical": true } } },
    },
  }),
};

const SETTINGS: Record<string, string> = {
  hy1: JSON.stringify({ port: 443, hop: { from: 0, to: 0 }, tls_mode: "acme_domain", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  hy2: JSON.stringify({ port: 8443, hop: { from: 0, to: 0 }, tls_mode: "acme_domain", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  hy3: JSON.stringify({ port: 2053, hop: { from: 0, to: 0 }, tls_mode: "self_signed", obfs: { type: "none", password: "" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true }),
  aw1: JSON.stringify({ name: "AWG 3.1", version: "3.1", port: 47213, mtu: 1280, keepalive: 31, egress: "direct", obfuscation: { preset: "dns", jc: 5 } }),
  aw2: JSON.stringify({ name: "AWG 2.0", version: "2.0", port: 33183, mtu: 1280, keepalive: 25, egress: "direct", obfuscation: { preset: "quic", jc: 5 } }),
  tg1: JSON.stringify({ port: 8443, fake_tls: { kind: "own", domain: "" } }),
};

const key = (id: string) => id.replace("prf_", "");

/** The profile a scene shows, as the panel answers GetProfile for it. */
export function pageDataOf(profileKey: string): PageData {
  const spec = PROFILES.find((p) => key(p.id) === profileKey) ?? PROFILES[0]!;
  const info = PROTOCOLS.find((p) => p.id === spec.protocol)!;
  const sum = summaries().find((s) => s.id === spec.id)!;
  const inbounds = Object.entries(spec.sv).map(([node, st]) => ({
    id: `inb_${key(spec.id)}_${node}`,
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
  return { tab, ...(dlg && { dlg }), ...(variant.split(".").includes("stale") && { stale: true }), ...(variant.split(".").includes("gone") && { gone: true }), variant };
}

const fleetOf = (variant: string): Fleet => ({ out: variant.split(".").includes("out"), noChecks: variant.split(".").includes("nochecks") });

/** The panel's calls of the page's own load states (the scene is its own page, so patching the shared client is safe). */
function fakeCalls() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.previewProfile = async () => ({ errors: [], warnings: [], clientPreview: "hysteria2://••••@de1.example.com:443/\n?sni=de1.example.com&obfs=salamander&obfs-password=••••\n#de1 · hy2 · 443", clientLabel: "de1", formats: [], obfuscationScore: undefined });
  p.updateProfile = async () => ({ profile: undefined, impact: undefined });
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  const { variant } = partsOf(state);
  fakeCalls();
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
    if (params.get("fx") === "1") setEffectsEnabled(true);
    const role = params.get("role");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="profiles" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <ProfilePage data={pageDataOf(partsOf(state).profile)} scene={sceneOf(state)} />
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
  { id: "profile-hy1-stale", state: "hy1.settings.stale", title: "Saved in another window", w: 1440, h: 1100 },
  { id: "profile-hy1-readonly", state: "hy1.settings", title: "An observer: the form reads, nothing changes", w: 1440, h: 1100, params: "&role=readonly" },
  { id: "profile-hy3", state: "hy3.settings", title: "On no node: the verdict says so", w: 1440, h: 1000 },
  { id: "profile-vl3", state: "vl3.settings", title: "In no group: the verdict says so", w: 1440, h: 1200 },
  { id: "profile-vl2", state: "vl2.settings", title: "VLESS TCP", w: 1440, h: 1400 },
  { id: "profile-aw1", state: "aw1.settings", title: "AmneziaWG 3.1", w: 1440, h: 1400 },
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
