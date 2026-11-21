import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { awg as awgApi, groups as groupsApi, profiles as profilesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { checksQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "@/screens/dev-kit-redesign";
import { ProfileNewScreen, type WizardDemo } from "@/screens/profiles/wizard";
import { hy2, trusttunnel, vless } from "@/screens/profiles/schema.fixtures";
import { emptyRun, type RunState } from "@/screens/profiles/wizard-run";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-profile-new: the new-profile wizard of the redesign in every state, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the
// side-by-side with the board:
//   /dev-kit?only=profile-new-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// states: t-fast (the first step, the board's default) | t-router (another card chosen) | t-own | t-few (a panel with two protocols)
//   s2-fast | s2-phone | s2-router | s2-router-old | s2-cdn | s2-cdn-empty (the CDN host missing) | s2-tg | s2-tt | s2-own | s2-all (All settings open)
//   s2-awg-all (and AmneziaWG's own cards) | s2-port-bad | s2-name-empty | s2-name-taken | s2-loading (the preview not here yet)
//   s3-fast | s3-phone | s3-router | s3-tt | s3-fixed (a domain written under fi3) | s3-self (the other way out taken) | s3-pick (two nodes ticked by hand) |
//   s3-loading (the check not answered) | s3-none (no node) | s4 | s4-two (a group ticked off) | s4-none (nobody gets it) | s4-nogroup (no group in the panel)
//   s5-run (the run half way) | s5-ok | s5-fail (de3 did not start) | s5-mend (a node refused: its port field) | s5-bad (the profile refused) | s5-nonodes
//   loading (the protocols not here yet) | noschema | helper | readonly (only the owner makes a profile)
// Made-up data of the board: the nodes de1 … spb1 (twelve), the groups family and friends, example.com, documentation addresses.

const now = Math.floor(Date.now() / 1000);
const nid = (n: string) => `nod_${n}`;

// name, country, address, status, protocols it already serves, reach of RU, a dark network
type Spec = { n: string; cc: string; addr: string; up?: boolean; has: string[]; reach: "ok" | "part" | "bad" | "none"; dark?: string };
const fleet: Spec[] = [
  { n: "de1", cc: "DE", addr: "de1.example.com", has: ["hysteria2", "vless", "awg"], reach: "bad" },
  { n: "de2", cc: "DE", addr: "de2.example.com", has: ["hysteria2", "vless", "awg"], reach: "bad" },
  { n: "de3", cc: "DE", addr: "de3.example.com", has: ["vless", "awg"], reach: "bad" },
  { n: "nl1", cc: "NL", addr: "nl1.example.com", has: ["hysteria2", "vless"], reach: "part" },
  { n: "nl2", cc: "NL", addr: "nl2.example.com", has: ["hysteria2", "vless"], reach: "bad" },
  { n: "nl3", cc: "NL", addr: "nl3.example.com", up: false, has: ["hysteria2", "vless"], reach: "none" },
  { n: "fi1", cc: "FI", addr: "fi1.example.com", has: ["hysteria2", "vless", "awg"], reach: "ok" },
  { n: "fi2", cc: "FI", addr: "fi2.example.com", has: ["hysteria2", "vless", "awg"], reach: "ok" },
  { n: "fi3", cc: "FI", addr: "203.0.113.32", has: ["hysteria2"], reach: "ok" },
  { n: "msk1", cc: "RU", addr: "msk1.example.com", has: ["vless"], reach: "ok" },
  { n: "msk2", cc: "RU", addr: "msk2.example.com", has: ["vless"], reach: "ok" },
  { n: "spb1", cc: "RU", addr: "spb1.example.com", has: ["vless"], reach: "part", dark: "Operator A" },
];

const nodeRow = (s: Spec) => ({
  id: nid(s.n),
  name: s.n,
  countryCode: s.cc,
  location: "",
  provider: "",
  notes: "",
  address: s.addr,
  status: s.up === false ? NodeStatus.DOWN : NodeStatus.ONLINE,
  reason: undefined,
  protocols: s.has,
  online: [],
  trafficTodayBytes: 0,
  hasMetrics: false,
  cpuPct: 0,
  uptimeS: 0,
  lastSeenUnix: now,
  awgBackend: "auto",
  awgPrepare: { supported: true },
});

const verdict = { ok: CountryVerdict.OK, part: CountryVerdict.PARTIAL, bad: CountryVerdict.BLOCKED, none: CountryVerdict.UNSPECIFIED } as const;
const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 600, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (s: Spec) => ({
  nodeId: nid(s.n), country: "RU", verdict: verdict[s.reach], atUnix: now - 600, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdict[s.reach], tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: s.reach === "ok" ? [vantage("Rostelecom", "reach")] : [vantage(s.dark ?? "MTS", "dark"), vantage("Rostelecom", "reach")], sourceVerdicts: [],
});
const relay = (via: string, exit: string) => ({ id: `r_${via}_${exit}`, viaNodeId: nid(via), viaNodeName: via, exitNodeId: nid(exit), exitNodeName: exit, enabled: true, state: InboundState.ACTIVE, lastError: "", protocol: "vless", network: "tcp", port: 443, exitPort: 443 });

// ---- the protocols: the schemas the plugins publish, trimmed to what the form reads
const awgSchema = JSON.stringify({
  type: "object",
  properties: {
    version: { type: "string", enum: ["3.1", "2.0"], "x-widget": "segmented", title: "Protocol version", "x-group": "basics", "x-order": 10, "x-critical": true },
    port: { type: "integer", title: "UDP port", "x-group": "basics", "x-order": 20, "x-critical": true },
    mtu: { type: "integer", title: "MTU", "x-unit": "bytes", "x-group": "basics", "x-order": 30 },
    obfuscation: { type: "object", title: "Obfuscation", "x-group": "obfuscation", properties: { preset: { type: "string", title: "Look" }, per_device_signature: { type: "boolean", title: "Own signature for each device" } } },
    egress: { type: "string", enum: ["direct", "warp"], title: "Exit", "x-group": "advanced", "x-widget": "segmented" },
  },
});
const mtSchema = JSON.stringify({
  type: "object",
  properties: {
    port: { type: "integer", title: "TCP port", "x-group": "basics", "x-order": 10, "x-critical": true },
    fake_tls: { type: "object", title: "Fake TLS", "x-group": "basics", "x-order": 20, properties: { kind: { type: "string", enum: ["own", "site"], "x-enum-labels": { own: "This node's name", site: "Another site" }, "x-widget": "segmented", title: "Name in the handshake" }, domain: { type: "string", title: "Site" } } },
  },
});
const protocols = [
  { id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP], settingsSchemaJson: hy2, defaultSettingsJson: JSON.stringify({ port: 443, tls_mode: "acme_domain", obfs: { type: "salamander", password: "demo-salamander-secret" }, masquerade: { type: "decoy" }, egress: "direct" }) },
  { id: "vless", displayName: "VLESS", apps: [App.HAPP], settingsSchemaJson: vless, defaultSettingsJson: JSON.stringify({ port: 443, transport: "xhttp", security: "reality", reality: { target_kind: "self", server_names: [], short_ids: [], fingerprint: "chrome" }, xhttp: { mode: "auto", x_padding_bytes: "100-1000" }, egress: "direct" }) },
  { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA], settingsSchemaJson: awgSchema, defaultSettingsJson: JSON.stringify({ version: "3.1", port: 47213, mtu: 1280, egress: "direct", obfuscation: { preset: "dns", per_device_signature: true } }) },
  { id: "mtproto", displayName: "MTProto", apps: [App.TELEGRAM], settingsSchemaJson: mtSchema, defaultSettingsJson: JSON.stringify({ port: 443, fake_tls: { kind: "own" } }) },
  { id: "trusttunnel", displayName: "TrustTunnel", apps: [App.TRUST_TUNNEL], settingsSchemaJson: trusttunnel, defaultSettingsJson: JSON.stringify({ port: 443, tls_mode: "acme_domain", egress: "direct" }) },
];

