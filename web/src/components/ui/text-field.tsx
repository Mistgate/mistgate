import { Field } from "@base-ui/react/field";
import type { ComponentProps, ReactNode } from "react";
import { Icon, IconChip, type IconName, type Tone } from "./icons";
import { cx } from "@/lib/cx";

type Props = Omit<ComponentProps<typeof Field.Control>, "className" | "size"> & {
  /** Eyebrow label above the field (11px, uppercase). */
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  /** Magnifier inside the field: search boxes and the command palette. */
  search?: boolean;
  /** Mono text: hex, IPs, codes. */
  mono?: boolean;
  className?: string;
  /** With a tone: a tinted chip in front of the label (what the field is about). */
  icon?: IconName;
  tone?: Tone;
};

/** 44px text field. Focus turns the border accent (no glow), an error turns it red. */
export function TextField({ label, hint, error, search, mono, className, icon, tone, ...rest }: Props) {
  return (
    <Field.Root invalid={!!error} className={cx("flex flex-col gap-1.5", className)}>
      {label && (
        <Field.Label className={cx("text-[11px] font-bold tracking-[0.1em] text-muted uppercase", icon && tone && "flex items-center gap-2")}>
          {icon && tone && <IconChip icon={icon} tone={tone} />}
          {label}
        </Field.Label>
      )}
      <div className="relative">
        {search && <Icon name="search" className="pointer-events-none absolute top-3.5 left-3 text-muted" />}
        <Field.Control
          className={cx(
            "h-11 w-full rounded-field border border-line bg-surface text-sm text-fg outline-none transition-colors duration-200 focus:border-accent data-invalid:border-danger",
            search ? "pr-3.5 pl-9 font-normal" : "px-3.5 font-medium",
            mono && "font-mono",
          )}
          {...rest}
        />
      </div>
      {error && (
        <Field.Error match className="screen-enter text-xs leading-snug text-danger-text">
          {error}
        </Field.Error>
      )}
      {hint && !error && <Field.Description className="text-xs leading-snug text-muted">{hint}</Field.Description>}
    </Field.Root>
  );
}
