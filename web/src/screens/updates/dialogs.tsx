import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { useT } from "@/i18n";
import { canaryOf, defaultBatch, type NodeUpdate } from "@/lib/updates";

/**
 * "Update all" and the per-node "Update": says in plain words what will happen before anything is sent. `nodes` are the
 * ones that will be updated (every outdated node for "all"); the panel picks the canary by the same rule as canaryOf.
 */
export function StartDialog({ nodes, version, single, busy, onConfirm, onClose }: { nodes: NodeUpdate[]; version: string; single: boolean; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  const canary = canaryOf(nodes);
  const one = single || nodes.length === 1;
  const name = nodes[0]?.name ?? "";
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={one ? t("up.start.titleOne", { name }) : t("up.start.title")}
      description={one ? t("up.start.leadOne", { name, version }) : t("up.start.lead", { version })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={busy} onClick={onConfirm}>
            {t("up.start.confirm")}
          </Button>
        </>
      }
    >
      <ol className="flex list-decimal flex-col gap-2 pl-5 text-[13px] leading-normal text-pretty marker:font-bold marker:text-muted">
        {one ? (
          <>
            <li>{t("up.start.step1One", { name })}</li>
            <li>{t("up.start.step2")}</li>
            <li>{t("up.start.step3One")}</li>
          </>
        ) : (
          <>
            <li>{t("up.start.step1", { name: canary?.name ?? "", users: canary?.onlineUsers ?? 0 })}</li>
            <li>{t("up.start.step2")}</li>
            <li>{t("up.start.step3", { batch: defaultBatch(nodes.length) })}</li>
          </>
        )}
      </ol>
      {!one && (
        <div className="flex flex-col gap-1.5">
          <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("up.start.nodes")}</span>
          <div className="flex flex-wrap gap-1.5">
            {nodes.map((n) => (
              <span key={n.nodeId} className="inline-flex h-[22px] items-center rounded-ctl bg-surface-2 px-2 font-mono text-[11px] font-semibold">
                {n.name}
              </span>
            ))}
          </div>
        </div>
      )}
    </Modal>
  );
}

export function RollbackDialog({ node, busy, onConfirm, onClose }: { node: NodeUpdate; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  const from = node.lastUpdate?.fromVersion ?? "";
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.rollback.title", { name: node.name })}
      description={from ? t("up.rollback.body", { version: from }) : t("up.rollback.bodyNoVersion")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("up.rollback.confirm")}
          </Button>
        </>
      }
    />
  );
}

export function CancelDialog({ busy, onConfirm, onClose }: { busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.cancel.title")}
      description={t("up.cancel.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("up.cancel.keep")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("up.cancel.confirm")}
          </Button>
        </>
      }
    />
  );
}
