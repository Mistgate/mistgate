import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider, useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { assistant as assistantApi, fleet as fleetApi, health as healthApi, nodes as nodesApi } from "@/lib/api";
import { accessQuery } from "@/lib/assistant";
import { useLang } from "@/i18n";
import { alertsQuery, checksQuery, doctorQuery, fixErrorText } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useTheme } from "@/lib/theme";
import { updatesQuery } from "@/lib/updates";
import { editionFeatures } from "@/i18n/edition";
import { alertsOf, checksOf, doctorOf, flagsOf, FLEET, NOW, nid, overviewOf, type Flags } from "@/screens/dev-kit-health-data";
import { applyScene, SceneGuard } from "@/screens/dev-kit-scene";
import { nodeRows } from "@/screens/dev-kit-profiles-data";
import { Shot } from "@/screens/dev-kit-redesign";
import { HealthPage, type HealthScene } from "@/screens/health";
import type { FixSeed, FixTarget } from "@/screens/health/fix";
import { healthTabs, type HealthTab } from "@/screens/health/tabs";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-health: the Health page of the redesign (PnHealth) in every state of the board and the product's own, each in a frame of its
// own width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the side-by-side with the board:
//   /dev-kit?only=health-page&state=<tab>[.<variant>…][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fx=1]
// <tab>: alerts | people | checks | doctor. <variant> is what a scene adds, as the board's switches are (Data: calm · nonodes · first · load · err; Case: crit ·
// fixfail · nock · off; Role: ro, the same as role=readonly) and what its script does by a click: open.<id> (the row that is open: j r x u m c, p1 p2 p3; on Checks the node,
// open.fi1) · dlg (the window of the journal fix) · dlgx (of the restart) · busy (the fix runs) · closeall (the window "Close all shown") · toast (the toast with Undo) ·
// node.<name> (the filter "By node") · ckrun (the walk of "Check now" at its first step) · ckdone (its flash) · legend (the fold opened) · docbusy ("Run again" going);
// and the product's states that the board does not draw: muted · grouped · loss · history · nodeproblems · edge (· mskclean: msk1 has no alert).
// Made-up data of the board: de1 fi1 nl1 msk1, alice, bob, documentation addresses; no call leaves the page (the panel's answers are the scene's own).

type Parts = { tab: HealthTab; variant: string[]; flags: Flags; at: (key: string) => string };
export const partsOf = (state: string): Parts => {
  const [tab = "alerts", ...variant] = state.split(".");
  return {
    tab: (healthTabs as readonly string[]).includes(tab) ? (tab as HealthTab) : "alerts",
    variant,
    flags: flagsOf(variant),
    // the word after a key: open.j, node.fi1
    at: (key) => (variant.includes(key) ? (variant[variant.indexOf(key) + 1] ?? "") : ""),
  };
};

const journal: FixTarget = { fixId: "journald_vacuum", params: {}, nodeId: nid("de1"), nodeName: "de1", key: `${nid("de1")}/journald_size/` };
const restart: FixTarget = { fixId: "restart_inbound", params: { inbound_id: "inb_xhttp" }, nodeId: nid("fi1"), nodeName: "fi1", profile: "VLESS · XHTTP", key: `${nid("fi1")}/inb_xhttp/` };

/** The scene's own part of the page: what is open, which window, which walk is where. */
function sceneOf(parts: Parts, failText: string): HealthScene {
  const { variant: v, flags } = parts;
  const has = (k: string) => v.includes(k);
  const open = parts.at("open");
  const rowOfAlert = open === "u" ? "u1" : open;
  const flow: FixSeed = {
    ...(has("dlg") && { dlg: { target: journal, planId: "pln_scene", plan: { fixId: "journald_vacuum", titleKey: "health.fix.journald_vacuum.plan", params: { before_mb: "3870", target_mb: "500" }, detail: "journal 3870 MB, cap 500 MB", disruptive: false } as never } }),
    ...(has("dlgx") && { dlg: { target: restart, planId: "pln_scene", plan: { fixId: "restart_inbound", titleKey: "health.fix.restart_inbound.plan", params: { inbounds: "inb_xhttp", profiles: "VLESS · XHTTP", online: "2" }, detail: "would restart inb_xhttp", disruptive: true } as never } }),
    ...(has("busy") && { busy: [journal] }),
    ...(flags.fixfail && { errors: { [journal.key]: failText } }),
  };
  return {
    flow,
    alerts: { node: has("node") ? nid(parts.at("node")) : undefined, open: rowOfAlert || (flags.fixfail || has("dlg") || has("busy") ? "j" : has("dlgx") ? "x" : undefined), closeAll: has("closeall") },
    people: { open: open || undefined, closeAll: has("closeall") },
    checks: { open: parts.tab === "checks" ? open || undefined : undefined, walk: has("ckrun") ? "run" : has("ckdone") ? "flash" : undefined, legend: has("legend") },
    doctor: { busy: has("docbusy") },
  };
}

