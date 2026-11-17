import type { ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cx } from "@/lib/cx";
import "./page.css";

// The drawn parts the Health and Reachability pages share, in the board's own marks (pn.css: .i, .pp-sec, .pv-pill, .rw-gl, .us-none, .hl-skl,
// .pv-qe; rules in ./page.css, all under .hlp). Both screens import this file, so the stylesheet rides with whichever of them loads first.

/** The glyph paths of the two boards, on the 24 grid (the board's `.i`: 16 px, a 1.5 stroke, round ends). */
export const P = {
  bell: "M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15zM10 20a2 2 0 0 0 4 0",
  clock: "M4 12a8 8 0 1 0 2.4-5.7M4 5v4h4M12 8v4l3 2",
  server: "M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01",
  eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  user: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM3 20c0-3.3 2.7-5.5 6-5.5s6 2.2 6 5.5",
  sparkle: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z",
  list: "M4 7h16M4 12h16M4 17h10",
  warn: "M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z",
  check: "M5 12.5l4.5 4.5L19 7.5",
  chev: "M9 6l6 6-6 6",
  arrow: "M5 12h14M13 6l6 6-6 6",
  down: "M7 10l5 5 5-5",
  globe: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18",
} as const;

export type Tone = "sand" | "sage" | "sky" | "mint" | "lav" | "rose" | "";

/** A glyph of the board (the `.i` svg). */
export function Gl({ d, className }: { d: string; className?: string }) {
  return (
    <svg className={cx("i", className)} viewBox="0 0 24 24" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}

/** The state before a word: "ok" a filled dot, "part" half, "bad" a cross, "busy" a turning ring, "none" a ring, "shut" a grey cross (the board's `.rw-gl`). */
export type DotKind = "ok" | "part" | "bad" | "busy" | "none" | "shut";
export function Dot({ kind, title, className }: { kind: DotKind; title?: string; className?: string }) {
  return <i className={cx("rw-gl", kind, className)} title={title} aria-hidden="true" />;
}

/** The pill of a verdict (the board's `.pv-pill`): its dot, then the word. An empty kind is the quiet grey one. */
export function Pill({ kind, glyph, children, className }: { kind: "ok" | "part" | "bad" | "busy" | ""; glyph?: DotKind; children: ReactNode; className?: string }) {
  return (
    <span className={cx("pv-pill", kind, className)}>
      <Dot kind={glyph ?? (kind || "none")} />
      {children}
    </span>
  );
}

/** The head of the page: the title and a line of what it says (the board's `.sb-h`). */
export function PageHead({ title, sub, className }: { title: ReactNode; sub?: ReactNode; className?: string }) {
  return (
    <header className={cx("sb-h", className)}>
      <h1>{title}</h1>
      {sub != null && <p>{sub}</p>}
    </header>
  );
}

/** A tab's label with its count (the board's `.bdg.up-n`): amber for what needs a look, `mut` quiet, `crit` red. */
export function TabLabel({ text, n, tone = "mut" }: { text: ReactNode; n?: number; tone?: "warn" | "mut" | "crit" }) {
  return (
    <>
      {text}
      {!!n && <span className={cx("bdg", tone === "warn" ? "" : tone)}>{n}</span>}
    </>
  );
}

/** A card with the icon head: a tinted square, the title (small capitals), a count and a button at the far end (the board's `.card.pp-sec.sb-sec`). */
export function Sec({
  tone = "",
  icon,
  title,
  count,
  action,
  className,
  headClassName,
  id,
  children,
  ...rest
}: {
  tone?: Tone;
  icon: string;
  title: ReactNode;
  count?: ReactNode;
  action?: ReactNode;
  className?: string;
  headClassName?: string;
  id?: string;
  children?: ReactNode;
} & Omit<React.ComponentProps<"section">, "title" | "className" | "id" | "children">) {
  return (
    <section id={id} className={cx("card pp-sec sb-sec", className)} {...rest}>
      <div className={cx("pp-sh", headClassName)}>
        <span className={cx("pp-ic", tone)} aria-hidden="true">
          <Gl d={icon} />
        </span>
        <h3>{title}</h3>
        {count != null && <span className="sb-cnt2 mono hl-cn">{count}</span>}
        {action}
      </div>
      {children}
    </section>
  );
}

/** The quiet card: a round glyph, a title, a line, a button (the board's `.us-none`; `hl-calm` adds the gap under it). */
export function Calm({
  kind = "",
  icon = P.check,
  title,
  text,
  action,
  className,
  role,
}: {
  kind?: "ok" | "bad" | "";
  icon?: string;
  title: ReactNode;
  text?: ReactNode;
  action?: ReactNode;
  className?: string;
  role?: "alert";
}) {
  return (
    <section className={cx("card us-none", className)} role={role}>
      <span className={cx("g", kind)} aria-hidden="true">
        <Gl d={icon} />
      </span>
      <b>{title}</b>
      {text != null && <span>{text}</span>}
      {action}
    </section>
  );
}

/** The load: two cards of grey bars in the shape of the tab (the board's `.hl-skl`). */
export function LoadCards({ label }: { label: string }) {
  return (
    <div className="hl-skl" aria-busy="true" aria-label={label}>
      <section className="card pp-skc">
        <i className="sk" style={{ width: 180 }} />
        <i className="sk b" />
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

/** What the screen's own query says when it fails: the red card with "Retry" (the board's `.us-none.pv-qe`). */
export function LoadFailed({ title, text, retry, onRetry }: { title: ReactNode; text: ReactNode; retry: ReactNode; onRetry: () => void }) {
  return (
    <section className="card us-none pv-qe" role="alert">
      <span className="g bad" aria-hidden="true">
        <Gl d={P.warn} />
      </span>
      <b>{title}</b>
      <span>{text}</span>
      <Button size="lg" onClick={onRetry}>
        {retry}
      </Button>
    </section>
  );
}
