import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import Palette from "@/components/palette";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { AuditSource, Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { DnsPresetSchema, DnsServerKind } from "@/gen/mistgate/admin/v1/dns_pb";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { Approvals } from "@/lib/integrations";
import { instanceQuery } from "@/lib/instance";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { ApprovalHistory, ApprovalQueue } from "@/screens/integrations/approvals";
import { SettingsLayout } from "@/screens/settings";
import { AdminsPage } from "@/screens/settings-pages/admins";
import { AuditPage } from "@/screens/settings-pages/audit";
import { BackupsPage } from "@/screens/settings-pages/backups";
import { DomainsPage } from "@/screens/settings-pages/domains";
import { InterfacePage } from "@/screens/settings-pages/interface";
import { ChangePassword, Enroll, passwordLoginQuery } from "@/screens/settings-pages/password";
import { passkeysQuery, SecurityPage } from "@/screens/settings-pages/security";
import { SessionsPage } from "@/screens/settings-pages/sessions";
import { DnsTab } from "@/screens/subscriptions/dns";
import { FormatsTab } from "@/screens/subscriptions/formats";
import type { Settings } from "@/screens/subscriptions/model";
import { clientsQuery, dnsPresetsQuery, settingsQuery, testAgentQuery } from "@/screens/subscriptions/queries";
import { RulesTab } from "@/screens/subscriptions/rules";
import { DnsSelect } from "@/screens/users/dns-select";
import { groupsQuery, protocolsQuery } from "@/screens/users/rpc";

// Development only (the /dev-kit page): the Settings group's screens on private query caches filled with demo data, so
// they render without a panel. A button that would call the panel fails here; the screenshots press only the ones
// that open something locally.

type Seed = [readonly unknown[], unknown][];
const now = Math.floor(Date.now() / 1000);

function Demo({ id, title, seed = [], children }: { id: string; title: string; seed?: Seed; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
    for (const [key, data] of seed) c.setQueryData(key, data);
    return c;
  });
  return (
    <section id={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <div className="flex flex-col gap-3.5">{children}</div>
      </QueryClientProvider>
    </section>
  );
}

const me = (role = Role.OWNER): Seed[number] => [meQuery.queryKey, { admin: { id: "adm_1", displayName: "Alice", role }, version: "0.1.0-dev", stepUpUntilUnix: 0n }];
const passkeys = (n: number): Seed[number] => [
  passkeysQuery.queryKey,
  {
    passkeys: [
      { id: "pk1", name: "iPhone", createdAtUnix: now - 86400 * 40, lastUsedAtUnix: now - 3600, backedUp: true },
      { id: "pk2", name: "YubiKey", createdAtUnix: now - 86400 * 12, lastUsedAtUnix: 0, backedUp: false },
    ].slice(0, n),
  },
];
const security = (on: boolean): Seed[number] => [["security-settings"], { turnstileEnabled: on, turnstileSiteKey: on ? "0x4AAAAAAABkMYinukE8nzYS" : "", turnstileSecretSet: on }];
const instance: Seed[number] = [
  instanceQuery.queryKey,
  { instance: { brandHead: "mist", brandTail: "gate", accent: "#b8acf2", language: "ru", hasLogo: false, logoVersion: "", adminUrl: "https://panel.example.com/admin-prefix/", subscriptionBase: "https://panel.example.com/s/" } },
];

const audit = (action: string, params: object, result = "ok", actor = "adm_1", source = AuditSource.PANEL, ago = 60) => ({
  id: `${action}-${ago}`,
  timeUnix: now - ago,
  actorId: actor,
  actorName: actor === "adm_1" ? "Alice" : "",
  action,
  paramsJson: JSON.stringify(params),
  result,
  ip: "203.0.113.7",
  source,
});
const auditPage = {
  pages: [
    {
      nextBeforeId: 0,
      entries: [
        audit("login", { method: "passkey" }, "ok", "adm_1", AuditSource.PANEL, 30),
        audit("login", { method: "password", login: "admin" }, "fail", "", AuditSource.PANEL, 90),
        audit("lockout", { source: "198.51.100.4", until: now + 900 }, "locked", "", AuditSource.PANEL, 120),
        audit("user_delete", { count: 1, names: "Марина" }, "ok", "adm_1", AuditSource.PANEL, 600),
        audit("inbound_add", { profile: "hy2 · 443", node: "de1", port: 443 }, "ok", "adm_1", AuditSource.PANEL, 900),
        audit("preset_default", { preset: "p2", name: "AdGuard: без рекламы" }, "ok", "adm_1", AuditSource.PANEL, 1200),
        audit("security_update", { turnstile_enabled: true, reason: "turnstile_secret_rejected" }, "rejected", "adm_1", AuditSource.PANEL, 1500),
        audit("password_change", { login: "alice", sessions_ended: 2 }, "ok", "adm_1", AuditSource.PANEL, 1800),
        audit("reset_login", { admin: "adm_1", login: "alice", created: false }, "ok", "cli", AuditSource.UNSPECIFIED, 4000),
      ],
    },
  ],
  pageParams: [0],
};

const subsSettings = {
  title: "",
  announcement: "",
  supportUrl: "",
  updateIntervalHours: 12,
  serverNameTemplate: "{flag} {country} · {profile}",
  rules: [
    { uaContains: "mihomo", format: SubFormat.MIHOMO_YAML },
    { uaContains: "clash", format: SubFormat.MIHOMO_YAML },
    { uaContains: "curl", format: SubFormat.DECOY },
  ],
  defaultDnsPresetId: "p1",
  apps: [],
  userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true },
} as unknown as Settings;
const presets = {
  presets: [
    create(DnsPresetSchema, { id: "p1", name: "Россия: .ru напрямую", builtin: true, isDefault: true, userCount: 12, servers: [{ kind: DnsServerKind.PLAIN, address: "1.1.1.1" }] }),
    create(DnsPresetSchema, { id: "p2", name: "AdGuard: без рекламы", builtin: true, userCount: 3, servers: [{ kind: DnsServerKind.PLAIN, address: "94.140.14.14" }] }),
  ],
  providers: [],
  clientSupport: [],
};
const subsSeed: Seed = [
  [settingsQuery.queryKey, { settings: subsSettings, effectiveTitle: "mistgate" }],
  [
    clientsQuery.queryKey,
    [
      { id: "happ", name: "Happ", protocols: ["vless", "hysteria2"], formats: [SubFormat.BASE64_URIS] },
      { id: "flclash", name: "FlClash", protocols: ["vless", "hysteria2", "awg"], formats: [SubFormat.MIHOMO_YAML, SubFormat.BASE64_URIS] },
      { id: "amnezia", name: "AmneziaVPN", protocols: ["awg"], formats: [] },
    ],
  ],
  [protocolsQuery.queryKey, [{ id: "vless", displayName: "VLESS" }, { id: "hysteria2", displayName: "Hysteria2" }, { id: "awg", displayName: "AmneziaWG" }]],
  [testAgentQuery("mihomo/1.18.9").queryKey, { ruleIndex: 0, format: SubFormat.MIHOMO_YAML, browser: false }],
  [dnsPresetsQuery.queryKey, presets],
  [groupsQuery.queryKey, [{ id: "grp_1", name: "Все", profileIds: [], userCount: 15, dnsPresetId: "" }]],
];

