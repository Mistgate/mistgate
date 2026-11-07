import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { profiles as profilesApi, relays as relaysApi } from "@/lib/api";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, doctorQuery } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { candidatesQuery, relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import type { WayId } from "@/lib/ways";
import { useWays } from "@/screens/node/ways";
import { WaysGraph } from "@/screens/node/ways-graph";
import { profileListQuery } from "@/screens/users/rpc";

// Development only, /dev-kit?only=graph&demo=ways: the real WaysGraph (the route map) of the node page on made-up data (private
// query cache, the relay and subscription calls faked here). &scenario=be (default: direct blocked in Russia, three relays
// through ru2 working, IPv6 per &v6=), entry (an entry node forwarding to be and de1), cdn (CDN and WARP servers, one relay
// failing); &extra=2 puts spb1 and de1 on the map as entries without a relay; &role=helper is the read-only view.

const now = Math.floor(Date.now() / 1000);
const min = 60;

const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 10 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = [], over: object = {}) => ({
  nodeId, country: "RU", verdict, atUnix: now - 10 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...dark.map((n) => vantage(n, "dark")), vantage("Rostelecom", "reach")], sourceVerdicts: [], ...over,
});

type V6 = "on" | "can" | "none" | "broken" | "down";
const v6Of = (s: V6) => ({
  entry: s === "on" || s === "down" ? "2001:db8:41::7" : "",
  entrySupported: s === "can" || s === "on" || s === "down",
  unavailableReason: s === "none" ? "no_ipv6" : s === "broken" || s === "down" ? "ipv6_broken" : "",
  addresses: s === "none" ? [] : [{ address: "2001:db8:41::7", prefixLen: 64 }],
  src: "2001:db8:41::7",
});
const factOf = (s: V6) => (s === "none" ? { ipv6: "no" } : s === "broken" || s === "down" ? { ipv6: "yes", connect: "failed" } : { ipv6: "yes", connect: "ok" });

type Scenario = "be" | "entry" | "cdn";
type Ib = ReturnType<typeof ib>;
const ib = (nodeId: string, id: string, profileName: string, protocol: string, port: number, over: object = {}) => ({
  id, profileId: `prf_${id}`, profileName, protocol, port, state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, nodeId, nodeName: nodeId.slice(4), ...over,
});

const relay = (over: object) => ({
  id: "r1", inboundId: "hy2", exitNodeId: "nod_be", exitNodeName: "be", profileId: "prf_hy2", profileName: "HY2", protocol: "hysteria2", viaNodeId: "nod_ru2", viaNodeName: "ru2", network: "udp", port: 443, exitPort: 443,
  enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.40", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200, bytes: 0, packets: 0, countedUnix: 0, ...over,
});
const tcp = { network: "tcp" };

