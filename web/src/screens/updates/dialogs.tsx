import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { BundleStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { updateDateTimeAtOffset, updateDateTimeInputAtOffset, updateTimezoneName, type NodeUpdate, type Updates } from "@/lib/updates";

export function UpdateNodeDialog({
  data,
  node,
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
  const bundle = data.bundle;
  const version = bundle?.version ?? "";
  const offset = data.scheduleTimezoneOffsetMinutes;
  const hasSchedule = node.scheduledUnix > 0;
  const canSchedule = !!bundle && bundle.status === BundleStatus.TRUSTED && node.supportsUpdate && node.built < bundle.built && !nodeInActiveRollout;
  const blockedMessage = blockedReason ? t(({
    offline: "up.schedule.offline",
    queued: "up.schedule.alreadyQueued",
    paused: "up.schedule.rolloutPaused",
    differentRelease: "up.schedule.otherRelease",
    unavailable: "up.schedule.busy",
  } as const)[blockedReason]) : "";
  const [mode, setMode] = useState<"now" | "schedule">(hasSchedule ? "schedule" : "now");
  const [localDatetime, setLocalDatetime] = useState(() =>
    hasSchedule
      ? updateDateTimeInputAtOffset(node.scheduledUnix, offset)
      : updateDateTimeInputAtOffset(data.nowUnix + 3600, offset),
  );
  // datetime-local only has minute precision. Round up so the displayed minimum can never be rejected by
  // the server's exact 60-second lead-time check when the current time has non-zero seconds.
  const minUnix = Math.ceil((data.nowUnix + 60) / 60) * 60;
  const min = updateDateTimeInputAtOffset(minUnix, offset);
  const savedAt = hasSchedule ? updateDateTimeAtOffset(node.scheduledUnix, node.scheduledTimezoneOffsetMinutes) : "";
  const scheduleIsCurrent = hasSchedule && node.scheduledBuilt === bundle?.built && node.scheduledVersion === version;
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.schedule.title", { name: node.name })}
      description={t("up.schedule.lead", { version })}
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <div className="flex flex-wrap gap-2">
            {hasSchedule && (
              <Button variant="danger" size="md" disabled={busy} onClick={onCancelSchedule}>
                {t("up.schedule.cancel")}
              </Button>
            )}
            {mode === "now" ? (
              <Button variant="primary" size="md" disabled={busy || !canUpdateNow} onClick={onUpdateNow}>
                {t(queueAfterRollout ? "up.schedule.addToRollout" : "up.schedule.now")}
              </Button>
            ) : (
              <Button variant="primary" size="md" disabled={busy || !canSchedule || !localDatetime || localDatetime < min} onClick={() => onSchedule(localDatetime)}>
                {t("up.schedule.save")}
              </Button>
            )}
          </div>
        </div>
      }
    >
      <div className="flex flex-col gap-3.5">
        <div className="grid gap-2 sm:grid-cols-2">
          <label className="flex cursor-pointer items-center gap-2 rounded-field border border-line bg-surface-2 px-3 py-2.5 text-[13px] font-semibold has-checked:border-accent has-checked:bg-accent-soft">
            <input type="radio" name="node-update-mode" value="now" checked={mode === "now"} onChange={() => setMode("now")} />
            {t(queueAfterRollout ? "up.schedule.queueChoice" : "up.schedule.nowChoice")}
          </label>
          <label className="flex cursor-pointer items-center gap-2 rounded-field border border-line bg-surface-2 px-3 py-2.5 text-[13px] font-semibold has-checked:border-accent has-checked:bg-accent-soft">
            <input type="radio" name="node-update-mode" value="schedule" checked={mode === "schedule"} onChange={() => setMode("schedule")} />
            {t("up.schedule.laterChoice")}
          </label>
        </div>
        {mode === "now" ? (
          <div className="rounded-field border border-line bg-canvas p-3.5 text-[13px] leading-relaxed text-muted">
            <p>{t(queueAfterRollout ? "up.schedule.queueBody" : "up.schedule.nowBody", { name: node.name, version })}</p>
            <p className="mt-1">{t("up.schedule.safety")}</p>
            {!canUpdateNow && blockedMessage && <p className="mt-2 text-warn">{blockedMessage}</p>}
          </div>
        ) : (
          <div className="flex flex-col gap-2.5 rounded-field border border-line bg-canvas p-3.5">
            <label htmlFor="node-update-at" className="text-xs font-semibold">{t("up.schedule.dateTime")}</label>
            <input id="node-update-at" type="datetime-local" min={min} value={localDatetime} onChange={(event) => setLocalDatetime(event.target.value)} className="h-10 rounded-ctl border border-line bg-surface px-3 font-mono text-sm text-fg" />
            <span className="text-xs text-muted">{t("up.schedule.timezone", { timezone: updateTimezoneName(offset) })}</span>
            {hasSchedule && (
              <p className="rounded-field border border-line bg-surface-2 px-3 py-2 text-xs leading-relaxed text-muted">
                {t(scheduleIsCurrent ? "up.schedule.current" : "up.schedule.stale", {
                  version: node.scheduledVersion,
                  at: savedAt,
                  timezone: updateTimezoneName(node.scheduledTimezoneOffsetMinutes),
                })}
              </p>
            )}
            {!canSchedule && <p className="text-xs text-warn">{t(nodeInActiveRollout ? "up.schedule.alreadyQueued" : "up.schedule.unavailable")}</p>}
          </div>
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
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.rollback.title", { name: node.name })}
      description={from ? t("up.rollback.body", { version: from }) : t("up.rollback.bodyNoVersion")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
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
      onOpenChange={(o) => !o && !busy && onClose()}
      title={t("up.cancel.title")}
      description={t("up.cancel.body")}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={busy} onClick={onClose}>
            {t("up.cancel.keep")}
          </Button>
          <Button variant="danger" size="md" disabled={busy} onClick={onConfirm}>
            {t("up.cancel.confirm")}
          </Button>
        </>
      }
    />
  );
}
