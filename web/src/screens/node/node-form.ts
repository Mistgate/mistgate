import type { MessageInitShape } from "@bufbuild/protobuf";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
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
  const [start, setStart] = useState(init);
  const [f, setF] = useState(start);
  const set = <K extends keyof T>(k: K, v: T[K]) => setF((x) => ({ ...x, [k]: v }));
  const changed = (k: keyof T) => f[k] !== start[k];
  const keys = Object.keys(start) as (keyof T)[];
  return {
    f,
    set,
    changed,
    /** Is any of these fields (all of them without a list) different from what the node had? */
    dirtyOf: (of?: readonly (keyof T)[]) => (of ?? keys).some(changed),
    dirty: keys.some(changed),
    /** A save went through: these fields (all) are what the node has now. */
    commit: (of?: readonly (keyof T)[]) => setStart((s) => ({ ...s, ...Object.fromEntries((of ?? keys).map((k) => [k, f[k]])) })),
    /** Back to what the node has: these fields (all). */
    revert: (of?: readonly (keyof T)[]) => setF((x) => ({ ...x, ...Object.fromEntries((of ?? keys).map((k) => [k, start[k]])) })),
  };
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

/** "Saved" for a second and a half after a save went through: the bar's confirmation. */
export function useSavedFlash() {
  const [saved, setSaved] = useState(false);
  const timer = useRef<number | undefined>(undefined);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  return [
    saved,
    () => {
      setSaved(true);
      window.clearTimeout(timer.current);
      timer.current = window.setTimeout(() => setSaved(false), 1800);
    },
  ] as const;
}

/**
 * Save of one section of a form that several cards share (the Server and Exit tabs): what changed in its fields goes to
 * UpdateNode, the form takes them as the node's own, the bar says "Saved" for a moment and the page's wave runs.
 */
export function useSaveSection<T extends Record<string, unknown>>(nodeId: string, form: ReturnType<typeof useNodeForm<T>>, fields: readonly (keyof T)[], payload: () => Update) {
  const [saved, flash] = useSavedFlash();
  const save = useSaveNode(nodeId, payload);
  return {
    saved,
    dirty: form.dirtyOf(fields),
    busy: save.isPending,
    cancel: () => form.revert(fields),
    save: () =>
      save.mutate(undefined, {
        onSuccess: () => {
          form.commit(fields);
          flash();
        },
      }),
  };
}