/** The panel's answers to what the page asks, in the state a scene asks for (the scene is its own page, so patching the shared client is safe). */
function fakeCalls(f: Flags) {
  const never = () => new Promise<never>(() => {});
  const fail = async () => {
    throw new Error("The panel did not answer");
  };
  const h = healthApi as unknown as Record<string, unknown>;
  const answer = <T,>(data: T) => (f.load ? never : f.err ? fail : async () => data);
  h.listAlerts = answer(alertsOf(f));
  h.getChecks = answer(checksOf(f));
  h.getDoctor = answer(doctorOf(f));
  h.runDoctor = async () => doctorOf(f);
  h.runChecksNow = async () => ({ scheduled: f.nock ? 0 : 3, skipped: 0 });
  h.muteAlert = async () => ({ alert: { mutedUntilUnix: NOW + 3600 } });
  h.dismissAlert = async () => ({ alert: {} });
  h.acceptDoctorItem = async () => ({ doctor: {} });
  h.unacceptDoctorItem = async () => ({ doctor: {} });
  h.applyFix = async (req: { dryRun: boolean; fixId: string }) =>
    req.dryRun
      ? { planId: "pln_scene", plan: { fixId: req.fixId, titleKey: `health.fix.${req.fixId}.plan`, params: { before_mb: "3870", target_mb: "500", profiles: "VLESS · XHTTP", online: "2", inbounds: "inb_xhttp" }, detail: "", disruptive: req.fixId === "restart_inbound" } }
      : { applied: true, affected: 1, resultParams: {} };
  (nodesApi as unknown as Record<string, unknown>).restartInbounds = async () => ({ restarted: 1 });
  (fleetApi as unknown as Record<string, unknown>).overview = f.load ? never : async () => overviewOf(f);
  (assistantApi as unknown as Record<string, unknown>).getAssistantAccess = async () => ({ available: true });
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role) {
  const { flags } = partsOf(state);
  fakeCalls(flags);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(flags.edge ? { edition: "edge", unavailable: [...editionFeatures] } : {}) } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: nodeRows().filter((n) => FLEET.some((x) => x.n === n.name)), expectedAgentVersion: "0.1.71" } as never);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  c.setQueryData(updatesQuery.queryKey, { nowUnix: NOW, panel: { version: "v0.1.71", built: NOW, update: { version: "v0.1.71", url: "", checkedUnix: NOW, available: false, supported: true, installable: true, installing: false, errorKey: "" } }, bundle: { status: BundleStatus.TRUSTED, version: "v0.1.71", built: NOW }, nodes: [] } as never);
  c.setQueryData(accessQuery.queryKey, { available: true } as never);
  if (!flags.load) {
    c.setQueryData(overviewQuery().queryKey, overviewOf(flags) as never);
    if (!flags.err) {
      c.setQueryData(alertsQuery.queryKey, alertsOf(flags) as never);
      c.setQueryData(checksQuery("").queryKey, checksOf(flags) as never);
      c.setQueryData(doctorQuery("").queryKey, doctorOf(flags) as never);
    }
  }
  return c;
}

/** The page of a scene, in the providers it needs, without the shell (the scene page puts the shell around it; the smoke test does not). */
export function SceneBody({ state }: { state: string }) {
  const parts = partsOf(state);
  const t = useT();
  const toast = useToast();
  const [tab, setTab] = useState<HealthTab>(parts.tab);
  const failText = t("hl.fix.failed", { error: fixErrorText(t, "busy") });
  useEffect(() => {
    // the toasts of the board's script, held long enough to be looked at
    if (parts.variant.includes("toast")) toast(t("hl.alerts.closedToast"), { undo: () => {}, ms: 600_000 });
    if (parts.flags.nock && parts.tab === "checks") toast(t("hl.checks.nowNone"), { ms: 600_000 });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once, as the scene opens
  }, []);
  return <HealthPage tab={tab} onTab={(next) => setTab(next as HealthTab)} scene={sceneOf(parts, failText)} />;
}

