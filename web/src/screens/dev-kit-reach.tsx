import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { blocklist, bsbord, cheburcheck, clientProbe, countryReach, globalping, health, nodes as nodesApi, ownProbe, tspuProbe } from "@/lib/api";
import { blocklistQuery } from "@/lib/blocklist";
import { bsbordQuery, unitsQuery } from "@/lib/bsbord";
import { cheburcheckQuery } from "@/lib/cheburcheck";
import { clientProbeQuery } from "@/lib/clientprobe";
import { editionFeatures } from "@/i18n/edition";
import { countryQuery, globalpingQuery } from "@/lib/globalping";
import { alertsQuery, checksQuery } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { ownProbeQuery } from "@/lib/ownprobe";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { history6Query, historyQuery } from "@/lib/reachability";
import { meQuery } from "@/lib/session";
import { useLang } from "@/i18n";
import { tspuQuery } from "@/lib/tspu";
import { updatesQuery } from "@/lib/updates";
import { ReachabilityScreen } from "@/screens/reachability";
import { ReachNav } from "@/screens/reachability/reach-nav";
import type { ReachSearch } from "@/screens/reachability/search";
import { Shot } from "@/screens/dev-kit-redesign";
import { useTheme } from "@/lib/theme";
import { alertsOf, checksOf, clientProbeOf, flagsOf, fiState, historyOf, NOW, nid, nodesOf, probeList, serverOf, tspuNodes, units, type Flags, type Srv } from "./dev-kit-reach-data";
import { applyScene, SceneGuard } from "./dev-kit-scene";
import { ShellLayout } from "./shell";

// Development only: the Reachability page of the redesign (PnReach) in every state of the board and of the product, each in a frame of its own
// width (an iframe, so the window's breakpoints answer to the frame). /dev-kit?only=pn-reach lists the frames; each is a page of its own:
//   /dev-kit?only=reach-page&state=<tab>[.<word>…][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// <tab>: nodes | events | sources. The words of the board: the data nosrc | nonodes | first | load | err; the fi1 row allok | bad | reg | tcp (the
// default is "some networks"); the networks of the open row many (twelve) | long (the names of live data); noreports (kl!ck has nothing on fi1); the sources' trouble gperr | gplim | bsbud | tsoff; the choices src.<id> (gp bl ts cc bs kl pr) | open.<node> |
// filter.<state> (ok part tcp bad reg nd); what a hand did dlgoff | toast | check | test | replace | enable. The product's own: v6 | dark | probes | kz | edge | ro, and
// the windows of the own probes dlgadd | dlgupd | dlgrev. Made-up data: the nodes de1, fi1, msk1, nl1, the operators A to E, the documentation networks.
// The scene's own "panel" answers the page's calls (nothing leaves the page) and keeps what a click changes, so a switch moves the page.

const wait = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const never = () => new Promise<never>(() => {});
const sha = "0a1b2c3d4e5f".padEnd(64, "0");
const install = "sh mgp.sh install 'https://panel.example.com/mgp-kb3x2o7q5vwiz4nrp6sleu5hfa' 'mgp_kb3x2o7q5vwiz4nrp6sleu5hfa2o7q5vwiz4nrp6sleu5hf'";

