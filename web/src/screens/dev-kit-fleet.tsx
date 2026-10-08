import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { AddNodeProvider, InstallSteps, useAddNode } from "@/components/add-node";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { EventSeverity, OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { alertsQuery, checksQuery } from "@/lib/health";
import { nodeQuery, nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { HealthScreen } from "@/screens/health";
import { EventsTab } from "@/screens/node/events";
import { NodesScreen } from "@/screens/nodes";
import { OverviewScreen } from "@/screens/overview";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";

// Development only, part of /dev-kit: the fleet screens (Overview with its first-run checklist and health strip, the
// node list, a node's events, the add-node window, Health) on made-up data in private query caches, so they can be looked
// at without a panel. Plain English on purpose: this page never ships.

const now = Math.floor(Date.now() / 1000);
const hour = now - (now % 3600);
let seq = 0;

type Over = Record<string, unknown>;
const reason = (code: string, params: Record<string, string> = {}) => ({ code, params });
const card = (name: string, status: NodeStatus, over: Over = {}) => ({
  id: `nod_${name}`, name, countryCode: "DE", location: "", provider: "", status, reason: undefined, online: [], downBps: 0, upBps: 0,
  hasMetrics: status === NodeStatus.ONLINE, cpuPct: 7, sparkBytes: Array.from({ length: 24 }, (_, i) => (status === NodeStatus.ONLINE ? 2e8 + i * 3e7 : 0)), ...over,
});
const row = (c: ReturnType<typeof card>, over: Over = {}) => ({
  id: c.id, name: c.name, countryCode: c.countryCode, location: c.location, provider: "examplehost", address: `${c.name}.example.com`, status: c.status, reason: c.reason,
  protocols: c.status === NodeStatus.PENDING ? [] : ["hysteria2", "awg"], online: c.online, trafficTodayBytes: c.status === NodeStatus.ONLINE ? 4.2e9 : 0,
  hasMetrics: c.hasMetrics, cpuPct: 7, ramPct: 40, agentVersion: "0.3.1", uptimeS: 86400 * 3, lastSeenUnix: now, awgBackend: "auto", ...over,
});
const series = (on: boolean) =>
  Array.from({ length: 24 }, (_, i) => ({
    startUnix: hour - (23 - i) * 3600,
    values: on ? [{ protocol: "hysteria2", value: 1.5e9 + Math.round(Math.sin(i / 3) * 6e8) }, { protocol: "awg", value: 4e8 }] : [],
  }));
const event = (code: string, ago: number, over: Over = {}) => ({
  id: ++seq, timeUnix: now - ago, severity: EventSeverity.INFO, code, params: {}, nodeId: "", nodeName: "", userId: "", userName: "", inboundId: "",
  profileName: "", protocol: "", source: "agent", ...over,
});
const overview = (cards: ReturnType<typeof card>[], over: Over = {}) => ({
  nowUnix: now, nodesTotal: cards.length, nodesProblem: 0, usersOnline: 0, nodes: cards, traffic: series(cards.some((c) => c.status === NodeStatus.ONLINE)),
  online: series(false), events: [], topConsumers: [], alertsActive: 0, alertsCritical: 0, ...over,
});

type Data = {
  overview: ReturnType<typeof overview>;
  nodes?: Over[];
  profiles?: Over[];
  groups?: Over[];
  users?: number;
  alerts?: Over[];
};

function client(d: Data) {
  const qc = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
  qc.setQueryData(overviewQuery(OverviewRange.OVERVIEW_RANGE_24H).queryKey, d.overview as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes: d.nodes ?? d.overview.nodes.map((c) => row(c)), expectedAgentVersion: "0.3.1" } as never);
  qc.setQueryData(profileListQuery.queryKey, (d.profiles ?? []) as never);
  qc.setQueryData(groupsQuery.queryKey, (d.groups ?? [{ id: "grp_all", name: "Все", profileIds: [], userCount: 0, dnsPresetId: "" }]) as never);
  qc.setQueryData(userCountQuery.queryKey, { counts: { all: d.users ?? 0 } } as never);
  qc.setQueryData(alertsQuery.queryKey, { nowUnix: now, active: d.alerts ?? [], history: [] } as never);
  qc.setQueryData(checksQuery().queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [] } as never);
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER, displayName: "owner" }, version: "dev" } as never);
  return qc;
}

