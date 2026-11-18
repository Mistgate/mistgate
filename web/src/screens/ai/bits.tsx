import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { plainText, useT } from "@/i18n";
import { cx } from "@/lib/cx";
import "./ai.css";

// What every tab of the AI screen is built of, as the board draws it (PnAI + pn.css): a card with a tinted glyph and a small-caps head
// (`Sec`), a state glyph with its pill (`Pill`), the load and the failure of a tab, the board's own dropdown (`Dd`) and the window (`Dlg`).
// Every rule is in ai.css, under .aip (the page) or .ai-dlg (a window).

/** The dev kit's scene: the flags of what a page opens with (a window, a row asking "Delete?"). The product has none: the set is empty. */
const SceneCtx = createContext<ReadonlySet<string>>(new Set());
export const SceneProvider = SceneCtx.Provider;
export const useScene = () => useContext(SceneCtx);

/** The board's 24-grid glyphs. */
export const ICONS = {
  spark: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z",
  plug: "M9 3v5M15 3v5M7 8h10v4a5 5 0 0 1-10 0zM12 17v4",
  coin: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM9.5 14.5c.5 1 1.4 1.5 2.5 1.5 1.5 0 2.5-.8 2.5-2s-1-1.7-2.5-2-2.5-.8-2.5-2 1-2 2.5-2c1.1 0 2 .5 2.5 1.5M12 6v1.5M12 16v1.5",
  note: "M6 3h9l4 4v14H6zM9 12h7M9 16h5",
  chat: "M4 5h16v11H9l-5 4z",
  key: "M15 7a4 4 0 1 1-3.9 5H8v3H5v-3H3v-2h8.1A4 4 0 0 1 15 7z",
  shield: "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z",
  history: "M4 12a8 8 0 1 0 2.4-5.7M4 5v4h4M12 8v4l3 2",
  chev: "M9 6l6 6-6 6",
  down: "M6 9l6 6 6-6",
  tick: "M5 12.5l4.5 4.5L19 7.5",
  plus: "M12 5v14M5 12h14",
  x: "M6 6l12 12M18 6L6 18",
  warn: "M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  globe: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18",
} as const;

export type Tone = "lav" | "sand" | "sky" | "mint";

/** A glyph of the board's 24 grid. */
export function Gl({ d, className }: { d: string; className?: string }) {
  return (
    <svg className={className} viewBox="0 0 24 24" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round">
      <path d={d} />
    </svg>
  );
}

/** A card of the board (card pp-sec sb-sec): the glyph, the small-caps title, what stands at its end, a lead, the body, a quiet foot line. */
export function Sec({ tone, d, title, aside, lead, foot, className, children }: { tone: Tone; d: string; title: ReactNode; aside?: ReactNode; lead?: ReactNode; foot?: ReactNode; className?: string; children?: ReactNode }) {
  return (
    <section className={cx("card pp-sec sb-sec", className)}>
      <div className="pp-sh">
        <span className={cx("pp-ic", tone)} aria-hidden="true">
          <Gl d={d} />
        </span>
        <h3>{title}</h3>
        {aside && <span className="ai-hb">{aside}</span>}
      </div>
      {lead && <p className="sb-lead">{lead}</p>}
      {children}
      {foot && <p className="sb-vfoot">{foot}</p>}
    </section>
  );
}

/** A row of a card: the label with its line on the start side, the control on the other. */
export function Row({ label, hint, className, children }: { label: ReactNode; hint?: ReactNode; className?: string; children: ReactNode }) {
  return (
    <div className={cx("pp-row", className)}>
      <div className="pp-lb">
        <b>{label}</b>
        {hint && <span>{hint}</span>}
      </div>
      <div className="pp-ct">{children}</div>
    </div>
  );
}

export type Kind = "ok" | "part" | "bad" | "busy" | "";

/** A state with its glyph (a shape as well as a colour) and its words. */
export function Pill({ kind, children, className }: { kind: Kind; children: ReactNode; className?: string }) {
  return (
    <span className={cx("pv-pill", kind, className)}>
      <i className={cx("rw-gl", kind)} aria-hidden="true" />
      {children}
    </span>
  );
}

/** The board's loading state of a tab: two cards of bars. */
export function Skeleton() {
  const t = useT();
  return (
    <div className="hl-skl" aria-busy="true" aria-label={t("aip.loading")}>
      <section className="card pp-skc">
        <i className="sk" style={{ width: 180 }} />
        <i className="sk b" />
        <i className="sk b" />
        <i className="sk b" />
      </section>
      <section className="card pp-skc">
        <i className="sk" style={{ width: 140 }} />
        <i className="sk b" />
        <i className="sk b" />
      </section>
    </div>
  );
}

/** The board's failure of a tab: what did not load, that nothing was changed, and Retry. */
export function ErrCard({ onRetry }: { onRetry: () => void }) {
  const t = useT();
  return (
    <section className="card us-none pv-qe" role="alert">
      <span className="g bad" aria-hidden="true">
        <Gl d={ICONS.warn} />
      </span>
      <b>{t("aip.err.title")}</b>
      <span>{t("aip.err.body")}</span>
      <Button size="lg" onClick={onRetry}>
        {t("aip.err.retry")}
      </Button>
    </section>
  );
}

