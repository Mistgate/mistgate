import { Code, ConnectError } from "@connectrpc/connect";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DeviceKind, QuotaReset, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { useT } from "@/i18n";
import { profiles as profilesApi, trustTunnel } from "@/lib/api";
import { clientApiQuery } from "@/lib/clientapi";
import { errorText } from "@/lib/errors";
import { takenNote } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { DecoyNotice } from "@/screens/node/inbound-check";
import { refusalOf } from "@/screens/profiles/deploy";
import { EditProfile, ImpactModal } from "@/screens/profiles/editor";
import { ProfilesScreen } from "@/screens/profiles/list";
import { ProtocolPicker } from "@/screens/profiles/pick";
import { hy2, trusttunnel as ttSchema, vless } from "@/screens/profiles/schema.fixtures";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import type { DetailN, DeviceN, UserN } from "@/screens/users/model";
import { AccessPanel, DevicesPanel } from "@/screens/users/user-detail";
import { inbound, Mock } from "./dev-kit-node";

// Development only (/dev-kit?only=trusttunnel): TrustTunnel on made-up data, one block per screenshot (data-shot, and data-do for the
// clicks the screenshot script makes first, joined by &&: "click:<selector>", "text:<selector>|<visible text>" or "aria:<selector>|<part of its aria-label>"; the text is the Russian one). The profile page and its
// words, the choice of a protocol, the list, the nine states of a person's key, the switch on the access card, the key among
// the devices, a helper's and an observer's view, and the Cloudflare edition. Nothing is fetched: every screen reads a private cache,
// and the calls the screens make are answered here.

const NOW = Math.floor(Date.now() / 1000);
const DAY = 86_400;
const GB = 1e9;

type Fill = [readonly unknown[], unknown][];

function Shot({ id, title, children, doing, viewport, narrow }: { id: string; title: string; children: ReactNode; doing?: string; viewport?: boolean; narrow?: boolean }) {
  return (
    <section data-shot={id} data-do={doing} data-viewport={viewport ? "" : undefined} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <div className={narrow ? "max-w-md" : undefined}>{children}</div>
    </section>
  );
}

// ---- the protocols, profiles and nodes the screens read ----

