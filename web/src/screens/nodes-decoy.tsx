import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, maxBulkNodes, ownBlockedBy, type DecoyMode } from "@/lib/decoy";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { DecoyModeChoice } from "./node/decoy";

type Node = Plain<NodeMsg>;

/**
 * The bar that appears when nodes are ticked on the Nodes page: how many, and "Set decoy site" for all of them at once
 * (SetNodesDecoyMode, up to 50). Own is off while any ticked node has no uploaded site, because the panel refuses it then.
 */
export function DecoyBulk({ nodes, onClear }: { nodes: Node[]; onClear: () => void }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <div className="flex flex-wrap items-center gap-2 rounded-card border border-accent-line bg-surface px-3.5 py-2.5">
      <span className="text-[13px] font-bold">{t.n("decoy.bulk.selected", nodes.length)}</span>
      {nodes.length >= maxBulkNodes && <span className="text-xs text-muted">{t("decoy.bulk.limit")}</span>}
      <span className="flex-1" />
      <Button variant="primary" size="md" onClick={() => setOpen(true)}>
        {t("decoy.bulk.action")}
      </Button>
      <Button variant="ghost" size="md" onClick={onClear}>
        {t("decoy.bulk.clear")}
      </Button>
      {open && <BulkDialog nodes={nodes} onClose={() => setOpen(false)} onDone={onClear} />}
    </div>
  );
}

function BulkDialog({ nodes, onClose, onDone }: { nodes: Node[]; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const edge = useEdition().off("decoy_own");
  const blocked = ownBlockedBy(nodes);
  const ownOff = edge || blocked.length > 0;
  const panelOff = nodes.some((n) => !n.decoyPanelAvailable);

  // start from the mode they all share, when they share one that can still be chosen
  const [start] = useState<DecoyMode>(() => {
    const first = asDecoyMode(nodes[0]?.decoyMode ?? "");
    const same = nodes.every((n) => asDecoyMode(n.decoyMode) === first);
    return same && !(first === "own" && ownOff) && !(first === "panel" && panelOff) ? first : "builtin";
  });
  const [mode, setMode] = useState<DecoyMode>(start);
  const [problem, setProblem] = useState("");

  const apply = useMutation({
    mutationFn: () => nodesApi.setNodesDecoyMode({ nodeIds: nodes.map((n) => n.id), mode }),
    onSuccess: (r) => {
      toast(t.n("decoy.bulk.done", r.nodes.length || nodes.length));
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      for (const n of nodes) void qc.invalidateQueries({ queryKey: ["node", n.id] });
      onDone();
      onClose();
    },
    onError: (e) => setProblem(errorText(e, t)),
  });

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("decoy.bulk.title", nodes.length)}
      description={nodes.map((n) => n.name).join(", ")}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={apply.isPending || (mode === "own" && ownOff) || (mode === "panel" && panelOff)} onClick={() => apply.mutate()}>
            {t("decoy.apply")}
          </Button>
        </>
      }
    >
      <DecoyModeChoice
        label={t("decoy.bulk.action")}
        value={mode}
        onChange={(m) => {
          setMode(m);
          setProblem("");
        }}
        disabled={apply.isPending}
        panelOff={panelOff}
        panelNote={panelOff ? t("decoy.panelNone") : undefined}
        ownOff={ownOff}
        ownNote={!edge && blocked.length > 0 ? t("decoy.bulk.ownBlocked", { names: blocked.join(", ") }) : undefined}
      />
      <EditionNotice feature="decoy_own" />
      {problem && <Notice tone="danger">{problem}</Notice>}
    </Modal>
  );
}
