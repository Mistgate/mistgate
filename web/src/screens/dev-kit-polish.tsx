import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { AddNodeProvider } from "@/components/add-node";
import { DangerZone, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { ApprovalState, TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { BundleStatus, NodeUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { approvalsQuery, tokensQuery } from "@/lib/integrations";
import { meQuery } from "@/lib/session";
import { IntegrationsScreen } from "@/screens/integrations";
import { TokensCard } from "@/screens/integrations/tokens";
import { NodesScreen } from "@/screens/nodes";
import { AuditPage } from "@/screens/settings-pages/audit";
import { passkeysQuery, SecurityPage } from "@/screens/settings-pages/security";
import { SessionsPage } from "@/screens/settings-pages/sessions";
import { UpdateToast } from "@/components/update-toast";
import { updatesQuery } from "@/lib/updates";
import { UpdatesScreen } from "@/screens/updates";
import { UsersScreen } from "@/screens/users/list";
import { groupsQuery } from "@/screens/users/rpc";
import { SettingRow } from "@/screens/users/ui";

// Development only (the /dev-kit page): the shared states (loading, errors, empty lists) as every screen shows them, on private query caches.
// A query that is not filled goes to a panel that is not there, so it fails: that is the load-error state.

type Seed = [readonly unknown[], unknown][];
const now = Math.floor(Date.now() / 1000);

function Shot({ id, title, seed = [], children }: { id: string; title: string; seed?: Seed; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    for (const [key, data] of seed) c.setQueryData(key, data);
    return c;
  });
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>
        <AddNodeProvider>
          <div className="flex flex-col gap-3.5">{children}</div>
        </AddNodeProvider>
      </QueryClientProvider>
    </section>
  );
}

const owner: Seed[number] = [meQuery.queryKey, { admin: { id: "adm_1", displayName: "Alice", role: Role.OWNER }, version: "0.1.0-dev", stepUpUntilUnix: 0n }];
const token = (over: object) => ({
  id: "tok_1",
  name: "claude-ops",
  profile: TokenProfile.OPERATOR,
  createdUnix: now - 86400 * 3,
  expiresUnix: now + 86400 * 60,
  lastUsedUnix: now - 180,
  lastUsedIp: "203.0.113.7",
  lastUsedVia: TokenChannel.MCP,
  revokedUnix: 0,
  createdByName: "Alice",
  rateLimitPerMin: 120,
  hint: "ab12",
  ...over,
});
const tokens = {
  nowUnix: now,
  tokens: [
    token({}),
    token({ id: "tok_2", name: "nightly-backup", profile: TokenProfile.READONLY, lastUsedUnix: now - 86400 * 2 - 600, lastUsedIp: "", lastUsedVia: TokenChannel.API, hint: "zz99" }),
    token({ id: "tok_3", name: "old-script", revokedUnix: now - 86400 * 9 }),
  ],
};
const decided = (over: object) => ({
  id: "pln_1",
  tokenId: "tok_1",
  tokenName: "claude-ops",
  tokenProfile: TokenProfile.ADMIN,
  tool: "user_disable",
  facts: [],
  danger: [],
  reason: "",
  state: ApprovalState.APPLIED,
  createdUnix: now - 400,
  expiresUnix: now + 200,
  decidedByName: "Alice",
  decidedUnix: now - 300,
  appliedUnix: now - 290,
  result: "3 users disabled.",
  error: "",
  outcomeCode: "users_disabled",
  outcomeParams: { n: "3" },
  ...over,
});
const approvals = {
  awaiting: 0,
  nowUnix: now,
  receivedMs: Date.now(),
  approvals: [
    decided({}),
    decided({ id: "pln_2", tool: "alert_mute", decidedUnix: now - 3 * 3600, appliedUnix: now - 3 * 3600 + 5, result: "Alert muted.", outcomeCode: "alert_muted", outcomeParams: { seconds: "3600" } }),
    decided({ id: "pln_3", tool: "rollout_start", state: ApprovalState.REJECTED, decidedUnix: now - 86400 * 2, appliedUnix: 0, result: "", outcomeCode: "", outcomeParams: {} }),
  ],
};

/** The user card's and the profile's danger zones, with their own rows and buttons (the screens need a route to render whole). */
function DangerZones() {
  const t = useT();
  return (
    <div className="grid items-start gap-3.5 md:grid-cols-2">
      <DangerZone title={t("users.danger")}>
        <SettingRow label={t("users.resetTrafficT")} hint={t("users.resetTrafficHint")}>
          <Button variant="secondary">{t("users.resetTraffic")}</Button>
        </SettingRow>
        <SettingRow label={t("users.deleteT")} hint={t("users.deleteHint")}>
          <Button variant="danger">{t("users.delete")}</Button>
        </SettingRow>
      </DangerZone>
      <DangerZone title={t("profiles.danger")}>
        <div className="flex items-center gap-3">
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <span className="text-[13px] font-bold">{t("profiles.delete")}</span>
            <span className="text-[11px] leading-snug text-muted">{t("profiles.deleteHint")}</span>
          </div>
          <Button variant="danger">{t("users.delete")}</Button>
        </div>
      </DangerZone>
    </div>
  );
}

// The people list on mock rows: who is online, what each used (link, keys, both, nothing), four groups with their chips.
const listGroups = [
  { id: "grp_7kq2m4xw3vbf", name: "SIMG", profileIds: [], userCount: 3, dnsPresetId: "", happNodes: 2, amneziaNodes: 1 },
  { id: "grp_c3ndr5e2p6ha", name: "family", profileIds: [], userCount: 2, dnsPresetId: "", happNodes: 2, amneziaNodes: 0 },
  { id: "grp_x9tf4gjn2k7f", name: "friends", profileIds: [], userCount: 2, dnsPresetId: "", happNodes: 2, amneziaNodes: 1 },
  { id: "grp_q5w8zr6y3m2e", name: "my", profileIds: [], userCount: 1, dnsPresetId: "", happNodes: 2, amneziaNodes: 1 },
];
const listUser = (id: string, name: string, group: number, over: object) => ({
  id: `usr_${id}`,
  name,
  groupId: listGroups[group]!.id,
  groupName: listGroups[group]!.name,
  status: UserStatus.ACTIVE,
  devicesUsed: 2,
  deviceLimit: 5,
  usedBytes: 12_300_000_000,
  quotaBytes: 100_000_000_000,
  expiresUnix: now + 86400 * 40,
  lastSeenUnix: now - 780,
  nextResetUnix: 0,
  speedLimitBps: 0,
  createdUnix: now - 86400 * 90,
  online: false,
  via: [App.HAPP],
  accessHapp: true,
  accessAmnezia: true,
  currentNodeId: "",
  currentNodeName: "",
  ...over,
});
const listRows = [
  listUser("a", "Marina", 1, { online: true, via: [App.HAPP, App.AMNEZIA], currentNodeId: "nod_1", currentNodeName: "de1", lastSeenUnix: now }),
  listUser("b", "Boris", 0, { online: true, currentNodeId: "nod_2", currentNodeName: "fi1", lastSeenUnix: now }),
  listUser("c", "Clara", 2, { via: [App.AMNEZIA], lastSeenUnix: now - 13 * 60 }),
  listUser("d", "Denis", 3, { via: [App.HAPP, App.AMNEZIA], lastSeenUnix: now - 3 * 3600 }),
  listUser("e", "Egor", 0, { via: [], lastSeenUnix: now - 86400 * 2 }),
  listUser("f", "Anna", 1, { accessHapp: false, accessAmnezia: false, via: [], lastSeenUnix: 0 }),
  listUser("g", "Ilya", 2, { status: UserStatus.DISABLED, lastSeenUnix: now - 86400 * 9 }),
];
const listPage = { pages: [{ users: listRows, nextPageToken: "", counts: { all: 7, online: 2, expiring: 0, overQuota: 0 } }], pageParams: [""] };

export function PolishKit() {
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
      <Shot
        id="users-list"
        title="Users: online in green, a tinted chip per group, Link / Keys chips"
        seed={[owner, [groupsQuery.queryKey, listGroups], [["users", "count"], { counts: { all: 7, online: 2 } }], [["users", "list", "all", "", ""], listPage]]}
      >
        <UsersScreen />
      </Shot>
      <Shot id="polish-int" title="Integrations: one card header, history in sand, “Новый токен”" seed={[owner, [tokensQuery.queryKey, tokens], [approvalsQuery.queryKey, approvals]]}>
        <IntegrationsScreen />
      </Shot>
      <Shot id="polish-int-empty" title="Tokens: none yet, one action" seed={[[tokensQuery.queryKey, { nowUnix: now, tokens: [] }]]}>
        <TokensCard />
      </Shot>
      <Shot id="polish-int-error" title="Tokens: the load failed (compact, inside the card)">
        <TokensCard />
      </Shot>
      <Shot id="polish-danger" title="Danger zone: the user and the profile (the node's is in “Node settings” above)">
        <DangerZones />
      </Shot>
      <Shot id="polish-settings-errors" title="Security (the Cloudflare card keeps its place), Sessions and Audit: the load failed" seed={[owner, [passkeysQuery.queryKey, { passkeys: [{ id: "pk1", name: "iPhone", createdAtUnix: now - 86400 * 40, lastUsedAtUnix: now - 3600, backedUp: true }] }]]}>
        <SecurityPage />
        <SessionsPage />
        <AuditPage />
      </Shot>
      <Shot id="polish-pages-errors" title="Nodes and Updates: the load failed (a screen of its own)">
        <NodesScreen />
        <UpdatesScreen />
      </Shot>
      {/* fixed in the corner of the window, so it only appears when asked for: /dev-kit?update-toast (its collapsed and closed states live in localStorage) */}
      {new URLSearchParams(location.search).has("update-toast") && (
        <Shot
          id="polish-update-toast"
          title="Update notice: a newer panel release, or with =nodes the nodes behind the bundle (fixed in the corner)"
          seed={[
            owner,
            [
              updatesQuery.queryKey,
              {
                nowUnix: now,
                panel: { version: "v0.1.17", built: now, update: { version: "v0.1.18", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.1.18", available: new URLSearchParams(location.search).get("update-toast") !== "nodes", supported: true, installable: true, installing: false, errorKey: "" } },
                bundle: { status: BundleStatus.TRUSTED, version: "v0.1.18", built: now },
                nodes: ["de1", "nl1", "fi1"].map((name, i) => ({ nodeId: `nod_${i}`, name, state: i < 2 ? NodeUpdateState.OUTDATED : NodeUpdateState.UP_TO_DATE })),
              },
            ],
          ]}
        >
          {/* the kit page centres its column with a transform, which would turn "fixed" into "inside the column" */}
          {createPortal(<UpdateToast />, document.body)}
        </Shot>
      )}
      <Shot id="polish-errors" title="QueryError and Pending: full and compact">
        <QueryError error={new Error("x")} onRetry={() => {}} />
        <div className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
          <QueryError compact error={new Error("x")} onRetry={() => {}} />
          <Pending compact />
        </div>
      </Shot>
    </div>
  );
}
