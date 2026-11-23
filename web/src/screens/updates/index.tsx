import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { useEdition } from "@/lib/edition";
import { osRows, osSummary, useOsUpdates } from "@/lib/os-updates";
import { canUpdateNode, fleetCounts, heroOf, isActive, outdatedNodes, useIsOwner, useUpdateActions, useUpdates, type NodeUpdate, type Updates } from "@/lib/updates";
import { AgentsTab, type AgentsScene } from "./agents";
import { ErrorCard, Skeleton } from "./bits";
import { CancelDialog, RollbackDialog, RolloutDialog, UpdateNodeDialog } from "./dialogs";
import { OsSection } from "./os";
import { PanelTab, type PanelScene } from "./panel";
import type { UpdateTab } from "./tabs";
import "./updates.css";

type Dialog =
  | { kind: "update"; node: NodeUpdate; mode?: "now" | "schedule" }
  | { kind: "rollback"; node: NodeUpdate }
  | { kind: "cancel"; id: string }
  | { kind: "rollout" }
  | null;

/** What a scene of the dev kit puts in place of what the page would find out by itself (the open tab, the panel's update, a window). */
export type UpdatesScene = { tab?: UpdateTab; panel?: PanelScene; agents?: AgentsScene; os?: { open?: string[]; picked?: string[] }; dialog?: NonNullable<Dialog> };

/** The line under the title: what can be updated now, what is going on, and the servers that wait for a reboot. */
function summary(t: T, d: Updates | undefined, rebooting: number, loading: boolean): string {
  if (!d) return loading ? t("uq.sum.load") : t("uq.sum.err");
  const panel = !!d.panel?.update?.available;
  const nodes = outdatedNodes(d.nodes).length;
  const hero = heroOf(d);
  const what =
    panel && nodes > 0
      ? t("uq.sum.panelNodes", { nodes: t.n("uq.sum.nodes", nodes) })
      : panel
        ? t("uq.sum.panelOnly")
        : hero.id === "running"
          ? t("up.sub.running")
          : hero.id === "paused"
            ? t("up.sub.paused")
            : nodes > 0
              ? t("uq.sum.nodesOnly", { nodes: t.n("uq.sum.nodes", nodes) })
              : hero.id === "manual"
                ? t("up.sub.manual", { names: hero.nodes.map((n) => n.name).join(", ") })
                : hero.id === "attention"
                  ? t("up.hero.attention.title")
                  : hero.id === "none"
                    ? t("up.sub.none")
                    : t("uq.sum.ok");
  return rebooting > 0 ? `${what} · ${t("uq.sum.reboot", { n: rebooting })}` : what;
}

const hashTabs: Record<string, UpdateTab> = { "#panel": "panel", "#pn": "panel", "#agents": "agents", "#ag": "agents", "#os": "os" };

/** The tabs under the title: the panel, the node agents, the servers' systems; a number on a tab says how much waits there. */
function TabStrip({ tabs, value, label, onPick }: { tabs: { value: UpdateTab; label: string; n: number }[]; value: UpdateTab; label: string; onPick: (v: UpdateTab) => void }) {
  const pick = (i: number) => {
    const next = tabs[(i + tabs.length) % tabs.length];
    if (!next) return;
    onPick(next.value);
    document.getElementById(`uq-tab-${next.value}`)?.focus();
  };
  return (
    <div role="tablist" aria-label={label} className="uq-tabs">
      {tabs.map((x, i) => (
        <button
          key={x.value}
          id={`uq-tab-${x.value}`}
          type="button"
          role="tab"
          aria-selected={x.value === value}
          aria-controls="uq-tabpanel"
          tabIndex={x.value === value ? 0 : -1}
          onClick={() => onPick(x.value)}
          onKeyDown={(e) => {
            const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
            if (step === 0) return;
            e.preventDefault();
            pick(i + step);
          }}
        >
          {x.label}
          {x.n > 0 && <span className="bdg up-n">{x.n}</span>}
        </button>
      ))}
    </div>
  );
}

/**
 * Updates, in the order they are done: this panel's own update first (the panel is updated before the nodes), then where
 * the fleet stands and the one button that moves it (with the signed release bundle at its foot), the rollout in progress
 * or the last one, then every node; the servers' operating system is a tab of its own.
 */