/** A screen on its own data, in a frame as wide as the panel's content column at 1280 px. */
function Demo({ title, data, children }: { title: string; data: Data; children: ReactNode }) {
  const [qc] = useState(() => client(data));
  return (
    <section className="flex flex-col gap-3" data-demo={title}>
      <SectionLabel>{title}</SectionLabel>
      <div className="rounded-card-lg border border-dashed border-line p-3 md:p-5">
        <QueryClientProvider client={qc}>{children}</QueryClientProvider>
      </div>
    </section>
  );
}

const empty: Data = { overview: overview([]) };
const pending: Data = {
  overview: overview([card("de1", NodeStatus.PENDING, { reason: reason("enrollment_pending", { expires_in_minutes: "52", expires_unix: String(now + 52 * 60) }) })]),
};
const noProfiles: Data = (() => {
  const de1 = card("de1", NodeStatus.ONLINE, { reason: reason("no_profiles") });
  return { overview: overview([de1]), nodes: [row(de1, { address: "203.0.113.10", protocols: [] })] };
})();

const fleetCards = [
  card("de1", NodeStatus.ONLINE, { online: [{ protocol: "hysteria2", users: 4 }, { protocol: "awg", users: 1 }], downBps: 58_400_000 }),
  card("fi2", NodeStatus.ONLINE, {
    countryCode: "FI",
    reason: reason("inbound_failed", { profile: "hy2 · WARP · 8443", error: "listen udp :8443: bind: address already in use", failed: "1", total: "3", inbound: "inb_2" }),
    online: [{ protocol: "hysteria2", users: 3 }],
    downBps: 12_100_000,
  }),
  card("nl1", NodeStatus.DOWN, { countryCode: "NL", reason: reason("agent_silent", { minutes: "3065" }) }),
  card("de2", NodeStatus.BLIP, { countryCode: "DE", reason: reason("host_blip", { minutes: "4" }) }),
  card("fi1", NodeStatus.ONLINE, { countryCode: "FI", reason: reason("no_profiles") }),
  card("se1", NodeStatus.PENDING, { countryCode: "SE", reason: reason("enrollment_expired") }),
];
const alert = (over: Over) => ({
  id: `alt_${++seq}`, severity: AlertSeverity.WARNING, kind: AlertKind.CHECK_FAILED, nodeId: "", nodeName: "", subject: "", titleKey: "", params: {}, whyKey: "",
  firstSeenUnix: now - 3600, lastSeenUnix: now, resolvedAtUnix: 0, resolution: "", mutedUntilUnix: 0, actions: ["open_node"], ...over,
});
const problems: Data = {
  overview: overview(fleetCards, {
    usersOnline: 8,
    alertsActive: 2,
    alertsCritical: 1,
    events: [
      event("node_down", 2820, { severity: EventSeverity.ERROR, params: { minutes: "10" }, nodeId: "nod_nl1", nodeName: "nl1", source: "panel" }),
      event("engine_failed", 3000, { severity: EventSeverity.ERROR, params: { error: "bind: address already in use" }, nodeId: "nod_fi2", nodeName: "fi2", profileName: "hy2 · WARP · 8443" }),
      event("node_blip", 5400, { params: { minutes: "4", rebooted: "false" }, nodeId: "nod_de2", nodeName: "de2", source: "panel" }),
      event("user_over_quota", 7200, { severity: EventSeverity.WARNING, userId: "usr_1", userName: "Зарина", source: "panel" }),
      event("update_step_passed", 9000, { params: { from_version: "0.3.0", to_version: "0.3.1" }, nodeId: "nod_de1", nodeName: "de1", source: "panel" }),
      event("node_enrolled", 86400, { params: { agent_version: "0.3.1" }, nodeId: "nod_fi1", nodeName: "fi1", source: "panel" }),
    ],
    topConsumers: [{ userId: "usr_2", userName: "Артём", nodeId: "nod_de1", nodeName: "de1", downBps: 31_000_000 }],
  }),
  nodes: fleetCards.map((c) => row(c, c.name === "fi2" ? { address: "fi2-helsinki-long-hostname.example.com" } : {})),
  profiles: [
    { id: "prf_1", name: "Hysteria2 · 443", protocol: "hysteria2", nodeCount: 3, userCount: 12 },
    { id: "prf_2", name: "AWG 3.1", protocol: "awg", nodeCount: 2, userCount: 4 },
  ],
  groups: [{ id: "grp_all", name: "Все", profileIds: ["prf_1", "prf_2"], userCount: 14, dnsPresetId: "" }],
  users: 14,
  alerts: [
    alert({ severity: AlertSeverity.CRITICAL, kind: AlertKind.NODE_DOWN, nodeId: "nod_nl1", nodeName: "nl1", titleKey: "health.alert.node_down.title", whyKey: "health.alert.node_down.why", params: { minutes: "3065" } }),
    alert({ nodeId: "nod_fi2", nodeName: "fi2", titleKey: "health.alert.check_failed.title", params: { profile: "hy2 · WARP · 8443" } }),
  ],
};

