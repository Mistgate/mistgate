import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Switch } from "@/components/ui/switch";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { profiles as profilesApi, relays as relaysApi } from "@/lib/api";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, doctorQuery } from "@/lib/health";
import { nodesQuery, nodeQuery } from "@/lib/queries";
import { candidatesQuery, relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { FleetMap } from "@/screens/nodes-map";

// Development only, /dev-kit?only=graph&demo=fleet: the Nodes page's fleet map on an 11-node fleet like the owner's, on a private
// query cache (the relay calls faked here). de, de2, nl, be, tr, ro, ru, ru2, mass, tll and a fresh node; ru and ru2 are entries
// relaying to be and tr. &role=helper is the read-only view, &focus=<id> selects a node (be, ru, ...).

const now = Math.floor(Date.now() / 1000);
const min = 60;
const id = (n: string) => `nod_${n}`;

const spec: [string, string, string, string, string[], number, number | null, NodeStatus, WarpState][] = [
  // name, country, location, address, protocols, people online, cpu, status, warp
  ["de", "DE", "Falkenstein", "203.0.113.11", ["hysteria2", "vless", "awg"], 34, 21, NodeStatus.ONLINE, WarpState.UP],
  ["de2", "DE", "Nuremberg", "203.0.113.12", ["hysteria2", "vless"], 12, 64, NodeStatus.ONLINE, WarpState.NOT_CONFIGURED],
  ["nl", "NL", "Amsterdam", "203.0.113.13", ["hysteria2", "vless", "mtproto"], 27, 33, NodeStatus.ONLINE, WarpState.UP],
  ["be", "BE", "Brussels", "203.0.113.40", ["hysteria2", "vless", "awg"], 19, 18, NodeStatus.ONLINE, WarpState.NOT_CONFIGURED],
  ["tr", "TR", "Istanbul", "203.0.113.41", ["hysteria2", "vless"], 8, 41, NodeStatus.ONLINE, WarpState.NOT_CONFIGURED],
  ["ro", "RO", "Bucharest", "203.0.113.42", ["vless"], 3, 12, NodeStatus.BLIP, WarpState.NOT_CONFIGURED],
  ["ru", "RU", "Moscow", "198.51.100.20", [], 0, 9, NodeStatus.ONLINE, WarpState.NOT_CONFIGURED],
  ["ru2", "RU", "Saint Petersburg", "198.51.100.21", [], 0, 7, NodeStatus.ONLINE, WarpState.NOT_CONFIGURED],
  ["mass", "FI", "Helsinki", "203.0.113.43", ["hysteria2", "vless", "awg"], 41, 72, NodeStatus.ONLINE, WarpState.UP],
  ["tll", "EE", "Tallinn", "203.0.113.44", ["hysteria2"], 0, null, NodeStatus.DOWN, WarpState.NOT_CONFIGURED],
  ["fresh", "FR", "Paris", "203.0.113.45", [], 0, null, NodeStatus.PENDING, WarpState.NOT_CONFIGURED],
];
const nodes = spec.map(([name, cc, location, address, protocols, people, cpu, status, warp]) => ({
  id: id(name), name, countryCode: cc, location, provider: "", address, status, protocols, online: people ? [{ protocol: protocols[0] ?? "vless", users: people }] : [], hasMetrics: cpu !== null, cpuPct: cpu ?? 0, warp: { state: warp }, ipv6: { entry: "", entrySupported: false, unavailableReason: "no_ipv6", addresses: [], src: "" },
}));

const net = (p: string) => (p === "hysteria2" || p === "awg" ? "udp" : "tcp");
const port = (p: string) => (p === "hysteria2" ? 443 : p === "awg" ? 14491 : p === "mtproto" ? 8888 : 8443);
const inboundsOf = (name: string, protocols: string[]) =>
  protocols.map((p) => ({ id: `${name}_${p}`, profileId: `prf_${name}_${p}`, profileName: p === "hysteria2" ? "HY2" : p === "vless" ? "VLESS TCP" : p === "awg" ? "AWG" : "MTProto", protocol: p, port: port(p), state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, nodeId: id(name), nodeName: name }));

const relay = (rid: string, via: string, exit: string, p: string, over: object = {}) => ({
  id: rid, inboundId: `${exit}_${p}`, exitNodeId: id(exit), exitNodeName: exit, profileId: `prf_${exit}_${p}`, profileName: p === "hysteria2" ? "HY2" : p === "vless" ? "VLESS TCP" : "AWG", protocol: p, viaNodeId: id(via), viaNodeName: via,
  network: net(p), port: port(p), exitPort: port(p), enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.40", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200, bytes: 0, packets: 0, countedUnix: 0, ...over,
});
const initialRelays = () => [
  relay("r1", "ru", "be", "hysteria2"),
  relay("r2", "ru", "be", "vless"),
  relay("r3", "ru", "tr", "hysteria2"),
  relay("r4", "ru2", "be", "hysteria2"),
  relay("r5", "ru2", "be", "awg", { enabled: false, state: InboundState.PENDING }),
  relay("r6", "ru2", "tr", "vless", { state: InboundState.FAILED, lastError: "forward_drop" }),
];

const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 10 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = []) => ({
  nodeId, country: "RU", verdict, atUnix: now - 10 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...dark.map((n) => vantage(n, "dark")), vantage("Rostelecom", "reach")], sourceVerdicts: [],
});
const checks = [
  country(id("de"), CountryVerdict.OK), country(id("de2"), CountryVerdict.PARTIAL, ["MTS"]), country(id("nl"), CountryVerdict.OK), country(id("be"), CountryVerdict.BLOCKED, ["MTS", "Beeline"]),
  country(id("tr"), CountryVerdict.BLOCKED, ["MTS", "Beeline"]), country(id("ro"), CountryVerdict.PARTIAL, ["MTS"]), country(id("ru"), CountryVerdict.OK), country(id("ru2"), CountryVerdict.OK),
  country(id("mass"), CountryVerdict.PARTIAL, ["Beeline"]), country(id("tll"), CountryVerdict.BLOCKED, ["MTS"]),
];

