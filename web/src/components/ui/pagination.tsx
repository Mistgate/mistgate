import { Button as BaseButton } from "@base-ui/react/button";
import { useEffect, useRef, type ReactNode } from "react";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { pageCount, pageItems, rangeOf } from "@/lib/paging";
import { Icon } from "./icons";
import { Select } from "./select";

// The pager at the bottom of a long list's card, one for every list: "Showing 21–40 of 137", the rows per page, and the
// pages (numbers with "…" on a wide screen, "3 of 7" between two big buttons on a phone). Numbered for a list whose size
// is known; CursorPagination for a log read newest first by cursor (no total: Newer / Older). It sits inside the card,
// under the rows, behind a hairline, so it reads as the card's own foot.

type SizeProps = {
  size: number;
  /** Rows per page to choose from; without it the size is fixed and there is no picker. */
  sizes?: readonly number[];
  onSize?: (size: number) => void;
};

const navBtn =
  "inline-flex size-8 flex-none items-center justify-center rounded-ctl border border-line bg-surface text-fg transition-colors duration-200 hover:bg-surface-2 data-disabled:pointer-events-none data-disabled:opacity-35";
// the phone's buttons are the 40px touch size and share the row
const bigBtn =
  "inline-flex h-10 min-w-0 flex-1 items-center justify-center gap-1.5 rounded-ctl border border-line bg-surface px-3 text-[13px] font-bold text-fg transition-colors duration-200 hover:bg-surface-2 data-disabled:pointer-events-none data-disabled:opacity-35";

/** The foot: scrolls the card back into view when the page changes, so the owner lands on the first row, not the pager. */
function Foot({ label, scrollKey, busy, flush, className, children }: { label: string; scrollKey: unknown; busy?: boolean; flush?: boolean; className?: string; children: ReactNode }) {
  const ref = useRef<HTMLElement>(null);
  const seen = useRef(scrollKey);
  useEffect(() => {
    if (seen.current === scrollKey) return; // the first paint, or a re-render of the same page
    seen.current = scrollKey;
    const card = ref.current?.parentElement;
    if (card && card.getBoundingClientRect().top < 0) card.scrollIntoView?.({ block: "start" });
  }, [scrollKey]);
  return (
    <nav ref={ref} aria-label={label} aria-busy={busy || undefined} className={cx("flex flex-wrap items-center gap-x-4 gap-y-2.5 py-3", !flush && "border-t border-line", className)}>
      {children}
    </nav>
  );
}

/** What is on show: announced politely, so a screen reader hears the new range after each page. */
function Summary({ children, busy }: { children: ReactNode; busy?: boolean }) {
  return (
    <p aria-live="polite" className={cx("min-w-0 flex-1 text-xs text-muted tabular-nums transition-opacity duration-200", busy && "opacity-60")}>
      {children}
    </p>
  );
}

function SizePicker({ size, sizes, onSize }: SizeProps) {
  const t = useT();
  if (!sizes || sizes.length < 2 || !onSize) return null;
  return (
    <div className="w-[150px] flex-none">
      <Select
        compact
        aria-label={t("pager.perPageLabel")}
        value={String(size)}
        onValueChange={(v) => onSize(Number(v))}
        options={sizes.map((n) => ({ value: String(n), label: t("pager.perPage", { n }) }))}
      />
    </div>
  );
}

function PageButton({ n, current, onClick }: { n: number; current: boolean; onClick: () => void }) {
  const t = useT();
  return (
    <BaseButton
      type="button"
      aria-label={t("pager.page", { n })}
      aria-current={current ? "page" : undefined}
      onClick={onClick}
      className={cx(
        "inline-flex h-8 min-w-8 items-center justify-center rounded-ctl border px-1.5 font-mono text-xs font-bold tabular-nums transition-colors duration-200",
        current ? "border-accent-line bg-accent-soft text-fg" : "border-transparent text-muted hover:bg-surface-2 hover:text-fg",
      )}
    >
      {n}
    </BaseButton>
  );
}

/**
 * Numbered pages. `page` is 1-based and already inside 1..last (clampPage). A list that fits the smallest size shows no
 * pager at all. `busy`: the next page is on its way and the old rows are still shown.
 */
