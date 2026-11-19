import { useState } from "react";
import { createPortal } from "react-dom";
import { ActionMenu, BatchRow, LeaveGuard, useEscClear, type ActionMeta, type Avail, type MenuEntry, type PanelHead, type RowLink } from "@/components/bulk";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Batch, Job, Row } from "@/lib/batch";
import { maxBulkNodes } from "@/lib/decoy";
import { cx } from "@/lib/cx";
import type { Plain } from "@/lib/plain";
import { actionMeta, AgentDialog, availability, BindDialog, decoyOutcome, panelFor, PasswordDialog, UnbindDialog, type ActionId, type BulkRun } from "./nodes-bulk-actions";
import { DecoyBulkDialog } from "./nodes-decoy";

// The bulk bar of the Nodes page (owner only), as the redesign board draws it (fz-bulk): over the foot of the page while nodes
// are selected, with the flags of the first ones, how many, "select all" under it, the four things done most as buttons with
// the number of nodes each takes, a menu of the rest, and a cross. A batch takes the actions' place: a spinner, its title,
// a dot per node, "2 of 4"; when it ends, the summary and "Got it"; a list of the nodes with what came of each opens under
// it by itself when something did not go well. Checks start at once; the actions with consequences (passwords, keys, the
// decoy site, the agent) ask first. The rules of who takes part, the calls and the confirms are in nodes-bulk-actions.tsx;
// running them in lib/batch.ts.

type Node = Plain<NodeMsg>;

/** Start at once: nothing to type, nothing that cannot be run again. */
const instant: ReadonlySet<ActionId> = new Set(["ports", "doctor", "panelKey"]);

// the glyphs of the board's buttons (24 grid, stroke 1.5)
const glyph: Partial<Record<ActionId, string>> = {
  ports: "M9 3v5M15 3v5M6 8h12v3a6 6 0 0 1-12 0zM12 17v4",
  doctor: "M3 12h4l2.5-6 4 12 2.5-6h5",
  panelKey: "M8 15a4 4 0 1 1 3.9-5H21v3h-2v2h-3v-2h-4.1A4 4 0 0 1 8 15z",
  agent: "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4",
};
/** The buttons of the bar, the rest are in its menu. */
const direct: ActionId[] = ["ports", "doctor", "panelKey", "agent"];

/**
 * The selected nodes and what can be done with them, floated over the page (a portal on the body), plus the room at the
 * page's foot so the bar never covers the last row. Esc clears the selection (not while typing, nor over a menu or a
 * window); leaving the page while a batch runs asks first.
 */
export function NodesBulk({
  nodes,
  picked,
  shown,
  batch,
  run,
  onClear,
  onAll,
  allShown,
  total,
}: {
  nodes: readonly Node[];
  picked: readonly Node[];
  shown: ReadonlySet<string>;
  batch: Batch;
  run: BulkRun;
  onClear: () => void;
  /** "Select all N" (every shown node), or clear them when all of them are ticked. */
  onAll: () => void;
  allShown: boolean;
  /** How many nodes the page shows (the ones that can be ticked). */
  total: number;
}) {
  const t = useT();
  const [dialog, setDialog] = useState<ActionId | null>(null);
  const job = batch.job;
  const hidden = new Set(picked.filter((n) => !shown.has(n.id)).map((n) => n.id));

  useEscClear(picked.length > 0, onClear);

  const avail = Object.fromEntries((Object.keys(actionMeta) as ActionId[]).map((a) => [a, availability(a, picked, run.ctx, t)])) as Record<ActionId, Avail>;
  const act = (a: ActionId) => (instant.has(a) ? void run.run(a, picked) : setDialog(a));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  // the decoy change is one call: its rows read how each node is doing from the live list
  const rows = job ? job.rows.map((r) => (job.spec.action === "decoy" && r.state === "ok" ? { ...r, ...decoyOutcome(t, byId.get(r.id)) } : r)) : [];
  const close = () => setDialog(null);
  const props = { nodes: picked, hidden, run, onClose: close };
  const panel = job ? panelFor(t, job.spec.action as ActionId) : null;
  const show = picked.length > 0 || !!job;

  return (
    <>
      {show && <div aria-hidden className="h-24 flex-none" />}
      {show &&
        createPortal(
          <BulkBar
            nodes={picked}
            hidden={hidden.size}
            allShown={allShown}
            total={total}
            avail={avail}
            busy={batch.running}
            job={job}
            rows={rows}
            panel={panel}
            onAction={act}
            onClear={onClear}
            onAll={onAll}
            onStop={batch.stop}
            onRetry={batch.retry}
            onCloseJob={batch.close}
          />,
          document.body,
        )}
      {dialog === "password" && <PasswordDialog {...props} />}
      {dialog === "bindKey" && <BindDialog {...props} />}
      {dialog === "unbindKey" && <UnbindDialog {...props} />}
      {dialog === "agent" && <AgentDialog nodes={picked} run={run} onClose={close} />}
      {dialog === "decoy" && <DecoyBulkDialog nodes={picked} onClose={close} onDone={() => void run.run("decoy", picked)} />}
      <LeaveGuard active={batch.running} title={t("bulk.leave.title")} body={t("bulk.leave.body")} onLeave={batch.stop} />
    </>
  );
}

