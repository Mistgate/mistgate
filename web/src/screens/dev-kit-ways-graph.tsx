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
import { profileListQuery } from "@/screens/users/rpc";
import { WaysGraph } from "@/screens/node/ways-graph";
import { useWays } from "@/screens/node/ways";
import type { WayId } from "@/lib/ways";

// Development only, /dev-kit?only=graph&demo=ways: the real WaysGraph of the node page on made-up data (private query cache,
// the relay and subscription calls faked here). The node "be" is cut off in Russia on every direct way; ru2 carries HY2 over
// a working relay, ru3's relay is failing, spb1 is a node that is not an entry yet ("Add entry node"). Servers cover every
// state: blocked, works (through a CDN), kept out of subscriptions, off, not relayable (AWG, CDN). The IPv6 row has every
// state in the selector.

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

const ib = (id: string, profileName: string, protocol: string, port: number, over: object = {}) => ({
  id, profileId: `prf_${id}`, profileName, protocol, port, state: InboundState.ACTIVE, enabled: true, egress: "direct", cdn: false, directInSubscriptions: true, nodeId: "nod_be", nodeName: "be", ...over,
});
const inbounds = [
  ib("hy2", "HY2", "hysteria2", 443),
  ib("awg", "AWG", "awg", 14491),
  ib("xhttp", "VLESS XHTTP", "vless", 4443),
  ib("vless", "VLESS TCP", "vless", 8443, { egress: "warp" }),
  ib("cdn", "VLESS CDN", "vless", 2053, { cdn: true }),
  ib("mt", "MTProto", "mtproto", 443, { directInSubscriptions: false }),
  ib("trj", "Trojan", "vless", 9443, { enabled: false, state: InboundState.DISABLED }),
];

const relay = (over: object) => ({
  id: "r1", inboundId: "hy2", exitNodeId: "nod_be", exitNodeName: "be", profileId: "prf_hy2", profileName: "HY2", protocol: "hysteria2", viaNodeId: "nod_ru2", viaNodeName: "ru2", network: "udp", port: 443, exitPort: 443,
  enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "203.0.113.40", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 7200, bytes: 0, packets: 0, countedUnix: 0, ...over,
});
const fixtureRelays = () => [
  relay({}),
  relay({ id: "r2", inboundId: "vless", profileName: "VLESS TCP", protocol: "vless", network: "tcp", port: 8443, exitPort: 8443, viaNodeId: "nod_ru3", viaNodeName: "ru3", state: InboundState.FAILED, lastError: "forward_drop" }),
  relay({ id: "r3", inboundId: "xhttp", profileName: "VLESS XHTTP", protocol: "vless", network: "tcp", port: 4443, exitPort: 4443, enabled: false, state: InboundState.PENDING }),
];

const fleet = [
  { id: "nod_be", name: "be", countryCode: "BE", status: NodeStatus.ONLINE },
  { id: "nod_ru2", name: "ru2", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_ru3", name: "ru3", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_spb1", name: "spb1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
];
const cand = (inb: (typeof inbounds)[number]) => ({
  inboundId: inb.id, nodeId: "nod_be", nodeName: "be", profileId: inb.profileId, profileName: inb.profileName, protocol: inb.protocol, network: inb.protocol === "hysteria2" || inb.protocol === "awg" ? "udp" : "tcp", port: inb.port,
  unsupported: inb.protocol === "awg" ? "awg" : "", hop: false, relayId: "", country: { verdict: "blocked", dark: ["MTS", "Beeline"], atUnix: now }, reach: { verdict: "reach", source: "tspu_row", atUnix: now },
});

let fakeRelays = fixtureRelays();
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

function Mock({ v6, role, children }: { v6: V6; role: Role; children: ReactNode }) {
  const [qc] = useState(() => {
    fakeRelays = fixtureRelays();
    fakeCalls();
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(nodesQuery.queryKey, { nodes: fleet } as never);
    c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
    c.setQueryData(profileListQuery.queryKey, inbounds.map((i) => ({ id: i.profileId, name: i.profileName, protocol: i.protocol, version: 1 })) as never);
    c.setQueryData(candidatesQuery("nod_ru2").queryKey, { agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates: inbounds.filter((i) => !i.cdn && i.enabled).map(cand) } as never);
    c.setQueryData(candidatesQuery("nod_ru3").queryKey, { agentSupported: true, entryCountry: { verdict: "partial", dark: ["MTS"], atUnix: now }, candidates: inbounds.filter((i) => !i.cdn && i.enabled).map(cand) } as never);
    c.setQueryData(candidatesQuery("nod_spb1").queryKey, { agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: now }, candidates: inbounds.filter((i) => !i.cdn && i.enabled).map(cand) } as never);
    return c;
  });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role } } as never);
  qc.setQueryData(relaysQuery("nod_be").queryKey, fakeRelays as never);
  const be = country("nod_be", CountryVerdict.BLOCKED, ["MTS", "Beeline"], { ipv6: country("nod_be", CountryVerdict.OK) });
  qc.setQueryData(checksQuery("nod_be").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [be] } as never);
  qc.setQueryData(checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: [be, country("nod_ru2", CountryVerdict.OK), country("nod_ru3", CountryVerdict.PARTIAL, ["MTS"])] } as never);
  qc.setQueryData(doctorQuery("nod_be").queryKey, { nowUnix: now, nodes: [{ nodeId: "nod_be", items: [{ id: "ipv6", status: 1, params: factOf(v6) }] }] } as never);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  );
}

function Screen({ v6 }: { v6: V6 }) {
  const [open, setOpen] = useState<WayId>("direct");
  const [full, setFull] = useState(new URLSearchParams(location.search).get("full") === "1");
  const extra = new URLSearchParams(location.search).get("extra") === "0" ? [] : [{ id: "nod_spb1", name: "spb1" }, { id: "nod_de1", name: "de1" }];
  const data = {
    node: { id: "nod_be", name: "be", countryCode: "BE", address: "203.0.113.40", status: NodeStatus.ONLINE, bandwidthMbps: 0, ipv6: v6Of(v6), warp: { state: WarpState.UP, colo: "AMS" } },
    inbounds,
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
  const [v6, setV6] = useState<V6>(() => (new URLSearchParams(location.search).get("v6") as V6) || "can");
  const [owner, setOwner] = useState(new URLSearchParams(location.search).get("role") !== "helper");
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-4">
        <label className="flex items-center gap-2 text-[13px] text-muted">
          IPv6 row
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
      <Mock key={String(owner)} v6={v6} role={owner ? Role.OWNER : Role.HELPER}>
        <Screen v6={v6} />
      </Mock>
    </div>
  );
}
