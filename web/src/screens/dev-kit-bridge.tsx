import { useEffect, type ReactNode } from "react";
import { Block, Card } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { setLang, isLang } from "@/i18n";
import { relays as relaysApi } from "@/lib/api";
import { countryQuery } from "@/lib/globalping";
import { checksQuery } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { candidatesQuery, relaysQuery, type RelayMsg } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { setTheme } from "@/lib/theme";
import { baseNode, facts, inbound, metrics, Mock, nodeFill } from "@/screens/dev-kit-node";
import { NodeDetail } from "@/screens/node/index";
import { AddRelayDialog, useRelayRuns } from "@/screens/node/relays";
import { RelayRows } from "@/screens/node/relay-rows";
import { ChangeWay, LinkViaLine } from "@/screens/node/relay-way";
import { settingsQuery } from "@/screens/subscriptions/queries";

// Development only, /dev-kit?only=bridge&state=…&tab=ways&way=via:nod_msk1&lang=ru&theme=light: the bridge of a relay (the
// reverse-relay plan §7-§8, R5), on made-up data and a private query cache (no server). Names: the exit de1, the entries msk1
// (RU) and spb1, the third node nl1. The scenes that are the exit's page need ?tab=ways&way=via:nod_msk1 (the lane through msk1).
//   direct        three relays in auto, all direct: the plain rows with the way chip
//   bridge        VLESS and MTProto on the bridge (the leg failed), Hysteria2 of the same pair stays direct
//   returning     on the bridge, 2 of 3 checks toward direct, with the speed results
//   near-full     the bridge at 190 of ~230 people
//   at-limit      pool 4, 468 of ~470: people turned away, a second entry offered
//   down          the exit does not hold the bridge
//   old-agent     the exit's agent has no bridge: the reason, the node to update
//   change        "Change" of a TCP relay (direct picked: the drop note)
//   udp           "Change" of a UDP relay: the bridge greyed with the reason
//   add           "Relay through msk1": notes under the servers, the way opened
//   check         the check window after "Check and turn on": direct fails, the bridge carries
//   link-via      the exit's page: the agent reaches the panel through msk1
//   reader        a helper's view: the state without the buttons
// Open each at 1440 and at 390 px, in both themes.

const now = Math.floor(Date.now() / 1000);
const min = 60;
const params = new URLSearchParams(location.search);
if (params.get("only") === "bridge") {
  // the screenshots pick the theme and the language by the address
  const th = params.get("theme");
  if (th === "light" || th === "dark") setTheme(th);
  const lg = params.get("lang");
  if (isLang(lg)) setLang(lg);
}

