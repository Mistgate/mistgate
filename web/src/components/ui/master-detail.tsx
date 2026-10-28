import { useEffect, useRef, type KeyboardEvent, type ReactNode } from "react";
import { Icon, IconChip, type IconName, type Tone } from "./icons";

// A list of things, each with its state, and the one that is open (Reachability's sources, a node's ways in): on a wide
// screen side by side, on a phone they take turns. The open one is a key in the URL; these are the shared moving parts.

/** Narrower than this the list and the open item take turns. */
export const phone = () => !!window.matchMedia?.("(max-width: 719.98px)").matches;

/**
 * Opening and closing on a phone: opening pushes (the browser's back closes it) and moves the focus to the way back; going
 * back, by the link or the browser, returns it to the row that was open. A wide screen only swaps what is shown.
 * `open(id, replace)` and `close()` navigate.
 */
export function useTurns<K extends string>({ chosen, backId, rowId, open, close }: { chosen: K | undefined; backId: string; rowId: (id: K) => string; open: (id: K, replace: boolean) => void; close: () => void }) {
  const tapped = useRef(false);
  const shown = useRef(chosen);
  useEffect(() => {
    const was = shown.current;
    shown.current = chosen;
    if (was === chosen || !phone()) return;
    const target = chosen ? document.getElementById(backId) : was && document.getElementById(rowId(was));
    target?.focus({ preventScroll: true });
    target?.scrollIntoView({ block: chosen ? "start" : "nearest" });
    // runs on every render (rowId is a new function each time) and acts only when the open item changed
  }, [chosen, backId, rowId]);
  const pick = (id: K) => {
    tapped.current = phone();
    open(id, !tapped.current);
  };
  const back = () => {
    if (tapped.current) window.history.back();
    else close();
    tapped.current = false;
  };
  return { pick, back };
}

/** Arrows (and Home, End) walk the rows of a list, Enter or Space opens: the list is one stop for Tab. */
export function walkTabs(e: KeyboardEvent<HTMLElement>) {
  const tabs = [...e.currentTarget.querySelectorAll<HTMLElement>("[role=tab]")];
  const i = tabs.indexOf(document.activeElement as HTMLElement);
  const to = ({ ArrowDown: i + 1, ArrowRight: i + 1, ArrowUp: i - 1, ArrowLeft: i - 1, Home: 0, End: tabs.length - 1 } as Record<string, number>)[e.key];
  if (i < 0 || to === undefined) return;
  e.preventDefault();
  tabs[(to + tabs.length) % tabs.length]!.focus();
}

/** The way back to the list, on a phone only. */
export function BackButton({ id, label, onClick }: { id: string; label: string; onClick: () => void }) {
  return (
    <button
      id={id}
      type="button"
      onClick={onClick}
      className="flex h-9 scroll-mt-20 items-center gap-1.5 self-start rounded-ctl pr-3 pl-1.5 text-[13px] font-bold text-muted transition-colors hover:bg-surface-2 hover:text-fg min-[720px]:hidden"
    >
      <Icon name="back" size={16} aria-hidden />
      {label}
    </button>
  );
}

/**
 * The open item: its header (icon, name with its small chips, a line on what it is, a control or state on the right), then
 * its blocks. As tall as the list at least; a short one leaves the room at its end.
 */
export function DetailCard({ icon, tone = "mint", title, extra, sub, control, children }: { icon: IconName; tone?: Tone; title: ReactNode; extra?: ReactNode; sub: ReactNode; control?: ReactNode; children: ReactNode }) {
  return (
    <section className="@container flex h-full min-w-0 flex-col rounded-card-lg border border-line bg-surface">
      <div className="flex items-start gap-3 px-4 pt-4 pb-3 md:px-5 md:pt-5">
        <IconChip icon={icon} tone={tone} size={36} className="rounded-[10px]" />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex min-h-6 flex-wrap items-center gap-x-2.5 gap-y-1">
            <h3 className="text-base leading-tight font-extrabold tracking-[-0.02em]">{title}</h3>
            {extra}
          </div>
          <p className="max-w-2xl text-xs leading-normal text-pretty text-muted">{sub}</p>
        </div>
        {control && <span className="flex h-6 items-center">{control}</span>}
      </div>
      {children}
    </section>
  );
}
