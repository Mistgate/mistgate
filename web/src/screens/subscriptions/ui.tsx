import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { cx } from "@/lib/cx";
import { useTx } from "@/screens/users/t";

/** The text box the Subscriptions forms share (40px, the page background, an accent border on focus). */
export const inputCls =
  "h-10 w-full min-w-0 rounded-field border border-line bg-canvas px-3 text-[13px] text-fg outline-none transition-colors duration-200 placeholder:text-faint focus:border-accent aria-invalid:border-danger";

/** A labelled field: bold label, the control, a short hint (or the error) under it. A hairline above all but the first. */
export function FieldBlock({ label, hint, error, htmlFor, children, className }: { label: ReactNode; hint?: ReactNode; error?: ReactNode; htmlFor?: string; children: ReactNode; className?: string }) {
  return (
    <div className={cx("flex flex-col gap-1.5 border-t border-line py-3.5 first:border-t-0", className)}>
      <label htmlFor={htmlFor} className="text-[13px] font-bold">
        {label}
      </label>
      {children}
      {error ? (
        <span role="alert" className="text-[11px] leading-snug text-danger-text">
          {error}
        </span>
      ) : (
        hint && <span className="text-[11px] leading-snug text-muted">{hint}</span>
      )}
    </div>
  );
}

/** The bar that appears while a form has unsaved changes, pinned above the dock on the phone. */
export function SaveBar({ busy, canSave, onSave, onDiscard }: { busy: boolean; canSave: boolean; onSave: () => void; onDiscard: () => void }) {
  const t = useTx();
  return (
    <div className="screen-enter sticky bottom-[84px] z-[3] flex flex-wrap items-center gap-2 rounded-card border border-accent-line bg-surface py-2 pe-2 ps-4 md:bottom-4">
      <span className="min-w-[140px] flex-1 text-[13px] font-bold">{t("subs.dirty")}</span>
      <Button variant="ghost" onClick={onDiscard} disabled={busy}>
        {t("subs.discard")}
      </Button>
      <Button variant="primary" disabled={!canSave || busy} onClick={onSave}>
        {t("subs.save")}
      </Button>
    </div>
  );
}

/** The line under a tab's top that says what the tab is for (one or two sentences, muted, a readable width). */
export function Lead({ children }: { children: ReactNode }) {
  return <p className="max-w-[680px] text-[13px] leading-normal text-muted">{children}</p>;
}

/** A pill the size of the filter chips that is pressed or not (`disabled`: a choice that is taken, `title` says by what). */
export function ToggleChip({ on, onClick, disabled, title, children }: { on: boolean; onClick: () => void; disabled?: boolean; title?: string; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      disabled={disabled}
      title={title}
      onClick={onClick}
      className={cx(
        "flex h-[30px] max-w-full items-center gap-1.5 rounded-[15px] border px-3 text-xs font-bold transition-colors duration-200 disabled:cursor-not-allowed disabled:opacity-60",
        on ? "border-accent-line bg-accent-soft text-fg" : "border-line bg-surface text-muted",
      )}
    >
      {on && <Icon name="check" size={12} />}
      {children}
    </button>
  );
}
