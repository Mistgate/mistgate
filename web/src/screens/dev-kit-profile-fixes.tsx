import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import { profiles as profilesApi } from "@/lib/api";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { baseNode, inbound, Mock, nodeFill } from "@/screens/dev-kit-node";
import { ProfilesTab } from "@/screens/node/profiles";
import { EditProfile, ImpactModal } from "@/screens/profiles/editor";
import { ProfilesScreen } from "@/screens/profiles/list";
import { parseSchema, parseSettings } from "@/screens/profiles/schema";
import { hy2, vless } from "@/screens/profiles/schema.fixtures";
import { SchemaForm, type Problem } from "@/screens/profiles/schema-form";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";

// Development only, part of /dev-kit ("Profile fixes"): the behaviour fixes of the profile pages on made-up data — the
// critical-change window for a Telegram link, the profile list and page for a helper and an observer (read-only), the words of
// a refused field by protocol (AmneziaWG, and Hysteria2 against VLESS), and the node's Profiles tab with MTProto's tcp port
// and VLESS's domain in the edit window. Nothing is fetched: every screen reads a private query cache.

const now = Math.floor(Date.now() / 1000);

const awgSchema = JSON.stringify({
  type: "object",
  properties: {
    version: { type: "string", enum: ["3.1", "2.0"], title: "Protocol version", "x-group": "basics", "x-order": 10 },
    mtu: { type: "integer", title: "MTU", "x-group": "basics", "x-order": 30 },
    subnet4: { type: "string", title: "Client network (IPv4)", "x-group": "basics", "x-order": 50 },
    obfuscation: {
      type: "object",
      "x-group": "obfuscation",
      properties: {
        s2: { type: "integer", title: "Response padding (S2)", "x-order": 20 },
        h3: { type: "string", title: "Cookie header (H3)", "x-order": 40 },
        i1: { type: "string", title: "Signature packet 1 (I1)", "x-order": 50 },
        rekey_timeout: { type: "string", title: "Rekey timeout", "x-order": 60 },
        signature_seed: { type: "string", title: "Signature seed", "x-order": 70 },
      },
    },
  },
});