const nodes = [
  { id: "nod_msk1", name: "msk1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_spb1", name: "spb1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
  { id: "nod_nl1", name: "nl1", countryCode: "NL", status: NodeStatus.ONLINE },
];

// ---- the relays of the pair de1 through msk1 ----

const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "ours", sent: 0, got: 0, atUnix: now - 2 * min, mb: 0, mbitS: 0, bytes: 0, connectMs: 0, firstByteMs: 0, durationMs: 0, port: 0, ...over });
const legDown = { ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout", verdict: "timeout" }) };
const check = (port: number, over: Record<string, unknown> = {}) => ({
  atUnix: now - 2 * min, network: "tcp", port, to: "192.0.2.10:443", leg: stage(), path: stage({ senderId: "nod_nl1", senderName: "nl1" }), ok: true, stage: "", verified: true, ...over,
});
const bridge = (over: Record<string, unknown> = {}) => ({ connected: true, conns: 2, rttMs: 38, sinceUnix: now - 3 * 3600 - 14 * min, error: "", clients: 120, pool: 2, capacity: 230, nearFull: false, ...over });

const base = (id: string, name: string, protocol: string, network: string, port: number, over: Record<string, unknown> = {}) => ({
  id: `rly_${id}`, inboundId: `inb_${id}`, exitNodeId: "nod_de1", exitNodeName: "de1", profileId: `prf_${id}`, profileName: name, protocol, viaNodeId: "nod_msk1", viaNodeName: "msk1",
  network, port, exitPort: port, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: "192.0.2.10", appliedUnix: now - 3600, exitLive: true, agentSupported: true, createdUnix: now - 86400,
  check: check(port, { network }), monitor: { atUnix: now - 4 * min, ok: true, stage: "", code: "", responseMs: 182, failStreak: 0 }, bytes: 1_200_000_000, packets: 18_400, countedUnix: now - 3 * min, bps: 0,
  mode: "auto", activeMode: "dnat", modeChangedUnix: 0, modeReason: "", directStreak: 0, reverseBlocked: network === "udp" ? "udp" : "", ...over,
});
const vless = (over: Record<string, unknown> = {}) => base("v", "VLESS · Reality", "vless", "tcp", 443, over);
const mtproto = (over: Record<string, unknown> = {}) => base("m", "MTProto", "mtproto", "tcp", 8443, over);
const hy2 = (over: Record<string, unknown> = {}) => base("h", "Hysteria2", "hysteria2", "udp", 8444, over);

/** On the bridge: the leg fails (it is why), the exit reaches the entry and holds the bridge. */
const onBridge = (port: number, over: Record<string, unknown> = {}) => ({
  activeMode: "reverse", modeReason: "leg_failed", modeChangedUnix: now - 3 * 3600 - 20 * min, directStreak: 0, bridge: bridge(),
  check: check(port, { ...legDown, reverseReach: stage({ port: 443, atUnix: now - 9 * min }), bridge: stage({ atUnix: now - 40 }), verified: false }),
  ...over,
});
const pair = (opts: Record<string, unknown> = {}) => [vless({ ...onBridge(443), ...opts }), mtproto({ ...onBridge(8443), ...opts }), hy2({ modeReason: "udp", modeChangedUnix: 0 })];

type Scene = { relays: unknown[]; extra?: Record<string, unknown> };
const speeds = {
  reverseSpeed: stage({ mb: 8, mbitS: 31, bytes: 8_000_000, durationMs: 2100, connectMs: 40, firstByteMs: 120, atUnix: now - 6 * min }),
  directSpeed: stage({ mb: 8, mbitS: 0, bytes: 1_100_000, durationMs: 9000, status: "fail", code: "timeout", atUnix: now - 50 * min }),
};
const scenes: Record<string, Scene> = {
  direct: { relays: [vless(), mtproto(), hy2()] },
  bridge: { relays: pair() },
  returning: {
    relays: [
      vless({ ...onBridge(443), directStreak: 2, check: check(443, { ...legDown, atUnix: now - 12 * min, reverseReach: stage({ port: 443 }), bridge: stage(), ...speeds, verified: false }) }),
      mtproto({ ...onBridge(8443), directStreak: 2 }),
      hy2({ modeReason: "udp" }),
    ],
  },
  "near-full": { relays: pair({ bridge: bridge({ clients: 190, nearFull: true }) }) },
  "at-limit": { relays: pair({ bridge: bridge({ clients: 468, capacity: 470, conns: 4, pool: 4, error: "stream_unavailable", rttMs: 41 }) }) },
  down: {
    relays: [
      vless({ ...onBridge(443), bridge: bridge({ connected: false, conns: 0, sinceUnix: 0, error: "connect", clients: 0 }), check: check(443, { ...legDown, reverseReach: stage({ port: 443 }), bridge: stage({ status: "fail", code: "connect" }), verified: false }) }),
      mtproto({ ...onBridge(8443), bridge: bridge({ connected: false, conns: 0, sinceUnix: 0, error: "connect", clients: 0 }), check: check(8443, { ...legDown, reverseReach: stage({ port: 443 }), bridge: stage({ status: "fail", code: "connect" }), verified: false }) }),
      hy2({ modeReason: "udp" }),
    ],
  },
  "old-agent": { relays: [vless({ reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" }), mtproto({ reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" }), hy2({ modeReason: "udp" })] },
};

// ---- the candidates of "Relay through msk1" ----

const cut = { verdict: "partial", dark: ["MTS"], atUnix: now };
const cand = (over: Record<string, unknown>) => ({
  inboundId: "inb_x", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_x", profileName: "VLESS · Reality", protocol: "vless", network: "tcp", port: 443, unsupported: "", hop: false, relayId: "",
  country: cut, reach: { verdict: "blocked", source: "tspu_row", atUnix: now }, exitReachesEntry: { verdict: "reach", source: "exit_probe", atUnix: now - 10 * min }, reverseBlocked: "", ...over,
});
const candidates = {
  agentSupported: true,
  entryCountry: { verdict: "ok", dark: [], atUnix: now },
  candidates: [
    cand({ inboundId: "inb_v2", profileName: "VLESS · Reality" }),
    cand({ inboundId: "inb_m2", profileName: "MTProto", protocol: "mtproto", port: 8443 }),
    cand({ inboundId: "inb_h2", profileName: "Hysteria2", protocol: "hysteria2", network: "udp", port: 8444, reverseBlocked: "udp" }),
    cand({ inboundId: "inb_n2", nodeId: "nod_nl1", nodeName: "nl1", profileName: "VLESS · XHTTP", exitReachesEntry: { verdict: "blocked", source: "exit_probe", atUnix: now - 10 * min } }),
    cand({ inboundId: "inb_o2", nodeId: "nod_fi1", nodeName: "fi1", profileName: "VLESS · TCP", reverseBlocked: "exit_agent_old" }),
  ],
};

const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 10 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = []) => ({
  nodeId, country: "RU", verdict, atUnix: now - 10 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...dark.map((n) => vantage(n, "dark")), vantage("Rostelecom", "reach")], sourceVerdicts: [],
});
const fleetChecks = [country("nod_de1", CountryVerdict.PARTIAL, ["MTS"]), country("nod_msk1", CountryVerdict.OK), country("nod_spb1", CountryVerdict.OK)];

