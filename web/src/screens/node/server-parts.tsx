import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { Glyph } from "@/screens/overview-parts";
import "./server.css";

// The pieces the Server tab's cards share (the board's .sv-c, .sv-h, .sv-row, .sv-save, .sv-opt, .sv-step, .nx-facts).

/** A card of a section (the board's .card.sv-c): it rises as the section opens. */
export function SvCard({ children, className, tone, ...rest }: { children: ReactNode; className?: string; tone?: "danger" } & Omit<React.HTMLAttributes<HTMLElement>, "className">) {
  return (
    <section {...rest} className={cx("sv-c relative rounded-card border border-line bg-surface", tone === "danger" && "danger", className)}>
      {children}
    </section>
  );
}

/** A card's head: the title 16/24 and what the card is about under it (the board's .sv-h). `mono`: a path or a command, not a sentence. */
export function SvHead({ title, hint, mono, id }: { title: ReactNode; hint?: ReactNode; mono?: boolean; id?: string }) {
  return (
    <div className="sv-h">
      <h3 id={id}>{title}</h3>
      {hint && <span className={mono ? "mono" : undefined}>{hint}</span>}
    </div>
  );
}

/** A line of a card: the title and what it means on the left, the control on the right, a hairline above (the board's .sv-row). */
export function SvRow({ title, hint, children, mono }: { title: ReactNode; hint?: ReactNode; children?: ReactNode; mono?: boolean }) {
  return (
    <div className="sv-row">
      <div>
        <b>{title}</b>
        {hint && <span className={mono ? "mono" : undefined}>{hint}</span>}
      </div>
      {children}
    </div>
  );
}

/**
 * The bar that opens under a form when something in it is changed: what is unsaved, Cancel and Save. After a save it says
 * "Saved" for a moment and closes. `busy` keeps the bar open and the button turned.
 */
export function SvSave({ dirty, saved, busy, invalid, onCancel, onSave, note }: { dirty: boolean; saved?: boolean; busy?: boolean; invalid?: boolean; onCancel: () => void; onSave: () => void; note?: ReactNode }) {
  const t = useT();
  const on = dirty || busy || saved;
  return (
    <div className={cx("sv-save", on && "on", saved && !dirty && !busy && "ok")} aria-live="polite">
      <span>{saved && !dirty && !busy ? t("np.sv.saved") : (note ?? t("np.sv.unsaved"))}</span>
      <Button type="button" variant="secondary" size="sm" disabled={busy} onClick={onCancel} tabIndex={on ? 0 : -1}>
        {t("np.sv.cancel")}
      </Button>
      <Button type="button" variant="primary" size="sm" disabled={busy || invalid || !dirty} onClick={onSave} tabIndex={on ? 0 : -1}>
        {t("common.save")}
      </Button>
    </div>
  );
}

/** The ring that fills with a dot (the board's .nx-rad). */
export function Radio({ on }: { on: boolean }) {
  return (
    <span className="nx-rad" aria-hidden aria-checked={on}>
      <i />
    </span>
  );
}

/** One choice of a list (the board's .sv-opt): a radio, the title with an optional mark, and what it means. */
export function SvOpt({ on, title, mark, children, onPick, disabled, name }: { on: boolean; title: ReactNode; mark?: ReactNode; children?: ReactNode; onPick: () => void; disabled?: boolean; name?: string }) {
  return (
    <button type="button" role="radio" aria-checked={on} aria-label={name} disabled={disabled} className={cx("sv-opt", on && "on")} onClick={onPick}>
      <Radio on={on} />
      <div>
        <b>
          {title}
          {mark && <em>{mark}</em>}
        </b>
        {children}
      </div>
    </button>
  );
}

/** - value + in a capsule (the board's .sv-step). */
export function SvStep({ value, onDec, onInc, decLabel, incLabel, decOff, incOff }: { value: ReactNode; onDec: () => void; onInc: () => void; decLabel: string; incLabel: string; decOff?: boolean; incOff?: boolean }) {
  return (
    <div role="group" className="sv-step">
      <button type="button" aria-label={decLabel} disabled={decOff} onClick={onDec}>
        <Glyph d="M6 12h12" size={16} stroke={1.5} />
      </button>
      <b className="mono">{value}</b>
      <button type="button" aria-label={incLabel} disabled={incOff} onClick={onInc}>
        <Glyph d="M6 12h12M12 6v12" size={16} stroke={1.5} />
      </button>
    </div>
  );
}

/** A command in a frame with a copy button and a line under it (the board's .sv-code). */
export function SvCode({ command, note, children }: { command: string; note?: ReactNode; children: ReactNode }) {
  return (
    <div className="sv-code">
      <code>{command}</code>
      {children}
      {note && <span>{note}</span>}
    </div>
  );
}

/** Term and value pairs (the board's .nx-facts). */
export function Facts({ items, className }: { items: { l: ReactNode; v: ReactNode; mono?: boolean; tone?: "warn" | "crit"; key?: string }[]; className?: string }) {
  return (
    <dl className={cx("nx-facts", className)}>
      {items.map((x, i) => (
        <div key={x.key ?? i}>
          <dt>{x.l}</dt>
          <dd className={cx(x.mono && "mono", x.tone)}>{x.v}</dd>
        </div>
      ))}
    </dl>
  );
}
