import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind, AlertSeverity, CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { nodes as nodesApi } from "@/lib/api";
import { countryQuery } from "@/lib/globalping";
import { alertsQuery, checksQuery, doctorQuery, type Alert } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { AlertCard } from "@/screens/health/alerts";
import { useFixFlow } from "@/screens/health/fix";
import { useWays, WaysTab } from "@/screens/node/ways";
import { NodesTable } from "@/screens/reachability/nodes";

// Development only: the IPv6 entry (ipv6 plan §3.10) in every state, on private query caches, for its screenshots.
// /dev-kit?only=ipv6&way=ipv6 opens the IPv6 lane of every node below. &add=ipv6&dlg=pick|checking|failed shows the node that
// is off alone, with the entry's dialog open and the panel's answers faked here: "checking" never answers, "failed" says that
// tcp/443 does not answer.

const now = Math.floor(Date.now() / 1000);
const min = 60;
type Fill = [readonly unknown[], unknown];

const v6 = (over: object = {}) => ({
  entry: "",
  entrySupported: true,
  unavailableReason: "",
  addresses: [
    { address: "2001:db8:41:7::1", prefixLen: 64 },
    { address: "2001:db8:41:7::a5", prefixLen: 64 },
  ],
  src: "2001:db8:41:7::1",
  ...over,
});
const passed = (over: object = {}) => ({
  address: "2001:db8:41:7::1",
  passed: true,
  errorCode: "",
  verdicts: [
    { network: "tcp", port: 443, verdict: "ours" },
    { network: "udp", port: 443, verdict: "ok" },
    { network: "udp", port: 8443, verdict: "ok" },
  ],
  sender: "fi1",
  checkedUnix: now - 12 * min,
  ...over,
});
const failed = passed({
  passed: false,
  errorCode: "check_failed",
  checkedUnix: now - 3 * min,
  verdicts: [
    { network: "tcp", port: 443, verdict: "timeout" },
    { network: "udp", port: 443, verdict: "broken" },
    { network: "udp", port: 8443, verdict: "ok" },
  ],
});

const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 10 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = [], over: object = {}) => ({
  nodeId,
  country: "RU",
  verdict,
  atUnix: now - 10 * min,
  probes: [],
  reason: "",
  eyeball: true,
  confidence: "eyeball",
  listedBy: [],
  cdnProvider: "",
  cdnRegion: "",
  sources: ["globalping"],
  globalpingVerdict: verdict,
  tspuVerdict: CountryVerdict.UNSPECIFIED,
  tspuPorts: [],
  dpiHop: 0,
  tspuVantageId: "",
  tspuVantageName: "",
  tspuAtUnix: 0,
  noProbe: [],
  vantages: [...dark.map((n) => vantage(n, "dark")), vantage("Rostelecom", "reach"), vantage("Beeline", "reach")],
  sourceVerdicts: [],
  ...over,
});
// IPv4 dark on MTS, IPv6 seen everywhere: the case for the entry
const cut = (nodeId: string) => country(nodeId, CountryVerdict.PARTIAL, ["MTS"], { ipv6: country(nodeId, CountryVerdict.OK) });

const inbounds = [
  { id: "inb_v", profileId: "prf_v", profileName: "VLESS · REALITY", protocol: "vless", port: 443, state: InboundState.ACTIVE, nodeId: "nod_de1", nodeName: "de1" },
  { id: "inb_h", profileId: "prf_h", profileName: "hy2 · 443", protocol: "hysteria2", port: 443, state: InboundState.ACTIVE, nodeId: "nod_de1", nodeName: "de1" },
  { id: "inb_a", profileId: "prf_a", profileName: "AWG 3.1", protocol: "awg", port: 8443, state: InboundState.ACTIVE, nodeId: "nod_de1", nodeName: "de1" },
];
const node = (ipv6: object) => ({ id: "nod_de1", name: "de1", countryCode: "DE", address: "de1.example.com", status: NodeStatus.ONLINE, bandwidthMbps: 0, ipv6 });
const fleet = [
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE, ipv6: v6() },
  { id: "nod_fi1", name: "fi1", countryCode: "FI", status: NodeStatus.ONLINE, ipv6: v6() },
];

function Mock({ role = Role.OWNER, fill = [], children }: { role?: Role; fill?: Fill[]; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: fleet } as never);
    c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
    c.setQueryData(relaysQuery("nod_de1").queryKey, [] as never);
    c.setQueryData(doctorQuery("nod_de1").queryKey, { nowUnix: now, nodes: [{ nodeId: "nod_de1", items: [{ id: "ipv6", status: 1, params: { ipv6: "yes", connect: "ok" } }] }] } as never);
    c.setQueryData(alertsQuery.queryKey, { nowUnix: now, active: [], history: [] } as never);
    for (const [key, value] of fill) c.setQueryData(key, value as never);
    return c;
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function Tab({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysTab data={data} ways={w} />;
}

/** The node's Ways in tab with its IPv6 entry in one state, seen by `role`. */
function WaysDemo({ ipv6, check = cut("nod_de1"), role }: { ipv6: object; check?: object; role?: Role }) {
  const data = { node: node(ipv6), inbounds, portChecks: [], tcpChecks: [], facts: { hasIpv6: true } } as never;
  return (
    <Mock role={role} fill={[[checksQuery("nod_de1").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [check] }]]}>
      <Tab data={data} />
    </Mock>
  );
}

function Demo({ title, shot, children }: { title: string; shot: string; children: ReactNode }) {
  return (
    <section data-shot={shot} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      {children}
    </section>
  );
}

