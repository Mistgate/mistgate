import { useLocation, useNavigate, useSearch } from "@tanstack/react-router";

// The position in a long list lives in the URL (?page=3&size=50, or ?before=1234 for a log read by cursor), so a reload,
// the back button and a copied link keep it. The pure parts (page maths, which numbers the pager shows) are here too.

export const pageCount = (total: number, size: number) => Math.max(1, Math.ceil(total / Math.max(1, size)));

/** A page number kept inside 1..last, whatever the URL said (rows may have gone since the link was made). */
export const clampPage = (page: number, total: number, size: number) => Math.min(Math.max(1, Math.trunc(page) || 1), pageCount(total, size));

/** The 1-based first and last row of a page ("21–40"), 0–0 for an empty list. */
export function rangeOf(page: number, size: number, total: number): { from: number; to: number } {
  if (total <= 0) return { from: 0, to: 0 };
  return { from: (page - 1) * size + 1, to: Math.min(page * size, total) };
}

/**
 * The page numbers to show. Up to seven pages: all of them. More: always seven slots (first, last, the current page and
 * its neighbours, "…" for what is left out), so the control keeps its width while the owner pages through.
 */
export function pageItems(page: number, count: number): (number | "gap")[] {
  if (count <= 7) return Array.from({ length: count }, (_, i) => i + 1);
  if (page <= 4) return [1, 2, 3, 4, 5, "gap", count];
  if (page >= count - 3) return [1, "gap", count - 4, count - 3, count - 2, count - 1, count];
  return [1, "gap", page - 1, page, page + 1, "gap", count];
}

export type PagingSearch = { page?: number; size?: number; before?: number };

const whole = (v: unknown, min: number, max = Number.MAX_SAFE_INTEGER) => (typeof v === "number" && Number.isInteger(v) && v >= min && v <= max ? v : undefined);

/** Search-param validation of a route with a paged list: only well-formed values stay in the URL (page 1 is the bare URL). */
export function validatePaging(s: Record<string, unknown>): PagingSearch {
  const page = whole(s.page, 2, 100000);
  const size = whole(s.size, 1, 200);
  const before = whole(s.before, 1);
  return { ...(page ? { page } : {}), ...(size ? { size } : {}), ...(before ? { before } : {}) };
}

type Options = { sizes: readonly number[]; defaultSize: number };

const sizeOf = (o: Options, s: PagingSearch) => (s.size !== undefined && o.sizes.includes(s.size) ? s.size : o.defaultSize);

/** Moves the paging params of the current route, keeping everything else in the query. `undefined` removes a param. */
function useMove() {
  const navigate = useNavigate();
  return (patch: PagingSearch, opts: { replace?: boolean; state?: (prev: Record<string, unknown>) => Record<string, unknown> } = {}) =>
    void navigate({
      search: (prev: Record<string, unknown>) => {
        const next: Record<string, unknown> = { ...prev, ...patch };
        for (const k of Object.keys(next)) if (next[k] === undefined) delete next[k];
        return next;
      },
      replace: opts.replace,
      ...(opts.state ? { state: opts.state } : {}),
    } as never);
}

/** Numbered pages of a list that is fully known (client side, or counted by the server): `page` and `size` in the URL. */
export function usePaging(o: Options) {
  const s = useSearch({ strict: false }) as PagingSearch;
  const move = useMove();
  const size = sizeOf(o, s);
  return {
    page: s.page ?? 1,
    size,
    setPage: (page: number, replace = false) => move({ page: page > 1 ? page : undefined }, { replace }),
    /** A new size starts from page 1: the old page number would point somewhere else. */
    setSize: (n: number) => move({ size: n === o.defaultSize ? undefined : n, page: undefined, before: undefined }, { replace: true }),
  };
}

type Trail = { pagerTrail?: number[] };

/**
 * Cursor pages of a log (`before` = the id the page starts under, newest first). The pages left behind are kept in the
 * history entry itself (its state), so "Newer" returns to exactly the page the owner came from, also after a reload or
 * the browser's back. A page opened from a link has no trail: its way back is "To newest".
 */
export function useCursorPaging(o: Options) {
  const s = useSearch({ strict: false }) as PagingSearch;
  const trail = useLocation({ select: (l) => (l.state as Trail).pagerTrail }) ?? [];
  const move = useMove();
  const size = sizeOf(o, s);
  const before = s.before ?? 0;
  // the trail starts at the first page (0) when the owner paged here from it; then the page number is known
  const anchored = before === 0 || trail[0] === 0;
  return {
    before,
    size,
    /** 1-based number of this page, or 0 when the page was opened from a link and the count is not known. */
    index: anchored ? trail.length + 1 : 0,
    hasNewer: before > 0,
    /** "Newer" knows the page it returns to; without a trail it jumps to the newest. */
    newerToFirst: before > 0 && trail.length === 0,
    older: (next: number) => move({ before: next }, { state: (p) => ({ ...p, pagerTrail: [...trail, before] }) }),
    newer: () => {
      const back = trail.at(-1);
      move({ before: back || undefined }, { state: (p) => ({ ...p, pagerTrail: trail.slice(0, -1) }) });
    },
    /** Back to the newest page (a filter changed: the old cursor means nothing under it). */
    reset: () => move({ before: undefined }, { replace: true, state: (p) => ({ ...p, pagerTrail: [] }) }),
    setSize: (n: number) => move({ size: n === o.defaultSize ? undefined : n, page: undefined, before: undefined }, { replace: true, state: (p) => ({ ...p, pagerTrail: [] }) }),
  };
}
