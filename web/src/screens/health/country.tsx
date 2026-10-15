import { Link } from "@tanstack/react-router";
import type { ReactElement } from "react";
import { Icon } from "@/components/ui/icons";
import { StatusDot, StatusGlyph } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryIn, countryLook, type CountryCheck, type CountryLook } from "@/lib/health";

// What one country makes of a node (GetChecksResponse.country_checks): what Globalping's probes inside it saw, what the TSPU
// probe (one of the owner's nodes there) saw and what the block lists say of its address, as a column of the Health matrix, a
// row of a node's card on the phone and on its page. OK stays quiet and "no data" grey; the two blocks take a problem colour,
// and a node in a block list that nobody checked live the warn colour drawn dashed: on record, not seen. Each carries a glyph
// as well, so the state never rests on colour alone. Two sources that disagree say so under the verdict. The tooltip names the
// probes with their ICMP and TCP answers, the probe node with its ports, the times, the lists, and where the verdict comes from.

type Kind = CountryLook["kind"] | "none";

const cellCls: Record<Kind, string> = {
  ok: "border border-line text-muted",
  warn: "tone-warn tint tone-text",
  bad: "tone-bad tint tone-text",
  listed: "tone-warn tint tint-dashed tone-text",
  unknown: "border border-dashed border-line text-muted",
  none: "border border-line text-faint",
};

const rowCls: Record<Kind, string> = {
  ok: "border border-line bg-surface-2",
  warn: "tone-warn tint",
  bad: "tone-bad tint",
  listed: "tone-warn tint tint-dashed",
  unknown: "border border-dashed border-line",
  none: "border border-dashed border-line",
};

