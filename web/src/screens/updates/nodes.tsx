import { Menu } from "@base-ui/react/menu";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { StatusPill, kindTextClass, type StatusKind } from "@/components/ui/status";
import { BundleStatus, NodeUpdateState, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { canRollbackNode, canUpdateNode, inActiveRollout, lastUpdateView, manualCommands, nodeStateKey, nodeStateKind, updateDateTimeAtOffset, updateTimezoneName, pendingStep, type NodeUpdate, type Rollout, type Step, type Updates } from "@/lib/updates";

// The same columns for the header and every row, so they line up: node, version, state, last update, actions.
const colsOwner = "md:grid-cols-[minmax(120px,1fr)_130px_150px_minmax(180px,1.6fr)_160px]";
const colsView = "md:grid-cols-[minmax(120px,1fr)_130px_150px_minmax(180px,1.6fr)]";

/** An agent that can update but has no crash-loop guard in its service file: only the 5-minute self-rollback protects it. */
const unguarded = (n: NodeUpdate) => n.supportsUpdate && !n.crashGuard;
const manual = (n: NodeUpdate) => n.state === NodeUpdateState.UNSUPPORTED;

function Version({ node, fmt }: { node: NodeUpdate; fmt: Fmt }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate font-mono text-xs font-bold">{node.version || "—"}</span>
      <span className="text-[11px] text-muted">{node.built ? t("up.built", { date: fmt.date(node.built) }) : ""}</span>
    </div>
  );
}

/** What last happened to the node on one line ("v0.1.22 → v0.1.23 · 9 h ago"); the reason under it only when it went wrong. */
function LastUpdate({ node, fmt }: { node: NodeUpdate; fmt: Fmt }) {
  const t = useT();
  const v = lastUpdateView(t, node.lastUpdate);
  const at = node.lastUpdate?.atUnix;
  const scheduled = node.scheduledUnix > 0;
  return (
    <div className="flex min-w-0 flex-col gap-0.5 text-xs">
      {scheduled && (
        <span className={cx("text-pretty", node.scheduledMissed ? kindTextClass.warn : "text-accent-text")}>
          {t(node.scheduledMissed ? "up.schedule.missedRow" : "up.schedule.row", {
            version: node.scheduledVersion,
            at: updateDateTimeAtOffset(node.scheduledUnix, node.scheduledTimezoneOffsetMinutes),
            timezone: updateTimezoneName(node.scheduledTimezoneOffsetMinutes),
          })}
        </span>
      )}
      <span className={cx("text-pretty", v.kind === "off" || v.kind === "ok" ? "text-muted" : kindTextClass[v.kind])}>
        {v.head}
        {at ? <span className="text-faint"> · {fmt.ago(at)}</span> : null}
      </span>
      {v.detail && <span className="leading-snug text-pretty text-muted">{v.detail}</span>}
    </div>
  );
}

/** A node still to go in the running rollout says so, in the place of "Update available": queued (in which stage), or being updated. */
function queuedPill(t: ReturnType<typeof useT>, step: Step): { kind: StatusKind; label: string } {
  if (step.state === StepState.SENT) return { kind: "busy", label: t("up.state.updating") };
  if (step.state === StepState.GATING) return { kind: "busy", label: t("up.state.checking") };
  return { kind: "off", label: step.stage === 0 ? t("up.state.queuedCanary") : t("up.state.queued", { n: step.stage }) };
}

function State({ node, step, open, onToggle }: { node: NodeUpdate; step?: Step; open: boolean; onToggle: () => void }) {
  const t = useT();
  const queued = step ? queuedPill(t, step) : null;
  return (
    <div className="flex flex-col items-start gap-1">
      <StatusPill kind={queued ? queued.kind : nodeStateKind(node.state)} label={queued ? queued.label : t(nodeStateKey(node.state))} sm />
      {unguarded(node) && <span className={cx("text-[11px] font-semibold", kindTextClass.warn)}>{t("up.noGuard")}</span>}
      {manual(node) && (
        <button
          type="button"
          aria-expanded={open}
          onClick={onToggle}
          className="-mx-1 flex min-h-7 items-center gap-1 rounded-ctl px-1 text-xs font-bold text-accent-text hover:bg-surface-2"
        >
          {t("up.howto.toggle")}
          <Icon name="chevronRight" size={12} className={cx("block flex-none transition-transform duration-200", open ? "-rotate-90" : "rotate-90")} />
        </button>
      )}
    </div>
  );
}

