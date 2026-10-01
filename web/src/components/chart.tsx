import { useId, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { smoothPath } from "@/lib/series";
import { cx } from "@/lib/cx";

// Smooth-line area charts, hand-written SVG (no chart library): lines through the points, a translucent
// gradient under each, two dashed gridlines, a hover guide with a dot. Colours and fill strengths come from
// CSS variables (index.css), which is how the light theme gets its calmer fills.

export type ChartLine = { key: string; values: readonly number[]; tone: "accent" | "them"; width: number };

const W = 600;
const PAD = 4;

function Plot({
  lines,
  max,
  height,
  hover,
  onHover,
  label,
}: {
  lines: readonly ChartLine[];
  max: number;
  height: number;
  hover: number | null;
  onHover: (i: number | null) => void;
  label: string;
}) {
  const uid = useId().replace(/:/g, "");
  const n = lines[0]?.values.length ?? 0;
  const xOf = (i: number) => (n < 2 ? W / 2 : (i / (n - 1)) * W);
  const yOf = (v: number) => height - PAD - (Math.min(v, max) / max) * (height - PAD * 2);
  const xs = Array.from({ length: n }, (_, i) => xOf(i));
  const dotAt = hover ?? n - 1;
  const primary = lines[0];

  function move(e: PointerEvent<HTMLDivElement>) {
    const box = e.currentTarget.getBoundingClientRect();
    const frac = box.width > 0 ? (e.clientX - box.left) / box.width : 0;
    onHover(n < 2 ? 0 : Math.max(0, Math.min(n - 1, Math.round(frac * (n - 1)))));
  }

  function key(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "ArrowLeft" || e.key === "ArrowRight") {
      e.preventDefault();
      const step = e.key === "ArrowLeft" ? -1 : 1;
      onHover(Math.max(0, Math.min(n - 1, (hover ?? n - 1) + step)));
    } else if (e.key === "Escape") onHover(null);
  }

  return (
    <div
      role="img"
      aria-label={label}
      tabIndex={0}
      onPointerMove={move}
      onPointerDown={move}
      onPointerLeave={() => onHover(null)}
      onBlur={() => onHover(null)}
      onKeyDown={key}
      className="relative cursor-crosshair touch-pan-y outline-none focus-visible:outline-2 focus-visible:outline-accent"
      style={{ height }}
    >
      <svg
        viewBox={`0 0 ${W} ${height}`}
        preserveAspectRatio="none"
        width="100%"
        height="100%"
        className="block overflow-visible"
        aria-hidden
      >
        <defs>
          {lines.map((l, k) => (
            <linearGradient key={l.key} id={`${uid}-${k}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={tone(l.tone)} style={{ stopOpacity: `var(--chart-fill-${k === 0 ? "a" : "b"})` }} />
              <stop offset="100%" stopColor={tone(l.tone)} stopOpacity={0} />
            </linearGradient>
          ))}
        </defs>
        {[0.33, 0.66].map((g) => (
          <line
            key={g}
            x1={0}
            x2={W}
            y1={height * g}
            y2={height * g}
            stroke="var(--border)"
            strokeDasharray="3 5"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {n > 1 &&
          lines.map((l, k) => {
            const d = smoothPath(xs, l.values.map(yOf));
            return (
              <g key={l.key}>
                <path d={`${d} L${W},${height} L0,${height} Z`} fill={`url(#${uid}-${k})`} />
                <path
                  d={d}
                  fill="none"
                  stroke={tone(l.tone)}
                  strokeWidth={l.width}
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              </g>
            );
          })}
      </svg>
      {hover !== null && n > 1 && (
        <span
          aria-hidden
          className="pointer-events-none absolute top-0 bottom-0 w-px bg-muted"
          style={{ left: `${(hover / (n - 1)) * 100}%` }}
        />
      )}
      {primary && n > 0 && (
        // an HTML dot: inside the stretched SVG a circle would turn into an ellipse
        <span
          aria-hidden
          className="pointer-events-none absolute size-2 rounded-full border-2 border-surface bg-(--chart-line) box-content"
          style={{
            left: `${n < 2 ? 50 : (dotAt / (n - 1)) * 100}%`,
            top: yOf(primary.values[dotAt] ?? 0),
            transform: "translate(-50%, -50%)",
          }}
        />
      )}
    </div>
  );
}

const tone = (t: ChartLine["tone"]) => (t === "accent" ? "var(--chart-line)" : "var(--them)");

export type Legend = { key: string; label: string; value: string; tone: "accent" | "them" };

/**
 * A chart card: eyebrow title and optional control on top, the headline number with its time label and a
 * per-protocol legend (both follow the hover), then the plot with a y axis and x labels.
 */
export function ChartCard({
  title,
  icon,
  iconTone,
  control,
  lines,
  max,
  yLabels,
  xLabels,
  height,
  idle,
  at,
  legend,
  label,
}: {
  title: ReactNode;
  /** With a tone: a tinted chip before the title. */
  icon?: IconName;
  iconTone?: Tone;
  control?: ReactNode;
  lines: readonly ChartLine[];
  max: number;
  yLabels: readonly [string, string, string];
  xLabels: readonly string[];
  height: number;
  /** Headline while nothing is hovered: time label and value. */
  idle: { when: string; main: string };
  /** Headline for point i while hovering. */
  at: (i: number) => { when: string; main: string };
  /** Legend for point i (null = nothing hovered). */
  legend: (i: number | null) => Legend[];
  label: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const n = lines[0]?.values.length ?? 0;
  const i = hover !== null && hover < n ? hover : null;
  const head = i === null ? idle : at(i);
  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex flex-wrap items-center gap-3">
        <h2 className={cx("flex-1 text-[11px] font-bold tracking-[0.1em] text-muted", icon && iconTone && "flex items-center gap-2")}>
          {icon && iconTone && <IconChip icon={icon} tone={iconTone} />}
          {title}
        </h2>
        {control}
      </div>
      <div className="flex min-h-11 flex-wrap items-end gap-x-[18px] gap-y-1">
        <div className="flex flex-col gap-0.5">
          <span className="text-xs text-muted">{head.when}</span>
          <span className="font-mono text-2xl leading-[1.1] font-bold tracking-[-0.04em]">{head.main}</span>
        </div>
        <ul className="flex flex-wrap gap-x-3.5 gap-y-1 pb-[3px] text-xs text-muted">
          {legend(i).map((l) => (
            <li key={l.key} className="flex items-center gap-1.5">
              <span className={cx("size-2 rounded-full", l.tone === "accent" ? "bg-accent" : "bg-them")} />
              {l.label} <b className="font-mono font-bold text-fg">{l.value}</b>
            </li>
          ))}
        </ul>
      </div>
      <div className="flex gap-2">
        <div
          aria-hidden
          className="-my-1.5 flex w-[34px] flex-none flex-col justify-between text-right font-mono text-[10px] text-faint"
          style={{ height }}
        >
          {yLabels.map((y, k) => (
            <span key={k}>{y}</span>
          ))}
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-2">
          <Plot lines={lines} max={max} height={height} hover={i} onHover={setHover} label={label} />
          <div aria-hidden className="flex justify-between font-mono text-[10px] text-faint">
            {xLabels.map((x, k) => (
              <span key={k}>{x}</span>
            ))}
          </div>
        </div>
      </div>
    </section>
  );
}
