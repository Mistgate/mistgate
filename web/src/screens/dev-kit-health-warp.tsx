import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus, WarpSource, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { alertsQuery, doctorQuery, type Alert } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { AlertsTab } from "@/screens/health/alerts";
import { DoctorTab } from "@/screens/health/doctor";
import { useFixFlow } from "@/screens/health/fix";
import { PeopleTab } from "@/screens/health/people";
import { ConnectionPanel } from "@/screens/users/user-detail";
import { NodeDoctorTab } from "@/screens/node/doctor";
import { SettingsTab } from "@/screens/node/settings";
import { WarpCard } from "@/screens/node/warp";
import { warpQuery } from "@/screens/node/warp-model";
import { EgressNote } from "@/screens/profiles/egress-note";
import { UpdatesScreen } from "@/screens/updates";

// Development only (the dev kit): the WARP card, node settings, alerts, the doctor and Updates as the health-warp work left
// them, on private query caches filled with made-up data, so every state can be looked at without a panel. Each block
// carries data-shot for the screenshot script.

const NOW = Math.floor(Date.now() / 1000);
const h = 3600;

function cache(fill: (c: QueryClient) => void) {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER, displayName: "Owner" } } as never);
  c.setQueryData(nodesQuery.queryKey, {
    nodes: [
      { id: "nod_de1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", warp: { state: WarpState.UP } },
      { id: "nod_fi1", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", warp: { state: WarpState.NOT_CONFIGURED } },
      { id: "nod_de2", name: "de2", status: NodeStatus.ONLINE, countryCode: "DE", warp: { state: WarpState.DISABLED } },
    ],
  } as never);
  fill(c);
  return c;
}

