import { Field } from "@base-ui/react/field";
import type { ComponentProps, ReactNode } from "react";
import { Icon, IconChip, type IconName, type Tone } from "./icons";
import { cx } from "@/lib/cx";

type Shared = {
  /** Label above the field (13px, sec, as typed: no capitals forced). */
  label?: ReactNode;
  hint?: ReactNode;
  error?: ReactNode;
  /** Mono text: hex, IPs, codes. */
  mono?: boolean;
  className?: string;
  /** With a tone: a tinted chip in front of the label (what the field is about). */
  icon?: IconName;
  tone?: Tone;
};

type Props = Omit<ComponentProps<typeof Field.Control>, "className" | "size"> &
  Shared & {
    /** Magnifier inside the field: search boxes and the command palette. */
    search?: boolean;
  };

/**
 * The look of a field's box, for the raw inputs of a screen that cannot be a TextField (a textarea in a composer, a cell in
 * a table): 40px, 10 radius, the page colour inside, a hairline, and the accent ring on focus. An error turns the line red.
 */
export const fieldClass =
  "w-full rounded-field border border-line bg-canvas text-sm text-fg outline-none transition-[border-color,box-shadow] duration-150 ease-out placeholder:text-muted hover:border-(--border-hover) focus:border-accent-line focus:shadow-[0_0_0_3px_var(--focus-ring)] disabled:opacity-45 data-invalid:border-danger data-invalid:hover:border-danger data-invalid:focus:border-danger data-invalid:focus:shadow-[0_0_0_3px_color-mix(in_srgb,var(--danger)_16%,transparent)]";

function Shell({ label, hint, error, icon, tone, className, children }: Shared & { children: ReactNode }) {
  return (
    <Field.Root invalid={!!error} className={cx("flex flex-col gap-1.5", className)}>
      {label && (
        <Field.Label className={cx("text-[13px] text-sec", icon && tone && "flex items-center gap-2")}>
          {icon && tone && <IconChip icon={icon} tone={tone} />}
          {label}
        </Field.Label>
      )}
      {children}
      {error && (
        <Field.Error match className="screen-enter text-xs leading-4 text-danger-text">
          {error}
        </Field.Error>
      )}
      {hint && !error && <Field.Description className="text-xs leading-4 text-muted">{hint}</Field.Description>}
    </Field.Root>
  );
}

/** 40px text field. Focus turns the line to the accent with a soft ring, an error turns it red. */
export function TextField({ label, hint, error, search, mono, className, icon, tone, ...rest }: Props) {
  return (
    <Shell label={label} hint={hint} error={error} icon={icon} tone={tone} className={className}>
      <div className="relative">
        {search && <Icon name="search" className="pointer-events-none absolute top-3 inset-s-3 text-muted" />}
        <Field.Control
          className={cx(fieldClass, "h-10", search ? "pe-3 ps-9" : "px-3", mono && "font-mono text-[13px]")}
          dir={mono ? "ltr" : undefined}
          {...rest}
        />
      </div>
    </Shell>
  );
}

/** The many-line field: the same box, 20px lines, grows down by the handle. */
export function TextArea({
  label,
  hint,
  error,
  mono,
  className,
  icon,
  tone,
  rows = 4,
  ...rest
}: Omit<ComponentProps<"textarea">, "className" | "children"> & Shared) {
  return (
    <Shell label={label} hint={hint} error={error} icon={icon} tone={tone} className={className}>
      <Field.Control
        render={<textarea rows={rows} {...rest} />}
        className={cx(fieldClass, "min-h-10 resize-y px-3 py-2.5 leading-5", mono && "font-mono text-[13px]")}
        dir={mono ? "ltr" : undefined}
      />
    </Shell>
  );
}
