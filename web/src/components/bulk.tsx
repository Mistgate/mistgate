import { Menu } from "@base-ui/react/menu";
import { Link, useBlocker } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { selectItemClass, selectPopupClass } from "@/components/ui/select";
import { StatusDot, StatusPill, type StatusKind } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import { dirOf, useLang, useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Job, Row, Skipped, Target } from "@/lib/batch";
import { cx } from "@/lib/cx";

// The parts the bulk bars of the Nodes and Users pages share: the dock the bar floats in, the row of actions and its
// menus, a batch shown row by row, the "Select ▾" menu, the lists under a confirm, Esc and the leave guard. What each page
// can do (its actions, who takes part, the calls) lives with the page: screens/nodes-bulk*.tsx, screens/users/bulk*.tsx.

export type Avail = { count: number; why?: string };

/** What a bar button or a menu row shows of an action: its section chip, its name, whether it is a removal. */
export type ActionMeta = { icon: IconName; tone: Tone; label: MessageKey; danger?: boolean };

/** One entry of a menu: an action with how many it takes; `null` is a divider. */
export type MenuEntry<A extends string> = { id: A; meta: ActionMeta; avail: Avail } | null;

/** A link to a row's own page: the router's `to`, its params and search. */
export type RowLink = { to: string; params?: Record<string, string>; search?: Record<string, unknown> };

/**
 * The bar and, while it shows, the room at the page's foot (so it never covers the last row): the bar itself is a portal on
 * the body, in the look of the phone's dock.
 */
export function BulkFloat({ show, wide, region, children }: { show: boolean; wide?: boolean; region: string; children: ReactNode }) {
  if (!show) return null;
  return (
    <>
      <div aria-hidden className="h-20 flex-none" />
      {createPortal(
        <BulkDock wide={wide} region={region}>
          {children}
        </BulkDock>,
        document.body,
      )}
    </>
  );
}

/**
 * The dock's material: translucent, blurred, the dock radius and its one shadow. Floating, it sits at the foot of the
 * content (beside the sidebar on a desktop, in the dock's place on a phone); `float={false}` draws it in place (dev kit).
 */
export function BulkDock({ float = true, wide, region, children }: { float?: boolean; wide?: boolean; region: string; children: ReactNode }) {
  const frame = (
    <div
      role="region"
      aria-label={region}
      className={cx(
        "pointer-events-auto flex w-full flex-col overflow-hidden rounded-dock border border-line bg-dock shadow-(--shadow-dock) backdrop-blur-[16px]",
        "transition-[translate,opacity] duration-200 ease-out-soft starting:translate-y-3 starting:opacity-0 md:w-fit md:max-w-full",
        wide && "md:min-w-[min(100%,640px)]",
      )}
    >
      {children}
    </div>
  );
  if (!float) return frame;
  return (
    <div data-bulk-bar className="pointer-events-none fixed inset-x-3.5 bottom-3.5 z-20 flex justify-center md:inset-s-56 md:inset-e-0 md:bottom-5 md:px-[clamp(24px,4vw,64px)]">
      {frame}
    </div>
  );
}

/**
 * Narrow when the full row (every action on the bar) is wider than the room the bar can take, its frame's parent: then
 * the actions that can go move into "More". Measured, not guessed, since the words differ by language; unknown
 * (no layout, as in tests) reads as wide.
 */
export function useNarrow(ref: RefObject<HTMLElement | null>): boolean {
  const lang = useLang();
  // decided per language: another one has other widths, so it starts wide and is measured again
  const [seen, setSeen] = useState({ lang, narrow: false });
  const narrow = seen.lang === lang && seen.narrow;
  const need = useRef(0);
  useLayoutEffect(() => {
    const row = ref.current;
    const room = row?.parentElement?.parentElement;
    if (!row || !room) return;
    const check = () => {
      if (!narrow) need.current = row.scrollWidth;
      const cs = getComputedStyle(room);
      const w = room.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
      const next = w > 0 && need.current > w + 1;
      setSeen((p) => (p.lang === lang && p.narrow === next ? p : { lang, narrow: next }));
    };
    check();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(check);
    ro.observe(room);
    return () => ro.disconnect();
  }, [ref, narrow, lang]);
  return narrow;
}

