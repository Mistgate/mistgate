import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CheckStatus, DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { alertsQuery, checksQuery, doctorQuery } from "@/lib/health";
import { overviewQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { updatesQuery } from "@/lib/updates";
import { ChecksTab } from "@/screens/health/checks";
import { AddInbound } from "@/screens/node/add-inbound";
import { NodeDetail } from "@/screens/node/index";
import { nodeAlertsQuery } from "@/screens/node/overview-parts";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";

// Development only: the node screens on a private query cache filled with what the server would say,
// so they render without a panel. ?tab=profiles (or logs, doctor...) on /dev-kit switches the tab of every node below.

const now = Math.floor(Date.now() / 1000);
const hours = Array.from({ length: 24 }, (_, i) => Math.round((0.4 + Math.sin(i / 3.2) * 0.3 + (i > 17 ? 0.5 : 0)) * 2.1e9));

const inbound = (over: object) => ({
  id: "inb_1",
  profileId: "prf_main",
  profileName: "hy2 · 443 · Salamander",
  protocol: "hysteria2",
  nodeId: "nod_de1",
  nodeName: "de1",
  state: InboundState.ACTIVE,
  port: 443,
  tlsServerName: "de1.example.com",
  certNotAfterUnix: now + 70 * 86400,
  certPinSha256: "",
  lastError: "",
  egress: "direct",
  ...over,
});

const baseNode = (over: object) => ({
  id: "nod_de1",
  name: "de1",
  countryCode: "DE",
  location: "Frankfurt",
  provider: "examplehost",
  address: "de1.example.com",
  status: NodeStatus.ONLINE,
  protocols: ["hysteria2"],
  online: [{ protocol: "hysteria2", users: 5 }],
  agentVersion: "0.3.1",
  lastSeenUnix: now - 5,
  awgBackend: "auto",
  warp: { state: WarpState.UP, source: 1, colo: "FRA", accountType: "free" },
  ...over,
});

const metrics = { cpuPct: 23, softirqPct: 4, load1: 0.42, ramUsedBytes: 610e6, ramTotalBytes: 2e9, diskUsedBytes: 7.1e9, diskTotalBytes: 20e9, netRxBps: 38e6, netTxBps: 41e6 };
const facts = { hostname: "de1", os: "Ubuntu 24.04", kernel: "6.8.0-45", arch: "amd64", cpuCount: 2, ramTotalBytes: 2e9, diskTotalBytes: 20e9, virt: "kvm", hasIpv6: true, bootUnix: now - 9 * 86400, engines: ["hysteria2 2.6.1"] };

const alert = (over: object) => ({
  id: "alt_1",
  severity: AlertSeverity.CRITICAL,
  kind: AlertKind.NO_TRAFFIC,
  nodeId: "nod_de1",
  nodeName: "de1",
  subject: "",
  titleKey: "health.alert.no_traffic.title",
  whyKey: "health.alert.no_traffic.why.udp_blocked",
  params: { failed: "2", total: "2", ports: "443, 8443" },
  firstSeenUnix: now - 27 * 60,
  lastSeenUnix: now,
  resolvedAtUnix: 0,
  resolution: "",
  mutedUntilUnix: 0,
  actions: ["mute"],
  ...over,
});

const history48 = (fail: (i: number) => boolean) =>
  Array.from({ length: 48 }, (_, i) => ({ startUnix: now - (47 - i) * 1800, ok: fail(i) ? 0 : 6, failed: fail(i) ? 6 : 0, latencyMs: 38 + (i % 5) * 4 }));
const never = () => false;
const cell = (status: CheckStatus, errorCode = "", latencyMs = 0, fail: (i: number) => boolean = never) => ({
  deployed: true,
  inboundId: `inb_${status}_${errorCode}`,
  failStreak: status === CheckStatus.FAILED ? 3 : 0,
  history: history48(fail),
  last: { status, atUnix: now - 40, latencyMs, exitIp: status === CheckStatus.OK ? "203.0.113.77" : "", exitCountry: status === CheckStatus.OK ? "DE" : "", errorCode, errorDetail: errorCode === "timeout" ? "handshake timeout after 8s" : "" },
});
const columns = [
  { profileId: "prf_main", profileName: "hy2 · 443 · Salamander", protocol: "hysteria2" },
  { profileId: "prf_warp", profileName: "hy2 · WARP · 8443", protocol: "hysteria2" },
  { profileId: "prf_awg", profileName: "AWG 3.1 · QUIC", protocol: "awg" },
];
const matrix = {
  nowUnix: now,
  intervalS: 300,
  columns,
  rows: [
    { nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.OK, "", 42), cell(CheckStatus.SKIPPED, "inbound_failed"), cell(CheckStatus.OK, "", 61)] },
    { nodeId: "nod_de2", nodeName: "de2", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.FAILED, "timeout", 0, (i) => i > 40), cell(CheckStatus.OK, "", 57), { deployed: false, inboundId: "", failStreak: 0, history: [] }] },
    { nodeId: "nod_fi1", nodeName: "fi1", countryCode: "FI", nodeStatus: NodeStatus.ONLINE, cells: [cell(CheckStatus.SKIPPED, "inbound_disabled"), cell(CheckStatus.SKIPPED, "inbound_pending"), cell(CheckStatus.SKIPPED, "client_unsupported")] },
    { nodeId: "nod_nl1", nodeName: "nl1", countryCode: "NL", nodeStatus: NodeStatus.DOWN, cells: [cell(CheckStatus.SKIPPED, "node_offline"), { deployed: false, inboundId: "", failStreak: 0, history: [] }, { deployed: false, inboundId: "", failStreak: 0, history: [] }] },
  ],
};