/** The panel behind the scene: the calls of the page, answered from the made-up Server; a change a hand makes is kept in it. */
export function fakePanel(f: Flags, srv: Srv) {
  const patch = (client: object, methods: Record<string, unknown>) => Object.assign(client as Record<string, unknown>, methods);
  const slow = f.v.has("load");
  let first = f.v.has("err");
  const gate = async () => {
    if (slow) return never();
    if (first) {
      first = false;
      await wait(150);
      throw new ConnectError("unavailable", Code.Unavailable);
    }
  };
  let retried = false;
  // the week's alerts fail the same way: once, and the retry shows the grey cards for a moment
  let alertsFirst = f.v.has("err");
  patch(countryReach, {
    getCountryReachability: async () => ({ settings: { country: srv.country } }),
    updateCountryReachability: async (req: { country: string }) => {
      srv.country = req.country;
      return { settings: { country: srv.country } };
    },
    getReachabilityHistory: async (req: { family?: number }) => {
      await gate();
      if (f.v.has("err") && !retried) {
        retried = true;
        await wait(1400);
      }
      return historyOf(f, srv, req.family === 6 ? 6 : 4);
    },
  });
  patch(health, {
    getChecks: async () => checksOf(f, srv),
    listAlerts: async () => {
      if (slow) return never();
      if (alertsFirst) {
        alertsFirst = false;
        await wait(150);
        throw new ConnectError("unavailable", Code.Unavailable);
      }
      if (f.v.has("err")) await wait(1400);
      return alertsOf(f, srv);
    },
  });
  patch(nodesApi, { listNodes: async () => nodesOf(f, srv) });

  // Globalping
  const gp = () => ({ ...srv.globalping });
  patch(globalping, {
    getGlobalping: async () => ({ settings: gp() }),
    updateGlobalping: async (req: { enabled?: boolean; intervalMin?: number; token?: string; ipv6?: boolean }) => {
      await wait(250);
      if (req.token !== undefined) srv.globalping.hasToken = true;
      Object.assign(srv.globalping, { ...(req.enabled !== undefined && { enabled: req.enabled }), ...(req.intervalMin && { intervalMin: req.intervalMin }), ...(req.ipv6 !== undefined && { ipv6: req.ipv6 }) });
      return { settings: gp() };
    },
    disconnectGlobalping: async () => {
      await wait(250);
      Object.assign(srv.globalping, { enabled: false, hasToken: false, ipv6: false });
      return { settings: gp() };
    },
    testGlobalping: async () => {
      await wait(f.v.has("check") ? 60_000 : 900);
      if (f.v.has("gperr")) throw new ConnectError("globalping_unauthorized", Code.FailedPrecondition);
      return { reachable: true, tokenUsed: true, limit: 500, remaining: 431, resetS: 1800, credits: 0 };
    },
  });

  // the block lists
  const lists = () => ({ enabled: srv.lists.enabled, available: srv.country === "RU", checkedUnix: srv.lists.enabled ? NOW - 35 * 60 : 0, listsUnix: NOW - 3 * 86400 });
  patch(blocklist, {
    getBlocklist: async () => ({ settings: lists() }),
    updateBlocklist: async (req: { enabled: boolean }) => {
      await wait(250);
      srv.lists.enabled = req.enabled;
      return { settings: lists() };
    },
    disconnectBlocklist: async () => {
      await wait(250);
      srv.lists.enabled = false;
      return { settings: lists() };
    },
    testBlocklist: async () => {
      await wait(f.v.has("check") ? 60_000 : 900);
      return { listsUnix: NOW - 3 * 86400, prefixes: 184233, cdnRanges: 612, nodesChecked: 4, nodesListed: fiState(f) === "reg" ? 1 : 0, mirrorUsed: false };
    },
  });

  // the TSPU probe
  const run = { status: "ok", atUnix: NOW - 48 * 60, dpiHop: 6, blockedRef: "198.51.100.4", nodesChecked: 4, detail: "", reason: "" };
  const tspu = () => ({
    enabled: srv.tspu.enabled, available: srv.country === "RU", unavailableReason: srv.country === "RU" ? "" : "country", probeNodeId: srv.tspu.probeNodeId, intervalMin: srv.tspu.intervalMin, nodes: tspuNodes(f),
    lastRun: srv.tspu.enabled && srv.tspu.probeNodeId ? run : undefined,
  });
  patch(tspuProbe, {
    getTspuProbe: async () => ({ settings: tspu() }),
    updateTspuProbe: async (req: { enabled?: boolean; probeNodeId?: string; intervalMin?: number }) => {
      await wait(250);
      if (req.enabled !== undefined) srv.tspu.enabled = req.enabled;
      if (req.probeNodeId !== undefined) srv.tspu.probeNodeId = req.probeNodeId;
      if (req.intervalMin) srv.tspu.intervalMin = req.intervalMin;
      return { settings: tspu() };
    },
    disconnectTspuProbe: async () => {
      await wait(250);
      Object.assign(srv.tspu, { enabled: false, probeNodeId: "" });
      return { settings: tspu() };
    },
    testTspuProbe: async () => {
      await wait(f.v.has("check") ? 60_000 : 1500);
      return { run: { ...run, atUnix: NOW }, nodes: ["de1", "fi1", "nl1"].map((n) => ({ nodeId: nid(n), name: n, verdict: n === "fi1" && fiState(f) !== "ok" ? "blocked" : "open", ports: [{ port: 443, verdict: "open" }] })) };
    },
  });

  // cheburcheck
  const cc = () => ({ ...srv.cheburcheck, unavailable: srv.country === "RU" ? "" : "country", lastVerdict: "ok", tokenUnknown: false });
  patch(cheburcheck, {
    getCheburcheck: async () => ({ settings: cc() }),
    updateCheburcheck: async (req: { enabled?: boolean; token?: string; publicEnabled?: boolean; intervalMin?: number; ownIntervalMin?: number }) => {
      await wait(250);
      if (req.token !== undefined) srv.cheburcheck.hasToken = req.token !== "";
      Object.assign(srv.cheburcheck, { ...(req.enabled !== undefined && { enabled: req.enabled }), ...(req.publicEnabled !== undefined && { publicEnabled: req.publicEnabled }), ...(req.intervalMin && { intervalMin: req.intervalMin }), ...(req.ownIntervalMin && { ownIntervalMin: req.ownIntervalMin }) });
      return { settings: cc() };
    },
    disconnectCheburcheck: async () => {
      await wait(250);
      Object.assign(srv.cheburcheck, { enabled: false, hasToken: false });
      return { settings: cc() };
    },
    testCheburcheck: async () => {
      await wait(f.v.has("check") ? 60_000 : 900);
      return { reachable: true };
    },
  });

  // bsbord
  const bs = () => ({ ...srv.bsbord, spendMonth: "2026-11", reservedCredits: 0, holdUntilUnix: 0, pending: false, capCredits: BigInt(srv.bsbord.capCredits), spentCredits: BigInt(srv.bsbord.spentCredits) });
  patch(bsbord, {
    getBsbord: async () => {
      if (srv.country !== "RU") throw new ConnectError("bsbord_country_unavailable", Code.Unavailable);
      return { settings: bs() };
    },
    updateBsbord: async (req: { enabled?: boolean; key?: string; intervalMin?: number; capCredits?: bigint; units?: { opKeys: string[] } }) => {
      await wait(250);
      if (req.key !== undefined) srv.bsbord.hasKey = true;
      if (req.units) srv.bsbord.units = req.units.opKeys;
      if (req.capCredits !== undefined) srv.bsbord.capCredits = Number(req.capCredits);
      Object.assign(srv.bsbord, { ...(req.enabled !== undefined && { enabled: req.enabled }), ...(req.intervalMin && { intervalMin: req.intervalMin }) });
      return { settings: bs() };
    },
    disconnectBsbord: async () => {
      await wait(250);
      Object.assign(srv.bsbord, { enabled: false, hasKey: false, units: [] });
      return { settings: bs() };
    },
    testBsbord: async () => {
      await wait(f.v.has("check") ? 60_000 : 900);
      return { balanceTotal: 52340, bonusCredits: 5000, tier: "standard", nProbeable: 5 };
    },
    previewBsbord: async () => {
      await wait(500);
      return { costCredits: 160, monthlyCredits: 4800, targetCount: 4, unitCount: srv.bsbord.units.length || 3, capReached: false };
    },
    listBsbordUnits: async () => ({ units, nUnits: units.length, nProbeable: 5 }),
  });

  // the reports of kl!ck users
  patch(clientProbe, {
    getClientProbe: async () => clientProbeOf(f, srv),
    updateClientProbe: async (req: { enabled: boolean }) => {
      await wait(250);
      srv.klick.enabled = req.enabled;
      return { settings: clientProbeOf(f, srv).settings };
    },
    disconnectClientProbe: async () => {
      await wait(250);
      srv.klick.enabled = false;
      return { settings: clientProbeOf(f, srv).settings };
    },
  });

  // the own probes
  const ownSettings = () => ({ enabled: srv.probes.enabled, intervalMin: srv.probes.intervalMin, fallbackUrl: srv.probes.fallbackUrl });
  patch(ownProbe, {
    get: async () => ({ settings: ownSettings(), probes: srv.probes.list, scriptVersion: "0a1b2c3d4e5f" }),
    update: async (req: { settings: { enabled: boolean; intervalMin: number; fallbackUrl: string } }) => {
      await wait(250);
      Object.assign(srv.probes, req.settings);
      return { settings: ownSettings() };
    },
    disconnect: async () => {
      await wait(250);
      Object.assign(srv.probes, { enabled: false, list: [] });
      return {};
    },
    revoke: async (req: { id: string }) => {
      await wait(250);
      srv.probes.list = srv.probes.list.map((p) => ((p as { id: string }).id === req.id ? { ...p, state: "revoked" } : p));
      return {};
    },
    create: async (req: { name: string; city: string; kind: string }) => {
      await wait(500);
      const made = { ...probeList()[2]!, ...req, id: "prb_new0001" };
      srv.probes.list = [...srv.probes.list, made];
      return { probe: made, token: "x", command: install, serverCommand: install, scriptSha256: sha };
    },
    checkNow: async () => ({}),
    confirmNetwork: async () => ({}),
    updateCommand: async () => ({ command: install, serverCommand: install, scriptVersion: "0a1b2c3d4e5f" }),
  });
}

