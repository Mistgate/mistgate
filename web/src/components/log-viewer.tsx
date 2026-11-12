import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Pending } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import type { DebugRecord } from "@/gen/mistgate/ops/v1/ops_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";

// The reader of cleaned log records, one page at a time, shared by the debug capture of a node and the logs a person's app sent.
// What it shows is text from other machines, so it is only ever put on the page as React text nodes: no markdown, no links made
// of what looks like one, no HTML. The font is mono and a long word wraps.

export type LogRecord = Plain<DebugRecord>;
export type LogPageData = { records: LogRecord[]; nextCursor: number; sources: string[] };

/** What the owner narrowed the records to. `fromMs` and `toMs` are unix milliseconds, 0 = open on that side. */
export type RecordFilter = { minLevel: "" | "info" | "warn" | "error"; source: string; query: string; fromMs: number; toMs: number };
export const defaultRecordFilter: RecordFilter = { minLevel: "", source: "", query: "", fromMs: 0, toMs: 0 };

const levelTone: { [level: string]: string } = { debug: "text-faint", info: "text-accent-text", warn: "text-warn", error: "text-danger" };

const pad = (n: number) => String(n).padStart(2, "0");

/** A time as a `datetime-local` input takes it: the browser's own zone, to the second. */
export function toLocalInput(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** The unix milliseconds of what a `datetime-local` input holds; 0 when it is empty. The end of a window includes its last second. */
export function fromLocalInput(value: string, end: boolean): number {
  if (!value) return 0;
  const ms = new Date(value).getTime();
  return Number.isNaN(ms) ? 0 : ms + (end ? 999 : 0);
}

/** The time of a record to the millisecond, in the viewer's zone: "15:06:21.274". */
export const stampOf = (fmt: ReturnType<typeof useFmt>, ms: number) =>
  `${new Date(ms).toLocaleTimeString(fmt.locale, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })}.${String(ms % 1000).padStart(3, "0")}`;

type Props = {
  /** The cache key of the first page; the filter is added to it. */
  queryKey: readonly unknown[];
  /** One page of records after the cursor (0 = from the first) with the filter applied. */
  load: (filter: RecordFilter, after: number) => Promise<LogPageData>;
  enabled?: boolean;
  /** The times of the first and last record (unix ms): with it the viewer has a "from" and a "to" bounded by them. */
  span?: { fromMs: number; toMs: number };
  /** Buttons at the end of the filters (a download). */
  actions?: ReactNode;
  /** `data-shot` of the block, the handle the dev kit's screenshots find it by. */
  shot?: string;
};

/** The filters, the records and "More records". A new filter starts over; "More" walks the cursor. */
export function LogViewer({ queryKey, load, enabled = true, span, actions, shot }: Props) {
  const t = useT();
  const fmt = useFmt();
  const [filter, setFilter] = useState<RecordFilter>(defaultRecordFilter);
  const [draft, setDraft] = useState("");
  // the pages after the first, for the filter they were read with: a new filter starts over without an effect
  const [paged, setPaged] = useState<{ filter: RecordFilter; extra: LogRecord[]; cursor: number | null }>({ filter, extra: [], cursor: null });
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    const id = setTimeout(() => setFilter((f) => (f.query === draft.trim() ? f : { ...f, query: draft.trim() })), 300);
    return () => clearTimeout(id);
  }, [draft]);
  const q = useQuery({ queryKey: [...queryKey, filter], queryFn: () => load(filter, 0), placeholderData: keepPreviousData, retry: false, enabled });
  const first = q.data;
  const pages = paged.filter === filter ? paged : { filter, extra: [], cursor: null }; // null cursor: the first page's own
  const next = pages.cursor ?? first?.nextCursor ?? 0;
  const records = [...(first?.records ?? []), ...pages.extra];

  async function more() {
    setLoading(true);
    try {
      const page = await load(filter, next);
      setPaged({ filter, extra: [...pages.extra, ...page.records], cursor: page.nextCursor });
    } finally {
      setLoading(false);
    }
  }

  const sources = first?.sources ?? [];
  const bounds = span && span.toMs >= span.fromMs ? { min: toLocalInput(span.fromMs), max: toLocalInput(span.toMs) } : undefined;
  return (
    <div data-shot={shot} className="flex flex-col gap-2.5">
      <FilterChips
        aria-label={t("debug.filter.level")}
        value={filter.minLevel || "all"}
        onValueChange={(v) => setFilter((f) => ({ ...f, minLevel: v === "all" ? "" : (v as RecordFilter["minLevel"]) }))}
        options={[
          { value: "all", label: t("debug.filter.all") },
          { value: "info", label: t("debug.filter.info") },
          { value: "warn", label: t("debug.filter.warn") },
          { value: "error", label: t("debug.filter.error") },
        ]}
        className="gap-1"
      />
      <div className="flex flex-wrap items-center gap-2">
        <div className="w-full sm:w-48">
          <Select
            aria-label={t("debug.filter.source")}
            value={filter.source || "any"}
            onValueChange={(v) => setFilter((f) => ({ ...f, source: v === "any" ? "" : v }))}
            options={[{ value: "any", label: t("debug.filter.anySource") }, ...sources.map((s) => ({ value: s, label: s }))]}
          />
        </div>
        <TextField
          search
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder={t("debug.filter.search")}
          aria-label={t("debug.filter.search")}
          className="min-w-40 flex-1"
        />
      </div>
      {(bounds || actions) && (
        <div className="flex flex-wrap items-end gap-x-3 gap-y-2">
          {bounds && (
            <>
              <TimeField
                label={t("clientlogs.from")}
                value={filter.fromMs ? toLocalInput(filter.fromMs) : ""}
                bounds={bounds}
                onChange={(v) => setFilter((f) => ({ ...f, fromMs: fromLocalInput(v, false) }))}
              />
              <TimeField
                label={t("clientlogs.to")}
                value={filter.toMs ? toLocalInput(filter.toMs) : ""}
                bounds={bounds}
                onChange={(v) => setFilter((f) => ({ ...f, toMs: fromLocalInput(v, true) }))}
              />
              {(filter.fromMs > 0 || filter.toMs > 0) && (
                <Button variant="ghost" size="md" onClick={() => setFilter((f) => ({ ...f, fromMs: 0, toMs: 0 }))}>
                  <span>{t("clientlogs.wholeBundle")}</span>
                </Button>
              )}
            </>
          )}
          {actions && <div className="ms-auto flex items-center gap-2">{actions}</div>}
        </div>
      )}
      <div className={cx("overflow-hidden rounded-[14px] border border-line bg-surface", q.isPlaceholderData && "opacity-60")} aria-label={t("debug.records")}>
        {records.map((r) => (
          <div key={r.seq} className="flex min-w-0 flex-col gap-0.5 border-b border-line px-3.5 py-2 last:border-b-0">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5 text-xs">
              <span className="text-faint">{stampOf(fmt, r.timeUnixMs)}</span>
              <span className={cx("font-mono font-bold uppercase", levelTone[r.level] ?? "text-muted")}>{r.level}</span>
              <span className="font-mono text-muted">{r.source}</span>
            </div>
            <p dir="auto" className="font-mono text-xs [overflow-wrap:anywhere] whitespace-pre-wrap">
              {r.message}
            </p>
            {Object.keys(r.attrs).length > 0 && (
              <p dir="auto" className="font-mono text-[11px] [overflow-wrap:anywhere] text-muted">
                {Object.entries(r.attrs)
                  .sort(([a], [b]) => a.localeCompare(b))
                  .map(([k, v]) => `${k}=${v}`)
                  .join("  ")}
              </p>
            )}
          </div>
        ))}
        {q.isPending && enabled && <Pending />}
        {first && records.length === 0 && <p className="px-3.5 py-3 text-muted">{t("debug.empty")}</p>}
      </div>
      {next > 0 && (
        <div>
          <Button variant="secondary" size="md" disabled={loading} onClick={() => void more()}>
            <span>{t("debug.more")}</span>
          </Button>
        </div>
      )}
    </div>
  );
}

/** A native date and time input of the bundle's own span: the browser draws the picker. */
function TimeField({ label, value, bounds, onChange }: { label: string; value: string; bounds: { min: string; max: string }; onChange: (v: string) => void }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{label}</span>
      <input
        type="datetime-local"
        step={1}
        min={bounds.min}
        max={bounds.max}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-[30px] rounded-ctl border border-line bg-surface px-2.5 font-mono text-xs text-fg outline-none transition-colors duration-200 focus:border-accent"
      />
    </label>
  );
}
