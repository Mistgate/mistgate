import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { cx } from "@/lib/cx";
import { path, Svg } from "./parts";
import "./subs-hx.css";

/**
 * The pieces the two tabs "Response headers" and "Mihomo templates" share (the board's hx- rules, subs-hx.css): the card with its head,
 * a row that unfolds into its editor, chips, a message with its glyph, the footer with the add button, the hint and the save, and the
 * choice of the person the preview is for. Nothing here knows what a header or a template is.
 */

/** The card head: the title and what the list holds (a count and a size, mono). */
export function CardHead({ title, count, bad }: { title: ReactNode; count?: ReactNode; bad?: boolean }) {
  return (
    <header className="hx-h">
      <h3>{title}</h3>
      {count !== undefined && <span className={cx("mono", bad && "bad")}>{count}</span>}
    </header>
  );
}

/** What stands where the list is empty: a line and what it means. */
export function Nothing({ title, text }: { title: ReactNode; text: ReactNode }) {
  return (
    <div className="hx-no">
      <b>{title}</b>
      <span>{text}</span>
    </div>
  );
}

/** The head of a row: its number, what it is (`children`, the lines of `.hx-kv`), a word at the end and the chevron. The whole is the button that unfolds it. */
export function RowHead({ n, open, onToggle, meta, metaClass, label, children }: { n: number; open: boolean; onToggle: () => void; meta: ReactNode; metaClass?: string; label?: string; children: ReactNode }) {
  return (
    <button type="button" className="hx-rh" aria-expanded={open} aria-label={label} onClick={onToggle}>
      <span className="hx-i mono">{n}</span>
      <span className="hx-kv">{children}</span>
      <span className={cx("hx-meta", metaClass)}>{meta}</span>
      <Svg d={path.chevD} className="hx-cv" />
    </button>
  );
}

/** A group of chips (the board's .hx-chs) with its name for a screen reader. */
export function Chips({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="hx-chs" role="group" aria-label={label}>
      {children}
    </div>
  );
}

/** One chip: pressed or not; `off` is a choice that is not there now (kept in the tab order, it says why with `title`). */
export function HxChip({ on, off, title, onClick, children }: { on: boolean; off?: boolean; title?: string; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" className="hx-ch" aria-pressed={on} aria-disabled={off || undefined} title={title} onClick={off ? undefined : onClick}>
      {children}
    </button>
  );
}

/** A labelled line of options ("Formats | chips"), the label 72 wide where the editor is wide. */
export function Opt({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="hx-opt">
      <span className="hx-ol">{label}</span>
      {children}
    </div>
  );
}

/** A message of an editor: red with a cross for what stops a save, amber with a half dot for what only needs a look. */
export function Msg({ kind, children }: { kind: "bad" | "part"; children: ReactNode }) {
  return (
    <p className={cx("hx-m", kind === "bad" && "bad")} role={kind === "bad" ? "alert" : undefined}>
      <i className={cx("rw-gl", kind)} aria-hidden="true" />
      <span>{children}</span>
    </p>
  );
}

type FootProps = {
  addLabel: string;
  onAdd: () => void;
  addOff: boolean;
  hint: ReactNode;
  bad: boolean;
  saveLabel: string;
  saveOff: boolean;
  onSave: () => void;
  /** Takes the changes back: only while there is something to take back. */
  onDiscard?: () => void;
  discardLabel?: string;
};

/** The foot of a list card: add, what to know (red while the list cannot be saved), save. */
export function Foot({ addLabel, onAdd, addOff, hint, bad, saveLabel, saveOff, onSave, onDiscard, discardLabel }: FootProps) {
  return (
    <footer className="hx-ft">
      <Button className="sb-btn" disabled={addOff} onClick={onAdd}>
        {addLabel}
      </Button>
      <span className={cx("hx-fh", bad && "bad")} aria-live="polite">
        {hint}
      </span>
      {onDiscard && (
        <Button variant="ghost" className="sb-btn hx-dis" onClick={onDiscard}>
          {discardLabel}
        </Button>
      )}
      <Button variant="primary" className="sb-btn" disabled={saveOff} onClick={onSave}>
        {saveLabel}
      </Button>
    </footer>
  );
}

/** More people than this: a list to pick from instead of a chip for each. */
const chipsUpTo = 8;

/** The person the preview is for: a chip each while they are few (the board), a list when they are many. */
export function People({ label, users, value, onPick }: { label: string; users: readonly { id: string; name: string }[]; value: string; onPick: (id: string) => void }) {
  return (
    <Opt label={label}>
      {users.length <= chipsUpTo ? (
        <Chips label={label}>
          {users.map((u) => (
            <HxChip key={u.id} on={u.id === value} onClick={() => onPick(u.id)}>
              {u.name}
            </HxChip>
          ))}
        </Chips>
      ) : (
        <div className="hx-sel">
          <Select aria-label={label} value={value} onValueChange={onPick} options={users.map((u) => ({ value: u.id, label: u.name }))} />
        </div>
      )}
    </Opt>
  );
}