const me = (role: number, edge: boolean) => ({
  admin: { id: "adm_1", displayName: "owner", login: "owner", role },
  version: "0.1.71",
  sourceUrl: "https://github.com/example/mistgate",
  sourceStars: 298,
  stepUpUntilUnix: 0n,
  edition: edge ? "edge" : "vps",
  unavailable: edge ? [...editionFeatures] : [],
});

/** The private query cache of a scene (exported for its smoke test): the shell's and the page's own queries, seeded; nothing else is asked of anyone. */
export function seeded(state: string, role: Role) {
  const f = flagsOf(state);
  const srv = serverOf(f);
  fakePanel(f, srv);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
  const set = (key: readonly unknown[], data: unknown) => c.setQueryData(key, data as never);
  const list = nodesOf(f, srv);
  set(meQuery.queryKey, me(role, f.v.has("edge")));
  set(nodesQuery.queryKey, { nodes: list.nodes, expectedAgentVersion: list.expectedAgentVersion });
  set(overviewQuery().queryKey, { nodesTotal: list.nodes.length, nodes: list.nodes.map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: NodeStatus.ONLINE, online: [], sparkBytes: [] })), alertsActive: 0, alertsCritical: 0 });
  set(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } });
  set(awaitingQuery.queryKey, { awaiting: 0 });
  set(updatesQuery.queryKey, { nowUnix: NOW, panel: { version: "v0.1.71", built: NOW, update: { version: "v0.1.71", url: "", checkedUnix: NOW, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: NOW }, nodes: [] });
  // a page that loads or fails asks the panel itself; the rest is in the cache from the start
  if (!f.v.has("load") && !f.v.has("err")) {
    set(historyQuery.queryKey, historyOf(f, srv));
    set(alertsQuery.queryKey, alertsOf(f, srv));
  }
  if (srv.globalping.ipv6) set(history6Query.queryKey, historyOf(f, srv, 6));
  set(checksQuery().queryKey, checksOf(f, srv));
  set(countryQuery.queryKey, { country: srv.country });
  set(globalpingQuery.queryKey, { ...srv.globalping });
  set(blocklistQuery.queryKey, { enabled: srv.lists.enabled, available: srv.country === "RU", checkedUnix: srv.lists.enabled ? NOW - 35 * 60 : 0, listsUnix: NOW - 3 * 86400 });
  set(tspuQuery.queryKey, { enabled: srv.tspu.enabled, available: srv.country === "RU", unavailableReason: srv.country === "RU" ? "" : "country", probeNodeId: srv.tspu.probeNodeId, intervalMin: srv.tspu.intervalMin, nodes: tspuNodes(f), lastRun: srv.tspu.enabled && srv.tspu.probeNodeId ? { status: "ok", atUnix: NOW - 48 * 60, dpiHop: 6, blockedRef: "198.51.100.4", nodesChecked: 4, detail: "", reason: "" } : undefined });
  set(cheburcheckQuery.queryKey, { ...srv.cheburcheck, unavailable: srv.country === "RU" ? "" : "country", lastVerdict: "ok", tokenUnknown: false });
  if (srv.country === "RU") set(bsbordQuery.queryKey, { ...srv.bsbord, spendMonth: "2026-11", reservedCredits: 0, holdUntilUnix: 0, pending: false });
  set(unitsQuery.queryKey, { units, nUnits: units.length, nProbeable: 5 });
  set(clientProbeQuery.queryKey, clientProbeOf(f, srv));
  set(ownProbeQuery.queryKey, { settings: { enabled: srv.probes.enabled, intervalMin: srv.probes.intervalMin, fallbackUrl: "" }, probes: srv.probes.list, scriptVersion: "0a1b2c3d4e5f" });
  return c;
}