function Command({ value }: { value: string }) {
  return (
    <div className="flex flex-wrap items-start gap-2">
      <code className="min-w-0 flex-[1_1_260px] rounded-field border border-line bg-surface px-2.5 py-2 font-mono text-xs leading-relaxed break-all text-fg select-all">
        {value}
      </code>
      <CopyButton value={value} />
    </div>
  );
}

/** The manual update of one node, as two commands to copy: put the release binary on it, run its install. */
function HowTo({ data, node }: { data: Updates; node: NodeUpdate }) {
  const t = useT();
  const c = manualCommands(data, node);
  return (
    <div className="flex flex-col gap-3 rounded-field border border-line bg-canvas p-3.5">
      <ol className="flex flex-col gap-3">
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-bold">{t("up.howto.copy")}</span>
          {c.copy ? <Command value={c.copy} /> : <p className="text-xs leading-snug text-pretty text-muted">{t("up.howto.noFile", { file: c.file })}</p>}
          {c.copy && <span className="text-[11px] leading-snug text-muted">{t(c.guessedArch ? "up.howto.copyHintArch" : "up.howto.copyHint")}</span>}
        </li>
        <li className="flex flex-col gap-1.5">
          <span className="text-xs font-bold">{t("up.howto.install")}</span>
          <Command value={c.install} />
        </li>
      </ol>
      <p className="text-[11px] leading-snug text-pretty text-muted">{t("up.howto.after")}</p>
    </div>
  );
}

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold outline-none select-none data-highlighted:bg-surface-2";

/** The quieter actions of a row behind "⋯": a schedule, and the roll back (which still asks before it does anything). */
function RowMenu({ node, onSchedule, onRollback, canSchedule }: { node: NodeUpdate; onSchedule: () => void; onRollback: () => void; canSchedule: boolean }) {
  const t = useT();
  const rollback = canRollbackNode(node);
  if (!canSchedule && !rollback) return null;
  return (
    <Menu.Root>
      <Menu.Trigger
        aria-label={t("up.action.more", { name: node.name })}
        className="inline-flex size-9 flex-none items-center justify-center rounded-ctl text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg data-popup-open:bg-surface-2 data-popup-open:text-fg"
      >
        <Icon name="ellipsis" size={16} />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup className="min-w-48 origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {canSchedule && (
              <Menu.Item className={cx(itemClass, "text-fg")} onClick={onSchedule}>
                {t("up.action.schedule")}
              </Menu.Item>
            )}
            {rollback && (
              <Menu.Item className={cx(itemClass, "text-danger-text")} onClick={onRollback}>
                {t("up.action.rollbackItem")}
              </Menu.Item>
            )}
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/** One main button per row ("Update", or "Schedule" when one is saved), the rest in the menu next to it. */
/** `part`: the phone puts the menu beside the state and keeps only the main button for the foot of the card. */
function Actions({ node, data, joined, part = "all", onUpdate, onSchedule, onRollback }: { node: NodeUpdate; data: Updates; joined: boolean; part?: "all" | "main" | "menu"; onUpdate: () => void; onSchedule: () => void; onRollback: () => void }) {
  const t = useT();
  const hasSchedule = node.scheduledUnix > 0;
  // a node of the active rollout is updated by it: no update or schedule of its own (the server adds a node to a rollout once)
  const canManageUpdate = !joined && (hasSchedule || (data.bundle?.status === BundleStatus.TRUSTED && canUpdateNode(node, data.bundle.built)));
  const menu = <RowMenu node={node} canSchedule={canManageUpdate && !hasSchedule} onSchedule={onSchedule} onRollback={onRollback} />;
  if (part === "menu") return menu;
  if (part === "main" && !canManageUpdate) return null;
  return (
    <div className="flex items-center gap-1.5 md:justify-end">
      {canManageUpdate && (
        <Button variant="secondary" size="md" aria-label={t("up.action.updateAria", { name: node.name })} onClick={onUpdate}>
          {hasSchedule ? t("up.action.manageUpdate") : t("up.action.update")}
        </Button>
      )}
      {part === "all" && menu}
    </div>
  );
}

