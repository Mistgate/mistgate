import { Menu } from "@base-ui/react/menu";
import { Link, useBlocker } from "@tanstack/react-router";
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { selectItemClass, selectPopupClass } from "@/components/ui/select";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useLang, useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import type { Batch, Job, Row } from "@/lib/batch";
import { maxBulkNodes } from "@/lib/decoy";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import type { Plain } from "@/lib/plain";
import { actionMeta, AgentDialog, availability, BindDialog, decoyOutcome, PasswordDialog, UnbindDialog, type ActionId, type BulkRun } from "./nodes-bulk-actions";
import { DecoyBulkDialog } from "./nodes-decoy";

// The bulk bar of the Nodes page (owner only): it floats at the foot of the content while nodes are selected, in the look of
// the phone's dock (on a phone it takes the dock's place), and grows upwards into the rows of a running batch. Checks
// start at once; the actions with consequences (passwords, keys, the decoy site, the agent) ask first. The rules of who
// takes part, the calls and the confirms are in nodes-bulk-actions.tsx; running them in lib/batch.ts.

type Node = Plain<NodeMsg>;

export type Avail = { count: number; why?: string };

/** Start at once: nothing to type, nothing that cannot be run again. */
const instant: ReadonlySet<ActionId> = new Set(["ports", "doctor", "panelKey"]);

/**
 * The selected nodes and what can be done with them, floated over the page (a portal on the body), plus the room at the
 * page's foot so the bar never covers the last row. Esc clears the selection (not while typing, nor over a menu or a
 * window); leaving the page while a batch runs asks first.
 */