function scenarioOf(s: Scenario) {
  if (s === "entry") {
    const own = [ib("nod_ru2", "own1", "VLESS TCP", "vless", 443), ib("nod_ru2", "own2", "HY2", "hysteria2", 8443)];
    return {
      nodeId: "nod_ru2",
      name: "ru2",
      cc: "RU",
      address: "198.51.100.20",
      inbounds: own,
      verdict: CountryVerdict.OK,
      relays: [
        relay({ id: "o1", viaNodeId: "nod_ru2", viaNodeName: "ru2", exitNodeId: "nod_be", exitNodeName: "be" }),
        relay({ id: "o2", inboundId: "vless", profileName: "VLESS TCP", ...tcp, port: 8443, viaNodeId: "nod_ru2", viaNodeName: "ru2", exitNodeId: "nod_be", exitNodeName: "be" }),
        relay({ id: "o3", inboundId: "x1", profileName: "Trojan", ...tcp, port: 2053, viaNodeId: "nod_ru2", viaNodeName: "ru2", exitNodeId: "nod_de1", exitNodeName: "de1", state: InboundState.FAILED, lastError: "forward_drop" }),
        relay({ id: "o4", inboundId: "x2", profileName: "VLESS XHTTP", ...tcp, port: 4443, viaNodeId: "nod_ru2", viaNodeName: "ru2", exitNodeId: "nod_de1", exitNodeName: "de1" }),
      ],
    };
  }
  if (s === "cdn") {
    return {
      nodeId: "nod_be",
      name: "be",
      cc: "BE",
      address: "203.0.113.40",
      inbounds: [
        ib("nod_be", "hy2", "HY2", "hysteria2", 443),
        ib("nod_be", "vless", "VLESS TCP", "vless", 8443, { egress: "warp" }),
        ib("nod_be", "xhttp", "VLESS XHTTP", "vless", 4443, { egress: "warp" }),
        ib("nod_be", "cdn1", "VLESS CDN", "vless", 2053, { cdn: true }),
        ib("nod_be", "cdn2", "Trojan CDN", "vless", 2083, { cdn: true }),
        ib("nod_be", "mt", "MTProto", "mtproto", 443, { directInSubscriptions: false }),
      ],
      verdict: CountryVerdict.PARTIAL,
      relays: [relay({}), relay({ id: "r2", inboundId: "vless", profileName: "VLESS TCP", ...tcp, port: 8443, viaNodeId: "nod_ru3", viaNodeName: "ru3", state: InboundState.FAILED, lastError: "forward_drop" })],
    };
  }
  return {
    nodeId: "nod_be",
    name: "be",
    cc: "BE",
    address: "203.0.113.40",
    inbounds: [
      ib("nod_be", "hy2", "HY2", "hysteria2", 443),
      ib("nod_be", "vless", "VLESS TCP", "vless", 8443),
      ib("nod_be", "xhttp", "VLESS XHTTP", "vless", 4443),
      ib("nod_be", "mt", "MTProto", "mtproto", 443, { directInSubscriptions: false }),
      ib("nod_be", "awg", "AWG2", "awg", 14491),
      ib("nod_be", "awg3", "AWG3.1", "awg", 14492),
      ib("nod_be", "trj", "Trojan", "vless", 9443, { enabled: false, state: InboundState.DISABLED }),
    ],
    verdict: CountryVerdict.BLOCKED,
    relays: [
      relay({}),
      relay({ id: "r2", inboundId: "vless", profileName: "VLESS TCP", ...tcp, port: 8443, exitPort: 8443 }),
      relay({ id: "r3", inboundId: "xhttp", profileName: "VLESS XHTTP", ...tcp, port: 4443, exitPort: 4443 }),
    ],
  };
}

const fleet = [
  { id: "nod_be", name: "be", countryCode: "BE", status: NodeStatus.ONLINE },
  { id: "nod_ru2", name: "ru2", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_ru3", name: "ru3", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_spb1", name: "spb1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
];
const cand = (inb: Ib) => ({
  inboundId: inb.id, nodeId: inb.nodeId, nodeName: inb.nodeName, profileId: inb.profileId, profileName: inb.profileName, protocol: inb.protocol, network: inb.protocol === "hysteria2" || inb.protocol === "awg" ? "udp" : "tcp", port: inb.port,
  unsupported: inb.protocol === "awg" ? "awg" : "", hop: false, relayId: "", country: { verdict: "blocked", dark: ["MTS", "Beeline"], atUnix: now }, reach: { verdict: "reach", source: "tspu_row", atUnix: now },
});

let fakeRelays: ReturnType<typeof relay>[] = [];
function fakeCalls() {
  const wait = () => new Promise((r) => setTimeout(r, 900));
  const api = relaysApi as unknown as Record<string, unknown>;
  api.listRelays = async () => ({ relays: fakeRelays });
  api.checkRelay = async (r: { id: string }) => {
    await wait();
    fakeRelays = fakeRelays.map((x) => (x.id === r.id ? { ...x, check: { ok: true, atUnix: now, leg: { status: "ok" }, path: { status: "ok" } } as never } : x));
    return { relay: fakeRelays.find((x) => x.id === r.id) };
  };
  api.updateRelay = async (r: { id: string; enabled?: boolean; port?: number }) => {
    await wait();
    fakeRelays = fakeRelays.map((x) => (x.id === r.id ? { ...x, ...(r.enabled !== undefined ? { enabled: r.enabled, state: InboundState.ACTIVE, check: undefined as never } : {}), ...(r.port ? { port: r.port } : {}) } : x));
    return { relay: fakeRelays.find((x) => x.id === r.id) };
  };
  api.deleteRelay = async (r: { id: string }) => {
    await wait();
    fakeRelays = fakeRelays.filter((x) => x.id !== r.id);
    return {};
  };
  (profilesApi as unknown as Record<string, unknown>).updateInbound = async () => {
    await wait();
    return { warnings: [] };
  };
}

function Mock({ v6, role, scenario, children }: { v6: V6; role: Role; scenario: Scenario; children: ReactNode }) {
  const sc = scenarioOf(scenario);
  const [qc] = useState(() => {
    fakeRelays = sc.relays;
    fakeCalls();
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(nodesQuery.queryKey, { nodes: fleet } as never);
    c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
    c.setQueryData(profileListQuery.queryKey, sc.inbounds.map((i) => ({ id: i.profileId, name: i.profileName, protocol: i.protocol, version: 1 })) as never);
    const own = sc.inbounds.filter((i) => !i.cdn && i.enabled);
    for (const [id, verdict, dark] of [["nod_ru2", "ok", []], ["nod_ru3", "partial", ["MTS"]], ["nod_spb1", "ok", []], ["nod_de1", "ok", []]] as const) {
      c.setQueryData(candidatesQuery(id).queryKey, { agentSupported: true, entryCountry: { verdict, dark, atUnix: now }, candidates: own.map(cand) } as never);
    }
    return c;
  });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  qc.setQueryData(relaysQuery(sc.nodeId).queryKey, role === Role.OWNER ? (fakeRelays as never) : ([] as never));
  const own = country(sc.nodeId, sc.verdict, sc.verdict === CountryVerdict.BLOCKED ? ["MTS", "Beeline"] : sc.verdict === CountryVerdict.PARTIAL ? ["MTS"] : [], { ipv6: country(sc.nodeId, CountryVerdict.OK) });
  qc.setQueryData(checksQuery(sc.nodeId).queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [own] } as never);
  qc.setQueryData(checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [own, country("nod_ru2", CountryVerdict.OK), country("nod_ru3", CountryVerdict.PARTIAL, ["MTS"])] } as never);
  qc.setQueryData(doctorQuery(sc.nodeId).queryKey, { nowUnix: now, nodes: [{ nodeId: sc.nodeId, items: [{ id: "ipv6", status: 1, params: factOf(v6) }] }] } as never);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  );
}