const protocols = [
  { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: hy2, defaultSettingsJson: "{}", apps: [App.HAPP] },
  { id: "mtproto", displayName: "MTProto", settingsSchemaJson: JSON.stringify({ type: "object", properties: {} }), defaultSettingsJson: "{}", apps: [App.TELEGRAM] },
  { id: "vless", displayName: "VLESS", settingsSchemaJson: vless, defaultSettingsJson: "{}", apps: [App.HAPP] },
];
const listRows = [
  { id: "prf_hy", name: "hy2 · 443 · Salamander", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 2, userCount: 12, version: 4, warnings: [], formats: [] },
  { id: "prf_tg", name: "Telegram · 8443", protocol: "mtproto", summary: "TCP 8443 · Fake TLS · www.example.org", nodeCount: 1, userCount: 3, version: 1, warnings: [], formats: [] },
];
const hySettings = JSON.stringify({ port: 443, tls_mode: "acme_domain", obfs: { type: "salamander", password: "••••" }, masquerade: { type: "decoy" }, up_mbps: 0, udp: true });
const kitNodes = [{ id: "nod_de1", name: "de1", status: 1, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2"], awgBackend: "auto" }];

/** The role the demo's screens see, on top of the cache `Mock` makes. */
const as = (role: Role): [readonly unknown[], unknown][] => [
  [meQuery.queryKey, { admin: { id: "adm_1", role } }],
  [protocolsQuery.queryKey, protocols],
  [profileListQuery.queryKey, listRows],
  [groupsQuery.queryKey, [{ id: "grp_all", name: "Everyone", profileIds: ["prf_hy"], userCount: 14, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 }]],
  [nodesQuery.queryKey, { nodes: kitNodes }],
  [["profiles", "detail", "prf_hy"], { profile: listRows[0], settingsJson: hySettings, inbounds: [inbound({})] }],
  [["subs", "settings"], { settings: { updateIntervalHours: 12, apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "" }],
];

function Demo({ title, shot, children }: { title: string; shot?: string; children: ReactNode }) {
  return (
    <section data-shot={shot} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <div className="rounded-card-lg border border-dashed border-line p-4">{children}</div>
    </section>
  );
}

function CritDemo() {
  const t = useTx();
  const [open, setOpen] = useState<"hy2" | "tg" | null>(null);
  const impact = { impact: { inboundsRestarted: 2, usersOnline: 1, devicesNeedReissue: 0, criticalFields: ["/port"], affectedUserNames: ["anna", "boris", "chen"] }, fields: ["Port"] } as never;
  const close = () => setOpen(null);
  return (
    <Mock fill={as(Role.OWNER)}>
      <div className="flex flex-wrap gap-2">
        <Button onClick={() => setOpen("hy2")}>Save a Hysteria2 port (subscription)</Button>
        <Button onClick={() => setOpen("tg")}>Save an MTProto port (Telegram link)</Button>
      </div>
      <ImpactModal impact={impact} userCount={12} protocol={protocols[0] as never} open={open === "hy2"} onClose={close} onConfirm={async () => close()} t={t} />
      <ImpactModal impact={impact} userCount={3} protocol={protocols[1] as never} open={open === "tg"} onClose={close} onConfirm={async () => close()} t={t} />
    </Mock>
  );
}

/** Dev only: what the panel would answer, so a refetch (the list polls) finds the same rows. */
function fakePreview() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.previewProfile = async () => ({ errors: [], warnings: [], clientPreview: "hysteria2://••••@de1.example.com:443/?obfs=salamander#hy2 · 443", clientLabel: "Subscription link · URI list", formats: [] });
  p.listProfiles = async () => ({ profiles: listRows });
  p.listProtocols = async () => ({ protocols });
  p.getProfile = async () => ({ profile: listRows[0], settingsJson: hySettings, inbounds: [inbound({})] });
}

function RoleDemo({ role, what }: { role: Role; what: "list" | "page" }) {
  useState(fakePreview);
  return <Mock fill={as(role)}>{what === "list" ? <ProfilesScreen /> : <EditProfile id="prf_hy" />}</Mock>;
}

const problems = (list: [string, string][]): Problem[] => list.map(([pointer, code]) => ({ pointer, code, message: "the server's English phrase" }));

function WordsDemo() {
  const awg = parseSettings(JSON.stringify({ version: "2.0", mtu: 1500, subnet4: "10.66.4.1/22", obfuscation: { s2: 28, h3: "5-9", i1: "<b 0x00", rekey_timeout: "5-15", signature_seed: "" } }));
  const hy = parseSettings(JSON.stringify({ port: 443, tls_mode: "acme_ip" }));
  return (
    <div className="grid gap-4 md:grid-cols-2">
      <SchemaForm
        protocol="awg"
        groups={parseSchema(awgSchema)}
        settings={awg}
        base={awg}
        onChange={() => {}}
        problems={problems([
          ["/mtu", "out_of_range"],
          ["/subnet4", "invalid"],
          ["/obfuscation/s2", "duplicate_size"],
          ["/obfuscation/h3", "overlap"],
          ["/obfuscation/i1", "invalid"],
          ["/obfuscation/rekey_timeout", "not_in_version"],
          ["/obfuscation/signature_seed", "required"],
        ])}
      />
      <SchemaForm protocol="hysteria2" groups={parseSchema(hy2)} settings={hy} base={hy} onChange={() => {}} problems={problems([["/tls_mode", "unsupported"]])} />
    </div>
  );
}

const tab = (over: object[]) => ({ node: baseNode({}), inbounds: over, portChecks: [{ port: 8443, verdict: "lossy", sent: 300, got: 189, checkedUnix: now - 60, sender: "panel" }], tcpChecks: [] }) as never;

export function ProfileFixesKit() {
  const failedTg = inbound({ id: "inb_tg", profileId: "prf_tg", profileName: "Telegram · 8443", protocol: "mtproto", port: 8443, tlsServerName: "", state: InboundState.FAILED, certNotAfterUnix: 0, lastError: "listen tcp :8443: bind: address already in use" });
  const vlessOk = inbound({ id: "inb_vl", profileId: "prf_vl", profileName: "VLESS · 443", protocol: "vless", port: 443, tlsServerName: "de1.example.com" });
  const vlessSite = inbound({ id: "inb_vs", profileId: "prf_vs", profileName: "VLESS · another site", protocol: "vless", port: 2053, tlsServerName: "" });
  const fill = [
    ...nodeFill("nod_de1", { doctor: [{ id: "port_conflicts", status: 3, titleKey: "doctor.port_conflicts.title", detail: "", detailCode: "", params: { inbound_id: "inb_tg", port: "8443", network: "tcp", process: "caddy(812)" }, whyKey: "health.doctor.port_conflicts.why", fixId: "", measuredUnix: now }] }),
    [["inbound-check", { kind: "update", inboundId: "inb_vl" }], { of: "inb_vl", state: "ok", inbound: { port: 443, tlsServerName: "de1.example.com" }, warnings: [], freePort: 0 }],
    [["inbound-check", { kind: "update", inboundId: "inb_tg" }], { of: "inb_tg", state: "ok", inbound: { port: 8443, tlsServerName: "" }, warnings: [], freePort: 4443 }],
  ] as [readonly unknown[], unknown][];
  return (
    <div id="profile-fixes" className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-6">
      <Demo shot="profile-fixes-crit" title="Profile fixes: the window before a critical change — Hysteria2 waits for a subscription refresh, a Telegram link is sent anew">
        <CritDemo />
      </Demo>
      <Demo shot="profile-fixes-list-helper" title="Profile fixes: the list for a helper — no “New profile”; the rows are readable">
        <RoleDemo role={Role.HELPER} what="list" />
      </Demo>
      <Demo shot="profile-fixes-page-observer" title="Profile fixes: the profile page for an observer — a view with one quiet line, no save bar, no delete section">
        <RoleDemo role={Role.READONLY} what="page" />
      </Demo>
      <Demo shot="profile-fixes-words" title="Profile fixes: a refused field in words by protocol — AmneziaWG (left), Hysteria2's certificate on an IP (right)">
        <WordsDemo />
      </Demo>
      <Demo shot="profile-fixes-node-tab" title="Profile fixes: the node's Profiles tab — MTProto is tcp/8443 and its UDP check is not shown, the failed row says tcp/8443; “Edit” on the VLESS rows: the domain is there for the node's own site, not for another site">
        <Mock fill={[...as(Role.OWNER), ...fill]}>
          <ProfilesTab data={tab([inbound({}), failedTg, vlessOk, vlessSite])} />
        </Mock>
      </Demo>
      <Demo shot="profile-fixes-node-tab-helper" title="Profile fixes: the node's Profiles tab for a helper — rows readable, only “Restart” (the helper's), one quiet line instead of the owner's buttons">
        <Mock fill={[...as(Role.HELPER), ...fill]}>
          <ProfilesTab data={tab([inbound({}), failedTg, vlessOk])} />
        </Mock>
      </Demo>
    </div>
  );
}