type Fill = [readonly unknown[], unknown];
const common = (relays: unknown[], role = Role.OWNER): Fill[] => [
  [meQuery.queryKey, { admin: { id: "adm_1", role } }],
  [nodesQuery.queryKey, { nodes }],
  [countryQuery.queryKey, { country: "RU" }],
  [settingsQuery.queryKey, { settings: { updateIntervalHours: 12, apps: [] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 0 }],
  [candidatesQuery("nod_msk1").queryKey, candidates],
  [relaysQuery("nod_de1").queryKey, relays],
  [relaysQuery("nod_msk1").queryKey, relays],
  [checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: fleetChecks }],
  [checksQuery("nod_de1").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: fleetChecks.slice(0, 1) }],
];

const de1Inbounds = [
  inbound({ id: "inb_v", profileId: "prf_v", profileName: "VLESS · Reality", protocol: "vless", port: 443, tlsServerName: "de1.example.com" }),
  inbound({ id: "inb_m", profileId: "prf_m", profileName: "MTProto", protocol: "mtproto", port: 8443, tlsServerName: "" }),
  inbound({ id: "inb_h", profileId: "prf_h", profileName: "Hysteria2", protocol: "hysteria2", port: 8444 }),
];

function Page({ relays, viaNodeId = "" }: { relays: unknown[]; viaNodeId?: string }) {
  return (
    <div className="relative left-1/2 w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2">
      <Mock fill={[...nodeFill("nod_de1"), ...common(relays)]}>
        <NodeDetail data={{ node: baseNode({ protocols: ["vless", "mtproto", "hysteria2"], linkViaNodeId: viaNodeId, ipv6: { entry: "", entrySupported: false, unavailableReason: "no_ipv6", addresses: [], src: "" } }), inbounds: de1Inbounds, onlineUsers: [], topToday: [], metrics, facts } as never} />
      </Mock>
    </div>
  );
}

/**
 * Puts the scene in its state: every action looks at the page and does its part only when it is not done yet (so a second run of
 * the effect, as in strict mode, undoes nothing). They are tried for a few seconds, until the dialog has what they need.
 */