// a node's history: an update in one line, a rollback in one line, numbers that say something
const nodeEvents = [
  event("node_recovered", 600, { params: { minutes: "47" }, source: "panel" }),
  event("node_down", 3420, { severity: EventSeverity.ERROR, params: { minutes: "10" }, source: "panel" }),
  event("state_applied", 7000, { params: { revision: "12", added: "1", removed: "0", changed: "0", users: "14" } }),
  event("update_step_rolled_back", 20_000, { severity: EventSeverity.WARNING, params: { from_version: "0.3.0", to_version: "0.3.1", reason: "probe_failed" }, source: "panel" }),
  event("update_rolled_back", 19_970, { severity: EventSeverity.WARNING, params: { from_version: "0.3.0", to_version: "0.3.1", reason: "not_committed" } }),
  event("agent_started", 19_970, { params: { version: "0.3.0", prev_version: "0.3.1", reason: "update" } }),
  event("agent_started", 20_300, { params: { version: "0.3.1", prev_version: "0.3.0", reason: "update" } }),
  event("port_lossy", 1800, { severity: EventSeverity.WARNING, params: { inbound: "inb_2", profile: "hy2 · WARP · 8443", port: "8443", sent: "300", got: "189", sender: "de2" }, profileName: "hy2 · WARP · 8443", inboundId: "inb_2", source: "panel" }),
  event("node_blip", 30_000, { params: { minutes: "3", rebooted: "true" }, source: "panel" }),
  event("clock_skew", 40_000, { severity: EventSeverity.WARNING, params: { offset_s: "4" } }),
  event("engine_failed", 50_000, { severity: EventSeverity.ERROR, params: { error: "listen udp :8443: bind: address already in use" }, profileName: "hy2 · WARP · 8443", inboundId: "inb_2" }),
  event("update_step_passed", 80_000, { params: { from_version: "0.2.9", to_version: "0.3.0" }, source: "panel" }),
  event("update_committed", 80_040, { params: { from_version: "0.2.9", to_version: "0.3.0" } }),
  event("engine_started", 80_290, { params: { reason: "agent_start" }, profileName: "Hysteria2 · 443", inboundId: "inb_1" }),
  event("agent_started", 80_300, { params: { version: "0.3.0", prev_version: "0.2.9", reason: "update" } }),
].sort((a, b) => b.id - a.id);