export const barButton =
  "inline-flex h-10 flex-none items-center gap-2 rounded-ctl px-2.5 text-[13px] font-bold whitespace-nowrap text-fg outline-none transition-[background-color,transform] duration-150 ease-out-soft hover:bg-surface-2 active:scale-[0.97] focus-visible:ring-2 focus-visible:ring-accent data-popup-open:bg-surface-2 disabled:pointer-events-none disabled:opacity-35";

/** The bar's first button: clear the selection (Esc does the same). */
export function ClearButton({ onClear }: { onClear: () => void }) {
  const t = useT();
  return (
    <button type="button" aria-label={t("bulk.clear")} title={`${t("bulk.clear")} (Esc)`} onClick={onClear} className={cx(barButton, "w-10 justify-center px-0 text-muted hover:text-fg")}>
      <Icon name="x" size={16} />
    </button>
  );
}

/** A direct action of the bar: its section chip, its name, how many it takes; off with the reason in a tooltip when none. */
export function BarAction({ meta, avail, busy, count, onClick }: { meta: ActionMeta; avail: Avail; busy: boolean; count?: boolean; onClick: () => void }) {
  const t = useT();
  const off = busy || avail.count === 0;
  const button = (
    <button type="button" disabled={off} onClick={onClick} className={barButton}>
      <IconChip icon={meta.icon} tone={meta.tone} />
      {t(meta.label)}
      {count && avail.count > 0 && <span className="font-mono text-xs font-medium text-muted tabular-nums">· {avail.count}</span>}
    </button>
  );
  if (!off || busy || !avail.why) return button;
  return (
    <Tip label={avail.why}>
      <span tabIndex={0} className="flex rounded-ctl outline-none focus-visible:ring-2 focus-visible:ring-accent">
        {button}
      </span>
    </Tip>
  );
}

