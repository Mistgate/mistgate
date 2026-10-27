import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { BsbordSettings as SettingsMsg, BsbordUnit as UnitMsg, PreviewBsbordResponse, TestBsbordResponse } from "@/gen/mistgate/admin/v1/bsbord_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { bsbord } from "./api";
import { isTransient } from "./errors";
import type { Fmt } from "./format";
import { sourceErrorText } from "./globalping";
import { plain, type Plain } from "./plain";

// The bsbord card of the Reachability page (bsbord.proto): paid SIM probes of mobile operators (a "unit" is one operator, region
// and white-list mode) test each node's IP:port. Owner only. The key goes up once and never comes back; changes, Disconnect and
// the price (Preview: free, but it sends the node addresses) need a step-up, Test does not (it reads the account and the unit
// list, no address). One credit is one kopeck: the card speaks roubles. The words are in i18n/country.ts.

export type BsbordSettings = Plain<SettingsMsg>;
export type BsbordUnit = Plain<UnitMsg>;
export type BsbordTest = Plain<TestBsbordResponse>;
export type BsbordPreview = Plain<PreviewBsbordResponse>;

/** GetBsbord for another country than Russia: the card says "not available" instead of failing. */
export const isCountryOff = (e: unknown) => ConnectError.from(e).rawMessage === "bsbord_country_unavailable";

export const bsbordQuery = queryOptions({
  queryKey: ["bsbord"],
  queryFn: async ({ signal }) => plain((await bsbord.getBsbord({}, { signal })).settings!),
  // "only for Russia" comes as Unavailable: an answer, not a dropped connection
  retry: (count, e) => count < 2 && isTransient(e) && !isCountryOff(e),
});

/** The units bsbord offers now (it asks bsbord: needs the key). The list changes slowly; a few minutes is fresh enough. */
export const unitsQuery = queryOptions({
  queryKey: ["bsbord", "units"],
  queryFn: async ({ signal }) => plain(await bsbord.listBsbordUnits({}, { signal })),
  staleTime: 5 * 60_000,
});

/** Minutes between rounds on offer (the server takes 60..1440, default 1440). */
export const bsbordIntervals = [60, 120, 180, 360, 720, 1440] as const;
export const bsbordDefaultInterval = 1440;
/** The cap the server takes, in roubles (1..1e9 credits). */
export const capRange = { min: 1, max: 10_000_000 } as const;

/** Credits (kopecks) in roubles: "100 ₽", "1.60 ₽" (the UI language's separators); kopecks only when there are some. */
export function rub(fmt: Fmt, credits: number): string {
  const r = credits / 100;
  return `${Number.isInteger(r) ? fmt.num(r, 0) : fmt.num(r, 2, true)} ₽`;
}

/** The shape of a key: bsk_live_ and up to 256 characters, no spaces. The server checks it again. */
export const looksLikeKey = (s: string) => /^bsk_live_\S{1,247}$/.test(s.trim());

const errorKeys: Record<string, MessageKey> = {
  key_invalid: "bb.err.key",
  units_invalid: "bb.err.units",
  "interval_min: 60 to 1440": "bb.err.interval",
  "cap_credits: 1 to 1000000000": "bb.err.cap",
  bsbord_key_required: "bb.err.keyRequired",
  bsbord_key_unreadable: "bb.err.keyUnreadable",
  bsbord_units_required: "bb.err.unitsRequired",
  bsbord_country_unavailable: "bb.err.country",
  bsbord_targets_unavailable: "bb.err.preview",
  bsbord_preview_unavailable: "bb.err.preview",
  bsbord_no_targets: "bb.err.noTargets",
  bsbord_unauthorized: "bb.err.unauthorized",
  bsbord_tier_too_low: "bb.err.tier",
  bsbord_no_balance: "bb.err.noBalance",
  bsbord_unreachable: "bb.err.unreachable",
};

/** A failed call in words; bsbord's own "slow down" is not the panel's "asked too often". */
export function bsbordErrorText(e: unknown, t: T): string {
  const c = ConnectError.from(e);
  if (c.code === Code.ResourceExhausted && c.rawMessage === "bsbord_rate_limited") return t("bb.err.rateLimited");
  return sourceErrorText(e, t, errorKeys);
}

/** What the last Test said, and when; it lives as long as the page. */
export type BsbordOutcome = { at: number } & ({ ok: BsbordTest } | { error: unknown });

/** The statuses that say something is wrong or under way; each has its words (bb.status.<code>). "ok" and "idle" do not. */
const statuses = ["pending", "cap_reached", "no_balance", "tier_too_low", "lost", "unauthorized", "rate_limited", "unavailable", "no_dpi_on", "no_targets", "no_units", "rejected", "cancelled"];

export type BsbordState =
  | { kind: "off"; ready: boolean }
  | { kind: "untested" }
  | { kind: "works"; test?: BsbordTest; runAt?: number }
  | { kind: "fails"; error: unknown }
  | { kind: "status"; status: string; holdUntil: number; runAt: number };