export function UpdatesScreen() {
  return <UpdatesView />;
}

export function UpdatesView({ scene }: { scene?: UpdatesScene }) {
  const t = useT();
  const q = useUpdates();
  const isOwner = useIsOwner();
  const edition = useEdition();
  const fx = useEffectsEnabled();
  // the servers' operating system is a tab of its own; the edition without it keeps the page as two
  const hasOs = !edition.off("os_updates");
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: UpdateTab };
  const [sceneTab, setSceneTab] = useState<UpdateTab | undefined>(scene?.tab);
  const os = useOsUpdates(hasOs);
  const actions = useUpdateActions();
  const [dialog, setDialog] = useState<Dialog>(scene?.dialog ?? null);
  const d = q.data;

  // the tabs are on the page while it loads and when it did not: what they hold is not known yet, so they carry no number
  const hasTabs = (n: (v: UpdateTab) => number): { value: UpdateTab; label: string; n: number }[] => [
    { value: "panel", label: t("up.panel.title"), n: n("panel") },
    { value: "agents", label: t("up.tab.agents"), n: n("agents") },
    ...(hasOs ? [{ value: "os" as const, label: t("up.tab.os"), n: n("os") }] : []),
  ];
  if (!d) {
    const quiet = hasTabs(() => 0);
    const open = [sceneTab ?? search.tab, hashTabs[location.hash]].find((v) => v && quiet.some((x) => x.value === v)) ?? "panel";
    return (
      <div className={cx("uq", fx && "uq-fx")}>
        <header className="uq-h">
          <h1>{t("up.title")}</h1>
          <p>{summary(t, d, 0, !q.isError)}</p>
        </header>
        <TabStrip tabs={quiet} value={open} label={t("up.tabs")} onPick={(v) => (scene ? setSceneTab(v) : void navigate({ to: "/updates", search: { tab: v }, replace: true }))} />
        <div id="uq-tabpanel" role="tabpanel" aria-labelledby={`uq-tab-${open}`}>
          {q.isError ? <ErrorCard onRetry={() => void q.refetch()} /> : <Skeleton />}
        </div>
      </div>
    );
  }

  const close = () => setDialog(null);
  // the dialog closes once the call went through; a refusal leaves it open under the toast
  const go = (run: Promise<boolean>) => void run.then((ok) => ok && close());
  const selectedUpdate = dialog?.kind === "update" ? dialog.node : undefined;
  const activeRollout = d.rollout && isActive(d.rollout) ? d.rollout : null;
  const currentBundle = d.bundle?.status === BundleStatus.TRUSTED ? d.bundle : null;
  const updateStep = activeRollout?.steps.find((step) => step.nodeId === selectedUpdate?.nodeId);
  const activeBundleMatches = !!activeRollout && !!currentBundle && activeRollout.toVersion === currentBundle.version && activeRollout.toBuilt === currentBundle.built;
  const queueAfterRollout = !!selectedUpdate && activeRollout?.status === RolloutStatus.RUNNING && activeBundleMatches && !updateStep;
  const canUpdateNow = !!selectedUpdate && !!currentBundle && selectedUpdate.state !== NodeUpdateState.OFFLINE && canUpdateNode(selectedUpdate, currentBundle.built) && (!activeRollout || queueAfterRollout);
  const updateBlockReason = !selectedUpdate
    ? undefined
    : selectedUpdate.state === NodeUpdateState.OFFLINE
      ? "offline"
      : updateStep
        ? "queued"
        : activeRollout?.status === RolloutStatus.PAUSED
          ? "paused"
          : activeRollout && !activeBundleMatches
            ? "differentRelease"
            : !canUpdateNow
              ? "unavailable"
              : undefined;

  // ---- the three tabs: each says what it holds in a number when something wants a hand
  const panelNew = !!d.panel?.update?.available;
  // a release that is out but not signed for this panel, or an update that went back, wants a look at the panel's tab too
  const panelKey = d.panel?.update?.errorKey ?? "";
  const panelLook = panelNew || panelKey === "unsigned" || panelKey === "expired" || panelKey === "install_failed";
  const c = fleetCounts(d.nodes);
  const nodesNeed = c.outdated + c.attention + c.manual + c.updating;
  const osNeeds = os.data ? osSummary(osRows(os.data)) : undefined;
  const osCount = osNeeds ? osNeeds.reboot + osNeeds.security : 0;
  const tabs = hasTabs((v) => (v === "panel" ? (panelNew ? 1 : 0) : v === "agents" ? nodesNeed : osCount));
  // without a choice in the address the page opens on the tab that needs a hand: the panel is updated before its nodes
  const hashTab = hashTabs[location.hash];
  const wanted = [sceneTab ?? search.tab, hashTab].find((v) => v && tabs.some((x) => x.value === v));
  const tab: UpdateTab = wanted ?? (panelLook ? "panel" : "agents");
  const setTab = (next: UpdateTab) => (scene ? setSceneTab(next) : void navigate({ to: "/updates", search: { tab: next }, replace: true }));
  return (
    <div className={cx("uq", fx && "uq-fx")}>
      <header className="uq-h">
        <h1>{t("up.title")}</h1>
        <p>{summary(t, d, osNeeds?.reboot ?? 0, false)}</p>
      </header>
      <TabStrip tabs={tabs} value={tab} label={t("up.tabs")} onPick={setTab} />
      <div id="uq-tabpanel" role="tabpanel" aria-labelledby={`uq-tab-${tab}`} key={tab} className="uq-tab">
        {tab === "panel" && <PanelTab data={d} owner={isOwner} actions={actions} refetch={() => q.refetch()} answeredAt={q.dataUpdatedAt} scene={scene?.panel} />}
        {tab === "agents" && (
          <AgentsTab
            data={d}
            owner={isOwner}
            actions={actions}
            onRollout={() => setDialog({ kind: "rollout" })}
            onCancel={() => d.rollout && setDialog({ kind: "cancel", id: d.rollout.id })}
            onSchedule={(n) => setDialog({ kind: "update", node: n, mode: n.scheduledUnix > 0 ? "schedule" : undefined })}
            onRollback={(n) => setDialog({ kind: "rollback", node: n })}
            scene={scene?.agents}
          />
        )}
        {tab === "os" && hasOs && <OsSection defaultOpenNames={scene?.os?.open} defaultPicked={scene?.os?.picked} />}
      </div>
      {dialog?.kind === "rollout" && currentBundle && (
        <RolloutDialog
          version={currentBundle.version}
          nodes={outdatedNodes(d.nodes)}
          busy={actions.busy}
          onClose={close}
          onStart={(ids) => go(actions.start(ids, { version: currentBundle.version, built: currentBundle.built }))}
        />
      )}
      {dialog?.kind === "update" && (
        <UpdateNodeDialog
          data={d}
          node={dialog.node}
          initialMode={dialog.mode}
          canUpdateNow={canUpdateNow}
          queueAfterRollout={queueAfterRollout}
          nodeInActiveRollout={!!updateStep}
          blockedReason={updateBlockReason}
          busy={actions.busy}
          onClose={close}
          onUpdateNow={() => go(actions.start([dialog.node.nodeId], { version: d.bundle?.version ?? "", built: d.bundle?.built ?? 0 }))}
          onSchedule={(localDatetime) =>
            go(
              actions.scheduleNode({
                nodeId: dialog.node.nodeId,
                localDatetime,
                timezoneOffsetMinutes: d.scheduleTimezoneOffsetMinutes,
                expectedVersion: d.bundle?.version ?? "",
                expectedBuilt: d.bundle?.built ?? 0,
              }),
            )
          }
          onCancelSchedule={() => go(actions.cancelNodeSchedule(dialog.node.nodeId))}
        />
      )}
      {dialog?.kind === "rollback" && (
        <RollbackDialog node={dialog.node} busy={actions.busy} onClose={close} onConfirm={() => go(actions.rollback({ id: dialog.node.nodeId, name: dialog.node.name }))} />
      )}
      {dialog?.kind === "cancel" && <CancelDialog busy={actions.busy} onClose={close} onConfirm={() => go(actions.cancel(dialog.id))} />}
    </div>
  );
}
