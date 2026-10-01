import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

export type SegmentedOption<V extends string> = { value: V; label: ReactNode; title?: string };

// thumb: 36px with a sliding selection. inset / flat: the 28px-item groups of Settings and
// of the list filters. lang: the RU/EN pill of the top bar, the selected item filled with the accent.
const looks = {
  thumb: {
    track: "relative grid h-9 auto-cols-fr grid-flow-col rounded-xl border border-line bg-surface p-[3px]",
    item: "relative rounded-[9px] px-3 text-xs font-bold text-muted transition-colors duration-300 data-checked:text-fg",
  },
  inset: {
    track: "flex gap-0.5 rounded-ctl bg-surface-2 p-0.5",
    item: "h-7 rounded-[9px] px-3 text-xs font-bold text-muted data-checked:bg-surface data-checked:text-fg",
  },
  flat: {
    track: "flex gap-0.5 rounded-xl border border-line bg-surface p-[3px]",
    item: "h-7 rounded-[9px] px-3 text-xs font-bold text-muted transition-colors duration-200 data-checked:bg-surface-2 data-checked:text-fg",
  },
  lang: {
    track: "flex h-7 gap-0.5 rounded-[14px] border border-line bg-surface p-0.5",
    item: "h-[22px] rounded-ctl px-2 font-mono text-[11px] font-bold text-muted data-checked:bg-accent data-checked:text-on-accent",
  },
} as const;

type Props<V extends string> = {
  value: V;
  onValueChange: (value: V) => void;
  options: readonly SegmentedOption<V>[];
  variant?: keyof typeof looks;
  "aria-label": string;
  className?: string;
};

export function Segmented<V extends string>({ value, onValueChange, options, variant = "inset", className, ...rest }: Props<V>) {
  const look = looks[variant];
  const index = Math.max(0, options.findIndex((o) => o.value === value));
  return (
    <RadioGroup
      value={value}
      onValueChange={(v) => onValueChange(v as V)}
      className={cx("box-border", look.track, className)}
      {...rest}
    >
      {variant === "thumb" && (
        <span
          aria-hidden
          className="absolute top-[3px] bottom-[3px] left-[3px] rounded-[9px] border border-accent-line bg-surface-2 transition-transform duration-[450ms] ease-[cubic-bezier(0.3,1.4,0.5,1)]"
          style={{ width: `calc((100% - 6px) / ${options.length})`, transform: `translateX(${index * 100}%)` }}
        />
      )}
      {options.map((o) => (
        <Radio.Root
          key={o.value}
          value={o.value}
          title={o.title}
          className={cx("inline-flex cursor-pointer items-center justify-center whitespace-nowrap", look.item)}
        >
          {o.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}