const fact = (key: string, value: string, code = "", params: Record<string, string> = {}, untrusted = false, untrustedParams: string[] = []) => ({ key, value, untrusted, code, params, untrustedParams });
const approval = (over: object) => ({
  id: "pln_1",
  tokenId: "tok_1",
  tokenName: "claude-ops",
  tokenProfile: TokenProfile.ADMIN,
  tool: "node_fix",
  facts: [],
  danger: [],
  reason: "",
  state: ApprovalState.AWAITING,
  createdUnix: now - 60,
  expiresUnix: now + 540,
  decidedByName: "",
  decidedUnix: 0,
  appliedUnix: 0,
  result: "",
  error: "",
  outcomeCode: "",
  outcomeParams: {},
  ...over,
});
const approvals = {
  awaiting: 2,
  nowUnix: now,
  receivedMs: Date.now(),
  approvals: [
    approval({
      facts: [
        fact("node", "de1", "", {}, true),
        fact("fix", "restart_inbound", "restart_inbound", { profile: "hy2 · 443", port: "443" }, false, ["profile"]),
        fact("detail", "inbound hy2-443 is FAILED: bind: address already in use", "", {}, true),
        fact("drops_sessions", "true"),
      ],
      danger: ["fleet"],
      reason: "the profile on de1 does not start",
    }),
    approval({
      id: "pln_2",
      tool: "user_reset_traffic",
      tokenProfile: TokenProfile.OPERATOR,
      facts: [
        fact("count", "3"),
        fact("users", "Марина, Лена, Дима", "", {}, true),
        fact("used", "412.3 GiB in all", "bytes", { bytes: "442700000000" }),
        fact("effect", "the used traffic of the current period becomes 0", "traffic_reset"),
      ],
      danger: [],
    }),
    approval({ id: "pln_3", tool: "user_disable", state: ApprovalState.APPLIED, decidedByName: "Alice", decidedUnix: now - 300, appliedUnix: now - 290, result: "3 users disabled.", outcomeCode: "users_disabled", outcomeParams: { n: "3" } }),
    approval({ id: "pln_4", tool: "alert_mute", state: ApprovalState.APPLIED, decidedByName: "Alice", decidedUnix: now - 900, appliedUnix: now - 890, result: "Alert muted.", outcomeCode: "alert_muted", outcomeParams: { seconds: "3600" } }),
    approval({ id: "pln_5", tool: "rollout_start", state: ApprovalState.FAILED, decidedByName: "Alice", decidedUnix: now - 1800, error: "failed_precondition: no trusted bundle", outcomeCode: "no_trusted_bundle" }),
  ],
} as unknown as Approvals;
const muteFacts = approval({ id: "pln_6", tool: "alert_mute", tokenProfile: TokenProfile.OPERATOR, facts: [fact("alert", "node_down, critical", "alert", { kind: "node_down", severity: "critical" }), fact("node", "fi1", "", {}, true), fact("duration", "3600 s", "seconds", { n: "3600" })] });
const createFacts = approval({
  id: "pln_7",
  tool: "user_create",
  tokenProfile: TokenProfile.OPERATOR,
  facts: [
    fact("name", "Марина", "", {}, true),
    fact("quota", "93.1 GiB, reset month", "quota", { bytes: "100000000000", reset: "month" }),
    fact("term", "never expires", "never"),
    fact("apps", "Subscription link only", "happ"),
    fact("nodes", "all nodes", "all"),
  ],
});

