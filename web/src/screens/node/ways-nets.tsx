import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { useFmt, type Fmt } from "@/lib/format";
import { countryIn, listName, probeReason, sourceName, type CountryCheck, type Vantage } from "@/lib/health";
import { historyQuery } from "@/lib/reachability";
import { hintOf } from "@/lib/ways";
import { waysDemo } from "./ways-demo";
import type { WaysModel } from "./ways";
import "./ways.css";

// The "is the node seen from the country" block of a way (the board's `.nd-sh` and `.nd-nets`, and what opens under them, `.nd-rud`): a chip
// per network, and on "More" a row per source with what it says in words and when. The sources come from the same country check the
// pill rests on: the probe node of the TSPU source, Globalping and the other live ones with their networks, the block lists, each own probe.

export type Glyph = "ok" | "part" | "bad" | "none";
/** A source's line: the glyph, its name, what it says in words, when (empty: it does not say), and whether the line is a quiet one (proves nothing). */
export type RuRow = { key: string; glyph: Glyph; name: string; text: string; ago: string; quiet: boolean };
/** A network of the chips: "idle" is the node being silent (nothing is known), "warn" some networks do not open it, "crit" none does. */
export type Chip = { name: string; tone: "ok" | "warn" | "crit" | "idle" };

const verdictText = (t: T, v: CountryVerdict) =>
  v === CountryVerdict.OK ? t("wy.ru.opens") : v === CountryVerdict.PARTIAL ? t("cr.v.partialLong") : v === CountryVerdict.TCP_BLOCKED ? t("cr.v.tcp") : v === CountryVerdict.BLOCKED ? t("wy.nets.shut") : t("cr.v.unknown");
const verdictGlyph = (v: CountryVerdict): Glyph => (v === CountryVerdict.OK ? "ok" : v === CountryVerdict.PARTIAL || v === CountryVerdict.TCP_BLOCKED ? "part" : v === CountryVerdict.BLOCKED ? "bad" : "none");

/** The networks of the check: a vantage each (the worse state of a network wins), else the valid probes of the round. `down`: the node is silent. */
export function chipsOf(c: CountryCheck | undefined, down = false): Chip[] {
  if (!c) return [];
  const nets = new Map<string, boolean>();
  for (const v of c.vantages) {
    const n = v.network || v.key;
    nets.set(n, (nets.get(n) ?? false) || v.state === "dark");
  }
  // a block of everything (or of TCP) makes every probe dark, but only a probe that reached another node says so itself
  const blocked = hintOf(c)?.verdict === "blocked";
  if (nets.size === 0) for (const p of c.probes) if (p.valid && p.network) nets.set(p.network, (nets.get(p.network) ?? false) || p.dark || blocked);
  const dark = blocked ? "crit" : "warn";
  return [...nets].map(([name, off]) => ({ name, tone: down ? "idle" : off ? dark : "ok" }));
}

const live = ["globalping", "cheburcheck", "cheburcheck_own", "bsbord"] as const;

/** What a source with networks says: from all of them, from some of them, from none; the count of the networks it looked from. */
function fromText(t: T, vs: readonly Pick<Vantage, "state" | "network" | "key">[]): { text: string; glyph: Glyph } | null {
  const nets = new Map<string, boolean>();
  for (const v of vs) if (v.state === "reach" || v.state === "dark" || v.state === "whitelist") nets.set(v.network || v.key, (nets.get(v.network || v.key) ?? false) || v.state !== "reach");
  const total = nets.size;
  if (total === 0) return null;
  const seen = [...nets.values()].filter((dark) => !dark).length;
  if (seen === total) return { text: t.n("wy.ru.from.all", total), glyph: "ok" };
  if (seen === 0) return { text: t("wy.ru.from.none"), glyph: "bad" };
  return { text: t.n("wy.ru.from.some", seen, { total }), glyph: "part" };
}

/**
 * The lines of "More": the probe node of the TSPU source, each source with networks (Globalping, cheburcheck, bsbord), the block lists (listed,
 * or not listed while they are on: `listsOn`), each own probe. A line has the word of its source and when the source said it; the node being
 * silent (`down`) leaves the live ones without data.
 */
