import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { GlobalpingSettings as SettingsMsg, TestGlobalpingResponse } from "@/gen/mistgate/admin/v1/globalping_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { countryReach, globalping } from "./api";
import { errorText } from "./errors";
import { countryIn, isLive, type CountryCheck } from "./health";
import { plain, type Plain } from "./plain";

// The "Reachability from a country" group of the Integrations screen: the shared country (country_reachability.proto) and the
// Globalping card (globalping.proto). Owner only; changing Globalping needs a step-up, the country does not (it sends nothing).
// The server answers with short codes; the words are in i18n/country.ts. The token goes up once and never comes back.

export type GlobalpingSettings = Plain<SettingsMsg>;
export type GlobalpingTest = Plain<TestGlobalpingResponse>;

export const globalpingQuery = queryOptions({
  queryKey: ["globalping"],
  queryFn: async ({ signal }) => plain((await globalping.getGlobalping({}, { signal })).settings!),
});

export const countryQuery = queryOptions({
  queryKey: ["country-reachability"],
  queryFn: async ({ signal }) => plain((await countryReach.getCountryReachability({}, { signal })).settings!),
});

/** The countries on offer: those the server has a control site of their own for (health/country.go countryControls). */
export const countryOptions = ["RU", "BY", "KZ", "CN", "IR", "TR"] as const;

/** Minutes between rounds on offer (the server takes 10..1440); a stored value outside the list is shown too. */
export const intervalOptions = [10, 15, 30, 60, 180, 360] as const;

/** The shape of a token: up to 256 characters, no spaces. The server checks it again; this only saves a round trip. */
export const looksLikeToken = (s: string) => /^\S{1,256}$/.test(s.trim());

/**
 * A failed call of a source card as one sentence: a test asked again too soon, the source's own codes (`keys`, from the
 * message of the error), the rest through errorText.
 */
export function sourceErrorText(e: unknown, t: T, keys: Record<string, MessageKey>): string {
  const c = ConnectError.from(e);
  if (c.code === Code.ResourceExhausted) return t("cr.tooSoon");
  if (c.code !== Code.PermissionDenied) {
    const key = keys[c.rawMessage];
    if (key) return t(key);
  }
  return errorText(e, t);
}

const errorKeys: Record<string, MessageKey> = {
  token_invalid: "gp.err.token",
  globalping_unauthorized: "gp.err.unauthorized",
  globalping_unreachable: "gp.err.unreachable",
};

export const globalpingErrorText = (e: unknown, t: T) => sourceErrorText(e, t, errorKeys);

/** What the last Test said, and when (unix seconds): the server keeps no record of it, so it lives as long as the page. */
export type TestOutcome = { at: number } & ({ ok: GlobalpingTest } | { error: unknown });

/** Unknown rounds that are Globalping's fault, not the nodes': they say the integration itself is in trouble. */
const sourceReasons = ["rate_limited", "source_error", "timeout"];

export type CardState =
  | { kind: "off" }
  | { kind: "untested" }
  | { kind: "works"; test?: GlobalpingTest; roundAt?: number }
  | { kind: "fails"; error: unknown }
  | { kind: "noData"; reason: string; at: number };

/**
 * The state line of the card (plan: off, on and working, failing, never tested). The newer of the last Test and the newest
 * round decides: a Test that failed after a good round is a failure, a good round after a failed Test is working again.
 * Only live rounds count: a row of the block lists alone says nothing about Globalping.
 */
export function cardState(s: Pick<GlobalpingSettings, "enabled">, test: TestOutcome | null, rows: readonly CountryCheck[]): CardState {
  if (!s.enabled) return { kind: "off" };
  const round = rows.filter(isLive).reduce<CountryCheck | undefined>((a, c) => (!a || c.atUnix > a.atUnix ? c : a), undefined);
  if (test && (!round || test.at >= round.atUnix)) return "ok" in test ? { kind: "works", test: test.ok } : { kind: "fails", error: test.error };
  if (round) {
    // the reason is the live round's own, whatever the lists made of the verdict (an unknown round and a list hit: listed)
    if (sourceReasons.includes(round.reason)) return { kind: "noData", reason: round.reason, at: round.atUnix };
    return { kind: "works", roundAt: round.atUnix };
  }
  return { kind: "untested" };
}

export function useGlobalpingActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(globalpingErrorText(e, t));
  };
  const settle = (s: GlobalpingSettings) => {
    qc.setQueryData(globalpingQuery.queryKey, s);
    // off empties GetChecks.country_checks: the column and the node rows go at once
    void qc.invalidateQueries({ queryKey: ["health", "checks"] });
  };

  // every change asks for a fresh step-up; only the fields that change are sent, the token in this one call and nowhere else
  const update = useMutation({
    mutationFn: async (req: { enabled?: boolean; intervalMin?: number; token?: string }) => plain((await guard(() => globalping.updateGlobalping(req))).settings!),
    onSuccess: (s, req) => {
      settle(s);
      toast(req.token !== undefined ? t("gp.token.saved") : req.enabled !== undefined ? t(s.enabled ? "gp.on" : "gp.off") : t("gp.saved"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => globalping.disconnectGlobalping({}))).settings!),
    onSuccess: (s) => {
      settle(s);
      toast(t("gp.disconnected"));
    },
    onError: fail,
  });
  // no step-up: it sends no address and changes nothing
  const test = useMutation({ mutationFn: async () => plain(await globalping.testGlobalping({})) });
  return { update, disconnect, test };
}

export function useCountryActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (country: string) => plain((await countryReach.updateCountryReachability({ country })).settings!),
    onSuccess: (s) => {
      qc.setQueryData(countryQuery.queryKey, s);
      toast(t("cr.country.saved", { country: countryIn(t, s.country) }));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
}
