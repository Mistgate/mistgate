import type { ReactNode } from "react";
import { IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { cx } from "@/lib/cx";
import type { UpdateTab } from "./tabs";

// The three things an installation keeps current, as one row of cards: this panel, the agents on the nodes, the
// servers' operating systems. Each card says where its layer stands, and the chosen one opens below. They are the
// page's tabs, so the state of all three is on screen whichever is open.

export type Layer = {
  value: UpdateTab;
  icon: IconName;
  tone: Tone;
  title: string;
  /** The one fact of the layer: a version, "11 of 11 up to date", "12 servers". */
  fact: ReactNode;
  pill: { kind: StatusKind; label: string };
  content: ReactNode;
};

export function Layers({ layers, value, onValueChange, ...rest }: { layers: readonly Layer[]; value: UpdateTab; onValueChange: (v: UpdateTab) => void; "aria-label": string }) {
  const active = layers.find((l) => l.value === value) ?? layers[0];
  if (!active) return null;
  const pick = (i: number) => {
    const next = layers[(i + layers.length) % layers.length];
    if (!next) return;
    onValueChange(next.value);
    document.getElementById(`up-layer-${next.value}`)?.focus();
  };
  return (
    <div className="flex flex-col gap-3.5">
      <div role="tablist" aria-orientation="horizontal" className={cx("grid gap-2 md:gap-3", layers.length === 3 ? "md:grid-cols-3" : "md:grid-cols-2")} {...rest}>
        {layers.map((l, i) => {
          const on = l.value === active.value;
          return (
            <button
              key={l.value}
              id={`up-layer-${l.value}`}
              type="button"
              role="tab"
              aria-selected={on}
              aria-controls="up-layer-panel"
              tabIndex={on ? 0 : -1}
              onClick={() => onValueChange(l.value)}
              onKeyDown={(e) => {
                const step = e.key === "ArrowRight" || e.key === "ArrowDown" ? 1 : e.key === "ArrowLeft" || e.key === "ArrowUp" ? -1 : 0;
                if (step === 0) return;
                e.preventDefault();
                pick(i + step);
              }}
              className={cx(
                "group relative flex min-w-0 items-center gap-3 overflow-hidden rounded-card-lg border p-3 text-start transition-colors md:flex-col md:items-stretch md:gap-2.5 md:p-4",
                on ? "border-accent-line bg-surface" : "border-line bg-surface/60 hover:bg-surface",
              )}
            >
              <span className="flex min-w-0 flex-1 items-center gap-2.5">
                <IconChip icon={l.icon} tone={l.tone} />
                <span className="flex min-w-0 flex-col">
                  <span className={cx("truncate text-[13px] font-bold", on ? "text-fg" : "text-muted group-hover:text-fg")}>{l.title}</span>
                  <span className="truncate text-xs text-muted md:hidden">{l.fact}</span>
                </span>
              </span>
              <span className="min-w-0 truncate font-mono text-[15px] font-bold max-md:hidden">{l.fact}</span>
              <span className="flex min-w-0 flex-none md:flex-auto">
                <StatusPill kind={l.pill.kind} label={l.pill.label} sm />
              </span>
              {/* the chosen card is tied to what opens under it */}
              <span aria-hidden className={cx("absolute inset-x-0 bottom-0 h-0.5 bg-accent transition-opacity duration-200", on ? "opacity-100" : "opacity-0")} />
            </button>
          );
        })}
      </div>
      <div id="up-layer-panel" role="tabpanel" aria-labelledby={`up-layer-${active.value}`} key={active.value} className="screen-enter outline-none">
        {active.content}
      </div>
    </div>
  );
}
