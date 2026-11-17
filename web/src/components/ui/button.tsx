import { Button as BaseButton } from "@base-ui/react/button";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

const variants = {
  primary: "bg-accent text-on-accent hover:bg-accent-hover",
  secondary: "border border-line bg-step text-fg hover:bg-(--step-hover) [&>svg:not([class*=text-])]:text-sec",
  outline: "border border-line bg-transparent text-fg hover:bg-step [&>svg:not([class*=text-])]:text-sec",
  // the quiet one: no frame, a step of grey on hover
  ghost: "bg-transparent text-sec hover:bg-step hover:text-fg",
  danger: "bg-danger-soft text-danger-text hover:bg-[color-mix(in_srgb,var(--danger)_22%,var(--surface))]",
  // a quiet destructive action: red text, the red wash only on hover
  ghostDanger: "bg-transparent text-danger-text hover:bg-danger-soft",
} as const;

// lg = 40px (fields, CTAs), md = 32px, sm = 28px (the height of a segment), xs = 24px (the height of a tag)
const sizes = {
  lg: "h-10 rounded-field px-4 text-sm font-medium",
  md: "h-8 rounded-ctl px-3 text-[13px] font-medium",
  sm: "h-7 rounded-ctl px-3 text-[13px] font-medium",
  xs: "h-6 rounded-chip px-2 text-xs font-medium",
} as const;

export type ButtonVariant = keyof typeof variants;
export type ButtonSize = keyof typeof sizes;

/** Class list of a button, for links that have to look like one. */
export function buttonClass(variant: ButtonVariant = "secondary", size: ButtonSize = "md", full?: boolean) {
  return cx(
    "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap transition-[transform,background-color,color,opacity] duration-150 ease-out active:scale-[0.98] data-disabled:pointer-events-none data-disabled:opacity-45",
    variants[variant],
    sizes[size],
    full && "w-full",
  );
}

type Props = Omit<ComponentProps<typeof BaseButton>, "className"> & {
  variant?: ButtonVariant;
  size?: ButtonSize;
  full?: boolean;
  className?: string;
};

export function Button({ variant = "secondary", size = "md", full, className, ...rest }: Props) {
  return <BaseButton className={cx(buttonClass(variant, size, full), className)} {...rest} />;
}