/** What a tab that only the owner has says to anyone else. */
export function OwnerOnly({ title, icon }: { title: ReactNode; icon?: string }) {
  return (
    <section className="card us-none">
      <span className="g" aria-hidden="true">
        <Gl d={icon ?? ICONS.lock} />
      </span>
      <b>{title}</b>
    </section>
  );
}

/** An empty card: a glyph, what is not there, one line, and what to do. */
export function Empty({ d, title, body, action, calm }: { d: string; title: ReactNode; body: ReactNode; action?: ReactNode; calm?: boolean }) {
  return (
    <section className={cx("card us-none rc-emp", calm && "hl-calm")}>
      <span className="g" aria-hidden="true">
        <Gl d={d} />
      </span>
      <b>{title}</b>
      <span>{body}</span>
      {action}
    </section>
  );
}

/** The board's dropdown: a button with the chosen name, a list under it with a tick on the chosen one; Esc and a click outside close it. */
export function Dd({ label, value, options, onChange, disabled, defaultOpen }: { label: string; value: string; options: { value: string; label: string }[]; onChange: (v: string) => void; disabled?: boolean; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(!!defaultOpen);
  const list = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const chosen = options.find((o) => o.value === value);
  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>('[aria-selected="true"]')?.focus();
  }, [open]);
  const move = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      setOpen(false);
      trigger.current?.focus();
      return;
    }
    const step = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    if (!step) return;
    e.preventDefault();
    const items = [...(list.current?.querySelectorAll<HTMLElement>("[role=option]") ?? [])];
    const at = items.indexOf(document.activeElement as HTMLElement);
    items[(at + step + items.length) % items.length]?.focus();
  };
  return (
    <div className="ai-dd" onKeyDown={move}>
      {open && <div className="ai-ddx" onClick={() => setOpen(false)} />}
      <button ref={trigger} type="button" className="ai-ddb" aria-label={label} aria-haspopup="listbox" aria-expanded={open} aria-disabled={disabled || undefined} onClick={() => !disabled && setOpen((v) => !v)}>
        <span>{chosen?.label ?? ""}</span>
        <Gl d={ICONS.down} />
      </button>
      {open && (
        <div className="ai-ddm" role="listbox" aria-label={label} ref={list}>
          {options.map((o) => (
            <button
              key={o.value}
              type="button"
              role="option"
              aria-selected={o.value === value}
              onClick={() => {
                setOpen(false);
                onChange(o.value);
              }}
            >
              <span>{o.label}</span>
              {o.value === value && <Gl d={ICONS.tick} />}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

/** The board's window: the base Modal in the board's skin (ai-dlg), the body under .us-db, the buttons in the footer. */
export function Dlg({ title, onClose, footer, className, shot, busy, locked, children }: { title: ReactNode; onClose: () => void; footer: ReactNode; className?: string; shot?: string; busy?: boolean; locked?: boolean; children: ReactNode }) {
  const t = useT();
  // `locked`: only the button in the footer closes it (the secret of a new token is shown once: Esc and a click outside must not lose it)
  return (
    <Modal open onOpenChange={(o) => !o && !busy && !locked && onClose()} title={title} closeLabel={locked ? undefined : t("common.close")} className={cx("ai-dlg", className)} shot={shot} footer={footer}>
      <div className="us-db">{children}</div>
    </Modal>
  );
}

/** The id that has just appeared in a list (not on the first draw): its row flashes once, as the board's "fresh" rows do. */
export function useFresh(ids: string[]): string {
  const prev = useRef<string[] | null>(null);
  const [fresh, setFresh] = useState("");
  const key = ids.join("\u0001");
  useEffect(() => {
    const before = prev.current;
    prev.current = ids;
    if (!before) return;
    const added = ids.find((i) => !before.includes(i));
    if (!added) return;
    setFresh(added);
    const h = setTimeout(() => setFresh(""), 1600);
    return () => clearTimeout(h);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the joined ids stand for the list
  }, [key]);
  return fresh;
}

/** A copy button of the board: the words swap for "Copied" for a moment; a browser that refuses says so, and the text stays to select. */
export function CopyBtn({ value, onFail }: { value: string; onFail?: () => void }) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(plainText(value));
      setDone(true);
      setTimeout(() => setDone(false), 1600);
    } catch {
      onFail?.();
      toast(t("aip.copyFailed"));
    }
  };
  return (
    <Button size="md" onClick={() => void copy()}>
      {done ? t("common.copied") : t("common.copy")}
    </Button>
  );
}

/** A switch row inside a window (the board's ai-sws). */
export function SwRow({ title, hint, children }: { title: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div>
      <span>
        <b>{title}</b>
        {hint && <em>{hint}</em>}
      </span>
      {children}
    </div>
  );
}