function EventsDemo() {
  const [qc] = useState(() => {
    const c = client(empty);
    c.setQueryData(["node-events", "nod_demo", "all"], { pages: [{ events: nodeEvents, hasMore: false }], pageParams: [0] });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <EventsTab nodeId="nod_demo" />
    </QueryClientProvider>
  );
}

const issued = (over: Over = {}) =>
  ({
    node: { id: "nod_demo", name: "de1", status: NodeStatus.PENDING, online: [], protocols: [] },
    installCommand:
      "chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel panel.example.com:443 --sni q3m8x2kd7w.invalid --ca-sha256 9f2c1a6d0b7e4c3a8f5d2e1b0c9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b1a --token 2qgx7mbd3jc5u4yvtwnfhx6kqjz4pm2s7ylbq3e5c6a7nd8p9r0s && /root/mistgate-node install",
    caFingerprint: "sha256:9f2c1a6d0b7e4c3a8f5d2e1b0c9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b1a",
    expiresUnix: now + 3600,
    copyCommand: "scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node",
    ...over,
  }) as never;

function InstallDemo({ variant, onClose }: { variant: "scp" | "manual" | "connected"; onClose: () => void }) {
  const t = useT();
  const [qc] = useState(() => {
    const c = client(empty);
    c.setQueryData(nodeQuery("nod_demo").queryKey, {
      node: { id: "nod_demo", name: "de1", status: variant === "connected" ? NodeStatus.ONLINE : NodeStatus.PENDING },
      inbounds: [],
    } as never);
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <Modal open onOpenChange={(o) => !o && onClose()} title={t("node.add.commandTitle", { name: "de1" })} description={t("node.add.commandBody")}>
        <InstallSteps issued={issued(variant === "manual" ? { copyCommand: "" } : {})} onClose={onClose} />
      </Modal>
    </QueryClientProvider>
  );
}

function AddNodeButtons() {
  const addNode = useAddNode();
  const [install, setInstall] = useState<"scp" | "manual" | "connected" | null>(null);
  return (
    <div className="flex flex-wrap gap-2">
      <Button onClick={() => addNode()}>Add node: the form</Button>
      <Button onClick={() => addNode({ id: "nod_de1", name: "de1" })}>New command for de1</Button>
      <Button onClick={() => setInstall("scp")}>Install steps: scp from the panel</Button>
      <Button onClick={() => setInstall("manual")}>Install steps: no trusted bundle</Button>
      <Button onClick={() => setInstall("connected")}>Install steps: connected</Button>
      {install && <InstallDemo key={install} variant={install} onClose={() => setInstall(null)} />}
    </div>
  );
}

export function FleetKit() {
  return (
    // out of the kit's narrow column: as wide as the panel's own content column
    <div className="relative left-1/2 flex w-screen -translate-x-1/2 flex-col gap-8 px-4">
      <div className="mx-auto flex w-full max-w-[954px] flex-col gap-8">
        <AddNodeProvider>
          <Demo title="Add node window" data={empty}>
            <AddNodeButtons />
          </Demo>
          <Demo title="Overview: an empty panel (first run)" data={empty}>
            <OverviewScreen />
          </Demo>
          <Demo title="Overview: the only node waits for its install" data={pending}>
            <OverviewScreen />
          </Demo>
          <Demo title="Overview: the node is online, but has no profiles (IP address)" data={noProfiles}>
            <OverviewScreen />
          </Demo>
          <Demo title="Overview: a fleet with problems" data={problems}>
            <OverviewScreen />
          </Demo>
          <Demo title="Nodes" data={problems}>
            <NodesScreen />
          </Demo>
          <Demo title="Health: the number of the header, node problems without an alert" data={problems}>
            <HealthScreen />
          </Demo>
          <Demo title="Node events" data={empty}>
            <EventsDemo />
          </Demo>
        </AddNodeProvider>
      </div>
    </div>
  );
}
