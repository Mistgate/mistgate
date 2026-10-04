import { useState } from "react";
import { PageTitle } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
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
import { BundleCard, PanelCard } from "./cards";
import { CancelDialog, RollbackDialog, UpdateNodeDialog } from "./dialogs";
import { HeroCard } from "./hero";
import { NodeTable } from "./nodes";
import { RolloutCard } from "./rollout";

type Dialog = { kind: "update"; node: NodeUpdate } | { kind: "rollback"; node: NodeUpdate } | { kind: "cancel"; id: string } | null;

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

/** Updates: the trusted release bundle, each node's installed version, and its immediate or scheduled update. */
export function UpdatesScreen() {
  const t = useT();
  const q = useUpdates();
  const owner = useIsOwner();
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

  return (
    <div className="flex flex-col gap-3.5">
      {header}
      <HeroCard
        data={d}
        owner={owner}
        actions={actions}
        onCancel={() => d.rollout && setDialog({ kind: "cancel", id: d.rollout.id })}
      />
      {d.rollout && <RolloutCard rollout={d.rollout} />}
      <NodeTable
        nodes={d.nodes}
        data={d}
        owner={owner}
        onUpdate={(n) => setDialog({ kind: "update", node: n })}
        onRollback={(n) => setDialog({ kind: "rollback", node: n })}
      />
      <div className="grid items-start gap-3.5 md:grid-cols-2">
        <BundleCard data={d} owner={owner} actions={actions} />
        <PanelCard data={d} owner={owner} actions={actions} />
      </div>
      {dialog?.kind === "update" && (
        <UpdateNodeDialog
          data={d}
          node={dialog.node}
          canUpdateNow={canUpdateNow}
          queueAfterRollout={queueAfterRollout}
          nodeInActiveRollout={!!updateStep}
          blockedReason={updateBlockReason}
          busy={actions.busy}
          onClose={close}
          onUpdateNow={() => go(actions.start([dialog.node.nodeId]))}
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