function useEnsure(actions: (() => void)[]) {
  useEffect(() => {
    let n = 0;
    const id = window.setInterval(() => {
      for (const a of actions) a();
      if (++n > 16) window.clearInterval(id);
    }, 250);
    return () => window.clearInterval(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
}
const box = (i: number) => dlg()?.querySelectorAll<HTMLInputElement>("input[type=checkbox]")[i];
/** Ticks the i-th server of the dialog. */
const tick = (i: number) => () => {
  const b = box(i);
  if (b && !b.checked) b.click();
};
/** Opens the way's choice in the dialog. */
const openWay = () => {
  const b = dlg()?.querySelector<HTMLElement>("button[aria-expanded=false]");
  if (b && document.querySelectorAll("input[type=checkbox]:checked").length > 0) b.click();
};
const scrollDown = () => {
  const el = dlg()?.querySelector(".overflow-y-auto");
  if (el && dlg()?.querySelector("[data-testid=way-choice]")) el.scrollTop = el.scrollHeight;
};
const dlg = () => document.querySelector("[role=dialog]");

function Change({ udp = false, pick }: { udp?: boolean; pick?: string }) {
  const r = (udp ? scenes.direct!.relays[2] : scenes.bridge!.relays[0]) as RelayMsg;
  useEnsure(pick ? [() => { const want = dlg()?.querySelector<HTMLInputElement>(`input[type=radio][value=${pick}]`); if (want && !want.checked) want.click(); }] : []);
  return (
    <Mock fill={common(scenes.direct!.relays)}>
      <ChangeWay r={r} onClose={() => {}} />
    </Mock>
  );
}

/** The check window's calls answer from here, no server. */
function stubChecks() {
  const found = check(443, { ...legDown, reverseReach: stage({ port: 443 }), verified: false });
  const api = relaysApi as unknown as Record<string, unknown>;
  const made = vless({ id: "rly_new", enabled: false, state: InboundState.PENDING, check: undefined, monitor: undefined, modeChangedUnix: 0 });
  api.createRelay = async () => ({ relay: made });
  api.checkRelay = async () => ({ relay: { ...made, check: found } });
  api.updateRelay = async () => ({ relay: vless({ id: "rly_new", ...onBridge(443) }) });
}

function Add({ run }: { run?: boolean }) {
  useEffect(() => {
    if (run) stubChecks();
  }, [run]);
  useEnsure(run ? [tick(0), () => { const b = [...(dlg()?.querySelectorAll("button") ?? [])].find((x) => x.className.includes("text-on-accent")); if (b && !dlg()?.querySelector("[data-testid=relay-runs]") && box(0)?.checked) b.click(); }] : [tick(0), tick(2), openWay, scrollDown]);
  return (
    <Mock fill={common(scenes.direct!.relays)}>
      <AddRelayDialog open onOpenChange={() => {}} entry={{ id: "nod_msk1", name: "msk1", cc: "RU" }} />
    </Mock>
  );
}

function Reader() {
  const runs = useRelayRuns();
  return (
    <Mock fill={common(scenes.bridge!.relays, Role.HELPER)}>
      <div className="flex max-w-[760px] flex-col gap-3.5">
        <LinkViaLine viaNodeId="nod_msk1" />
        <Card lg>
          <Block flush className="pt-4" title="de1 · msk1">
            <RelayRows list={scenes.returning!.relays as RelayMsg[]} runs={runs} readOnly people="RU" />
          </Block>
        </Card>
      </div>
    </Mock>
  );
}

function Shot({ id, children }: { id: string; children: ReactNode }) {
  return <section data-shot={id}>{children}</section>;
}

export function BridgeKit() {
  const state = params.get("state") ?? "bridge";
  const scene = scenes[state];
  if (scene) return <Shot id={`bridge-${state}`}><Page relays={scene.relays} /></Shot>;
  switch (state) {
    case "change":
      return <Change pick="dnat" />;
    case "udp":
      return <Change udp />;
    case "add":
      return <Add />;
    case "check":
      return <Add run />;
    case "link-via":
      return <Shot id="bridge-link-via"><Page relays={scenes.direct!.relays} viaNodeId="nod_msk1" /></Shot>;
    case "reader":
      return <Reader />;
  }
  return <Shot id="bridge-bridge"><Page relays={scenes.bridge!.relays} /></Shot>;
}
