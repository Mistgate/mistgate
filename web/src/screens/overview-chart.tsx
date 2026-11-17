import { useId, useMemo, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Segmented } from "@/components/ui/segmented";
import { useT, type T } from "@/i18n";
import { scaleBytes, useFmt } from "@/lib/format";
import { foldDays, niceCeil, protocolName, tail, toSeries, type Series } from "@/lib/series";
import { CardHead } from "./overview-parts";
import type { OverviewResponse } from "./overview-types";

// The chart card of the Overview: traffic or people online, by protocol, stacked, for a day or a week, with the numbers over
// it and a reading at the pointer (or at the newest point while nothing points). Drawn by hand in SVG; the board's geometry.

export const PLOT_W = 720;
export const PLOT_H = 240;

type Tab = "traffic" | "online";
export type ChartRange = "24h" | "7d";

/** The x of point i of n across the plot. */
const xOf = (i: number, n: number) => (n < 2 ? PLOT_W / 2 : (i / (n - 1)) * PLOT_W);

/**
 * A line through the values: a smooth curve (Catmull-Rom as cubic segments) for a quantity that flows (traffic), steps for a
 * count (people online: it holds until the next reading). `top` is the value at the top of the plot.
 */
export function linePath(values: readonly number[], top: number, steps: boolean): string {
  const n = values.length;
  const y = (v: number) => PLOT_H - (Math.min(v, top) / top) * PLOT_H;
  if (n === 0) return "";
  if (n === 1) return `M0 ${y(values[0]!).toFixed(1)}H${PLOT_W}`;
  let d = `M0 ${y(values[0]!).toFixed(1)}`;
  if (steps) {
    for (let i = 1; i < n; i++) d += `H${xOf(i - 0.5, n).toFixed(1)}V${y(values[i]!).toFixed(1)}`;
    return `${d}H${PLOT_W}`;
  }
  const dx = PLOT_W / (n - 1) / 3;
  for (let i = 0; i < n - 1; i++) {
    const p0 = values[Math.max(0, i - 1)]!;
    const p1 = values[i]!;
    const p2 = values[i + 1]!;
    const p3 = values[Math.min(n - 1, i + 2)]!;
    d += `C${(xOf(i, n) + dx).toFixed(1)} ${y(p1 + (p2 - p0) / 6).toFixed(1)} ${(xOf(i + 1, n) - dx).toFixed(1)} ${y(p2 - (p3 - p1) / 6).toFixed(1)} ${xOf(i + 1, n).toFixed(1)} ${y(p2).toFixed(1)}`;
  }
  return d;
}

/** The step of the four grid lines: the first of the board's 1, 2, 4, 5, 10, 20, 25, 50, 100 that makes four of them reach `max`. */
export function gridStep(max: number): number {
  return [1, 2, 4, 5, 10, 20, 25, 50, 100].find((q) => q * 4 >= max) ?? niceCeil(max / 4);
}

// the line of each protocol: the accent for Hysteria2, sky for VLESS, lavender for AmneziaWG, three more for anything else
const lineVar = (protocol: string, k: number) =>
  protocol === "hysteria2" ? "--pl-a" : protocol === "vless" ? "--pl-b" : protocol === "amneziawg" || protocol === "awg" ? "--pl-c" : (["--pl-d", "--pl-e", "--pl-f"][k % 3] as string);
const stopColor: Record<string, string> = { "--pl-a": "var(--accent)", "--pl-b": "#8ec8ea", "--pl-c": "#b8acf2", "--pl-d": "#dcc68c", "--pl-e": "#e6a6b0", "--pl-f": "#b0cc90" };

const pad2 = (n: number) => String(n).padStart(2, "0");
const hourLabel = (unix: number) => `${pad2(new Date(unix * 1000).getHours())}:00`;

type Layer = { key: string; protocol: string; v: string; own: number[]; cum: number[] };