/** The panel's answers to the dialog, for its screenshots. */
function fakeAnswers(dlg: string) {
  const api = nodesApi as unknown as Record<string, unknown>;
  const later = <T,>(v: T, ms = 500) => new Promise<T>((r) => setTimeout(() => r(v), ms));
  api.updateIpv6Entry = () => (dlg === "checking" ? new Promise(() => {}) : later({ node: {}, errorCode: "check_failed", check: { ...failed, checkedUnix: now } }));
  api.checkIpv6 = () => (dlg === "checking" ? new Promise(() => {}) : later({ ...failed, errorCode: "", checkedUnix: now }));
}

// the Reachability row: fi1 cut over IPv4 where IPv6 gets through, entry off; de1 seen everywhere
const lane = (verdicts: [number, CountryVerdict][]) => ({ source: "globalping", points: verdicts.map(([atUnix, verdict]) => ({ atUnix, verdict })), untilUnix: now });
const since = now - 7 * 86400;
const reachChecks = new Map([
  ["nod_de1", country("nod_de1", CountryVerdict.OK, [], { ipv6: country("nod_de1", CountryVerdict.OK) })],
  ["nod_fi1", country("nod_fi1", CountryVerdict.TCP_BLOCKED, ["MTS", "MegaFon"], { ipv6: country("nod_fi1", CountryVerdict.OK) })],
]) as never;
const reachLanes = new Map([
  ["nod_de1", new Map([["globalping", lane([[since + 600, CountryVerdict.OK]])]])],
  [
    "nod_fi1",
    new Map([
      [
        "globalping",
        lane([
          [since + 600, CountryVerdict.OK],
          [now - 2 * 86400, CountryVerdict.TCP_BLOCKED],
        ]),
      ],
    ]),
  ],
]) as never;
const reachLanes6 = new Map([
  ["nod_de1", lane([[now - 3 * 86400, CountryVerdict.OK]])],
  ["nod_fi1", lane([[now - 3 * 86400, CountryVerdict.OK]])],
]) as never;

const countryAlert = (params: Record<string, string>): Alert =>
  ({
    id: `alt_${params.ipv6}`,
    severity: AlertSeverity.WARNING,
    kind: AlertKind.COUNTRY_BLOCKED,
    nodeId: "nod_de1",
    nodeName: "de1",
    subject: "partial",
    titleKey: "health.alert.country_blocked.title",
    whyKey: "",
    params: { country: "RU", verdict: "partial", networks: "MTS", ...params },
    firstSeenUnix: now - 5 * 3600,
    openedUnix: now - 5 * 3600,
    lastSeenUnix: now,
    resolvedAtUnix: 0,
    resolution: "",
    mutedUntilUnix: 0,
    actions: ["open_node", "mute"],
  }) as never;

function AlertDemo({ a }: { a: Alert }) {
  const flow = useFixFlow();
  return <AlertCard alert={a} now={now} flow={flow} />;
}

export function Ipv6Kit() {
  const dlg = new URLSearchParams(location.search).get("dlg") ?? "";
  // wider than the kit's column: about the content width of the shell at 1280 px
  const wide = "relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-8";
  if (dlg) {
    fakeAnswers(dlg);
    return (
      <div className={wide}>
        <Demo title="Off, with the entry's dialog open" shot="dialog">
          <WaysDemo ipv6={v6()} />
        </Demo>
      </div>
    );
  }
  return (
    <div className={wide}>
      <Demo title="Off: IPv4 dark on MTS, IPv6 seen everywhere (owner)" shot="off">
        <WaysDemo ipv6={v6()} />
      </Demo>
      <Demo title="On, checked by another node" shot="on">
        <WaysDemo ipv6={v6({ entry: "2001:db8:41:7::1", lastCheck: passed() })} />
      </Demo>
      <Demo title="On, checked by Globalping only" shot="on-globalping">
        <WaysDemo ipv6={v6({ entry: "2001:db8:41:7::1", lastCheck: passed({ sender: "globalping", verdicts: [{ network: "tcp", port: 443, verdict: "reachable" }] }) })} />
      </Demo>
      <Demo title="Off, the last check failed" shot="failed">
        <WaysDemo ipv6={v6({ lastCheck: failed })} check={country("nod_de1", CountryVerdict.OK)} />
      </Demo>
      <Demo title="Not available: IPv6 broken at the hoster" shot="unavailable">
        <WaysDemo ipv6={v6({ entrySupported: false, unavailableReason: "ipv6_broken" })} check={country("nod_de1", CountryVerdict.PARTIAL, ["MTS"])} />
      </Demo>
      <Demo title="Helper: the same node, nothing to press" shot="helper">
        <WaysDemo ipv6={v6({ lastCheck: failed })} role={Role.HELPER} />
      </Demo>
      <Demo title="Reachability: the hint under a node cut over IPv4" shot="reach">
        <Mock>
          <NodesTable
            nodes={fleet}
            checks={reachChecks}
            lanes={reachLanes}
            lanes6={reachLanes6}
            sources={["globalping"]}
            probeNode=""
            country="RU"
            since={since}
            now={now}
            owner
          />
        </Mock>
      </Demo>
      <Demo title="Alerts: IPv6 gets through (owner), and the entry is on" shot="alert">
        <Mock>
          <div className="flex flex-col gap-3.5">
            <AlertDemo a={countryAlert({ ipv6: "open" })} />
            <AlertDemo a={countryAlert({ ipv6: "entry" })} />
          </div>
        </Mock>
      </Demo>
    </div>
  );
}
