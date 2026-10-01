import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import type { ReactNode } from "react";
import { cx } from "@/lib/cx";

export type ChipOption<V extends string> = { value: V; label: ReactNode };

/**
 * Row of 30px filter pills (log levels, audit source): the chosen one is accent-soft with an accent border.
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
      className={cx("flex flex-wrap gap-1.5", className)}
      {...rest}
    >
      {options.map((o) => (
        <Radio.Root
          key={o.value}
          value={o.value}
          className="flex h-[30px] cursor-pointer items-center gap-1.5 rounded-[15px] border border-line bg-surface px-3 text-xs font-bold whitespace-nowrap text-muted transition-colors duration-200 data-checked:border-accent-line data-checked:bg-accent-soft data-checked:text-fg"
        >
          {o.label}
        </Radio.Root>
      ))}
    </RadioGroup>
  );
}
