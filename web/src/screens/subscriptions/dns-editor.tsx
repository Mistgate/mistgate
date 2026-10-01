import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { DangerZone, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { dns } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import type { Key, Tx } from "@/screens/users/t";
import { ConfirmModal, SettingRow, SwitchRow } from "@/screens/users/ui";
import {
  appResults,
  catalogOf,
  categoryKey,
  dnsAddrKey,
  dnsCategories,
  dnsKindKey,
  dnsKinds,
  dnsTransports,
  emptyServer,
  parseSuffixes,
  presetName,
  presetProblem,
  serverProblem,
  transportKey,
  unicodeSuffix,
  variantLabel,
  variantNote,
  variantServer,
  type Catalog,
  type DnsData,
  type DnsServerN,
  type Preset,
  type Provider,
  type SplitRuleN,
} from "./model";
import { useSaveSettings } from "./queries";
import { FieldBlock, inputCls } from "./ui";

type Draft = { name: string; description: string; servers: DnsServerN[]; split: SplitRuleN[]; ipv4Only: boolean; splitDirect: boolean; transport: DnsTransport };

// The panel keeps split suffixes as punycode; the admin reads and types them as Unicode (".рф"), the panel converts back.
const draftOf = (p?: Preset): Draft =>
  p
    ? {
        name: p.name,
        description: p.description,
        servers: p.servers,
        split: p.split.map((r) => ({ ...r, suffixes: r.suffixes.map(unicodeSuffix) })),
        ipv4Only: p.ipv4Only,
        splitDirect: p.splitDirect,
        transport: p.preferredTransport === DnsTransport.UNSPECIFIED ? DnsTransport.PLAIN : p.preferredTransport,
      }
    : { name: "", description: "", servers: [], split: [], ipv4Only: false, splitDirect: false, transport: DnsTransport.PLAIN };

// a catalog server goes by its id alone; the panel fills in the addresses
const cleanServers = (l: DnsServerN[]) => l.map((s) => (s.providerVariant ? { providerVariant: s.providerVariant } : { kind: s.kind, address: s.address.trim() }));

/** The editor of one preset (or a new one) in a modal: basics, servers, split rules, options; the bar at the bottom stays in view. */
export function PresetEditor({ open, preset, data, t, fallback, onClose }: { open: boolean; preset?: Preset; data: DnsData; t: Tx; fallback: string; onClose: () => void }) {
  const toast = useToast();
  const qc = useQueryClient();
  const saveSettings = useSaveSettings();
  const base = draftOf(preset);
  const [d, setD] = useState<Draft>(base);
  const [error, setError] = useState<string | null>(null);
  const [asking, setAsking] = useState(false);
  const [leaving, setLeaving] = useState(false);
  const dirty = JSON.stringify(d) !== JSON.stringify(base);
  const problem = presetProblem(d);
  const catalog = useMemo(() => catalogOf(data.providers), [data.providers]);

  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ["dns"] }), qc.invalidateQueries({ queryKey: ["users"] }), qc.invalidateQueries({ queryKey: ["groups"] })]);
  const body = () => ({
    name: d.name.trim(),
    description: d.description.trim(),
    ipv4Only: d.ipv4Only,
    splitDirect: d.splitDirect,
    preferredTransport: d.transport,
    servers: cleanServers(d.servers),
    split: d.split.map((r) => ({ suffixes: r.suffixes, servers: cleanServers(r.servers) })),
  });

  const save = useMutation({
    mutationFn: async () => (preset ? await dns.updateDnsPreset({ id: preset.id, ...body() }) : await dns.createDnsPreset(body())),
    onSuccess: async () => {
      await refresh();
      toast(t(preset ? "subs.dns.saved" : "subs.dns.created"));
      onClose();
    },
    onError: (e) => setError(errorText(e, t)),
  });

  async function makeDefault() {
    if (!preset) return;
    try {
      await saveSettings.mutateAsync((cur) => ({ ...cur, defaultDnsPresetId: preset.id }));
      toast(t("subs.dns.defaultSet", { name: presetName(preset, t.lang) }));
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }

  async function remove() {
    if (!preset) return;
    try {
      await dns.deleteDnsPreset({ id: preset.id });
    } catch (e) {
      setError(errorText(e, t));
      throw e;
    }
    await refresh();
    toast(t("subs.dns.deleted"));
    onClose();
  }

  const close = (next: boolean) => {
    if (next) return;
    if (dirty) setLeaving(true);
    else onClose();
  };
  const setServers = (servers: DnsServerN[]) => setD((x) => ({ ...x, servers }));
  const setRule = (i: number, patch: Partial<SplitRuleN>) => setD((x) => ({ ...x, split: x.split.map((r, j) => (j === i ? { ...r, ...patch } : r)) }));
  const canDelete = !!preset && !preset.builtin && !preset.isDefault;

  return (
    <Modal
      open={open}
      onOpenChange={close}
      closeLabel={t("subs.dns.close")}
      title={preset ? presetName(preset, t.lang) : t("subs.dns.newName")}
      description={preset?.builtin ? t("subs.dns.builtinNote") : preset ? undefined : t("subs.dns.newNote")}
      className="md:max-w-[640px]!"
    >
      <Section title={t("subs.dns.basics")} icon="tag" tone="lavender">
        <FieldBlock label={t("subs.dns.name")} htmlFor="dns-name" className="border-t-0! py-0!">
          <input id="dns-name" className={inputCls} value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} maxLength={64} autoComplete="off" placeholder={t("subs.dns.namePh")} />
        </FieldBlock>
        <FieldBlock label={t("subs.dns.desc")} htmlFor="dns-desc" hint={t(preset?.builtin ? "subs.dns.descHintBuiltin" : "subs.dns.descHint")} className="border-t-0! py-0!">
          <textarea
            id="dns-desc"
            rows={preset?.builtin ? 2 : 1}
            className={cx(inputCls, "h-auto min-h-10 resize-y py-2.5 leading-snug")}
            value={d.description}
            onChange={(e) => setD({ ...d, description: e.target.value })}
            maxLength={200}
            placeholder={t("subs.dns.descPh")}
          />
        </FieldBlock>
      </Section>

      <Section title={t("subs.dns.servers")} icon="dns" tone="sky" hint={t("subs.dns.serversHint")}>
        <ServerList servers={d.servers} t={t} providers={data.providers} catalog={catalog} onChange={setServers} />
      </Section>

      <Section title={t("subs.dns.proto")} icon="lock" tone="sage" hint={t("subs.dns.protoHint")}>
        <Segmented
          variant="thumb"
          aria-label={t("subs.dns.protoAria")}
          value={String(d.transport)}
          onValueChange={(v) => setD((x) => ({ ...x, transport: Number(v) as DnsTransport }))}
          options={dnsTransports.map((tr) => ({ value: String(tr), label: t(transportKey[tr]) }))}
          className="w-full sm:w-[300px]"
        />
        <AppsRow servers={d.servers} transport={d.transport} data={data} catalog={catalog} t={t} />
        {d.servers.some((s) => !s.providerVariant) && <span className="text-[11px] leading-snug text-muted">{t("subs.dns.protoCustom")}</span>}
      </Section>

      <Section title={t("subs.dns.split")} icon="filter" tone="mint" hint={t("subs.dns.splitHint")}>
        {d.split.length === 0 && <p className="rounded-xl bg-surface-2 px-3 py-2.5 text-xs leading-normal text-muted">{t("subs.dns.splitNone")}</p>}
        {d.split.map((r, i) => (
          <div key={i} className="flex flex-col gap-2.5 rounded-card bg-surface-2 p-3">
            <div className="flex items-center gap-2">
              <span className="flex-1 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.dns.splitRule", { n: i + 1 })}</span>
              <IconButton variant="flat" aria-label={t("subs.dns.removeSplit", { n: i + 1 })} onClick={() => setD({ ...d, split: d.split.filter((_, j) => j !== i) })}>
                <Icon name="trash" size={14} />
              </IconButton>
            </div>
            <span className="text-xs font-bold">{t("subs.dns.suffixes")}</span>
            <SuffixChips label={t("subs.dns.splitRule", { n: i + 1 })} suffixes={r.suffixes} t={t} onChange={(suffixes) => setRule(i, { suffixes })} />
            <span className="text-xs font-bold">{t("subs.dns.splitServers")}</span>
            <ServerList scope={t("subs.dns.splitRule", { n: i + 1 })} servers={r.servers} t={t} providers={data.providers} catalog={catalog} onChange={(servers) => setRule(i, { servers })} />
          </div>
        ))}
        <button type="button" className="self-start text-xs font-bold text-accent-text" onClick={() => setD({ ...d, split: [...d.split, { suffixes: [], servers: [] }] })}>
          + {t("subs.dns.addSplit")}
        </button>
      </Section>

      <Section title={t("subs.dns.options")} icon="sliders" tone="sand">
        <div className="-my-1 flex flex-col">
          <SwitchRow label={t("subs.dns.direct")} hint={t("subs.dns.directHint")} checked={d.splitDirect} onCheckedChange={(on) => setD({ ...d, splitDirect: on })} />
          <SwitchRow label={t("subs.dns.ipv4")} hint={t("subs.dns.ipv4Hint")} checked={d.ipv4Only} onCheckedChange={(on) => setD({ ...d, ipv4Only: on })} />
          {preset && !preset.isDefault && (
            <SettingRow label={t("subs.dns.setDefaultT")} hint={t("subs.dns.setDefaultHint")}>
              <Button variant="secondary" disabled={saveSettings.isPending} onClick={() => void makeDefault()}>
                {t("subs.dns.setDefault")}
              </Button>
            </SettingRow>
          )}
        </div>
      </Section>

      {preset && !preset.builtin && (
        <DangerZone title={t("subs.dns.danger")}>
          <p className="text-xs leading-normal text-muted">
            {preset.isDefault ? t("subs.dns.deleteIsDefault") : preset.userCount > 0 ? t.n("subs.dns.deleteBody", preset.userCount, { fallback }) : t("subs.dns.deleteNobody", { fallback })}
          </p>
          <Button variant="danger" className="self-start" disabled={!canDelete} onClick={() => setAsking(true)}>
            <Icon name="trash" size={14} />
            {t("subs.dns.delete")}
          </Button>
        </DangerZone>
      )}

      {error && <Notice tone="danger">{error}</Notice>}

      <div className="sticky -bottom-5 z-[3] -mx-5 -mb-5 flex flex-wrap items-center gap-2 border-t border-line bg-surface px-5 py-3">
        <span role={dirty && problem ? "alert" : undefined} className={cx("min-w-[140px] flex-1 text-[13px] leading-snug", dirty && problem ? "text-danger-text" : "font-bold")}>
          {dirty ? (problem ? t(problem) : t("subs.dirty")) : ""}
        </span>
        <Button variant="ghost" disabled={!dirty || save.isPending} onClick={() => { setD(base); setError(null); }}>
          {t("subs.discard")}
        </Button>
        <Button
          variant="primary"
          disabled={save.isPending || (!!preset && !dirty) || problem !== null}
          onClick={() => {
            setError(null);
            save.mutate();
          }}
        >
          {preset ? t("subs.dns.save") : t("subs.dns.create")}
        </Button>
      </div>

      {preset && (
        <ConfirmModal
          open={asking}
          onOpenChange={setAsking}
          title={t("subs.dns.deleteT", { name: presetName(preset, t.lang) })}
          description={preset.userCount > 0 ? t.n("subs.dns.deleteBody", preset.userCount, { fallback }) : t("subs.dns.deleteNobody", { fallback })}
          confirmLabel={t("subs.dns.delete")}
          danger
          onConfirm={remove}
        />
      )}
      <ConfirmModal
        open={leaving}
        onOpenChange={setLeaving}
        title={t("subs.dns.leaveT")}
        description={t("subs.dns.leaveBody")}
        confirmLabel={t("subs.dns.leave")}
        danger
        onConfirm={async () => onClose()}
      />
    </Modal>
  );
}

