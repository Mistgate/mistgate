import { Link } from "@tanstack/react-router";
import { useId, useState, type ReactElement } from "react";
import { Icon } from "@/components/ui/icons";
import { StatusDot, StatusGlyph } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryIn, countryLook, type BreakdownNet, type CountryCheck, type CountryLook } from "@/lib/health";

// What one country makes of a node (GetChecksResponse.country_checks): what Globalping's probes inside it saw, what the TSPU
// probe (one of the owner's nodes there) saw and what the block lists say of its address, as a column of the Health matrix, a
// row of a node's card on the phone and on its page. OK stays quiet and "no data" grey; the blocks take a problem colour (a
// block on some networks the warn colour with a half disc, apart from the "!" of TCP blocked), and a node in a block list that
// nobody checked live the warn colour drawn dashed: on record, not seen. Each carries a glyph as well, so the state never rests
// on colour alone. Two sources that disagree say so under the verdict. The tooltip says what each source (Globalping, the TSPU
// probe, cheburcheck, bsbord) said with its reason and age, names the Globalping probes with their ICMP and TCP answers (and the
// networks a partial block cuts off), the probe node with its ports, the times, the lists, and where the verdict comes from.

type Kind = CountryLook["kind"] | "none";

const cellCls: Record<Kind, string> = {
  ok: "border border-line text-muted",
  partial: "tone-warn tint tone-text",
  warn: "tone-warn tint tone-text",
  bad: "tone-bad tint tone-text",
  listed: "tone-warn tint tint-dashed tone-text",
  unknown: "border border-dashed border-line text-muted",
  none: "border border-line text-faint",
};

