import type { ReactElement } from "react";
import { Icon } from "@/components/ui/icons";
import { StatusDot, StatusGlyph } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryIn, countryLook, type CountryCheck, type CountryLook } from "@/lib/health";

// What probes inside one country saw of a node (GetChecksResponse.country_checks): a column of the Health matrix, a row of
// a node's card on the phone and on its page. OK stays quiet and "no data" grey; only the two blocks take a problem colour,
// and both carry a glyph as well, so the state never rests on colour alone. The tooltip names the probes, their ICMP and
// TCP answers, the time of the round and how the probes were picked.

type Kind = CountryLook["kind"] | "none";

const cellCls: Record<Kind, string> = {
  ok: "border border-line text-muted",
  warn: "tone-warn tint tone-text",
  bad: "tone-bad tint tone-text",
  unknown: "border border-dashed border-line text-muted",
  none: "border border-line text-faint",
};

const rowCls: Record<Kind, string> = {
  ok: "border border-line bg-surface-2",
  warn: "tone-warn tint",
  bad: "tone-bad tint",
  unknown: "border border-dashed border-line",
  none: "border border-dashed border-line",
};

/** The shape of the state: a quiet tick, "!" and "×" on their colour, a grey dot for no data. */
function Mark({ kind }: { kind: Kind }) {
  if (kind === "ok") return <Icon name="check" size={12} aria-hidden className="flex-none" />;
  if (kind === "warn" || kind === "bad") return <StatusGlyph kind={kind} />;
  return <StatusDot kind="off" />;
}

/** The verdict, then each probe on two lines (where, then what it got; one that did not count is greyed), the time, and how the probes were picked. */
function CountryTip({ look, children }: { look: CountryLook; children: ReactElement }) {
  const label = (
    <span className="flex flex-col gap-1">
      <span>{look.head}</span>
      {look.probes.map((p, i) => (
        <span key={i} className={cx("flex flex-col font-medium", !p.counted && "text-muted")}>
          {p.where}
          <span className="font-mono text-[11px] text-muted">{p.result}</span>
        </span>
      ))}
      <span className="font-medium">{look.at}</span>
      <span className="font-normal text-muted">{look.via}</span>
    </span>
  );
  return <Tip label={label}>{children}</Tip>;
}

/** "From Russia", the name of the column and of the rows. */
export function useCountryTitle(code: string): string {
  const t = useT();
  return t("cr.from", { country: countryIn(t, code) });
}

/** The column's head in the matrix, set apart from the profiles: it is not a client check from the panel's server. */
export function CountryHead({ code }: { code: string }) {
  const title = useCountryTitle(code);
  return (
    <span className="ml-2 flex items-end gap-1 self-end px-1 pb-1 text-[11px] leading-tight font-bold text-muted">
      <Icon name="globe" size={12} aria-hidden className="mb-px flex-none" />
      {title}
    </span>
  );
}

/** A node's cell in the matrix. A node with no round yet (new, or offline when the rounds ran) gets a dash. */
export function CountryCell({ check, node, code }: { check: CountryCheck | undefined; node: string; code: string }) {
  const t = useT();
  const fmt = useFmt();
  const title = useCountryTitle(code);
  const box = "ml-2 box-border flex h-11 flex-col items-center justify-center rounded-[11px] px-1.5 text-center text-xs leading-tight font-bold";
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
      <span role="img" tabIndex={0} aria-label={t("hl.cell.aria", { node, profile: title, state: look.head })} className={cx(box, cellCls[look.kind])}>
        <span className="flex items-center gap-1">
          <Mark kind={look.kind} />
          {look.label}
        </span>
        {look.kind === "warn" && <span className="text-[10px] font-semibold text-muted">{t("cr.icmpPasses")}</span>}
      </span>
    </CountryTip>
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
        <Mark kind={kind} />
        <b className="break-words">{title}</b>
      </span>
      <span className={cx("text-xs font-bold", kind === "warn" || kind === "bad" ? "tone-text" : "text-muted")}>{look?.label ?? t("hl.cell.none")}</span>
      <span className="min-w-0 flex-[2_1_160px] text-[11px] leading-snug text-muted">{look ? look.line : t("cr.v.notYet")}</span>
    </div>
  );
  return look ? <CountryTip look={look}>{row}</CountryTip> : row;
}
