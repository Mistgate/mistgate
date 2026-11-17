import type { ReactNode } from "react";
import { cx } from "@/lib/cx";
import { StatusGlyph, type StatusKind } from "./status";

/** A state line inside a card: the status tint and its glyph, still (nothing blinks), an optional action at its end. */
export function Callout({ kind, alert, action, className, children }: { kind: "warn" | "bad"; alert?: boolean; action?: ReactNode; className?: string; children: ReactNode }) {
  const Body = typeof children === "string" ? "p" : "div"; // a sentence is a paragraph; a title with its line is not
  return (
    <div role={alert ? "alert" : undefined} className={cx(`tint tone-${kind} flex flex-wrap items-start gap-x-2 gap-y-2.5 rounded-field p-3`, className)}>
      <span className="pt-px">
        <StatusGlyph kind={kind} />
      </span>
      <Body className="min-w-0 flex-1 basis-48 text-[13px] leading-5 text-pretty text-fg">{children}</Body>
      {action && <div className="flex flex-wrap gap-2 sm:flex-none max-sm:ms-[22px] max-sm:min-w-0 max-sm:max-w-[calc(100%-22px)]">{action}</div>}
    </div>
  );
}

/** Inline notice: amber by default, red for errors, grey and still for "nothing is wrong, this just is not here". A dot, a bold lead-in and the text. */
export function Notice({
  title,
  children,
  tone = "warn",
  className,
}: {
  title?: ReactNode;
  children?: ReactNode;
  tone?: "warn" | "danger" | "quiet";
  className?: string;
}) {
  const danger = tone === "danger";
  const quiet = tone === "quiet";
  return (
    <div
      role={danger ? "alert" : "status"}
      className={cx(
        "screen-enter flex items-center gap-2.5 rounded-field border px-3 py-2.5",
        danger ? "border-[color-mix(in_srgb,var(--danger)_45%,transparent)] bg-danger-soft" : quiet ? "border-line bg-step" : "border-warn-line bg-warn-soft",
        className,
      )}
    >
      <span
        aria-hidden
        className={cx("size-2 flex-none rounded-full", quiet ? "bg-muted" : cx("animate-[mg-ui-blink_1s_infinite]", danger ? "bg-danger" : "bg-warn"))}
      />
      <div className="text-[13px] leading-5 text-fg">
        {title && <b className="font-medium">{title} </b>}
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
        <h2 className="text-base leading-6 font-medium">{title}</h2>
        {children && <p className="text-sm leading-5 text-pretty text-sec">{children}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-2">{actions}</div>}
    </section>
  );
}

/** Calm empty state: an optional icon in a soft square (red with `bad`: a load that failed), a 16px title, one quiet line, an optional action. */
export function EmptyState({
  icon,
  title,
  children,
  action,
  bad,
  className,
}: {
  icon?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  action?: ReactNode;
  bad?: boolean;
  className?: string;
}) {
  return (
    <div className={cx("screen-enter flex flex-col items-center gap-1 px-6 py-10 text-center", className)}>
      {icon && <span className={cx("mb-2.5 grid size-11 place-items-center rounded-[14px]", bad ? "bg-danger-soft text-danger" : "bg-step text-sec")}>{icon}</span>}
      <div className="text-base leading-6 font-medium">{title}</div>
      {children && <div className="max-w-[420px] text-sm leading-5 text-sec">{children}</div>}
      {action && <div className="mt-3.5">{action}</div>}
    </div>
  );
}