export function NodesBulk({ nodes, picked, shown, batch, run, onClear }: { nodes: readonly Node[]; picked: readonly Node[]; shown: ReadonlySet<string>; batch: Batch; run: BulkRun; onClear: () => void }) {
  const t = useT();
  const [dialog, setDialog] = useState<ActionId | null>(null);
  const agentOff = useEdition().off("agent_updates");
  const job = batch.job;
  const hidden = new Set(picked.filter((n) => !shown.has(n.id)).map((n) => n.id));

  useEffect(() => {
    if (picked.length === 0) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if ((e.target as Element | null)?.closest?.("textarea, select, [contenteditable=true], input:not([type=checkbox]):not([type=radio])")) return;
      if (document.querySelector("[role=dialog], [role=menu]")) return;
      onClear();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [picked.length, onClear]);

  // a batch in flight is not left by accident: "Leave" stops it after the node in progress
  const running = useRef(false);
  useEffect(() => {
    running.current = batch.running;
  }, [batch.running]);
  const blocker = useBlocker({ shouldBlockFn: () => running.current, enableBeforeUnload: () => running.current, withResolver: true });

  const avail = Object.fromEntries((Object.keys(actionMeta) as ActionId[]).map((a) => [a, availability(a, picked, run.ctx, t)])) as Record<ActionId, Avail>;
  const act = (a: ActionId) => (instant.has(a) ? void run.run(a, picked) : setDialog(a));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  // the decoy change is one call: its rows read how each node is doing from the live list
  const rows = job ? job.rows.map((r) => (job.spec.action === "decoy" && r.state === "ok" ? { ...r, ...decoyOutcome(t, byId.get(r.id)) } : r)) : [];
  const close = () => setDialog(null);
  const props = { nodes: picked, hidden, run, onClose: close };

  return (
    <>
      {(picked.length > 0 || job) && (
        <>
          <div aria-hidden className="h-20 flex-none" />
          {createPortal(
            <BulkDock wide={!!job}>
              {job && <BatchPanel job={job} rows={rows} onStop={batch.stop} onRetry={batch.retry} onClose={batch.close} />}
              {picked.length > 0 && (
                <BulkControls count={picked.length} hidden={hidden.size} avail={avail} agentOff={agentOff} busy={batch.running} onAction={act} onClear={onClear} />
              )}
            </BulkDock>,
            document.body,
          )}
        </>
      )}
      {dialog === "password" && <PasswordDialog {...props} />}
      {dialog === "bindKey" && <BindDialog {...props} />}
      {dialog === "unbindKey" && <UnbindDialog {...props} />}
      {dialog === "agent" && <AgentDialog nodes={picked} run={run} onClose={close} />}
      {dialog === "decoy" && <DecoyBulkDialog nodes={picked} onClose={close} onDone={() => void run.run("decoy", picked)} />}
      <Modal
        open={blocker.status === "blocked"}
        onOpenChange={(o) => !o && blocker.reset?.()}
        title={t("bulk.leave.title")}
        description={t("bulk.leave.body")}
        footer={
          <>
            <Button variant="ghost" size="md" onClick={() => blocker.reset?.()}>
              {t("bulk.leave.stay")}
            </Button>
            <Button
              variant="danger"
              size="md"
              onClick={() => {
                batch.stop();
                blocker.proceed?.();
              }}
            >
              {t("bulk.leave.go")}
            </Button>
          </>
        }
      />
    </>
  );
}

/**
 * The dock's material: translucent, blurred, the dock radius and its one shadow. Floating, it sits at the foot of the
 * content (beside the sidebar on a desktop, in the dock's place on a phone); `float={false}` draws it in place (dev kit).
 */
export function BulkDock({ float = true, wide, children }: { float?: boolean; wide?: boolean; children: ReactNode }) {
  const t = useT();
  const frame = (
    <div
      role="region"
      aria-label={t("bulk.region")}
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
 * "Update agent" and the password go into "More". Measured, not guessed, since the words differ by language; unknown
 * (no layout, as in tests) reads as wide.
 */
function useNarrow(ref: RefObject<HTMLElement | null>): boolean {
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

const barButton =
  "inline-flex h-10 flex-none items-center gap-2 rounded-ctl px-2.5 text-[13px] font-bold whitespace-nowrap text-fg outline-none transition-[background-color,transform] duration-150 ease-out-soft hover:bg-surface-2 active:scale-[0.97] focus-visible:ring-2 focus-visible:ring-accent data-popup-open:bg-surface-2 disabled:pointer-events-none disabled:opacity-35";

/** The bar's row: clear, the count, then Check ▾, Update agent, Change password and More ▾. */
export function BulkControls({
  count,
  hidden,
  avail,
  agentOff,
  busy,
  onAction,
  onClear,
}: {
  count: number;
  hidden: number;
  avail: Record<ActionId, Avail>;
  agentOff: boolean;
  /** A batch runs: a second one waits for it. */
  busy: boolean;
  onAction: (a: ActionId) => void;
  onClear: () => void;
}) {
  const t = useT();
  const ref = useRef<HTMLDivElement>(null);
  const narrow = useNarrow(ref);
  const item = (a: ActionId) => ({ action: a, avail: avail[a] });
  const more = [
    ...(narrow ? [...(agentOff ? [] : [item("agent")]), item("password"), null] : []),
    item("panelKey"),
    item("bindKey"),
    item("unbindKey"),
    null,
    item("decoy"),
  ];
  return (
    <div ref={ref} className="flex h-[60px] flex-none items-center gap-1 p-2.5">
      <button type="button" aria-label={t("bulk.clear")} title={`${t("bulk.clear")} (Esc)`} onClick={onClear} className={cx(barButton, "w-10 justify-center px-0 text-muted hover:text-fg")}>
        <Icon name="x" size={16} />
      </button>
      {/* wide, the words keep their room (so the row's measure is honest); narrow, the count takes what is left */}
      <div className={cx("flex items-center gap-2", narrow ? "min-w-0 flex-1" : "flex-none pe-1")}>
        <span className="inline-flex h-6 min-w-6 flex-none items-center justify-center rounded-full bg-accent-soft px-2 font-mono text-xs font-bold text-accent-text tabular-nums">{count}</span>
        <span className="truncate text-[13px] font-semibold whitespace-nowrap">{t.n(narrow ? "bulk.selectedShort" : "bulk.selected", count)}</span>
        {!narrow && hidden > 0 && <span className="text-xs whitespace-nowrap text-muted">· {t.n("bulk.hidden", hidden)}</span>}
        {!narrow && count >= maxBulkNodes && <span className="text-xs whitespace-nowrap text-muted">· {t("bulk.limit")}</span>}
      </div>
      <span aria-hidden className={cx("h-6 w-px flex-none bg-line", narrow ? "mx-0.5" : "mx-1")} />
      <ActionMenu label={t("bulk.check")} icon="pulse" tone="sky" items={[item("ports"), item("doctor")]} hidden={narrow ? hidden : 0} busy={busy} onAction={onAction} align="start" />
      {!narrow && !agentOff && <BarAction action="agent" avail={avail.agent} busy={busy} onAction={onAction} count />}
      {!narrow && <BarAction action="password" avail={avail.password} busy={busy} onAction={onAction} />}
      <ActionMenu label={t("bulk.more")} items={more} hidden={narrow ? hidden : 0} busy={busy} onAction={onAction} iconOnly={narrow} align="end" />
    </div>
  );
}

/** A direct action of the bar: its section chip, its name, how many it takes; off with the reason in a tooltip when none. */
function BarAction({ action, avail, busy, count, onAction }: { action: ActionId; avail: Avail; busy: boolean; count?: boolean; onAction: (a: ActionId) => void }) {
  const t = useT();
  const m = actionMeta[action];
  const off = busy || avail.count === 0;
  const button = (
    <button type="button" disabled={off} onClick={() => onAction(action)} className={barButton}>
      <IconChip icon={m.icon} tone={m.tone} />
      {t(m.label)}
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

type MenuEntry = { action: ActionId; avail: Avail } | null;

/** "Check ▾" and "More ▾": they open upwards, each row its chip, its name, how many it takes, or why none. */
function ActionMenu({ label, icon, tone, items, hidden, busy, iconOnly, align, onAction }: { label: string; icon?: IconName; tone?: Tone; items: MenuEntry[]; hidden: number; busy: boolean; iconOnly?: boolean; align: "start" | "end"; onAction: (a: ActionId) => void }) {
  const t = useT();
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
            {/* on a phone the bar has no room for it: the selection wider than the list says so here */}
            {hidden > 0 && <p className="px-2.5 py-1.5 text-[11px] font-medium text-muted">{t.n("bulk.hidden", hidden)}</p>}
            {items.map((x, i) => (x ? <ActionItem key={x.action} action={x.action} avail={x.avail} busy={busy} onAction={onAction} /> : <Menu.Separator key={`sep${i}`} className="mx-2.5 my-1 h-px bg-line" />))}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

function ActionItem({ action, avail, busy, onAction }: { action: ActionId; avail: Avail; busy: boolean; onAction: (a: ActionId) => void }) {
  const t = useT();
  const m = actionMeta[action];
  const off = busy || avail.count === 0;
  return (
    <Menu.Item disabled={off} onClick={() => onAction(action)} className={cx(selectItemClass, "gap-2.5 data-disabled:cursor-default")}>
      <IconChip icon={m.icon} tone={m.tone} className={off ? "opacity-45" : undefined} />
      <span className="flex min-w-0 flex-1 flex-col">
        <span className={off ? "text-muted" : m.danger ? "text-danger-text" : undefined}>{t(m.label)}</span>
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

/**
 * The batch above the bar: what runs and how far ("SSH password change · 3 of 7", Stop), a bar that fills, then a row per
 * node (name, host, state, what came of it, "Open" to its card). At the end the summary takes the head, with Retry for
 * the failed and the never-started, and Close; only the summary is read out.
 */
export function BatchPanel({ job, rows, onStop, onRetry, onClose }: { job: Job; rows: readonly Row[]; onStop: () => void; onRetry: () => void; onClose: () => void }) {
  const t = useT();
  const m = actionMeta[job.spec.action as ActionId];
  const [open, setOpen] = useState(false);
  useEffect(() => {
    const id = requestAnimationFrame(() => setOpen(true));
    return () => cancelAnimationFrame(id);
  }, []);
  const count = (s: Row["state"]) => rows.filter((r) => r.state === s).length;
  const [ok, bad, skipped, notStarted] = [count("ok"), count("bad"), count("skipped"), count("notStarted")];
  const total = rows.length - skipped;
  const done = ok + bad + notStarted;
  const again = bad + notStarted;
  const finished = !job.running;
  return (
    <div className="grid transition-[grid-template-rows] duration-[250ms] ease-out-soft" style={{ gridTemplateRows: open ? "1fr" : "0fr" }}>
      <div className="min-h-0 overflow-hidden">
        <div data-batch className="@container flex max-h-[min(60dvh,520px)] flex-col border-b border-line">
          {/* the title's line is the chip's height: they line up with no nudge, and the hint falls under the title */}
          <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
            <div className="flex min-w-0 flex-[1_1_240px] items-start gap-2.5">
              <IconChip icon={m.icon} tone={m.tone} size={20} />
              <div className="flex min-w-0 flex-col">
                {!finished && (
                  <p className="min-w-0 text-[13px] leading-5 font-bold">
                    {t(m.title)} <span className="font-mono text-xs font-medium text-muted tabular-nums">· {t("bulk.progress", { done, n: total })}</span>
                  </p>
                )}
                <p aria-live="polite" className={finished ? "min-w-0 text-[13px] leading-5 font-bold text-pretty" : "sr-only"}>
                  {finished && (
                    <>
                      {t.n(m.done, rows.length, { ok })}
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
                {finished && bad > 0 && m.ssh && <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.retryHint")}</p>}
              </div>
            </div>
            <div className="ms-auto flex flex-none flex-wrap gap-2">
              {!finished && (
                <Button variant="secondary" size="sm" disabled={job.stopping} onClick={onStop}>
                  {job.stopping ? t("bulk.stopping") : t("bulk.stop")}
                </Button>
              )}
              {finished && again > 0 && job.spec.call && (
                <Button variant="secondary" size="sm" onClick={onRetry}>
                  <Icon name="refresh" size={12} />
                  {t("bulk.retry", { n: again })}
                </Button>
              )}
              {finished && (
                <Button variant="ghost" size="sm" onClick={onClose}>
                  {t("common.close")}
                </Button>
              )}
            </div>
          </div>
          {!finished && (
            <div role="progressbar" aria-label={t(m.title)} aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} className="mx-4 mb-1 h-1 flex-none overflow-hidden rounded-full bg-surface-2">
              <div className="h-full origin-left rounded-full bg-accent transition-transform duration-300 ease-out-soft rtl:origin-right" style={{ transform: `scaleX(${total > 0 ? Math.max(0.03, done / total) : 0})` }} />
            </div>
          )}
          <ul className="grid min-h-0 flex-1 grid-cols-[minmax(0,1fr)_auto] content-start gap-x-3 overflow-y-auto overscroll-contain px-4 pb-1 @lg:grid-cols-[minmax(0,13rem)_auto_minmax(0,1fr)]">
            {rows.map((r) => (
              <BatchRow key={r.id} row={r} link={m.link} />
            ))}
          </ul>
        </div>
      </div>
    </div>
  );
}

function BatchRow({ row, link }: { row: Row; link?: (typeof actionMeta)[ActionId]["link"] }) {
  const t = useT();
  const look = stateLook[row.state];
  const ok = row.state === "ok";
  const kind = ok ? (row.kind ?? look.kind) : look.kind;
  const label = ok ? (row.label ?? t(look.word)) : t(look.word);
  const open = ok && row.link && link;
  return (
    <li className="col-span-full grid grid-cols-subgrid items-center gap-y-0.5 border-t border-line py-2.5 first:border-t-0">
      <div className="flex min-w-0 items-baseline gap-2">
        <b className="truncate text-[13px] font-bold">
          <bdi>{row.name}</bdi>
        </b>
        <span dir="ltr" className="min-w-0 truncate font-mono text-xs text-muted">
          {row.host}
        </span>
      </div>
      <span className="flex justify-self-end @lg:justify-self-start">
        <StatusPill sm kind={kind} label={label} />
      </span>
      {(row.note || open) && (
        <p className="col-span-full flex min-w-0 flex-wrap items-baseline gap-x-2 text-xs leading-snug @lg:col-span-1">
          {row.note && <span className={cx("min-w-0 text-pretty", row.state === "bad" ? "text-danger-text" : "text-muted")}>{row.note}</span>}
          {open && (
            <Link to="/nodes/$id" params={{ id: row.id }} search={link} className="font-semibold whitespace-nowrap text-fg underline decoration-faint decoration-dotted underline-offset-2 transition-colors duration-150 hover:decoration-fg">
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

export type QuickPick = { label: MessageKey; ids: readonly string[] };

/** "Select ▾" by the filters: every pick replaces the selection (at most 50), and the last one clears it. */
export function PickMenu({ picks, any, onPick }: { picks: readonly QuickPick[]; any: boolean; onPick: (ids: readonly string[]) => void }) {
  const t = useT();
  return (
    <Menu.Root>
      <Menu.Trigger className="flex h-9 flex-none items-center gap-2 rounded-ctl border border-line bg-surface px-3 text-xs font-medium text-fg outline-none transition-colors duration-200 focus-visible:border-accent data-popup-open:border-accent-line">
        <Icon name="check" size={12} className="text-muted" />
        {t("bulk.pick")}
        <Icon name="chevronDown" size={12} className="text-muted" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="start" className="z-50 outline-none">
          <Menu.Popup className={cx(selectPopupClass, "min-w-52")}>
            {picks.map((p) => (
              <Menu.Item key={p.label} disabled={p.ids.length === 0} onClick={() => onPick(p.ids)} className={cx(selectItemClass, "justify-between gap-4 data-disabled:cursor-default data-disabled:text-muted")}>
                {t(p.label)}
                <span className="font-mono text-xs font-medium text-muted tabular-nums">{p.ids.length}</span>
              </Menu.Item>
            ))}
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