const profileRows = [
  { id: "prf_main", name: "Main · 443", protocol: "hysteria2" },
  { id: "prf_site", name: "Site · 443", protocol: "vless" },
];
const group = (gid: string, name: string, color: string, userCount: number, profileIds: string[]) => ({ id: `grp_${gid}`, name, color, profileIds, userCount, extraUserCount: 0, happNodes: 4, amneziaNodes: 0, telegramNodes: 0, disabledNodeIds: [] as string[], dnsPresetId: "" });
const groupRows = [group("friends", "friends", "sky", 14, []), group("family", "family", "sand", 9, ["prf_main"])];

const preview = (protocol: string, settingsJson: string) => {
  const s = JSON.parse(settingsJson) as { port?: number; security?: string; cdn?: { host?: string } };
  const port = s.port ?? 443;
  if (protocol === "awg") return `[Interface]\nPrivateKey = ••••\nAddress = 10.66.x.x/32\n\n[Peer]\nEndpoint = node.example.org:${port}`;
  if (protocol === "vless") return `vless://••••@${s.security === "tls" ? (s.cdn?.host || "cdn.example.org") : "node.example.org"}:${port}\n?security=${s.security === "tls" ? "tls" : "reality&sid=••••"}&type=xhttp`;
  if (protocol === "mtproto") return `tg://proxy?server=node.example.org\n&port=${port}&secret=••••`;
  if (protocol === "trusttunnel") return `Mistgate app\nnode.example.org:${port} · HTTP/2\n\nOfficial apps\nlink tt:// — on the person's page`;
  return `hysteria2://••••@node.example.org:${port}/\n?obfs=salamander&obfs-password=••••`;
};

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
const never = () => new Promise<never>(() => {});

