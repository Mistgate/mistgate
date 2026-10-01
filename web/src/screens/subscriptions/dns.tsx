import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { DnsCategory } from "@/gen/mistgate/admin/v1/dns_pb";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Icon, type IconName } from "@/components/ui/icons";
import { Select } from "@/components/ui/select";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { DnsDelivery } from "@/screens/users/dns-select";
import { useTx, type Key, type Tx } from "@/screens/users/t";
import { Pending, QueryError } from "@/components/ui/query-error";
import { customChip, PresetGlyph, Pill, ServerChip, transportChipIcon, UsersChip } from "./dns-parts";
import { PresetEditor } from "./dns-editor";
import { catalogOf, categoryKey, describePreset, dnsCategories, dnsKindKey, presetName, presetSubtitle, transportKey, variantLabel, variantNote, type Catalog, type Preset } from "./model";
import { dnsPresetsQuery, useSaveSettings } from "./queries";
import { Lead } from "./ui";

type Pick = string | "new" | null;
const SHOWN_SERVERS = 3;
const ALL = "all";

/** "DNS": the default for everyone on top, then the presets as cards filtered by category; a click opens the editor in a modal. */
export function DnsTab() {
  const t = useTx();
  const q = useQuery(dnsPresetsQuery);
  // `pick` stays while the modal plays its closing animation; `open` is what shows it
  const [pick, setPick] = useState<Pick>(null);
  const [open, setOpen] = useState(false);
  const [cat, setCat] = useState<string>(ALL);
  const catalog = useMemo(() => catalogOf(q.data?.providers ?? []), [q.data?.providers]);
  const show = (p: Pick) => {
    setPick(p);
    setOpen(true);
  };
  if (q.isPending) return <Pending />;
  if (q.isError) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  const data = q.data;
  const presets = data.presets;
  const current = pick && pick !== "new" ? presets.find((p) => p.id === pick) : undefined;
  const fallback = presets.map((p) => (p.isDefault ? presetName(p, t.lang) : "")).find(Boolean) ?? "";

  // a chip for every category that has presets, in the fixed order; "Own" only while somebody has made one
  const count = (c: DnsCategory) => presets.filter((p) => p.category === c).length;
  const chips = [...dnsCategories, DnsCategory.UNSPECIFIED].filter((c) => count(c) > 0);
  const shownCat = chips.some((c) => String(c) === cat) ? cat : ALL; // the chosen category emptied under us: back to all
  const shown = shownCat === ALL ? presets : presets.filter((p) => String(p.category) === shownCat);

  return (
    <div className="flex flex-col gap-4">
      <Lead>{t("subs.dns.lead")}</Lead>

      <DefaultCard presets={presets} catalog={catalog} t={t} />

      <section className="flex flex-col gap-3">
        <div className="flex items-center gap-3">
          <SectionLabel className="flex-1" icon="dns" tone="sky">
            {t("subs.dns.presets")} <span className="ml-1 font-mono text-faint">{presets.length}</span>
          </SectionLabel>
          <Button variant="secondary" onClick={() => show("new")}>
            <Icon name="plus" size={14} />
            {t("subs.dns.new")}
          </Button>
        </div>
        <FilterChips
          aria-label={t("subs.dns.cat.aria")}
          value={shownCat}
          onValueChange={setCat}
          options={[
            { value: ALL, label: t("subs.dns.cat.all") },
            ...chips.map((c) => ({
              value: String(c),
              label: (
                <>
                  {t(categoryKey[c])}
                  <span className="font-mono text-[11px] text-faint">{count(c)}</span>
                </>
              ),
            })),
          ]}
        />
        {presets.length === 0 && <p className="text-[13px] text-muted">{t("subs.dns.none")}</p>}
        <div className="grid gap-3 md:grid-cols-2">
          {shown.map((p) => (
            <PresetCard key={p.id} p={p} t={t} catalog={catalog} onOpen={() => show(p.id)} />
          ))}
          <button
            type="button"
            onClick={() => show("new")}
            className="card-hover flex min-h-[104px] cursor-pointer items-center justify-center gap-2 rounded-card-lg border border-dashed border-line text-[13px] font-bold text-muted hover:text-fg"
          >
            <Icon name="plus" size={15} />
            {t("subs.dns.new")}
          </button>
        </div>
      </section>

      {pick !== null && (pick === "new" || current) && (
        // remounted when the preset changes on the server (a save from another tab), so the form restarts from it
        <PresetEditor key={pick === "new" ? "new" : pick + JSON.stringify(current)} open={open} preset={current} data={data} t={t} fallback={fallback} onClose={() => setOpen(false)} />
      )}
    </div>
  );
}

const how: readonly { icon: IconName; who: Key; id: string; name: Key }[] = [
  { icon: "map", who: "subs.dns.how.ru", id: "dns_builtin_ru_split", name: "subs.dns.how.ru.name" },
  { icon: "globe", who: "subs.dns.how.abroad", id: "dns_builtin_ru_proxied", name: "subs.dns.how.abroad.name" },
  { icon: "shieldOff", who: "subs.dns.how.ads", id: "dns_builtin_adblock", name: "subs.dns.how.ads.name" },
];