export function ruRows(t: T, fmt: Fmt, c: CountryCheck | undefined, opts: { listsOn?: boolean; down?: boolean } = {}): RuRow[] {
  if (!c) return [];
  const rows: RuRow[] = [];
  const ago = (u: number) => (u > 0 ? fmt.ago(u) : "");
  const nodata = (key: string, name: string, at: number): RuRow => ({ key, glyph: "none", name, text: t("cr.v.unknown"), ago: ago(at), quiet: true });
  if (c.tspuAtUnix > 0) {
    const name = `${sourceName(t, "tspu")} ${c.tspuVantageName || c.tspuVantageId}`.trim();
    rows.push(opts.down ? nodata("tspu", name, c.tspuAtUnix) : { key: "tspu", glyph: verdictGlyph(c.tspuVerdict), name, text: verdictText(t, c.tspuVerdict), ago: ago(c.tspuAtUnix), quiet: c.tspuVerdict === CountryVerdict.UNKNOWN || c.tspuVerdict === CountryVerdict.UNSPECIFIED });
  }
  for (const s of live) {
    const vs = c.vantages.filter((v) => v.source === s);
    const said = c.sourceVerdicts.find((x) => x.source === s);
    const gp = s === "globalping" && (c.sources.includes("globalping") || c.probes.length > 0 || vs.length > 0);
    if (!vs.length && !said && !gp) continue;
    const at = Math.max(said?.atUnix ?? 0, ...vs.map((v) => v.atUnix), s === "globalping" ? c.atUnix : 0);
    if (opts.down) {
      rows.push(nodata(s, sourceName(t, s), at));
      continue;
    }
    const verdict = said?.verdict ?? (s === "globalping" ? c.globalpingVerdict : CountryVerdict.UNKNOWN);
    const silent = verdict === CountryVerdict.UNKNOWN || verdict === CountryVerdict.UNSPECIFIED;
    // a round of old rows has its probes and no vantages: a probe that got nothing at all is dark (a round that decided nothing counts none)
    const own = vs.length || s !== "globalping" ? vs : silent ? [] : c.probes.filter((p) => p.valid && p.network).map((p) => ({ key: p.network, network: p.network, state: p.dark || (!p.icmpOk && p.tcp.length > 0 && p.tcp.every((x) => !x.ok)) ? "dark" : "reach" }));
    const from = fromText(t, own);
    rows.push(from ? { key: s, name: sourceName(t, s), ago: ago(at), quiet: false, ...from } : { key: s, glyph: verdictGlyph(verdict), name: sourceName(t, s), text: verdictText(t, verdict), ago: ago(at), quiet: silent });
  }
  if (c.listedBy.length > 0) rows.push({ key: "lists", glyph: "bad", name: sourceName(t, "lists"), text: t("wy.ru.listed", { lists: c.listedBy.map((l) => listName(t, l)).join(t("cr.tip.sep")) }), ago: ago(c.atUnix), quiet: false });
  else if (opts.listsOn) rows.push({ key: "lists", glyph: "ok", name: sourceName(t, "lists"), text: t("wy.ru.notListed"), ago: "", quiet: false });
  for (const v of c.vantages.filter((x) => x.source === "probes").sort((a, b) => (a.probeName || a.key).localeCompare(b.probeName || b.key, fmt.locale))) {
    const what = v.asn ? `AS${v.asn}` : v.probeName || v.network || v.key;
    rows.push({
      key: `probe:${v.key}`,
      glyph: v.state === "reach" ? "ok" : v.state === "dark" ? "bad" : "none",
      name: t("wy.ru.own", { what }),
      text: v.state === "reach" ? t("pr.ok") : probeReason(t, v),
      ago: ago(v.atUnix),
      quiet: v.state !== "reach" && v.state !== "dark",
    });
  }
  return rows;
}

