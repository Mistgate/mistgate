import { Button as BaseButton } from "@base-ui/react/button";
import type { ComponentProps } from "react";
import { cx } from "@/lib/cx";

const variants = {
  primary: "bg-accent text-on-accent",
  secondary: "border border-line bg-surface text-fg",
  outline: "border border-line bg-transparent text-fg",
  ghost: "bg-transparent text-muted hover:bg-surface-2",
  danger: "bg-danger-soft text-danger-text",
  // a quiet destructive action: red text, the red wash only on hover
  ghostDanger: "bg-transparent text-danger-text hover:bg-danger-soft",
} as const;

// lg = 44px fields/CTAs, md = 36px, sm = 28px pill, xs = 22px
const sizes = {
  lg: "h-11 rounded-field px-4 text-sm font-extrabold",
  md: "h-9 rounded-ctl px-3.5 text-[13px] font-bold",
  sm: "h-7 rounded-[14px] px-[11px] text-xs font-bold",
  xs: "h-[22px] rounded-ctl px-2 text-[11px] font-bold",
} as const;

export type ButtonVariant = keyof typeof variants;
export type ButtonSize = keyof typeof sizes;

/** Class list of a button, for links that have to look like one. */
export function buttonClass(variant: ButtonVariant = "secondary", size: ButtonSize = "md", full?: boolean) {
  return cx(
    "inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap transition-[transform,background-color,opacity] duration-200 ease-spring active:scale-[0.97] data-disabled:pointer-events-none data-disabled:opacity-35",
    variants[variant],
    sizes[size],
    // secondary/outline are a notch lighter than the primary CTA
    (variant === "secondary" || variant === "outline") && size === "lg" && "font-bold",
    variant === "ghost" && "font-semibold",
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