const protocols = [
  { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: hy2, defaultSettingsJson: "{}", apps: [App.HAPP] },
  { id: "vless", displayName: "VLESS", settingsSchemaJson: vless, defaultSettingsJson: "{}", apps: [App.HAPP] },
  { id: "awg", displayName: "AmneziaWG", settingsSchemaJson: JSON.stringify({ type: "object", properties: {} }), defaultSettingsJson: "{}", apps: [App.AMNEZIA] },
  { id: "mtproto", displayName: "MTProto", settingsSchemaJson: JSON.stringify({ type: "object", properties: {} }), defaultSettingsJson: "{}", apps: [App.TELEGRAM] },
  { id: "trusttunnel", displayName: "TrustTunnel", settingsSchemaJson: ttSchema, defaultSettingsJson: JSON.stringify({ port: 443, h3: false, sni: "", tls_mode: "acme_domain", egress: "direct" }), apps: [App.TRUST_TUNNEL] },
];
const row = (over: object) => ({ id: "prf_tt", name: "TrustTunnel · 443", protocol: "trusttunnel", summary: "TCP 443 · HTTP/2 · Let's Encrypt", nodeCount: 2, userCount: 12, version: 3, warnings: [], formats: [], ...over });
const listRows = [
  { id: "prf_hy", name: "hy2 · 443 · Salamander", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 2, userCount: 12, version: 4, warnings: [], formats: [] },
  row({ name: "TrustTunnel · 443 · h3", summary: "TCP 443 + UDP 443 · HTTP/2 + HTTP/3 · Let's Encrypt", nodeCount: 1 }),
  row({ id: "prf_tt2", name: "TrustTunnel · 8443 · WARP", summary: "TCP 8443 · HTTP/2 · Let's Encrypt · WARP", nodeCount: 1, userCount: 3, warnings: [{ code: "inbound_failed", params: { node: "fi1" } }] }),
  { id: "prf_tg", name: "Telegram · 8443", protocol: "mtproto", summary: "TCP 8443 · Fake TLS", nodeCount: 1, userCount: 3, version: 1, warnings: [], formats: [] },
];
const nodes = [
  { id: "nod_de1", name: "de1", status: 1, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2", "trusttunnel"], awgBackend: "auto" },
  { id: "nod_nl1", name: "nl1", status: 1, countryCode: "NL", address: "nl1.example.com", protocols: ["trusttunnel"], awgBackend: "auto" },
  { id: "nod_fi1", name: "fi1", status: 1, countryCode: "FI", address: "203.0.113.10", protocols: ["trusttunnel"], awgBackend: "auto" },
];
const ttSettings = (over: object = {}) => JSON.stringify({ port: 443, h3: true, sni: "vpn.example.com", tls_mode: "acme_domain", egress: "direct", ...over });
const detail = (settings: string, profile: object = listRows[1]!) => ({ profile, settingsJson: settings, inbounds: [inbound({ id: "inb_tt", profileId: "prf_tt", profileName: "TrustTunnel · 443 · h3", protocol: "trusttunnel", port: 443, tlsServerName: "vpn.example.com" })] });

const as = (role: Role, extra: Fill = [], me: object = {}): Fill => [
  [meQuery.queryKey, { admin: { id: "adm_1", role }, edition: "vps", unavailable: [], ...me }],
  [protocolsQuery.queryKey, protocols],
  [profileListQuery.queryKey, listRows],
  [groupsQuery.queryKey, [{ id: "grp_f", name: "friends", profileIds: ["prf_hy", "prf_tt"], userCount: 5, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 }]],
  [nodesQuery.queryKey, { nodes }],
  [["subs", "settings"], { settings: { updateIntervalHours: 12, apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }],
  ...extra,
];

/** Dev only: what the panel would answer to the preview of a TrustTunnel form, by what the form holds. */
function fakeProfileCalls() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.previewProfile = async (r: { settingsJson: string }) => {
    const s = JSON.parse(r.settingsJson) as { port: number; sni: string; tls_mode: string; h3: boolean };
    const errors = [] as { pointer: string; code: string; message: string }[];
    if (s.sni.includes("!")) errors.push({ pointer: "/sni", code: "invalid", message: "must be a host name (not an IP address) for a Let's Encrypt certificate" });
    if (s.port > 65535 || s.port < 1) errors.push({ pointer: "/port", code: "out_of_range", message: "port must be 1-65535" });
    const pin = s.tls_mode === "self_signed";
    const path = `{"server":"de1.example.com","port":${s.port},"sni":"${s.sni || "de1.example.com"}","username":"••••","password":"••••","transport":"${s.h3 ? "auto" : "h2"}","tls":"${pin ? "pin" : "ca"}"${pin ? ',"pin_sha256":"••••"' : ""}}`;
    return { errors, warnings: [], clientPreview: path, clientLabel: "Mistgate app · path settings", formats: [], summary: "", obfuscationScore: 0 };
  };
  p.listProfiles = async () => ({ profiles: listRows });
  p.listProtocols = async () => ({ protocols });
}

function Profile({ id, settings, role = Role.OWNER }: { id: string; settings: string; role?: Role }) {
  useState(fakeProfileCalls);
  return (
    <Mock fill={as(role, [[["profiles", "detail", id], detail(settings)]])}>
      <EditProfile id={id} />
    </Mock>
  );
}

function PickDemo() {
  const t = useTx();
  const [value, setValue] = useState("trusttunnel");
  return (
    <Mock fill={as(Role.OWNER)}>
      <ProtocolPicker protocols={protocols} value={value} onValue={setValue} onContinue={() => {}} onBack={() => {}} drafts={new Set()} t={t} />
    </Mock>
  );
}

function ListDemo({ role }: { role: Role }) {
  useState(fakeProfileCalls);
  return (
    <Mock fill={as(role)}>
      <ProfilesScreen />
    </Mock>
  );
}

/** What the panel says about the ports of a TrustTunnel profile: the Hysteria2 site gives way, TCP/UDP taken, the other refusals. */
function Notes() {
  const t = useTx();
  const refused = (msg: string, protocol = "trusttunnel") => refusalOf(new ConnectError(msg, Code.AlreadyExists), t, { name: "de1", address: "de1.example.com" }, protocol).text;
  const taken = { node: "de1", port: "443", hy2: "hy2 · 443 · Salamander", trusttunnel: "TrustTunnel · 443" };
  return (
    <div className="flex max-w-3xl flex-col gap-3">
      <DecoyNotice check={{ state: "ok", inbound: undefined, warnings: [{ code: "hy2_site_taken", params: taken }], freePort: 0 } as never} />
      <Notice tone="warn">{refused("port_taken: port=443&profile=hy2 · 443 · Salamander&free=8443")}</Notice>
      <Notice tone="warn">{refused("port_taken: port=443&profile=hy2 · 443 · Salamander")}</Notice>
      <Notice tone="warn">{refused("acme_needs_domain: address=203.0.113.10")}</Notice>
      <span className="hidden">{takenNote(t, taken)}</span>
    </div>
  );
}

function ImpactDemo() {
  const t = useTx();
  const [open, setOpen] = useState(false);
  const impact = { impact: { inboundsRestarted: 2, usersOnline: 1, devicesNeedReissue: 0, criticalFields: ["/port"], affectedUserNames: ["anna", "boris", "chen"] }, fields: ["TCP port"] } as never;
  return (
    <Mock fill={as(Role.OWNER)}>
      <Button onClick={() => setOpen(true)}>Save a new TCP port of the profile</Button>
      <ImpactModal impact={impact} userCount={12} protocol={protocols[4] as never} open={open} onClose={() => setOpen(false)} onConfirm={async () => setOpen(false)} t={t} />
    </Mock>
  );
}

// ---- a person ----

const alice = (over: object = {}): UserN =>
  ({
    id: "usr_alice", name: "alice", subscriptionName: "", groupId: "grp_f", groupName: "friends", status: UserStatus.ACTIVE, apps: { happ: false, amnezia: false, telegram: false, mistgate: true, trustTunnel: true }, via: [],
    devicesUsed: 3, deviceLimit: 5, devicesIdle: 0, usedBytes: 37.4 * GB, quotaBytes: 100 * GB, quotaReset: QuotaReset.MONTH, nextResetUnix: NOW + 9 * DAY, expiresUnix: NOW + 24 * DAY,
    lastSeenUnix: NOW - 300, speedLimitBps: 0, deviceSpeedLimitBps: 0, createdUnix: NOW - 60 * DAY, nodes: { all: true, nodeIds: [] }, dnsPresetId: "", ...over,
  }) as unknown as UserN;
const device = (id: string, over: Partial<DeviceN>): DeviceN =>
  ({
    id, kind: DeviceKind.LINK, model: "", platform: "ios", client: "", osVersion: "", awgProfileId: "", awgProfileName: "", awgVersion: "", address: "", stale: false, online: false, onlineNodeIds: [], protocols: [],
    firstSeenUnix: NOW - 30 * DAY, lastSeenUnix: NOW - 60, lastHandshakeUnix: 0, usedBytes: 4 * GB, fingerprint: "", enrolled: false, enrolLinkStale: false, idle: false, idleSinceUnix: 0, ...over,
  }) as unknown as DeviceN;
const ttKey = device("dev_tt", { kind: DeviceKind.KEY, model: "TrustTunnel key", platform: "other", protocols: ["trusttunnel"], firstSeenUnix: NOW - 3 * DAY });
const awgKey = device("dev_awg", { kind: DeviceKind.KEY, model: "Pixel 7", platform: "android", awgProfileId: "p_awg", awgProfileName: "AWG 3.1", lastHandshakeUnix: NOW - 3600, online: true });
const iphone = device("dev_ip", { model: "iPhone 15", client: "Happ/3.4.2", osVersion: "18.1", online: true, onlineNodeIds: ["nod_de1"] });

// a made-up key's links: about the length of a real tt:// link, so the QR code has the density a person will see
const url = (n: string) => `tt://?AAEBAQ9kZTEuZXhhbXBsZS5jb20FEHVhbGljZTIzNDU2NzIzNDUGIGFCMjM0NTY3Y0QyMzQ1NjdlRjIzNDU2N2dIMjM0NTY3AhNkZTEuZXhhbXBsZS5jb206NDQzBAEACQEBDA${n}DRYKMTkyLjAuMi41MwoxOTIuMC4yLjU0`;
const link = (over: object = {}) => ({ nodeId: "nod_de1", nodeName: "de1", profileId: "prf_tt", profileName: "TrustTunnel · 443 · h3", url: url("DZGUx"), hasIpv6: false, relay: false, via: "", note: "", ...over });
const links = [
  link(),
  link({ hasIpv6: true, nodeId: "nod_nl1", nodeName: "nl1", url: url("DbmwxA") }),
  link({ relay: true, via: "RU", url: url("DZGUy") }),
  link({ nodeId: "nod_fi1", nodeName: "fi1", profileId: "prf_tt2", profileName: "TrustTunnel · 8443 · WARP", url: "", note: "self_signed" }),
];
const withKey = (over: object = {}) => ({ hasKey: true, keyDeviceId: "dev_tt", keyIssuedUnix: BigInt(NOW - 3 * DAY), links, ...over });
const noKey = { hasKey: false, keyDeviceId: "", keyIssuedUnix: 0n, links: [] };

const country = (id: string) => ({ nod_de1: "DE", nod_nl1: "NL", nod_fi1: "FI" })[id] ?? "";

/** The person's devices card with only the TrustTunnel block (and the key among the devices), as the page draws it. */
function Person({ user, keys, devices = [ttKey], role = Role.OWNER, me = {}, hasProfile = true }: { user: UserN; keys: object | null; devices?: DeviceN[]; role?: Role; me?: object; hasProfile?: boolean }) {
  const fill: Fill = [
    ...as(role, [], me),
    ...(keys ? ([[["users", "trusttunnel", user.id, !!user.apps?.trustTunnel], keys]] as Fill) : []),
    [clientApiQuery.queryKey, { enabled: true, stats: { people: 24, peopleOn: 17, devices: 31 } }],
    [dnsPresetsQuery.queryKey, { presets: [], providers: [], clientSupport: [] }],
    [settingsQuery.queryKey, { settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0 }],
  ];
  return (
    <Mock fill={fill}>
      <DevicesPanel
        user={user}
        devices={devices}
        profiles={(hasProfile ? [{ id: "prf_tt", name: "TrustTunnel · 443 · h3", protocol: "trusttunnel" }] : []) as never}
        nodeName={(id) => id.replace("nod_", "")}
        nodeCountry={country}
        actions={{ refresh: () => Promise.resolve(), revoke: () => Promise.resolve() } as never}
        onEditGroup={() => {}}
      />
    </Mock>
  );
}

/** The access card with its switches; `update` is what a switch asks of the panel. */
function Access({ user, update, devices = [], role = Role.OWNER, me = {} }: { user: UserN; update?: (patch: unknown) => Promise<void>; devices?: DeviceN[]; role?: Role; me?: object }) {
  const accessNodes = [
    { nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", location: "Frankfurt", provider: "Hetzner", protocols: ["hysteria2", "trusttunnel"], nodeProtocols: ["hysteria2", "trusttunnel"], selected: true },
    { nodeId: "nod_nl1", nodeName: "nl1", countryCode: "NL", location: "Amsterdam", provider: "Northwind", protocols: ["trusttunnel"], nodeProtocols: ["trusttunnel"], selected: true },
  ];
  const data = { user, devices, blocked: [], dailyTraffic: [], nodeTraffic: [], profiles: [{ id: "prf_hy", name: "hy2 · 443", protocol: "hysteria2" }, { id: "prf_tt", name: "TrustTunnel · 443 · h3", protocol: "trusttunnel" }], nodeAccess: accessNodes } as unknown as DetailN;
  const fill: Fill = [...as(role, [], me), [clientApiQuery.queryKey, { enabled: true, stats: { people: 24, peopleOn: 17, devices: 31 } }], [dnsPresetsQuery.queryKey, { presets: [], providers: [], clientSupport: [] }]];
  return (
    <Mock fill={fill}>
      <AccessWithToasts data={data} update={update} />
    </Mock>
  );
}

function AccessWithToasts({ data, update }: { data: DetailN; update?: (patch: unknown) => Promise<void> }) {
  const toast = useToast();
  const t = useT();
  const actions = {
    // as the card's own: the panel's refusal in words, in a toast, and the switch keeps its place
    update: async (patch: unknown) => {
      try {
        await (update ?? (() => new Promise<void>((r) => setTimeout(r, 200))))(patch);
      } catch (e) {
        toast.error(errorText(e, t));
        throw e;
      }
    },
  };
  return <AccessPanel data={data} actions={actions as never} />;
}

function fakeKeyCalls() {
  const k = trustTunnel as unknown as Record<string, unknown>;
  k.getTrustTunnelLinks = async () => noKey;
  k.issueTrustTunnelKey = async () => {
    await new Promise((r) => setTimeout(r, 150));
    throw new ConnectError("device_limit: 5/5: iPhone 15, Pixel 7, MacBook, Work PC, Router", Code.FailedPrecondition);
  };
  k.revokeTrustTunnelKey = async () => ({ trustTunnel: noKey });
}

const limit = () => {
  throw new ConnectError("device_limit: 5/5: iPhone 15, Pixel 7, MacBook, Work PC, Router", Code.FailedPrecondition);
};

export function TrustTunnelKit() {
  useState(fakeKeyCalls);
  const paused = alice({ status: UserStatus.DISABLED });
  const expired = alice({ status: UserStatus.EXPIRED, name: "boris" });
  const off = alice({ apps: { happ: false, amnezia: false, telegram: false, mistgate: true, trustTunnel: false } });
  const full = alice({ devicesUsed: 5 });
  return (
    <div className="flex flex-col gap-10">
      <Shot id="profile" title="TrustTunnel profile (owner): the form, its summary, where it runs, and what the Mistgate app and the official apps get">
        <Profile id="prf_tt" settings={ttSettings()} />
      </Shot>
      <Shot id="profile-self" title="TrustTunnel profile with a self-signed certificate: the official apps get no link, the Mistgate app keeps its path (the pin)">
        <Profile id="prf_tt" settings={ttSettings({ tls_mode: "self_signed", h3: false, sni: "" })} />
      </Shot>
      <Shot id="profile-problems" title="TrustTunnel profile with a refused field (the name is not a host name; the port is out of range) in the words of the panel">
        <Profile id="prf_tt" settings={ttSettings({ sni: "vpn!example.com", port: 70000 })} />
      </Shot>
      <Shot id="profile-notes" title="What the panel says about the ports: the Hysteria2 site gives way (hy2_site_taken), the port is taken (TCP and UDP of HTTP/3), a domain is needed">
        <Notes />
      </Shot>
      <Shot id="profile-impact" title="A critical change of a TrustTunnel profile: the tt:// link stops working until people add the new one" viewport doing="click:button">
        <ImpactDemo />
      </Shot>
      <Shot id="pick" title="New profile, step 1: TrustTunnel among the protocols, marked experimental, with its apps">
        <PickDemo />
      </Shot>
      <Shot id="list" title="The profiles list with two TrustTunnel profiles (HTTP/3 on, WARP exit with a failed node)">
        <ListDemo role={Role.OWNER} />
      </Shot>
      <Shot id="helper-profile" title="A helper reads the profile (no Save, no delete: only the owner changes profiles)">
        <Profile id="prf_tt" settings={ttSettings()} role={Role.HELPER} />
      </Shot>

      <Shot id="key-off" title="The person's key, 1: the switch is off" narrow>
        <Person user={off} keys={null} devices={[]} />
      </Shot>
      <Shot id="key-issue" title="The person's key, 2: on, no key, an active person: Issue key" narrow>
        <Person user={alice()} keys={noKey} devices={[]} />
      </Shot>
      <Shot id="key-issue-blocked" title="The person's key, 2: on, no key, the person is disabled: no button that would be refused" narrow>
        <Person user={paused} keys={noKey} devices={[]} />
      </Shot>
      <Shot id="key-paused" title="The person's key, 3: a key, the person is disabled: on hold, no links" narrow>
        <Person user={paused} keys={withKey({ links: [] })} />
      </Shot>
      <Shot id="key-paused-expired" title="The person's key, 3: a key, the person's term has ended: on hold, no links" narrow>
        <Person user={expired} keys={withKey({ links: [] })} />
      </Shot>
      <Shot id="key-empty" title="The person's key, 4: a key and no server for it: said, with the way to the profiles" narrow>
        <Person user={alice()} keys={withKey({ links: [] })} />
      </Shot>
      <Shot id="key-links" title="The person's key, 5 to 7: a link per server; IPv6 on the exit; through a relay (via RU); a self-signed server with no link and the way to its profile" narrow>
        <Person user={alice()} keys={withKey()} />
      </Shot>
      <Shot id="key-qr" title="The person's key: the QR code of a link (a real tt:// link is about 240 characters) and Copy" narrow doing={"aria:button|QR"}>
        <Person user={alice()} keys={withKey({ links: [links[0]] })} />
      </Shot>
      <Shot id="key-limit" title="The person's key, 8: Issue key at the device limit; the refusal stays in the block" narrow doing={"click:[data-testid=tt-section] button"}>
        <Person user={full} keys={noKey} devices={[]} />
      </Shot>
      <Shot id="key-revoke" title="The person's key, 9: Revoke key asks first" viewport doing={"aria:[data-testid=tt-section] button|TrustTunnel&&text:[role=menuitem]|Отозвать"}>
        <Person user={alice()} keys={withKey({ links: [links[0]] })} />
      </Shot>
      <Shot id="access" title="The access card: the TrustTunnel switch, on" narrow>
        <Access user={alice({ apps: { happ: true, amnezia: false, telegram: false, mistgate: true, trustTunnel: true } })} devices={[ttKey]} />
      </Shot>
      <Shot id="access-limit" title="The access card: the switch at the device limit; nothing is saved and the refusal is shown" viewport narrow doing={"text:label|TrustTunnel"}>
        <Access user={alice({ apps: { happ: true, amnezia: false, telegram: false, mistgate: true, trustTunnel: false }, devicesUsed: 5 })} update={async () => limit()} />
      </Shot>
      <Shot id="access-off" title="The access card: turning TrustTunnel off asks first, the key is revoked" viewport narrow doing={"text:label|TrustTunnel"}>
        <Access user={alice({ apps: { happ: true, amnezia: false, telegram: false, mistgate: true, trustTunnel: true } })} devices={[ttKey]} />
      </Shot>
      <Shot id="devices" title="The devices of a person with an AmneziaVPN key and a TrustTunnel key: told apart, the second in its own block with only Revoke key" narrow>
        <Person user={alice({ apps: { happ: true, amnezia: true, telegram: false, mistgate: true, trustTunnel: true } })} keys={withKey({ links: [links[0]] })} devices={[iphone, awgKey, ttKey]} />
      </Shot>
      <Shot id="observer" title="A read-only admin: no call is made, no button; the block says who sees the key and links" narrow>
        <Person user={alice()} keys={null} role={Role.READONLY} />
      </Shot>
      <Shot id="edge" title="The Cloudflare edition: TrustTunnel is not offered, said in the block and at the switch" narrow>
        <div className="flex flex-col gap-4">
          <Person user={alice()} keys={null} devices={[]} me={{ edition: "edge", unavailable: ["backups", "mcp", "trusttunnel"] }} />
          <Access user={alice({ apps: { happ: true, amnezia: false, telegram: false, mistgate: true, trustTunnel: false } })} me={{ edition: "edge", unavailable: ["backups", "mcp", "trusttunnel"] }} />
        </div>
      </Shot>
    </div>
  );
}
