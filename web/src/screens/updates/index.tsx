import { useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { PageTitle } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import type { StatusKind } from "@/components/ui/status";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { useEdition } from "@/lib/edition";
import { osRows, osSummary, useOsUpdates } from "@/lib/os-updates";
import {
  heroOf,
  isActive,
  outdatedNodes,
  canUpdateNode,
  useIsOwner,
  useUpdateActions,
  useUpdates,
  type NodeUpdate,
  type Updates,
} from "@/lib/updates";
import { PanelBar, panelUpdate } from "./cards";
import { CancelDialog, RollbackDialog, RolloutDialog, UpdateNodeDialog } from "./dialogs";
import { HeroCard } from "./hero";
import { Layers, type Layer } from "./layers";
import { NodeTable } from "./nodes";
import { OsSection } from "./os";
import { RolloutCard } from "./rollout";
import type { UpdateTab } from "./tabs";

type Dialog =
  | { kind: "update"; node: NodeUpdate; mode?: "now" | "schedule" }
  | { kind: "rollback"; node: NodeUpdate }
  | { kind: "cancel"; id: string }
  | { kind: "rollout" }
  | null;

function subtitle(t: T, d?: Updates): string {
  if (!d) return t("up.sub.loading");
  if (d.nodes.length === 0) return t("up.sub.none");
  if (d.rollout && isActive(d.rollout)) return d.rollout.status === RolloutStatus.RUNNING ? t("up.sub.running") : t("up.sub.paused");
  const n = outdatedNodes(d.nodes).length;
  if (n > 0) return t.n("up.sub.outdated", n);
  const hero = heroOf(d);
  if (hero.id === "manual") return t("up.sub.manual", { names: hero.nodes.map((x) => x.name).join(", ") });
  return hero.id === "attention" ? t("up.hero.attention.title") : t("up.sub.current");
}

/**
 * Updates, in the order they are done: this panel's own update first (the panel is updated before the nodes), then where
 * the fleet stands and the one button that moves it (with the signed release bundle at its foot), the rollout in progress
 * or the last one, then every node with one main action per row.
 */
export function UpdatesScreen() {
  const t = useT();
  const q = useUpdates();
  // no update, schedule, roll back or bundle rescan there: the buttons go and the nodes stay listed
  const isOwner = useIsOwner();
  const edition = useEdition();
  const owner = !edition.off("agent_updates") && isOwner;
  // the servers' operating system is a tab of its own; the edition without it keeps the page as one list
  const hasOs = !edition.off("os_updates");
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: UpdateTab };
  const setTab = (next: UpdateTab) => void navigate({ to: "/updates", search: { tab: next }, replace: true });
  const os = useOsUpdates(hasOs);
  const actions = useUpdateActions();
  const [dialog, setDialog] = useState<Dialog>(null);
  const d = q.data;

  const header = (
    <div className="flex flex-col gap-[3px]">
      <PageTitle>{t("up.title")}</PageTitle>
      {!(q.isError && !d) && <span className="text-xs text-muted">{subtitle(t, d)}</span>}
    </div>
  );

  if (!d) {
    return (
      <div className="flex flex-col gap-3.5">
        {header}
        {q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />}
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
  const canUpdateNow = !!selectedUpdate && !!currentBundle && selectedUpdate.state !== NodeUpdateState.OFFLINE &&
    canUpdateNode(selectedUpdate, currentBundle.built) && (!activeRollout || queueAfterRollout);
  const updateBlockReason = !selectedUpdate ? undefined
    : selectedUpdate.state === NodeUpdateState.OFFLINE ? "offline"
      : updateStep ? "queued"
        : activeRollout?.status === RolloutStatus.PAUSED ? "paused"
          : activeRollout && !activeBundleMatches ? "differentRelease"
            : !canUpdateNow ? "unavailable" : undefined;

  const agents = (
    <div className="flex flex-col gap-3.5">
      <HeroCard
        data={d}
        owner={owner}
        actions={actions}
        onCancel={() => d.rollout && setDialog({ kind: "cancel", id: d.rollout.id })}
        onRollout={() => setDialog({ kind: "rollout" })}
      />
      {/* a rollout in progress sits right under the hero; the last finished one is history, after the nodes */}
      {activeRollout && <RolloutCard rollout={activeRollout} />}
      <NodeTable
        nodes={d.nodes}
        data={d}
        owner={owner}
        rollout={activeRollout}
        onUpdate={(n) => setDialog({ kind: "update", node: n })}
        onSchedule={(n) => setDialog({ kind: "update", node: n, mode: "schedule" })}
        onRollback={(n) => setDialog({ kind: "rollback", node: n })}
      />
      {d.rollout && !activeRollout && <RolloutCard rollout={d.rollout} />}
    </div>
  );
  // ---- the three layers: each card says where its layer stands, the chosen one opens below
  const panelLook = panelUpdate(t, d, edition.edge);
  const hero = heroOf(d);
  const outdated = outdatedNodes(d.nodes).length;
  // "up to date" is what the node itself reported, not "whatever is not outdated": an offline node's version is unknown
  // against a new bundle, and counting it as current made the card say 3 of 11 when the three were simply offline.
  const upToDate = d.nodes.filter((n) => n.state === NodeUpdateState.UP_TO_DATE).length;
  const offline = d.nodes.filter((n) => n.state === NodeUpdateState.OFFLINE).length;
  const agentsPill: { kind: StatusKind; label: string } =
    hero.id === "running" ? { kind: "busy", label: t("up.layer.pill.running") }
      : hero.id === "paused" ? { kind: "warn", label: t("up.layer.pill.paused") }
        : hero.id === "none" ? { kind: "off", label: t("up.nodes.empty") }
          : outdated > 0 ? { kind: "warn", label: t("up.layer.pill.outdated", { n: outdated }) }
            : hero.id === "current" ? { kind: "ok", label: t("up.layer.pill.ok") }
              : { kind: "warn", label: t("up.layer.pill.attention") };
  const osNeeds = os.data ? osSummary(osRows(os.data)) : undefined;
  const osPill: { kind: StatusKind; label: string } = !osNeeds || osNeeds.known === 0 ? { kind: "off", label: t("up.os.state.unknown") }
    : osNeeds.reboot > 0 ? { kind: "warn", label: t("up.layer.pill.reboot", { n: osNeeds.reboot }) }
      : osNeeds.security > 0 ? { kind: "warn", label: t("up.layer.pill.security", { n: osNeeds.security }) }
        : osNeeds.updates > 0 ? { kind: "off", label: t("up.layer.pill.updates", { n: osNeeds.updates }) }
          : { kind: "ok", label: t("up.layer.pill.ok") };
  const layers: Layer[] = [
    {
      value: "panel", icon: "server", tone: "lavender", title: t("up.panel.title"), fact: d.panel?.version || "—",
      pill: { kind: panelLook.kind, label: panelLook.label },
      content: <PanelBar data={d} owner={isOwner} actions={actions} />,
    },
    {
      value: "agents", icon: "network", tone: "sky", title: t("up.tab.agents"),
      fact: d.nodes.length === 0 ? "—"
        : t("up.layer.agents.fact", { ok: upToDate, n: d.nodes.length }) + (offline > 0 ? " · " + t("up.layer.agents.offline", { n: offline }) : ""),
      pill: agentsPill, content: agents,
    },
    ...(hasOs
      ? [{
          value: "os" as const, icon: "cpu" as const, tone: "mint" as const, title: t("up.tab.os"),
          fact: os.data ? t("up.layer.os.fact", { n: os.data.nodes.length + 1 }) : "—",
          pill: osPill, content: <OsSection />,
        }]
      : []),
  ];
  // without a choice in the address the page opens on the layer that needs a hand: the panel is updated before its nodes
  const wanted = search.tab && layers.some((l) => l.value === search.tab) ? search.tab : undefined;
  const tab: UpdateTab = wanted ?? (panelLook.update?.available ? "panel" : "agents");

  return (
    <div className="flex flex-col gap-3.5">
      {header}
      <EditionNotice feature="agent_updates" />
      <Layers aria-label={t("up.tabs")} layers={layers} value={tab} onValueChange={setTab} />
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
          onSchedule={(localDatetime) => go(actions.scheduleNode({
            nodeId: dialog.node.nodeId,
            localDatetime,
            timezoneOffsetMinutes: d.scheduleTimezoneOffsetMinutes,
            expectedVersion: d.bundle?.version ?? "",
            expectedBuilt: d.bundle?.built ?? 0,
          }))}
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