/** What a hand does in a scene, once the page is there: presses the button of a source (a Test that runs, a Pause, a dialog). */
const presses = (f: Flags): [string, string][] => {
  const src = f.src ?? "globalping";
  const out: [string, string][] = [];
  if (f.v.has("gperr")) out.push(["globalping", "test"]);
  if (f.v.has("check") || f.v.has("test")) out.push([src, "test"]);
  // the problem box's button comes once the failed Test has been answered (a second and a half)
  if (f.v.has("replace")) out.push(["globalping", "replace"]);
  if (f.v.has("dlgoff")) out.push([src, "disconnect"]);
  if (f.v.has("toast")) out.push([src, "pause"]);
  if (f.v.has("enable")) out.push([src, "enable"]);
  if (f.v.has("dlgadd")) out.push(["probes", "add"]);
  if (f.v.has("dlgupd")) out.push(["probes", "update"]);
  if (f.v.has("dlgrev")) out.push(["probes", "revoke"]);
  return out;
};

/** One whole page of the scene: the page in the real shell, at the window's own width. */
export function ReachScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "nodes";
  const f = flagsOf(state);
  const roleWord = params.get("role") ?? (f.v.has("ro") ? "readonly" : "owner");
  const role = roleWord === "helper" ? Role.HELPER : roleWord === "readonly" ? Role.READONLY : Role.OWNER;
  const [qc] = useState(() => {
    applyScene(params);
    return seeded(state, role);
  });
  const [search, setSearch] = useState<ReachSearch>({ ...(f.tab !== "nodes" ? { tab: f.tab } : {}), ...(f.src ? { source: f.src } : {}) });
  // a hand's first moves, a moment after the page is drawn
  const [moves] = useState(() => presses(f));
  useEffect(() => {
    const timers = moves.map(([source, act], i) => window.setTimeout(() => document.querySelector<HTMLElement>(`[data-source="${source}"] [data-act="${act}"]`)?.click(), 700 + i * 500 + (act === "replace" ? 1000 : 0)));
    return () => timers.forEach((x) => window.clearTimeout(x));
  }, [moves]);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <SceneGuard>
            <ShellLayout current="reachability" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <ReachNav.Provider value={{ search, go: setSearch }}>
                <ReachabilityScreen scene={{ filter: f.filter, open: f.open ? nid(f.open) : undefined }} />
              </ReachNav.Provider>
            </ShellLayout>
          </SceneGuard>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const frame = (id: string, state: string, title: string, w = 1440, h = 1100, params?: string): Frame => ({ id, state, title, w, h, params });