export function SettingsDemos() {
  const [modal, setModal] = useState<"change" | "rebind" | "add" | null>(null);
  const [palette, setPalette] = useState(false);
  const [dnsPick, setDnsPick] = useState("");
  return (
    <>
      <Demo id="demo-settings-strip" title="Settings: the strip of pages (no page is open on /dev-kit)">
        <SettingsLayout />
      </Demo>
      <Demo id="demo-settings-interface" title="Settings → Interface (owner)" seed={[me(), instance]}>
        <InterfacePage />
      </Demo>
      <Demo id="demo-settings-security" title="Settings → Security: passkeys, password and code, sign-in (check on)" seed={[me(), passkeys(2), [passwordLoginQuery.queryKey, { enabled: true, login: "alice", available: true }], security(true)]}>
        <SecurityPage />
      </Demo>
      <Demo id="demo-settings-security-passkey" title="Settings → Security: a passkey admin, the check off" seed={[me(), passkeys(1), [passwordLoginQuery.queryKey, { enabled: false, login: "", available: true }], security(false)]}>
        <SecurityPage />
      </Demo>
      <Demo id="demo-settings-password-modals" title="Password and code: the windows">
        <div className="flex flex-wrap gap-2">
          <Button onClick={() => setModal("change")}>Change password (modal)</Button>
          <Button onClick={() => setModal("rebind")}>Re-bind the app (QR + code)</Button>
          <Button onClick={() => setModal("add")}>Add password and code (form)</Button>
        </div>
        {modal === "change" && <ChangePassword login="alice" onClose={() => setModal(null)} />}
        {modal === "rebind" && <Enroll begun={{ ceremonyId: "cer_demo", uri: "otpauth://totp/mistgate:alice?secret=JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP&issuer=mistgate", secret: "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP" }} onClose={() => setModal(null)} />}
        {modal === "add" && <Enroll onClose={() => setModal(null)} />}
      </Demo>
      <Demo
        id="demo-settings-sessions"
        title="Settings → Sessions"
        seed={[
          [
            ["sessions"],
            {
              sessions: [
                { id: "s1", current: true, userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36", ip: "203.0.113.7", createdAtUnix: now - 7200, lastSeenAtUnix: now - 5 },
                { id: "s2", current: false, userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1", ip: "198.51.100.23", createdAtUnix: now - 86400 * 3, lastSeenAtUnix: now - 3600 },
                { id: "s3", current: false, userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", ip: "192.0.2.44", createdAtUnix: now - 86400 * 9, lastSeenAtUnix: now - 86400 },
              ],
            },
          ],
        ]}
      >
        <SessionsPage />
      </Demo>
      <Demo id="demo-settings-audit" title="Settings → Audit" seed={[[["audit", "all", "all"], auditPage]]}>
        <AuditPage />
      </Demo>
      <Demo id="demo-settings-admins" title="Settings → Admins" seed={[me(), passkeys(2), [passwordLoginQuery.queryKey, { enabled: true, login: "alice", available: true }]]}>
        <AdminsPage />
      </Demo>
      <Demo id="demo-settings-domains" title="Settings → Domains (owner)" seed={[me(), instance]}>
        <DomainsPage />
      </Demo>
      <Demo id="demo-settings-backups" title="Settings → Backups">
        <BackupsPage />
      </Demo>
      <Demo id="demo-subs-formats" title="Subscriptions → Apps & formats" seed={subsSeed}>
        <FormatsTab go={() => {}} />
      </Demo>
      <Demo id="demo-subs-rules" title="Subscriptions → Who gets what" seed={subsSeed}>
        <RulesTab settings={subsSettings} />
      </Demo>
      <Demo id="demo-subs-dns" title="Subscriptions → DNS, and the preset select of a user" seed={subsSeed}>
        <DnsTab />
        <div className="flex max-w-md flex-col gap-2 rounded-card-lg border border-line bg-surface p-4">
          <span className="text-[13px] font-bold">DNS</span>
          <DnsSelect value={dnsPick} onChange={setDnsPick} inherited={{ id: "p1", fromGroup: false }} />
        </div>
      </Demo>
      <Demo id="demo-int-approvals" title="Integrations → what an agent asks, and what came of it">
        <ApprovalQueue data={{ ...approvals, awaiting: 4, approvals: [...approvals.approvals.slice(0, 2), muteFacts, createFacts] as never }} />
        <ApprovalHistory data={approvals} />
      </Demo>
      <Demo id="demo-palette" title="Command palette" seed={[[nodesQuery.queryKey, { nodes: [{ id: "n1", name: "de1", countryCode: "DE", location: "Frankfurt", provider: "", address: "", status: NodeStatus.ONLINE }] }]]}>
        <Button onClick={() => setPalette(true)}>Open the palette</Button>
        <Palette open={palette} onOpenChange={setPalette} />
      </Demo>
    </>
  );
}
