import { ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import type { GetWarpReachSettingsResponse, UpdateWarpReachSettingsResponse } from "@/gen/mistgate/warpreach/v1/warpreach_pb";
import { warpReach } from "./api";
import { errorCode, errorText } from "./errors";

// The sites every node's WARP is checked against (proto/mistgate/warpreach/v1), owner only. The panel asks each node every 15 minutes
// to fetch them through its tunnel and say which show a block page. An empty list switches the check off. The Cloudflare edition has
// no such check and answers unavailable_in_edition: feature=health_checks.

/** How many names the panel keeps (plugin.MaxWarpReachDomains). */
export const reachMax = 8;

export const warpReachQuery = queryOptions({
  queryKey: ["warp-reach", "settings"],
  queryFn: () => warpReach.getWarpReachSettings({}),
  retry: false,
});

/** The panel does not run this check (the Cloudflare edition): the refusal names the edition, not a failure. */
export const isReachUnavailable = (e: unknown) => errorCode(ConnectError.from(e).rawMessage) === "unavailable_in_edition";

const label = "[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?";
const host = new RegExp(`^${label}(?:\\.${label})+$`);

/** What the panel accepts as a name: a lower-case host with at least two parts, no address, port or path. The panel decides; this is the hint. */
export const isReachDomain = (s: string) => s.length <= 253 && host.test(s);

/** What a typed name is cleaned to before it is judged: the spaces off and the case down (the panel takes only lower case). */
export const cleanReachDomain = (s: string) => s.trim().toLowerCase();

/** The refusal codes the panel gives for a list (INVALID_ARGUMENT), the same words the client hints with. */
export type ReachProblem = "reach_domains_too_many" | "reach_domain_invalid" | "reach_domain_duplicate";
const problems: readonly string[] = ["reach_domains_too_many", "reach_domain_invalid", "reach_domain_duplicate"];

/** The code of a refusal of a list, if it is one of the three; anything else is another failure. */
export function reachProblemOf(e: unknown): ReachProblem | undefined {
  const code = errorCode(ConnectError.from(e).rawMessage);
  return problems.includes(code) ? (code as ReachProblem) : undefined;
}

/** What is wrong with a list as the panel would judge it (a hint ahead of the call), if anything. */
export function listProblem(list: readonly string[]): ReachProblem | undefined {
  if (list.length > reachMax) return "reach_domains_too_many";
  if (list.some((d) => !isReachDomain(d))) return "reach_domain_invalid";
  if (new Set(list).size !== list.length) return "reach_domain_duplicate";
  return undefined;
}

/** The list the panel holds, the built-in one and whether the owner changed it. */
export type ReachState = { domains: string[]; defaults: string[]; customised: boolean };

export const reachStateOf = (r: GetWarpReachSettingsResponse): ReachState => ({
  domains: [...(r.settings?.domains ?? [])],
  defaults: [...(r.defaults?.domains ?? [])],
  customised: r.customised,
});

/** Saves the list, or goes back to the built-in one. A refusal by its code is for the card to show beside the field, so it is thrown on; another failure is a toast. */
export function useWarpReachActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const done = (message: "saved" | "reset") => (r: UpdateWarpReachSettingsResponse) => {
    qc.setQueryData(warpReachQuery.queryKey, (old: GetWarpReachSettingsResponse | undefined) => (old ? { ...old, settings: r.settings, customised: r.customised } : old));
    toast(t(message === "saved" ? "sx.warp.saved" : "sx.warp.resetDone"));
  };
  const onError = (e: unknown) => {
    if (!reachProblemOf(e)) toast.error(errorText(e, t));
  };
  const save = useMutation({ mutationFn: (domains: string[]) => warpReach.updateWarpReachSettings({ settings: { domains } }), onSuccess: done("saved"), onError });
  const reset = useMutation({ mutationFn: () => warpReach.updateWarpReachSettings({ useDefaults: true }), onSuccess: done("reset"), onError });
  return { save, reset, busy: save.isPending || reset.isPending };
}
