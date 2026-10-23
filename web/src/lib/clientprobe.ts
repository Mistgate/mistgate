import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { GetClientProbeResponse } from "@/gen/mistgate/admin/v1/client_probe_pb";
import { useT, type T } from "@/i18n";
import { clientProbe } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";

// The kl!ck card of the Reachability sources (client_probe.proto): people who use the kl!ck app send what its local check saw
// of the servers from their own networks. Owner only; switching and disconnecting need a step-up. The words are in
// i18n/country.ts ("kc.").

export type ClientProbe = Plain<GetClientProbeResponse>;
export type ClientProbeNode = ClientProbe["nodes"][number];
export type ClientProbeTally = NonNullable<ClientProbeNode["tally"]>;

export const clientProbeQuery = queryOptions({
  queryKey: ["clientProbe"],
  queryFn: async ({ signal }) => plain(await clientProbe.getClientProbe({}, { signal })),
});

export type ClientProbeState = { kind: "off" } | { kind: "waiting" } | { kind: "works"; reports: number; lastAt: number };

/** The state line: off, on with no report yet, or on with reports (how many in 7 days, the newest). */
export function clientProbeState(p: ClientProbe): ClientProbeState {
  const s = p.settings;
  if (!s?.enabled) return { kind: "off" };
  if (!s.reports) return { kind: "waiting" };
  return { kind: "works", reports: s.reports, lastAt: s.lastUnix };
}

/** What went wrong for the reports of a node or a group: freezes, blocks, failures, each counted; none is "". */
export function troubleText(t: T, x: ClientProbeTally): string {
  const failed = x.handshakeFailed + x.noAnswer;
  return [
    x.freeze ? t("kc.freeze", { n: x.freeze }) : "",
    x.tcpBlocked ? t("kc.tcpBlocked", { n: x.tcpBlocked }) : "",
    failed ? t("kc.failed", { n: failed }) : "",
  ]
    .filter(Boolean)
    .join(", ");
}

/** One operator or region: its name ("unknown" for none), its reports and what they said ("MTS PJSC (5): freeze 3"). */
export function groupText(t: T, name: string, x: ClientProbeTally): string {
  const trouble = troubleText(t, x);
  return t(trouble ? "kc.group.trouble" : "kc.group.ok", { name: name || t("kc.unknown"), trouble, n: x.reports });
}

/** The worst a node's reports said, for its mark: a freeze or a block is the warning the source is there for. */
export function nodeKind(x: ClientProbeTally): "ok" | "warn" | "unknown" {
  if (x.freeze || x.tcpBlocked) return "warn";
  if (x.ok === x.reports) return "ok";
  return "unknown";
}

export function useClientProbeActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
  };
  const fresh = () => void qc.invalidateQueries({ queryKey: clientProbeQuery.queryKey });

  const update = useMutation({
    mutationFn: async (req: { enabled: boolean }) => plain((await guard(() => clientProbe.updateClientProbe(req))).settings!),
    onSuccess: (s) => {
      fresh();
      toast(t(s.enabled ? "kc.on" : "kc.off"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => clientProbe.disconnectClientProbe({}))).settings!),
    onSuccess: () => {
      fresh();
      toast(t("kc.disconnected"));
    },
    onError: fail,
  });
  return { update, disconnect };
}