/** One whole page of the scene: the page in the real shell, at the window's own width. */
export function HealthScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "alerts";
  const [qc] = useState(() => {
    applyScene(params);
    const role = params.get("role");
    const ro = partsOf(state).variant.includes("ro");
    return seeded(state, role === "helper" ? Role.HELPER : role === "readonly" || ro ? Role.READONLY : Role.OWNER);
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <AddNodeProvider>
          <SceneGuard>
            <ShellLayout current="health" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <SceneBody state={state} />
            </ShellLayout>
          </SceneGuard>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const light = "&theme=light";
export const frames: Frame[] = [
  // the page in its four widths, its two themes and right to left
  { id: "health-alerts", state: "alerts", title: "Alerts: five rows, the first open, By node and Checks at the side (1440)", w: 1440, h: 1100 },
  { id: "health-alerts-light", state: "alerts", title: "The same on the light theme", w: 1440, h: 1100, params: light },
  { id: "health-alerts-1000", state: "alerts", title: "A 1000 window: the rail", w: 1000, h: 1100 },
  { id: "health-alerts-phone", state: "alerts", title: "A phone, 400 wide", w: 400, h: 2400 },
  { id: "health-alerts-fa", state: "alerts", title: "Right to left (fa)", w: 1440, h: 1100, params: "&lang=fa" },
  { id: "health-alerts-ru-light", state: "alerts", title: "Russian, light", w: 1440, h: 1100, params: `&lang=ru${light}` },
  // the board's switches: Data
  { id: "health-calm", state: "alerts.calm", title: "Data: all quiet", w: 1440, h: 900 },
  { id: "health-nonodes", state: "alerts.nonodes", title: "Data: no nodes", w: 1440, h: 700 },
  { id: "health-first", state: "alerts.first", title: "Data: the first round", w: 1440, h: 900 },
  { id: "health-load", state: "alerts.load", title: "Data: loading", w: 1440, h: 700 },
  { id: "health-err", state: "alerts.err", title: "Data: it did not load", w: 1440, h: 700 },
  // Case
  { id: "health-crit", state: "alerts.crit", title: "Case: a node fell (its row is open, red)", w: 1440, h: 1100 },
  { id: "health-fixfail", state: "alerts.fixfail", title: "Case: the fix did not work (the red box, Try again)", w: 1440, h: 1100 },
  { id: "health-nock", state: "checks.nock", title: "Case: nothing to check (the toast)", w: 1440, h: 900 },
  { id: "health-off", state: "checks.off", title: "Case: a node out of touch", w: 1440, h: 900 },
  // Role
  { id: "health-ro", state: "alerts.ro", title: "Role: an observer (the row says who may fix it)", w: 1440, h: 1100 },
  { id: "health-helper", state: "alerts", title: "Role: a helper (no fix, the rest is there)", w: 1440, h: 1100, params: "&role=helper" },
  // what the script does by a click
  { id: "health-open-r", state: "alerts.open.r", title: "A row opened: some networks of Russia", w: 1440, h: 1100 },
  { id: "health-open-x", state: "alerts.open.x", title: "A row opened: a profile that fails the check (the restart)", w: 1440, h: 1100 },
  { id: "health-open-u", state: "alerts.open.u", title: "A card of three ports of one node", w: 1440, h: 1200 },
  { id: "health-dlg", state: "alerts.dlg", title: "The window of the journal fix", w: 1440, h: 900 },
  { id: "health-dlg-phone", state: "alerts.dlg", title: "The same window on a phone (a sheet)", w: 400, h: 900 },
  { id: "health-dlgx", state: "alerts.dlgx", title: "The window of the restart", w: 1440, h: 900 },
  { id: "health-busy", state: "alerts.busy", title: "The fix runs (the line in place of the buttons)", w: 1440, h: 1100 },
  { id: "health-closeall", state: "alerts.closeall", title: "The window \"Close all shown\"", w: 1440, h: 900 },
  { id: "health-toast", state: "alerts.toast", title: "The toast with Undo", w: 1440, h: 900 },
  { id: "health-node", state: "alerts.node.fi1", title: "By node: fi1 picked", w: 1440, h: 1100 },
  { id: "health-node-none", state: "alerts.node.msk1.mskclean", title: "By node: a node with no alert, \"Show all\"", w: 1440, h: 900 },
  // People
  { id: "health-people", state: "people", title: "People: three rows, the card for the assistant", w: 1440, h: 1000 },
  { id: "health-people-light", state: "people", title: "People, light", w: 1440, h: 1000, params: light },
  { id: "health-people-phone", state: "people", title: "People on a phone", w: 400, h: 1700 },
  { id: "health-people-calm", state: "people.calm", title: "People: all quiet", w: 1440, h: 700 },
  { id: "health-people-closeall", state: "people.closeall", title: "People: \"Close all shown\"", w: 1440, h: 900 },
  // Checks
  { id: "health-checks", state: "checks", title: "Checks: the table, the failing row open (1440)", w: 1440, h: 1100 },
  { id: "health-checks-light", state: "checks", title: "Checks, light", w: 1440, h: 1100, params: light },
  { id: "health-checks-1000", state: "checks", title: "Checks, 1000", w: 1000, h: 1100 },
  { id: "health-checks-phone", state: "checks", title: "Checks on a phone: the cells under the node", w: 400, h: 2000 },
  { id: "health-checks-fa", state: "checks", title: "Checks, right to left", w: 1440, h: 1100, params: "&lang=fa" },
  { id: "health-ckrun", state: "checks.ckrun", title: "\"Check now\": the walk at its first row", w: 1440, h: 1100 },
  { id: "health-ckdone", state: "checks.ckdone", title: "\"Check now\": the flash after the last row", w: 1440, h: 1100 },
  { id: "health-legend", state: "checks.legend", title: "The legend opened", w: 1440, h: 1100 },
  { id: "health-checks-de1", state: "checks.open.de1", title: "A row opened: the boxes of de1", w: 1440, h: 1100 },
  { id: "health-checks-first", state: "checks.first", title: "Checks: the first round", w: 1440, h: 900 },
  { id: "health-checks-loss", state: "checks.loss", title: "Checks: the panel's server loses UDP (the note)", w: 1440, h: 1100 },
  // Doctor
  { id: "health-doctor", state: "doctor", title: "Doctor: the sum, one finding, the nodes (1440)", w: 1440, h: 900 },
  { id: "health-doctor-light", state: "doctor", title: "Doctor, light", w: 1440, h: 900, params: light },
  { id: "health-doctor-phone", state: "doctor", title: "Doctor on a phone", w: 400, h: 1700 },
  { id: "health-doctor-fa", state: "doctor", title: "Doctor, right to left", w: 1440, h: 900, params: "&lang=fa" },
  { id: "health-docbusy", state: "doctor.docbusy", title: "\"Run again\": the line over the sum, the node being asked", w: 1440, h: 900 },
  { id: "health-doctor-dlg", state: "doctor.dlg", title: "The window of the fix, from the finding", w: 1440, h: 900 },
  { id: "health-doctor-busy", state: "doctor.busy", title: "The fix of the finding runs", w: 1440, h: 900 },
  { id: "health-doctor-fail", state: "doctor.fixfail", title: "The fix of the finding did not work (the card shook)", w: 1440, h: 900 },
  { id: "health-doctor-calm", state: "doctor.calm", title: "Doctor: found nothing", w: 1440, h: 700 },
  { id: "health-doctor-first", state: "doctor.first", title: "Doctor: not run yet", w: 1440, h: 700 },
  // the product's own, that the board does not draw
  { id: "health-muted", state: "alerts.muted", title: "Product: a muted alert in its fold", w: 1440, h: 1100 },
  { id: "health-grouped", state: "alerts.grouped.open.g1", title: "Product: profiles that fail on one node, one row", w: 1440, h: 1100 },
  { id: "health-loss", state: "alerts.loss.open.pl", title: "Product: a lossy path, the table of two senders", w: 1440, h: 1100 },
  { id: "health-history", state: "alerts.history", title: "Product: a long history, its events and episodes", w: 1440, h: 1300 },
  { id: "health-nodeproblems", state: "alerts.nodeproblems", title: "Product: a node that is a problem with no alert", w: 1440, h: 1100 },
  { id: "health-edge", state: "alerts.edge", title: "Product: the Cloudflare edition (no checks)", w: 1440, h: 1000 },
];

export function PnHealthKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Health</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=health-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}

export type { Flags };