/** A private cache for the blocks under it: made once, filled by `fill`. */
function Mock({ fill, children }: { fill: (c: QueryClient) => void; children: ReactNode }) {
  const [qc] = useState(() => cache(fill));
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function Shot({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      {children}
    </section>
  );
}

// ---- WARP

const account = (over: object = {}) => ({
  nodeId: "",
  source: WarpSource.REGISTERED,
  accountType: "free",
  enabled: true,
  endpointV4: "162.159.192.1",
  ports: [2408],
  useReserved: true,
  hasToken: true,
  ...over,
});
const health = (over: object = {}) => ({
  state: WarpState.UP,
  backend: "kernel",
  endpoint: "162.159.192.1:2408",
  lastHandshakeUnix: NOW - 40,
  warpFlag: "on",
  colo: "ARN",
  probeCloudflareOk: true,
  probeOtherOk: true,
  consecutiveFailures: 0,
  rxBytes: 7_400_000_000,
  txBytes: 1_100_000_000,
  lastError: "",
  reportedUnix: NOW - 5,
  probeCloudflare: { ok: true, latencyMs: 180, atUnix: NOW - 12 },
  probeOther: { ok: true, latencyMs: 240, atUnix: NOW - 12 },
  checkedUnix: NOW - 12,
  ...over,
});
const warp = (over: object = {}) => ({
  account: account(),
  health: health(),
  pendingApply: false,
  applyError: "",
  agentSupports: true,
  tosUrl: "https://www.cloudflare.com/application/terms/",
  needsAttention: false,
  attentionReason: "",
  inbounds: [{ inboundId: "inb_w1", profileName: "hy2 · WARP · 8443", online: 3 }],
  ...over,
});
const dead = { ok: false, latencyMs: 6001, atUnix: NOW - 20 };
const warpCards = [
  { id: "nod_w_ok", name: "de1", title: "Working (Pause asks first)", data: warp() },
  {
    id: "nod_w_down",
    name: "fi1",
    title: "Down on a live account: Restart WARP first",
    data: warp({
      health: health({ state: WarpState.DOWN, lastHandshakeUnix: NOW - 3 * h - 300, consecutiveFailures: 37, lastError: "handshake_stale; ladder: every endpoint tried", probeCloudflareOk: false, probeOtherOk: false, probeCloudflare: dead, probeOther: dead }),
      needsAttention: true,
      attentionReason: "down_after_ladder",
    }),
  },
  {
    id: "nod_w_revoked",
    name: "de2",
    title: "Revoked by Cloudflare: Register again",
    data: warp({ health: health({ state: WarpState.DOWN, warpFlag: "off", lastError: "warp_flag_off", consecutiveFailures: 5, probeOtherOk: false, probeOther: dead }), needsAttention: true, attentionReason: "revoked" }),
  },
  {
    id: "nod_w_paused",
    name: "nl1",
    title: "Paused (Resume needs no question)",
    data: warp({ account: account({ enabled: false }), health: health({ state: WarpState.DISABLED, lastHandshakeUnix: NOW - 26 * h, reportedUnix: NOW - 30, checkedUnix: 0, probeCloudflare: undefined, probeOther: undefined }) }),
  },
];

// ---- node settings

const nodeData = {
  node: { id: "nod_w_ok", name: "de1", address: "de1.example.com", countryCode: "DE", location: "Frankfurt", provider: "ExampleHost", status: NodeStatus.ONLINE, awgBackend: "auto" },
  inbounds: [],
  onlineUsers: [],
  topToday: [],
  notes: "",
  dnsResolvers: [],
  timeouts: { livenessTimeoutS: 90, applyTimeoutS: 120, dialTimeoutS: 15 },
  facts: { virt: "kvm" },
};

// ---- alerts

const alert = (over: Partial<Alert>): Alert => ({
  id: "alt",
  severity: AlertSeverity.WARNING,
  kind: AlertKind.CHECK_FAILED,
  nodeId: "nod_de1",
  nodeName: "de1",
  subject: "",
  titleKey: "health.alert.check_failed.title",
  params: {},
  whyKey: "",
  firstSeenUnix: NOW - 47 * 60,
  openedUnix: over.firstSeenUnix ?? NOW - 47 * 60,
  lastSeenUnix: NOW,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["open_node", "mute"],
  ...over,
});
const activeAlerts: Alert[] = [
  alert({
    id: "a1",
    severity: AlertSeverity.CRITICAL,
    kind: AlertKind.NO_TRAFFIC,
    nodeId: "nod_fi1",
    nodeName: "fi1",
    titleKey: "health.alert.no_traffic.title",
    whyKey: "health.alert.no_traffic.why.udp_all_blocked",
    params: { failed: "2", total: "2", ports: "443, 8443" },
    firstSeenUnix: NOW - 2 * h,
  }),
  alert({
    id: "a2",
    subject: "inb_w1",
    whyKey: "health.alert.check_failed.why.timeout",
    params: { inbound: "inb_w1", profile: "hy2 · WARP · 8443", port: "8443", error_code: "timeout", online: "3" },
    actions: ["restart_inbound", "open_node", "mute"],
  }),
  alert({
    id: "a3",
    nodeId: "nod_de2",
    nodeName: "de2",
    subject: "inb_2",
    whyKey: "health.alert.check_failed.why.udp_blocked",
    params: { inbound: "inb_2", profile: "hy2 · 8443", port: "8443", error_code: "timeout" },
    actions: ["open_profiles", "open_node", "mute"],
  }),
  alert({
    id: "a4",
    kind: AlertKind.CERT_EXPIRY,
    nodeId: "nod_de2",
    nodeName: "de2",
    subject: "inb_3",
    titleKey: "health.alert.cert_expiry.title",
    whyKey: "health.alert.cert_expiry.why",
    params: { inbound: "inb_3", profile: "hy2 · 443 · Salamander", server_name: "de2.example.com", days_left: "5" },
    actions: ["open_profiles", "open_node", "mute"],
  }),
  alert({
    id: "a5",
    kind: AlertKind.DOCTOR_WARN,
    subject: "foreign_vpn",
    titleKey: "health.alert.doctor_warn.title",
    whyKey: "health.doctor.foreign_vpn.why",
    params: { check: "foreign_vpn", names: "unit:x-ui" },
    actions: ["open_node", "accept", "mute"],
  }),
  alert({
    id: "a6",
    severity: AlertSeverity.CRITICAL,
    kind: AlertKind.NODE_DOWN,
    nodeId: "nod_nl1",
    nodeName: "nl1",
    titleKey: "health.alert.node_down.title",
    whyKey: "health.alert.node_down.why",
    params: { minutes: "185" },
    firstSeenUnix: NOW - 3 * h,
    mutedUntilUnix: NOW + 5 * h,
  }),
];
const historyAlerts: Alert[] = [
  alert({ id: "h1", kind: AlertKind.DOCTOR_WARN, titleKey: "health.alert.doctor_warn.title", params: { check: "ipv6" }, resolvedAtUnix: NOW - 2 * h, resolution: "accepted", firstSeenUnix: NOW - 30 * h }),
  alert({ id: "h2", params: { profile: "hy2 · WARP · 8443" }, resolvedAtUnix: NOW - 5 * h, resolution: "superseded", firstSeenUnix: NOW - 6 * h }),
];

// alerts about people: every kind and variant, with the node alerts above mixed in to show they stay out of People
const personAlert = (over: Partial<Alert>): Alert =>
  alert({ nodeId: "", nodeName: "", titleKey: "health.alert.user_connection.title", actions: ["open_user", "mute"], ...over });
const peopleAlerts: Alert[] = [
  personAlert({
    id: "p1",
    kind: AlertKind.ACCESS_ENDED,
    subject: "usr_masha",
    titleKey: "health.alert.access_ended.title",
    whyKey: "health.alert.access_ended.why.expired",
    params: { user_name: "Masha", user_id: "usr_masha", since: String(NOW - 3 * 24 * h) },
    firstSeenUnix: NOW - 5 * h,
  }),
  personAlert({
    id: "p2",
    kind: AlertKind.ACCESS_ENDED,
    subject: "usr_dima",
    titleKey: "health.alert.access_ended.title",
    whyKey: "health.alert.access_ended.why.quota",
    params: { user_name: "Dima", user_id: "usr_dima", since: String(NOW - 26 * h) },
    firstSeenUnix: NOW - 2 * h,
    mutedUntilUnix: NOW + 3 * h,
  }),
  personAlert({
    id: "p3",
    severity: AlertSeverity.INFO,
    kind: AlertKind.USER_CONNECTION,
    subject: "dev_never",
    whyKey: "health.alert.user_connection.why.never_connected",
    params: { user_name: "Oleg", user_id: "usr_oleg" },
    firstSeenUnix: NOW - 20 * h,
  }),
  personAlert({
    id: "p4",
    severity: AlertSeverity.INFO,
    kind: AlertKind.USER_CONNECTION,
    subject: "dev_stale",
    whyKey: "health.alert.user_connection.why.stale_key",
    params: { user_name: "Anya", user_id: "usr_anya" },
    firstSeenUnix: NOW - 3 * h,
  }),
  personAlert({
    id: "p5",
    severity: AlertSeverity.INFO,
    kind: AlertKind.USER_CONNECTION,
    subject: "usr_ivan",
    whyKey: "health.alert.user_connection.why.silent",
    params: { user_name: "Ivan", user_id: "usr_ivan" },
    firstSeenUnix: NOW - 30 * h,
  }),
];
// the node alert of people ("Connections dropped on the node") is about the node: it stays with the node alerts
const usersImpacted = alert({
  id: "a7",
  kind: AlertKind.USERS_IMPACTED,
  nodeId: "nod_de1",
  nodeName: "de1",
  subject: "hysteria2",
  titleKey: "health.alert.users_impacted.title",
  whyKey: "health.alert.users_impacted.why.gone",
  params: { now: "0", usual: "6", users: "6" },
});

function Alerts() {
  const flow = useFixFlow();
  return (
    <>
      <AlertsTab active={[...activeAlerts, usersImpacted, ...peopleAlerts]} history={historyAlerts} now={NOW} flow={flow} />
      {flow.modal}
    </>
  );
}

function People({ active = [...peopleAlerts, ...activeAlerts] }: { active?: Alert[] }) {
  const flow = useFixFlow();
  return (
    <>
      <PeopleTab active={active} now={NOW} flow={flow} />
      {flow.modal}
    </>
  );
}

// ---- doctor

const item = (over: object) => ({ id: "", status: DoctorStatus.OK, titleKey: "", detail: "", detailCode: "", params: {}, whyKey: "", fixId: "", measuredUnix: NOW - 60, acceptedUnix: 0, acceptedBy: "", acceptedByName: "", ...over });
const nodeDoctor = (over: object) => ({ nodeId: "nod_de1", nodeName: "de1", nodeStatus: NodeStatus.ONLINE, agentSupported: true, hasReport: true, receivedUnix: NOW - 120, ageS: 120, stale: false, items: [], agentVersion: "0.3.0", lastSeenUnix: NOW, ...over });
const de1Items = [
  item({
    id: "port_conflicts",
    status: DoctorStatus.FAIL,
    titleKey: "doctor.port_conflicts.title",
    whyKey: "health.doctor.port_conflicts.why",
    detailCode: "port_conflicts.held",
    params: { inbound_id: "inb_2", profile: "hy2 · 8443", network: "udp", port: "8443", process: "caddy(812)" },
  }),
  item({
    id: "warp_path",
    status: DoctorStatus.FAIL,
    titleKey: "doctor.warp_path.title",
    whyKey: "health.doctor.warp_path.why",
    detailCode: "warp_path.down",
    params: { state: "down", backend: "kernel", error: "handshake_stale; ladder: every endpoint tried", profiles: "hy2 · WARP · 8443" },
  }),
  item({
    id: "port_conflicts",
    status: DoctorStatus.FAIL,
    titleKey: "doctor.port_conflicts.title",
    detailCode: "port_conflicts.bind_failed",
    fixId: "restart_inbound",
    params: { inbound_id: "inb_w1", profile: "hy2 · WARP · 8443", network: "udp", port: "443" },
  }),
  item({ id: "time_sync", status: DoctorStatus.WARN, titleKey: "doctor.time_sync.title", whyKey: "health.doctor.time_sync.why", detailCode: "time_sync.offset", params: { offset_s: "4", ntp_synced: "no" } }),
  item({ id: "foreign_vpn", status: DoctorStatus.WARN, titleKey: "doctor.foreign_vpn.title", whyKey: "health.doctor.foreign_vpn.why", detailCode: "foreign_vpn.found", params: { names: "unit:x-ui", count: "1" } }),
  item({ id: "memory_pressure", status: DoctorStatus.WARN, titleKey: "doctor.memory_pressure.title", whyKey: "health.doctor.memory_pressure.why", detailCode: "memory_pressure.usage", params: { avail_pct: "9", swap_pct: "64", oom_kills: "1" }, acceptedUnix: NOW - 26 * h, acceptedBy: "adm_1", acceptedByName: "Owner" }),
  item({ id: "ipv6", status: DoctorStatus.OK, titleKey: "doctor.ipv6.title", detailCode: "ipv6.none_warp_ipv4", params: { ipv6: "no" } }),
  item({ id: "resolver", status: DoctorStatus.OK, titleKey: "doctor.resolver.title", detailCode: "resolver.ok", params: { domains: "4", failed_count: "0", median_ms: "12" } }),
  item({ id: "disk_space", status: DoctorStatus.OK, titleKey: "doctor.disk_space.title", detailCode: "disk_space.usage", params: { mount: "/", used_pct: "41", free_mb: "14800", inode_pct: "9" } }),
];
const fleetDoctor = {
  nowUnix: NOW,
  nodes: [
    nodeDoctor({ items: de1Items.slice(0, 2) }),
    nodeDoctor({ nodeId: "nod_de2", nodeName: "de2", items: [item({ id: "dstate_tasks", status: DoctorStatus.FAIL, titleKey: "doctor.dstate_tasks.title", whyKey: "health.doctor.dstate_tasks.why", detailCode: "dstate_tasks.stuck", params: { stuck: "2", tasks: "apt-get(812),dpkg(901)" } })] }),
    nodeDoctor({ nodeId: "nod_fi1", nodeName: "fi1", agentSupported: false, hasReport: false, agentVersion: "0.2.7" }),
    nodeDoctor({ nodeId: "nod_nl1", nodeName: "nl1", nodeStatus: NodeStatus.DOWN, lastSeenUnix: NOW - 3 * h, stale: true, ageS: 3 * h, items: [item({ id: "disk_space", status: DoctorStatus.OK })] }),
  ],
};

function Fleet() {
  const flow = useFixFlow();
  return (
    <>
      <DoctorTab data={fleetDoctor as never} flow={flow} />
      {flow.modal}
    </>
  );
}

// ---- updates

const OLD = NOW - 40 * 86_400; // build stamps: the old agent, the current release, the new one
const CUR = NOW - 3 * 86_400;
const NEW = NOW - 2 * h;
const upNode = (over: object) => ({ nodeId: "nod_de1", name: "de1", version: "0.3.0-9dd4c", built: CUR, supportsUpdate: true, crashGuard: true, state: NodeUpdateState.UP_TO_DATE, inbounds: 2, onlineUsers: 4, address: "de1.example.com", arch: "amd64", ...over });
const bundle = {
  status: BundleStatus.TRUSTED,
  version: "0.3.0-9dd4c",
  built: CUR,
  expiresUnix: 0,
  files: [
    { os: "linux", arch: "amd64", name: "mistgate-node-linux-amd64", size: 23_400_000, sha256: "9dd4c0123456789abcdef" },
    { os: "linux", arch: "arm64", name: "mistgate-node-linux-arm64", size: 21_900_000, sha256: "1a2b3c0123456789abcdef" },
  ],
  errorKey: "",
  params: {},
  scannedUnix: NOW - 600,
};
const updatesPage = (over: object) => ({
  nowUnix: NOW,
  panel: { version: "0.3.0-9dd4c", built: CUR, hasReleaseKey: true, releaseKeyFingerprint: "abcd1234abcd1234" },
  bundle,
  distDir: "/var/lib/mistgate/dist",
  ...over,
});
const manualUpdates = updatesPage({
  nodes: [upNode({}), upNode({ nodeId: "nod_fi1", name: "fi1", version: "0.2.7-1f0e2", built: OLD, supportsUpdate: false, state: NodeUpdateState.UNSUPPORTED, address: "203.0.113.12", arch: "arm64" })],
});
const pausedUpdates = updatesPage({
  bundle: { ...bundle, version: "0.3.1-77ab1", built: NEW },
  nodes: [
    upNode({ state: NodeUpdateState.OUTDATED }),
    upNode({ nodeId: "nod_de2", name: "de2", state: NodeUpdateState.OUTDATED, address: "de2.example.com" }),
    upNode({ nodeId: "nod_fi1", name: "fi1", state: NodeUpdateState.OUTDATED, address: "fi1.example.com" }),
  ],
  rollout: {
    id: "rol_1",
    status: RolloutStatus.PAUSED,
    toVersion: "0.3.1-77ab1",
    toBuilt: NEW,
    batchSize: 1,
    createdUnix: NOW - 20 * 60,
    finishedUnix: 0,
    pauseKey: "updates.pause.gate_failed",
    pauseParams: { node: "de2", reason: "probe_failed" },
    steps: [
      { nodeId: "nod_de2", nodeName: "de2", stage: 0, state: StepState.ROLLED_BACK, fromVersion: "0.3.0-9dd4c", fromBuilt: CUR, startedUnix: NOW - 19 * 60, finishedUnix: NOW - 14 * 60, errorKey: "updates.step.err.probe_failed", params: {} },
      { nodeId: "nod_de1", nodeName: "de1", stage: 1, state: StepState.PENDING, fromVersion: "", fromBuilt: 0, startedUnix: 0, finishedUnix: 0, errorKey: "", params: {} },
      { nodeId: "nod_fi1", nodeName: "fi1", stage: 2, state: StepState.PENDING, fromVersion: "", fromBuilt: 0, startedUnix: 0, finishedUnix: 0, errorKey: "", params: {} },
    ],
  },
});

/** The health-warp blocks of the dev kit, as wide as the app's content column at 1280 px (the kit itself is narrower). */
export function HealthWarpKit() {
  return (
    <div className="relative left-1/2 flex w-[min(calc(100vw-2rem),1040px)] -translate-x-1/2 flex-col gap-8">
      <Shot id="warp" title="WARP card (node settings): working / down / revoked / paused">
        <Mock fill={(c) => warpCards.forEach((w) => c.setQueryData(warpQuery(w.id).queryKey, w.data as never))}>
          {warpCards.map((w) => (
            <div key={w.id} data-shot={`warp-${w.id}`} className="flex flex-col gap-1.5">
              <span className="text-[11px] text-muted">{w.title}</span>
              <WarpCard nodeId={w.id} nodeName={w.name} retired={false} />
            </div>
          ))}
        </Mock>
      </Shot>

      <Shot id="settings" title="Node settings: WARP first, the honest DNS, liveness and country fields">
        <Mock
          fill={(c) => {
            c.setQueryData(warpQuery("nod_w_ok").queryKey, warp() as never);
            c.setQueryData(doctorQuery("nod_w_ok").queryKey, { nowUnix: NOW, nodes: [nodeDoctor({ nodeId: "nod_w_ok", items: [] })] } as never);
          }}
        >
          <SettingsTab data={nodeData as never} />
        </Mock>
      </Shot>

      <Shot id="egress" title="Under “Exit: WARP”: every node without WARP is a link to its WARP card">
        <Mock fill={() => {}}>
          <div className="rounded-card border border-line bg-surface p-4">
            <EgressNote nodeIds={["nod_de1", "nod_fi1", "nod_de2"]} />
          </div>
        </Mock>
      </Shot>

      <Shot id="alerts" title="Alerts: the cause and the next step, restart by name, mute ▾, accept (the alerts about people are in the list but not shown: they are under People)">
        <Mock fill={() => {}}>
          <Alerts />
        </Mock>
      </Shot>

      <Shot id="people" title="Health ▸ People: access ended (expired, quota), never connected, stale key, silent app; one muted; warnings first">
        <Mock fill={() => {}}>
          <People />
        </Mock>
      </Shot>

      <Shot id="people-empty" title="Health ▸ People: nothing to show">
        <Mock fill={() => {}}>
          <People active={activeAlerts} />
        </Mock>
      </Shot>

      <Shot id="user-connection" title="A user's page ▸ Connection: the open alerts of this person (nothing open: no block)">
        <Mock fill={(c) => c.setQueryData(alertsQuery.queryKey, { nowUnix: NOW, active: [...activeAlerts, ...peopleAlerts], history: [] } as never)}>
          <div className="flex max-w-xl flex-col gap-3">
            <ConnectionPanel userId="usr_masha" />
            <ConnectionPanel userId="usr_nobody" />
          </div>
        </Mock>
      </Shot>

      <Shot id="doctor" title="A node's doctor: Problem/Attention, Manual ▾, accept, accepted, restart by name">
        <Mock fill={(c) => c.setQueryData(doctorQuery("nod_de1").queryKey, { nowUnix: NOW, nodes: [nodeDoctor({ items: de1Items })] } as never)}>
          <NodeDoctorTab nodeId="nod_de1" nodeName="de1" />
        </Mock>
      </Shot>

      <Shot id="fleet" title="The fleet doctor: one line per node without a fresh report">
        <Mock fill={() => {}}>
          <Fleet />
        </Mock>
      </Shot>

      <Shot id="updates-manual" title="Updates: a node waits for the manual step">
        <Mock fill={(c) => c.setQueryData(updatesQuery.queryKey, manualUpdates as never)}>
          <UpdatesScreen />
        </Mock>
      </Shot>

      <Shot id="updates-paused" title="Updates: paused because a node failed the check">
        <Mock fill={(c) => c.setQueryData(updatesQuery.queryKey, pausedUpdates as never)}>
          <UpdatesScreen />
        </Mock>
      </Shot>
    </div>
  );
}