function Screen({ v6, scenario }: { v6: V6; scenario: Scenario }) {
  const sc = scenarioOf(scenario);
  const q = new URLSearchParams(location.search);
  const [open, setOpen] = useState<WayId>("direct");
  const [full, setFull] = useState(q.get("full") === "1");
  const extra = q.get("extra") === "2" ? [{ id: "nod_spb1", name: "spb1" }, { id: "nod_de1", name: "de1" }] : [];
  const data = {
    node: { id: sc.nodeId, name: sc.name, countryCode: sc.cc, address: sc.address, status: NodeStatus.ONLINE, bandwidthMbps: 0, ipv6: v6Of(v6), warp: { state: WarpState.NOT_CONFIGURED } },
    inbounds: sc.inbounds,
    portChecks: [],
    tcpChecks: [],
    facts: { hasIpv6: v6 !== "none" },
  } as never;
  const w = useWays(data);
  return (
    <WaysGraph
      data={data}
      w={w}
      open={open}
      onOpen={setOpen}
      onIpv6={() => undefined}
      full={full}
      onFull={setFull}
      initialExtra={extra}
      detail={
        <div className="rounded-card border border-line bg-surface p-4 text-[13px] text-muted">
          <p className="mb-1 text-[14px] font-semibold text-fg">{open}</p>
          The way&apos;s detail card (ports, checks, the country row) opens here: below the graph, or in a drawer on the right in full screen.
        </div>
      }
    />
  );
}

export function WaysShowcase() {
  const q = new URLSearchParams(location.search);
  const [v6, setV6] = useState<V6>(() => (q.get("v6") as V6) || "broken");
  const [owner, setOwner] = useState(q.get("role") !== "helper");
  const [scenario, setScenario] = useState<Scenario>(() => (q.get("scenario") as Scenario) || "be");
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-4">
        <Segmented
          variant="flat"
          aria-label="Scenario"
          value={scenario}
          onValueChange={setScenario}
          options={[
            { value: "be", label: "be: blocked, via ru2" },
            { value: "cdn", label: "CDN + WARP" },
            { value: "entry", label: "entry node" },
          ]}
        />
        <label className="flex items-center gap-2 text-[13px] text-muted">
          IPv6
          <Segmented
            variant="flat"
            aria-label="IPv6 state"
            value={v6}
            onValueChange={setV6}
            options={[
              { value: "on", label: "works" },
              { value: "can", label: "can turn on" },
              { value: "none", label: "none here" },
              { value: "broken", label: "broken" },
              { value: "down", label: "on, broken" },
            ]}
          />
        </label>
        <label className="flex items-center gap-2 text-[13px] text-muted">
          Owner
          <Switch checked={owner} onCheckedChange={setOwner} aria-label="Owner" />
        </label>
      </div>
      <Mock key={`${owner}/${scenario}`} v6={v6} role={owner ? Role.OWNER : Role.HELPER} scenario={scenario}>
        <Screen v6={v6} scenario={scenario} />
      </Mock>
    </div>
  );
}