/** The state line: off (and whether the key and the units are there to switch it on); the newer of the last Test and the last round; what the last round ended with. */
export function bsbordState(s: BsbordSettings, test: BsbordOutcome | null): BsbordState {
  if (!s.enabled) return { kind: "off", ready: s.hasKey && s.units.length > 0 };
  if (test && test.at >= s.lastRunUnix) return "ok" in test ? { kind: "works", test: test.ok } : { kind: "fails", error: test.error };
  if (statuses.includes(s.lastStatus)) return { kind: "status", status: s.lastStatus, holdUntil: s.holdUntilUnix, runAt: s.lastRunUnix };
  if (s.lastRunUnix > 0) return { kind: "works", runAt: s.lastRunUnix };
  return { kind: "untested" };
}

/** A hold the server keeps until "never" (a refused key, a plan that does not allow it) is no time to show. */
export const holdShown = (holdUntil: number, now = Date.now() / 1000) => holdUntil > now && holdUntil < now + 400 * 86400;

export type District = { code: string; name: string; units: BsbordUnit[] };

const CRIMEA = "krym";

/** The units by district: Crimea first (the default), then by name; inside, by operator and unit name. */
export function districts(units: readonly BsbordUnit[], locale: string): District[] {
  const by = new Map<string, District>();
  for (const u of units) {
    const code = u.regionCode || u.region;
    const d = by.get(code) ?? { code, name: u.region || u.regionCode, units: [] };
    d.units.push(u);
    by.set(code, d);
  }
  const cmp = (a: string, b: string) => a.localeCompare(b, locale);
  for (const d of by.values()) d.units.sort((a, b) => cmp(a.operator, b.operator) || cmp(a.name, b.name));
  return [...by.values()].sort((a, b) => Number(b.code === CRIMEA) - Number(a.code === CRIMEA) || cmp(a.name, b.name));
}

/** The Crimea preset: every probeable unit in Crimea, with and without the white list. */
export const crimeaPreset = (units: readonly BsbordUnit[]) => units.filter((u) => u.probeable && u.regionCode === CRIMEA).map((u) => u.opKey);

/** One per operator in a district: each operator's first probeable unit there without the white list (a white list cuts every node alike). */
export function perOperatorPreset(units: readonly BsbordUnit[], district: string): string[] {
  const seen = new Set<string>();
  const out: string[] = [];
  for (const u of units) {
    if ((u.regionCode || u.region) !== district || !u.probeable || u.dpi !== "off" || seen.has(u.operator)) continue;
    seen.add(u.operator);
    out.push(u.opKey);
  }
  return out;
}

/** A unit under the white list: its operator cuts every foreign address there, whatever the node. */
export const isWhiteList = (u: Pick<BsbordUnit, "dpi">) => u.dpi === "on";

export function useBsbordActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(bsbordErrorText(e, t));
  };
  const settle = (s: BsbordSettings) => {
    qc.setQueryData(bsbordQuery.queryKey, s);
    void qc.invalidateQueries({ queryKey: ["health", "checks"] });
  };

  // every change asks for a fresh step-up; only the fields that change are sent, the key in this one call and nowhere else
  const update = useMutation({
    mutationFn: async (req: { enabled?: boolean; key?: string; intervalMin?: number; capCredits?: number; units?: string[] }) => {
      const { capCredits, units, ...rest } = req;
      const msg = { ...rest, ...(capCredits !== undefined && { capCredits: BigInt(capCredits) }), ...(units && { units: { opKeys: units } }) };
      return plain((await guard(() => bsbord.updateBsbord(msg))).settings!);
    },
    onSuccess: (s, req) => {
      settle(s);
      // a new key may open other units: ask the list again
      if (req.key !== undefined) void qc.invalidateQueries({ queryKey: unitsQuery.queryKey });
      toast(req.key !== undefined ? t("bb.key.saved") : req.enabled !== undefined ? t(s.enabled ? "bb.on" : "bb.off") : t("common.saved"));
    },
    onError: fail,
  });
  const disconnect = useMutation({
    mutationFn: async () => plain((await guard(() => bsbord.disconnectBsbord({}))).settings!),
    onSuccess: (s) => {
      settle(s);
      qc.removeQueries({ queryKey: unitsQuery.queryKey });
      toast(t("bb.disconnected"));
    },
    onError: fail,
  });
  // no step-up: it reads the account and the units, no address
  const test = useMutation({ mutationFn: async () => plain(await bsbord.testBsbord({})) });
  // free, but it sends the node addresses to bsbord: a step-up, and only after the consent (the card asks first)
  const preview = useMutation({ mutationFn: async () => plain(await guard(() => bsbord.previewBsbord({}))) });
  return { update, disconnect, test, preview };
}
