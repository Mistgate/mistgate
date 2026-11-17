import { Fragment, type ComponentProps, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { cx } from "@/lib/cx";
import { useT } from "@/i18n";
import { G } from "@/screens/users/user-bits";
import "./settings.css";

// What every Settings tab is made of, as the board draws it (PnSettings + pn.css: the pp-, sb-, sx-, rc-, an- rules, ported under
// the one root `.sx`; the rules are in settings.css). The frame (screens/settings.tsx) puts a tab's content under the tab strip;
// a tab is cards (`Sec`), rows in them (`Row`), a foot with buttons (`Foot`), a switch row (`Swr`), a value to copy (`CmdRow`), a
// banner (`Pb`). Part B (Security, Sessions, Audit, Backups) builds its cards from the same pieces.

/** The board's 24-grid glyphs: the heads of the cards and the marks that go with them. */
export const ICONS = {
  device: "M4 5h16v11H4zM9 20h6M12 16v4",
  people: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM3 20a6 6 0 0 1 12 0M16 4.5a3.5 3.5 0 0 1 0 6.5M17.5 14.5A6 6 0 0 1 21 20",
  gauge: "M4 16a8 8 0 1 1 16 0M12 16l4-5",
  lines: "M5 6h14M5 12h14M5 18h9",
  refresh: "M4 12a8 8 0 0 1 14-5.3L20 9M20 4v5h-5M20 12a8 8 0 0 1-14 5.3L4 15M4 20v-5h5",
  info: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 11v5M12 8v.01",
  clock: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  download: "M12 4v11M7 11l5 5 5-5M5 20h14",
  bars: "M4 20V10M10 20V4M16 20v-7M22 20H2",
  globe: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18",
  warn: "M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z",
  tick: "M5 12.5l4.5 4.5L19 7.5",
  chev: "M6 9l6 6 6-6",
  plus: "M12 5v14M5 12h14",
  // Part B's heads
  passkey: "M8 11a4 4 0 0 1 8 0v2M12 11v6M5 14v-3a7 7 0 0 1 14 0v5M8 15v3M16 16v2",
  lock: "M7 11V8a5 5 0 0 1 10 0v3M6 11h12v9H6z",
  cloud: "M7 18a4 4 0 0 1-.5-8A6 6 0 0 1 18 9.5 4 4 0 0 1 17 18z",
  history: "M4 12a8 8 0 1 0 2.4-5.7M4 5v4h4M12 8v4l3 2",
  key: "M15 7a4 4 0 1 1-3.9 5H8v3H5v-3H3v-2h8.1A4 4 0 0 1 15 7z",
  terminal: "M4 5h16v14H4zM8 10l3 2-3 2M13 15h3",
  box: "M4 7l8-4 8 4v10l-8 4-8-4zM4 7l8 4 8-4M12 11v10",
} as const;

export type Tone = "mint" | "lav" | "sky" | "sand" | "sage" | "rose" | "none";

/** The root of a Settings screen: every rule of settings*.css hangs on `.sx`, so nothing leaks to the other screens. */
export function SxRoot({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cx("sx", className)}>{children}</div>;
}

/**
 * A card of the board (card pp-sec sb-sec): the tinted glyph and the title (a small-caps head), what stands at the end of the head
 * (`aside`: a button, a count), a lead line under it, the body, and a quiet foot line (`foot`).
 */
export function Sec({
  tone,
  d,
  title,
  aside,
  lead,
  foot,
  className,
  children,
  ...rest
}: {
  tone: Tone;
  d: string;
  title: ReactNode;
  aside?: ReactNode;
  lead?: ReactNode;
  foot?: ReactNode;
  className?: string;
  children?: ReactNode;
  "data-card"?: string;
}) {
  return (
    <section className={cx("card pp-sec sb-sec", className)} {...rest}>
      <div className="pp-sh">
        <span className={cx("pp-ic", tone)} aria-hidden>
          <G d={d} size={15} stroke={1.5} />
        </span>
        <h2>{title}</h2>
        {aside}
      </div>
      {lead && <p className="sb-lead">{lead}</p>}
      {children}
      {foot && <p className="sb-vfoot">{foot}</p>}
    </section>
  );
}

/** The rows of a card (a label with its hint on the start side, the control on the other). */
export function Rows({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cx("pp-rows", className)}>{children}</div>;
}

/** One row: `label` (with a `badge` after it), a `hint` under it, and the control. `end` pushes a switch to the end edge. */
export function Row({ label, hint, badge, end, className, children }: { label: ReactNode; hint?: ReactNode; badge?: ReactNode; end?: boolean; className?: string; children: ReactNode }) {
  return (
    <div className={cx("pp-row", className)}>
      <div className="pp-lb">
        <b>
          {label}
          {badge}
        </b>
        {hint && <span>{hint}</span>}
      </div>
      <div className={cx("pp-ct", end && "sx-end")}>{children}</div>
    </div>
  );
}

/** The foot of a card that holds buttons (sx-foot): they sit in a row, 8px apart. */
export function Foot({ className, children }: { className?: string; children: ReactNode }) {
  return <div className={cx("sx-foot", className)}>{children}</div>;
}

/** A card's switch row: the words on the start, the switch on the end (sx-swr). */
export function Swr({ label, hint, className, children }: { label: ReactNode; hint?: ReactNode; className?: string; children: ReactNode }) {
  return (
    <div className={cx("sx-swr", !!hint && "sx-swr2", className)}>
      {hint ? (
        <span>
          <b>{label}</b>
          <em>{hint}</em>
        </span>
      ) : (
        <b>{label}</b>
      )}
      {children}
    </div>
  );
}

/** A value to copy (an address, a command): mono text in a bordered field with "Copy" at the end (an-code sx-cmd). `wrap` lets a long command break. */
export function CmdRow({ value, wrap, className }: { value: string; wrap?: boolean; className?: string }) {
  return (
    <div className={cx("an-code sx-cmd", wrap && "an-cw", className)}>
      <code className="mono">{value}</code>
      <CopyButton value={value} size="md" />
    </div>
  );
}

/** The board's banner (rc-pb): a glyph that is a shape as well as a colour, a bold line and a quiet one, a button at the end. */
export function Pb({ tone, title, children, action, className }: { tone: "bad" | "part"; title?: ReactNode; children?: ReactNode; action?: ReactNode; className?: string }) {
  return (
    <div className={cx("rc-pb sx-pb", tone === "bad" && "bad", className)} role={tone === "bad" ? "alert" : undefined}>
      <i className={cx("rw-gl", tone)} aria-hidden />
      <div>
        {title && <b>{title}</b>}
        {children && <span>{children}</span>}
      </div>
      {action}
    </div>
  );
}

/** A quiet small tag after a label (uq-otg). */
export function Otg({ children, className }: { children: ReactNode; className?: string }) {
  return <i className={cx("uq-otg", className)}>{children}</i>;
}

/** The board's spinner: a ring that turns (an-sp). */
export function Spin() {
  return <span className="an-sp" aria-hidden />;
}

/** A button that works and says so: the board's busy button (the spinner ahead of the words, the press ignored while it runs; `disabled` is for nothing to do yet). */
export function BusyButton({ busy, disabled, className, onClick, children, ...rest }: { busy?: boolean; children: ReactNode } & Omit<ComponentProps<typeof Button>, "children">) {
  return (
    <Button {...rest} disabled={disabled && !busy} aria-busy={busy || undefined} className={cx(busy && "sx-busy", className)} onClick={busy ? undefined : onClick}>
      {busy && <Spin />}
      {children}
    </Button>
  );
}

/** A text with `{name}` slots, the slots filled with nodes (a link, a mono word): "Use {link} for scripts" -> text, link, text. */
export function slots(template: string, nodes: Record<string, ReactNode>): ReactNode[] {
  return template.split(/(\{\w+\})/).map((part, i) => {
    const name = /^\{(\w+)\}$/.exec(part)?.[1];
    return name && name in nodes ? <Fragment key={i}>{nodes[name]}</Fragment> : part;
  });
}

/** A mono word in running text. */
export const Mono = ({ children }: { children: ReactNode }) => <span className="mono">{children}</span>;

/**
 * The tab's load state, as the board draws it: two cards of grey bars while the data is on its way, and "Settings failed to load" with
 * "Retry" when the panel did not answer. A tab hands it what its main query says; the frame stays put either way.
 */
export function SxGate({ pending, error, onRetry, children }: { pending: boolean; error: boolean; onRetry: () => void; children: ReactNode }) {
  if (error) return <SxFailed onRetry={onRetry} />;
  if (pending) return <SxSkeleton />;
  return <>{children}</>;
}

export function SxSkeleton() {
  const t = useT();
  return (
    <div className="hl-skl" aria-busy="true" aria-label={t("sx.load")}>
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

export function SxFailed({ onRetry }: { onRetry: () => void }) {
  const t = useT();
  return (
    <section className="card us-none pv-qe" role="alert">
      <span className="g bad" aria-hidden>
        <G d={ICONS.warn} size={20} stroke={1.5} />
      </span>
      <b>{t("sx.err.title")}</b>
      <span>{t("sx.err.body")}</span>
      <Button size="lg" onClick={onRetry}>
        {t("sx.err.retry")}
      </Button>
    </section>
  );
}
