import type { MessageInitShape } from "@bufbuild/protobuf";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useToast } from "@/components/ui/toast";
import type { UpdateNodeRequestSchema } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";

type Update = Omit<MessageInitShape<typeof UpdateNodeRequestSchema>, "nodeId" | "$typeName">;

/**
 * A form over some fields of a node (Exit, Server). It starts from the values the node had when it opened, once: a poll
 * must not overwrite what is being typed (the caller keys it by the node). `changed` tells what Save sends: UpdateNode
 * leaves an unset field alone, so the two forms never overwrite each other's fields.
 */
export function useNodeForm<T extends Record<string, unknown>>(init: () => T) {
  const [start] = useState(init);
  const [f, setF] = useState(start);
  const set = <K extends keyof T>(k: K, v: T[K]) => setF((x) => ({ ...x, [k]: v }));
  const changed = (k: keyof T) => f[k] !== start[k];
  return { f, set, changed, dirty: (Object.keys(start) as (keyof T)[]).some(changed) };
}

/** Save of a node form: the changed fields go to UpdateNode, then the node, the list and the Overview are read again. */
export function useSaveNode(nodeId: string, fields: () => Update) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => nodesApi.updateNode({ nodeId, ...fields() }),
    onSuccess: () => {
      toast(t("common.saved"));
      void qc.invalidateQueries({ queryKey: ["node", nodeId] });
      void qc.invalidateQueries({ queryKey: ["nodes"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
}