/** The layers stacked bottom to top (Hysteria2 first); with no traffic at all one empty layer, so the plot still has its line. */
export function stack(s: Series): Layer[] {
  const protocols = s.protocols.length ? s.protocols : ["total"];
  const own = protocols.map((p) => s.values[p] ?? s.total.map(() => 0));
  return protocols.map((p, k) => ({
    key: p,
    protocol: p,
    v: lineVar(p, k),
    own: own[k]!,
    cum: s.total.map((_, i) => own.slice(0, k + 1).reduce((a, o) => a + (o[i] ?? 0), 0)),
  }));
}

export function ChartCard({ data, range, onRange }: { data: OverviewResponse; range: ChartRange; onRange: (r: ChartRange) => void }) {
  const t = useT();
  const fmt = useFmt();
  const uid = useId().replace(/:/g, "");
  const [tab, setTab] = useState<Tab>("traffic");
  const [hover, setHover] = useState(-1);
  const traffic = tab === "traffic";
  const daily = range === "7d";

  const s = useMemo(() => {
    const hourly = toSeries(traffic ? data.traffic : data.online);
    return daily ? foldDays(hourly, traffic ? "sum" : "max") : tail(hourly, 24);
  }, [data.traffic, data.online, traffic, daily]);
  const layers = useMemo(() => stack(s), [s]);
  const n = s.times.length;
  const mx = Math.max(0, ...s.total);
  // traffic is drawn in one unit (GB, MB...) so the axis reads in round numbers
  const size = scaleBytes(mx);
  const div = traffic ? 1000 ** size.unit : 1;
  const unit = traffic ? t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[size.unit]}`) : "";
  const step = gridStep(mx / div);
  const top = step * 4;
  const hi = hover < 0 || hover >= n ? n - 1 : hover;
  const f = (v: number) => (traffic ? fmt.num(v / div, v / div >= 100 ? 0 : 1) : String(Math.round(v)));
  const suffix = traffic ? ` ${t(daily ? "ov.unitDay" : "ov.unitHour", { unit })}` : "";
  const sum = (a: readonly number[]) => a.reduce((x, y) => x + y, 0);
  const peak = Math.max(0, s.total.indexOf(mx));

  const when = (i: number) => (daily ? (i === n - 1 ? t("ov.today") : dayLabel(s.times[i]!, fmt.locale)) : i === n - 1 ? t("ov.now") : hourLabel(s.times[i]!));
  const note = traffic
    ? mx === 0
      ? t("ov.stat.noTraffic")
      : daily
        ? t("ov.chart.busiest", { when: dayLabel(s.times[peak]!, fmt.locale) })
        : t("ov.stat.peak", { time: hourLabel(s.times[peak]!) })
    : mx === 0
      ? t("ov.onlineNone")
      : t("ov.chart.mostOnline", { n: mx, when: daily ? dayLabel(s.times[peak]!, fmt.locale) : hourLabel(s.times[peak]!) });
  const total = traffic ? f(sum(s.total)) : String(s.total[n - 1] ?? 0);
  const xl = useMemo(() => xLabels(s, daily, t, fmt.locale), [s, daily, t, fmt.locale]);
  const y = (v: number) => (1 - Math.min(v, top * div) / (top * div)) * 100;
  const left = n < 2 ? 50 : (hi / (n - 1)) * 100;

  function move(e: PointerEvent<HTMLDivElement>) {
    const r = e.currentTarget.getBoundingClientRect();
    if (n > 1 && r.width > 0) setHover(Math.max(0, Math.min(n - 1, Math.round(((e.clientX - r.left) / r.width) * (n - 1)))));
  }
  function key(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      setHover(Math.max(0, Math.min(n - 1, hi + (e.key === "ArrowLeft" ? -1 : 1))));
    } else if (e.key === "Escape") setHover(-1);
  }

  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead>
        <Segmented
          aria-label={t("ov.chart.show")}
          value={tab}
          onValueChange={(v) => {
            setTab(v);
            setHover(-1);
          }}
          options={[
            { value: "traffic", label: t("ov.chart.traffic") },
            { value: "online", label: t("ov.chart.online") },
          ]}
        />
        <span className="flex-1" />
        <Segmented
          aria-label={t("ov.range")}
          value={range}
          onValueChange={(v) => {
            onRange(v);
            setHover(-1);
          }}
          options={[
            { value: "24h", label: t("ov.chart.p24") },
            { value: "7d", label: t("ov.chart.p7") },
          ]}
        />
      </CardHead>
      <div className="flex flex-wrap items-start gap-x-6 gap-y-2 px-6 pt-1 max-md:gap-x-4 max-md:px-4">
        <div>
          <div className="text-[32px] leading-9 font-medium tracking-[-0.02em] tabular-nums">
            <Scramble value={total} className="inline-block font-[inherit] tabular-nums" />
            <small className="ms-1 text-sm font-normal tracking-normal text-sec">{traffic ? unit : t("ov.chart.onlineNow")}</small>
          </div>
          <div className="mt-1 text-[13px] text-muted">{note}</div>
        </div>
        <div className="ms-auto flex flex-wrap gap-x-5 gap-y-1 pt-2.5 text-[13px] text-sec">
          {layers
            .filter((l) => l.protocol !== "total")
            .map((l) => (
              <span key={l.key} className="inline-flex items-center gap-2">
                <i aria-hidden className="h-0.5 w-3 rounded-[1px]" style={{ background: `var(${l.v})` }} />
                <span>
                  {protocolName(l.protocol)}{" "}
                  <b className="font-mono font-medium text-fg tabular-nums">
                    <Scramble value={traffic ? `${f(sum(l.own))} ${unit}` : String(l.own[n - 1] ?? 0)} className="tabular-nums" />
                  </b>
                </span>
              </span>
            ))}
        </div>
      </div>
      {/* time runs left to right in every language: the axis labels sit under the svg's own coordinates */}
      <div dir="ltr">
        <div
          role="img"
          aria-label={t("common.labeled", { label: t(traffic ? "ov.chart.traffic" : "ov.chart.online"), value: `${total} ${traffic ? unit : t("ov.chart.onlineNow")}` })}
          tabIndex={0}
          onPointerMove={move}
          onPointerDown={move}
          onPointerLeave={() => setHover(-1)}
          onBlur={() => setHover(-1)}
          onKeyDown={key}
          className="ov-plot relative mt-4 me-6 ms-14 h-60 cursor-crosshair touch-pan-y outline-none focus-visible:outline-2 focus-visible:outline-accent-mark max-md:me-4 max-md:ms-11 max-md:h-[184px]"
        >
          <svg viewBox={`0 0 ${PLOT_W} ${PLOT_H}`} preserveAspectRatio="none" aria-hidden>
            <defs>
              {layers.map((l, k) => (
                <linearGradient key={l.key} id={`${uid}-${k}`} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0" stopColor={stopColor[l.v]} stopOpacity={k === 0 ? 0.34 : 0.3} />
                  <stop offset="1" stopColor={stopColor[l.v]} stopOpacity={0.03} />
                </linearGradient>
              ))}
            </defs>
            <path className="grid" d="M0 0.5H720M0 60H720M0 120H720M0 180H720" />
            <path className="base" d="M0 239.5H720" />
            {[...layers].reverse().map((l) => {
              const k = layers.indexOf(l);
              const d = linePath(l.cum.map((v) => v / div), top, !traffic);
              return <path key={`f${l.key}`} className="area" fill={`url(#${uid}-${k})`} d={d ? `${d}L${PLOT_W} ${PLOT_H}L0 ${PLOT_H}Z` : ""} />;
            })}
            {[...layers].reverse().map((l) => (
              <path key={`l${l.key}`} className="ln" style={{ "--c": `var(${l.v})` } as CSSProperties} d={linePath(l.cum.map((v) => v / div), top, !traffic)} />
            ))}
          </svg>
          {[4, 3, 2, 1].map((q, i) => (
            <span key={q} aria-hidden className="absolute -translate-y-1/2 font-mono text-[11px] text-muted" style={{ insetInlineEnd: "calc(100% + 12px)", top: `${i * 25}%` }}>
              {fmt.num(step * q, 0)}
            </span>
          ))}
          {mx > 0 && (
            <span aria-hidden className="pointer-events-none absolute flex -translate-x-1/2 -translate-y-full flex-col items-center gap-1 pb-1.5" style={{ left: `${n < 2 ? 50 : (peak / (n - 1)) * 100}%`, top: `${y(mx)}%` }}>
              <b className="rounded-chip bg-surface-2 px-1.5 font-mono text-[11px] leading-4 font-medium whitespace-nowrap text-sec">{t("ov.chart.peak", { value: f(mx) + suffix })}</b>
              <i className="order-2 -mb-[9px] size-1.5 rounded-full" style={{ background: `var(${layers.at(-1)!.v})` }} />
            </span>
          )}
          <span aria-hidden className="pointer-events-none absolute top-0 bottom-0 w-px bg-[color-mix(in_srgb,var(--text)_16%,transparent)]" style={{ left: `${left}%` }} />
          {layers.map((l) => (
            <span key={`p${l.key}`} aria-hidden className="pointer-events-none absolute -mt-1 -ml-1 size-2 rounded-full shadow-[0_0_0_3px_var(--surface)]" style={{ left: `${left}%`, top: `${y(l.cum[hi] ?? 0)}%`, background: `var(${l.v})` }} />
          ))}
          <div
            aria-hidden
            className="pointer-events-none absolute top-2 z-[2] min-w-[184px] rounded-field border border-line bg-surface-2 px-3 py-2.5 text-xs leading-4 whitespace-nowrap"
            style={{ left: `${left}%`, transform: hi > n / 2 ? "translateX(calc(-100% - 16px))" : "translateX(16px)" }}
          >
            <b className="mb-1.5 block font-mono font-medium text-muted">{when(hi)}</b>
            <span className="mb-1 flex items-center gap-2 border-b border-line pt-0.5 pb-1.5 text-fg">
              <span>{t("ov.chart.total")}</span>
              <em className="ms-auto ps-4 font-mono font-medium not-italic tabular-nums">{f(s.total[hi] ?? 0) + suffix}</em>
            </span>
            {[...layers].reverse().map((l) => (
              <span key={l.key} className="flex items-center gap-2 py-0.5">
                <i className="h-0.5 w-2" style={{ background: `var(${l.v})` }} />
                <span className="text-sec">{l.protocol === "total" ? t("ov.chart.total") : protocolName(l.protocol)}</span>
                <em className="ms-auto ps-4 font-mono font-medium not-italic tabular-nums">{f(l.own[hi] ?? 0) + suffix}</em>
              </span>
            ))}
          </div>
        </div>
        <div aria-hidden className="mt-2 mb-5 me-6 ms-14 flex justify-between font-mono text-[11px] text-muted max-md:me-4 max-md:ms-11 max-md:mb-4">
          {xl.map((x, i) => (
            <span key={i}>{x}</span>
          ))}
        </div>
      </div>
    </section>
  );
}

function dayLabel(unix: number, locale: string) {
  return new Date(unix * 1000).toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" });
}

/** Four marks under the plot and "now": every six hours for a day, every other day for a week. */
export function xLabels(s: Series, daily: boolean, t: T, locale: string): string[] {
  const n = s.times.length;
  if (n < 2) return n === 1 ? [t(daily ? "ov.today" : "ov.now")] : [];
  const at = (i: number) => (daily ? new Date(s.times[i]! * 1000).toLocaleDateString(locale, { day: "numeric", month: "short" }) : hourLabel(s.times[i]!));
  const every = daily ? 2 : 6;
  const marks: string[] = [];
  for (let i = 0; i < n - 1 && marks.length < 4; i += every) marks.push(at(i));
  // the last mark is "now" (or "today"); one that would sit on top of it is dropped
  if (n - 1 - (marks.length - 1) * every < every / 2 + 1 && marks.length > 1) marks.pop();
  return [...marks, t(daily ? "ov.today" : "ov.now")];
}
