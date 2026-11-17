import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

export type SegmentedOption<V extends string> = { value: V; label: ReactNode; title?: string };

// One look, three ways to draw the chosen item. A 28px item in a 2px frame on the page colour; the chosen one is a step lighter.
// thumb: the lighter plate slides to the chosen item. inset / flat: it just switches.
const track = "rounded-field border border-line bg-canvas p-0.5";
const item = "h-7 rounded-ctl px-3 text-[13px] text-sec transition-colors duration-150 ease-out hover:text-fg data-checked:text-fg";
const looks = {
  thumb: {
    track: cx("relative grid h-[34px] auto-cols-fr grid-flow-col", track),
    item: cx("relative", item),
  },
  inset: {
    track: cx("flex gap-0.5", track),
    item: cx(item, "data-checked:bg-surface-2"),
  },
  flat: {
    track: cx("flex gap-0.5", track),
    item: cx(item, "data-checked:bg-surface-2"),
  },
} as const;

type Props<V extends string> = {
  value: V;
  onValueChange: (value: V) => void;
  options: readonly SegmentedOption<V>[];
  variant?: keyof typeof looks;
  "aria-label": string;
  className?: string;
  /** Long labels wrap onto two lines instead of pushing the control wider than its box (a phone). */
  wrap?: boolean;
};

export function Segmented<V extends string>({ value, onValueChange, options, variant = "inset", className, wrap, ...rest }: Props<V>) {
  const look = looks[variant];
  const index = Math.max(0, options.findIndex((o) => o.value === value));
  return (
    <RadioGroup
      value={value}
      onValueChange={(v) => onValueChange(v as V)}
      className={cx("box-border", look.track, wrap && "h-auto min-h-[34px]", className)}
      {...rest}
    >
      {variant === "thumb" && (
        <span
          aria-hidden
          className="absolute top-0.5 bottom-0.5 inset-s-0.5 rounded-ctl bg-surface-2 transition-transform duration-[350ms] ease-spring"
          style={{ width: `calc((100% - 4px) / ${options.length})`, transform: `translateX(calc(${index * 100}% * var(--x-dir)))` }}
        />
      )}
      {options.map((o) => (
        <Radio.Root
          key={o.value}
          value={o.value}
          title={o.title}
          className={cx("inline-flex cursor-pointer items-center justify-center", wrap ? "py-1 text-center leading-tight" : "whitespace-nowrap", look.item)}
        >
          {o.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}
