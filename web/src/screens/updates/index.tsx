import { useState } from "react";
import { PageTitle } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { BundleStatus, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import {
  canStart,
  heroOf,
  isActive,
  outdatedNodes,
  useIsOwner,
  useUpdateActions,
  useUpdates,
  type NodeUpdate,
  type Updates,
} from "@/lib/updates";
import { BundleCard, PanelCard } from "./cards";
import { CancelDialog, RollbackDialog, StartDialog } from "./dialogs";
import { HeroCard } from "./hero";
import { NodeTable } from "./nodes";
import { RolloutCard } from "./rollout";

type Dialog = { kind: "start"; nodes: NodeUpdate[]; single: boolean } | { kind: "rollback"; node: NodeUpdate } | { kind: "cancel"; id: string } | null;

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

/** Updates: the release bundle the panel holds, what every node runs, and the staged rollout (canary, batches, rollback). */
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

  const version = d.bundle?.version ?? "";
  const start = canStart(d, owner);
  // a retry of one node needs a trusted bundle and no running rollout, same as "update all"
  const allowUpdate = d.bundle?.status === BundleStatus.TRUSTED && !isActive(d.rollout);
  const close = () => setDialog(null);
  // the dialog closes once the call went through; a refusal leaves it open under the toast
  const go = (run: Promise<boolean>) => void run.then((ok) => ok && close());

  return (
    <div className="flex flex-col gap-3.5">
      {header}
      <HeroCard
        data={d}
        owner={owner}
        canStart={start}
        actions={actions}
        onStart={() => setDialog({ kind: "start", nodes: outdatedNodes(d.nodes), single: false })}
        onCancel={() => d.rollout && setDialog({ kind: "cancel", id: d.rollout.id })}
      />
      {d.rollout && <RolloutCard rollout={d.rollout} />}
      <NodeTable
        nodes={d.nodes}
        data={d}
        owner={owner}
        allowUpdate={allowUpdate}
        onUpdate={(n) => setDialog({ kind: "start", nodes: [n], single: true })}
        onRollback={(n) => setDialog({ kind: "rollback", node: n })}
      />
      <div className="grid items-start gap-3.5 md:grid-cols-2">
        <BundleCard data={d} owner={owner} actions={actions} />
        <PanelCard data={d} owner={owner} actions={actions} />
      </div>
      {dialog?.kind === "start" && (
        <StartDialog
          nodes={dialog.nodes}
          version={version}
          single={dialog.single}
          busy={actions.busy}
          onClose={close}
          onConfirm={() => go(actions.start(dialog.single ? dialog.nodes.map((n) => n.nodeId) : []))}
        />
      )}
      {dialog?.kind === "rollback" && (
        <RollbackDialog node={dialog.node} busy={actions.busy} onClose={close} onConfirm={() => go(actions.rollback({ id: dialog.node.nodeId, name: dialog.node.name }))} />
      )}
      {dialog?.kind === "cancel" && <CancelDialog busy={actions.busy} onClose={close} onConfirm={() => go(actions.cancel(dialog.id))} />}
    </div>
  );
}
