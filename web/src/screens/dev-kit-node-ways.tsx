import type { QueryClient } from "@tanstack/react-query";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { checksQuery, doctorQuery } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { dictionaries, currentLang, fill } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { candidatesQuery, relaysQuery } from "@/lib/relay";
import { setJob, type Job } from "./node/relay-job";
import { waysDemo } from "./node/ways-demo";
import { ruChecksOf, ruKindOf, type RuKind } from "./dev-kit-node-ru";
import { historyQuery } from "@/lib/reachability";

// Development only. The scenes of the node page's Ways in tab, /dev-kit?only=nodepage-frame&tab=ways&w=<scene>: the states of the board's
// PnNode switches, each a page of its own. The parameters (all optional, names as the board's switches):
//   role=exit|entry   de1 (an exit, "How people get here") or msk1 (an entry, "Whom it carries")
//   dir=open|part|shut  how de1 opens straight from Russia: every network, all but Operator C, none
//   ways=ok|part|bad    the relay path of the exit: works, not for Operator C, msk1 does not reach de1
//   set=all|norl|nov6|dir  the ways de1 has: all, no relay, no IPv6 entry, direct only
//   cas=<cascade>     how the relays work: "" (forwarding), br, back, manual, near, full, udp, fwd, port, drop, stale, nobr, unk, none
//   cause=<break>     a broken relay server: fw, old, leg, out, taken, down, gate, br
//   err=<code>        the word of the agent about the bridge (connect, pin_mismatch, no_verification, tls_protocol, connection_limit, pair_timeout,
//                     target_connect, identity_unavailable, site_listen_failed, bad_config, closed): a pair on the bridge with that problem
//   read=1            the panel could not read the relays the last time (the cascade's "read error")
//   edit=port|mode    the editor of the first server's port or method open under its row
//   add=1|via:nod_msk1|via:nod_msk2  the wizard "Make reachable" open: at the entries, or at the servers of that entry (msk2 is the one de1 reaches
//                     only over a bridge); wz=1 the wizard at its first step, the node
//   job=run|done|bridge|part|bad|stopped  the run of the wizard's last step (open it with add=via:...) in that state
//   wip=1             relays made and never turned on (the strip "Not finished"); run=1 a run in the background (the strip with its bar)
//   used=1            every other node carries de1 already: the wizard has no entry to offer
//   ru=<kind>         what the country check says of the node whose card is open (dev-kit-node-ru.ts: ok | partial | tcp | bad | listed | unknown | none | probe | points |
//                     conflict | old | long); more=1 opens the sources under the networks ("More")
//   extra=spb1        a second entry for de1; pairs=2|3|4 the exits of msk1
//   way=<lane>        the open way (?way= of the page), view=graph|list, map=1
// Made-up data: nodes de1, nl1, fi1, msk1, spb1, the networks Operator A to D.

const NOW = Math.floor(Date.now() / 1000);
const MIN = 60;

export type WaysScene = {
  role: "exit" | "entry";
  dir: "open" | "part" | "shut";
  ways: "ok" | "part" | "bad";
  set: "all" | "norl" | "nov6" | "dir";
  cas: string;
  cause: string;
  extra: string;
  pairs: number;
  err: string;
  read: boolean;
  edit: "" | "port" | "mode";
  add: string;
  wz: boolean;
  job: string;
  wip: boolean;
  run: boolean;
  used: boolean;
  ru: RuKind | "";
  more: boolean;
};

export const waysSceneOf = (p: URLSearchParams): WaysScene => ({
  role: p.get("role") === "entry" ? "entry" : "exit",
  dir: p.get("dir") === "shut" ? "shut" : p.get("dir") === "part" ? "part" : "open",
  ways: p.get("ways") === "bad" ? "bad" : p.get("ways") === "part" ? "part" : "ok",
  set: p.get("set") === "norl" || p.get("set") === "nov6" || p.get("set") === "dir" ? (p.get("set") as WaysScene["set"]) : "all",
  cas: p.get("cas") ?? "",
  cause: p.get("cause") ?? "",
  extra: p.get("extra") ?? "",
  pairs: Math.min(4, Math.max(1, Number(p.get("pairs") ?? 3))),
  err: p.get("err") ?? "",
  read: p.get("read") === "1",
  edit: p.get("edit") === "port" || p.get("edit") === "mode" ? (p.get("edit") as "port" | "mode") : "",
  add: p.get("add") ?? "",
  wz: p.get("wz") === "1",
  job: p.get("job") ?? "",
  wip: p.get("wip") === "1",
  run: p.get("run") === "1",
  used: p.get("used") === "1",
  ru: ruKindOf(p.get("ru")) ?? "",
  more: p.get("more") === "1",
});