function Name({ node }: { node: NodeUpdate }) {
  const t = useT();
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <Link to="/nodes/$id" params={{ id: node.nodeId }} className="truncate text-sm font-bold hover:underline">
        {node.name}
      </Link>
      <span className="text-[11px] text-muted">{t("up.nodes.online", { n: node.onlineUsers })}</span>
    </div>
  );
}

/**
 * Every node with its version, update state and the last thing that happened to it. The owner gets one main button per
 * row; scheduling and rolling back sit behind the menu beside it.
 */
export function NodeTable({
  nodes,
  data,
  owner,
  rollout,
  onUpdate,
  onSchedule,
  onRollback,
}: {
  nodes: NodeUpdate[];
  /** The page's data: the bundle and the panel's dist folder the manual commands are built from. */
  data: Updates;
  owner: boolean;
  /** The active rollout, if any: its nodes show where they stand in it and offer no update of their own. */
  rollout?: Rollout | null;
  onUpdate: (n: NodeUpdate) => void;
  onSchedule: (n: NodeUpdate) => void;
  onRollback: (n: NodeUpdate) => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const heads = [t("up.nodes.col.node"), t("up.nodes.col.version"), t("up.nodes.col.state"), t("up.nodes.col.last")];
  const noGuard = nodes.filter(unguarded).map((n) => n.name);
  const actions = (n: NodeUpdate, part: "all" | "main" | "menu" = "all") => <Actions node={n} data={data} joined={inActiveRollout(rollout, n.nodeId)} part={part} onUpdate={() => onUpdate(n)} onSchedule={() => onSchedule(n)} onRollback={() => onRollback(n)} />;

  return (
    <section className="flex flex-col gap-3">
      <SectionLabel as="h2" icon="network" tone="sky">
        {t("up.nodes.title")}
      </SectionLabel>
      {nodes.length === 0 ? (
        <div className="rounded-card-lg border border-dashed border-line px-6 py-8 text-center text-[13px] text-muted">{t("up.nodes.empty")}</div>
      ) : (
        <>
          <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
            <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", owner ? colsOwner : colsView)}>
              {heads.map((h) => (
                <span key={h}>{h}</span>
              ))}
              {owner && <span />}
            </div>
            {nodes.map((n) => (
              <div key={n.nodeId} className="border-b border-line last:border-b-0">
                <div className={cx("grid min-h-[60px] items-center gap-4 px-5 py-2.5 text-[13px]", owner ? colsOwner : colsView)}>
                  <Name node={n} />
                  <Version node={n} fmt={fmt} />
                  <State node={n} step={pendingStep(rollout, n.nodeId)} open={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} />
                  <LastUpdate node={n} fmt={fmt} />
                  {owner && actions(n)}
                </div>
                {open.has(n.nodeId) && (
                  <div className="px-5 pb-4">
                    <HowTo data={data} node={n} />
                  </div>
                )}
              </div>
            ))}
          </div>
          <div className="flex flex-col gap-2 md:hidden">
            {nodes.map((n) => (
              <div key={n.nodeId} className="flex flex-col gap-3 rounded-card border border-line bg-surface p-3.5">
                <div className="flex items-start justify-between gap-2">
                  <Name node={n} />
                  <div className="flex items-start gap-1">
                    <State node={n} step={pendingStep(rollout, n.nodeId)} open={open.has(n.nodeId)} onToggle={() => toggle(n.nodeId)} />
                    {owner && actions(n, "menu")}
                  </div>
                </div>
                <div className="grid grid-cols-2 gap-3">
                  <Version node={n} fmt={fmt} />
                  <LastUpdate node={n} fmt={fmt} />
                </div>
                {open.has(n.nodeId) && <HowTo data={data} node={n} />}
                {owner && actions(n, "main")}
              </div>
            ))}
          </div>
        </>
      )}
      {noGuard.length > 0 && (
        <Notice title={t("up.guard.title", { names: noGuard.join(", ") })}>
          {t("up.guard.body")}
        </Notice>
      )}
    </section>
  );
}
