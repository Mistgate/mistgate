import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, ownBlockedBy, type DecoyMode } from "@/lib/decoy";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { DecoyModeChoice } from "./node/decoy";

type Node = Plain<NodeMsg>;

/**
 * "Decoy site…" of the Nodes page's bulk bar: one mode for all the selected nodes in one call (SetNodesDecoyMode, up to
 * 50). Own is off while any of them has no uploaded site, because the panel refuses it then. Once the panel took it,
 * `onDone` runs (the bar lists the nodes, each with its state read live) and the window closes; a refusal stays in it.
 */
export function DecoyBulkDialog({ nodes, onClose, onDone }: { nodes: readonly Node[]; onClose: () => void; onDone: () => void }) {
  const t = useT();
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
    onSuccess: () => {
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
