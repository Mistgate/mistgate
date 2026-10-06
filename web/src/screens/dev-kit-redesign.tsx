import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { EventSeverity } from "@/gen/mistgate/admin/v1/fleet_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { ApprovalState, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { AuditSource } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Alert } from "@/lib/health";
import { nodesQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { AlertsTab } from "@/screens/health/alerts";
import { useFixFlow } from "@/screens/health/fix";
import { ApprovalHistory } from "@/screens/integrations/approvals";
import { EventsTab } from "@/screens/node/events";
import { AuditPage } from "@/screens/settings-pages/audit";
import { UpdatesScreen } from "@/screens/updates";
import { UsersScreen } from "@/screens/users/list";
import { groupsQuery } from "@/screens/users/rpc";

// Development only (the dev kit): the paged lists and the redesigned Updates page on made-up data (fake names, documentation
// addresses), on private query caches, so every state can be looked at without a panel. Each block carries data-shot for the
// screenshot script. The paged blocks read their place from the URL of the kit: /dev-kit?page=3, /dev-kit?before=4950.

const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86_400;

type Seed = [readonly unknown[], unknown][];

function Shot({ id, title, seed = [], children }: { id: string; title: string; seed?: Seed; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "Owner", role: Role.OWNER }, version: "0.1.24", stepUpUntilUnix: 0n } as never);
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

// ---- paged lists

const names = ["Marina", "Boris", "Clara", "Denis", "Egor", "Anna", "Ilya", "Vera", "Oleg", "Nina", "Pavel", "Zoya", "Timur", "Lena", "Misha", "Sasha"];
const groups = [
  { id: "grp_a", name: "family", color: "sand" },
  { id: "grp_b", name: "friends", color: "sage" },
  { id: "grp_c", name: "team", color: "lavender" },
];
const person = (i: number) => {
  const g = groups[i % groups.length]!;
  return {
    id: `usr_${i}`,
    name: `${names[i % names.length]}${i >= names.length ? ` ${Math.floor(i / names.length) + 1}` : ""}`,
    groupId: g.id,
    groupName: g.name,
    groupColor: g.color,
    status: i % 23 === 7 ? UserStatus.DISABLED : UserStatus.ACTIVE,
    devicesUsed: 1 + (i % 4),
    deviceLimit: 5,
    usedBytes: (i % 9) * 11_300_000_000 + 1_000_000_000,
    quotaBytes: 100_000_000_000,
    expiresUnix: NOW + D * (5 + (i % 60)),
    lastSeenUnix: NOW - (i % 11) * 3 * H - 600,
    nextResetUnix: 0,
    speedLimitBps: 0,
    createdUnix: NOW - D * 90,
    online: i % 5 === 0,
    via: i % 3 === 0 ? [App.HAPP, App.AMNEZIA] : [App.HAPP],
    accessHapp: true,
    accessAmnezia: i % 3 === 0,
    currentNodeId: i % 5 === 0 ? "nod_1" : "",
    currentNodeName: i % 5 === 0 ? "de1" : "",
  };
};
const userPage = (page: number, size = 50) => ({
  users: Array.from({ length: Math.max(0, Math.min(size, 137 - (page - 1) * size)) }, (_, k) => person((page - 1) * size + k)),
  nextPageToken: "",
  counts: { all: 137, online: 28, expiring: 9, overQuota: 2 },
});

const resolutions = ["recovered", "recovered", "recovered", "node_removed", "recovered"];
const closed = (n: number): Alert => ({
  id: `alt_${n}`,
  severity: n % 7 === 0 ? AlertSeverity.CRITICAL : AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "nod_1",
  nodeName: ["de1", "fi1", "nl1", "se1"][n % 4]!,
  subject: "inb_1",
  titleKey: "health.alert.check_failed.title",
  params: { profile: ["hy2 · 443", "AWG · 51820", "hy2 · WARP"][n % 3]!, port: "443" },
  whyKey: "",
  firstSeenUnix: NOW - n * 3000 - 1800,
  lastSeenUnix: NOW - n * 3000,
  resolvedAtUnix: NOW - n * 3000,
  resolution: resolutions[n % resolutions.length]!,
  mutedUntilUnix: 0,
  actions: [],
});

