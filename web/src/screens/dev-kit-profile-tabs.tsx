import { Code, ConnectError } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";
import type { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { groups as groupsApi, health as healthApi, nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { checksQuery } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { GROUPS, NODES, NOW, nid, nodeRows, PROFILES } from "@/screens/dev-kit-profiles-data";
import type { Frame } from "@/screens/dev-kit-profile";
import type { PageData } from "@/screens/profiles/page-types";
import { twinPlanKey } from "@/screens/profiles/twin";
import { groupsQuery } from "@/screens/users/rpc";

// Development only. What the Servers and People tabs and the windows of the profile page add to the scenes of dev-kit-profile.tsx: the made-up
// answers of their own calls (check before the click, put on nodes, take off, copy, delete) and the frames that show their states. A scene's
// address is /dev-kit?only=profile-page&state=<profile>.<tab>[.<variant>…]; the variants of these parts are, by tab:
//   servers: menu (the first row's ⋯ open) | ask (its "Take off" asked once more) | edit [port | sni] (its port and domain open) | load | err |
//     nochecks | out (nl3 out of touch) | chk | chkd (the check walk, mid-way and just done) | apply | applied (the apply course) | own (a port of its
//     own, a switched-off server, an address left out of the subscriptions) | cert (a certificate about to end) | warp (the profile exits through WARP)
//   people: menu (the "Add to a group" menu open) | nogroups (the panel has no group at all)
//   windows: put [pchk | run [hang | fail] on-de2+nl3+fi2 (out: nl3 is out of touch)] | take [pick-de1+nl1] | del [typed | run | err-deployed | err-name | err-stale | err-gone |
//     err-partial] | twin [done]

const NEVER = new Promise<never>(() => {});
const tokens = (state: string) => state.split(".").slice(2);
const profileKey = (state: string) => state.split(".")[0] ?? "hy1";
const nodeOfId = (id: string) => id.replace("nod_", "");
const err = (msg: string, code = Code.FailedPrecondition) => new ConnectError(msg, code);
const PORT = (key: string) => PROFILES.find((p) => p.id === `prf_${key}`)?.port ?? 443;

/** The panel's answers to the calls of the tabs and windows, for the state a scene asks for. Every method is set on every call: scenes share the client. */
function fakeCalls(state: string) {
  const v = new Set(tokens(state));
  const key = profileKey(state);
  const port = PORT(key);
  const p = profilesApi as unknown as Record<string, unknown>;
  const n = nodesApi as unknown as Record<string, unknown>;
  const g = groupsApi as unknown as Record<string, unknown>;
  const h = healthApi as unknown as Record<string, unknown>;
  const inboundOf = (node: string, st: InboundState, lastError = "") => ({ id: `inb_${key}_${node}`, profileId: `prf_${key}`, nodeId: nid(node), nodeName: node, port, state: st, lastError });
  p.createInbound = async (req: { nodeId: string; validateOnly?: boolean; tlsServerNameOverride?: string; portOverride?: number }) => {
    const node = nodeOfId(req.nodeId);
    if (req.validateOnly) {
      if (v.has("pchk")) return NEVER;
      if (node === "nl2") throw err(`port_taken: port=${port}&profile=VLESS · TCP&free=${port + 1}`);
      if (node === "fi3" && !req.tlsServerNameOverride && key !== "aw1") throw err(`acme_needs_domain: address=${NODES.find((x) => x.n === "fi3")?.ip}`);
      if (node === "spb1") throw err("agent_too_old: the node's agent is older than this profile needs");
      return { inbound: { port: req.portOverride || port, tlsServerName: "" }, warnings: [], freePort: 0 };
    }
    if (v.has("hang") && node === "fi2") return NEVER;
    return { inbound: inboundOf(node, InboundState.PENDING), warnings: [] };
  };
  p.getProfile = async () => ({
    profile: undefined,
    settingsJson: "{}",
    inbounds: NODES.map((x) => (v.has("fail") && x.n === "fi2" ? inboundOf(x.n, InboundState.FAILED, `listen udp [::]:${port}: bind: address already in use`) : inboundOf(x.n, InboundState.ACTIVE))),
  });
  p.updateInbound = async (req: { validateOnly?: boolean; portOverride?: number }) => ({ inbound: { port: req.portOverride || port, tlsServerName: "" }, warnings: [], freePort: req.validateOnly ? port + 1000 : 0 });
  p.removeProfileFromNodes = async (req: { nodeIds: string[]; deleteProfile?: boolean }) => (v.has("run") ? NEVER : { removed: req.nodeIds.length, profileDeleted: !!req.deleteProfile });
  p.deleteProfile = async () => ({});
  p.twinProfile = async (req: { dryRun?: boolean; egress?: string; port?: number }) => {
    const to = req.egress || "warp";
    const base = { name: `${PROFILES.find((x) => x.id === `prf_${key}`)?.name ?? "Profile"} · ${to === "warp" ? "WARP" : "direct"}`, egress: to, port: port + 1, nodeIds: Object.keys(PROFILES.find((x) => x.id === `prf_${key}`)?.sv ?? {}).map(nid), groupIds: GROUPS.filter((x) => x.profileIds.includes(`prf_${key}`)).map((x) => x.id), hopDropped: false, portChecks: [] };
    return req.dryRun ? base : { ...base, profile: { id: `prf_${key}w`, name: base.name } };
  };
  n.restartInbounds = async () => ({});
  n.prepareAwgKernel = async () => ({ outcome: 1 });
  n.updateNode = async () => ({});
  g.updateGroup = async () => ({});
  g.createGroup = async () => ({});
  h.runChecksNow = async () => ({ scheduled: 1, skipped: 0 });
  h.getChecks = async () => (v.has("load") ? NEVER : v.has("err") ? Promise.reject(err("the panel did not answer", Code.Unavailable)) : NEVER);
}

/** The profile's data as a state of these tabs wants it: a server starting, an AmneziaWG status, a certificate about to end, a port of its own. */
export function tabsData(data: PageData, state: string): PageData {
  const v = new Set(tokens(state));
  // "free": the profile is on no node yet (an AmneziaWG one then has client networks it can still change)
  const inbounds = (v.has("free") ? [] : data.inbounds).map((i, k) => {
    const o = { ...i } as Record<string, unknown>;
    if (v.has("apply") && k === 0) o.state = InboundState.PENDING;
    if (i.protocol === "awg") o.awg = { backend: k === 0 ? "userspace" : "kernel", backendVersion: "", ifaceUp: true, peers: k === 0 ? 12 : 2, peersHandshaken: 12, peersOnline: k === 0 ? 7 : 1, newestHandshakeUnix: NOW - (k === 0 ? 60 : 360), udpRxPackets: 100, unknownPeerEvents: 0, reportedUnix: NOW };
    if (v.has("cert") && o.certNotAfterUnix) o.certNotAfterUnix = NOW + 5 * 86400;
    if (v.has("own") && k === 0) o.portOverride = 8444;
    if (v.has("own") && k === 0) o.port = 8444;
    if (v.has("own") && k === 1) o.directInSubscriptions = false;
    if (v.has("own") && k === 2) {
      o.state = InboundState.DISABLED;
      o.enabled = false;
    }
    return o;
  });
  const settingsJson = v.has("warp") ? JSON.stringify({ ...JSON.parse(data.initial.settingsJson), egress: "warp" }) : data.initial.settingsJson;
  return { ...data, inbounds: inbounds as never, initial: { ...data.initial, settingsJson } };
}

/** Seeds what a state of these tabs needs into the scene's query cache (and puts its calls on the shared client). */
export function seedTabs(qc: QueryClient, state: string, _role: Role): void {
  const v = new Set(tokens(state));
  fakeCalls(state);
  // WARP is up on one node only: the others are named where a WARP exit lacks it
  const nodes = nodeRows().map((n) => ({ ...n, awgBackend: "auto", warp: { state: n.name === "de1" ? WarpState.UP : WarpState.NOT_CONFIGURED }, ...(v.has("out") && n.name === "nl3" && { status: NodeStatus.DOWN }) }));
  qc.setQueryData(nodesQuery.queryKey, { nodes, expectedAgentVersion: "0.1.71" } as never);
  if (v.has("load") || v.has("err")) qc.removeQueries({ queryKey: checksQuery("").queryKey });
  if (v.has("nogroups")) qc.setQueryData(groupsQuery.queryKey, [] as never);
  // the plan of a copy (the server's dry run), under the key the window reads
  for (const to of ["warp", "direct"] as const) {
    const key = profileKey(state);
    const spec = PROFILES.find((x) => x.id === `prf_${key}`);
    if (!spec) continue;
    qc.setQueryData(twinPlanKey(spec.id, to, 1), { name: `${spec.name} · ${to === "warp" ? "WARP" : "direct"}`, egress: to, port: spec.port + 1, nodeIds: Object.keys(spec.sv).map(nid), groupIds: GROUPS.filter((x) => x.profileIds.includes(spec.id)).map((x) => x.id), hopDropped: false, portChecks: [] } as never);
  }
}

const f = (id: string, state: string, title: string, w = 1440, h = 900, params = ""): Frame => ({ id, state, title, w, h, ...(params && { params }) });

/** The frames of the tabs and windows: the gallery appends them to the page's own. */
export const tabFrames: Frame[] = [
  f("srv-hy1", "hy1.servers", "Servers: Hysteria2, one server did not start (1440)", 1440, 1100),
  f("srv-hy1-light", "hy1.servers", "The same on the light theme", 1440, 1100, "&theme=light"),
  f("srv-hy1-1000", "hy1.servers", "A 1000 window", 1000, 1300),
  f("srv-hy1-phone", "hy1.servers", "A phone, 400 wide", 400, 2200),
  f("srv-hy1-fa", "hy1.servers", "Right to left (fa)", 1440, 1100, "&lang=fa"),
  f("srv-hy1-ru", "hy1.servers", "Russian", 1440, 1100, "&lang=ru"),
  f("srv-menu", "hy1.servers.menu", "A row's menu", 1440, 1100),
  f("srv-ask", "hy1.servers.menu.ask", "The menu asks once more before it takes the server off", 1440, 1100),
  f("srv-edit", "hy1.servers.edit", "A server's port and domain, open in its row", 1440, 1100),
  f("srv-edit-port", "hy1.servers.edit.port", "\"Change the port\": the field holds a free one", 1440, 1100),
  f("srv-load", "hy1.servers.load", "The checks are loading", 1440, 900),
  f("srv-err", "hy1.servers.err", "The checks did not load", 1440, 900),
  f("srv-nochecks", "vl1.servers.nochecks", "No round has run yet", 1440, 900),
  f("srv-out", "aw2.servers.out", "A node out of touch", 1440, 900),
  f("srv-chk", "vl1.servers.chk", "\"Check now\": the walk, mid-way", 1440, 900),
  f("srv-chkd", "vl1.servers.chkd", "\"Check now\": just done", 1440, 900),
  f("srv-vl2", "vl2.servers", "A server starting and a check that fails", 1440, 1100),
  f("srv-apply", "vl2.servers.apply", "The apply course, mid-way", 1440, 1200),
  f("srv-applied", "vl1.servers.applied", "Applied on every node", 1440, 1200),
  f("srv-aw1", "aw1.servers", "AmneziaWG: devices and the backend", 1440, 900),
  f("srv-aw1-warp", "aw1.servers.warp", "A WARP exit: the nodes that lack WARP are named", 1440, 900),
  f("srv-own", "hy1.servers.own", "A port of its own, a server switched off, an address left out", 1440, 1100),
  f("srv-cert", "hy1.servers.cert", "A certificate about to end", 1440, 900),
  f("srv-tt1", "tt1.servers", "TrustTunnel", 1440, 900),
  f("srv-none", "hy3.servers", "On no node", 1440, 700),
  f("srv-helper", "hy1.servers", "A helper: the menu has the restart only", 1440, 1100, "&role=helper"),
  f("srv-readonly", "hy1.servers", "An observer: nothing to change", 1440, 1100, "&role=readonly"),
  f("ppl-hy1", "hy1.people", "People: who gets the profile", 1440, 800),
  f("ppl-hy1-light", "hy1.people", "The same on the light theme", 1440, 800, "&theme=light"),
  f("ppl-hy1-phone", "hy1.people", "A phone, 400 wide", 400, 1200),
  f("ppl-hy1-fa", "hy1.people", "Right to left (fa)", 1440, 800, "&lang=fa"),
  f("ppl-menu", "vl1.people.menu", "The \"Add to a group\" menu", 1440, 800),
  f("ppl-nogroup", "vl3.people", "In no group", 1440, 700),
  f("ppl-nogroups", "vl3.people.nogroups", "The panel has no group at all", 1440, 700),
  f("ppl-aw1", "aw1.people", "AmneziaWG", 1440, 700),
  f("ppl-tt1", "tt1.people", "TrustTunnel", 1440, 700),
  f("ppl-tg1", "tg1.people", "Telegram", 1440, 800),
  f("ppl-helper", "hy1.people", "A helper changes groups", 1440, 800, "&role=helper"),
  f("ppl-readonly", "hy1.people", "An observer reads", 1440, 800, "&role=readonly"),
  f("put-chk", "hy1.servers.put.pchk", "Put on nodes: the nodes are being checked", 1440, 900),
  f("put-pick", "hy1.servers.put", "Put on nodes: what each node says", 1440, 1100),
  f("put-pick-light", "hy1.servers.put", "The same on the light theme", 1440, 1100, "&theme=light"),
  f("put-pick-phone", "hy1.servers.put", "A phone", 400, 1600),
  f("put-pick-fa", "hy1.servers.put", "Right to left (fa)", 1440, 1100, "&lang=fa"),
  f("put-aw1", "aw1.servers.put", "The first AmneziaWG on a node: its backend", 1440, 1100),
  f("put-run", "hy1.servers.put.run.hang.out.on-de2+nl3+fi2", "Put on nodes: running", 1440, 800),
  f("put-done", "hy1.servers.put.run.out.on-de2+nl3+fi2", "Put on nodes: done, one waits for its node", 1440, 800),
  f("put-fail", "hy1.servers.put.run.fail.on-de2+fi2", "Put on nodes: one did not start", 1440, 800),
  f("take", "hy1.servers.take", "Take off nodes: nothing ticked", 1440, 800),
  f("take-some", "hy1.servers.take.pick-de1+nl1", "Take off nodes: two ticked, with the relay", 1440, 900),
  f("del", "hy1.servers.del", "Delete: every node ticked, the name asked", 1440, 900),
  f("del-typed", "hy1.servers.del.typed", "Delete: the name typed", 1440, 900),
  f("del-run", "hy1.servers.del.typed.run", "Delete: running", 1440, 800),
  f("del-light", "hy1.servers.del", "The same on the light theme", 1440, 900, "&theme=light"),
  f("del-phone", "hy1.servers.del", "A phone", 400, 1500),
  f("del-fa", "hy1.servers.del", "Right to left (fa)", 1440, 900, "&lang=fa"),
  f("del-err-deployed", "hy1.servers.del.typed.err-deployed", "Delete: the profile got a node meanwhile", 1440, 900),
  f("del-err-name", "hy1.servers.del.typed.err-name", "Delete: the name did not match", 1440, 900),
  f("del-err-stale", "hy1.servers.del.typed.err-stale", "Delete: the data changed", 1440, 900),
  f("del-err-gone", "hy1.servers.del.typed.err-gone", "Delete: already deleted", 1440, 900),
  f("del-err-partial", "hy1.servers.take.pick-de1+nl1+fi1.err-partial", "Take off: not every node answered", 1440, 900),
  f("del-none", "hy3.servers.del", "Delete a profile that is on no node", 1440, 700),
  f("del-aw1", "aw1.servers.del.typed", "Delete an AmneziaWG profile", 1440, 900),
  f("twin-warp", "aw1.servers.twin", "A copy through WARP: the nodes without WARP can be skipped", 1440, 900),
  f("twin-direct", "aw1.servers.warp.twin", "A copy without WARP", 1440, 800),
  f("twin-done", "aw1.servers.twin.done", "The copy is made", 1440, 900),
];