/** "probe msk1" with the node in the mono face, and when. */
function Sub({ t, probe, at }: { t: T; probe: string; at: string }) {
  const line = probe ? t("wy.ru.probe", { node: probe }) : "";
  const i = probe ? line.indexOf(probe) : -1;
  return (
    <>
      {i >= 0 ? (
        <>
          {line.slice(0, i)}
          <span className="mono">{probe}</span>
          {line.slice(i + probe.length)}
        </>
      ) : (
        line
      )}
      {line && at && " · "}
      {at}
    </>
  );
}

/**
 * The seen-from-the-country block of a way. `kind`: "seen" asks it of another node ("Is msk1 seen from Russia"), "open" says it of this one
 * ("From Russia it opens on every network"), "self" is the entry's own card: the same question and its answer, no details.
 * With a check, "More" lays out the sources behind the chips; the node being silent (`w.offline`) leaves the chips without data.
 */
export function Nets({ w, name, check, kind }: { w: WaysModel; name: string; check?: CountryCheck; kind: "seen" | "open" | "self" }) {
  const t = useT();
  const fmt = useFmt();
  const fx = useEffectsEnabled();
  const [more, setMore] = useState(!!waysDemo.current?.nets);
  // the silence is this node's: another node's chips ("seen" is asked of the entry) are its own
  const down = w.offline && kind !== "seen";
  const chips = chipsOf(check, down);
  const shut = chips.filter((c) => c.tone === "warn" || c.tone === "crit").length;
  const country = countryIn(t, w.cc);
  // the lists say "not in the registry" only while they are on: the week's sources know
  const lists = useQuery({ ...historyQuery, enabled: more && !!check, select: (h) => h.sources.includes("lists") });
  const rows = more && check ? ruRows(t, fmt, check, { listsOn: lists.data, down }) : [];
  const probe = check ? check.tspuVantageName || check.tspuVantageId : "";
  const at = check && check.atUnix > 0 ? fmt.ago(check.atUnix) : "";
  const quiet = chips.length > 0 && !down;
  const title =
    kind === "open" && quiet
      ? t(shut === 0 ? "wy.ru.title.all" : shut === chips.length ? "wy.ru.title.none" : "wy.ru.title.some", { country })
      : t("ways.block.seen", { node: name, country });
  const expandable = kind !== "self" && !!check;
  return (
    <>
      <div className="wy-sh">
        <h4>{title}</h4>
        {check && (
          <span>
            {kind === "self" && quiet ? <>{t(shut === 0 ? "wy.ru.in.all" : shut === chips.length ? "wy.ru.in.none" : "wy.ru.in.some")}{at && " · "}{at}</> : <Sub t={t} probe={probe} at={at} />}
          </span>
        )}
        {expandable && (
          <button type="button" className="wy-lk wy-sa" aria-expanded={more} onClick={() => setMore((x) => !x)}>
            {more ? t("wy.nets.less") : t("wy.nets.more")}
          </button>
        )}
      </div>
      {chips.length > 0 && (
        <div className={cx("wy-nets", more && "open")}>
          {chips.map((c) => (
            <span key={c.name} className={c.tone === "ok" ? "" : c.tone}>
              <i aria-hidden className={cx("wy-dot", c.tone !== "ok" && c.tone)} />
              <b>{c.name}</b>
              <em>{c.tone === "idle" ? t("cr.v.unknown") : c.tone === "ok" ? "" : t("wy.nets.shut")}</em>
            </span>
          ))}
        </div>
      )}
      {more && (
        <div className={cx("wy-rud", fx && "fx")} data-testid="nets-rows">
          {rows.map((r) => (
            <div key={r.key} className={cx("wy-rur", r.quiet && "mut")} data-testid={`ru-${r.key}`}>
              <i aria-hidden className={cx("wy-gl", r.glyph)} />
              <b title={r.name}>{r.name}</b>
              <span>{r.text}</span>
              <em>{r.ago}</em>
            </div>
          ))}
          <Link to="/reachability" className="wy-lk">
            {t("wy.ru.go")} ›
          </Link>
        </div>
      )}
      {!check && (
        <p className="wy-hint !mt-1">
          {t("ways.noCountry")}{" "}
          {w.owner && (
            <Link to="/reachability" className="wy-lk">
              {t("ways.sources")}
            </Link>
          )}
        </p>
      )}
    </>
  );
}
