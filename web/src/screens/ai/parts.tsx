import type { ReactNode } from "react";
import { Card } from "@/components/ui/bits";
import { cx } from "@/lib/cx";

// The AI screen's building blocks: a quiet heading (sentence case, a short line under it) and a settings list, rows of
// label + description on the start side and the control on the end side, divided by hairlines.

export function Heading({ title, desc, right }: { title: ReactNode; desc?: ReactNode; right?: ReactNode }) {
  return (
    <div className="flex items-start gap-3">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <h2 className="text-[14px] leading-tight font-semibold">{title}</h2>
        {desc && <p className="text-xs leading-snug text-pretty text-muted">{desc}</p>}
      </div>
      {right}
    </div>
  );
}

export function Section({ title, desc, children, className }: { title: ReactNode; desc?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("flex flex-col gap-2.5", className)}>
      <Heading title={title} desc={desc} />
      {children}
    </section>
  );
}

/** A card of rows with hairlines between them. */
export function RowList({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <Card lg className={cx("flex flex-col divide-y divide-line px-4 py-0", className)}>
      {children}
    </Card>
  );
}

/** One setting: its name and a line about it, the control at the far end (under the text on a phone). `stack` puts a wide control under the text. */
export function SettingRow({ label, hint, children, stack, htmlFor }: { label: ReactNode; hint?: ReactNode; children: ReactNode; stack?: boolean; htmlFor?: string }) {
  return (
    <div className={cx("flex gap-x-6 gap-y-2.5 py-3.5", stack ? "flex-col" : "flex-col sm:flex-row sm:items-center")}>
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <label htmlFor={htmlFor} className="text-[13.5px] font-medium">
          {label}
        </label>
        {hint && <span className="text-xs leading-snug text-pretty text-muted">{hint}</span>}
      </div>
      <div className={cx("flex flex-none sm:justify-end", stack ? "w-full" : "sm:max-w-[60%]")}>{children}</div>
    </div>
  );
}