function AlertsDemo() {
  const flow = useFixFlow();
  return <AlertsTab active={[]} history={Array.from({ length: 137 }, (_, i) => closed(i + 1))} now={NOW} flow={flow} />;
}

const audit = (id: number) => ({
  id,
  timeUnix: NOW - (5000 - id) * 420,
  source: [AuditSource.PANEL, AuditSource.BOT, AuditSource.MCP, AuditSource.API][id % 4]!,
  actorId: "adm_1",
  actorName: "Owner",
  action: ["login", "node_update", "user_create", "alert_mute"][id % 4]!,
  paramsJson: "{}",
  result: "ok",
  ip: `203.0.113.${(id % 200) + 1}`,
});
const auditPage = (before: number) => {
  const top = before === 0 ? 5000 : before - 1;
  return { entries: Array.from({ length: 50 }, (_, i) => audit(top - i)), nextBeforeId: top - 49 };
};

const event = (id: number) => ({
  id,
  timeUnix: NOW - (400 - id) * 900,
  severity: EventSeverity.INFO,
  code: id % 3 === 0 ? "agent_started" : "state_applied",
  params: id % 3 === 0 ? { version: "0.1.24", reason: "boot" } : { revision: String(id), inbounds: "2", added: "0", removed: "0", changed: "1", users: "0" },
  nodeId: "nod_1",
  nodeName: "de1",
  userId: "",
  userName: "",
  inboundId: "",
  profileName: "",
  protocol: "",
  source: "agent",
});

const decided = (n: number) => ({
  id: `pln_${n}`,
  tokenId: "tok_1",
  tokenName: "claude-ops",
  tokenProfile: TokenProfile.OPERATOR,
  tool: ["node_fix", "rollout_start", "user_disable", "alert_mute"][n % 4]!,
  facts: [],
  danger: [],
  reason: "",
  state: n % 6 === 0 ? ApprovalState.REJECTED : ApprovalState.APPLIED,
  createdUnix: NOW - n * 5000,
  expiresUnix: NOW - n * 5000 + 600,
  decidedByName: "Owner",
  decidedUnix: NOW - n * 5000 + 60,
  appliedUnix: NOW - n * 5000 + 70,
  result: "Done.",
  error: "",
});

export function PagingKit() {
  const url = new URLSearchParams(location.search);
  const pageNo = Number(url.get("page") ?? 1) || 1;
  const size = Number(url.get("size") ?? 50) || 50;
  const before = Number(url.get("before") ?? 0) || 0;
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
      <Shot id="paging-alerts" title="Health → Alerts → history: 137 rows, 20 to a page (?page=3&size=50 to move)">
        <AlertsDemo />
      </Shot>
      <Shot
        id="paging-users"
        title="Users: 137 people, the server counts and skips rows (offset)"
        seed={[
          [groupsQuery.queryKey, groups.map((g) => ({ id: g.id, name: g.name, profileIds: [], userCount: 40, dnsPresetId: "", happNodes: 2, amneziaNodes: 1, color: g.color }))],
          [userCountQuery.queryKey, { counts: { all: 137, online: 28 } }],
          [nodesQuery.queryKey, { nodes: [{ id: "nod_1", name: "de1", status: NodeStatus.ONLINE }] }],
          ...[1, 2, 3, 4, 5, 6].flatMap((p) => [25, 50, 100].map((s): [readonly unknown[], unknown] => [["users", "list", "all", "", "", p, s], userPage(p, s)])),
        ]}
      >
        <UsersScreen />
      </Shot>
      <Shot id="paging-audit" title="Settings → Audit: a log read by cursor, Newer / Older (?before=4951)" seed={[[["audit", "all", "all", before, size], auditPage(before)]]}>
        <AuditPage />
      </Shot>
      <Shot
        id="paging-events"
        title="Node → Events: the same cursor pager under the day cards"
        seed={[[["node-events", "nod_1", "all", before, size], { events: Array.from({ length: size }, (_, i) => event(400 - i - (before ? 50 : 0))), hasMore: true }]]}
      >
        <EventsTab nodeId="nod_1" />
      </Shot>
      <Shot id="paging-approvals" title="Integrations → recent decisions: ten to a page, no size picker">
        <ApprovalHistory data={{ approvals: Array.from({ length: 25 }, (_, i) => decided(i + 1)), awaiting: 0, nowUnix: NOW, receivedMs: NOW * 1000 } as never} />
      </Shot>
      <p className="text-xs text-muted">page {pageNo}</p>
    </div>
  );
}