/** "In a block list": three lines of a list in the round warn glyph, apart from the "!" of TCP blocked. */
function ListedGlyph() {
  return (
    <StatusGlyph
      kind="warn"
      glyph={
        <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden className="block">
          <path d="M1 1.5h6M1 4h6M1 6.5h6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      }
    />
  );
}

/** The shape of the state: a quiet tick, "!" and "×" on their colour, the list mark, a grey dot for no data. */
export function CountryMark({ kind }: { kind: Kind }) {
  if (kind === "ok") return <Icon name="check" size={12} aria-hidden className="flex-none" />;
  if (kind === "warn" || kind === "bad") return <StatusGlyph kind={kind} />;
  if (kind === "listed") return <ListedGlyph />;
  return <StatusDot kind="off" />;
}

/**
 * The verdict; two sources that disagree, and what each said; each Globalping probe on two lines (where, then what it got; one
 * that did not count is greyed) and the round's time; the probe node with its ports and the run's time; what the lists add;
 * where it comes from.
 */
function CountryTip({ look, children }: { look: CountryLook; children: ReactElement }) {
  const t = useT();
  const label = (
    <span className="flex flex-col gap-1">
      <span>{look.head}</span>
      {look.conflict && (
        <span className="flex flex-col">
          <span className="tone-warn tone-text">{t("cr.tip.conflict")}</span>
          <span className="font-medium">{look.conflict}</span>
        </span>
      )}
      {look.probes.map((p, i) => (
        <span key={i} className={cx("flex flex-col font-medium", !p.counted && "text-muted")}>
          {p.where}
          <span className="font-mono text-[11px] text-muted">{p.result}</span>
        </span>
      ))}
      {look.at && <span className="font-medium">{look.at}</span>}
      {look.gpNone && <span className="font-medium text-muted">{look.gpNone}</span>}
      {look.tspu && (
        <span className="flex flex-col font-medium">
          {look.tspu.where}
          {look.tspu.ports && <span className="font-mono text-[11px] text-muted">{look.tspu.ports}</span>}
        </span>
      )}
      {look.tspu && <span className="font-medium">{look.tspu.at}</span>}
      {look.lists.map((l) => (
        <span key={l} className="font-medium">
          {l}
        </span>
      ))}
      {look.via && <span className="font-normal text-muted">{look.via}</span>}
    </span>
  );
  return <Tip label={label}>{children}</Tip>;
}

/** "From Russia", the name of the column and of the rows. */
export function useCountryTitle(code: string): string {
  const t = useT();
  return t("cr.from", { country: countryIn(t, code) });
}

/** The column's head in the matrix, set apart from the profiles: it is not a client check from the panel's server. It leads to the Reachability page. */
export function CountryHead({ code }: { code: string }) {
  const t = useT();
  const title = useCountryTitle(code);
  return (
    <Link
      to="/reachability"
      title={t("rc.open")}
      className="ml-2 flex items-end gap-1 self-end px-1 pb-1 text-[11px] leading-tight font-bold text-muted underline-offset-2 transition-colors hover:text-fg hover:underline"
    >
      <Icon name="globe" size={12} aria-hidden className="mb-px flex-none" />
      {title}
    </Link>
  );
}

/**
 * A node's cell in the matrix. A node with no round yet (new, or offline when the rounds ran) gets a dash. A live verdict
 * whose address is in a block list as well keeps its own look and gets the list mark in the corner.
 */
export function CountryCell({ check, node, code }: { check: CountryCheck | undefined; node: string; code: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = useCountryTitle(code);
  const box = "relative ml-2 box-border flex h-11 flex-col items-center justify-center rounded-[11px] px-1.5 text-center text-xs leading-tight font-bold";
  if (!check)
    return (
      <span className={cx(box, cellCls.none)} title={t("cr.v.notYet")}>
        <span aria-hidden>{t("hl.cell.none")}</span>
        <span className="sr-only">{t("hl.cell.aria", { node, profile: title, state: t("cr.v.notYet") })}</span>
      </span>
    );
  const look = countryLook(t, fmt, check);
  return (
    <CountryTip look={look}>
      <span role="img" tabIndex={0} aria-label={t("hl.cell.aria", { node, profile: title, state: look.state })} className={cx(box, cellCls[look.kind])}>
        <span className="flex items-center gap-1">
          <CountryMark kind={look.kind} />
          {look.label}
        </span>
        {look.note && <span className="text-[10px] font-semibold text-muted">{look.note}</span>}
        {look.alsoListed && (
          <span data-testid="also-listed" className="absolute top-1 right-1 flex">
            <ListedGlyph />
          </span>
        )}
      </span>
    </CountryTip>
  );
}

/**
 * The verdict as a pill with the cell's second line under it (the Reachability page's table): the same colours, glyphs and
 * tooltip as the cell. A node with no row yet gets a dash.
 */
export function CountryPill({ check, node, code }: { check: CountryCheck | undefined; node: string; code: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = useCountryTitle(code);
  const pill = "inline-flex h-[26px] w-fit max-w-full items-center gap-1.5 rounded-[13px] px-2.5 text-xs leading-none font-bold whitespace-nowrap";
  if (!check)
    return (
      <span className={cx(pill, cellCls.none)} title={t("cr.v.notYet")}>
        <span aria-hidden>{t("hl.cell.none")}</span>
        <span className="sr-only">{t("hl.cell.aria", { node, profile: title, state: t("cr.v.notYet") })}</span>
      </span>
    );
  const look = countryLook(t, fmt, check);
  return (
    <span className="flex min-w-0 flex-col items-start gap-1">
      <CountryTip look={look}>
        <span role="img" tabIndex={0} aria-label={t("hl.cell.aria", { node, profile: title, state: look.state })} className={cx(pill, cellCls[look.kind])}>
          <CountryMark kind={look.kind} />
          {look.label}
          {look.alsoListed && <ListedGlyph />}
        </span>
      </CountryTip>
      {look.note && <span className="text-[11px] leading-tight font-semibold text-muted">{look.note}</span>}
    </span>
  );
}

/**
 * The same verdict as a row (a node's card on the phone, the node page): its words are what a screen reader reads on focus,
 * and the quiet line says what the tooltip would, for a phone that has no hover.
 */
export function CountryRow({ check, code }: { check: CountryCheck | undefined; code: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = useCountryTitle(code);
  const look = check ? countryLook(t, fmt, check) : null;
  const kind: Kind = look?.kind ?? "none";
  const row = (
    <div
      tabIndex={look ? 0 : undefined}
      data-testid="country-row"
      className={cx("flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-field px-3 py-2.5 text-[13px]", rowCls[kind])}
    >
      <span className="flex min-w-0 flex-[1_1_140px] items-center gap-2">
        <CountryMark kind={kind} />
        {/* the title leads to the Reachability page, where the sources and the week are */}
        <Link to="/reachability" title={t("rc.open")} className="font-bold break-words underline-offset-2 hover:underline">
          {title}
        </Link>
      </span>
      <span className={cx("flex items-center gap-1.5 text-xs font-bold", kind === "warn" || kind === "bad" || kind === "listed" ? "tone-text" : "text-muted")}>
        {look?.label ?? t("hl.cell.none")}
        {look?.alsoListed && <ListedGlyph />}
      </span>
      <span className="min-w-0 flex-[2_1_160px] text-[11px] leading-snug text-muted">{look ? look.line : t("cr.v.notYet")}</span>
    </div>
  );
  return look ? <CountryTip look={look}>{row}</CountryTip> : row;
}