function Section({ title, hint, icon, tone, children }: { title: string; hint?: string; icon: IconName; tone: Tone; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3 border-t border-line pt-4 first:border-t-0 first:pt-0">
      <div className="flex flex-col gap-1">
        <SectionLabel icon={icon} tone={tone}>
          {title}
        </SectionLabel>
        {hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>}
      </div>
      {children}
    </section>
  );
}

const appName: Partial<Record<DnsClient, Key>> = { [DnsClient.HAPP]: "subs.dns.app.happ", [DnsClient.MIHOMO]: "subs.dns.app.mihomo", [DnsClient.AMNEZIAWG]: "subs.dns.app.amneziawg" };
const appWhy: Partial<Record<DnsClient, Key>> = { [DnsClient.HAPP]: "subs.dns.why.happ", [DnsClient.MIHOMO]: "subs.dns.why.mihomo", [DnsClient.AMNEZIAWG]: "subs.dns.why.amneziawg" };

/** "Happ: DoH ✓ · Amnezia: Plain, a key can only hold IP addresses": what each app ends up with for the preset as it is drafted. */
function AppsRow({ servers, transport, data, catalog, t }: { servers: DnsServerN[]; transport: DnsTransport; data: DnsData; catalog: Catalog; t: Tx }) {
  const results = appResults(servers, transport, data.clientSupport, catalog);
  return (
    <div className="flex flex-col gap-1.5 rounded-xl bg-surface-2 px-3 py-2.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.dns.apps")}</span>
      <ul className="flex flex-col gap-x-4 gap-y-1 text-xs sm:flex-row sm:flex-wrap">
        {results.map((r) => {
          const got = r.transport ?? null;
          const ok = got !== null && got === transport;
          return (
            <li key={r.client} className="flex min-w-0 items-center gap-1 leading-snug">
              <b className="font-bold">{t(appName[r.client] ?? "subs.dns.app.happ")}:</b>
              <span className={cx(got === null && "text-muted")}>{got === null ? t("subs.dns.res.none") : t(transportKey[got])}</span>
              {ok ? <Icon name="check" size={12} strokeWidth={3} className="flex-none text-accent-text" /> : <span className="text-muted">— {t(appWhy[r.client] ?? "subs.dns.why.mihomo")}</span>}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

/** The catalog as a panel: every variant as a button, grouped by category; a variant already in the list is ticked and cannot be added twice. */
function CatalogPicker({ providers, have, t, onPick, onClose }: { providers: Provider[]; have: ReadonlySet<string>; t: Tx; onPick: (id: string) => void; onClose: () => void }) {
  return (
    <div className="flex flex-col gap-3 rounded-card border border-line bg-canvas p-3">
      <div className="flex items-center gap-2">
        <SectionLabel className="flex-1">{t("subs.dns.catalogT")}</SectionLabel>
        <IconButton variant="flat" aria-label={t("subs.dns.catalogClose")} onClick={onClose}>
          <Icon name="x" size={14} />
        </IconButton>
      </div>
      {dnsCategories.map((c) => {
        const items = providers.flatMap((provider) => provider.variants.filter((variant) => variant.category === c).map((variant) => ({ provider, variant })));
        if (items.length === 0) return null;
        return (
          <div key={c} className="flex flex-col gap-1.5">
            <span className="text-xs font-bold">{t(categoryKey[c])}</span>
            <div className="flex flex-wrap gap-1.5">
              {items.map((e) => {
                const has = have.has(e.variant.id);
                return (
                  <button
                    key={e.variant.id}
                    type="button"
                    disabled={has}
                    title={has ? t("subs.dns.catalogHas") : variantNote(e.variant, t.lang)}
                    onClick={() => onPick(e.variant.id)}
                    className="inline-flex h-8 cursor-pointer items-center gap-1.5 rounded-ctl border border-line bg-surface px-2.5 text-xs font-bold whitespace-nowrap transition-colors duration-200 hover:border-accent-line disabled:cursor-default disabled:opacity-50 disabled:hover:border-line"
                  >
                    {has && <Icon name="check" size={11} strokeWidth={3} />}
                    {variantLabel(e, t.lang)}
                  </button>
                );
              })}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/**
 * The resolvers of a preset, or of one split rule: catalog servers as a line with their note, custom ones as "kind +
 * address" rows (a bad address is flagged under its row). Add from the catalog or type one.
 */
function ServerList({ servers, t, onChange, scope, providers, catalog }: { servers: DnsServerN[]; t: Tx; onChange: (servers: DnsServerN[]) => void; scope?: string; providers: Provider[]; catalog: Catalog }) {
  const name = (text: string) => (scope ? `${scope} · ${text}` : text); // two lists on one screen: tell them apart
  const set = (i: number, patch: Partial<DnsServerN>) => onChange(servers.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  const [picking, setPicking] = useState(false);
  const have = new Set(servers.map((s) => s.providerVariant).filter(Boolean));
  return (
    <div className="flex flex-col gap-2.5">
      {servers.length === 0 && <p className="rounded-xl bg-surface-2 px-3 py-2.5 text-xs leading-normal text-muted">{t("subs.dns.noServers")}</p>}
      {servers.map((s, i) => {
        if (s.providerVariant) {
          const v = catalog.get(s.providerVariant);
          const label = v ? variantLabel(v, t.lang) : t("subs.dns.variantGone", { id: s.providerVariant });
          return (
            <div key={i} className="flex items-center gap-2.5 rounded-field bg-surface-2 py-1.5 pr-1.5 pl-3">
              <span className="flex min-w-0 flex-1 flex-col">
                <b className={cx("truncate text-[13px] leading-snug", !v && "text-danger-text")}>{label}</b>
                {v && <span className="truncate text-[11px] leading-snug text-muted">{variantNote(v.variant, t.lang)}</span>}
              </span>
              <IconButton variant="flat" aria-label={name(t("subs.dns.variantRemove", { name: label }))} onClick={() => onChange(servers.filter((_, j) => j !== i))}>
                <Icon name="x" size={14} />
              </IconButton>
            </div>
          );
        }
        const bad = serverProblem(s.kind, s.address);
        return (
          <div key={i} className="flex flex-col gap-1">
            <div className="flex flex-col gap-1.5 sm:flex-row sm:items-center sm:gap-2">
              <Segmented
                aria-label={name(t("subs.dns.kind", { n: i + 1 }))}
                value={String(s.kind)}
                onValueChange={(v) => set(i, { kind: Number(v) as DnsServerKind })}
                options={dnsKinds.map((k) => ({ value: String(k), label: t(dnsKindKey[k]!) }))}
                className="self-start sm:flex-none [&>*]:flex-1"
              />
              <div className="flex items-center gap-2 sm:flex-1">
                <input
                  aria-label={name(t("subs.dns.addr", { n: i + 1 }))}
                  aria-invalid={bad !== null || undefined}
                  className={cx(inputCls, "h-9 flex-1 font-mono text-xs")}
                  value={s.address}
                  placeholder={t(dnsAddrKey[s.kind] ?? "subs.dns.addr.plain")}
                  onChange={(e) => set(i, { address: e.target.value })}
                  autoComplete="off"
                  spellCheck={false}
                />
                <IconButton variant="flat" aria-label={name(t("subs.dns.removeServer", { n: i + 1 }))} onClick={() => onChange(servers.filter((_, j) => j !== i))}>
                  <Icon name="x" size={14} />
                </IconButton>
              </div>
            </div>
            {bad && (
              <span role="alert" className="text-[11px] leading-snug text-danger-text">
                {t(bad)}
              </span>
            )}
          </div>
        );
      })}
      {picking && (
        <CatalogPicker
          providers={providers}
          have={have as ReadonlySet<string>}
          t={t}
          onPick={(id) => {
            onChange([...servers, variantServer(id)]);
            setPicking(false);
          }}
          onClose={() => setPicking(false)}
        />
      )}
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        <button type="button" aria-expanded={picking} className="text-xs font-bold text-accent-text" onClick={() => setPicking((x) => !x)}>
          + {t("subs.dns.fromCatalog")}
        </button>
        <button type="button" className="text-xs font-bold text-accent-text" onClick={() => onChange([...servers, emptyServer()])}>
          + {t("subs.dns.addCustom")}
        </button>
      </div>
    </div>
  );
}

/** Suffix chips with a box that takes more on Enter, comma or when it loses focus; a pasted list is split, punycode is shown as Unicode. */
function SuffixChips({ suffixes, t, onChange, label }: { suffixes: string[]; t: Tx; onChange: (suffixes: string[]) => void; label: string }) {
  const [text, setText] = useState("");
  const commit = () => {
    const more = parseSuffixes(text).map(unicodeSuffix);
    setText("");
    if (more.length > 0) onChange([...new Set([...suffixes, ...more])]);
  };
  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-field border border-line bg-canvas p-1.5 focus-within:border-accent">
      {suffixes.map((s) => (
        <span key={s} className="inline-flex h-6 items-center gap-1 rounded-ctl bg-accent-soft pr-1 pl-2 font-mono text-[11px] font-bold text-fg">
          {s}
          <button type="button" aria-label={t("subs.dns.suffixRemove", { s })} className="flex size-4 items-center justify-center rounded text-muted hover:text-fg" onClick={() => onChange(suffixes.filter((x) => x !== s))}>
            <Icon name="x" size={10} />
          </button>
        </span>
      ))}
      <input
        aria-label={`${label} · ${t("subs.dns.suffixes")}`}
        value={text}
        placeholder={suffixes.length === 0 ? t("subs.dns.suffixPh") : t("subs.dns.suffixMore")}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === ",") {
            e.preventDefault();
            commit();
          } else if (e.key === "Backspace" && text === "" && suffixes.length > 0) onChange(suffixes.slice(0, -1));
        }}
        autoComplete="off"
        spellCheck={false}
        className="h-6 min-w-[110px] flex-1 bg-transparent px-1 font-mono text-xs text-fg outline-none placeholder:text-faint"
      />
    </div>
  );
}