const NETS = ["Operator A", "Operator B", "Operator C", "Operator D"];
const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: NOW - 10 * MIN, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
/** The CountryCheck of a node as Russia sees it; `dark` the networks that do not open it. */
const country = (nodeId: string, dark: string[], over: object = {}) => {
  const verdict = dark.length === 0 ? CountryVerdict.OK : dark.length === NETS.length ? CountryVerdict.BLOCKED : CountryVerdict.PARTIAL;
  return {
    nodeId, country: "RU", verdict, atUnix: NOW - 10 * MIN, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
    globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
    vantages: NETS.map((n) => vantage(n, dark.includes(n) ? "dark" : "reach")), sourceVerdicts: [], ...over,
  };
};

const stage = (over: Record<string, unknown> = {}) => ({ status: "ok", code: "", senderId: "", senderName: "", verdict: "ours", sent: 0, got: 0, atUnix: NOW - 4 * MIN, mb: 0, mbitS: 0, bytes: 0, connectMs: 0, firstByteMs: 0, durationMs: 0, port: 0, ...over });
const bridgeOf = (over: Record<string, unknown> = {}) => ({ connected: true, conns: 2, rttMs: 38, sinceUnix: NOW - 3 * 3600, error: "", clients: 120, pool: 2, capacity: 230, nearFull: false, ...over });

type Relay = Record<string, unknown>;
const relayOf = (entry: { id: string; name: string }, exit: { id: string; name: string; ip: string }, k: "vless" | "hy2" | "awg", over: Relay = {}): Relay => {
  const udp = k !== "vless";
  const base: Relay = {
    id: `rly_${entry.name}_${exit.name}_${k}`, inboundId: `inb_${exit.name}_${k}`, exitNodeId: exit.id, exitNodeName: exit.name, profileId: `prf_${k}`,
    profileName: k === "vless" ? "VLESS · Reality" : k === "hy2" ? "Hysteria2 · 443" : "AmneziaWG 3.1", protocol: k === "vless" ? "vless" : k === "hy2" ? "hysteria2" : "amneziawg",
    viaNodeId: entry.id, viaNodeName: entry.name, network: udp ? "udp" : "tcp", port: k === "vless" ? 8443 : 443, exitPort: 443, enabled: true, state: InboundState.ACTIVE, lastError: "", toIp: exit.ip,
    appliedUnix: NOW - 3 * 3600, exitLive: true, agentSupported: true, createdUnix: NOW - 2 * 86400, bytes: 22_800n, packets: 82n, countedUnix: NOW - 3 * MIN, bps: 0n,
    mode: "auto", activeMode: "dnat", modeChangedUnix: 0, modeReason: "", directStreak: 0, reverseBlocked: udp ? "udp" : "",
    check: { atUnix: NOW - 4 * MIN, network: udp ? "udp" : "tcp", port: 443, to: `${exit.ip}:443`, leg: stage(), path: stage({ senderId: "nod_nl1", senderName: "nl1" }), ok: true, stage: "", verified: true },
    monitor: { atUnix: NOW - 4 * MIN, ok: true, stage: "", code: "", responseMs: 182, failStreak: 0 },
  };
  return { ...base, ...over };
};

/** The words an agent says that are no failure of the bridge: it holds, or will in a while. */
const waiting = ["no_verification", "connection_limit", "closed"];