// ---- Updates

const V_OLD = "v0.1.23";
const V_NEW = "v0.1.24";
const B_OLD = NOW - 4 * D;
const B_NEW = NOW - 6 * H;
const upNode = (over: object) => ({
  nodeId: "nod_de1",
  name: "de1",
  version: V_NEW,
  built: B_NEW,
  supportsUpdate: true,
  crashGuard: true,
  state: NodeUpdateState.UP_TO_DATE,
  scheduledUnix: 0,
  scheduledVersion: "",
  scheduledBuilt: 0,
  scheduledTimezoneOffsetMinutes: 0,
  scheduledMissed: false,
  inbounds: 2,
  onlineUsers: 4,
  address: "de1.example.com",
  arch: "amd64",
  ...over,
});
const lastOk = (from: string, to: string, ago: number) => ({ outcome: "ok", fromVersion: from, toVersion: to, reason: "", atUnix: NOW - ago });
const bundle = {
  status: BundleStatus.TRUSTED,
  version: V_NEW,
  built: B_NEW,
  expiresUnix: NOW + 88 * D,
  files: [
    { os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 23_400_000, sha256: "9dd4c0123456789abcdef" },
    { os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 21_900_000, sha256: "1a2b3c0123456789abcdef" },
  ],
  errorKey: "",
  params: {},
  scannedUnix: NOW - 600,
};
const page = (over: object) => ({
  nowUnix: NOW,
  scheduleTimezoneOffsetMinutes: 180,
  panel: {
    version: "v0.1.24",
    built: B_NEW,
    hasReleaseKey: true,
    releaseKeyFingerprint: "abcd1234abcd1234",
    update: { version: "v0.1.24", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.1.24", publishedUnix: B_NEW, checkedUnix: NOW - 300, available: false, supported: true, installable: true, installing: false, errorKey: "", built: B_NEW, sha256: "9dd4c0123456789abcdef0123456789abcdef0123456789abcdef0123456789a" },
  },
  bundle,
  distDir: "/var/lib/mistgate/dist",
  ...over,
});
const doneStep = (nodeId: string, nodeName: string, stage: number, from: number) => ({ nodeId, nodeName, stage, state: StepState.PASSED, fromVersion: V_OLD, fromBuilt: B_OLD, startedUnix: NOW - from, finishedUnix: NOW - from + 420, errorKey: "", params: {} });
const lastRollout = {
  id: "rol_0",
  status: RolloutStatus.DONE,
  toVersion: V_NEW,
  toBuilt: B_NEW,
  batchSize: 1,
  createdUnix: NOW - 2 * H,
  finishedUnix: NOW - H,
  pauseKey: "",
  pauseParams: {},
  steps: [doneStep("nod_nl1", "nl1", 0, 2 * H), doneStep("nod_se1", "se1", 1, 5400)],
};
const available = page({
  nodes: [
    upNode({ nodeId: "nod_de1", name: "de1", version: V_OLD, built: B_OLD, state: NodeUpdateState.OUTDATED, onlineUsers: 5, lastUpdate: lastOk("v0.1.22", V_OLD, 9 * H) }),
    upNode({ nodeId: "nod_fi1", name: "fi1", version: V_OLD, built: B_OLD, state: NodeUpdateState.OUTDATED, onlineUsers: 1, address: "fi1.example.com", lastUpdate: lastOk("v0.1.22", V_OLD, 9 * H) }),
    upNode({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 2, address: "nl1.example.com", lastUpdate: lastOk(V_OLD, V_NEW, 2 * H) }),
    upNode({ nodeId: "nod_se1", name: "se1", onlineUsers: 3, address: "se1.example.com", lastUpdate: lastOk(V_OLD, V_NEW, 5400) }),
  ],
  rollout: lastRollout,
});
const runningStep = (nodeId: string, nodeName: string, stage: number, state: StepState) => ({ nodeId, nodeName, stage, state, fromVersion: V_OLD, fromBuilt: B_OLD, startedUnix: NOW - 600, finishedUnix: 0, errorKey: "", params: {} });
const running = page({
  nodes: [
    upNode({ nodeId: "nod_nl1", name: "nl1", onlineUsers: 1, address: "nl1.example.com", lastUpdate: lastOk(V_OLD, V_NEW, 20 * 60) }),
    upNode({ nodeId: "nod_de1", name: "de1", version: V_OLD, built: B_OLD, state: NodeUpdateState.UPDATING, onlineUsers: 5 }),
    upNode({ nodeId: "nod_fi1", name: "fi1", version: V_OLD, built: B_OLD, state: NodeUpdateState.OUTDATED, onlineUsers: 2, address: "fi1.example.com" }),
    upNode({ nodeId: "nod_se1", name: "se1", version: V_OLD, built: B_OLD, state: NodeUpdateState.OUTDATED, onlineUsers: 3, address: "se1.example.com" }),
  ],
  rollout: {
    id: "rol_1",
    status: RolloutStatus.RUNNING,
    toVersion: V_NEW,
    toBuilt: B_NEW,
    batchSize: 1,
    createdUnix: NOW - 25 * 60,
    finishedUnix: 0,
    pauseKey: "",
    pauseParams: {},
    steps: [
      { ...doneStep("nod_nl1", "nl1", 0, 25 * 60), finishedUnix: NOW - 20 * 60 },
      runningStep("nod_de1", "de1", 1, StepState.GATING),
      runningStep("nod_fi1", "fi1", 2, StepState.PENDING),
      runningStep("nod_se1", "se1", 3, StepState.PENDING),
    ],
  },
});
const panelOffer = page({
  panel: {
    version: "v0.1.24",
    built: B_NEW,
    hasReleaseKey: true,
    releaseKeyFingerprint: "abcd1234abcd1234",
    update: { version: "v0.1.25", url: "https://github.com/Mistgate/mistgate/releases/tag/v0.1.25", publishedUnix: NOW - H, checkedUnix: NOW - 300, available: true, supported: true, installable: true, installing: false, errorKey: "", built: NOW - H, sha256: "77ab10123456789abcdef0123456789abcdef0123456789abcdef0123456789a" },
  },
  nodes: [
    upNode({ nodeId: "nod_de1", name: "de1", lastUpdate: lastOk(V_OLD, V_NEW, 5 * H) }),
    upNode({ nodeId: "nod_fi1", name: "fi1", address: "fi1.example.com", lastUpdate: lastOk(V_OLD, V_NEW, 5 * H) }),
    upNode({ nodeId: "nod_nl1", name: "nl1", address: "nl1.example.com", lastUpdate: lastOk(V_OLD, V_NEW, 4 * H) }),
  ],
});
const untrusted = page({
  bundle: { ...bundle, status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.file_mismatch", params: { file: "mistgate-node-linux-amd64" } },
  nodes: [upNode({ state: NodeUpdateState.OUTDATED, version: V_OLD, built: B_OLD }), upNode({ nodeId: "nod_fi1", name: "fi1", state: NodeUpdateState.OFFLINE, version: V_OLD, built: B_OLD, address: "fi1.example.com" })],
});

export function UpdatesKit() {
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1100px)] -translate-x-1/2 flex-col gap-10">
      <Shot id="updates-available" title="Updates: 2 of 4 nodes on the new agent, the rest to update, the last rollout folded" seed={[[updatesQuery.queryKey, available]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="updates-running" title="Updates: a rollout of 4 nodes running, one stage at a time (the server's batch plan: the canary, then 1 at a time while fewer than 5 are to be updated)" seed={[[updatesQuery.queryKey, running]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="updates-panel" title="Updates: every node current, a signed panel release is out" seed={[[updatesQuery.queryKey, panelOffer]]}>
        <UpdatesScreen />
      </Shot>
      <Shot id="updates-untrusted" title="Updates: the bundle failed the check (its details open by themselves)" seed={[[updatesQuery.queryKey, untrusted]]}>
        <UpdatesScreen />
      </Shot>
    </div>
  );
}
