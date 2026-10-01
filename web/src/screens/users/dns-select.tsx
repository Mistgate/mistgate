import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { DnsSource } from "@/gen/mistgate/admin/v1/user_pb";
import { Icon } from "@/components/ui/icons";
import { Select } from "@/components/ui/select";
import { catalogOf, presetName, presetSubtitle, transportKey } from "@/screens/subscriptions/model";
import { dnsPresetsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import { groupsQuery } from "./rpc";
import { useTx, type Key, type Tx } from "./t";

const INHERIT = "inherit"; // Select values are strings; an empty preset id (inherit) needs a stand-in

/** What a user or group with no preset of its own gets: the preset id, and whether it comes from the group. */
export type Inherited = { id: string; fromGroup: boolean };

/**
 * What a user or group with no preset of its own gets: the group's preset for a user (when `groupId` is given and
 * the group has one), else the instance default. The id is empty until the lists arrive.
 */
export function useInheritedDns(groupId?: string): Inherited {
  const presets = useQuery(dnsPresetsQuery);
  const groups = useQuery(groupsQuery);
  const list = presets.data?.presets ?? [];
  const groupPreset = groupId ? groups.data?.find((g) => g.id === groupId)?.dnsPresetId : "";
  const fromGroup = !!groupPreset && list.some((p) => p.id === groupPreset);
  return { id: (fromGroup ? groupPreset : list.find((p) => p.isDefault)?.id) ?? "", fromGroup };
}

/** A preset's name in the UI language, for text that only has the id and the stored name (the user's "applies" line). */
export function useDnsLabel(): (id: string, name: string) => string {
  const t = useTx();
  return (id, name) => presetName({ id, name }, t.lang);
}

/** When a changed preset reaches each app: the line under every preset select (the default, a group's, a user's). */
export function DnsDelivery() {
  const t = useTx();
  const apps = useLinkAppNames();
  return (
    <span className="flex items-start gap-1.5 text-[11px] leading-snug text-muted">
      <Icon name="clock" size={12} className="mt-px block flex-none" />
      {t("subs.dns.delivery", { apps })}
    </span>
  );
}

/**
 * "Default — name" (or "Like the group — name") or one of the presets, each with what is inside under its name. `value` is the preset id set on the user or group, "" = inherit.
 * Under it, when the change reaches the apps (`note={false}` leaves that out).
 */
export function DnsSelect({ value, onChange, inherited, label, note = true }: { value: string; onChange: (id: string) => void; inherited: Inherited; label?: string; note?: boolean }) {
  const t = useTx();
  const presets = useQuery(dnsPresetsQuery);
  const list = presets.data?.presets ?? [];
  const catalog = useMemo(() => catalogOf(presets.data?.providers ?? []), [presets.data?.providers]);
  // until the presets are here the select would show a raw id, so a blank box holds the place
  if (!presets.data) return <div className="h-11 rounded-field bg-surface-2" />;
  const hint = (p: (typeof list)[number]) =>
    presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));
  const inh = list.find((p) => p.id === inherited.id);
  const options = [
    { value: INHERIT, label: t(inherited.fromGroup ? "subs.dns.inheritGroup" : "subs.dns.inherit", { name: inh ? presetName(inh, t.lang) : "…" }), hint: inh ? hint(inh) : undefined },
    ...list.map((p) => ({ value: p.id, label: presetName(p, t.lang), hint: hint(p) })),
  ];
  // a preset that was deleted under the form shows as inherit
  const shown = value && list.some((p) => p.id === value) ? value : INHERIT;
  return (
    <>
      <div className="[&>button>span:first-child]:min-w-0 [&>button>span:first-child]:truncate">
        <Select aria-label={label ?? t("subs.dns.field")} value={shown} onValueChange={(v) => onChange(v === INHERIT ? "" : v)} options={options} />
      </div>
      {note && <DnsDelivery />}
    </>
  );
}

const sourceKey: Partial<Record<DnsSource, Key>> = {
  [DnsSource.USER]: "subs.dns.src.user",
  [DnsSource.GROUP]: "subs.dns.src.group",
  [DnsSource.DEFAULT]: "subs.dns.src.default",
};

/** "Applies: Russia: .ru direct · from the group" under the user's DNS select. */
export function effectiveDnsText(t: Tx, name: string, source: DnsSource): string {
  if (!name) return "";
  const src = sourceKey[source];
  return src ? `${t("subs.dns.effective", { name })} · ${t(src)}` : t("subs.dns.effective", { name });
}
