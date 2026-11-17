import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useFresh } from "@/components/fx/fresh";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Flag } from "@/components/ui/flag";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import { DnsCategory, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useTx, type Key, type Tx } from "@/screens/users/t";
import { dnsDemo } from "./demo-dns";
import { ChipText, useDnsRole } from "./dns-parts";
import { PresetEditor } from "./dns-editor";
import { catalogOf, chipOf, countryName, describePreset, dnsChips, presetName, presetSubtitle, shortAddress, splitFirst, transportKey, variantLabel, type Catalog, type Preset } from "./model";
import { path, Svg } from "./parts";
import { dnsPresetsQuery, useLinkAppList, useSaveSettings } from "./queries";
import "./subs-dns.css";

type Pick = string | "new" | null;
const ALL = "all";
/** How long a row that was just made the default keeps its flash (the board clears it after 2.6 s). */
const JUST_MS = 2600;

/**
 * "DNS": the preset everyone gets by default on top, then the list of presets (a dot to make one the default, a row to open it,
 * chips for the category), and the editor of one preset in place of the list. The default is changed with one click, with a note
 * and Undo; the editor is left with its own button, a question asked first when something in it is not saved.
 */
export function DnsTab() {
  const t = useTx();
  const q = useQuery(dnsPresetsQuery);
  const toast = useToast();
  const save = useSaveSettings();
  const { owner, write } = useDnsRole();
  const [pick, setPick] = useState<Pick>(dnsDemo.open ?? null);
  const [cat, setCat] = useState<string>(dnsDemo.cat ?? ALL);
  // the default as the screen draws it before the panel has answered (a change is shown at once, taken back if it fails)
  const [chosen, setChosen] = useState<string | null>(null);
  const [just, setJust] = useState(dnsDemo.just ?? "");
  const timer = useRef<number | undefined>(undefined);
  const top = useRef<HTMLDivElement>(null);
  const catalog = useMemo(() => catalogOf(q.data?.providers ?? []), [q.data?.providers]);
  const presets = q.data?.presets ?? [];
  const current = pick && pick !== "new" ? presets.find((p) => p.id === pick) : undefined;
  const editing = pick === "new" || !!current;
  const def = presets.find((p) => p.isDefault);
  const defId = chosen ?? def?.id ?? "";

  useEffect(() => () => window.clearTimeout(timer.current), []);
  // the list and the editor take the same place: a change of one for the other brings the top of the tab into view, and the focus
  // goes where a person who used the keyboard would look next (the back button; the row that was open, or the New button)
  const show = (p: Pick) => {
    const from = pick;
    setPick(p);
    requestAnimationFrame(() => {
      const box = top.current;
      box?.scrollIntoView?.({ block: "nearest" });
      const next = p !== null ? box?.querySelector<HTMLElement>(".nd-back") : [...(box?.querySelectorAll<HTMLElement>(".dq-r") ?? [])].find((r) => r.dataset.preset === from)?.querySelector<HTMLElement>(".dq-open") ?? box?.querySelector<HTMLElement>(".dq-h .sb-btn");
      next?.focus({ preventScroll: true });
    });
  };

  const flash = (id: string) => {
    setJust(id);
    window.clearTimeout(timer.current);
    timer.current = window.setTimeout(() => setJust(""), JUST_MS);
  };
  async function makeDefault(id: string, quiet?: boolean) {
    const prev = def?.id ?? "";
    setChosen(id);
    try {
      await save.mutateAsync((cur) => ({ ...cur, defaultDnsPresetId: id }));
    } catch (e) {
      setChosen(null);
      toast.error(errorText(e, t));
      return;
    }
    setChosen(null);
    flash(id);
    const p = presets.find((x) => x.id === id);
    toast(t("sx.dns.nowDef", { name: p ? presetName(p, t.lang) : "" }), quiet ? undefined : { undo: () => void makeDefault(prev, true) });
  }
  // a scene can start with the note of a change that was just made
  const noted = useRef(false);
  useEffect(() => {
    const name = dnsDemo.nowDefault;
    if (!name || noted.current) return;
    noted.current = true;
    toast(t("sx.dns.nowDef", { name }), { undo: () => {} });
  }, [toast, t]);

  if (q.isPending) return <Pending />;
  if (q.isError) return <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  const data = q.data;
  const fallback = def ? presetName(def, t.lang) : "";

  if (editing) {
    return (
      <div className="dq-tab" ref={top}>
        {/* remounted when the preset changes on the server (a save from another tab), so the form restarts from it */}
        <PresetEditor key={pick === "new" ? "new" : `${pick}${JSON.stringify(current)}`} preset={current} data={data} t={t} fallback={fallback} onClose={() => show(null)} />
      </div>
    );
  }

  // a chip for every category that has presets, in the fixed order (a regional one for each country); "Own" only while somebody has made one
  const count = (c: string) => presets.filter((p) => chipOf(p) === c).length;
  const chips = dnsChips(presets);
  const shownCat = chips.includes(cat) ? cat : ALL; // the chosen category emptied under us: back to all
  const shown = shownCat === ALL ? presets : presets.filter((p) => chipOf(p) === shownCat);
  const nameOf = (id: string) => {
    const p = presets.find((x) => x.id === id);
    return p ? presetName(p, t.lang) : undefined;
  };

  return (
    <div className="dq-tab" ref={top}>
      <p className="sb-intro">{t("subs.dns.lead")}</p>
      <DefaultNow preset={presets.find((p) => p.id === defId)} catalog={catalog} t={t} just={!!just && just === defId} />
      <div className="dq-main">
        <div className="dq-h">
          <h2>{t("subs.dns.presets")}</h2>
          <span className="mono">{presets.length}</span>
          <FilterChips
            className="dq-cat"
            aria-label={t("subs.dns.cat.aria")}
            value={shownCat}
            onValueChange={setCat}
            options={[
              { value: ALL, label: t("subs.dns.cat.all") },
              ...chips.map((c) => ({
                value: c,
                label: (
                  <>
                    <ChipText chip={c} t={t} />
                    <span className="mono">{count(c)}</span>
                  </>
                ),
              })),
            ]}
          />
          {owner && (
            <Button variant="primary" className="sb-btn" onClick={() => show("new")}>
              <Svg d={path.plus} />
              {t("subs.dns.new")}
            </Button>
          )}
        </div>
        <section className="card dq-list">
          <div className="dq-r dq-rh" aria-hidden="true">
            <span>{t("sx.dns.thDef")}</span>
            <span>{t("sx.dns.thPreset")}</span>
            <span>{t("subs.dns.servers")}</span>
            <span>{t("sx.dns.thUse")}</span>
            <span />
          </div>
          {presets.length === 0 && <p className="dq-emp">{t("subs.dns.none")}</p>}
          {shown.map((p) => (
            <PresetRow key={p.id} p={p} t={t} catalog={catalog} isDef={p.id === defId} just={just === p.id} canPick={write} onDefault={() => void makeDefault(p.id)} onOpen={() => show(p.id)} />
          ))}
        </section>
        <p className="dq-foot">{t("sx.dns.foot")}</p>
        <div className="dq-how">
          <span>{t("sx.dns.how")}</span>
          {how.map((h) => (
            <div key={h.id}>
              <em>{t(h.who)}</em>
              <Svg d={path.arrowR} />
              <b>{nameOf(h.id) ?? t(h.name)}</b>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}

const how: readonly { who: Key; id: string; name: Key }[] = [
  { who: "subs.dns.how.ru", id: "dns_builtin_ru_split", name: "subs.dns.how.ru.name" },
  { who: "subs.dns.how.abroad", id: "dns_builtin_ru_proxied", name: "subs.dns.how.abroad.name" },
  { who: "subs.dns.how.ads", id: "dns_builtin_adblock", name: "subs.dns.how.ads.name" },
];

/** What is inside a preset, one line: "Cloudflare · Google · DoH · .ru direct". */
export const subtitleOf = (p: Preset, t: Tx, catalog: Catalog) =>
  presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]), (c) => countryName(c, t));

/** The card on top: the preset everyone without their own gets, what it holds, who it reaches and when. */
function DefaultNow({ preset, catalog, t, just }: { preset: Preset | undefined; catalog: Catalog; t: Tx; just: boolean }) {
  const apps = useLinkAppList();
  const text = preset ? describePreset(preset.description, t.lang) : "";
  return (
    <section className={cx("card dq-now", just && "just")}>
      <div className="dq-nm">
        <span className="pp-ic mint" aria-hidden="true">
          <Svg d={path.globe} />
        </span>
        <div>
          <span>{t("subs.dns.defaultT")}</span>
          <b>{preset ? presetName(preset, t.lang) : t("sx.dns.noDef")}</b>
          <em>{preset ? [subtitleOf(preset, t, catalog), text].filter(Boolean).join(" · ") : t("sx.dns.noDefHint")}</em>
        </div>
      </div>
      <dl className="dq-facts">
        <div>
          <dt>{t("sx.dns.who")}</dt>
          <dd>{t("sx.dns.whoText")}</dd>
        </div>
        <div>
          <dt>{t("sx.dns.when")}</dt>
          <dd>{apps.length > 0 ? t("sx.dns.whenApps", { apps: apps.join(", ") }) : t("sx.dns.whenPlain")}</dd>
        </div>
      </dl>
    </section>
  );
}

/** One row of the list: the dot that makes the preset the default for everyone, and the button that opens it. */
function PresetRow({ p, t, catalog, isDef, just, canPick, onDefault, onOpen }: { p: Preset; t: Tx; catalog: Catalog; isDef: boolean; just: boolean; canPick: boolean; onDefault: () => void; onOpen: () => void }) {
  const ref = useFresh<HTMLDivElement>("dns", p.id);
  const name = presetName(p, t.lang);
  const text = describePreset(p.description, t.lang);
  const servers = p.servers.map((s) => {
    const v = s.providerVariant ? catalog.get(s.providerVariant) : undefined;
    return v ? variantLabel(v, t.lang) : s.providerVariant ? s.providerVariant : shortAddress(s.kind, s.address);
  });
  const line = servers.join(", ") || "—";
  const direct = p.splitDirect ? splitFirst(p.split, (c) => countryName(c, t)) : "";
  const modes = [t(transportKey[p.preferredTransport === DnsTransport.UNSPECIFIED ? DnsTransport.PLAIN : p.preferredTransport]), ...(direct ? [t("subs.dns.sub.direct", { s: direct })] : [])];
  return (
    <div ref={ref} data-preset={p.id} className={cx("dq-r", isDef && "def", just && "just")}>
      <button
        type="button"
        className="dq-def"
        role="radio"
        aria-checked={isDef}
        aria-label={`${isDef ? t("subs.dns.defaultT") : t("sx.dns.mkDef")}: ${name}`}
        title={isDef ? t("subs.dns.defaultT") : t("sx.dns.mkDef")}
        disabled={!canPick && !isDef}
        onClick={isDef ? undefined : onDefault}
      >
        <span className="nx-rad" aria-hidden="true" aria-checked={isDef}>
          <i />
        </span>
      </button>
      <button type="button" className="dq-open" onClick={onOpen}>
        <span className="dq-n">
          <b>
            {name}
            {p.builtin && <i>{t("subs.dns.builtin")}</i>}
            {p.blockAds && <i>{t("subs.dns.blockAdsTag")}</i>}
            {p.splitDirect && <i className="sky">{t("subs.dns.directTag")}</i>}
            {p.category === DnsCategory.REGIONAL && p.country && (
              <i className="lav">
                <Flag code={p.country} size={9} />
                {countryName(p.country, t)}
              </i>
            )}
          </b>
          <em>{text || t("subs.dns.noDesc")}</em>
        </span>
        <span className="dq-s">
          <span className="mono" title={line}>
            {line}
          </span>
          <span className="dq-m">
            {modes.map((m) => (
              <i key={m} className="mono">
                {m}
              </i>
            ))}
          </span>
        </span>
        <span className={cx("dq-u", isDef ? "all" : p.userCount === 0 && "none")} title={isDef ? t.n("subs.dns.users", p.userCount) : undefined}>
          <b className="mono">{isDef ? t("sx.dns.allWord") : p.userCount > 0 ? p.userCount : "—"}</b>
          <em>{isDef ? t("subs.dns.default") : p.userCount > 0 ? t.n("sx.dns.used", p.userCount) : t("sx.dns.noUse")}</em>
        </span>
        <Svg d={path.chevR} className="dq-chev" />
      </button>
    </div>
  );
}