/** One round button of the bar: 40px, a glyph in the quiet colour. */
const roundButton = "grid size-10 flex-none place-items-center rounded-field text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg";

export function BulkBar({
  nodes,
  hidden,
  allShown,
  total,
  avail,
  busy,
  job,
  rows,
  panel,
  float = true,
  onAction,
  onClear,
  onAll,
  onStop,
  onRetry,
  onCloseJob,
}: {
  nodes: readonly Pick<Node, "id" | "countryCode">[];
  hidden: number;
  allShown: boolean;
  total: number;
  avail: Record<ActionId, Avail>;
  /** A batch runs: a second one waits for it. */
  busy: boolean;
  job: Job | null;
  rows: readonly Row[];
  panel: { head: PanelHead; open: (row: Row) => RowLink | null } | null;
  /** Floating over the page; false draws it in place (the dev kit). */
  float?: boolean;
  onAction: (a: ActionId) => void;
  onClear: () => void;
  onAll: () => void;
  onStop: () => void;
  onRetry: () => void;
  onCloseJob: () => void;
}) {
  const t = useT();
  const count = nodes.length;
  const finished = !!job && !job.running;
  const done = job ? rows.filter((r) => r.state === "ok" || r.state === "bad" || r.state === "notStarted").length : 0;
  const share = job ? rows.filter((r) => r.state !== "skipped").length : 0;
  const trouble = finished && rows.some((r) => r.state === "bad" || r.state === "skipped" || r.state === "notStarted");
  // the nodes' list is open with the batch (what came of each node is the point of a check); the arrow folds it, for this batch
  // (the choice belongs to the batch it was made in)
  const [opened, setOpened] = useState<{ spec: Job["spec"]; open: boolean } | null>(null);
  const list = (job && opened?.spec === job.spec ? opened.open : null) ?? true;
  const item = (a: ActionId): MenuEntry<ActionId> => ({ id: a, meta: actionMeta[a], avail: avail[a] });
  const more = [item("password"), item("bindKey"), item("unbindKey"), null, item("decoy")];

  const bar = (
    <div
      role="toolbar"
      aria-label={t("bulk.region")}
      className={cx(
        "fz-bulk pointer-events-auto relative isolate flex max-w-full flex-col rounded-[18px] border bg-[color-mix(in_srgb,var(--text)_9%,var(--surface))] shadow-[0_16px_48px_rgb(0_0_0/0.55),0_0_0_1px_rgb(0_0_0/0.3)] max-md:overflow-x-auto max-md:rounded-2xl max-md:[scrollbar-width:none]",
        finished && !trouble ? "border-accent-line" : "border-[color-mix(in_srgb,var(--text)_26%,transparent)]",
      )}
    >
      {job && panel && list && rows.length > 0 && (
        <ul
          data-batch
          className="@container grid max-h-[min(40dvh,360px)] min-h-0 grid-cols-[minmax(0,1fr)_auto] content-start gap-x-3 overflow-y-auto overscroll-contain border-b border-line px-4 pt-1 pb-1 @lg:grid-cols-[minmax(0,13rem)_auto_minmax(0,1fr)]"
        >
          {rows.map((r) => (
            <BatchRow key={r.id} row={r} link={panel.open(r)} />
          ))}
        </ul>
      )}
      <div className="flex min-h-[60px] items-center gap-3 py-2 ps-3 pe-2 max-md:min-h-0 max-md:gap-2 max-md:p-1.5">
        <div className="flex flex-none items-center gap-3 border-e border-[color-mix(in_srgb,var(--text)_18%,transparent)] pe-3 max-md:pe-2">
          <span aria-hidden className="flex ps-2 max-md:hidden">
            {nodes.slice(0, 4).map((n) => (
              <span key={n.id} className="fz-bulk-pop -ms-2 flex rounded-[5px] shadow-[0_0_0_2px_var(--surface-2)]">
                <Flag code={n.countryCode} size={20} className="w-7" />
              </span>
            ))}
          </span>
          <div className="flex flex-col">
            <b className="leading-[18px] font-extrabold whitespace-nowrap">
              {count} {t.n("bulk.selected", count)}
            </b>
            <span className="flex items-baseline gap-1.5 text-xs leading-4 whitespace-nowrap">
              <button type="button" onClick={onAll} className="text-start text-sec underline-offset-[3px] hover:text-fg hover:underline">
                {allShown ? t("nv.bulk.none") : t("nv.bulk.all", { n: total })}
              </button>
              {hidden > 0 && <span className="text-muted max-md:hidden">· {t.n("bulk.hidden", hidden)}</span>}
              {count >= maxBulkNodes && <span className="text-muted max-md:hidden">· {t("bulk.limit")}</span>}
            </span>
          </div>
        </div>
        {!job && (
          <div className="flex gap-1">
            {direct.map((a) => (
              <Act key={a} meta={actionMeta[a]} d={glyph[a]!} avail={avail[a]} busy={busy} onClick={() => onAction(a)} />
            ))}
            <ActionMenu label={t("bulk.more")} items={more} busy={busy} onAction={onAction} iconOnly align="end" />
          </div>
        )}
        {job && !finished && (
          <div className="fz-bulk-in flex h-11 min-w-[360px] items-center gap-3 ps-1 pe-2 max-[1200px]:min-w-0 max-md:min-w-[260px]">
            <span aria-hidden className="size-3.5 flex-none animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-tone-sky border-t-transparent" />
            <b className="font-medium whitespace-nowrap max-md:text-[13px]">{panel?.head.title}</b>
            <Dots rows={rows} />
            <span className="ms-auto font-mono text-xs font-normal text-muted tabular-nums">{t("bulk.progress", { done: Math.min(done + 1, share), n: share })}</span>
            {rows.length > 0 && <DetailsToggle open={list} onClick={() => setOpened({ spec: job.spec, open: !list })} />}
            <Button variant="ghost" size="sm" disabled={job.stopping} onClick={onStop}>
              {job.stopping ? t("bulk.stopping") : t("bulk.stop")}
            </Button>
          </div>
        )}
        {job && finished && panel && <Summary job={job} rows={rows} head={panel.head} list={list} onList={() => setOpened({ spec: job.spec, open: !list })} onRetry={onRetry} onClose={onCloseJob} />}
        <button type="button" aria-label={t("bulk.clear")} title={`${t("bulk.clear")} (Esc)`} onClick={onClear} className={roundButton}>
          <Icon name="x" size={16} />
        </button>
      </div>
    </div>
  );
  if (!float) return bar;
  return (
    <div data-nodes-bar className="pointer-events-none fixed inset-x-3 bottom-[104px] z-20 flex justify-center md:inset-s-(--menu-w) md:inset-e-0 md:bottom-6 md:px-5 rail:px-8">
      {bar}
    </div>
  );
}