export const frames: Frame[] = [
  // the look in every width, theme and direction
  frame("reach-nodes", "nodes", "Nodes: the summary, the table, the last events and the sources (1440, dark)"),
  frame("reach-nodes-light", "nodes", "The same on the light theme", 1440, 1100, "&theme=light"),
  frame("reach-nodes-1000", "nodes", "A 1000 window: the rail, the words of the sources go into the open row", 1000, 1100),
  frame("reach-nodes-phone", "nodes", "A phone, 400 wide: a row is a card", 400, 1700),
  frame("reach-nodes-fa", "nodes", "Right to left (fa)", 1440, 1100, "&lang=fa"),
  // "Check now" has the country check run out of turn for a node: its radar in the row and the words under its verdict
  frame("reach-now-running", "nodes.open.fi1.cnrunning", "Check now: the round runs, the radar in the row, what is asked and what is not under the verdict", 1440, 1500, "&fx=1"),
  frame("reach-now-done", "nodes.open.fi1.cndone", "Check now: the round has ended, the own probes' job waits", 1440, 1500),
  frame("reach-now-early", "nodes.open.fi1.cnearly", "Check now: pressed too early, the next recheck in 7 min", 1440, 1500),
  frame("reach-now-nobudget", "nodes.open.fi1.cnnobudget", "Check now: Globalping skipped for the tests left, the others went on", 1440, 1500),
  frame("reach-now-running-phone", "nodes.open.fi1.cnrunning", "Check now: the round runs, on a phone, 400 wide", 400, 2600, "&fx=1"),
  frame("reach-events", "events", "Events: the week's alerts of the country"),
  frame("reach-events-light", "events", "The same on the light theme", 1440, 700, "&theme=light"),
  frame("reach-events-phone", "events", "Events on a phone", 400, 800),
  frame("reach-sources", "sources", "Sources: the list and the open one (Globalping)"),
  frame("reach-sources-light", "sources", "The same on the light theme", 1440, 1100, "&theme=light"),
  frame("reach-sources-1000", "sources", "A 1000 window: the list is a row of tiles above the open source", 1000, 1300),
  frame("reach-sources-phone", "sources", "Sources on a phone: tiles that scroll sideways, the open source under them", 400, 1900),
  frame("reach-sources-fa", "sources", "Sources, right to left (fa)", 1440, 1100, "&lang=fa"),
  // the board's states: the data
  frame("reach-nosrc", "nodes.nosrc", "Data: no sources are connected", 1440, 700),
  frame("reach-nosrc-events", "events.nosrc", "No sources: the Events tab says so", 1440, 600),
  frame("reach-events-first", "events.first", "Events while the first round runs", 1440, 600),
  frame("reach-events-nonodes", "events.nonodes", "Events with no nodes yet", 1440, 600),
  frame("reach-events-allok", "events.allok", "Events: nothing in the week (every node opens)", 1440, 600),
  frame("reach-events-load", "events.load", "Events: loading (the grey cards)", 1440, 600),
  frame("reach-events-err", "events.err", "Events: the week did not load (and Try again works)", 1440, 600),
  frame("reach-nosrc-sources", "sources.nosrc", "No sources: every one is off, none has a token", 1440, 1000),
  frame("reach-first-sources", "sources.first", "Sources while the first round runs: every one is on, none has said anything", 1440, 1000),
  frame("reach-nosrc-enable", "sources.nosrc.src.gp.enable", "No sources: Globalping is switched on, the toast, the first round", 1440, 1000),
  frame("reach-nonodes", "nodes.nonodes", "Data: no nodes yet", 1440, 700),
  frame("reach-first", "nodes.first", "Data: the first round of checks is running", 1440, 1000),
  frame("reach-load", "nodes.load", "Data: loading (the grey cards)", 1440, 700),
  frame("reach-err", "nodes.err", "Data: the page did not load (and Try again works)", 1440, 700),
  // the board's states: the fi1 row
  frame("reach-allok", "nodes.allok", "Nodes: all reachable"),
  frame("reach-bad", "nodes.bad", "Nodes: fi1 is unreachable"),
  frame("reach-reg", "nodes.reg", "Nodes: fi1 is in the registry"),
  frame("reach-tcp", "nodes.tcp", "Nodes: TCP is cut on fi1"),
  frame("reach-open", "nodes.open.fi1", "A row opened: the sources, the reports of kl!ck, the operators, the regions, the week", 1440, 1500),
  frame("reach-open-phone", "nodes.open.fi1", "The opened row on a phone", 400, 2200),
  frame("reach-open-1000", "nodes.open.fi1", "The opened row, 1000: the sources as words in it", 1000, 1500),
  // the open row (the board's r3) in every state of the fi1 row, in both themes, narrow, and with the long data
  frame("reach-open-allok", "nodes.allok.open.fi1", "The opened row: every network sees the node", 1440, 1400),
  frame("reach-open-tcp", "nodes.tcp.open.fi1", "The opened row: TCP is cut, the probe node does not see it", 1440, 1500),
  frame("reach-open-bad", "nodes.bad.open.fi1", "The opened row: no network sees the node", 1440, 1500),
  frame("reach-open-reg", "nodes.reg.open.fi1", "The opened row: reachable, the address is in the registry", 1440, 1500),
  frame("reach-open-nd", "nodes.first.open.fi1", "The opened row: no data yet (the first round)", 1440, 1100),
  frame("reach-open-noreports", "nodes.noreports.open.fi1", "The opened row: kl!ck has no reports on the node", 1440, 1300),
  frame("reach-open-light", "nodes.open.fi1", "The opened row on the light theme", 1440, 1500, "&theme=light"),
  frame("reach-open-bad-light", "nodes.bad.open.fi1", "The opened row, unreachable, on the light theme", 1440, 1500, "&theme=light"),
  frame("reach-open-many", "nodes.many.open.fi1", "The opened row: twelve networks", 1440, 2000),
  frame("reach-open-long", "nodes.long.open.fi1", "The opened row: long names of operators, cities and probes", 1440, 1900),
  frame("reach-open-long-phone", "nodes.long.open.fi1", "The opened row, long names, on a phone, 400 wide", 400, 3400),
  frame("reach-open-many-phone", "nodes.many.open.fi1", "The opened row, twelve networks, on a phone, 400 wide", 400, 3400),
  frame("reach-open-bad-phone", "nodes.bad.open.fi1", "The opened row, unreachable, on a phone, 400 wide", 400, 3000),
  frame("reach-open-fa", "nodes.open.fi1", "The opened row, right to left (fa)", 1440, 1500, "&lang=fa"),
  frame("reach-filter", "nodes.filter.part", "A chip of the summary is pressed: only the nodes in that state", 1440, 800),
  frame("reach-filter-none", "nodes.filter.part.allok", "...and nobody is in that state (no such nodes)", 1440, 800),
  // the board's states: the sources' trouble
  frame("reach-gperr", "sources.gperr.src.gp", "Sources: Globalping does not take the token (a Test fails)", 1440, 1200),
  frame("reach-gperr-replace", "sources.gperr.src.gp.replace", "...and \"Replace the token\" opens the field", 1440, 1300),
  frame("reach-gplim", "nodes.gplim", "Sources: the hourly limit is used up: the note over the nodes", 1440, 1200),
  frame("reach-gplim-src", "sources.gplim.src.gp", "...and the problem in the source", 1440, 1200),
  frame("reach-bsbud", "sources.bsbud.src.bs", "Sources: the month's budget is spent", 1440, 1500),
  frame("reach-tsoff", "sources.tsoff.src.ts", "Sources: the probe node is not in touch", 1440, 1100),
  frame("reach-tsoff-nodes", "nodes.tsoff", "...and the note over the nodes", 1440, 1000),
  // the board's states: the choice of a source
  frame("reach-src-bl", "sources.src.bl", "Source: the block lists", 1440, 900),
  frame("reach-src-ts", "sources.src.ts", "Source: the TSPU probe", 1440, 1000),
  frame("reach-src-cc", "sources.src.cc", "Source: cheburcheck", 1440, 1000),
  frame("reach-src-bs", "sources.src.bs", "Source: bsbord: the spend, the units", 1440, 1700),
  frame("reach-src-kl", "sources.src.kl", "Source: the reports of kl!ck users", 1440, 1000),
  frame("reach-src-pr", "sources.src.pr", "Source: own probes, off", 1440, 800),
  // what a hand does
  frame("reach-dlgoff", "sources.src.gp.dlgoff", "\"Disconnect Globalping?\"", 1440, 900),
  frame("reach-dlgoff-phone", "sources.src.gp.dlgoff", "The same window on a phone (a sheet)", 400, 900),
  frame("reach-toast", "sources.src.bl.toast", "Pause: the toast with Undo", 1440, 900),
  frame("reach-test-done", "sources.src.gp.test", "A Test went through: the toast, the line of the source", 1440, 1000),
  frame("reach-check", "sources.src.gp.check", "A Test is running: the spinner in the button", 1440, 1000),
  frame("reach-check-ts", "sources.src.ts.check", "A TSPU run takes minutes: the spinner, the pill", 1440, 1000),
  frame("reach-ro", "nodes.ro", "An observer: the table and the events, no country, no sources", 1440, 1000),
  frame("reach-ro-sources", "sources.ro", "An observer: the Sources tab says who changes them", 1440, 700),
  frame("reach-helper", "nodes.open.fi1", "A helper: the same, no buttons of the owner", 1440, 1300, "&role=helper"),
  // the product's own
  frame("reach-v6", "nodes.v6.open.fi1", "IPv6 is checked too: the family, the lane, the hint for the entry", 1440, 1600),
  frame("reach-v6-light", "nodes.v6.open.fi1", "The same on the light theme", 1440, 1600, "&theme=light"),
  frame("reach-dark", "nodes.dark", "A network that reaches none of the nodes", 1440, 1100),
  frame("reach-probes", "sources.src.pr.probes", "Own probes: the list, the settings, \"Add a probe\"", 1440, 1500),
  frame("reach-probes-light", "sources.src.pr.probes", "The same on the light theme", 1440, 1500, "&theme=light"),
  frame("reach-probes-add", "sources.src.pr.probes.dlgadd", "The window that adds a probe", 1440, 900),
  frame("reach-probes-update", "sources.src.pr.probes.dlgupd", "The update command of a probe", 1440, 900),
  frame("reach-probes-revoke", "sources.src.pr.probes.dlgrev", "Revoke asks first", 1440, 900),
  frame("reach-kz", "sources.src.bl.kz", "Another country than Russia: the sources that look from Russia only say so", 1440, 900),
  frame("reach-kz-nodes", "nodes.kz", "...and the page head names it", 1440, 900),
  frame("reach-edge", "sources.src.pr.edge", "The Cloudflare edition: no own probes, no TSPU probe", 1440, 800),
  frame("reach-consent", "sources.nosrc.src.cc.enable", "cheburcheck: what leaves the panel is asked before Switch on", 1440, 900),
];

export function PnReachKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Reachability</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((fr) => (
        <Shot key={fr.id} id={fr.id} title={fr.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={fr.title} src={`${path}?only=reach-page&state=${fr.state}${fr.params ?? ""}`} width={fr.w} height={fr.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