/** The cascade's states of the board, as relay messages for the pair entry → exit. */
function pairRelays(entry: { id: string; name: string }, exit: { id: string; name: string; ip: string }, cas: string, cause: string, err = ""): Relay[] {
  const v = relayOf(entry, exit, "vless");
  const h = relayOf(entry, exit, "hy2");
  const legDown = (r: Relay, over: Relay = {}): Relay => ({ ...r, check: { ...(r.check as object), ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout", verdict: "timeout" }), ...over } });
  const onBridge = (r: Relay, b: Relay = {}): Relay => ({
    ...legDown(r, { reverseReach: stage({ port: 8443 }), bridge: stage() }),
    activeMode: "reverse", modeReason: "leg_failed", modeChangedUnix: NOW - 3 * 3600, directStreak: 1, reverseBlocked: "", bridge: bridgeOf(b),
  });
  let rs: Relay[] = [v, h];
  // a bridge with the agent's word about it: a failure (it does not hold) or a pause (it holds and will in a while)
  if (err) {
    const bad = !waiting.includes(err);
    return [bad ? { ...onBridge(v, { connected: false, error: err }), check: { ...(onBridge(v).check as object), ok: false, stage: "bridge", bridge: stage({ status: "fail", code: err }) } } : onBridge(v, { error: err }), h];
  }
  switch (cas) {
    case "br":
      rs = [onBridge(v), h];
      break;
    case "port":
      rs = [onBridge(v), h];
      break;
    case "back":
      rs = [{ ...v, modeReason: "direct_back", modeChangedUnix: NOW - 3600, check: { ...(v.check as object), reverseReach: stage({ port: 8443 }) } }, h];
      break;
    case "manual":
      rs = [{ ...onBridge(v), mode: "reverse", modeReason: "owner" }, h];
      break;
    case "near":
      rs = [onBridge(v, { clients: 196, capacity: 230, nearFull: true }), h];
      break;
    case "full":
      rs = [onBridge(v, { clients: 468, capacity: 470, pool: 4, nearFull: true, error: "stream_limit" }), h];
      break;
    case "udp":
      rs = [onBridge(v), legDown({ ...h, state: InboundState.FAILED, lastError: "" }, { stage: "path", path: stage({ status: "fail", code: "timeout" }) })];
      break;
    case "fwd":
      rs = [{ ...v, mode: "dnat" }, { ...h, mode: "dnat" }];
      break;
    case "drop":
      rs = [{ ...onBridge(v, { connected: false, error: "connect" }), check: { ...(onBridge(v).check as object), bridge: stage({ status: "fail", code: "connect" }) } }, h];
      break;
    case "stale":
      rs = [{ ...v, check: { ...(v.check as object), reverseReach: stage({ port: 8443, atUnix: NOW - 40 * MIN }) } }, h];
      break;
    case "nobr":
      rs = [{ ...v, reverseBlocked: "exit_agent_old", modeReason: "reverse_unsupported" }, h];
      break;
    case "unk":
      rs = [v, h];
      break;
    case "none":
      rs = [legDown({ ...v, state: InboundState.FAILED }, { reverseReach: stage({ status: "fail", code: "connect", port: 8443 }) }), legDown({ ...h, state: InboundState.FAILED })];
      break;
    default:
      if (cas === "") rs = [{ ...v, check: { ...(v.check as object), reverseReach: stage({ port: 8443 }) } }, h];
  }
  // a broken server of the pair
  const w = rs[0]!;
  const broken = (patch: Relay): Relay[] => [{ ...w, ...patch }, ...rs.slice(1)];
  switch (cause) {
    case "fw":
      return broken({ state: InboundState.FAILED, lastError: "forward_drop" });
    case "old":
      return broken({ state: InboundState.FAILED, lastError: "agent_old", agentSupported: false });
    case "leg":
      return broken({ state: InboundState.FAILED, ...legDown(w) });
    case "out":
      return broken({ state: InboundState.FAILED, check: { ...(w.check as object), ok: false, stage: "path", path: stage({ status: "fail", code: "timeout" }) } });
    case "taken":
      return broken({ state: InboundState.FAILED, lastError: "port_taken" });
    case "down":
      return broken({ state: InboundState.FAILED, exitLive: false, lastError: "exit_offline" });
    case "gate":
      return broken({ enabled: false, state: InboundState.PENDING, check: undefined, monitor: undefined });
    case "br":
      return broken({ ...onBridge(w, { connected: false, error: "connect" }), state: InboundState.FAILED });
  }
  return rs;
}

const de1 = { id: "nod_de1", name: "de1", ip: "203.0.113.7" };
const NODE_SPEC = [
  { id: "nod_msk1", name: "msk1", countryCode: "RU", location: "Moscow" },
  { id: "nod_msk2", name: "msk2", countryCode: "RU", location: "Moscow" },
  { id: "nod_spb1", name: "spb1", countryCode: "RU", location: "Saint Petersburg" },
  { id: "nod_nl1", name: "nl1", countryCode: "NL", location: "Amsterdam" },
  { id: "nod_nl2", name: "nl2", countryCode: "NL", location: "Amsterdam" },
  { id: "nod_fi1", name: "fi1", countryCode: "FI", location: "Helsinki" },
];
const EXITS = [
  { id: "nod_nl1", name: "nl1", ip: "203.0.113.21" },
  { id: "nod_fi1", name: "fi1", ip: "203.0.113.31" },
  { id: "nod_nl2", name: "nl2", ip: "203.0.113.22" },
  { id: de1.id, name: "de1", ip: de1.ip },
];

const say = (key: MessageKey, vars: Record<string, string | number> = {}) => fill(dictionaries[currentLang()][key], vars);

type Kind = "vless" | "hy2" | "awg";
const KINDS: Kind[] = ["vless", "hy2", "awg"];
const KIND = {
  vless: { name: "VLESS · Reality", protocol: "vless", network: "tcp", port: 2053 },
  hy2: { name: "Hysteria2", protocol: "hysteria2", network: "udp", port: 4443 },
  awg: { name: "AmneziaWG 3.1", protocol: "awg", network: "udp", port: 10819 },
} as const;
/** What de1 offers an entry: its three servers, with what the panel knows of the way (the TSPU row, whether de1 reaches the entry, who would get them). */
const candidate = (k: Kind, entryId: string): Record<string, unknown> => {
  const bridge = entryId === "nod_msk2";
  const known = entryId !== "nod_spb1";
  return {
    inboundId: `inb_de1_${k}`, nodeId: "nod_de1", nodeName: "de1", profileId: `prf_${k}`, profileName: KIND[k].name, protocol: KIND[k].protocol, network: KIND[k].network, port: KIND[k].port,
    unsupported: "", hop: false, relayId: "", country: { verdict: "blocked", dark: NETS, atUnix: NOW },
    reach: { verdict: !known ? "" : bridge ? "blocked" : "reach", source: !known ? "" : bridge ? "tspu_row" : "tspu_row", atUnix: known ? NOW - 40 * MIN : 0 },
    exitReachesEntry: { verdict: bridge ? "reach" : "", source: bridge ? "exit_probe" : "", atUnix: bridge ? NOW - 40 * MIN : 0 },
    audience: { people: 14, staticAwgDevices: k === "awg" ? 3 : 0 }, reverseBlocked: KIND[k].network === "udp" ? "udp" : "",
  };
};
const entryNode = (id: string) => ({ id, name: id.slice(4), cc: "RU" });

/** The run of the wizard's last step in the state a scene wants, as the store holds it. */
function jobScene(kind: string, entryId: string): Job {
  const entry = entryNode(entryId);
  const bridge = kind === "bridge" || entryId === "nod_msk2";
  const items = KINDS.map((k) => ({ c: candidate(k, entryId) as never }));
  const ok = { leg: "ok", path: "ok", tone: "ok", text: "" } as const;
  const okBridge = { leg: "fail", soft: true, reach: "ok", bridge: "ok", path: "ok", tone: "ok", text: "" } as const;
  const bad = { leg: "fail", path: "skipped", tone: "bad", text: say("relay.dlg.result.fail", { reason: say("relay.why.leg", { entry: entry.name, exit: "de1", exitPort: 4443, port: 4443, network: "udp", net: "udp" }) }) } as const;
  const busy = { leg: "running", path: "waiting" } as const;
  const one = bridge ? okBridge : ok;
  const runs: Record<string, object> = {
    run: { vless: ok, hy2: bridge ? { leg: "fail", soft: true, reach: "running", bridge: "running", path: "waiting" } : busy, awg: {} },
    done: { vless: ok, hy2: ok, awg: ok },
    bridge: { vless: one, hy2: one, awg: one },
    part: { vless: ok, hy2: bad, awg: ok },
    bad: { vless: { ...bad, free: 2054, text: say("err.port_taken", { port: 2053, node: "msk1", profile: "Trojan" }) }, hy2: bad, awg: bad },
    stopped: { vless: ok, hy2: {}, awg: {} },
  }[kind === "bridge" ? "bridge" : kind] ?? {};
  const states = { run: [1, 1, "running"], done: [3, 3, "finished"], bridge: [3, 3, "finished"], part: [3, 2, "finished"], bad: [3, 0, "finished"], stopped: [1, 1, "stopped"] } as const;
  const [done, on, state] = states[(kind in states ? kind : "run") as keyof typeof states];
  const byServer = Object.fromEntries(KINDS.map((k) => [`inb_de1_${k}`, (runs as Record<Kind, object>)[k]]).filter(([, r]) => r && Object.keys(r).length > 0));
  return { exitId: "nod_de1", exitName: "de1", exitCc: "DE", entry, items, bridge, runs: byServer as never, done, on, state };
}

/** Relays that a run left: made and checked, never turned on. */
const leftOff = (entry: { id: string; name: string }, exit: { id: string; name: string; ip: string }): Relay[] =>
  pairRelays(entry, exit, "", "").map((r) => ({ ...r, enabled: false, appliedUnix: 0, state: InboundState.PENDING, monitor: undefined, check: { ...(r.check as object), path: stage({ status: "skipped", code: "off", verdict: "" }) } }));

/** The relays were read before, and the last read failed: the data stays, the query says it is in error. */
function unread(qc: QueryClient, nodeId: string) {
  qc.getQueryCache().find({ queryKey: relaysQuery(nodeId).queryKey })?.setState({ status: "error", error: new Error("read"), fetchStatus: "idle", errorUpdateCount: 1, errorUpdatedAt: Date.now() } as never);
}

/** Seeds the scene's queries into `qc` and returns the page data of the node it is about (de1, or msk1 for an entry). */
export function seedWays(qc: QueryClient, data: Record<string, unknown>, sc: WaysScene, nodeStatus: NodeStatus): Record<string, unknown> {
  const msk1 = { id: "nod_msk1", name: "msk1" };
  const spb1 = { id: "nod_spb1", name: "spb1" };
  // the fleet: every node the tab names
  waysDemo.current = sc.edit || sc.more ? { ...(sc.edit ? { edit: sc.edit } : {}), ...(sc.more ? { nets: true } : {}) } : undefined;
  const base = qc.getQueryData(nodesQuery.queryKey) as { nodes: Record<string, unknown>[] };
  const own = base.nodes[0]!;
  // the wizard's entries are the nodes of Russia, with a node or two of other countries for the list to have somewhere to go
  const spec = sc.add || sc.wz ? NODE_SPEC.filter((n) => ["msk1", "msk2", "spb1", "fi1"].includes(n.name)) : NODE_SPEC;
  const fleet = [own, ...spec.map((n) => ({ ...own, ...n, status: NodeStatus.ONLINE, hasMetrics: true, cpuPct: n.name === "msk1" ? 34 : n.name === "msk2" ? 12 : 6 }))];
  qc.setQueryData(nodesQuery.queryKey, { ...base, nodes: fleet } as never);
  waysDemo.current = { ...(waysDemo.current ?? {}), ...(sc.wz ? { wizard: { step: 1 as const } } : {}) };
  for (const n of spec) qc.setQueryData(candidatesQuery(n.id).queryKey, { agentSupported: true, entryCountry: { verdict: "ok", dark: [], atUnix: NOW }, candidates: KINDS.map((k) => candidate(k, n.id)) } as never);
  // a run on its way (or over) for the wizard's last step, or a run in the background behind the page
  setJob(sc.job || sc.run ? jobScene(sc.job || "run", sc.add.startsWith("via:") ? sc.add.slice(4) : "nod_msk1") : undefined);

  // the check of the node whose card is open, as the scene says (the one of dev-kit-node-ru.ts); the block lists are on in the week's sources
  const ruOf = (id: string) => (sc.ru && sc.ru !== "none" ? ruChecksOf(sc.ru, NOW).map((c) => ({ ...c, nodeId: id })) : []);
  if (sc.ru) qc.setQueryData(historyQuery.queryKey, { country: "RU", sources: ["globalping", "lists", "tspu"], sinceUnix: NOW - 7 * 86400, nowUnix: NOW, nodes: [] } as never);
  const darkOf = sc.dir === "shut" ? NETS : sc.dir === "part" ? [NETS[2]!] : [];
  const checks0 = [country("nod_de1", darkOf), country("nod_msk1", sc.ways === "bad" && sc.cas === "" ? [] : []), country("nod_msk2", []), country("nod_spb1", [NETS[0]!]), ...EXITS.slice(0, 3).map((e) => country(e.id, darkOf.length ? [] : []))];
  const openId = sc.role === "exit" ? "nod_de1" : "nod_msk1";
  const checks = sc.ru ? [...checks0.filter((c) => c.nodeId !== openId), ...(ruOf(openId) as typeof checks0)] : checks0;
  const rowsOf = (id: string) => ({ nodeId: id, nodeName: id.slice(4), countryCode: "", nodeStatus, cells: [] });

  if (sc.role === "exit") {
    const withRelay = sc.set === "all" || sc.set === "nov6";
    const relays: Relay[] = sc.wip
      ? leftOff(msk1, de1)
      : sc.used
        ? ["nod_msk1", "nod_msk2", "nod_spb1", "nod_fi1"].flatMap((id) => pairRelays({ id, name: id.slice(4) }, de1, "", "").slice(0, 1))
        : !withRelay
        ? []
        : [
          ...pairRelays(msk1, de1, sc.cas, sc.cause, sc.err).map((r) => (sc.ways === "bad" ? { ...r, state: InboundState.FAILED, check: { ...(r.check as object), ok: false, stage: "leg", leg: stage({ status: "fail", code: "timeout" }) } } : r)),
          ...(sc.extra === "spb1" ? pairRelays(spb1, de1, "", "").slice(0, 1) : []),
        ];
    qc.setQueryData(relaysQuery("nod_de1").queryKey, relays as never);
    const cs = checks.map((c) => (c.nodeId === "nod_msk1" && sc.ways === "part" ? country("nod_msk1", [NETS[2]!]) : c));
    qc.setQueryData(checksQuery("nod_de1").queryKey, { nowUnix: NOW, intervalS: 300, columns: [], rows: [rowsOf("nod_de1")], countryChecks: cs } as never);
    qc.setQueryData(checksQuery("").queryKey, { nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: cs } as never);
    const v6 = sc.set === "nov6" || sc.set === "dir";
    const node = data.node as Record<string, unknown>;
    node.ipv6 = { entry: v6 ? "" : "2001:db8:7::1", entrySupported: true, unavailableReason: "", addresses: [{ address: "2001:db8:7::1", prefixLen: 64 }], src: "2001:db8:7::1" };
    node.status = nodeStatus;
    // the ports of the Direct way: what the check found and when
    const check = (port: number, over: object = {}) => ({ nodeId: "nod_de1", port, verdict: "ok", sent: 300, got: 300, checkedUnix: NOW - 6 * MIN, badUnix: 0, sender: "fi1", reason: "", senderVerdict: "", confirmSender: "", confirmSent: 0, confirmGot: 0, confirmVerdict: "", blame: "", protocolOkUnix: 0, ...over });
    data.portChecks = [check(443), check(51820, { checkedUnix: NOW - 15 * MIN, got: 299 })];
    data.tcpChecks = [
      { nodeId: "nod_de1", port: 443, verdict: "ours", role: "vless", profile: "VLESS · Reality", checkedUnix: NOW - 11 * MIN, sender: "fi1", reason: "", alpn: false },
      { nodeId: "nod_de1", port: 80, verdict: "ours", role: "acme", profile: "", checkedUnix: NOW - 11 * MIN, sender: "fi1", reason: "", alpn: false },
    ];
    if (sc.set === "dir") qc.setQueryData(relaysQuery("nod_de1").queryKey, [] as never);
    if (sc.read) unread(qc, "nod_de1");
    return data;
  }

  // an entry: msk1 passes several exits on
  const list = (sc.pairs === 4 ? EXITS : EXITS.slice(0, sc.pairs)).map((x, i) => ({ x, cas: i === 0 ? sc.cas : i === 1 ? "br" : "", cause: i === 0 ? sc.cause : i === 2 ? "leg" : "" }));
  const relays = list.flatMap(({ x, cas, cause }, i) => (sc.wip && i === 1 ? leftOff(msk1, x) : pairRelays(msk1, x, cas, cause, i === 0 ? sc.err : "")));
  qc.setQueryData(relaysQuery("nod_msk1").queryKey, relays as never);
  if (sc.read) unread(qc, "nod_msk1");
  qc.setQueryData(checksQuery("nod_msk1").queryKey, { nowUnix: NOW, intervalS: 300, columns: [], rows: [rowsOf("nod_msk1")], countryChecks: checks } as never);
  qc.setQueryData(checksQuery("").queryKey, { nowUnix: NOW, intervalS: 300, columns: [], rows: [], countryChecks: checks } as never);
  qc.setQueryData(doctorQuery("nod_msk1").queryKey, { nowUnix: NOW, nodes: [{ nodeId: "nod_msk1", nodeName: "msk1", nodeStatus, agentSupported: true, hasReport: true, receivedUnix: NOW - 5 * MIN, ageS: 300, stale: false, items: [] }] } as never);
  const node = {
    ...(data.node as object), id: "nod_msk1", name: "msk1", countryCode: "RU", location: "Moscow", address: "198.51.100.20", status: nodeStatus, protocols: [], online: [],
    bandwidthMbps: sc.cas === "near" ? 0 : 1000, bridgePort: sc.cas === "port" ? 8443 : 443, bridgePortReason: sc.cas === "port" ? "reality_external_target" : "", bridgePortProfileId: sc.cas === "port" ? "prf_vless" : "",
    ipv6: { entry: "", entrySupported: false, unavailableReason: "no_ipv6", addresses: [], src: "" },
  };
  return { ...data, node, inbounds: [], portChecks: [], tcpChecks: [], facts: { ...(data.facts as object), hasIpv6: false } };
}

/** The frames of the scene kit: [id, title, query, width, height]. */
export const waysFrames: { id: string; title: string; q: string; w: number; h: number; state?: string }[] = [
  { id: "ways-exit-ok", title: "Ways: de1 works direct and through msk1, three ways (forwarding)", q: "w=1", w: 1440, h: 1500 },
  { id: "ways-exit-graph", title: "Ways: the same, the graph", q: "w=1&view=graph", w: 1440, h: 1700 },
  { id: "ways-exit-relay", title: "Ways: the open way is through msk1, the servers", q: "w=1&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-exit-part", title: "Ways: through msk1 works, not for Operator C", q: "w=1&ways=part&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-exit-bad", title: "Ways: msk1 does not reach de1, the relay does not pass anyone", q: "w=1&ways=bad&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-exit-shut", title: "Ways: direct is closed from Russia, the bypass is through msk1", q: "w=1&dir=shut", w: 1440, h: 1500 },
  { id: "ways-exit-part-direct", title: "Ways: direct is dark on Operator C", q: "w=1&dir=part", w: 1440, h: 1500 },
  { id: "ways-exit-norelay", title: "Ways: no relay (a ghost row and a button to make it reachable)", q: "w=1&set=norl&dir=shut", w: 1440, h: 1400 },
  { id: "ways-exit-nov6", title: "Ways: IPv6 entry is off", q: "w=1&set=nov6&way=ipv6", w: 1440, h: 1300 },
  { id: "ways-exit-dir", title: "Ways: direct only", q: "w=1&set=dir", w: 1440, h: 1300 },
  { id: "ways-exit-two", title: "Ways: a second entry, spb1", q: "w=1&extra=spb1", w: 1440, h: 1500 },
  { id: "ways-cas-br", title: "Cascade: the bridge (auto)", q: "w=1&cas=br&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-back", title: "Cascade: back on forwarding", q: "w=1&cas=back&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-manual", title: "Cascade: the bridge only, by hand", q: "w=1&cas=manual&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-near", title: "Cascade: the bridge is almost full", q: "w=1&cas=near&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-full", title: "Cascade: the bridge is at its limit", q: "w=1&cas=full&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-udp", title: "Cascade: the bridge for TCP, a UDP server down", q: "w=1&cas=udp&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-fwd", title: "Cascade: forwarding only, chosen by hand", q: "w=1&cas=fwd&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-drop", title: "Cascade: the bridge does not hold", q: "w=1&cas=drop&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-stale", title: "Cascade: the check of the bridge is old", q: "w=1&cas=stale&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-nobr", title: "Cascade: the bridge is not available, the agent is old", q: "w=1&cas=nobr&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-unk", title: "Cascade: the bridge was not checked yet", q: "w=1&cas=unk&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cas-none", title: "Cascade: neither forwarding nor the bridge works", q: "w=1&cas=none&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cause-fw", title: "A broken server: the firewall drops forwarding", q: "w=1&cause=fw&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cause-leg", title: "A broken server: the leg does not pass", q: "w=1&cause=leg&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cause-out", title: "A broken server: nothing comes from outside", q: "w=1&cause=out&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cause-gate", title: "A server that was not checked: it cannot be turned on yet", q: "w=1&cause=gate&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-cause-br", title: "A broken server on the bridge", q: "w=1&cas=br&cause=br&way=via:nod_msk1", w: 1440, h: 1900 },
  ...[
    ["connect", "The bridge does not hold: the exit cannot connect to the entry"],
    ["pin_mismatch", "The bridge does not hold: the certificate of the entry's site does not match"],
    ["no_verification", "The bridge waits for the pin of the entry's certificate (a quiet line)"],
    ["tls_protocol", "The bridge does not hold: something else answers on the bridge port"],
    ["connection_limit", "The bridge is at its limit of connections from the exit (a warning, it passes by itself)"],
    ["pair_timeout", "The bridge loses people's connections: the second half did not come in 5 s"],
    ["target_connect", "The bridge does not hold: the server on the exit does not take the bridge's connections"],
    ["identity_unavailable", "The bridge does not hold: the agent has no node certificate yet"],
    ["site_listen_failed", "The bridge does not hold: the bridge site did not come up on the entry"],
    ["bad_config", "The bridge does not hold: the panel sent bad bridge settings"],
    ["closed", "The bridge is restarting (a quiet line)"],
  ].map(([code, title]) => ({ id: `ways-err-${code}`, title: `Bridge error: ${title}`, q: `w=1&err=${code}&way=via:nod_msk1`, w: 1440, h: 1900 })),
  { id: "ways-cas-err", title: "Cascade: the panel could not read the relays, one line and Try again", q: "w=1&read=1&cas=br&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-edit-port", title: "A server's row with the port editor open under it", q: "w=1&edit=port&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-edit-mode", title: "A server's row with the method editor open under it", q: "w=1&edit=mode&way=via:nod_msk1", w: 1440, h: 1900 },
  { id: "ways-entry-err", title: "Entry: the bridge of nl1 is down, the exit cannot reach the entry", q: "w=1&role=entry&err=connect&way=out:nod_nl1", w: 1440, h: 2000 },
  { id: "wz-1", title: "Wizard, step 1: the node, chosen already, no way in at all", q: "w=1&set=norl&dir=shut&wz=1&add=1", w: 1440, h: 1500 },
  { id: "wz-1-part", title: "Wizard, step 1: a way works for some, a spare one is added", q: "w=1&dir=shut&wz=1&add=1&extra=spb1", w: 1440, h: 1500 },
  { id: "wz-2", title: "Wizard, step 2: the entries, the first fits and is chosen, one only over a bridge", q: "w=1&set=norl&dir=shut&add=1", w: 1440, h: 1500 },
  { id: "wz-2-none", title: "Wizard, step 2: every entry carries de1 already", q: "w=1&used=1&add=1", w: 1440, h: 1300 },
  { id: "wz-3", title: "Wizard, step 3: the three servers through msk1, what people get, AmneziaVPN keys", q: "w=1&set=norl&dir=shut&add=via:nod_msk1", w: 1440, h: 1500 },
  { id: "wz-3-bridge", title: "Wizard, step 3: through msk2 only over a bridge, the UDP servers cannot go", q: "w=1&set=norl&dir=shut&add=via:nod_msk2", w: 1440, h: 1500 },
  { id: "wz-4-run", title: "Wizard, step 4: the run, one server through, one checking, one waits", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=run", w: 1440, h: 1500 },
  { id: "wz-4-run-bridge", title: "Wizard, step 4: the run over a bridge, the second server waits for the bridge", q: "w=1&set=norl&dir=shut&add=via:nod_msk2&job=run", w: 1440, h: 1500 },
  { id: "wz-4-done", title: "Wizard, step 4: all three are on, the wave and the glints at the edge (with the effects)", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=done&fx=1", w: 1440, h: 1500 },
  { id: "wz-4-done-bridge", title: "Wizard, step 4: all on over a bridge", q: "w=1&set=norl&dir=shut&add=via:nod_msk2&job=bridge", w: 1440, h: 1500 },
  { id: "wz-4-part", title: "Wizard, step 4: two of three, one did not come up and says why", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=part", w: 1440, h: 1500 },
  { id: "wz-4-bad", title: "Wizard, step 4: none came up, the port is taken", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=bad", w: 1440, h: 1500 },
  { id: "wz-4-stopped", title: "Wizard, step 4: stopped after the first", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=stopped", w: 1440, h: 1500 },
  { id: "wz-2-phone", title: "Wizard, step 2 on a phone, 400 wide", q: "w=1&set=norl&dir=shut&add=1", w: 400, h: 1800 },
  { id: "wz-3-phone", title: "Wizard, step 3 on a phone, 400 wide", q: "w=1&set=norl&dir=shut&add=via:nod_msk2", w: 400, h: 2200 },
  { id: "wz-4-phone", title: "Wizard, step 4 on a phone, 400 wide", q: "w=1&set=norl&dir=shut&add=via:nod_msk1&job=part", w: 400, h: 2000 },
  { id: "wip-left", title: "Ways: a run left three relays made and not turned on (the strip Not finished)", q: "w=1&set=norl&dir=shut&wip=1", w: 1440, h: 1500 },
  { id: "wip-run", title: "Ways: a run goes on in the background (the strip with its bar)", q: "w=1&set=norl&dir=shut&run=1", w: 1440, h: 1500 },
  { id: "wip-entry", title: "Entry: a run left relays for nl1 made and not turned on", q: "w=1&role=entry&wip=1&pairs=3", w: 1440, h: 1700 },
  { id: "ways-entry", title: "Entry: msk1 passes three exits (the list)", q: "w=1&role=entry", w: 1440, h: 1700 },
  { id: "ways-entry-graph", title: "Entry: the graph", q: "w=1&role=entry&view=graph", w: 1440, h: 1700 },
  { id: "ways-entry-four", title: "Entry: four exits", q: "w=1&role=entry&pairs=4", w: 1440, h: 1700 },
  { id: "ways-entry-bridge", title: "Entry: a pair on the bridge, its port is 8443", q: "w=1&role=entry&cas=port&way=out:nod_nl1", w: 1440, h: 2000 },
  { id: "ways-entry-limit", title: "Entry: the bridge of nl1 is at its limit", q: "w=1&role=entry&cas=full&way=out:nod_nl1", w: 1440, h: 2000 },
  { id: "ways-entry-fw", title: "Entry: the firewall drops forwarding (the firewall of the entry)", q: "w=1&role=entry&cause=fw&way=out:nod_nl1", w: 1440, h: 2000 },
  // the block "Is it seen from the country": the states of the board's chips and of "More", the data of dev-kit-node-ru.ts
  ...(
    [
      ["ok", "every network opens it, the probe, Globalping and the lists agree"],
      ["partial", "some networks do not open it"],
      ["tcp", "TCP is cut"],
      ["bad", "no network opens it"],
      ["listed", "the address is in the registry, no live check"],
      ["unknown", "no data: too few probes"],
      ["probe", "the probe node and the registry say"],
      ["points", "two own probes, one does not prove anything"],
      ["conflict", "the sources disagree"],
      ["old", "a result days old"],
      ["long", "twelve networks, long names of networks and probes"],
    ] as const
  ).flatMap(([k, what]) => [
    { id: `ways-ru-${k}`, title: `Seen from Russia (${k}): ${what}`, q: `w=1&way=direct&ru=${k}`, w: 1440, h: 1500 },
    { id: `ways-ru-${k}-more`, title: `Seen from Russia (${k}), More open: ${what}`, q: `w=1&way=direct&ru=${k}&more=1`, w: 1440, h: 1700 },
  ]),
  { id: "ways-ru-none", title: "Seen from Russia: the check has nothing on the node", q: "w=1&way=direct&ru=none", w: 1440, h: 1500 },
  { id: "ways-ru-down", title: "Seen from Russia: the node is silent, the chips have no data, More open", q: "w=1&way=direct&ru=ok&more=1", w: 1440, h: 1700, state: "down" },
  { id: "ways-ru-self", title: "Seen from Russia: the entry's own card (no More)", q: "w=1&role=entry&ru=partial", w: 1440, h: 1700 },
  { id: "ways-ru-relay", title: "Seen from Russia: msk1 under the way through it, More open", q: "w=1&way=via:nod_msk1&ru=partial&more=1", w: 1440, h: 1900 },
  { id: "ways-ru-long-phone", title: "Seen from Russia: the long data on a phone, 400 wide", q: "w=1&way=direct&ru=long&more=1", w: 400, h: 3400 },
  { id: "ways-ru-points-phone", title: "Seen from Russia: own probes on a phone, 400 wide", q: "w=1&way=direct&ru=points&more=1", w: 400, h: 2800 },
  { id: "ways-exit-down", title: "Ways: the node does not answer", q: "w=1", w: 1440, h: 1500, state: "down" },
  { id: "ways-exit-viewer", title: "Ways: a read-only role", q: "w=1", w: 1440, h: 1300, state: "viewer" },
  { id: "ways-exit-1100", title: "Ways at 1100 wide: one column", q: "w=1&way=via:nod_msk1", w: 1100, h: 2600 },
  { id: "ways-exit-phone", title: "Ways on a phone, 400 wide", q: "w=1&way=via:nod_msk1", w: 400, h: 3600 },
  { id: "ways-entry-phone", title: "Entry on a phone, 400 wide", q: "w=1&role=entry", w: 400, h: 3400 },
];