/** The default preset with a select to change it, and a three-line answer to "which one do I pick?". */
function DefaultCard({ presets, catalog, t }: { presets: Preset[]; catalog: Catalog; t: Tx }) {
  const toast = useToast();
  const save = useSaveSettings();
  const def = presets.find((p) => p.isDefault);
  const nameOf = (id: string) => {
    const p = presets.find((x) => x.id === id);
    return p ? presetName(p, t.lang) : undefined;
  };

  async function change(id: string) {
    if (id === def?.id) return;
    try {
      await save.mutateAsync((cur) => ({ ...cur, defaultDnsPresetId: id }));
      toast(t("subs.dns.defaultSet", { name: nameOf(id) ?? "" }));
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }

  return (
    <Card lg className="grid gap-5 p-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)] md:gap-7">
      <div className="flex flex-col gap-2.5">
        <SectionLabel icon="sliders" tone="sand">
          {t("subs.dns.defaultT")}
        </SectionLabel>
        <Select aria-label={t("subs.dns.defaultT")} value={def?.id ?? ""} onValueChange={(v) => void change(v)} options={presets.map((p) => ({ value: p.id, label: presetName(p, t.lang), hint: subtitleOf(p, t, catalog) }))} />
        <span className="text-[11px] leading-snug text-muted">{t("subs.dns.defaultHint")}</span>
        <DnsDelivery />
      </div>
      <div className="flex flex-col gap-2.5 md:border-l md:border-line md:pl-7">
        <SectionLabel icon="info" tone="lavender">
          {t("subs.dns.howT")}
        </SectionLabel>
        <ul className="flex flex-col gap-2">
          {how.map((h) => (
            <li key={h.id} className="flex items-center gap-2.5 text-[13px]">
              <span aria-hidden className="grid size-7 flex-none place-items-center rounded-lg bg-surface-2 text-muted">
                <Icon name={h.icon} size={15} />
              </span>
              <span className="min-w-0 leading-snug">
                <span className="text-muted">{t(h.who)}</span> <span aria-hidden className="text-faint">→</span> <b className="font-bold">{nameOf(h.id) ?? t(h.name)}</b>
              </span>
            </li>
          ))}
        </ul>
      </div>
    </Card>
  );
}

/** What is inside a preset, one line: "Cloudflare · Google · DoH · .ru direct". */
export const subtitleOf = (p: Preset, t: Tx, catalog: Catalog) =>
  presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));

function PresetCard({ p, t, catalog, onOpen }: { p: Preset; t: Tx; catalog: Catalog; onOpen: () => void }) {
  const text = describePreset(p.description, t.lang);
  const name = presetName(p, t.lang);
  const shown = p.servers.slice(0, SHOWN_SERVERS);
  const more = p.servers.length - shown.length;
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cx("card-hover flex min-w-0 flex-col gap-3 rounded-card-lg border bg-surface p-4 text-left", p.isDefault ? "border-accent-line" : "border-line")}
    >
      <span className="flex items-start gap-3">
        <PresetGlyph preset={p} />
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex items-center gap-2">
            <b className="min-w-0 flex-1 text-[15px] leading-snug tracking-[-0.01em] break-words">{name}</b>
            {p.isDefault && (
              <Pill accent icon="check">
                {t("subs.dns.default")}
              </Pill>
            )}
          </span>
          <span className="truncate text-xs font-semibold">{subtitleOf(p, t, catalog)}</span>
          <span title={text} className="truncate text-xs text-muted">
            {text || t("subs.dns.noDesc")}
          </span>
        </span>
      </span>
      <span className="flex flex-wrap gap-1.5">
        {shown.map((s, i) => {
          const v = s.providerVariant ? catalog.get(s.providerVariant) : undefined;
          return v ? (
            <ServerChip key={i} icon={transportChipIcon(p.preferredTransport)} text={variantLabel(v, t.lang)} title={variantNote(v.variant, t.lang)} />
          ) : (
            <ServerChip key={i} {...customChip(s, t(dnsKindKey[s.kind]!))} />
          );
        })}
        {more > 0 && <span className="inline-flex h-6 items-center rounded-ctl px-1.5 font-mono text-[11px] text-faint">+{more}</span>}
      </span>
      <span className="mt-auto flex items-center gap-1.5">
        {p.builtin && <Pill>{t("subs.dns.builtin")}</Pill>}
        {p.splitDirect && <Pill icon="map">{t("subs.dns.directTag")}</Pill>}
        <span className="flex-1" />
        <UsersChip count={p.userCount} label={p.userCount > 0 ? t.n("subs.dns.users", p.userCount) : t("subs.dns.usersNone")} />
      </span>
    </button>
  );
}
