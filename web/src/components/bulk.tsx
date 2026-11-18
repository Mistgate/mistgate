import { Menu } from "@base-ui/react/menu";
import { Link, useBlocker } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { selectItemClass, selectPopupClass } from "@/components/ui/select";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { dirOf, useLang, useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Row, Skipped, Target } from "@/lib/batch";
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

export const barButton =
  "inline-flex h-10 flex-none items-center gap-2 rounded-ctl px-2.5 text-[13px] font-bold whitespace-nowrap text-fg outline-none transition-[background-color,transform] duration-150 ease-out-soft hover:bg-surface-2 active:scale-[0.97] focus-visible:ring-2 focus-visible:ring-accent data-popup-open:bg-surface-2 disabled:pointer-events-none disabled:opacity-35";

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

export function BatchRow({ row, link }: { row: Row; link: RowLink | null }) {
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
