import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createContext, use, useMemo, useState } from "react";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { IconButton } from "@/components/ui/icon-button";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { DnsClient, DnsServerKind, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { dns } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import type { Key, Tx } from "@/screens/users/t";
import { ConfirmModal } from "@/screens/users/ui";
import { dnsDemo } from "./demo-dns";
import { Back, ChipText, dnsPath, useDnsRole } from "./dns-parts";
import { useLeaveGuard } from "./guard";
import {
  appResults,
  catalogOf,
  chipOf,
  cleanServers,
  cleanSplit,
  countryName,
  dnsAddrKey,
  dnsChips,
  dnsKindKey,
  dnsKinds,
  dnsTransports,
  addCategories,
  emptyServer,
  geoCategories,
  geoChoices,
  parseCategories,
  parseSuffixes,
  presetName,
  presetProblem,
  removeCategory,
  serverProblem,
  toggleGeo,
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
import { Grid, path, Row, Section, Svg, Toggle } from "./parts";
import { useSaveSettings } from "./queries";
import "./subs-dns.css";

type Draft = { name: string; description: string; servers: DnsServerN[]; split: SplitRuleN[]; ipv4Only: boolean; splitDirect: boolean; blockAds: boolean; transport: DnsTransport };

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
        blockAds: p.blockAds,
        transport: p.preferredTransport === DnsTransport.UNSPECIFIED ? DnsTransport.PLAIN : p.preferredTransport,
      }
    : { name: "", description: "", servers: [], split: [], ipv4Only: false, splitDirect: false, blockAds: false, transport: DnsTransport.PLAIN };

/** A viewer who may not change presets reads the same form: the fields hold their text, nothing adds, removes or switches. */
const Ro = createContext(false);

/**
 * The editor of one preset (or a new one) in place of the list: back, the name, then the board's two columns — the basics, the
 * servers and how they are asked, the split rules and the options on the left, what each app gets and the bar of Reset and Save on the
 * right. The bar says what stops a save; something not saved asks before the back button leaves.
 */
export function PresetEditor({ preset, data, t, fallback, onClose }: { preset?: Preset; data: DnsData; t: Tx; fallback: string; onClose: () => void }) {
  const toast = useToast();
  const qc = useQueryClient();
  const saveSettings = useSaveSettings();
  const { owner, write } = useDnsRole();
  const base = draftOf(preset);
  const [d, setD] = useState<Draft>(() => ({ ...base, ...dnsDemo.draft }));
  const [error, setError] = useState<string | null>(dnsDemo.fail ?? null);
  const [asking, setAsking] = useState(!!dnsDemo.ask);
  const [leaving, setLeaving] = useState(!!dnsDemo.leave);
  const dirty = JSON.stringify(d) !== JSON.stringify(base);
  const problem = presetProblem(d);
  const catalog = useMemo(() => catalogOf(data.providers), [data.providers]);
  useLeaveGuard(dirty && owner);

  const refresh = () => Promise.all([qc.invalidateQueries({ queryKey: ["dns"] }), qc.invalidateQueries({ queryKey: ["users"] }), qc.invalidateQueries({ queryKey: ["groups"] })]);
  const body = () => ({
    name: d.name.trim(),
    description: d.description.trim(),
    ipv4Only: d.ipv4Only,
    splitDirect: d.splitDirect,
    // an update carries the flag only when it was changed here: absent, the panel keeps what is stored
    blockAds: preset && d.blockAds === preset.blockAds ? undefined : d.blockAds,
    preferredTransport: d.transport,
    servers: cleanServers(d.servers),
    split: cleanSplit(d.split),
  });

  const save = useMutation({
    mutationFn: async () => (preset ? await dns.updateDnsPreset({ id: preset.id, ...body() }) : await dns.createDnsPreset(body())),
    onSuccess: async (r) => {
      await refresh();
      toast(t(preset ? "subs.dns.saved" : "subs.dns.created"));
      // a new preset is washed in the list it lands in
      if (!preset) markFresh("dns", r.preset?.id ?? "");
      onClose();
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const busy = save.isPending || !!dnsDemo.busy;

  async function makeDefault() {
    if (!preset) return;
    try {
      await saveSettings.mutateAsync((cur) => ({ ...cur, defaultDnsPresetId: preset.id }));
      toast(t("sx.dns.nowDef", { name: presetName(preset, t.lang) }));
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

  const back = () => (dirty && owner ? setLeaving(true) : onClose());
  const setServers = (servers: DnsServerN[]) => setD((x) => ({ ...x, servers }));
  const setRule = (i: number, patch: Partial<SplitRuleN>) => setD((x) => ({ ...x, split: x.split.map((r, j) => (j === i ? { ...r, ...patch } : r)) }));
  const canDelete = !!preset && !preset.builtin && !preset.isDefault;
  const ro = !owner;
  const usedBy = preset ? (preset.userCount > 0 ? t.n("subs.dns.deleteBody", preset.userCount, { fallback }) : t("subs.dns.deleteNobody", { fallback })) : "";
  const note = error ?? (dirty && problem ? t(problem) : dirty ? t("subs.dirty") : "");

  return (
    <Ro value={ro}>
      <Back label={t("subs.dns.presets")} onClick={back} />
      <div className="sb-edh">
        <h2>{preset ? presetName(preset, t.lang) : t("subs.dns.newName")}</h2>
        <span>{preset ? t(preset.builtin ? "sx.dns.edBuiltin" : "sx.dns.edOwn") : t("sx.dns.edNew")}</span>
      </div>
      <Grid
        main={
          <>
            <Section tone="lav" icon={path.lines} title={t("subs.dns.basics")}>
              <div className="pp-rows">
                <Row label={t("subs.dns.name")}>
                  <input id="dns-name" className="an-i" aria-label={t("subs.dns.name")} value={d.name} readOnly={ro} onChange={(e) => setD({ ...d, name: e.target.value })} maxLength={64} autoComplete="off" placeholder={t("subs.dns.namePh")} />
                </Row>
                <Row label={t("subs.dns.desc")} hint={t(preset?.builtin ? "sx.dns.descHintB" : "sx.dns.descHint")}>
                  <textarea id="dns-desc" aria-label={t("subs.dns.desc")} className="an-i sb-ta" rows={2} value={d.description} readOnly={ro} onChange={(e) => setD({ ...d, description: e.target.value })} maxLength={200} placeholder={t("subs.dns.descPh")} />
                </Row>
              </div>
            </Section>

            <Section tone="sky" icon={path.globe} title={t("subs.dns.servers")}>
              <p className="sb-lead">{t("sx.dns.serversLead")}</p>
              <ServerList servers={d.servers} t={t} providers={data.providers} catalog={catalog} onChange={setServers} start={!!dnsDemo.catalog} />
              <div className="pp-rows">
                <Row label={t("subs.dns.proto")} hint={t("sx.dns.protoHint")}>
                  {ro ? (
                    <span className="mono">{t(transportKey[d.transport])}</span>
                  ) : (
                    <Segmented
                      variant="thumb"
                      aria-label={t("subs.dns.protoAria")}
                      value={String(d.transport)}
                      onValueChange={(v) => setD((x) => ({ ...x, transport: Number(v) as DnsTransport }))}
                      options={dnsTransports.map((tr) => ({ value: String(tr), label: t(transportKey[tr]) }))}
                      className="sb-seg pp-seg"
                    />
                  )}
                </Row>
              </div>
              {d.servers.some((s) => !s.providerVariant) && <p className="sb-lead dq-sub">{t("subs.dns.protoCustom")}</p>}
            </Section>

            <Section tone="sand" icon={dnsPath.split} title={t("subs.dns.split")}>
              <p className="sb-lead">{t("subs.dns.splitHint")}</p>
              {d.split.length === 0 && <p className="sb-emp">{t("sx.dns.splitNone")}</p>}
              {d.split.map((r, i) => {
                const label = t("subs.dns.splitRule", { n: i + 1 });
                return (
                  <div key={i} className="dq-rule" role="group" aria-label={label}>
                    <div className="dq-rt">
                      <b>{label}</b>
                      {!ro && (
                        <IconButton variant="flat" className="sb-ic" aria-label={t("subs.dns.removeSplit", { n: i + 1 })} onClick={() => setD({ ...d, split: d.split.filter((_, j) => j !== i) })}>
                          <Svg d={path.x} />
                        </IconButton>
                      )}
                    </div>
                    <p className="dq-gl">{t("subs.dns.suffixes")}</p>
                    <SuffixChips label={label} suffixes={r.suffixes} t={t} onChange={(suffixes) => setRule(i, { suffixes })} />
                    <GeoToggles scope={label} geo={r.geo} t={t} onChange={(geo) => setRule(i, { geo })} />
                    <SiteCategories scope={label} geo={r.geo} t={t} onChange={(geo) => setRule(i, { geo })} />
                    <p className="dq-gl">{t("subs.dns.splitServers")}</p>
                    <ServerList scope={label} servers={r.servers} t={t} providers={data.providers} catalog={catalog} onChange={(servers) => setRule(i, { servers })} />
                  </div>
                );
              })}
              {!ro && (
                <div className="sb-apf sb-pad dq-ar">
                  <Button className="sb-btn" onClick={() => setD({ ...d, split: [...d.split, { suffixes: [], geo: [], servers: [] }] })}>
                    <Svg d={path.plus} />
                    {t("subs.dns.addSplit")}
                  </Button>
                </div>
              )}
            </Section>

            <Section tone="rose" icon={path.sliders} title={t("subs.dns.options")}>
              <div className="sb-tgs">
                <Toggle title={t("subs.dns.blockAds")} hint={t("subs.dns.blockAdsHint")} checked={d.blockAds} disabled={ro} onChange={(on) => setD({ ...d, blockAds: on })} />
                <Toggle title={t("subs.dns.direct")} hint={t("subs.dns.directHint")} checked={d.splitDirect} disabled={ro} onChange={(on) => setD({ ...d, splitDirect: on })} />
                <Toggle title={t("subs.dns.ipv4")} hint={t("subs.dns.ipv4Hint")} checked={d.ipv4Only} disabled={ro} onChange={(on) => setD({ ...d, ipv4Only: on })} />
                {preset && !preset.isDefault && write && (
                  <div className="sb-tg">
                    <div>
                      <b>{t("subs.dns.setDefaultT")}</b>
                      <span>{t("subs.dns.setDefaultHint")}</span>
                    </div>
                    <Button className="sb-btn" disabled={saveSettings.isPending} onClick={() => void makeDefault()}>
                      {t("subs.dns.setDefault")}
                    </Button>
                  </div>
                )}
              </div>
            </Section>

            {preset && !preset.builtin && owner && (
              <Section tone="rose" icon={dnsPath.trash} title={t("subs.dns.danger")}>
                <div className="sb-tgs">
                  <div className="sb-tg">
                    <div>
                      <span>{preset.isDefault ? t("subs.dns.deleteIsDefault") : usedBy}</span>
                    </div>
                    <Button variant="danger" className="sb-btn" disabled={!canDelete} onClick={() => setAsking(true)}>
                      <Svg d={dnsPath.trash} />
                      {t("subs.dns.delete")}
                    </Button>
                  </div>
                </div>
              </Section>
            )}
          </>
        }
        side={
          <>
            <Section tone="mint" icon={path.phone} title={t("subs.dns.apps")}>
              <AppsGet servers={d.servers} transport={d.transport} data={data} catalog={catalog} t={t} />
            </Section>
            <div className="sb-edf">
              {ro ? (
                <p className="dq-em ro">{t("sx.dns.roNote")}</p>
              ) : (
                <>
                  {note && (
                    <p role={error || (dirty && problem) ? "alert" : undefined} className={cx("dq-em", (error || (dirty && problem)) && "bad")}>
                      {note}
                    </p>
                  )}
                  <Button size="lg" className="sb-btn" disabled={!dirty || busy} onClick={() => { setD(base); setError(null); }}>
                    {t("sx.dns.reset")}
                  </Button>
                  <Button
                    size="lg"
                    variant="primary"
                    className="sb-btn"
                    disabled={busy || (!!preset && !dirty) || problem !== null}
                    onClick={() => {
                      setError(null);
                      save.mutate();
                    }}
                  >
                    {t(preset ? "subs.dns.save" : "subs.dns.create")}
                  </Button>
                </>
              )}
            </div>
          </>
        }
      />

      {preset && (
        <ConfirmModal
          open={asking}
          onOpenChange={setAsking}
          title={t("subs.dns.deleteT", { name: presetName(preset, t.lang) })}
          description={usedBy}
          confirmLabel={t("subs.dns.delete")}
          danger
          onConfirm={remove}
        />
      )}
      <ConfirmModal open={leaving} onOpenChange={setLeaving} title={t("subs.dns.leaveT")} description={t("subs.dns.leaveBody")} confirmLabel={t("subs.dns.leave")} danger onConfirm={async () => onClose()} />
    </Ro>
  );
}

const appName: Partial<Record<DnsClient, Key>> = {
  [DnsClient.HAPP]: "subs.dns.app.happ",
  [DnsClient.MIHOMO]: "subs.dns.app.mihomo",
  [DnsClient.XRAY]: "subs.dns.app.xray",
  [DnsClient.SINGBOX]: "subs.dns.app.singbox",
  [DnsClient.AMNEZIAWG]: "subs.dns.app.amneziawg",
};
const appWhy: Partial<Record<DnsClient, Key>> = { [DnsClient.HAPP]: "subs.dns.why.happ", [DnsClient.MIHOMO]: "subs.dns.why.mihomo", [DnsClient.XRAY]: "subs.dns.why.xray", [DnsClient.AMNEZIAWG]: "subs.dns.why.amneziawg" };

/**
 * "Happ: DoH · Amnezia: Plain, a key can only hold IP addresses": what each app ends up with for the preset as it is drafted, a
 * row per app (the board's .sb-get). sing-box takes one main resolver (internal/panel/dns/singbox.go): with more than one it says
 * the rest are dropped. With no server nothing is said but "nothing".
 */
function AppsGet({ servers, transport, data, catalog, t }: { servers: DnsServerN[]; transport: DnsTransport; data: DnsData; catalog: Catalog; t: Tx }) {
  const results = appResults(servers, transport, data.clientSupport, catalog);
  return (
    <div className="sb-get">
      {results.map((r) => {
        const got = r.transport ?? null;
        const ok = got !== null && got === transport;
        const why = servers.length === 0 ? "" : !ok ? t(appWhy[r.client] ?? "subs.dns.why.mihomo") : r.client === DnsClient.SINGBOX && r.resolvers > 1 ? t("subs.dns.why.singbox") : r.client === DnsClient.AMNEZIAWG ? t("subs.dns.why.amneziawgOk") : "";
        return (
          <div key={r.client} className={cx(got === null && "no")}>
            <span>{t(appName[r.client] ?? "subs.dns.app.happ")}</span>
            <b>{got === null ? t("subs.dns.res.none") : t(transportKey[got])}</b>
            <em>{why}</em>
          </div>
        );
      })}
    </div>
  );
}

/** The catalog as a panel: every variant as a pill, grouped by category; a variant already in the list is ticked and cannot be added twice. */
function CatalogPicker({ providers, have, t, onPick, onClose }: { providers: Provider[]; have: ReadonlySet<string>; t: Tx; onPick: (id: string) => void; onClose: () => void }) {
  return (
    <div className="dq-cp">
      <div className="dq-cp-h">
        <b>{t("subs.dns.catalogT")}</b>
        <IconButton variant="flat" className="sb-ic" aria-label={t("subs.dns.catalogClose")} onClick={onClose}>
          <Svg d={path.x} />
        </IconButton>
      </div>
      {dnsChips(providers.flatMap((provider) => provider.variants)).map((c) => {
        const items = providers.flatMap((provider) => provider.variants.filter((variant) => chipOf(variant) === c).map((variant) => ({ provider, variant })));
        return (
          <div key={c} className="dq-cg">
            <span>
              <ChipText chip={c} t={t} />
            </span>
            <div className="dq-pks">
              {items.map((e) => {
                const has = have.has(e.variant.id);
                return (
                  <button key={e.variant.id} type="button" className="dq-pk" disabled={has} title={has ? t("subs.dns.catalogHas") : variantNote(e.variant, t.lang)} onClick={() => onPick(e.variant.id)}>
                    {has && <Svg d={path.check} />}
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
 * The resolvers of a preset, or of one split rule: catalog servers as a numbered row with their note, custom ones as "kind + address"
 * (a bad address is flagged under its row). Add from the catalog or type one. The order is the order they are asked in.
 */
function ServerList({ servers, t, onChange, scope, providers, catalog, start }: { servers: DnsServerN[]; t: Tx; onChange: (servers: DnsServerN[]) => void; scope?: string; providers: Provider[]; catalog: Catalog; start?: boolean }) {
  const ro = use(Ro);
  const name = (text: string) => (scope ? `${scope} · ${text}` : text); // two lists on one screen: tell them apart
  const set = (i: number, patch: Partial<DnsServerN>) => onChange(servers.map((s, j) => (j === i ? { ...s, ...patch } : s)));
  const [picking, setPicking] = useState(!!start);
  const have = new Set(servers.map((s) => s.providerVariant).filter(Boolean));
  const remove = (label: string, i: number) =>
    !ro && (
      <IconButton variant="flat" className="sb-ic" aria-label={label} onClick={() => onChange(servers.filter((_, j) => j !== i))}>
        <Svg d={path.x} />
      </IconButton>
    );
  return (
    <>
      {servers.length === 0 && <p className="sb-emp">{t("subs.dns.noServers")}</p>}
      <div className="sb-srvs">
        {servers.map((s, i) => {
          if (s.providerVariant) {
            const v = catalog.get(s.providerVariant);
            const label = v ? variantLabel(v, t.lang) : t("subs.dns.variantGone", { id: s.providerVariant });
            return (
              <div key={i} className="sb-srv">
                <span className="sb-no mono">{i + 1}</span>
                <span className={cx("dq-sv", !v && "gone")}>
                  <b>{label}</b>
                  {v && <em>{variantNote(v.variant, t.lang)}</em>}
                </span>
                {remove(name(t("subs.dns.variantRemove", { name: label })), i)}
              </div>
            );
          }
          const bad = serverProblem(s.kind, s.address);
          return (
            <div key={i} className="sb-srv own">
              <span className="sb-no mono">{i + 1}</span>
              {ro ? (
                <span className="dq-kd">{t(dnsKindKey[s.kind]!)}</span>
              ) : (
                <Segmented
                  aria-label={name(t("subs.dns.kind", { n: i + 1 }))}
                  value={String(s.kind)}
                  onValueChange={(v) => set(i, { kind: Number(v) as DnsServerKind })}
                  options={dnsKinds.map((k) => ({ value: String(k), label: t(dnsKindKey[k]!) }))}
                  className="sb-seg sb-sg"
                />
              )}
              <input
                aria-label={name(t("subs.dns.addr", { n: i + 1 }))}
                aria-invalid={bad !== null || undefined}
                className="an-i mono dq-ad"
                value={s.address}
                readOnly={ro}
                placeholder={t(dnsAddrKey[s.kind] ?? "subs.dns.addr.plain")}
                onChange={(e) => set(i, { address: e.target.value })}
                autoComplete="off"
                spellCheck={false}
              />
              {remove(name(t("subs.dns.removeServer", { n: i + 1 })), i)}
              {bad && (
                <p role="alert" className="dq-er">
                  {t(bad)}
                </p>
              )}
            </div>
          );
        })}
      </div>
      {picking && !ro && (
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
      {!ro && (
        <div className="sb-apf sb-pad">
          <Button className="sb-btn" aria-expanded={picking} onClick={() => setPicking((x) => !x)}>
            <Svg d={path.plus} />
            {t("subs.dns.fromCatalog")}
          </Button>
          <Button className="sb-btn" onClick={() => onChange([...servers, emptyServer()])}>
            <Svg d={path.plus} />
            {t("subs.dns.addCustom")}
          </Button>
        </div>
      )}
    </>
  );
}

/** "All sites of a country: [China]": the country lists of a rule as on/off pills, and what the apps do with them. */
function GeoToggles({ geo, t, onChange, scope }: { geo: string[]; t: Tx; onChange: (geo: string[]) => void; scope: string }) {
  const ro = use(Ro);
  return (
    <>
      <p className="dq-gl">{t("subs.dns.geo")}</p>
      <div role="group" aria-label={`${scope} · ${t("subs.dns.geo")}`} className="dq-geo">
        {geoChoices(geo).map((c) => {
          const on = geo.includes(c);
          return (
            <button key={c} type="button" className="dq-pk" aria-pressed={on} disabled={ro} onClick={() => onChange(toggleGeo(geo, c))}>
              {on && <Svg d={path.check} />}
              <Flag code={c} size={12} />
              {countryName(c, t)}
            </button>
          );
        })}
      </div>
      <p className="sb-lead dq-sub">{t("subs.dns.geoHint")}</p>
    </>
  );
}

/** "Site category (geosite)": the categories of a rule as chips (kept in geo as "geosite:<name>") and a box that takes more; only the form of a name is checked. */
function SiteCategories({ geo, t, onChange, scope }: { geo: string[]; t: Tx; onChange: (geo: string[]) => void; scope: string }) {
  const ro = use(Ro);
  const [text, setText] = useState("");
  const [bad, setBad] = useState<string[]>([]);
  const names = geoCategories(geo);
  const commit = () => {
    const more = parseCategories(text);
    if (more.ok.length > 0) onChange(addCategories(geo, more.ok));
    setText(more.bad.join(" "));
    setBad(more.bad);
  };
  const label = `${scope} · ${t("subs.dns.geoSite")}`;
  return (
    <>
      <p className="dq-gl">{t("subs.dns.geoSite")}</p>
      <div className="sb-chips" role="group" aria-label={label}>
        {names.map((n) => (
          <span key={n} className="mono">
            {n}
            {!ro && (
              <button type="button" aria-label={t("subs.dns.suffixRemove", { s: n })} onClick={() => onChange(removeCategory(geo, n))}>
                <Svg d={path.x} />
              </button>
            )}
          </span>
        ))}
      </div>
      {!ro && (
        <div className="sb-pad">
          <input
            className="an-i mono sb-spl"
            aria-label={label}
            aria-invalid={bad.length > 0 || undefined}
            value={text}
            placeholder={names.length === 0 ? t("subs.dns.geoSitePh") : t("subs.dns.suffixMore")}
            onChange={(e) => {
              setText(e.target.value);
              setBad([]);
            }}
            onBlur={commit}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === ",") {
                e.preventDefault();
                commit();
              } else if (e.key === "Backspace" && text === "" && names.length > 0) onChange(removeCategory(geo, names[names.length - 1]!));
            }}
            autoComplete="off"
            spellCheck={false}
          />
          {bad.length > 0 && (
            <p role="alert" className="dq-er">
              {t("subs.dns.geoSiteBad", { s: bad.join(", ") })}
            </p>
          )}
        </div>
      )}
      <p className="sb-lead dq-sub">{t("subs.dns.geoSiteHint")}</p>
    </>
  );
}

/** Suffix chips (the board's .sb-chips) with a box that takes more on Enter, comma or when it loses focus; a pasted list is split, punycode is shown as Unicode. */
function SuffixChips({ suffixes, t, onChange, label }: { suffixes: string[]; t: Tx; onChange: (suffixes: string[]) => void; label: string }) {
  const ro = use(Ro);
  const [text, setText] = useState("");
  const commit = () => {
    const more = parseSuffixes(text).map(unicodeSuffix);
    setText("");
    if (more.length > 0) onChange([...new Set([...suffixes, ...more])]);
  };
  return (
    <>
      <div className="sb-chips">
        {suffixes.map((s) => (
          <span key={s} className="mono">
            {s}
            {!ro && (
              <button type="button" aria-label={t("subs.dns.suffixRemove", { s })} onClick={() => onChange(suffixes.filter((x) => x !== s))}>
                <Svg d={path.x} />
              </button>
            )}
          </span>
        ))}
      </div>
      {!ro && (
        <div className="sb-pad">
          <input
            className="an-i mono sb-spl"
            aria-label={`${label} · ${t("sx.dns.suffixAria")}`}
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
          />
        </div>
      )}
    </>
  );
}
