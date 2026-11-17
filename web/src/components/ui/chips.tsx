import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

export type ChipOption<V extends string> = { value: V; label: ReactNode };

/**
 * Row of 32px filter pills (log levels, audit source): the chosen one is a step lighter, with no frame.
 * One choice at a time, arrow keys move it.
 */
export function FilterChips<V extends string>({
  value,
  onValueChange,
  options,
  className,
  ...rest
}: {
  value: V;
  onValueChange: (value: V) => void;
  options: readonly ChipOption<V>[];
  "aria-label": string;
  className?: string;
}) {
  return (
    <RadioGroup
      value={value}
      onValueChange={(v) => onValueChange(v as V)}
      className={cx("flex flex-wrap gap-1", className)}
      {...rest}
    >
      {options.map((o) => (
        <Radio.Root
          key={o.value}
          value={o.value}
          className="flex h-8 cursor-pointer items-center gap-2 rounded-2xl border border-line px-3 text-[13px] whitespace-nowrap text-sec transition-colors duration-150 ease-out hover:text-fg data-checked:border-transparent data-checked:bg-surface-2 data-checked:text-fg"
        >
          {o.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}
