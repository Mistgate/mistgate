import { useRef, useState } from "react";
import { ActionMenu, BarAction, BatchPanel, BulkFloat, ClearButton, LeaveGuard, useEscClear, useNarrow, type Avail, type MenuEntry } from "@/components/bulk";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Batch } from "@/lib/batch";
import { maxBulkNodes } from "@/lib/decoy";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import type { Plain } from "@/lib/plain";
import { actionMeta, AgentDialog, availability, BindDialog, decoyOutcome, panelFor, PasswordDialog, UnbindDialog, type ActionId, type BulkRun } from "./nodes-bulk-actions";
import { DecoyBulkDialog } from "./nodes-decoy";

// The bulk bar of the Nodes page (owner only): it floats at the foot of the content while nodes are selected, in the look of
// the phone's dock (on a phone it takes the dock's place), and grows upwards into the rows of a running batch. Checks
// start at once; the actions with consequences (passwords, keys, the decoy site, the agent) ask first. The rules of who
// takes part, the calls and the confirms are in nodes-bulk-actions.tsx; running them in lib/batch.ts; the parts the bar
// shares with the Users page's are in components/bulk.tsx.

type Node = Plain<NodeMsg>;

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

  useEscClear(picked.length > 0, onClear);

  const avail = Object.fromEntries((Object.keys(actionMeta) as ActionId[]).map((a) => [a, availability(a, picked, run.ctx, t)])) as Record<ActionId, Avail>;
  const act = (a: ActionId) => (instant.has(a) ? void run.run(a, picked) : setDialog(a));
  const byId = new Map(nodes.map((n) => [n.id, n]));
  // the decoy change is one call: its rows read how each node is doing from the live list
  const rows = job ? job.rows.map((r) => (job.spec.action === "decoy" && r.state === "ok" ? { ...r, ...decoyOutcome(t, byId.get(r.id)) } : r)) : [];
  const close = () => setDialog(null);
  const props = { nodes: picked, hidden, run, onClose: close };
  const panel = job ? panelFor(t, job.spec.action as ActionId) : null;

  return (
    <>
      <BulkFloat show={picked.length > 0 || !!job} wide={!!job} region={t("bulk.region")}>
        {job && panel && <BatchPanel job={job} rows={rows} head={panel.head} open={panel.open} onStop={batch.stop} onRetry={batch.retry} onClose={batch.close} />}
        {picked.length > 0 && <BulkControls count={picked.length} hidden={hidden.size} avail={avail} agentOff={agentOff} busy={batch.running} onAction={act} onClear={onClear} />}
      </BulkFloat>
      {dialog === "password" && <PasswordDialog {...props} />}
      {dialog === "bindKey" && <BindDialog {...props} />}
      {dialog === "unbindKey" && <UnbindDialog {...props} />}
      {dialog === "agent" && <AgentDialog nodes={picked} run={run} onClose={close} />}
      {dialog === "decoy" && <DecoyBulkDialog nodes={picked} onClose={close} onDone={() => void run.run("decoy", picked)} />}
      <LeaveGuard active={batch.running} title={t("bulk.leave.title")} body={t("bulk.leave.body")} onLeave={batch.stop} />
    </>
  );
}

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
  const item = (a: ActionId): MenuEntry<ActionId> => ({ id: a, meta: actionMeta[a], avail: avail[a] });
  const more = [
    ...(narrow ? [...(agentOff ? [] : [item("agent")]), item("password"), null] : []),
    item("panelKey"),
    item("bindKey"),
    item("unbindKey"),
    null,
    item("decoy"),
  ];
  const note = narrow && hidden > 0 ? t.n("bulk.hidden", hidden) : undefined;
  return (
    <div ref={ref} className="flex h-[60px] flex-none items-center gap-1 p-2.5">
      <ClearButton onClear={onClear} />
      {/* wide, the words keep their room (so the row's measure is honest); narrow, the count takes what is left */}
      <div className={cx("flex items-center gap-2", narrow ? "min-w-0 flex-1" : "flex-none pe-1")}>
        <span className="inline-flex h-6 min-w-6 flex-none items-center justify-center rounded-full bg-accent-soft px-2 font-mono text-xs font-bold text-accent-text tabular-nums">{count}</span>
        <span className="truncate text-[13px] font-semibold whitespace-nowrap">{t.n(narrow ? "bulk.selectedShort" : "bulk.selected", count)}</span>
        {!narrow && hidden > 0 && <span className="text-xs whitespace-nowrap text-muted">· {t.n("bulk.hidden", hidden)}</span>}
        {!narrow && count >= maxBulkNodes && <span className="text-xs whitespace-nowrap text-muted">· {t("bulk.limit")}</span>}
      </div>
      <span aria-hidden className={cx("h-6 w-px flex-none bg-line", narrow ? "mx-0.5" : "mx-1")} />
      <ActionMenu label={t("bulk.check")} icon="pulse" tone="sky" items={[item("ports"), item("doctor")]} note={note} busy={busy} onAction={onAction} align="start" />
      {!narrow && !agentOff && <BarAction meta={actionMeta.agent} avail={avail.agent} busy={busy} onClick={() => onAction("agent")} count />}
      {!narrow && <BarAction meta={actionMeta.password} avail={avail.password} busy={busy} onClick={() => onAction("password")} />}
      <ActionMenu label={t("bulk.more")} items={more} note={note} busy={busy} onAction={onAction} iconOnly={narrow} align="end" />
    </div>
  );
}
