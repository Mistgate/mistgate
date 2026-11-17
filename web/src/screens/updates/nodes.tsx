import { Menu } from "@base-ui/react/menu";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { BundleStatus, NodeUpdateState, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt, type Fmt } from "@/lib/format";
import {
  canRollbackNode,
  canUpdateNode,
  inActiveRollout,
  lastUpdateView,
  manualCommands,
  nodeStateKey,
  nodeStateKind,
  pendingStep,
  updateDateTimeAtOffset,
  updateTimezoneName,
  type NodeUpdate,
  type Rollout,
  type Step,
  type Updates,
} from "@/lib/updates";
import type { StatusKind } from "@/components/ui/status";
import { CardHead, D, NodeName, Pill, gk, useFlags } from "./bits";

// The "Nodes" card of the agents tab (the board's .uq-t): a row per node with its version, its state and the last thing that
// happened to it. The owner gets one button per row (the schedule window, which also updates at once); rolling back sits behind
// the menu beside it.

/** An agent that can update but has no crash-loop guard in its service file: only the 5-minute self-rollback protects it. */
export const unguarded = (n: NodeUpdate) => n.supportsUpdate && !n.crashGuard;
const manual = (n: NodeUpdate) => n.state === NodeUpdateState.UNSUPPORTED;

export function Command({ value }: { value: string }) {
  return (
    <div className="flex flex-wrap items-start gap-2">
      <code className="min-w-0 flex-[1_1_260px] rounded-field border border-line bg-surface px-2.5 py-2 font-mono text-xs leading-relaxed break-all whitespace-pre-wrap text-fg select-all">{value}</code>
      <CopyButton value={value} />
    </div>
  );
}

/**
 * The manual update of one node, as two commands to copy: put the release binary on it, run its install. On the Cloudflare
 * edition both run on the node, which downloads the panel's release itself and checks its SHA-256.
 */
function HowTo({ data, node }: { data: Updates; node: NodeUpdate }) {
  const t = useT();
  const c = manualCommands(data, node, useEdition().edge);
  const hint = c.onNode ? (c.guessedArch ? "up.howto.fetchHintArch" : "up.howto.fetchHint") : c.guessedArch ? "up.howto.copyHintArch" : "up.howto.copyHint";
  return (
    <div className="uq-how">
      <ol>
        <li>
          <b>{t(c.onNode ? "up.howto.fetch" : "up.howto.copy")}</b>
          {c.copy ? <Command value={c.copy} /> : <p>{t(c.onNode ? "up.howto.fetchNoFile" : "up.howto.noFile", { file: c.file })}</p>}
          {c.copy && <span>{t(hint, { file: c.file })}</span>}
        </li>
        <li>
          <b>{t("up.howto.install")}</b>
          <Command value={c.install} />
        </li>
      </ol>
      <p>{t("up.howto.after")}</p>
    </div>
  );
}

/** A node still to go in the running rollout says so, in the place of "Update available": queued (in which stage), or being updated. */
function queuedPill(t: ReturnType<typeof useT>, step: Step): { kind: StatusKind; label: string } {
  if (step.state === StepState.SENT) return { kind: "busy", label: t("up.state.updating") };
  if (step.state === StepState.GATING) return { kind: "busy", label: t("up.state.checking") };
  return { kind: "off", label: step.stage === 0 ? t("up.state.queuedCanary") : t("up.state.queued", { n: step.stage }) };
}

/** What last happened to the node on one line ("v0.1.22 → v0.1.23 · 9 h ago"), what is planned, and why it went wrong. */
function Last({ node, fmt }: { node: NodeUpdate; fmt: Fmt }) {
  const t = useT();
  const v = lastUpdateView(t, node.lastUpdate);
  const at = node.lastUpdate?.atUnix;
  return (
    <>
      {node.scheduledUnix > 0 && (
        <span className={cx("uq-lw", node.scheduledMissed && "miss")}>
          {t(node.scheduledMissed ? "up.schedule.missedRow" : "up.schedule.row", {
            version: node.scheduledVersion,
            at: updateDateTimeAtOffset(node.scheduledUnix, node.scheduledTimezoneOffsetMinutes),
            timezone: updateTimezoneName(node.scheduledTimezoneOffsetMinutes),
          })}
        </span>
      )}
      <span className={cx(v.kind === "bad" && "uq-lb", v.kind === "warn" && "uq-lx")}>
        {v.head}
        {at ? <> · {fmt.ago(at)}</> : null}
      </span>
      {v.detail && <span className="uq-lq">{v.detail}</span>}
    </>
  );
}

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold outline-none select-none data-highlighted:bg-surface-2";