/** One of the bar's buttons: its glyph, its name (a tooltip under 1200px, where only the glyph stays) and how many nodes it takes. */
function Act({ meta, d, avail, busy, onClick }: { meta: ActionMeta; d: string; avail: Avail; busy: boolean; onClick: () => void }) {
  const t = useT();
  const off = busy || avail.count === 0;
  const button = (
    <button
      type="button"
      disabled={off}
      aria-label={t(meta.label)}
      onClick={onClick}
      className="group/act inline-flex h-11 flex-none items-center gap-2 rounded-[12px] px-3 font-medium whitespace-nowrap text-fg transition-[background-color,transform] duration-150 ease-out hover:bg-[color-mix(in_srgb,var(--text)_8%,transparent)] active:scale-[0.97] disabled:pointer-events-none disabled:opacity-40 max-md:px-2.5"
    >
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className="block flex-none opacity-80 transition-[transform,color,opacity] duration-[250ms] ease-spring group-hover/act:scale-[1.12] group-hover/act:text-accent-mark group-hover/act:opacity-100">
        <path d={d} />
      </svg>
      <span className="max-[1200px]:hidden">{t(meta.label)}</span>
      {avail.count > 0 && <em className="grid h-5 min-w-5 place-items-center rounded-[10px] bg-[color-mix(in_srgb,var(--text)_14%,transparent)] px-1.5 font-mono text-[11px] font-normal text-fg not-italic">{avail.count}</em>}
    </button>
  );
  // what a button that cannot be pressed says why: the nodes are not fit for it
  if (!busy && avail.count === 0 && avail.why)
    return (
      <Tip label={avail.why}>
        <span tabIndex={0} className="flex rounded-[12px] outline-none focus-visible:ring-2 focus-visible:ring-accent">
          {button}
        </span>
      </Tip>
    );
  return button;
}

