import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { BlocklistSettings as SettingsMsg, TestBlocklistResponse } from "@/gen/mistgate/admin/v1/blocklist_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { blocklist } from "./api";
import { sourceErrorText } from "./globalping";
import { plain, type Plain } from "./plain";

// The block-list card of the "Reachability from a country" group (blocklist.proto): the panel downloads the public lists and
// compares the node addresses itself, so nothing about a node leaves it. Owner only; switching and disconnecting need a
// step-up, Test does not. The server answers with short codes; the words are in i18n/country.ts.

export type BlocklistSettings = Plain<SettingsMsg>;
export type BlocklistTest = Plain<TestBlocklistResponse>;

export const blocklistQuery = queryOptions({
  queryKey: ["blocklist"],
  queryFn: async ({ signal }) => plain((await blocklist.getBlocklist({}, { signal })).settings!),
});

const errorKeys: Record<string, MessageKey> = {
  blocklist_off: "bl.err.off",
  blocklist_unavailable_for_country: "bl.err.country",
  blocklist_unreachable: "bl.err.unreachable",
};

export const blocklistErrorText = (e: unknown, t: T) => sourceErrorText(e, t, errorKeys);

/** What the last Test said, and when (unix seconds): kept as long as the page, like the Globalping card's. */
export type ListsOutcome = { at: number } & ({ ok: BlocklistTest } | { error: unknown });

export type ListsState =
  | { kind: "off" }
  | { kind: "unavailable" }
  | { kind: "untested" }
  | { kind: "works"; listsAt: number; checkedAt: number; test?: BlocklistTest }
  | { kind: "fails"; error: unknown };

/**
 * The state line of the card. Off, and on for another country than Russia, say so whatever happened before. Otherwise the
 * newer of the last Test and the last stored check decides, as on the Globalping card.
 */
export function listsState(s: BlocklistSettings, test: ListsOutcome | null): ListsState {
  if (!s.enabled) return { kind: "off" };
  if (!s.available) return { kind: "unavailable" };
  if (test && test.at >= s.checkedUnix)
    return "ok" in test ? { kind: "works", listsAt: test.ok.listsUnix, checkedAt: test.at, test: test.ok } : { kind: "fails", error: test.error };
  if (s.checkedUnix) return { kind: "works", listsAt: s.listsUnix, checkedAt: s.checkedUnix };
  return { kind: "untested" };
}

export function useBlocklistActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(blocklistErrorText(e, t));
  };
  // the verdicts on Health and the node pages follow at once: off hides the lists' word, a Test stores a new one
  const fresh = () => void qc.invalidateQueries({ queryKey: ["health", "checks"] });
  const settle = (s: BlocklistSettings) => {
    qc.setQueryData(blocklistQuery.queryKey, s);
    fresh();
  };

  const update = useMutation({
    mutationFn: async (req: { enabled: boolean }) => plain((await guard(() => blocklist.updateBlocklist(req))).settings!),
    onSuccess: (s) => {
      settle(s);
      toast(t(s.enabled ? "bl.on" : "bl.off"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => blocklist.disconnectBlocklist({}))).settings!),
    onSuccess: (s) => {
      settle(s);
      toast(t("bl.disconnected"));
    },
    onError: fail,
  });
  // no step-up: it downloads public lists and sends nothing about the nodes
  const test = useMutation({
    mutationFn: async () => plain(await blocklist.testBlocklist({})),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: blocklistQuery.queryKey });
      fresh();
    },
  });
  return { update, disconnect, test };
}
