import { Tabs } from "@base-ui/react/tabs";
import { useEffect, useRef, type ReactNode } from "react";
import { Switch } from "@/components/ui/switch";
import { cx } from "@/lib/cx";
import "./subs.css";

/**
 * The pieces every tab of the Subscriptions screen is built from (the board's PnSubs): a section card with its tinted mark, rows of
 * "label | control", a switch with its explanation, the status glyph, a column grid with a side column. Their rules are in
 * subs.css, under .sbp, the page's root.
 */

/** The board's icon paths (24x24, a 1.5 stroke); the drawing is the `.i` of subs.css. */
export const path = {
  check: "M5 12.500l4.500 4.500L19 7.500",
  chevR: "M9 6l6 6-6 6",
  chevD: "M7 10l5 5 5-5",
  arrowR: "M5 12h14M13 6l6 6-6 6",
  plus: "M12 5v14M5 12h14",
  x: "M6 6l12 12M18 6L6 18",
  up: "M7 14l5-5 5 5",
  grip: "M9 6h.01M15 6h.01M9 12h.01M15 12h.01M9 18h.01M15 18h.01",
  search: "M11 18a7 7 0 1 0 0-14 7 7 0 0 0 0 14zM20 20l-4-4",
  eye: "M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12zM12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6z",
  file: "M7 3h7l4 4v14H7zM14 3v4h4",
  phone: "M8 3h8v18H8zM11 18h2",
  sliders: "M4 7h10M18 7h2M4 12h4M12 12h8M4 17h12M14 5v4M8 10v4M16 15v4",
  globe: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.500 2.700 2.500 15.300 0 18M12 3c-2.500 2.700-2.500 15.300 0 18",
  pulse: "M3 12h4l2.500-6 4 12 2.500-6h5",
  lines: "M5 6h14M5 12h14M5 18h9",
  list: "M4 6h16M4 12h10M4 18h6M17 15l3 3-3 3",
  monitor: "M4 5h16v11H4zM9 20h6M12 16v4",
  noFile: "M5 5l14 14M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z",
} as const;

export function Svg({ d, className }: { d: string; className?: string }) {
  return (
    <svg className={cx("i", className)} viewBox="0 0 24 24" aria-hidden="true">
      <path d={d} />
    </svg>
  );
}

/** The 8px status glyph of the board (`rw-gl`): ok, part (half), bad (a cross), busy (turning), or a quiet ring. */
export function Glyph({ kind, className }: { kind?: "ok" | "part" | "bad" | "busy" | "none"; className?: string }) {
  return <i className={cx("rw-gl", kind, className)} aria-hidden="true" />;
}

export type SectionTone = "lav" | "mint" | "sand" | "sky" | "rose" | "sage" | "none";

/** A section card: its tinted mark (an icon path or a node), the title in capitals, an optional side (a button, a count) at the end. */
export function Section({ tone, icon, title, side, children, className, shot }: { tone: SectionTone; icon: string | ReactNode; title: ReactNode; side?: ReactNode; children: ReactNode; className?: string; shot?: string }) {
  return (
    <section className={cx("card pp-sec sb-sec", className)} data-shot={shot}>
      <div className="pp-sh">
        <span className={cx("pp-ic", tone)} aria-hidden="true">
          {typeof icon === "string" ? <Svg d={icon} /> : icon}
        </span>
        <h3>{title}</h3>
        {side}
      </div>
      {children}
    </section>
  );
}

/** The two-column grid of a tab: the main column and a side one (it sticks while the page scrolls, under 1040 it goes below). */
export function Grid({ main, side, className }: { main: ReactNode; side?: ReactNode; className?: string }) {
  return (
    <div className={cx("pf-g pp-grid sb-grid", className)}>
      <div className="col">{main}</div>
      {side && <aside className="col pp-as">{side}</aside>}
    </div>
  );
}

/** One row of a section: the label with its hint on the left, the control on the right. */
export function Row({ label, hint, children, className }: { label: ReactNode; hint?: ReactNode; children: ReactNode; className?: string }) {
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

/** A switch with its title and an explanation: the rows of "What the apps get" and of the page's switches. */
export function Toggle({ id, title, label, hint, checked, onChange, disabled, shot }: { id?: string; title: ReactNode; /** The accessible name, where the title alone ("On") would not say what the switch is for. */ label?: string; hint?: ReactNode; checked: boolean; onChange: (on: boolean) => void; disabled?: boolean; shot?: string }) {
  return (
    <div className="sb-tg" id={id} data-shot={shot}>
      <div>
        <b>{title}</b>
        {hint && <span>{hint}</span>}
      </div>
      <Switch className="sb-sw" aria-label={label ?? (typeof title === "string" ? title : undefined)} checked={checked} onCheckedChange={onChange} disabled={disabled} />
    </div>
  );
}

export type TabSpec = { value: string; label: ReactNode; badge?: ReactNode };

/**
 * The strip of tabs under the page title (the board's .nd-tabs.sb-tabs): 44 high, the chosen one carries a line of the accent, a
 * badge (amber) counts what needs a look. The panels are drawn by the caller below it. A strip wider than the window scrolls and
 * brings the chosen tab into view.
 */
export function SbTabs({ items, value, onValueChange, label }: { items: readonly TabSpec[]; value: string; onValueChange: (v: string) => void; label: string }) {
  const list = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const el = list.current;
    const active = el?.querySelector<HTMLElement>('[role="tab"][aria-selected="true"]');
    if (!el || !active) return;
    const box = el.getBoundingClientRect();
    const tab = active.getBoundingClientRect();
    if (tab.left < box.left) el.scrollLeft -= box.left - tab.left + 16;
    else if (tab.right > box.right) el.scrollLeft += tab.right - box.right + 16;
  }, [value]);
  return (
    <Tabs.Root value={value} onValueChange={(v) => onValueChange(String(v))}>
      <Tabs.List ref={list} className="sb-tabs" aria-label={label}>
        {items.map((it) => (
          <Tabs.Tab key={it.value} value={it.value}>
            {it.label}
            {it.badge !== undefined && <span className="bdg up-n warn">{it.badge}</span>}
          </Tabs.Tab>
        ))}
      </Tabs.List>
    </Tabs.Root>
  );
}
