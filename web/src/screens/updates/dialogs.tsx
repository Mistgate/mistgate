import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { canaryOf, defaultBatch, updateDateTimeAtOffset, updateDateTimeInputAtOffset, updateTimezoneName, type NodeUpdate, type Updates } from "@/lib/updates";
import { NodeName, useFlags } from "./bits";

/**
 * Update the rest of the fleet: the nodes that can be updated, each with a tick (all ticked), the canary named, and what
 * happens in words. Nothing starts until "Start rollout"; the call is pinned to the bundle shown (and asks for step-up).
 */
export function RolloutDialog({
  version,
  nodes,
  busy,
  onStart,
  onClose,
  children,
}: {
  version: string;
  /** The nodes the update would take: outdated, able to update themselves. */
  nodes: NodeUpdate[];
  busy: boolean;
  onStart: (nodeIds: string[]) => void;
  onClose: () => void;
  /** Under the list: the Nodes page's bulk bar names the selected nodes the update leaves out, and why. */
  children?: ReactNode;
}) {
  const t = useT();
  const flags = useFlags();
  const noProbe = useEdition().off("health_checks"); // the gate there has no client-eye check yet
  const [off, setOff] = useState<ReadonlySet<string>>(new Set());
  const picked = nodes.filter((n) => !off.has(n.nodeId));
  const canary = canaryOf(picked);
  const toggle = (id: string) =>
    setOff((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.rollout.title", { version })}
      closeLabel={t("common.close")}
      className="uq-dlg"
      shot="updates-rollout-dialog"
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" disabled={busy || picked.length === 0} onClick={() => onStart(picked.map((n) => n.nodeId))}>
            {t("up.rollout.go")}
          </Button>
        </>
      }
    >
      <div className="uq-db">
        <p>{picked.length > 1 ? t("up.rollout.lead", { batch: defaultBatch(picked.length) }) : t("up.rollout.leadOne")}</p>
        <ul aria-label={t("up.rollout.nodes")} className="uq-can">
          {nodes.map((n) => {
            const on = !off.has(n.nodeId);
            const row = (
              <>
                <NodeName name={n.name} countryCode={flags.get(n.nodeId)} />
                {n === canary && <span className="pv-pill">{t("up.rollout.canary")}</span>}
                <em>{t("up.nodes.online", { n: n.onlineUsers })}</em>
              </>
            );
            return (
              <li key={n.nodeId}>
                {nodes.length > 1 ? (
                  <label>
                    <input type="checkbox" checked={on} onChange={() => toggle(n.nodeId)} aria-label={n.name} />
                    {row}
                  </label>
                ) : (
                  <div className="uq-canrow">{row}</div>
                )}
              </li>
            );
          })}
        </ul>
        {children}
        <p>
          {t("up.rollout.restart")} {t("up.ro.safety")}
          {noProbe && ` ${t("up.gate.noProbe")}`}
        </p>
      </div>
    </Modal>
  );
}