/** The panel's calls of this screen, answered here (the scene is its own page, so patching the shared client is safe). */
function fakeCalls(state: string) {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.previewProfile = async (r: { protocol: string; settingsJson: string }) => {
    if (state === "s2-loading") return never();
    return { errors: [], warnings: [], clientPreview: preview(r.protocol, r.settingsJson), clientLabel: "", obfuscationScore: undefined, formats: formatsOf(r.protocol, r.settingsJson) };
  };
  p.createInbound = async (r: { nodeId: string; protocol: string; settingsJson: string; portOverride: number; tlsServerNameOverride: string; validateOnly?: boolean }) => {
    if (state === "s3-loading") return never();
    const name = r.nodeId.replace("nod_", "");
    const s = JSON.parse(r.settingsJson || "{}") as { port?: number; tls_mode?: string; security?: string; reality?: { target_kind?: string }; fake_tls?: { kind?: string } };
    const port = r.portOverride || s.port || 443;
    const net = r.protocol === "hysteria2" || r.protocol === "awg" ? "udp" : "tcp";
    const needsDomain = r.protocol === "hysteria2" || r.protocol === "trusttunnel" ? s.tls_mode !== "self_signed" : r.protocol === "vless" ? (s.security === "tls" ? s.tls_mode !== "self_signed" : s.reality?.target_kind === "self") : r.protocol === "mtproto" ? s.fake_tls?.kind !== "site" : false;
    if (name === "spb1") throw new ConnectError("agent_too_old", Code.FailedPrecondition);
    if (name === "fi3" && needsDomain && !r.tlsServerNameOverride) throw new ConnectError(`${r.protocol === "vless" ? "reality" : r.protocol === "mtproto" ? "mtproto" : "acme"}_needs_domain: node=fi3&address=203.0.113.32`, Code.InvalidArgument);
    if (name === "nl1" && port === 443 && r.protocol !== "awg") throw new ConnectError(`port_taken: port=443&profile=Site · 443&free=8443`, Code.AlreadyExists);
    if (name === "nl2" && port === 443 && r.protocol === "hysteria2") throw new ConnectError(`port_lossy: port=443&node=nl2&sent=300&got=264&at=${now - 120}&sender=de2&free=2053`, Code.FailedPrecondition);
    if (!r.validateOnly) await wait(300);
    const warnings = name === "fi1" && port === 443 && (r.protocol === "vless" || r.protocol === "trusttunnel") ? [{ code: "hy2_site_taken", params: { node: "fi1", port: String(port), protocol: net } }] : [];
    return { inbound: { id: `inb_${name}`, port, nodeName: name, tlsServerName: r.tlsServerNameOverride }, warnings, freePort: 0 };
  };
  p.createProfile = async () => {
    await wait(300);
    return { profile: { id: "prf_new", name: "Hysteria2 · 443", version: 1 } };
  };
  p.getProfile = async () => ({ profile: { id: "prf_new", name: "Hysteria2 · 443", version: 1 }, settingsJson: "{}", inbounds: fleet.map((s) => ({ id: `inb_${s.n}`, nodeId: nid(s.n), nodeName: s.n, state: InboundState.ACTIVE, lastError: "", port: 443, protocol: "hysteria2" })) });
  p.updateInbound = async () => ({ inbound: {}, warnings: [] });
  (groupsApi as unknown as Record<string, unknown>).updateGroup = async () => ({});
  (groupsApi as unknown as Record<string, unknown>).createGroup = async () => ({ group: group("everyone", "everyone", "sky", 0, []) });
  (awgApi as unknown as Record<string, unknown>).generateObfuscation = async () => ({ obfuscationJson: JSON.stringify({ preset: "dns", per_device_signature: true }) });
  (awgApi as unknown as Record<string, unknown>).listMimicryPresets = async () => ({ presets: [], domains: [] });
}