const profiles = [
  { id: "prf_main", name: "hy2 · 443 · Salamander", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 3, userCount: 12, version: 4, warnings: [] },
  { id: "prf_warp", name: "hy2 · WARP · 8443", protocol: "hysteria2", summary: "UDP 8443 · Salamander · Let's Encrypt · WARP", nodeCount: 2, userCount: 12, version: 2, warnings: [] },
  { id: "prf_new", name: "Основной", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
  { id: "prf_awg", name: "AWG 3.1 · QUIC", protocol: "awg", summary: "UDP 51820 · AWG 3.1 · QUIC", nodeCount: 1, userCount: 3, version: 1, warnings: [] },
];

type Fill = [readonly unknown[], unknown];

/** A private cache: every query the screens ask is answered from here, nothing is fetched. */
function Mock({ fill, children }: { fill: Fill[]; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER } } as never);
    c.setQueryData(updatesQuery.queryKey, { nodes: [] } as never);
    c.setQueryData(groupsQuery.queryKey, [{ id: "grp_all", name: "Все", profileIds: [], userCount: 14, dnsPresetId: "" }] as never);
    c.setQueryData(profileListQuery.queryKey, profiles as never);
    c.setQueryData(alertsQuery.queryKey, { nowUnix: now, active: [], history: [] } as never);
    for (const [key, value] of fill) c.setQueryData(key, value as never);
    return c;
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const nodeFill = (id: string, over: { alerts?: object; doctor?: object[]; checks?: object } = {}): Fill[] => [
  [nodeAlertsQuery(id).queryKey, { nowUnix: now, active: [], history: [], ...over.alerts }],
  [doctorQuery(id).queryKey, { nowUnix: now, nodes: [{ nodeId: id, nodeName: "de1", nodeStatus: NodeStatus.ONLINE, agentSupported: true, hasReport: true, receivedUnix: now - 60, ageS: 60, stale: false, items: over.doctor ?? [] }] }],
  [checksQuery(id).queryKey, over.checks ?? { nowUnix: now, intervalS: 300, columns, rows: [matrix.rows[0]] }],
  [overviewQuery().queryKey, { nowUnix: now, nodes: [{ id, name: "de1", sparkBytes: hours }], traffic: [], online: [], events: [], topConsumers: [] }],
];

/** The node's own checks, one cell per profile in the order of `columns`: what the server says about a node in that state. */
const nodeChecks = (status: NodeStatus, cells: object[]) => ({ nowUnix: now, intervalS: 300, columns: columns.slice(0, cells.length), rows: [{ ...matrix.rows[0], nodeStatus: status, cells }] });