/** "Check ▾" and "More ▾": they open upwards, each row its chip, its name, how many it takes, or why none. */
export function ActionMenu<A extends string>({
  label,
  icon,
  tone,
  items,
  note,
  busy,
  iconOnly,
  align,
  onAction,
}: {
  label: string;
  icon?: IconName;
  tone?: Tone;
  items: MenuEntry<A>[];
  /** On a phone the bar has no room for it: a line above the rows (the selection wider than the list says so here). */
  note?: string;
  busy: boolean;
  iconOnly?: boolean;
  align: "start" | "end";
  onAction: (a: A) => void;
}) {
  return (
    <Menu.Root>
      <Menu.Trigger aria-label={iconOnly ? label : undefined} className={cx(barButton, iconOnly && "w-10 justify-center px-0")}>
        {icon && tone ? <IconChip icon={icon} tone={tone} /> : iconOnly && <Icon name="ellipsis" size={16} className="text-muted" />}
        {!iconOnly && (
          <>
            {label}
            <Icon name="chevronDown" size={12} className="text-muted" />
          </>
        )}
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner side="top" align={align} sideOffset={12} collisionPadding={14} className="z-50 outline-none">
          <Menu.Popup className={cx(selectPopupClass, "w-max max-w-[calc(100vw-28px)] min-w-60")}>
            {note && <p className="px-2.5 py-1.5 text-[11px] font-medium text-muted">{note}</p>}
            {items.map((x, i) => (x ? <ActionItem key={x.id} entry={x} busy={busy} onAction={onAction} /> : <Menu.Separator key={`sep${i}`} className="mx-2.5 my-1 h-px bg-line" />))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

function ActionItem<A extends string>({ entry, busy, onAction }: { entry: NonNullable<MenuEntry<A>>; busy: boolean; onAction: (a: A) => void }) {
  const t = useT();
  const { meta, avail } = entry;
  const off = busy || avail.count === 0;
  return (
    <Menu.Item disabled={off} onClick={() => onAction(entry.id)} className={cx(selectItemClass, "gap-2.5 data-disabled:cursor-default")}>
      <IconChip icon={meta.icon} tone={meta.tone} className={off ? "opacity-45" : undefined} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className={off ? "text-muted" : meta.danger ? "text-danger-text" : undefined}>{t(meta.label)}</span>
        {off && !busy && avail.why && <span className="max-w-64 text-[11px] leading-snug font-medium text-pretty text-muted">{avail.why}</span>}
      </span>
      {!off && <span className="ps-3 font-mono text-xs font-medium text-muted tabular-nums">{avail.count}</span>}
    </Menu.Item>
  );
}

// ---------------------------------------------------------------------------------------------------
// A batch, row by row

const stateLook: Record<Row["state"], { kind: StatusKind; word: MessageKey }> = {
  waiting: { kind: "off", word: "bulk.state.waiting" },
  running: { kind: "busy", word: "bulk.state.running" },
  ok: { kind: "ok", word: "bulk.state.ok" },
  bad: { kind: "bad", word: "bulk.state.bad" },
  skipped: { kind: "off", word: "bulk.state.skipped" },
  notStarted: { kind: "off", word: "bulk.state.notStarted" },
};

/** What the head of a batch says: its chip, its title while it runs, its summary at the end, a hint under a failure. */
export type PanelHead = {
  icon: IconName;
  tone: Tone;
  title: string;
  /** The summary: of `n` rows (the skipped included) `ok` came out. */
  done: (n: number, ok: number) => string;
  /** A line under the summary when something failed. */
  hint?: string;
};

/**
 * The batch above the bar: what runs and how far ("SSH password change · 3 of 7", Stop), a bar that fills, then a row per
 * item (name, what came of it, "Open" to its card). At the end the summary takes the head, with Retry for the failed and
 * the never-started, `extra` (a button of the page's own) and Close; only the summary is read out. With `fold`, a batch
 * of more rows than that shows counters in place of the rows that went well, and keeps the failed ones in sight.
 */
export function BatchPanel({
  job,
  rows,
  head,
  open,
  fold,
  extra,
  onStop,
  onRetry,
  onClose,
}: {
  job: Job;
  rows: readonly Row[];
  head: PanelHead;
  /** The row's own page, when it has one to show (an "Open" link). */
  open?: (row: Row) => RowLink | null;
  fold?: number;
  extra?: ReactNode;
  onStop: () => void;
  onRetry: () => void;
  onClose: () => void;
}) {
  const t = useT();
  const [shown, setShown] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setShown(true));
    return () => cancelAnimationFrame(id);
  }, []);
  const count = (s: Row["state"]) => rows.filter((r) => r.state === s).length;
  const [ok, bad, skipped, notStarted, running, waiting] = [count("ok"), count("bad"), count("skipped"), count("notStarted"), count("running"), count("waiting")];
  const total = rows.length - skipped;
  const done = ok + bad + notStarted;
  const again = bad + notStarted;
  const finished = !job.running;
  const folded = !!fold && rows.length > fold;
  const list = folded ? rows.filter((r) => r.state === "bad") : rows;
  return (
    <div className="grid transition-[grid-template-rows] duration-[250ms] ease-out-soft" style={{ gridTemplateRows: shown ? "1fr" : "0fr" }}>
      <div className="min-h-0 overflow-hidden">
        <div data-batch className="@container flex max-h-[min(60dvh,520px)] flex-col border-b border-line">
          {/* the title's line is the chip's height: they line up with no nudge, and the hint falls under the title */}
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
            <div className="flex min-w-0 flex-[1_1_240px] items-start gap-2.5">
              <IconChip icon={head.icon} tone={head.tone} size={20} />
              <div className="flex min-w-0 flex-col">
                {!finished && (
                  <p className="min-w-0 text-[13px] leading-5 font-bold">
                    {head.title} <span className="font-mono text-xs font-medium text-muted tabular-nums">· {t("bulk.progress", { done, n: total })}</span>
                  </p>
                )}
                <p aria-live="polite" className={finished ? "min-w-0 text-[13px] leading-5 font-bold text-pretty" : "sr-only"}>
                  {finished && (
                    <>
                      {head.done(rows.length, ok)}
                      {[bad && t.n("bulk.sum.bad", bad), skipped && t.n("bulk.sum.skipped", skipped), notStarted && t.n("bulk.sum.notStarted", notStarted)]
                        .filter(Boolean)
                        .map((part) => (
                          <span key={String(part)} className="font-medium text-muted">
                            {" · "}
                            {part}
                          </span>
                        ))}
                    </>
                  )}
                </p>
                {finished && bad > 0 && head.hint && <p className="text-xs leading-snug text-pretty text-muted">{head.hint}</p>}
              </div>
            </div>
            <div className="ms-auto flex flex-none flex-wrap gap-2">
              {!finished && (
                <Button variant="secondary" size="sm" disabled={job.stopping} onClick={onStop}>
                  {job.stopping ? t("bulk.stopping") : t("bulk.stop")}
                </Button>
              )}
              {finished && again > 0 && (job.spec.call || job.spec.many) && (
                <Button variant="secondary" size="sm" onClick={onRetry}>
                  <Icon name="refresh" size={12} />
                  {t("bulk.retry", { n: again })}
                </Button>
              )}
              {finished && extra}
              {finished && (
                <Button variant="ghost" size="sm" onClick={onClose}>
                  {t("common.close")}
                </Button>
              )}
            </div>
          </div>
          {!finished && (
            <div role="progressbar" aria-label={head.title} aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} className="mx-4 mb-1 h-1 flex-none overflow-hidden rounded-full bg-surface-2">
              <div className="h-full origin-left rounded-full bg-accent transition-transform duration-300 ease-out-soft rtl:origin-right" style={{ transform: `scaleX(${total > 0 ? Math.max(0.03, done / total) : 0})` }} />
            </div>
          )}
          {folded && !finished && (
            <p data-fold className="flex flex-wrap items-center gap-x-4 gap-y-1 px-4 pt-1 pb-3 text-xs font-medium text-muted tabular-nums">
              {(
                [
                  [ok, "ok", t("bulk.fold.done", { n: ok })],
                  [running, "busy", t("bulk.fold.running", { n: running })],
                  [waiting, "off", t("bulk.fold.waiting", { n: waiting })],
                  [skipped, "off", t.n("bulk.sum.skipped", skipped)],
                  [notStarted, "off", t.n("bulk.sum.notStarted", notStarted)],
                ] as const
              )
                .filter(([n]) => n > 0)
                .map(([, kind, text]) => (
                  <span key={text} className="inline-flex items-center gap-1.5 whitespace-nowrap">
                    <StatusDot kind={kind} />
                    {text}
                  </span>
                ))}
            </p>
          )}
          {list.length > 0 && (
            <ul className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_auto] content-start gap-x-3 overflow-y-auto overscroll-contain px-4 pb-1 @lg:grid-cols-[minmax(0,13rem)_auto_minmax(0,1fr)]">
              {list.map((r) => (
                <BatchRow key={r.id} row={r} link={open?.(r) ?? null} />
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  );
}

function BatchRow({ row, link }: { row: Row; link: RowLink | null }) {
  const t = useT();
  const look = stateLook[row.state];
  const ok = row.state === "ok";
  const kind = ok ? (row.kind ?? look.kind) : look.kind;
  const label = ok ? (row.label ?? t(look.word)) : t(look.word);
  return (
    <li className="col-span-full grid grid-cols-subgrid items-center gap-y-0.5 border-t border-line py-2.5 first:border-t-0">
      <div className="flex min-w-0 items-baseline gap-2">
        <b className="truncate text-[13px] font-bold">
          <bdi>{row.name}</bdi>
        </b>
        {row.host && (
          <span dir="ltr" className="min-w-0 truncate font-mono text-xs text-muted">
            {row.host}
          </span>
        )}
      </div>
      <span className="flex justify-self-end @lg:justify-self-start">
        <StatusPill sm kind={kind} label={label} />
      </span>
      {(row.note || link) && (
        <p className="col-span-full flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs leading-snug @lg:col-span-1">
          {row.note && <span className={cx("min-w-0 text-pretty", row.state === "bad" ? "text-danger-text" : "text-muted")}>{row.note}</span>}
          {link && (
            <Link to={link.to as never} params={link.params as never} search={link.search as never} className="font-semibold whitespace-nowrap text-fg underline decoration-faint decoration-dotted underline-offset-2 transition-colors duration-150 hover:decoration-fg">
              {t("bulk.open")}
            </Link>
          )}
        </p>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------------------------------
// Quick picks

/** One row of "Select ▾": its words and the ids it picks; no ids yet (still being collected) reads "…" and cannot be chosen. */
export type QuickPick = { label: string; ids?: readonly string[] } | null;

/**
 * "Select ▾" by the filters: every pick replaces the selection, and the last one clears it. `null` is a divider. `head` is a
 * line above the rows; `onOpenChange` lets a page start collecting what its picks need only once the menu opens.
 */
export function PickMenu({ picks, any, head, onPick, onOpenChange }: { picks: readonly QuickPick[]; any: boolean; head?: string; onPick: (ids: readonly string[]) => void; onOpenChange?: (open: boolean) => void }) {
  const t = useT();
  return (
    <Menu.Root onOpenChange={(o) => onOpenChange?.(o)}>
      <Menu.Trigger className="flex h-9 flex-none items-center gap-2 rounded-ctl border border-line bg-surface px-3 text-xs font-medium text-fg outline-none transition-colors duration-200 focus-visible:border-accent data-popup-open:border-accent-line">
        <Icon name="check" size={12} className="text-muted" />
        {t("bulk.pick")}
        <Icon name="chevronDown" size={12} className="text-muted" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="start" className="z-50 outline-none">
          <Menu.Popup aria-busy={picks.some((p) => p && !p.ids) || undefined} className={cx(selectPopupClass, "min-w-52")}>
            {head && <p className="px-2.5 py-1.5 text-[11px] font-medium text-muted">{head}</p>}
            {picks.map((p, i) =>
              p ? (
                <Menu.Item key={p.label} disabled={!p.ids || p.ids.length === 0} onClick={() => p.ids && onPick(p.ids)} className={cx(selectItemClass, "justify-between gap-4 data-disabled:cursor-default data-disabled:text-muted")}>
                  {p.label}
                  <span className="font-mono text-xs font-medium text-muted tabular-nums">{p.ids ? p.ids.length : "…"}</span>
                </Menu.Item>
              ) : (
                <Menu.Separator key={`sep${i}`} className="mx-2.5 my-1 h-px bg-line" />
              ),
            )}
            <Menu.Separator className="mx-2.5 my-1 h-px bg-line" />
            <Menu.Item disabled={!any} onClick={() => onPick([])} className={cx(selectItemClass, "data-disabled:cursor-default data-disabled:text-muted")}>
              {t("bulk.clear")}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

// ---------------------------------------------------------------------------------------------------
// Under a confirm

/** The names of a list, each its own direction; past `max`, "and 12 more". */
function Names({ list, max }: { list: readonly { id: string; name: string }[]; max?: number }) {
  const t = useT();
  // the Arabic comma between names in a right-to-left sentence: a Latin comma there ends up on the wrong side of the name
  const comma = dirOf(useLang()) === "rtl" ? "، " : ", ";
  const cut = max !== undefined && list.length > max + 1 ? max : list.length;
  return (
    <>
      {list.slice(0, cut).map((x, i) => (
        <span key={x.id}>
          {i > 0 && comma}
          <bdi>{x.name}</bdi>
        </span>
      ))}
      {cut < list.length && <span> {t("bulk.list.more", { n: list.length - cut })}</span>}
    </>
  );
}

/**
 * Who takes part and who sits out, under a confirm: the names (each its own direction; with `max`, the first ones and
 * "and N more"), the skipped grouped by their reason. `title` heads the first list ("Nodes (3)").
 */
export function TargetList({ title, note, targets, skipped, max }: { title?: string; note?: string; targets?: readonly Target[]; skipped: readonly Skipped[]; max?: number }) {
  const t = useT();
  // the skipped grouped by their reason: "de2, fi2 — Logs in by key only"
  const reasons = new Map<string, Skipped[]>();
  for (const s of skipped) reasons.set(s.note, [...(reasons.get(s.note) ?? []), s]);
  return (
    <div className="flex flex-col gap-2 rounded-field border border-line bg-surface-2 px-3.5 py-3 text-xs leading-snug">
      {targets && (
        <p className="text-pretty">
          <b className="font-bold text-fg">{title}</b>
          {note && <span className="text-muted"> · {note}</span>}
          <span className="block text-muted">{targets.length > 0 ? <Names list={targets} max={max} /> : "—"}</span>
        </p>
      )}
      {skipped.length > 0 && (
        <div className={cx("flex flex-col gap-0.5", targets && "border-t border-line pt-2")}>
          <b className="font-bold text-fg">{t("bulk.list.skipped", { n: skipped.length })}</b>
          {[...reasons].map(([why, list]) => (
            <span key={why} className="text-pretty text-muted">
              <Names list={list} max={max} /> — {why}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

/** Cancel and the confirming button of a window. */
export function Footer({ go, label, danger, disabled, onClose }: { go: () => void; label: string; danger?: boolean; disabled?: boolean; onClose: () => void }) {
  const t = useT();
  return (
    <>
      <Button variant="ghost" size="md" onClick={onClose}>
        {t("common.cancel")}
      </Button>
      <Button variant={danger ? "danger" : "primary"} size="md" disabled={disabled} onClick={go}>
        {label}
      </Button>
    </>
  );
}

// ---------------------------------------------------------------------------------------------------
// Esc and leaving

/** Esc clears the selection while there is one (not while typing, nor over a menu or a window). */
export function useEscClear(active: boolean, onClear: () => void) {
  useEffect(() => {
    if (!active) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if ((e.target as Element | null)?.closest?.("textarea, select, [contenteditable=true], input:not([type=checkbox]):not([type=radio])")) return;
      if (document.querySelector("[role=dialog], [role=menu]")) return;
      onClear();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [active, onClear]);
}

/**
 * A batch in flight is not left by accident: "Leave" stops it after the call in progress. With `leaves`, only a move for
 * which it answers true asks (a page that keeps its own state across paging and sorting); the browser's own "leave the
 * page" prompt always does while it runs.
 */
export function LeaveGuard({
  active,
  leaves,
  title,
  body,
  onLeave,
}: {
  active: boolean;
  leaves?: (next: { fullPath: string; search: Record<string, unknown> }) => boolean;
  title: string;
  body: string;
  onLeave: () => void;
}) {
  const t = useT();
  const running = useRef(false);
  const leaving = useRef(leaves);
  useEffect(() => {
    running.current = active;
    leaving.current = leaves;
  }, [active, leaves]);
  const blocker = useBlocker({
    shouldBlockFn: ({ next }) => running.current && (leaving.current?.({ fullPath: String(next.fullPath), search: (next.search ?? {}) as Record<string, unknown> }) ?? true),
    enableBeforeUnload: () => running.current,
    withResolver: true,
  });
  return (
    <Modal
      open={blocker.status === "blocked"}
      onOpenChange={(o) => !o && blocker.reset?.()}
      title={title}
      description={body}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={() => blocker.reset?.()}>
            {t("bulk.leave.stay")}
          </Button>
          <Button
            variant="danger"
            size="md"
            onClick={() => {
              onLeave();
              blocker.proceed?.();
            }}
          >
            {t("bulk.leave.go")}
          </Button>
        </>
      }
    />
  );
}
