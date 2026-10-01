import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import type { StatusKind } from "./status";

/** Inline notice: amber by default, red for errors. A blinking dot, a bold lead-in and the text. */
export function Notice({
  title,
  children,
  tone = "warn",
  className,
}: {
  title?: ReactNode;
  children?: ReactNode;
  tone?: "warn" | "danger";
  className?: string;
}) {
  const danger = tone === "danger";
  return (
    <div
      role={danger ? "alert" : "status"}
      className={cx(
        "screen-enter flex items-center gap-2.5 rounded-field border px-3 py-2.5",
        danger ? "border-[color-mix(in_oklch,var(--danger)_45%,var(--border))] bg-danger-soft" : "border-warn-line bg-warn-soft",
        className,
      )}
    >
      <span
        aria-hidden
        className={cx("size-2 flex-none animate-[mg-ui-blink_1s_infinite] rounded-full", danger ? "bg-danger" : "bg-warn")}
      />
      <div className="text-xs leading-snug text-fg">
        {title && <b>{title} </b>}
        {children}
      </div>
    </div>
  );
}

/** State banner under a page header (node unreachable, waiting for the agent...): tinted by the status, with actions on the right. */
export function Banner({
  kind,
  title,
  children,
  actions,
}: {
  kind: StatusKind;
  title: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <section className={cx(`tone-${kind} tint screen-enter flex flex-wrap items-start gap-3 rounded-card px-4 py-3.5`)}>
      <div className="flex min-w-[220px] flex-1 flex-col gap-1">
        <h2 className="text-[15px] font-extrabold tracking-[-0.02em]">{title}</h2>
        {children && <p className="text-[13px] leading-snug text-pretty text-muted">{children}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </section>
  );
}

/** Calm empty state: optional icon, a heavy title, one muted line, optional action. */
export function EmptyState({
  icon,
  title,
  children,
  action,
  className,
}: {
  icon?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("screen-enter flex flex-col items-center gap-1.5 px-6 py-10 text-center", className)}>
      {icon && <span className="mb-3 grid size-12 place-items-center rounded-full border border-line bg-surface">{icon}</span>}
      <div className="text-[22px] font-extrabold tracking-[-0.03em]">{title}</div>
      {children && <div className="max-w-md text-[13px] text-muted">{children}</div>}
      {action && <div className="mt-3">{action}</div>}
    </div>
  );
}