/** Which formats the preview says carry a profile (the real server decides; a made-up answer in the same shape). */
function formatsOf(protocol: string, settingsJson: string) {
  const s = JSON.parse(settingsJson) as { transport?: string };
  const f = (format: number, ok: boolean, note = "") => ({ format, ok, note });
  if (protocol === "awg") return [f(2, true), f(1, false, "awg"), f(3, false, "awg"), f(4, false, "awg")];
  if (protocol === "vless" && s.transport === "xhttp") return [f(1, true), f(2, true), f(3, true), f(4, false, "xhttp")];
  if (protocol === "mtproto" || protocol === "trusttunnel") return [];
  return [f(1, true), f(2, true), f(3, true), f(4, true)];
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  fakeCalls(state);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  const rows = state === "s3-none" ? [] : state === "t-few" ? fleet.slice(0, 4) : fleet;
  const list = state === "loading" ? undefined : state === "noschema" ? [] : state === "t-few" ? protocols.slice(0, 2) : protocols;
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0 } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: rows.map(nodeRow), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(overviewQuery().queryKey, { nodesTotal: rows.length, nodes: rows.map((s) => ({ id: nid(s.n), name: s.n, countryCode: s.cc, location: "", status: s.up === false ? NodeStatus.DOWN : NodeStatus.ONLINE, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 23, online: 4, devicesUsed: 40, devicesOnline: 6 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, { nowUnix: now, panel: { version: "v0.1.71", built: now, update: { version: "v0.1.71", url: "", checkedUnix: now, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: now }, nodes: [] } as never);
  c.setQueryData(checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: rows.map(country) } as never);
  c.setQueryData(relaysQuery("").queryKey, (role === Role.OWNER ? [relay("msk1", "de1"), relay("msk2", "nl1")] : []) as never);
  c.setQueryData(groupsQuery.queryKey, (state === "s4-nogroup" ? [] : groupRows) as never);
  c.setQueryData(profileListQuery.queryKey, (state === "s2-name-taken" ? [...profileRows, { id: "prf_t", name: "Hysteria2 · 443", protocol: "hysteria2" }] : profileRows) as never);
  if (list) c.setQueryData(protocolsQuery.queryKey, list as never);
  else (profilesApi as unknown as Record<string, unknown>).listProtocols = never;
  c.setQueryData(settingsQuery.queryKey, { settings: { updateIntervalHours: 12, apps: [{ kind: App.HAPP, name: "Happ" }, { kind: App.HAPP, name: "Klick" }] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0 } as never);
  return c;
}

// ---- the last step as it stands at some moment
const node = (n: string, over: Partial<RunState["nodes"][number]>) => ({ id: nid(n), name: n, phase: "ok" as const, text: "", fix: {}, mend: "" as const, since: 0, ...over });
const ids = ["de1", "de2", "de3", "nl2", "fi1", "fi2"];
const runBase = (): RunState => ({ ...emptyRun(ids.map((n) => ({ id: nid(n), name: n })), ["friends"]), profile: { phase: "ok", text: "", id: "prf_new" }, groups: { phase: "ok", names: ["friends"], text: "" } });
const runs: Record<string, RunState> = {
  "s5-run": { ...runBase(), nodes: [node("de1", {}), node("de2", { phase: "apply" }), node("de3", { phase: "put" }), node("nl2", { phase: "wait" }), node("fi1", { phase: "wait" }), node("fi2", { phase: "wait" })] },
  "s5-ok": { ...runBase(), nodes: ids.map((n) => node(n, {})) },
  "s5-fail": { ...runBase(), nodes: ids.map((n) => (n === "de3" ? node(n, { phase: "bad", text: "Did not start: port udp/443 is held by another program (caddy)", mend: "port" as const, free: "8443" }) : node(n, {}))) },
  "s5-mend": { ...runBase(), nodes: ids.map((n) => (n === "nl2" ? node(n, { phase: "bad", text: "Port 443 on nl2 loses UDP packets. 2053 is free.", mend: "port" as const, free: "2053", lossy: "443", fix: { port: "2053" } }) : node(n, {}))) },
  "s5-bad": { ...emptyRun(), profile: { phase: "bad", text: "The name is taken", id: "", fields: true }, groups: { phase: "none", names: [], text: "" } },
  "s5-nonodes": { ...emptyRun([], []), profile: { phase: "ok", text: "", id: "prf_new" }, groups: { phase: "ok", names: ["friends"], text: "" } },
};
runs["s5-run"]!.nodes[1]!.since = now;

const own = (id: string): WizardDemo => ({ step: 2, task: "own", protocol: id });
/** What a scene starts the wizard with (exported for its smoke test). */
export const demoOf: Record<string, WizardDemo> = {
  "t-fast": {},
  "t-few": {},
  "t-router": { task: "router" },
  "t-own": { task: "own" },
  "s2-fast": { step: 2, task: "fast" },
  "s2-phone": { step: 2, task: "phone" },
  "s2-router": { step: 2, task: "router" },
  "s2-router-old": { step: 2, task: "router", settings: (s) => ({ ...s, version: "2.0" }) },
  "s2-cdn": { step: 2, task: "cdn", cdn: "cdn.example.org" },
  "s2-cdn-empty": { step: 2, task: "cdn" },
  "s2-tg": { step: 2, task: "tg" },
  "s2-tt": { step: 2, task: "tt" },
  "s2-own": own("hysteria2"),
  "s2-all": { step: 2, task: "fast", all: true },
  "s2-awg-all": { step: 2, task: "router", all: true },
  "s2-port-bad": { step: 2, task: "fast", port: "70000" },
  "s2-name-empty": { step: 2, task: "fast", name: "" },
  "s2-name-taken": { step: 2, task: "fast" },
  "s2-loading": { step: 2, task: "fast" },
  "s3-fast": { step: 3, task: "fast" },
  "s3-phone": { step: 3, task: "phone" },
  "s3-router": { step: 3, task: "router" },
  "s3-tt": { step: 3, task: "tt" },
  "s3-fixed": { step: 3, task: "fast", fixes: { nod_fi3: { sni: "vpn.example.com" } } },
  "s3-self": { step: 3, task: "fast", settings: (s) => ({ ...s, tls_mode: "self_signed" }) },
  "s3-pick": { step: 3, task: "fast", picked: { nod_de1: true, nod_fi1: true, nod_de2: false, nod_nl1: false, nod_nl2: false, nod_nl3: false, nod_fi2: false, nod_de3: false } },
  "s3-loading": { step: 3, task: "fast" },
  "s3-none": { step: 3, task: "fast" },
  s4: { step: 4, task: "fast" },
  "s4-two": { step: 4, task: "fast", groups: { grp_friends: true, grp_family: true } },
  "s4-none": { step: 4, task: "fast", groups: { grp_friends: false } },
  "s4-nogroup": { step: 4, task: "fast" },
};
for (const k of Object.keys(runs)) demoOf[k] = { step: 5, task: "fast", run: runs[k] };
// the nodes the profile is put on, in the scenes of the people step: the ones the run shows
for (const k of Object.keys(demoOf)) if (k.startsWith("s4")) demoOf[k] = { ...demoOf[k], picked: Object.fromEntries(fleet.map((s) => [nid(s.n), ids.includes(s.n)])) };

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function ProfileNewPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "t-fast";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    const role = params.get("role") ?? (state === "helper" ? "helper" : state === "readonly" ? "readonly" : "");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="profiles" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            <ProfileNewScreen demo={demoOf[state] ?? {}} />
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "t-fast", state: "t-fast", title: "Step 1: the tasks, the board's default chosen (1440)", w: 1440, h: 900 },
  { id: "t-fast-light", state: "t-fast", title: "The same on the light theme", w: 1440, h: 900, params: "&theme=light" },
  { id: "t-fast-1000", state: "t-fast", title: "A 1000 window: three cards in a row", w: 1000, h: 1000 },
  { id: "t-fast-phone", state: "t-fast", title: "A phone, 400 wide: one card in a row, the current step only", w: 400, h: 1400 },
  { id: "t-fast-fa", state: "t-fast", title: "Right to left (fa)", w: 1440, h: 900, params: "&lang=fa" },
  { id: "t-fast-fx", state: "t-fast", title: "With the effects on", w: 1440, h: 900, params: "&fx=1" },
  { id: "t-router", state: "t-router", title: "Step 1: AmneziaWG chosen", w: 1440, h: 900 },
  { id: "t-own", state: "t-own", title: "Step 1: \"I'll pick the protocol myself\"", w: 1440, h: 900 },
  { id: "t-few", state: "t-few", title: "A panel with two protocols: the tasks of the others are not drawn", w: 1440, h: 900 },
  { id: "s2-fast", state: "s2-fast", title: "Step 2: Hysteria2", w: 1440, h: 900 },
  { id: "s2-fast-light", state: "s2-fast", title: "Step 2, light", w: 1440, h: 900, params: "&theme=light" },
  { id: "s2-fast-phone", state: "s2-fast", title: "Step 2 on a phone", w: 400, h: 1500 },
  { id: "s2-fast-ru", state: "s2-fast", title: "Step 2 in Russian", w: 1440, h: 900, params: "&lang=ru" },
  { id: "s2-fast-fa", state: "s2-fast", title: "Step 2, right to left", w: 1440, h: 900, params: "&lang=fa" },
  { id: "s2-phone", state: "s2-phone", title: "Step 2: VLESS REALITY", w: 1440, h: 900 },
  { id: "s2-router", state: "s2-router", title: "Step 2: AmneziaWG (the question of old routers)", w: 1440, h: 900 },
  { id: "s2-router-old", state: "s2-router-old", title: "Step 2: AmneziaWG 2.0", w: 1440, h: 900 },
  { id: "s2-cdn", state: "s2-cdn", title: "Step 2: through a CDN, the host written", w: 1440, h: 900 },
  { id: "s2-cdn-empty", state: "s2-cdn-empty", title: "Step 2: through a CDN, the host missing (the bar is off)", w: 1440, h: 900 },
  { id: "s2-tg", state: "s2-tg", title: "Step 2: Telegram", w: 1440, h: 900 },
  { id: "s2-tt", state: "s2-tt", title: "Step 2: TrustTunnel", w: 1440, h: 900 },
  { id: "s2-own", state: "s2-own", title: "Step 2: the protocol picked by hand, All settings open", w: 1440, h: 1200 },
  { id: "s2-all", state: "s2-all", title: "Step 2: All settings open (the rows, then the whole form)", w: 1440, h: 1500 },
  { id: "s2-all-phone", state: "s2-all", title: "All settings on a phone", w: 400, h: 2200 },
  { id: "s2-awg-all", state: "s2-awg-all", title: "Step 2: AmneziaWG with its own cards", w: 1440, h: 1500 },
  { id: "s2-port-bad", state: "s2-port-bad", title: "Step 2: a port that is no port", w: 1440, h: 900 },
  { id: "s2-name-empty", state: "s2-name-empty", title: "Step 2: no name", w: 1440, h: 900 },
  { id: "s2-name-taken", state: "s2-name-taken", title: "Step 2: the name is taken", w: 1440, h: 900 },
  { id: "s2-loading", state: "s2-loading", title: "Step 2: the preview is not here yet", w: 1440, h: 900 },
  { id: "s3-fast", state: "s3-fast", title: "Step 3: Hysteria2 on the twelve (a domain for fi3, a port for nl1 and nl2, an old agent, an entrance)", w: 1440, h: 1200 },
  { id: "s3-fast-light", state: "s3-fast", title: "Step 3, light", w: 1440, h: 1200, params: "&theme=light" },
  { id: "s3-fast-phone", state: "s3-fast", title: "Step 3 on a phone", w: 400, h: 2000 },
  { id: "s3-fast-fa", state: "s3-fast", title: "Step 3, right to left", w: 1440, h: 1200, params: "&lang=fa" },
  { id: "s3-phone", state: "s3-phone", title: "Step 3: VLESS (the other way out is another site)", w: 1440, h: 1200 },
  { id: "s3-router", state: "s3-router", title: "Step 3: AmneziaWG (the first one on a node: its backend)", w: 1440, h: 1200 },
  { id: "s3-tt", state: "s3-tt", title: "Step 3: TrustTunnel", w: 1440, h: 1200 },
  { id: "s3-fixed", state: "s3-fixed", title: "Step 3: a domain written under fi3", w: 1440, h: 1200 },
  { id: "s3-self", state: "s3-self", title: "Step 3: the self-signed certificate taken", w: 1440, h: 1200 },
  { id: "s3-pick", state: "s3-pick", title: "Step 3: two nodes ticked by hand", w: 1440, h: 1200 },
  { id: "s3-loading", state: "s3-loading", title: "Step 3: the check has not answered", w: 1440, h: 1200 },
  { id: "s3-none", state: "s3-none", title: "Step 3: no node in the panel", w: 1440, h: 700 },
  { id: "s4", state: "s4", title: "Step 4: the groups", w: 1440, h: 800 },
  { id: "s4-light", state: "s4", title: "Step 4, light", w: 1440, h: 800, params: "&theme=light" },
  { id: "s4-phone", state: "s4", title: "Step 4 on a phone", w: 400, h: 1100 },
  { id: "s4-two", state: "s4-two", title: "Step 4: both groups ticked", w: 1440, h: 800 },
  { id: "s4-none", state: "s4-none", title: "Step 4: nobody gets it", w: 1440, h: 800 },
  { id: "s4-nogroup", state: "s4-nogroup", title: "Step 4: no group in the panel", w: 1440, h: 800 },
  { id: "s5-run", state: "s5-run", title: "Step 5: half way", w: 1440, h: 900 },
  { id: "s5-run-phone", state: "s5-run", title: "Step 5 half way, a phone", w: 400, h: 1200 },
  { id: "s5-ok", state: "s5-ok", title: "Step 5: all runs", w: 1440, h: 900 },
  { id: "s5-ok-light", state: "s5-ok", title: "Step 5, light", w: 1440, h: 900, params: "&theme=light" },
  { id: "s5-ok-fa", state: "s5-ok", title: "Step 5, right to left", w: 1440, h: 900, params: "&lang=fa" },
  { id: "s5-fail", state: "s5-fail", title: "Step 5: de3 did not start", w: 1440, h: 900 },
  { id: "s5-fail-phone", state: "s5-fail", title: "Step 5: de3 did not start, a phone", w: 400, h: 1300 },
  { id: "s5-mend", state: "s5-mend", title: "Step 5: nl2 refused: its port to try again", w: 1440, h: 900 },
  { id: "s5-bad", state: "s5-bad", title: "Step 5: the profile itself was refused", w: 1440, h: 600 },
  { id: "s5-nonodes", state: "s5-nonodes", title: "Step 5: made with no node to put it on", w: 1440, h: 600 },
  { id: "loading", state: "loading", title: "The protocols are not here yet", w: 1440, h: 400 },
  { id: "noschema", state: "noschema", title: "A panel with no protocol", w: 1440, h: 400 },
  { id: "helper", state: "helper", title: "A helper: only the owner makes a profile", w: 1440, h: 500, params: "&role=helper" },
  { id: "readonly", state: "readonly", title: "An observer", w: 1440, h: 500, params: "&role=readonly" },
];

export function PnProfileNewKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>New profile</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=profile-new-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