export function UpdateNodeDialog({
  data,
  node,
  initialMode,
  canUpdateNow,
  queueAfterRollout,
  nodeInActiveRollout,
  blockedReason,
  busy,
  onUpdateNow,
  onSchedule,
  onCancelSchedule,
  onClose,
}: {
  data: Updates;
  node: NodeUpdate;
  /** "schedule" opens on the date and time (the menu's "Schedule…"); by default it opens on the saved schedule, else on "now". */
  initialMode?: "now" | "schedule";
  canUpdateNow: boolean;
  queueAfterRollout: boolean;
  nodeInActiveRollout: boolean;
  blockedReason?: "offline" | "queued" | "paused" | "differentRelease" | "unavailable";
  busy: boolean;
  onUpdateNow: () => void;
  onSchedule: (localDatetime: string) => void;
  onCancelSchedule: () => void;
  onClose: () => void;
}) {
  const t = useT();
  const noProbe = useEdition().off("health_checks");
  const bundle = data.bundle;
  const version = bundle?.version ?? "";
  const offset = data.scheduleTimezoneOffsetMinutes;
  const hasSchedule = node.scheduledUnix > 0;
  const canSchedule = !!bundle && bundle.status === BundleStatus.TRUSTED && node.supportsUpdate && node.built < bundle.built && !nodeInActiveRollout;
  const blockedMessage = blockedReason
    ? t(
        (
          {
            offline: "up.schedule.offline",
            queued: "up.schedule.alreadyQueued",
            paused: "up.schedule.rolloutPaused",
            differentRelease: "up.schedule.otherRelease",
            unavailable: "up.schedule.busy",
          } as const
        )[blockedReason],
      )
    : "";
  const [mode, setMode] = useState<"now" | "schedule">(initialMode ?? (hasSchedule ? "schedule" : "now"));
  const [localDatetime, setLocalDatetime] = useState(() => (hasSchedule ? updateDateTimeInputAtOffset(node.scheduledUnix, offset) : updateDateTimeInputAtOffset(data.nowUnix + 3600, offset)));
  // datetime-local only has minute precision. Round up so the displayed minimum can never be rejected by
  // the server's exact 60-second lead-time check when the current time has non-zero seconds.
  const minUnix = Math.ceil((data.nowUnix + 60) / 60) * 60;
  const min = updateDateTimeInputAtOffset(minUnix, offset);
  const savedAt = hasSchedule ? updateDateTimeAtOffset(node.scheduledUnix, node.scheduledTimezoneOffsetMinutes) : "";
  const scheduleIsCurrent = hasSchedule && node.scheduledBuilt === bundle?.built && node.scheduledVersion === version;
  const options = [
    { id: "now" as const, title: t(queueAfterRollout ? "up.schedule.queueChoice" : "up.schedule.nowChoice"), sub: t("uq.opt.nowSub") },
    { id: "schedule" as const, title: t("up.schedule.laterChoice"), sub: t("uq.opt.laterSub") },
  ];
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.schedule.title", { name: node.name })}
      closeLabel={t("common.close")}
      className="uq-dlg"
      shot="updates-schedule-dialog"
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          {hasSchedule && (
            <Button variant="secondary" size="lg" disabled={busy} onClick={onCancelSchedule}>
              {t("up.schedule.cancel")}
            </Button>
          )}
          {mode === "now" ? (
            <Button variant="primary" size="lg" disabled={busy || !canUpdateNow} onClick={onUpdateNow}>
              {t(queueAfterRollout ? "up.schedule.addToRollout" : "up.schedule.now")}
            </Button>
          ) : (
            <Button variant="primary" size="lg" disabled={busy || !canSchedule || !localDatetime || localDatetime < min} onClick={() => onSchedule(localDatetime)}>
              {t("up.schedule.save")}
            </Button>
          )}
        </>
      }
    >
      <div className="uq-db">
        <p>{t("up.schedule.lead", { version })}</p>
        <div className="uq-opts" role="radiogroup" aria-label={t("up.schedule.title", { name: node.name })}>
          {options.map((o) => (
            <button key={o.id} type="button" role="radio" aria-checked={mode === o.id} className="uq-opt" onClick={() => setMode(o.id)}>
              <span className="nx-rad" aria-hidden data-checked={mode === o.id}>
                <i />
              </span>
              <span>
                <b>{o.title}</b>
                <em>{o.sub}</em>
              </span>
            </button>
          ))}
        </div>
        {mode === "now" ? (
          <>
            <p>{t(queueAfterRollout ? "up.schedule.queueBody" : "up.schedule.nowBody", { name: node.name, version })}</p>
            <p>
              {t("up.schedule.safety")}
              {noProbe && ` ${t("up.gate.noProbe")}`}
            </p>
            {!canUpdateNow && blockedMessage && <p className="uq-warn">{blockedMessage}</p>}
          </>
        ) : (
          <>
            <label className="uq-when" htmlFor="node-update-at">
              <span>{t("up.schedule.dateTime")}</span>
              <input id="node-update-at" type="datetime-local" min={min} value={localDatetime} onChange={(event) => setLocalDatetime(event.target.value)} className={cx("an-i", "mono")} />
              <small>{t("up.schedule.timezone", { timezone: updateTimezoneName(offset) })}</small>
            </label>
            {hasSchedule && (
              <p className="uq-cur mono">
                {t(scheduleIsCurrent ? "up.schedule.current" : "up.schedule.stale", {
                  version: node.scheduledVersion,
                  at: savedAt,
                  timezone: updateTimezoneName(node.scheduledTimezoneOffsetMinutes),
                })}
              </p>
            )}
            {!canSchedule && <p className="uq-warn">{t(nodeInActiveRollout ? "up.schedule.alreadyQueued" : "up.schedule.unavailable")}</p>}
          </>
        )}
      </div>
    </Modal>
  );
}

export function RollbackDialog({ node, busy, onConfirm, onClose }: { node: NodeUpdate; busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  const from = node.lastUpdate?.fromVersion ?? "";
  return (
    <Modal
      open
      className="uq-dlg"
      closeLabel={t("common.close")}
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.rollback.title", { name: node.name })}
      description={from ? t("up.rollback.body", { version: from }) : t("up.rollback.bodyNoVersion")}
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="lg" disabled={busy} onClick={onConfirm}>
            {t("up.rollback.confirm")}
          </Button>
        </>
      }
    />
  );
}

export function CancelDialog({ busy, onConfirm, onClose }: { busy: boolean; onConfirm: () => void; onClose: () => void }) {
  const t = useT();
  return (
    <Modal
      open
      className="uq-dlg"
      closeLabel={t("common.close")}
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.cancel.title")}
      description={t("up.cancel.body")}
      footer={
        <>
          <Button variant="secondary" size="lg" disabled={busy} onClick={onClose}>
            {t("up.cancel.keep")}
          </Button>
          <Button variant="danger" size="lg" disabled={busy} onClick={onConfirm}>
            {t("up.cancel.confirm")}
          </Button>
        </>
      }
    />
  );
}
