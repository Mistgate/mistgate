import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

/** Inline controls for a node's body: none of them starts a node drag (data-nodrag). */

/** A label with a value badge on the right and a slider under it. */
export function GraphSlider({
  label,
  value,
  min = 0,
  max = 1,
  step = 0.01,
  format = (v) => String(v),
  onChange,
  disabled,
}: {
  label: ReactNode;
  value: number;
  min?: number;
  max?: number;
  step?: number;
  format?: (v: number) => string;
  onChange: (v: number) => void;
  disabled?: boolean;
}) {
  const pct = ((value - min) / (max - min)) * 100;
  return (
    <div className="px-3.5 py-1.5" data-nodrag>
      <div className="mb-1.5 flex items-center justify-between gap-2 text-[12.5px]">
        <span className="min-w-0 truncate text-muted">{label}</span>
        <GraphValue>{format(value)}</GraphValue>
      </div>
      <input
        type="range"
        className="mg-range"
        min={min}
        max={max}
        step={step}
        value={value}
        disabled={disabled}
        aria-label={typeof label === "string" ? label : undefined}
        style={{ "--p": `${pct}%` } as React.CSSProperties}
        onChange={(e) => onChange(Number(e.target.value))}
      />
    </div>
  );
}

/** A small mono badge for a value (a port, a number). */
export function GraphValue({ children, tone, className }: { children: ReactNode; tone?: "ok" | "warn" | "bad"; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex h-5 items-center rounded-md border border-line bg-surface-2 px-1.5 font-mono text-[11px] font-medium tabular-nums text-fg",
        tone && `tone-${tone} tone-text`,
        className,
      )}
    >
      {children}
    </span>
  );
}
