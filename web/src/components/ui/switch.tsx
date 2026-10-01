import { Switch as BaseSwitch } from "@base-ui/react/switch";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

type Props = Omit<ComponentProps<typeof BaseSwitch.Root>, "className"> & { className?: string };

/** 40x24 toggle. Give it an accessible name with aria-label or by nesting it in a <label>. */
export function Switch({ className, ...rest }: Props) {
  return (
    <BaseSwitch.Root
      className={cx(
        "relative h-6 w-10 shrink-0 rounded-xl bg-surface-2 transition-colors duration-300 data-checked:bg-accent data-disabled:opacity-35",
        className,
      )}
      {...rest}
    >
      <BaseSwitch.Thumb className="absolute top-[3px] left-[3px] size-[18px] rounded-full bg-muted transition-[transform,background-color] duration-[400ms] ease-spring-strong data-checked:translate-x-4 data-checked:bg-on-accent" />
    </BaseSwitch.Root>
  );
}