/** A dot for each node of a batch: done, the one at work, failed, waiting. */
function Dots({ rows }: { rows: readonly Row[] }) {
  const list = rows.filter((r) => r.state !== "skipped");
  if (list.length > 16) return null;
  return (
    <span aria-hidden className="flex gap-1">
      {list.map((r) => (
        <i
          key={r.id}
          className={cx(
            "size-2 rounded-full transition-[background-color,transform] duration-300 ease-out",
            r.state === "ok" ? "bg-accent-mark" : r.state === "running" ? "scale-[1.4] bg-tone-sky" : r.state === "bad" ? "bg-danger" : "bg-step",
          )}
        />
      ))}
    </span>
  );
}

/** The end of a batch: a mint tick (an amber mark when something failed), what came of it, retry, the nodes' list and "Got it". */
function Summary({ job, rows, head, list, onList, onRetry, onClose }: { job: Job; rows: readonly Row[]; head: PanelHead; list: boolean; onList: () => void; onRetry: () => void; onClose: () => void }) {
  const t = useT();
  const count = (s: Row["state"]) => rows.filter((r) => r.state === s).length;
  const [ok, bad, skipped, notStarted] = [count("ok"), count("bad"), count("skipped"), count("notStarted")];
  const again = bad + notStarted;
  const parts = [bad && t.n("bulk.sum.bad", bad), skipped && t.n("bulk.sum.skipped", skipped), notStarted && t.n("bulk.sum.notStarted", notStarted)].filter(Boolean);
  const kind = bad > 0 ? "warn" : "ok";
  return (
    <div className="fz-bulk-in flex min-h-11 min-w-[360px] items-center gap-3 ps-1 pe-2 max-[1200px]:min-w-0 max-md:min-w-[260px]">
      <span aria-hidden className={cx(`tone-${kind} fz-bulk-pop grid size-6 flex-none place-items-center rounded-full bg-(--c) text-(--c-ink)`)}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <path d={bad > 0 ? "M12 7v6M12 17h.01" : "M5 12.5l4.5 4.5L19 7.5"} />
        </svg>
      </span>
      <div className="flex min-w-0 flex-col">
        <b aria-live="polite" className="font-medium text-pretty max-md:text-[13px]">
          {head.done(rows.length, ok)}
          {parts.map((p) => (
            <span key={String(p)} className="font-normal text-muted">
              {" · "}
              {p}
            </span>
          ))}
        </b>
        {bad > 0 && head.hint && <span className="text-xs leading-4 text-pretty text-muted max-md:hidden">{head.hint}</span>}
      </div>
      <span className="ms-auto flex flex-none items-center gap-1">
        {again > 0 && (job.spec.call || job.spec.many) && (
          <Button variant="secondary" size="md" onClick={onRetry}>
            <Icon name="refresh" size={12} />
            {t("bulk.retry", { n: again })}
          </Button>
        )}
        {rows.length > 0 && <DetailsToggle open={list} onClick={onList} />}
        <Button variant="secondary" size="md" onClick={onClose}>
          {t("nv.bulk.got")}
        </Button>
      </span>
    </div>
  );
}

/** The arrow that folds the list of the nodes of a batch in or out. */
function DetailsToggle({ open, onClick }: { open: boolean; onClick: () => void }) {
  const t = useT();
  return (
    <button type="button" aria-expanded={open} aria-label={t("nv.bulk.details")} title={t("nv.bulk.details")} onClick={onClick} className={cx(roundButton, "size-8")}>
      <Icon name="chevronDown" size={14} className={cx("block flex-none transition-transform duration-200", open && "rotate-180")} />
    </button>
  );
}
