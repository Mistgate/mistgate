import { Button as BaseButton } from "@base-ui/react/button";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

// square: back/search buttons (34, or 36 on the mobile header). close: round x that turns on hover.
// round / roundAccent: the 26px - and + of the stepper.
const variants = {
  square: "size-[34px] rounded-ctl border border-line bg-surface text-fg hover:-translate-x-0.5",
  close: "size-7 rounded-full bg-surface text-muted transition-transform duration-[250ms] ease-spring-strong hover:rotate-90",
  round: "size-[26px] rounded-full border border-line bg-surface-2 text-fg active:scale-[0.88]",
  roundAccent: "size-[26px] rounded-full bg-accent text-on-accent active:scale-[0.88]",
  // a bare 28px icon in a row of controls (move, remove)
  flat: "size-[28px] rounded-lg text-muted hover:bg-surface-2 hover:text-fg",
  // a 36px bordered tool button sitting on a surface-2 field (copy next to a value)
  field: "size-9 rounded-ctl border border-line bg-surface text-muted hover:text-fg active:scale-[0.92]",
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
        "inline-flex shrink-0 items-center justify-center p-0 transition-transform duration-200 data-disabled:pointer-events-none data-disabled:opacity-35",
        variants[variant],
        className,
      )}
      {...rest}
    />
  );
}
