import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { StepUpProvider } from "@/components/step-up";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { BundleStatus, NodeUpdateState, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { backups, updates as updatesApi } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { awaitingQuery } from "@/lib/integrations";
import { osUpdatesQuery } from "@/lib/os-updates";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { Shot } from "@/screens/dev-kit-redesign";
import {
  agentNotes,
  bundle,
  CUR,
  extraNodes,
  fi1Old,
  flagOf,
  NEW,
  NOW,
  osData,
  panelNotes,
  rollout,
  step,
  updatesData,
  upNode,
  type OsKind,
  type PanelKind,
} from "@/screens/dev-kit-updates-data";
import { UpdatesView, type UpdatesScene } from "@/screens/updates";
import type { PanelPhase } from "@/screens/updates/panel";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-updates: the Updates screen of the redesign (PnUpdates) in every state of the board, each in a frame of its
// own width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for
// the side-by-side with the board:
//   /dev-kit?only=pn-updates-page&state=<tab>.<state>[.<what is open>][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly]
//                                 [&fleet=0|1|4|12][&issues=0|1|4|9][&fx=1]
// <tab>: pn (this panel) | ag (the node agents) | os (the servers' systems).
// pn.<state>: new (a release is out: the board's "есть обновление") | cur (up to date) | run0 … run3 (installing, at the step: the takeover screen)
//   | runx (installing, the step is not known: what the page really sees) | slow (no answer for two minutes) | out (the new version answered: the screen
//   holds still) | done (just updated) | back (went back) | gh (GitHub did not answer) | sig (the signature did not match) | load | err
//   | chan (the root policy does not allow the channel) | unsup (cannot update itself: no systemd) | edge (the Cloudflare edition).
//   what is open: det (the details) | more (earlier versions) | ghbusy (asking GitHub) | dlg-upd | dlg-upd-busy | dlg-upd-done (the window; a backup running; made) |
//   dlg-ch | dlg-ch-test | dlg-ch-dev (the channel window, a channel picked).
// ag.<state>: new (one node to update, the canary leads) | roll0 … roll2 (the rollout at its step) | fail (the canary did not pass) | cur (all up to date)
//   | curjust (the canary has just finished) | off (a node is offline) | none (no nodes) | paused (the owner paused) | nobundle | untrusted | manual | edge.
//   n<count> (any of them with more nodes: the board's twelve are "ag.new.n12"; from seven nodes the cards give way to the matrix) | wait (every third node of the
//   extra ones waits for the update) | mix (one of each state a node can be in, a rollout running) | open (the matrix unfolded).
//   what is open: last (the stages of the last rollout) | pkbusy (the folder is read) | named (ag.fail: the gate names the profiles that did not pass) | rs-<answer> (a click on "read the folder again" answered: fresh | installed | downloading | failed | wait-rollout | wait-os_rollout | wait-panel_update | wait-other) | dlg-roll | dlg-sch (the window of fi1: its schedule) | dlg-sch-now | dlg-sch-new
//   (no schedule yet, "now") | dlg-sch-new-at | dlg-rollback | dlg-cancel | howto.
// os.<state>: new (de1 needs a reboot, nl1 has security updates; de1 open) | calm | fail (the install stopped) | off (nl1 offline) | busy (installing) | just (just done)
//   what is open: closed (no row open) | tick (two ticked) | dlg-sec | dlg-reboot (the windows of de1).
// Made-up data of the board: the panel v0.1.71 and the release v0.1.86, the nodes de1, fi1, nl1 and msk1, documentation addresses.

type Tab = "panel" | "agents" | "os";
type Parsed = { tab: Tab; kind: string; mods: Set<string> };

export function parse(state: string): Parsed {
  const [t = "pn", kind = "new", ...mods] = state.split(".");
  return { tab: t === "ag" ? "agents" : t === "os" ? "os" : "panel", kind, mods: new Set(mods) };
}

const POOL = [
  ["de1", "DE"], ["fi1", "FI"], ["nl1", "NL"], ["msk1", "RU"], ["de2", "DE"], ["fr1", "FR"], ["se1", "SE"], ["pl1", "PL"], ["no1", "NO"], ["lv1", "LV"], ["lt1", "LT"], ["at1", "AT"],
] as const;

/** The nodes of the shell (the menu's counter, the fleet button) and the alerts under it: the board's "fleet" and "issues". Nodes the page itself names (a big fleet) join them. */
function shellSeed(qc: QueryClient, fleet: number, issues: number, more: readonly string[] = []) {
  const named: (readonly [string, string])[] = [...POOL.slice(0, fleet), ...more.filter((n) => !POOL.slice(0, fleet).some(([p]) => p === n)).map((n) => [n, flagOf(n)] as const)];
  const cards = named.map(([name, cc], i) => ({
    id: `nod_${name}`,
    name,
    countryCode: cc,
    location: "",
    status: i < Math.min(issues, fleet) ? (i === 0 ? NodeStatus.DOWN : NodeStatus.ONLINE) : NodeStatus.ONLINE,
    online: [],
    sparkBytes: [],
  }));
  qc.setQueryData(nodesQuery.queryKey, { nodes: cards.map((c) => ({ ...c, address: `${c.name}.example.com`, version: CUR })), expectedAgentVersion: "0.1.71" } as never);
  qc.setQueryData(overviewQuery().queryKey, { nodesTotal: cards.length, nodes: cards, alertsActive: issues, alertsCritical: issues > 0 ? 1 : 0 } as never);
}

/** What the page is given for a state: the panel's answer, the servers' answer and the page's own scene. */
export function build(p: Parsed): { data: unknown; os: unknown; scene: UpdatesScene; edition?: { edition: string; unavailable: string[] }; broken?: "load" | "err" } {
  const { tab, kind, mods } = p;
  const scene: UpdatesScene = { tab, panel: {}, agents: {}, os: {} };
  const panel = scene.panel!;
  let panelKind: PanelKind = "new";
  const opts: Parameters<typeof updatesData>[0] = {};
  let edition: { edition: string; unavailable: string[] } | undefined;
  let broken: "load" | "err" | undefined;

  if (tab === "panel") {
    const run = /^run([0-3])$/.exec(kind);
    if (run || kind === "runx" || kind === "slow") {
      const ph: PanelPhase = { kind: "run", from: CUR, to: NEW, step: run ? Number(run[1]) : kind === "slow" ? 2 : undefined, slow: kind === "slow" };
      panel.phase = ph;
    } else if (kind === "out" || kind === "done") {
      panelKind = "done";
      panel.phase = kind === "out" ? { kind: "out", from: CUR, to: NEW, hold: true } : { kind: "done", from: CUR, to: NEW };
      panel.notes = panelNotes;
    } else if (kind === "cur") panelKind = "cur";
    else if (kind === "gh") panelKind = "gh";
    else if (kind === "sig") panelKind = "sig";
    else if (kind === "back") panelKind = "back";
    else if (kind === "load" || kind === "err") broken = kind;
    if (kind === "ghbusy" || mods.has("ghbusy")) panel.checking = true;
    if (mods.has("det")) panel.details = true;
    if (mods.has("more")) panel.more = true;
    const dlg = [...mods].find((m) => m.startsWith("dlg-"));
    if (dlg?.startsWith("dlg-upd")) panel.win = "update";
    if (dlg?.startsWith("dlg-ch")) {
      panel.win = "channel";
      if (dlg === "dlg-ch-test") panel.pick = "testing";
      if (dlg === "dlg-ch-dev") panel.pick = "dev";
    }
    if (kind === "chan") {
      opts.running = CUR;
    }
  }
  const data = updatesData({ ...opts, panel: panelKind });
  const d = data as unknown as { panel: { update: Record<string, unknown> }; nodes: unknown[]; rollout: unknown; bundle: unknown; updateChannel: string };
  if (kind === "chan") {
    d.updateChannel = "testing";
    d.panel.update = { ...d.panel.update, version: "v0.1.86-testing.1", installable: false, errorKey: "channel_not_allowed" };
  }
  if (kind === "unsup") d.panel.update = { ...d.panel.update, supported: false, installable: false };
  if (tab === "panel" && (kind.startsWith("run") || kind === "slow")) d.panel.update = { ...d.panel.update, installing: true };
  if (kind === "edge") {
    edition = { edition: "edge", unavailable: ["backups", "mcp", "health_checks", "agent_logs", "panel_update", "decoy_own", "tspu_probe"] };
    d.panel.update = { ...d.panel.update, supported: false, installable: false, errorKey: "unsupported", available: false };
  }

  if (tab === "agents") {
    const fi1 = d.nodes[1] as Record<string, unknown>;
    const fiStep = (state: StepState, over: Record<string, unknown> = {}) => step("fi1", "fi1", 0, state, { fromVersion: "v0.1.70", finishedUnix: 0, ...over });
    const others = d.nodes.filter((_, i) => i !== 1);
    const allCur = (extra: Record<string, unknown> = {}) => [others[0], upNode("fi1", "fi1", { onlineUsers: 0, lastUpdate: { outcome: "ok", fromVersion: "v0.1.70", fromBuilt: 0, toVersion: CUR, toBuilt: 0, reason: "", atUnix: NOW - 30, ...extra } }), others[1], others[2]];
    d.panel.update = { ...d.panel.update, available: false, version: CUR };
    const roll = /^roll([0-2])$/.exec(kind);
    if (roll) {
      const i = Number(roll[1]);
      d.nodes = [others[0], { ...fi1, state: NodeUpdateState.UPDATING, scheduledUnix: 0 }, others[1], others[2]];
      d.rollout = rollout(
        RolloutStatus.RUNNING,
        [i === 0 ? fiStep(StepState.SENT) : i === 1 ? fiStep(StepState.GATING) : fiStep(StepState.PASSED, { finishedUnix: NOW - 5 })],
        { createdUnix: NOW - 90 },
      );
      if (i === 2) d.nodes = allCur().map((n) => (n && (n as { name: string }).name === "fi1" ? { ...(n as object), state: NodeUpdateState.UPDATING } : n));
    } else if (kind === "fail") {
      d.nodes = [others[0], fi1Old({ state: NodeUpdateState.ROLLED_BACK, scheduledUnix: 0, onlineUsers: 0, lastUpdate: { outcome: "rolled_back", fromVersion: "v0.1.70", fromBuilt: 0, toVersion: CUR, toBuilt: 0, reason: "gate_probe_failed", atUnix: NOW - 6 * 60 } }), others[1], others[2]];
      // "named": the gate says which profiles did not pass (and which were not held against the update), as gate_profiles.go writes them
      const named = mods.has("named")
        ? { failed_profiles: JSON.stringify([{ name: "Main · 443", code: "timeout" }, { name: "Backup", code: "no_round" }]), failed_more: "2", skipped_profiles: JSON.stringify(["Old VLESS"]), skipped_more: "1" }
        : {};
      d.rollout = rollout(RolloutStatus.PAUSED, [fiStep(StepState.ROLLED_BACK, { errorKey: "updates.step.err.probe_failed", params: named, finishedUnix: NOW - 6 * 60 })], {
        pauseKey: "updates.pause.gate_failed",
        pauseParams: { node: "fi1", reason: "probe_failed", ...named },
        createdUnix: NOW - 12 * 60,
      });
    } else if (kind === "cur" || kind === "curjust") {
      d.nodes = allCur(kind === "cur" ? { atUnix: NOW - 3 * 3600 } : {}).map((n, i) => (kind === "cur" && i === 1 ? { ...(n as object), lastUpdate: { outcome: "ok", fromVersion: "v0.1.70", fromBuilt: 0, toVersion: CUR, toBuilt: 0, reason: "", atUnix: NOW - 3 * 3600 } } : n));
      d.rollout = rollout(RolloutStatus.DONE, [fiStep(StepState.PASSED, { finishedUnix: kind === "cur" ? NOW - 3 * 3600 : NOW - 20 })], { createdUnix: kind === "cur" ? NOW - 3 * 3600 - 400 : NOW - 400, finishedUnix: kind === "cur" ? NOW - 3 * 3600 : NOW - 20 });
    } else if (kind === "off") {
      d.nodes = [others[0], { ...fi1, state: NodeUpdateState.OFFLINE }, others[1], others[2]];
    } else if (kind === "none") {
      d.nodes = [];
      d.rollout = null;
    } else if (kind === "paused") {
      d.nodes = [others[0], { ...fi1, state: NodeUpdateState.OUTDATED }, others[1], others[2]];
      d.rollout = rollout(RolloutStatus.PAUSED, [step("nl1", "nl1", 0, StepState.PASSED), step("fi1", "fi1", 1, StepState.PENDING)], { pauseKey: "updates.pause.owner", createdUnix: NOW - 20 * 60 });
    } else if (kind === "nobundle") {
      d.bundle = bundle({ status: BundleStatus.MISSING, version: "", built: 0, files: [], errorKey: "updates.bundle.err.no_manifest" });
    } else if (kind === "untrusted") {
      d.bundle = bundle({ status: BundleStatus.UNTRUSTED, errorKey: "updates.bundle.err.file_mismatch", params: { file: "mistgate-node-linux-amd64" } });
    } else if (kind === "manual") {
      d.nodes = [others[0], fi1Old({ state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false, scheduledUnix: 0, lastUpdate: undefined }), others[1], others[2]];
    } else if (kind === "edge") {
      edition = { edition: "edge", unavailable: ["backups", "mcp", "health_checks", "agent_logs", "panel_update", "decoy_own", "tspu_probe"] };
      d.bundle = bundle({ downloadBase: "https://github.com/example/mistgate/releases/download/v0.1.71/" });
    } else if (kind === "notes") {
      (d as unknown as { agentNotes: unknown }).agentNotes = agentNotes;
    }
    // a bigger fleet: more nodes after the four of the board (up to date, or waiting, or one of each state)
    const total = Number([...mods].find((m) => /^n\d+$/.test(m))?.slice(1));
    if (total > d.nodes.length) {
      const how = mods.has("mix") ? "mix" : mods.has("wait") ? "wait" : "up";
      d.nodes = [...d.nodes, ...extraNodes(total - d.nodes.length, new Set(d.nodes.map((n) => (n as { name: string }).name)), how)];
      if (how === "mix") {
        const waiting = d.nodes.filter((n) => (n as { state: NodeUpdateState }).state === NodeUpdateState.OUTDATED && (n as { name: string }).name !== "fi1") as { name: string; nodeId: string }[];
        d.rollout = rollout(RolloutStatus.RUNNING, [fiStep(StepState.GATING), ...waiting.slice(0, 3).map((n) => step(n.nodeId.slice(4), n.name, 1, StepState.PENDING))], { createdUnix: NOW - 600 });
      }
    }
    if (mods.has("open")) scene.agents!.matrixOpen = true;
    if (mods.has("last")) scene.agents!.last = true;
    if (mods.has("pkbusy")) scene.agents!.reading = true;
    const fiNode = d.nodes.find((n) => (n as { name: string }).name === "fi1") as never;
    const dlg = [...mods].find((m) => m.startsWith("dlg-"));
    if (dlg === "dlg-roll") scene.dialog = { kind: "rollout" };
    else if (dlg?.startsWith("dlg-sch")) {
      const fresh = dlg.startsWith("dlg-sch-new");
      const node = fresh ? ({ ...(fiNode as object), scheduledUnix: 0 } as never) : fiNode;
      const mode = dlg === "dlg-sch-now" || dlg === "dlg-sch-new" ? "now" : "schedule";
      scene.dialog = { kind: "update", node, mode };
    } else if (dlg === "dlg-rollback") scene.dialog = { kind: "rollback", node: d.nodes[0] as never };
    else if (dlg === "dlg-cancel") scene.dialog = { kind: "cancel", id: "rol_1" };
    // fi1 with no schedule of its own for the "new" window: the list keeps the one of the board
    if (dlg === "dlg-sch-new" || dlg === "dlg-sch-new-at") d.nodes = d.nodes.map((n) => ((n as { name: string }).name === "fi1" ? { ...(n as object), scheduledUnix: 0 } : n));
    if (mods.has("howto")) d.nodes = d.nodes.map((n) => ((n as { name: string }).name === "fi1" ? { ...(n as object), state: NodeUpdateState.UNSUPPORTED, supportsUpdate: false } : n));
  }

  if (tab === "os") {
    const k = (["new", "calm", "fail", "off", "busy", "just"].includes(kind) ? kind : "new") as OsKind;
    scene.os = { open: mods.has("closed") ? [] : ["nod_de1"], picked: mods.has("tick") ? ["nod_de1", "nod_nl1"] : undefined };
    return { data, os: osData(k), scene, edition };
  }
  return { data, os: osData("new"), scene, edition, broken };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

/** The panel's calls of the scene: what a button asks for is answered after a breath and changes nothing on the page. */
export function fakeCalls(built: ReturnType<typeof build>, mods: Set<string>) {
  const api = updatesApi as unknown as Record<string, unknown>;
  api.getUpdates = () => (built.broken === "load" ? new Promise(() => {}) : built.broken === "err" ? Promise.reject(new Error("panel did not answer")) : Promise.resolve(built.data));
  api.getOsUpdates = () => Promise.resolve(built.os);
  for (const name of ["checkPanelUpdate", "installPanelUpdate", "startRollout", "pauseRollout", "resumeRollout", "cancelRollout", "rollbackNode", "rescanBundle", "scheduleNodeUpdate", "cancelNodeUpdateSchedule", "setUpdateChannel", "setUpdateTimezone", "startOsRollout", "cancelOsRollout", "pauseOsRollout", "resumeOsRollout"]) {
    api[name] = async () => {
      await sleep(900);
      return {};
    };
  }
  // "read the folder again" answers with what the download did: `rs-fresh|installed|downloading|failed` or `rs-wait-rollout|os_rollout|panel_update|other`
  const rs = [...mods].find((m) => m.startsWith("rs-"))?.slice(3);
  if (rs) {
    api.rescanBundle = async () => {
      await sleep(900);
      return { bundle: { params: rs.startsWith("wait-") ? { sync: "waiting", sync_wait: rs.slice(5) } : { sync: rs } } };
    };
  }
  api.refreshOsUpdates = async ({ nodeIds }: { nodeIds: string[] }) => {
    await sleep(600);
    return { results: nodeIds.map((nodeId) => ({ nodeId, outcome: "started", code: "" })) };
  };
  const r2 = {
    accountId: "0123456789abcdef", bucket: "mistgate-backups", accessKeyId: "not-a-real-key-id", hasSecret: true, ageRecipient: "age1notarealrecipientkeyexamplexxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    lastSuccessUnix: mods.has("dlg-upd-done") ? NOW - 20 : NOW - 11 * 3600, lastErrorCode: "", running: mods.has("dlg-upd-busy"),
  };
  (backups as unknown as Record<string, unknown>).getBackupSettings = async () => ({ settings: r2 });
  (backups as unknown as Record<string, unknown>).createBackup = async () => ({});
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role, fleet = 4, issues = 1) {
  const p = parse(state);
  const built = build(p);
  fakeCalls(built, p.mods);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...built.edition } as never);
  shellSeed(c, fleet, issues, ((built.data as { nodes?: { name: string }[] }).nodes ?? []).map((n) => n.name));
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  if (!built.broken) c.setQueryData(updatesQuery.queryKey, built.data as never);
  c.setQueryData(osUpdatesQuery.queryKey, built.os as never);
  return c;
}