export function Pagination({
  total,
  page,
  onPage,
  label,
  busy,
  flush,
  className,
  ...sizing
}: SizeProps & { total: number; page: number; onPage: (page: number) => void; label?: string; busy?: boolean; flush?: boolean; className?: string }) {
  const t = useT();
  const { size, sizes } = sizing;
  const smallest = sizes?.length ? Math.min(...sizes) : size;
  if (total <= smallest) return null;
  const count = pageCount(total, size);
  const { from, to } = rangeOf(page, size, total);
  return (
    <Foot label={label ?? t("pager.nav")} scrollKey={page} busy={busy} flush={flush} className={className}>
      <Summary busy={busy}>{t("pager.range", { from, to, total })}</Summary>
      <SizePicker {...sizing} />
      {count > 1 && (
        <>
          <ul className="hidden items-center gap-1 md:flex">
            <li>
              <BaseButton type="button" aria-label={t("pager.prevAria")} disabled={page <= 1} onClick={() => onPage(page - 1)} className={navBtn}>
                <Icon name="back" size={14} />
              </BaseButton>
            </li>
            {pageItems(page, count).map((it, i) => (
              <li key={it === "gap" ? `gap${i}` : it}>
                {it === "gap" ? (
                  <span aria-hidden className="inline-flex h-8 w-6 items-center justify-center text-xs text-faint">
                    …
                  </span>
                ) : (
                  <PageButton n={it} current={it === page} onClick={() => onPage(it)} />
                )}
              </li>
            ))}
            <li>
              <BaseButton type="button" aria-label={t("pager.nextAria")} disabled={page >= count} onClick={() => onPage(page + 1)} className={navBtn}>
                <Icon name="chevronRight" size={14} />
              </BaseButton>
            </li>
          </ul>
          <div className="flex w-full items-center gap-2 md:hidden">
            <BaseButton type="button" aria-label={t("pager.prevAria")} disabled={page <= 1} onClick={() => onPage(page - 1)} className={bigBtn}>
              <Icon name="back" size={14} />
              {t("pager.prev")}
            </BaseButton>
            <span className="min-w-[72px] text-center font-mono text-xs font-bold text-muted tabular-nums">{t("pager.pageOf", { page, count })}</span>
            <BaseButton type="button" aria-label={t("pager.nextAria")} disabled={page >= count} onClick={() => onPage(page + 1)} className={bigBtn}>
              {t("pager.next")}
              <Icon name="chevronRight" size={14} />
            </BaseButton>
          </div>
        </>
      )}
    </Foot>
  );
}

export type Cursor = {
  /** The id the page starts under (0 = the newest page): a change of it is a new page. */
  before: number;
  /** Rows on this page. */
  count: number;
  /** Is there a page after this one (older rows)? */
  hasOlder: boolean;
  /** Is this page past the first? */
  hasNewer: boolean;
  /** Newer has no trail to follow: it jumps to the newest page ("To newest"). */
  toNewest: boolean;
  /** 1-based number of this page; 0 when unknown (opened from a link). */
  index: number;
  onOlder: () => void;
  onNewer: () => void;
};

/**
 * A log read by cursor, newest first (audit, node events): Newer / Older, no total. The first page with nothing older
 * shows no pager. Say "Entries 51–100" when the page number is known, else just how many rows are on show.
 */
export function CursorPagination({ cursor: c, label, busy, flush, className, ...sizing }: SizeProps & { cursor: Cursor; label?: string; busy?: boolean; flush?: boolean; className?: string }) {
  const t = useT();
  const { size } = sizing;
  if (!c.hasNewer && !c.hasOlder) return null;
  const from = c.index > 0 ? (c.index - 1) * size + 1 : 0;
  const summary = from > 0 ? t("pager.rangeFrom", { from, to: from + c.count - 1 }) : t.n("pager.count", c.count);
  const newerLabel = c.toNewest ? t("pager.toNewest") : t("pager.newer");
  const newerAria = c.toNewest ? t("pager.toNewestAria") : t("pager.newerAria");
  return (
    <Foot label={label ?? t("pager.nav")} scrollKey={c.before} busy={busy} flush={flush} className={className}>
      <Summary busy={busy}>{summary}</Summary>
      <SizePicker {...sizing} />
      <div className="flex w-full items-center gap-2 md:w-auto">
        <BaseButton type="button" aria-label={newerAria} disabled={!c.hasNewer} onClick={c.onNewer} className={cx(bigBtn, "md:h-8 md:flex-none md:px-2.5 md:text-xs")}>
          <Icon name="back" size={14} />
          {newerLabel}
        </BaseButton>
        {c.index > 0 && <span className="min-w-[72px] text-center font-mono text-xs font-bold text-muted tabular-nums">{t("pager.page", { n: c.index })}</span>}
        <BaseButton type="button" aria-label={t("pager.olderAria")} disabled={!c.hasOlder} onClick={c.onOlder} className={cx(bigBtn, "md:h-8 md:flex-none md:px-2.5 md:text-xs")}>
          {t("pager.older")}
          <Icon name="chevronRight" size={14} />
        </BaseButton>
      </div>
    </Foot>
  );
}
