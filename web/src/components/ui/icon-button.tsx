import { Button as BaseButton } from "@base-ui/react/button";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

// square: the 40px quiet icon button (the board's .ic: search, back, theme, the x of a window). compact: the 32px one of a panel's
// header or a stepper (8px radius). flat: a bare 28px icon in a row of controls. field: a 40px tool button next to a field (copy).
const variants = {
  square: "size-10 rounded-field text-sec hover:bg-step hover:text-fg",
  compact: "size-8 rounded-ctl text-sec hover:bg-step hover:text-fg",
  flat: "size-7 rounded-ctl text-muted hover:bg-step hover:text-fg",
  field: "size-10 rounded-field border border-line bg-step text-sec hover:bg-(--step-hover) hover:text-fg active:scale-[0.96]",
} as const;

type Props = Omit<ComponentProps<typeof BaseButton>, "className"> & {
  variant?: keyof typeof variants;
  className?: string;
  /** Accessible name: an icon button has no text of its own. */
  "aria-label": string;
};

export function IconButton({ variant = "square", className, ...rest }: Props) {
  return (
    <BaseButton
      className={cx(
        "inline-flex shrink-0 items-center justify-center p-0 transition-[transform,background-color,color] duration-150 ease-out data-disabled:pointer-events-none data-disabled:opacity-45",
        variants[variant],
        className,
      )}
      {...rest}
    />
  );
}
