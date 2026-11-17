import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { decoySites, nodes as nodesApi } from "@/lib/api";
import { asDecoyMode, type DecoyMode } from "@/lib/decoy";
import { isDraft, sitesQuery } from "@/lib/decoysites";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import type { Plain } from "@/lib/plain";
import { LibraryPicker } from "./decoys/picker";
import { DecoyModeChoice } from "./node/decoy";

type Node = Plain<NodeMsg>;

/**
 * "Decoy site…" of the Nodes page's bulk bar: one choice for all the selected nodes in one call (up to 50). Built-in and
 * the panel's directory are a mode (SetNodesDecoyMode); a site of your own is one site of the library, chosen from the list
 * under it and put on every node (AssignDecoySite), with a link to open it. Own is off while the library has no ready site
 * and in the Cloudflare edition. Once the panel took it, `onDone` runs (the bar lists the nodes, each with its state read
 * live) and the window closes; a refusal stays in it.
 */
export function DecoyBulkDialog({ nodes, onClose, onDone }: { nodes: readonly Node[]; onClose: () => void; onDone: () => void }) {
  const t = useT();
  const qc = useQueryClient();
  const edge = useEdition().off("decoy_own");
  const library = useQuery({ ...sitesQuery, enabled: !edge });
  const ready = (library.data?.sites ?? []).filter((x) => !isDraft(x));
  const noSites = !edge && library.isSuccess && ready.length === 0;
  const ownOff = edge || noSites;
  const panelOff = nodes.some((n) => !n.decoyPanelAvailable);

  // start from the mode they all share, when they share one that can still be chosen
  const [start] = useState<DecoyMode>(() => {
    const first = asDecoyMode(nodes[0]?.decoyMode ?? "");
    const same = nodes.every((n) => asDecoyMode(n.decoyMode) === first);
    return same && !(first === "own" && ownOff) && !(first === "panel" && panelOff) ? first : "builtin";
  });
  const [mode, setMode] = useState<DecoyMode>(start);
  const [siteId, setSiteId] = useState("");
  const [problem, setProblem] = useState("");

  const apply = useMutation({
    mutationFn: async () => {
      const nodeIds = nodes.map((n) => n.id);
      if (mode === "own") await decoySites.assignDecoySite({ siteId, nodeIds, panel: false });
      else await nodesApi.setNodesDecoyMode({ nodeIds, mode });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["decoy-sites"] });
      void qc.invalidateQueries({ queryKey: ["decoy-site"] });
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
          <Button variant="primary" size="md" disabled={apply.isPending || (mode === "own" && (ownOff || !siteId)) || (mode === "panel" && panelOff)} onClick={() => apply.mutate()}>
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
        ownNote={noSites ? t("dz.bulk.noSites") : undefined}
        ownExtra={
          <>
            <LibraryPicker value={siteId} onChange={setSiteId} disabled={apply.isPending} />
            {!siteId && ready.length > 0 && <p className="text-xs text-muted">{t("dz.bulk.needSite")}</p>}
          </>
        }
      />
      <EditionNotice feature="decoy_own" />
      {problem && <Notice tone="danger">{problem}</Notice>}
    </Modal>
  );
}