/** The quieter action of a row behind "⋯": the roll back (which still asks before it does anything). */
function RowMenu({ node, onRollback }: { node: NodeUpdate; onRollback: () => void }) {
  const t = useT();
  if (!canRollbackNode(node)) return null;
  return (
    <Menu.Root>
      <Menu.Trigger
        aria-label={t("up.action.more", { name: node.name })}
        className="inline-flex size-8 flex-none items-center justify-center rounded-ctl text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg data-popup-open:bg-surface-2 data-popup-open:text-fg"
      >
        <Icon name="ellipsis" size={16} />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup className="min-w-48 origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            <Menu.Item className={cx(itemClass, "text-danger-text")} onClick={onRollback}>
              {t("up.action.rollbackItem")}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

function Row({
  node,
  data,
  owner,
  rollout,
  countryCode,
  open,
  onToggle,
  onSchedule,
  onRollback,
}: {
  node: NodeUpdate;
  data: Updates;
  owner: boolean;
  rollout?: Rollout | null;
  countryCode?: string;
  open: boolean;
  onToggle: () => void;
  onSchedule: () => void;
  onRollback: () => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const step = pendingStep(rollout, node.nodeId);
  const queued = step ? queuedPill(t, step) : null;
  const kind = queued ? queued.kind : nodeStateKind(node.state);
  const label = queued ? queued.label : t(nodeStateKey(node.state));
  const hasSchedule = node.scheduledUnix > 0;
  // a node of the active rollout is updated by it: no update or schedule of its own (the server adds a node to a rollout once)
  const joined = inActiveRollout(rollout, node.nodeId);
  const canManage = owner && !joined && (hasSchedule || (data.bundle?.status === BundleStatus.TRUSTED && canUpdateNode(node, data.bundle.built)));
  return (
    <>
      <div className="uq-r">
        <span className="uq-n">
          <NodeName name={node.name} countryCode={countryCode} to={<Link to="/nodes/$id" params={{ id: node.nodeId }} className="mono hover:underline"><b>{node.name}</b></Link>} />
          <em>{t("up.nodes.online", { n: node.onlineUsers })}</em>
        </span>
        <span className="uq-ver">
          <b className="mono">{node.version || "—"}</b>
          <em>{node.built ? t("up.built", { date: fmt.date(node.built) }) : ""}</em>
        </span>
        <span className="uq-st">
          <Pill k={gk(kind)}>{label}</Pill>
          {unguarded(node) && <span className="uq-guard">{t("up.noGuard")}</span>}
          {manual(node) && (
            <button type="button" className="an-lk nd-lk" aria-expanded={open} onClick={onToggle}>
              {t("up.howto.toggle")}
            </button>
          )}
        </span>
        <span className="uq-last">
          <span>
            <Last node={node} fmt={fmt} />
          </span>
          {canManage && (
            <Button variant="secondary" size="md" aria-label={t("up.action.updateAria", { name: node.name })} onClick={onSchedule}>
              {t("up.action.manageUpdate")}
            </Button>
          )}
          {owner && <RowMenu node={node} onRollback={onRollback} />}
        </span>
      </div>
      {open && manual(node) && <HowTo data={data} node={node} />}
    </>
  );
}

/** Every node with its version, update state and the last thing that happened to it. */
export function NodesCard({
  nodes,
  data,
  owner,
  rollout,
  onSchedule,
  onRollback,
}: {
  nodes: NodeUpdate[];
  /** The page's data: the bundle and the panel's dist folder the manual commands are built from. */
  data: Updates;
  owner: boolean;
  /** The active rollout, if any: its nodes show where they stand in it and offer no update of their own. */
  rollout?: Rollout | null;
  onSchedule: (n: NodeUpdate) => void;
  onRollback: (n: NodeUpdate) => void;
}) {
  const t = useT();
  const flags = useFlags();
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const toggle = (id: string) =>
    setOpen((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  const noGuard = nodes.filter(unguarded).map((n) => n.name);
  return (
    <section className="uq-card pp-sec" aria-label={t("up.nodes.title")}>
      <CardHead icon={D.server} tone="sky" title={t("up.nodes.title")} />
      <div className="uq-t">
        <div className="uq-r uq-h" aria-hidden>
          <span>{t("up.nodes.col.node")}</span>
          <span>{t("up.nodes.col.version")}</span>
          <span>{t("up.nodes.col.state")}</span>
          <span>{t("up.nodes.col.last")}</span>
        </div>
        {nodes.map((n) => (
          <Row
            key={n.nodeId}
            node={n}
            data={data}
            owner={owner}
            rollout={rollout}
            countryCode={flags.get(n.nodeId)}
            open={open.has(n.nodeId)}
            onToggle={() => toggle(n.nodeId)}
            onSchedule={() => onSchedule(n)}
            onRollback={() => onRollback(n)}
          />
        ))}
      </div>
      {noGuard.length > 0 && (
        <div className="uq-bf">
          <Notice title={t("up.guard.title", { names: noGuard.join(", ") })}>{t("up.guard.body")}</Notice>
        </div>
      )}
    </section>
  );
}