let fake = initialRelays();
function fakeCalls() {
  const wait = () => new Promise((r) => setTimeout(r, 700));
  const api = relaysApi as unknown as Record<string, unknown>;
  api.listRelays = async (r: { nodeId: string }) => ({ relays: fake.filter((x) => !r.nodeId || x.viaNodeId === r.nodeId || x.exitNodeId === r.nodeId) });
  api.checkRelay = async (r: { id: string }) => {
    await wait();
    fake = fake.map((x) => (x.id === r.id ? { ...x, check: { ok: true, atUnix: now, leg: { status: "ok" }, path: { status: "ok" } } as never } : x));
    return { relay: fake.find((x) => x.id === r.id) };
  };
  api.updateRelay = async (r: { id: string; enabled?: boolean; port?: number }) => {
    await wait();
    fake = fake.map((x) => (x.id === r.id ? { ...x, ...(r.enabled !== undefined ? { enabled: r.enabled, state: InboundState.ACTIVE, check: undefined as never } : {}), ...(r.port ? { port: r.port } : {}) } : x));
    return { relay: fake.find((x) => x.id === r.id) };
  };
  api.deleteRelay = async (r: { id: string }) => {
    await wait();
    fake = fake.filter((x) => x.id !== r.id);
    return {};
  };
  api.createRelay = async (r: { inboundId: string; viaNodeId: string; port: number }) => {
    await wait();
    const [exit, p] = r.inboundId.split("_") as [string, string];
    const via = nodes.find((n) => n.id === r.viaNodeId)!;
    const made = relay(`rn${fake.length + 1}`, via.name, exit, p, { enabled: false, state: InboundState.PENDING });
    fake = [...fake, made];
    return { relay: made };
  };
  (profilesApi as unknown as Record<string, unknown>).updateInbound = async () => {
    await wait();
    return { warnings: [] };
  };
}

function Mock({ role, children }: { role: Role; children: ReactNode }) {
  const [qc] = useState(() => {
    fake = initialRelays();
    fakeCalls();
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(nodesQuery.queryKey, { nodes } as never);
    c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
    c.setQueryData(checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: checks } as never);
    for (const n of nodes) {
      c.setQueryData(nodeQuery(n.id).queryKey, { node: n, inbounds: inboundsOf(n.name, n.protocols), portChecks: [], tcpChecks: [], facts: { hasIpv6: false } } as never);
      c.setQueryData(checksQuery(n.id).queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: checks.filter((x) => x.nodeId === n.id) } as never);
      c.setQueryData(doctorQuery(n.id).queryKey, { nowUnix: now, nodes: [{ nodeId: n.id, items: [{ id: "ipv6", status: 1, params: { ipv6: "no" } }] }] } as never);
      c.setQueryData(candidatesQuery(n.id).queryKey, {
        agentSupported: true,
        entryCountry: { verdict: "ok", dark: [], atUnix: now },
        candidates: nodes.filter((o) => o.id !== n.id).flatMap((o) => inboundsOf(o.name, o.protocols).map((i) => ({ inboundId: i.id, nodeId: o.id, nodeName: o.name, profileId: i.profileId, profileName: i.profileName, protocol: i.protocol, network: net(i.protocol), port: i.port, unsupported: i.protocol === "awg" ? "awg" : "", hop: false, relayId: "", country: { verdict: "blocked", dark: ["MTS"], atUnix: now }, reach: { verdict: "reach", source: "tspu_row", atUnix: now } }))),
      } as never);
    }
    return c;
  });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  qc.setQueryData(relaysQuery("").queryKey, role === Role.OWNER ? (fake as never) : ([] as never));
  for (const n of nodes) qc.setQueryData(relaysQuery(n.id).queryKey, role === Role.OWNER ? (fake.filter((x) => x.viaNodeId === n.id || x.exitNodeId === n.id) as never) : ([] as never));
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  );
}

export function FleetShowcase() {
  const q = new URLSearchParams(location.search);
  const [owner, setOwner] = useState(q.get("role") !== "helper");
  const [focus, setFocus] = useState<string | undefined>(q.get("focus") ? id(q.get("focus")!) : undefined);
  const [opened, setOpened] = useState<string | null>(null);
  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-4 text-[13px] text-muted">
        <label className="flex items-center gap-2">
          Owner
          <Switch checked={owner} onCheckedChange={setOwner} aria-label="Owner" />
        </label>
        {opened && <span data-testid="opened">opened the node page of {opened} (Ways in, route map)</span>}
      </div>
      <Mock key={String(owner)} role={owner ? Role.OWNER : Role.HELPER}>
        <FleetMap nodes={nodes as never} owner={owner} focus={focus} onFocus={setFocus} onOpenNode={(n) => setOpened(nodes.find((x) => x.id === n)?.name ?? n)} />
      </Mock>
    </div>
  );
}