const rowCls: Record<Kind, string> = {
  ok: "border border-line bg-surface-2",
  partial: "tone-warn tint",
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

/** "Blocked on some networks": a half disc in the round warn glyph, apart from the "!" of TCP blocked. */
function PartialGlyph() {
  return (
    <StatusGlyph
      kind="warn"
      glyph={
        <svg width="8" height="8" viewBox="0 0 8 8" aria-hidden className="block">
          <circle cx="4" cy="4" r="3.1" fill="none" stroke="currentColor" strokeWidth="1.3" />
          <path d="M4 .9a3.1 3.1 0 0 1 0 6.2z" fill="currentColor" />
        </svg>
      }
    />
  );
}

/** The shape of the state: a quiet tick, the half disc, "!" and "×" on their colour, the list mark, a grey dot for no data. */
export function CountryMark({ kind }: { kind: Kind }) {
  if (kind === "ok") return <Icon name="check" size={12} aria-hidden className="flex-none" />;
  if (kind === "partial") return <PartialGlyph />;
  if (kind === "warn" || kind === "bad") return <StatusGlyph kind={kind} />;
  if (kind === "listed") return <ListedGlyph />;
  return <StatusDot kind="off" />;
}

/**
 * The verdict; what each source said (in the warn colour when they disagree); each Globalping probe on two lines (where, then
 * what it got; one that did not count is greyed) and the round's time; the probe node with its ports and the run's time; what
 * the lists add; where it comes from.
 */
function CountryTip({ look, children }: { look: CountryLook; children: ReactElement }) {
  const t = useT();
  const label = (
    <span className="flex flex-col gap-1">
      <span>{look.head}</span>
      {look.conflict && (
        <span className="flex flex-col">
          <span className="tone-warn tone-text">{t("cr.tip.conflict")}</span>
          {look.said.length === 0 && <span className="font-medium">{look.conflict}</span>}
        </span>
      )}
      {look.said.length > 0 && (
        <span className="flex flex-col font-medium">
          {look.said.map((line) => (
            <span key={line}>{line}</span>
          ))}
        </span>
      )}
      {look.dark && <span className="tone-warn tone-text">{look.dark}</span>}
      {look.probes.map((p, i) => (
        <span key={i} className={cx("flex flex-col font-medium", !p.counted && "text-muted", p.dark && "tone-warn tone-text")}>
          {p.where}
          <span className="font-mono text-[11px] text-muted">{p.result}</span>
        </span>
      ))}
      {look.noProbe && <span className="font-medium text-muted">{look.noProbe}</span>}
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
        {look.note && <span className="max-w-full truncate text-[10px] font-semibold text-muted">{look.note}</span>}
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
 * tooltip as the cell. A node with no row yet gets a dash. `tag`: the address family ("IPv4", "IPv6") while IPv6 is checked too.
 */
export function CountryPill({ check, node, code, tag = "" }: { check: CountryCheck | undefined; node: string; code: string; tag?: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = [useCountryTitle(code), tag].filter(Boolean).join(" · ");
  const pill = "inline-flex h-[26px] w-fit max-w-full items-center gap-1.5 rounded-[13px] px-2.5 text-xs leading-none font-bold whitespace-nowrap";
  const tagged = tag && (
    <span aria-hidden className="font-mono text-[10px] font-bold opacity-70">
      {tag}
    </span>
  );
  if (!check)
    return (
      <span className={cx(pill, cellCls.none)} title={t("cr.v.notYet")}>
        {tagged}
        <span aria-hidden>{t("hl.cell.none")}</span>
        <span className="sr-only">{t("hl.cell.aria", { node, profile: title, state: t("cr.v.notYet") })}</span>
      </span>
    );
  const look = countryLook(t, fmt, check);
  return (
    <span className="flex min-w-0 flex-col items-start gap-1">
      <CountryTip look={look}>
        <span role="img" tabIndex={0} aria-label={t("hl.cell.aria", { node, profile: title, state: look.state })} className={cx(pill, cellCls[look.kind])}>
          {tagged}
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
 * and the quiet line says what the tooltip would, for a phone that has no hover. A block on some networks has its breakdown
 * under the row instead of the line, in the same tinted box but outside the tooltip's trigger.
 */
export function CountryRow({ check, code }: { check: CountryCheck | undefined; code: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = useCountryTitle(code);
  const look = check ? countryLook(t, fmt, check) : null;
  const kind: Kind = look?.kind ?? "none";
  const breakdown = look?.breakdown;
  const row = (
    <div
      tabIndex={look ? 0 : undefined}
      data-testid="country-row"
      className={cx("flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 rounded-field px-3 py-2.5 text-[13px]", !breakdown && rowCls[kind])}
    >
      <span className="flex min-w-0 flex-[1_1_140px] items-center gap-2">
        <CountryMark kind={kind} />
        {/* the title leads to the Reachability page, where the sources and the week are */}
        <Link to="/reachability" title={t("rc.open")} className="font-bold break-words underline-offset-2 hover:underline">
          {title}
        </Link>
      </span>
      <span className={cx("flex items-center gap-1.5 text-xs font-bold", kind === "ok" || kind === "unknown" || kind === "none" ? "text-muted" : "tone-text")}>
        {look?.label ?? t("hl.cell.none")}
        {look?.alsoListed && <ListedGlyph />}
      </span>
      {!breakdown && <span className="min-w-0 flex-[2_1_160px] text-[11px] leading-snug text-muted">{look ? look.line : t("cr.v.notYet")}</span>}
    </div>
  );
  const tipped = look ? <CountryTip look={look}>{row}</CountryTip> : row;
  if (!breakdown) return tipped;
  return (
    <div className={cx("flex min-w-0 flex-col rounded-field", rowCls[kind])}>
      {tipped}
      <PartialBreakdown breakdown={breakdown} className="mx-3 border-t border-[color-mix(in_oklch,var(--c)_35%,var(--border))] pt-2.5 pb-3" />
    </div>
  );
}

/**
 * Every source's networks under a verdict that has dark ones: those that do not see the node (✕), those that reach it (✓),
 * those under a white list (○), those without a clear answer (?), each a list under its own label with the source in every
 * line; the operators the round could not ask; what may help; each source's age. The names come from the sources and stay text.
 */
export function PartialBreakdown({ breakdown, className }: { breakdown: NonNullable<CountryLook["breakdown"]>; className?: string }) {
  const t = useT();
  return (
    <div data-testid="partial-breakdown" className={cx("flex min-w-0 flex-col gap-2.5 text-[13px]", className)}>
      <NetList label={t("cr.part.dark")} nets={breakdown.dark} mark="dark" />
      {breakdown.seen.length > 0 && <NetList label={t("cr.part.seen")} nets={breakdown.seen} mark="seen" />}
      {breakdown.white.length > 0 && <NetList label={t("cr.part.white")} nets={breakdown.white} mark="white" />}
      {breakdown.unsure.length > 0 && <NetList label={t("cr.part.unsure")} nets={breakdown.unsure} mark="unsure" />}
      {breakdown.noProbe && <p className="text-xs leading-snug text-muted">{breakdown.noProbe}</p>}
      <p className="text-xs leading-snug text-pretty">
        {t("cr.part.hint")}{" "}
        <a href={t("cr.part.guideUrl")} target="_blank" rel="noopener noreferrer" className="font-bold text-accent-text underline decoration-dotted underline-offset-2">
          {t("cr.part.guide")}
        </a>
      </p>
      <p className="text-[11px] text-muted">{breakdown.at}</p>
    </div>
  );
}

const marks = {
  dark: ["✕", "tone-bad tone-text"],
  seen: ["✓", "tone-ok tone-text"],
  white: ["○", "text-muted"],
  unsure: ["?", "text-muted"],
} as const;

/** A list longer than this shows its first lines and "and 27 more"; the button opens the rest. */
const SHORT = 6;

/** One group of the breakdown: its label, then a line per network with its mark, its name and its AS, region, kind and source muted. */
function NetList({ label, nets, mark }: { label: string; nets: readonly BreakdownNet[]; mark: keyof typeof marks }) {
  const t = useT();
  const id = useId();
  const [open, setOpen] = useState(false);
  const cut = !open && nets.length > SHORT + 1;
  const shown = cut ? nets.slice(0, SHORT) : nets;
  const [glyph, cls] = marks[mark];
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span id={id} className="text-xs font-bold">
        {label}
      </span>
      <ul id={`${id}-list`} aria-labelledby={id} className="flex flex-col gap-0.5">
        {shown.map((n, i) => (
          <li key={i} className="grid grid-cols-[14px_minmax(0,1fr)] items-baseline gap-x-1.5">
            <span aria-hidden className={cx("text-center text-xs font-extrabold", cls)}>
              {glyph}
            </span>
            <span className="min-w-0 break-words">
              <span className="font-semibold">{n.name}</span> <span className="font-mono text-[11px] text-muted">{n.tag}</span>
            </span>
          </li>
        ))}
      </ul>
      {nets.length > SHORT + 1 && (
        <button
          type="button"
          aria-expanded={open}
          aria-controls={`${id}-list`}
          onClick={() => setOpen(!open)}
          className="ml-5 w-fit rounded-sm text-xs font-semibold text-muted underline decoration-dotted underline-offset-2 hover:text-fg"
        >
          {open ? t("cr.part.fewer") : t("cr.part.more", { n: nets.length - SHORT })}
        </button>
      )}
    </div>
  );
}