const doctorItem = (id: string, status: DoctorStatus, params: object = {}) => ({ id, status, titleKey: `doctor.${id}.title`, detail: "", detailCode: "", params, whyKey: `health.doctor.${id}.why`, fixId: "", measuredUnix: now });

function Demo({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <div className="rounded-card-lg border border-dashed border-line p-4">{children}</div>
    </section>
  );
}

/** The add-profile dialog behind a button, with the server's check answers already in the cache. */
function AddDemo({ label, node, inbounds = [], checks, list }: { label: string; node: object; inbounds?: object[]; checks: Fill[]; list?: object[] }) {
  const [open, setOpen] = useState(false);
  const fill: Fill[] = [...checks];
  if (list) fill.push([profileListQuery.queryKey, list]);
  return (
    <Mock fill={fill}>
      <Button onClick={() => setOpen(true)}>{label}</Button>
      <AddInbound open={open} onOpenChange={setOpen} data={{ node: baseNode(node), inbounds } as never} />
    </Mock>
  );
}

const check = (nodeId: string, profileId: string, port: string, sni: string) => ["inbound-check", { kind: "create", nodeId, profileId, port, sni }] as const;

export function NodeDemos() {
  // wider than the kit's column: about the content width of the shell at 1280 px
  return (
    <div className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-6">
      <Demo title="Node: no traffic (the alert's why), WARP down, doctor 2 problems, one profile failed">
        <Mock
          fill={nodeFill("nod_de1", {
            alerts: { active: [alert({})], history: [alert({ id: "alt_0", resolvedAtUnix: now - 86400, firstSeenUnix: now - 86400 - 28 * 60, resolution: "cleared" })] },
            doctor: [doctorItem("warp_path", DoctorStatus.FAIL), doctorItem("time_sync", DoctorStatus.WARN)],
            checks: nodeChecks(NodeStatus.NO_TRAFFIC, [cell(CheckStatus.FAILED, "timeout", 0, (i) => i > 44), cell(CheckStatus.SKIPPED, "inbound_failed")]),
          })}
        >
          <NodeDetail
            data={
              {
                node: baseNode({ status: NodeStatus.NO_TRAFFIC, warp: { state: WarpState.DOWN, source: 1, colo: "", accountType: "free" } }),
                inbounds: [
                  inbound({
                    id: "inb_3",
                    profileId: "prf_awg",
                    profileName: "AWG3.1",
                    protocol: "awg",
                    port: 33183,
                    tlsServerName: "",
                    awg: { backend: "kernel", backendVersion: "amneziawg kernel module, genl v3, maxattr 34", ifaceUp: true, peers: 15, peersOnline: 1, peersHandshaken: 2, newestHandshakeUnix: now - 70, udpRxPackets: 900 },
                  }),
                  inbound({}),
                  inbound({ id: "inb_2", profileId: "prf_warp", profileName: "hy2 · WARP · 8443", port: 8443, egress: "warp", state: InboundState.FAILED, certNotAfterUnix: now + 5 * 86400, lastError: "egress warp: tunnel is down" }),
                ],
                metrics,
                facts,
              } as never
            }
          />
        </Mock>
      </Demo>

      <Demo title="Node: unreachable for 47 min">
        <Mock fill={nodeFill("nod_de1", { checks: nodeChecks(NodeStatus.DOWN, [cell(CheckStatus.SKIPPED, "node_offline")]) })}>
          <NodeDetail
            data={{ node: baseNode({ status: NodeStatus.DOWN, online: [], lastSeenUnix: now - 47 * 60, reason: { code: "agent_silent", params: { minutes: "47" } }, warp: { state: WarpState.UNKNOWN, source: 1, colo: "", accountType: "" } }), inbounds: [inbound({})], metrics, facts } as never}
          />
        </Mock>
      </Demo>

      <Demo title="Node: just connected, no profile yet">
        <Mock fill={nodeFill("nod_de1", { checks: { nowUnix: now, intervalS: 300, columns: [], rows: [] } })}>
          <NodeDetail data={{ node: baseNode({ online: [], warp: { state: WarpState.NOT_CONFIGURED } }), inbounds: [], metrics, facts } as never} />
        </Mock>
      </Demo>

      <Demo title="Node: a profile did not start (port held by caddy), a certificate runs out in 5 days">
        <Mock
          fill={[
            ...nodeFill("nod_de1", { doctor: [doctorItem("port_conflicts", DoctorStatus.FAIL, { inbound_id: "inb_2", port: "8443", network: "udp", process: "caddy(812)" })] }),
            // "Change port": the check of the inbound as it is answers with a free port
            [["inbound-check", { kind: "update", inboundId: "inb_2" }], { of: "inb_2", state: "ok", inbound: { port: 8443, tlsServerName: "de1.example.com" }, warnings: [], freePort: 4443 }],
          ]}
        >
          <NodeDetail
            data={
              {
                node: baseNode({
                  reason: { code: "inbound_failed", params: { inbound: "inb_2", profile: "hy2 · WARP · 8443", error: "listen udp :8443: bind: address already in use", failed: "1", total: "3" } },
                }),
                inbounds: [
                  inbound({ certNotAfterUnix: now + 5 * 86400 }),
                  inbound({ id: "inb_2", profileId: "prf_warp", profileName: "hy2 · WARP · 8443", port: 8443, egress: "warp", state: InboundState.FAILED, certNotAfterUnix: 0, lastError: "listen udp :8443: bind: address already in use" }),
                  inbound({ id: "inb_3", profileId: "prf_awg", profileName: "AWG 3.1 · QUIC", protocol: "awg", port: 51820, tlsServerName: "", certNotAfterUnix: 0, awg: { backend: "kernel", backendVersion: "amneziawg kernel module", ifaceUp: true, peers: 3, peersOnline: 1, peersHandshaken: 3, newestHandshakeUnix: now - 30, udpRxPackets: 900 } }),
                ],
                metrics,
                facts,
              } as never
            }
          />
        </Mock>
      </Demo>

      <Demo title="Add a profile: IP node and a Let's Encrypt profile / its port taken / a WARP exit without WARP / no profile in the panel">
        <div className="flex flex-wrap gap-2">
          <AddDemo
            label="IP node, Let's Encrypt"
            node={{ id: "nod_ip", name: "ip1", address: "203.0.113.10", warp: { state: WarpState.NOT_CONFIGURED } }}
            list={[profiles[2]!, profiles[0]!]}
            checks={[
              [check("nod_ip", "prf_new", "", ""), { of: "nod_ip/prf_new", state: "refused", code: "acme_needs_domain", vars: { address: "203.0.113.10", node: "ip1" } }],
              [check("nod_ip", "prf_main", "", ""), { of: "nod_ip/prf_main", state: "refused", code: "acme_needs_domain", vars: { address: "203.0.113.10", node: "ip1" } }],
            ]}
          />
          <AddDemo
            label="Port 443 taken"
            node={{}}
            inbounds={[inbound({})]}
            list={[profiles[2]!]}
            checks={[
              [check("nod_de1", "prf_new", "", ""), { of: "nod_de1/prf_new", state: "refused", code: "port_taken", vars: { port: "443", profile: "hy2 · 443 · Salamander", node: "de1", free: "8443" } }],
              [check("nod_de1", "prf_new", "8443", ""), { of: "nod_de1/prf_new", state: "ok", inbound: { port: 8443, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [], freePort: 4443 }],
            ]}
          />
          <AddDemo
            label="WARP exit, no WARP"
            node={{ warp: { state: WarpState.NOT_CONFIGURED } }}
            list={[profiles[1]!]}
            checks={[[check("nod_de1", "prf_warp", "", ""), { of: "nod_de1/prf_warp", state: "ok", inbound: { port: 8443, tlsServerName: "de1.example.com", nodeName: "de1" }, warnings: [{ code: "warp_missing", params: { state: "none" } }], freePort: 4443 }]]}
          />
          <AddDemo label="No profile in the panel (IP node)" node={{ id: "nod_ip", name: "ip1", address: "203.0.113.10" }} list={[]} checks={[]} />
        </div>
      </Demo>

      <Demo title="Health → Checks: failed is red, off is grey, no link is grey-blue; the legend; the phone gets a card per node">
        <Mock fill={[]}>
          <ChecksTab data={matrix as never} />
        </Mock>
      </Demo>
    </div>
  );
}
