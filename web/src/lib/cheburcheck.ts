import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { CheburcheckSettings as SettingsMsg, TestCheburcheckResponse } from "@/gen/mistgate/admin/v1/cheburcheck_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cheburcheck } from "./api";
import { sourceErrorText } from "./globalping";
import { plain, type Plain } from "./plain";

// The cheburcheck card of the Reachability page (cheburcheck.proto): volunteer probes of cheburcheck.ru test whether a SYN to
// each node's IPv4 address passes the TSPU box; an own probe (a cheburprobe token) can run more often. Owner only; every change
// and Disconnect need a step-up, Test does not (it reads the site's status and sends no address). The token goes up once and
// never comes back. The words are in i18n/country.ts.

export type CheburcheckSettings = Plain<SettingsMsg>;
export type CheburcheckTest = Plain<TestCheburcheckResponse>;

export const cheburcheckQuery = queryOptions({
  queryKey: ["cheburcheck"],
  queryFn: async ({ signal }) => plain((await cheburcheck.getCheburcheck({}, { signal })).settings!),
});

/** Minutes between runs on offer. Public probes: 3 h at the least (the site replays its 3-hour cache), the own probe: 30 min. */
export const publicIntervals = [180, 360, 720, 1440] as const;
export const ownIntervals = [30, 60, 120, 180, 360] as const;

const errorKeys: Record<string, MessageKey> = {
  token_invalid: "cc.err.token",
  "interval_min: at least 180": "cc.err.interval",
  "own_interval_min: 30 to 10080": "cc.err.ownInterval",
  cheburcheck_unreachable: "cc.err.unreachable",
};

export const cheburcheckErrorText = (e: unknown, t: T) => sourceErrorText(e, t, errorKeys);

/** What the last Test said, and when (unix seconds); it lives as long as the page. */
export type CheburcheckOutcome = { at: number } & ({ ok: CheburcheckTest } | { error: unknown });

/** The statuses the server keeps on the source: each has its own words (cc.status.<code>). */
const statuses = ["rate_limited", "ip_probes_off", "token_unknown", "unreachable"];

export type CheburcheckState =
  | { kind: "off" }
  | { kind: "unavailable" }
  | { kind: "untested" }
  | { kind: "works"; test?: CheburcheckTest; runAt?: number; verdict?: string }
  | { kind: "fails"; error: unknown }
  | { kind: "status"; status: string; holdUntil: number };

/**
 * The state line: off; on for another country than Russia; the newer of the last Test and the last stored run; a status the
 * server holds (the site asked to slow down, it switched address checks off, it does not know the token) before a run.
 */
export function cheburcheckState(s: CheburcheckSettings, test: CheburcheckOutcome | null): CheburcheckState {
  if (!s.enabled) return { kind: "off" };
  if (s.unavailable) return { kind: "unavailable" };
  if (test && test.at >= s.lastRunUnix) return "ok" in test ? { kind: "works", test: test.ok } : { kind: "fails", error: test.error };
  if (statuses.includes(s.status)) return { kind: "status", status: s.status, holdUntil: s.holdUntilUnix };
  if (s.lastRunUnix > 0) return { kind: "works", runAt: s.lastRunUnix, verdict: s.lastVerdict };
  return { kind: "untested" };
}

export function useCheburcheckActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(cheburcheckErrorText(e, t));
  };
  const settle = (s: CheburcheckSettings) => {
    qc.setQueryData(cheburcheckQuery.queryKey, s);
    // on and off change GetChecks and the week's lanes at once
    void qc.invalidateQueries({ queryKey: ["health", "checks"] });
  };

  // every change asks for a fresh step-up; only the fields that change are sent, the token in this one call and nowhere else
  const update = useMutation({
    mutationFn: async (req: { enabled?: boolean; publicEnabled?: boolean; token?: string; intervalMin?: number; ownIntervalMin?: number }) =>
      plain((await guard(() => cheburcheck.updateCheburcheck(req))).settings!),
    onSuccess: (s, req) => {
      settle(s);
      toast(
        req.token !== undefined ? t(req.token ? "cc.token.saved" : "cc.token.removed") : req.enabled !== undefined ? t(s.enabled ? "cc.on" : "cc.off") : t("common.saved"),
      );
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => cheburcheck.disconnectCheburcheck({}))).settings!),
    onSuccess: (s) => {
      settle(s);
      toast(t("cc.disconnected"));
    },
    onError: fail,
  });
  // no step-up: it reads the site's status and sends no address
  const test = useMutation({ mutationFn: async () => plain(await cheburcheck.testCheburcheck({})) });
  return { update, disconnect, test };
}