const roleOf = (r: string | null) => (r === "helper" ? Role.HELPER : r === "readonly" ? Role.READONLY : Role.OWNER);
const num = (v: string | null, otherwise: number) => (v !== null && !Number.isNaN(Number(v)) ? Number(v) : otherwise);

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function UpdatesScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "pn.new";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    return seeded(state, roleOf(params.get("role")), num(params.get("fleet"), 4), num(params.get("issues"), 1));
  });
  const built = build(parse(state));
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <StepUpProvider>
          <AddNodeProvider>
            <ShellLayout current="updates" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <UpdatesView scene={built.scene} />
            </ShellLayout>
          </AddNodeProvider>
        </StepUpProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const f = (id: string, state: string, title: string, w = 1440, h = 1100, params = ""): Frame => ({ id: `updates-${id}`, state, title, w, h, params });
export const frames: Frame[] = [
  // this panel
  f("pn-new", "pn.new", "This panel: a release is out (the board's \"есть обновление\")", 1440, 1400),
  f("pn-new-light", "pn.new", "The same on the light theme", 1440, 1400, "&theme=light"),
  f("pn-new-1000", "pn.new", "A 1000 window", 1000, 1500),
  f("pn-new-phone", "pn.new", "A phone, 400 wide", 400, 2300),
  f("pn-new-fa", "pn.new", "Right to left (fa)", 1440, 1400, "&lang=fa"),
  f("pn-new-fx", "pn.new", "With the reveal animation on (the tab enters, the halo breathes)", 1440, 1400, "&fx=1"),
  f("pn-new-det", "pn.new.det", "The details open", 1440, 1500),
  f("pn-new-more", "pn.new.more", "Earlier versions open", 1440, 1700),
  f("pn-new-readonly", "pn.new", "An observer: no buttons, and the reason", 1440, 1400, "&role=readonly"),
  f("pn-new-helper", "pn.new", "A helper (not the owner): the same as an observer", 1440, 1400, "&role=helper"),
  f("pn-cur", "pn.cur", "Up to date", 1440, 700),
  f("pn-cur-phone", "pn.cur", "Up to date on a phone", 400, 1100),
  f("pn-ghbusy", "pn.ghbusy", "Asking GitHub: the spinner and the sweep over the card", 1440, 1400),
  f("pn-gh", "pn.gh", "GitHub did not answer", 1440, 1400),
  f("pn-sig", "pn.sig", "The signature did not match", 1440, 1100),
  f("pn-back", "pn.back", "The panel went back", 1440, 1400),
  f("pn-run0", "pn.run0", "Installing, step 1: the takeover screen", 1440, 900),
  f("pn-run1", "pn.run1", "Installing, step 2", 1440, 900),
  f("pn-run2", "pn.run2", "Installing, step 3: no link to the panel, and that is normal", 1440, 900),
  f("pn-run3", "pn.run3", "Installing, step 4: the check", 1440, 900),
  f("pn-run1-light", "pn.run1", "The takeover on the light theme", 1440, 900, "&theme=light"),
  f("pn-run1-phone", "pn.run1", "The takeover on a phone", 400, 800),
  f("pn-runx", "pn.runx", "Installing, no step known: what the page sees in the product", 1440, 900),
  f("pn-slow", "pn.slow", "No answer for two minutes: the matrix turns amber, Reload offered", 1440, 900),
  f("pn-out", "pn.out", "The new version answered: it holds still (live, it fades after a moment)", 1440, 900),
  f("pn-done", "pn.done", "Just updated: the card says so, the notes stay", 1440, 1500),
  f("pn-load", "pn.load", "Loading", 1440, 700),
  f("pn-err", "pn.err", "Did not load", 1440, 700),
  f("pn-chan", "pn.chan", "The channel the root policy does not allow", 1440, 1500),
  f("pn-unsup", "pn.unsup", "A panel that cannot update itself: the steps by hand", 1440, 1700),
  f("pn-edge", "pn.edge", "The Cloudflare edition: updated with a command", 1440, 800),
  f("pn-dlg-upd", "pn.new.dlg-upd", "The window before the update: a backup 11 h ago", 1440, 1000),
  f("pn-dlg-upd-phone", "pn.new.dlg-upd", "The same window on a phone (a sheet)", 400, 1000),
  f("pn-dlg-upd-busy", "pn.new.dlg-upd-busy", "A backup to R2 is running: Update waits", 1440, 1000),
  f("pn-dlg-upd-done", "pn.new.dlg-upd-done", "A backup was just made", 1440, 1000),
  f("pn-dlg-ch", "pn.new.dlg-ch", "The channel window: Stable is current, nothing picked", 1440, 1000),
  f("pn-dlg-ch-test", "pn.new.dlg-ch-test", "Testing picked: a warning, Switch is on", 1440, 1000),
  f("pn-dlg-ch-dev", "pn.new.dlg-ch-dev", "Development picked", 1440, 1000),
  // node agents
  f("ag-new", "ag.new", "Node agents: one node can be updated, the canary leads, fi1 has a schedule", 1440, 1500),
  f("ag-new-light", "ag.new", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("ag-new-1000", "ag.new", "A 1000 window", 1000, 1700),
  f("ag-new-phone", "ag.new", "A phone", 400, 2800),
  f("ag-new-fa", "ag.new", "Right to left (fa)", 1440, 1500, "&lang=fa"),
  f("ag-new-readonly", "ag.new", "An observer", 1440, 1500, "&role=readonly"),
  f("ag-roll0", "ag.roll0", "A rollout: the canary is being installed", 1440, 1500),
  f("ag-roll1", "ag.roll1", "A rollout: the canary is checked for 5 minutes", 1440, 1500),
  f("ag-roll2", "ag.roll2", "A rollout: the canary passed", 1440, 1500),
  f("ag-fail", "ag.fail", "The canary did not pass: the rollout is paused", 1440, 1500),
  f("ag-cur", "ag.cur", "All up to date", 1440, 1300),
  f("ag-curjust", "ag.curjust", "The canary has just finished: its card flashes", 1440, 1300),
  f("ag-off", "ag.off", "A node is offline", 1440, 1500),
  f("ag-none", "ag.none", "No nodes", 1440, 700),
  f("ag-n6", "ag.new.n6", "Six nodes: still a card each", 1440, 1500),
  f("ag-n7", "ag.new.n7", "Seven nodes: the lead's card and the matrix of the rest", 1440, 1500),
  f("ag-n12", "ag.new.n12", "Twelve nodes (the board's 12): fi1 waits, the others are cells", 1440, 1500),
  f("ag-n12-light", "ag.new.n12", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("ag-n12-1000", "ag.new.n12", "A 1000 window: the card above the matrix", 1000, 1700),
  f("ag-n12-phone", "ag.new.n12", "A phone: two columns of cells", 400, 2800),
  f("ag-n12-fa", "ag.new.n12", "Right to left (fa)", 1440, 1500, "&lang=fa"),
  f("ag-n12-roll0", "ag.roll0.n12", "Twelve nodes, the canary is being installed", 1440, 1500),
  f("ag-n12-roll1", "ag.roll1.n12", "Twelve nodes, the canary is checked for 5 minutes", 1440, 1500),
  f("ag-n12-fail", "ag.fail.n12", "Twelve nodes, the canary did not pass", 1440, 1500),
  f("ag-n12-off", "ag.off.n12", "Twelve nodes, the canary is offline", 1440, 1500),
  f("ag-n12-cur", "ag.cur.n12", "Twelve nodes, all up to date: the matrix across the card", 1440, 1300),
  f("ag-n12-curjust", "ag.curjust.n12", "Twelve nodes, the canary has just finished: its cell flashes", 1440, 1300),
  f("ag-n12-wait", "ag.new.n12.wait", "Twelve nodes, every third waits: the others wait in the matrix", 1440, 1500),
  f("ag-n12-mix", "ag.new.n12.mix", "Twelve nodes in every state: waiting, updating, failed, offline, by hand, in the queue", 1440, 1500),
  f("ag-n12-mix-light", "ag.new.n12.mix", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("ag-n40", "ag.new.n40.wait", "Forty nodes: the matrix folds to eight rows", 1440, 1900),
  f("ag-n40-phone", "ag.new.n40.wait", "Forty nodes on a phone", 400, 2800),
  f("ag-n120", "ag.new.n120.mix", "A hundred and twenty nodes: folded, Show all", 1440, 2200),
  f("ag-n120-open", "ag.new.n120.mix.open", "A hundred and twenty nodes: unfolded", 1440, 3200),
  f("ag-paused", "ag.paused", "The owner paused the rollout", 1440, 1500),
  f("ag-nobundle", "ag.nobundle", "No release bundle: the steps to make one", 1440, 1500),
  f("ag-untrusted", "ag.untrusted", "A bundle the panel does not trust", 1440, 1500),
  f("ag-manual", "ag.manual", "A node that is updated by hand", 1440, 1500),
  f("ag-howto", "ag.manual.howto", "The two commands of a manual update", 1440, 1500),
  f("ag-edge", "ag.edge", "The Cloudflare edition: no bundle card", 1440, 1300),
  f("ag-notes", "ag.notes", "What is new in the agent (a card of releases)", 1440, 1700),
  f("ag-last", "ag.new.last", "The stages of the last rollout open", 1440, 1700),
  f("ag-pkbusy", "ag.new.pkbusy", "The release folder is being read", 1440, 1500),
  f("ag-dlg-roll", "ag.new.dlg-roll", "The window: update to v0.1.71 (the canary named)", 1440, 1000),
  f("ag-dlg-sch", "ag.new.dlg-sch", "The window of fi1: a schedule is set", 1440, 1000),
  f("ag-dlg-sch-now", "ag.new.dlg-sch-now", "The window of fi1: update now", 1440, 1000),
  f("ag-dlg-sch-new", "ag.new.dlg-sch-new", "No schedule yet: now", 1440, 1000),
  f("ag-dlg-sch-new-at", "ag.new.dlg-sch-new-at", "No schedule yet: date and time", 1440, 1000),
  f("ag-dlg-sch-phone", "ag.new.dlg-sch", "The window of fi1 on a phone", 400, 1000),
  f("ag-dlg-rollback", "ag.new.dlg-rollback", "Roll a node back", 1440, 900),
  f("ag-dlg-cancel", "ag.roll1.dlg-cancel", "Cancel the rollout", 1440, 900),
  // the servers' systems
  f("os-new", "os.new", "Servers: de1 needs a reboot, nl1 has security updates; de1 open", 1440, 1500),
  f("os-new-light", "os.new", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("os-new-1000", "os.new", "A 1000 window", 1000, 1700),
  f("os-new-phone", "os.new", "A phone", 400, 2600),
  f("os-new-fa", "os.new", "Right to left (fa)", 1440, 1500, "&lang=fa"),
  f("os-closed", "os.new.closed", "Every row closed", 1440, 1100),
  f("os-readonly", "os.new", "An observer: the row opens to the names and the node's page", 1440, 1500, "&role=readonly"),
  f("os-calm", "os.calm", "Everything up to date", 1440, 900),
  f("os-fail", "os.fail", "The install stopped: the reason, Close", 1440, 1500),
  f("os-off", "os.off", "A node is offline", 1440, 1300),
  f("os-busy", "os.busy", "Installing: the sweep over the row", 1440, 1500),
  f("os-just", "os.just", "Just done: the row flashes", 1440, 1500),
  f("os-tick", "os.new.closed.tick", "Two nodes ticked: the bar above the table", 1440, 1300),
];

export function PnUpdatesKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Updates</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((fr) => (
        <Shot key={fr.id} id={fr.id} title={fr.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={fr.title} src={`${path}?only=pn-updates-page&state=${fr.state}${fr.params ?? ""}`} width={fr.w} height={fr.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}

