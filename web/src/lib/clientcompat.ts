import { keepPreviousData, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { StatusKind } from "@/components/ui/status";
import { ClientCompatState, type ClientCompatRule, type ListOutdatedDevicesResponse, type OutdatedDevice } from "@/gen/mistgate/admin/v1/client_compat_pb";
import { useT, type T } from "@/i18n";
import { clientCompat } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";

// The "App versions" tab of Subscriptions (client_compat.proto): which apps and versions send a post-quantum key share in the
// REALITY handshake. The panel lists the devices that really get REALITY servers with a verdict (ok / below the minimum /
// unsupported / unknown) and keeps the table of minimum versions (the built-in one plus the owner's edits). Nothing here
// disconnects anybody or shows anything to a person. The words are in i18n/clientcompat.ts ("cc.").

export { ClientCompatState };
export type CompatRule = Plain<ClientCompatRule>;
export type CompatDevice = Plain<OutdatedDevice>;
export type CompatDevices = Plain<ListOutdatedDevicesResponse>;

export const clientCompatQuery = queryOptions({
  queryKey: ["clientCompat", "rules"],
  queryFn: async ({ signal }) => plain(await clientCompat.getClientCompat({}, { signal })),
});

export const pageSizes = [25, 50, 100] as const;

/** One page of the devices: `state` UNSPECIFIED is everything but ok, `userId` "" is everybody; `page` is 1-based. */
export const outdatedDevicesQuery = (o: { state: ClientCompatState; userId: string; page: number; size: number }) =>
  queryOptions({
    queryKey: ["clientCompat", "devices", o.state, o.userId, o.page, o.size],
    queryFn: async ({ signal }) => plain(await clientCompat.listOutdatedDevices({ state: o.state, userId: o.userId, pageSize: o.size, offset: (o.page - 1) * o.size }, { signal })),
    // the old rows stay while the next page loads
    placeholderData: keepPreviousData,
  });

/** The pill of a state: the colour says how serious it is (grey-blue, not red, for "we do not know"). */
export function stateKind(state: ClientCompatState): StatusKind {
  switch (state) {
    case ClientCompatState.OK:
      return "ok";
    case ClientCompatState.BELOW_MINIMUM:
      return "warn";
    case ClientCompatState.UNSUPPORTED:
      return "bad";
    default:
      return "blip";
  }
}

export const stateWord = {
  [ClientCompatState.UNSPECIFIED]: "cc.state.attention",
  [ClientCompatState.OK]: "cc.state.ok",
  [ClientCompatState.BELOW_MINIMUM]: "cc.state.below",
  [ClientCompatState.UNSUPPORTED]: "cc.state.unsupported",
  [ClientCompatState.UNKNOWN]: "cc.state.unknown",
} as const;

/** "What is missing" for a device: the version to update to, or why nothing helps; "—" for a device that is fine. */
export function needText(t: T, d: Pick<CompatDevice, "state" | "reason" | "needVersion" | "fingerprint">): string {
  if (d.state === ClientCompatState.OK) return "—";
  switch (d.reason) {
    case "version":
      return t("cc.need.version", { v: d.needVersion });
    case "app":
      return t("cc.need.app");
    case "fingerprint":
      return t("cc.need.fingerprint", { fp: d.fingerprint });
    case "app_unknown":
      return t("cc.need.appUnknown");
    case "no_threshold":
      return t("cc.need.noThreshold");
    case "no_version":
      return t("cc.need.noVersion");
    default:
      return "—";
  }
}

const platformNames: Record<string, string> = { ios: "iOS", android: "Android", windows: "Windows", macos: "macOS", linux: "Linux" };
/** A platform id the way people write it ("macos" is macOS); an id this table does not know stays as it is. */
export const platformName = (id: string) => platformNames[id] ?? id;
export const platforms = Object.keys(platformNames);

const coreNames: Record<string, string> = { singbox: "sing-box" };
export const coreName = (id: string) => coreNames[id] ?? id;
export const cores = ["xray", "mihomo", "singbox", "own", "unknown"];

/** A row is the app on a platform ("" = any). */
export const ruleKey = (r: Pick<CompatRule, "app" | "platform">) => `${r.app}\n${r.platform}`;

/** Do two rows of the same app say the same? */
export const sameRule = (a: CompatRule, b: CompatRule) => a.core === b.core && a.unsupported === b.unsupported && a.minChrome === b.minChrome && a.minOther === b.minOther;

/** Do the two tables say the same (the order and the titles, which only come back from the server, do not matter)? */
export function rulesEqual(a: readonly CompatRule[], b: readonly CompatRule[]): boolean {
  if (a.length !== b.length) return false;
  const byKey = new Map(b.map((r) => [ruleKey(r), r]));
  return a.every((r) => {
    const o = byKey.get(ruleKey(r));
    return !!o && sameRule(r, o);
  });
}

/** The whole table as the request wants it (no titles: they only come out). */
const wire = (rules: readonly CompatRule[]) => rules.map((r) => ({ app: r.app, platform: r.platform, core: r.core, unsupported: r.unsupported, minChrome: r.minChrome, minOther: r.minOther }));

/** Saves the whole table (the owner, with a step-up); a toast says it, the tab reads the new table. */
export function useClientCompatSave() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  return useMutation({
    mutationFn: async (rules: readonly CompatRule[]) => plain(await guard(() => clientCompat.updateClientCompat({ rules: wire(rules) }))),
    onSuccess: (r) => {
      qc.setQueryData(clientCompatQuery.queryKey, r);
      void qc.invalidateQueries({ queryKey: ["clientCompat"] });
      toast(t("cc.saved"));
    },
    onError: (e) => {
      if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
    },
  });
}
