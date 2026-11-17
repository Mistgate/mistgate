import { Switch as BaseSwitch } from "@base-ui/react/switch";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

type Props = Omit<ComponentProps<typeof BaseSwitch.Root>, "className"> & { className?: string };

/** 36x20 toggle: the track is a step of grey, the accent when on; the thumb is the text colour, the ink on the accent. Give it an accessible name with aria-label or by nesting it in a <label>. */
export function Switch({ className, ...rest }: Props) {
  return (
    <BaseSwitch.Root
      className={cx(
        "relative h-5 w-9 shrink-0 rounded-[10px] bg-[color-mix(in_srgb,var(--text)_16%,transparent)] transition-colors duration-200 ease-out data-checked:bg-accent-mark data-disabled:opacity-45",
        className,
      )}
      {...rest}
    >
      <BaseSwitch.Thumb className="absolute top-0.5 inset-s-0.5 size-4 rounded-full bg-fg transition-[transform,background-color] duration-[250ms] ease-spring data-checked:translate-x-4 rtl:data-checked:-translate-x-4 data-checked:bg-on-accent" />
    </BaseSwitch.Root>
  );
}
