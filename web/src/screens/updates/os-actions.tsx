import { Menu } from "@base-ui/react/menu";
import { Link } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { selectItemClass, selectPopupClass } from "@/components/ui/select";
import { StatusDot, StatusPill } from "@/components/ui/status";
import { OsRolloutMode, OsStepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { blockKeys, blockOf, blockText, elapsedText, gateWaitText, gateWindowText, osRunOf, pauseCode, pauseText, phaseText, resultLine, stepPill, stepReason, stepSince, useOsActions, type OsNodeRun } from "@/lib/os-run";
import type { OsRow } from "@/lib/os-updates";
import { useIsOwner, vantageNote } from "@/lib/updates";
import { OsConfirmDialog, type OsDialogMode } from "./os-confirm";

// What the owner does with one node's operating system, and what is going on there: the buttons (install the security
// updates, reboot, check for updates), the run of the step with its phase and an unending bar, and the outcome of the last
// run for a day. One block for the node's Server tab (the card, with the buttons) and the Updates table (`compact`: only the
// run and its outcome under the row; the three actions are the ⋯ menu of the row, OsRowMenu). What cannot be done says why:
// under the button, or in the menu item.

/** A clock that ticks while `active`, for "3 min so far". */
function useNowSec(active: boolean, everyMs = 15_000) {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Math.floor(Date.now() / 1000)), everyMs);
    return () => clearInterval(id);
  }, [active, everyMs]);
  return now;
}

/** The step under way: what the node is doing, for how long, and a bar that has no end because nobody knows it. */
function Progress({ run, now, compact }: { run: Extract<OsNodeRun, { kind: "running" }>; now: number; compact: boolean }) {
  const t = useT();
  const { step, rollout } = run;
  const since = stepSince(step, rollout);
  // the check after the job: which profiles have not passed yet, and how long it has waited of the most it will
  const gating = step.state === OsStepState.GATING;
  const waitingFor = gating ? gateWaitText(t, step.params) : "";
  const window = gating ? gateWindowText(t, step.params, since, now) : "";
  return (
    <div role="status" className={cx("flex min-w-0 flex-col gap-2 rounded-field border border-line bg-surface-2", compact ? "px-3 py-2" : "px-3 py-2.5")}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <div className="flex min-w-0 items-start gap-2">
          <span className="mt-[3px]">
            <StatusDot kind="busy" />
          </span>
          <b className="min-w-0 text-[13px] text-pretty">{phaseText(t, step)}</b>
        </div>
        <span className="text-[11px] text-muted max-md:ps-4">{window || t("os.run.elapsed", { time: elapsedText(t, since, now) })}</span>
      </div>
      {waitingFor && <p className="ps-4 text-xs leading-snug text-pretty break-words">{waitingFor}</p>}
      <div
        role="progressbar"
        aria-label={t("os.run.aria")}
        className="h-1.5 w-full overflow-hidden rounded-full bg-surface-3"
      >
        <div className="h-full w-1/3 animate-[mg-ui-slide_1.6s_ease-in-out_infinite] rounded-full bg-accent" />
      </div>
      <span className="text-[11px] text-muted">{t(rollout.mode === OsRolloutMode.REBOOT_ONLY ? "os.run.title.reboot" : "os.run.title.security")}</span>
    </div>
  );
}

/** The outcome of the last run: a pill and a line, and for a stopped run the banner with why and the Close button. */
function Result({ run, row, owner, busy, onClose }: { run: Extract<OsNodeRun, { kind: "result" }>; row: OsRow; owner: boolean; busy: boolean; onClose: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const { step, rollout, stopped } = run;
  const pill = stepPill(step);
  const reason = stepReason(t, step.errorKey, step.params);
  // the gate's look from another node at the panel's own path: why a pass is not a plain pass, or why a probe failure stands
  const note = vantageNote(t, row.name ?? step.nodeName, step.params, step.state === OsStepState.PASSED);
  // the pause names the step's reason and mode itself; an older answer lacks them, the step has them
  const pause = { ...step.params, reason: step.errorKey, ...rollout.pauseParams };
  const banner = pauseText(t, rollout.pauseKey, row.name ?? step.nodeName, pause);
  // the banner already says "did not come back" / "updates installed, not rebooted": the step's headline would repeat it
  const cause = pauseCode(rollout.pauseKey, pause).split(".")[0]!;
  const sameCause = !!step.errorKey && (step.errorKey === cause || step.errorKey.endsWith(`.${cause}`));
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <StatusPill kind={pill.kind} label={t(pill.key)} sm />
        <span className="min-w-0 text-xs leading-snug text-pretty text-muted">{stopped ? fmt.ago(step.finishedUnix || rollout.createdUnix) : resultLine(t, step, false)}</span>
      </div>
      {note && <span className="text-xs leading-snug text-pretty text-muted">{note}</span>}
      {stopped && (
        <Callout
          kind="bad"
          alert
          action={
            owner && (
              <Button variant="secondary" size="sm" disabled={busy} onClick={onClose}>
                {t("os.btn.close")}
              </Button>
            )
          }
        >
          <div className="flex flex-col gap-1">
            <b>{banner}</b>
            {reason.title && !sameCause && <span>{reason.title}</span>}
            {reason.detail && <span>{reason.detail}</span>}
            <span className="text-muted">{t("os.stopped.hint")}</span>
          </div>
        </Callout>
      )}
    </div>
  );
}

/** Why the buttons are off (install and reboot often share one reason), with the way out where there is one. */
function Why({ keys }: { keys: string[] }) {
  const t = useT();
  if (keys.length === 0) return null;
  return (
    <div className="flex flex-col gap-1">
      {keys.map((k) => (
        <p key={k} className="text-[11px] leading-snug text-pretty text-muted">
          {blockText(t, k)}
          {k === "agent_old" && (
            <Link to="/updates" search={{ tab: "agents" }} className="ms-1.5 font-bold whitespace-nowrap text-accent-text hover:underline">
              {t("os.block.agent_old.link")}
            </Link>
          )}
        </p>
      ))}
    </div>
  );
}

/** The ⋯ of a table row: the three actions of the card's buttons, an unavailable one greyed out with its reason under it. Owner only; nothing while a step is under way. */
export function OsRowMenu({ row, defaultOpen }: { row: OsRow; /** The dev kit shows the menu open. */ defaultOpen?: boolean }) {
  const t = useT();
  const owner = useIsOwner();
  const actions = useOsActions();
  const [dialog, setDialog] = useState<OsDialogMode | null>(null);
  if (!owner || !row.nodeId || osRunOf(row).kind === "running") return null;
  const nodeId = row.nodeId;
  const name = row.name ?? "";
  const items = [
    { id: "install", label: t("os.btn.install"), block: blockOf(row, "install"), run: () => setDialog("install") },
    { id: "reboot", label: t("os.btn.reboot"), block: blockOf(row, "reboot"), run: () => setDialog("reboot") },
    { id: "refresh", label: t(actions.refreshing ? "os.btn.refreshing" : "os.btn.refresh"), block: blockOf(row, "refresh"), run: () => void actions.refresh({ nodeId, name }) },
  ];
  // one reason for several items (a busy node, an open run) is said once on top, not under each of them
  const reasons = [...new Set(items.map((i) => i.block).filter(Boolean))];
  const shared = reasons.length === 1 && items.filter((i) => i.block).length > 1 ? reasons[0]! : "";
  return (
    <>
      <Menu.Root defaultOpen={defaultOpen}>
        <Menu.Trigger
          aria-label={t("os.menu.label", { name })}
          className="inline-flex size-9 flex-none items-center justify-center rounded-ctl text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg data-popup-open:bg-surface-2 data-popup-open:text-fg"
        >
          <Icon name="ellipsis" size={16} />
        </Menu.Trigger>
        <Menu.Portal>
          <Menu.Positioner sideOffset={6} align="end" collisionPadding={14} className="z-50 outline-none">
            <Menu.Popup className={cx(selectPopupClass, "w-max max-w-[calc(100vw-28px)] min-w-56")}>
              {shared && <p className="max-w-64 px-2.5 py-1.5 text-[11px] leading-snug font-medium text-pretty text-muted">{blockText(t, shared)}</p>}
              {items.map((i) => (
                <Menu.Item key={i.id} disabled={!!i.block || actions.busy} onClick={i.run} className={cx(selectItemClass, "data-disabled:cursor-default")}>
                  <span className="flex min-w-0 flex-col">
                    <span className={i.block || actions.busy ? "text-muted" : undefined}>{i.label}</span>
                    {i.block && !shared && <span className="max-w-64 text-[11px] leading-snug font-medium text-pretty text-muted">{blockText(t, i.block)}</span>}
                  </span>
                </Menu.Item>
              ))}
            </Menu.Popup>
          </Menu.Positioner>
        </Menu.Portal>
      </Menu.Root>
      {dialog && <OsConfirmDialog row={row} mode={dialog} actions={actions} onClose={() => setDialog(null)} />}
    </>
  );
}

/**
 * The block of a node: the run under way or its outcome, the buttons (owner only, and not in the table, where the row's menu
 * has them) and what stops them. Shows nothing for the panel's own server row, and nothing to a non-owner when there is
 * nothing to report.
 */
export function OsNodeActions({ row, compact = false }: { row: OsRow; compact?: boolean }) {
  const t = useT();
  const owner = useIsOwner();
  const actions = useOsActions();
  const [dialog, setDialog] = useState<OsDialogMode | null>(null);
  const running = row.step?.state === OsStepState.SENT || row.step?.state === OsStepState.REBOOTING || row.step?.state === OsStepState.GATING || row.step?.state === OsStepState.PENDING;
  const now = useNowSec(!!running);
  if (!row.nodeId) return null;
  const run = osRunOf(row);
  if ((!owner || compact) && run.kind === "idle") return null;

  const name = row.name ?? "";
  const size = compact ? "sm" : "md";
  const install = blockOf(row, "install");
  const reboot = blockOf(row, "reboot");
  const refresh = blockOf(row, "refresh");
  const off = actions.busy;

  return (
    <div data-shot="os-node-actions" className="flex min-w-0 flex-col gap-2.5">
      {run.kind === "running" && <Progress run={run} now={now} compact={compact} />}
      {run.kind === "running" && owner && run.step.state === OsStepState.PENDING && (
        <div>
          <Button variant="ghost" size={size} disabled={off} onClick={() => void actions.close(run.rollout.id)}>
            {t("os.btn.cancel")}
          </Button>
        </div>
      )}
      {run.kind === "result" && <Result run={run} row={row} owner={owner} busy={off} onClose={() => void actions.close(run.rollout.id)} />}
      {owner && !compact && run.kind !== "running" && (
        <>
          <div className="flex flex-wrap items-center gap-2">
            <Button variant="primary" size={size} disabled={!!install || off} onClick={() => setDialog("install")}>
              {t("os.btn.install")}
            </Button>
            <Button variant="secondary" size={size} disabled={!!reboot || off} onClick={() => setDialog("reboot")}>
              {t("os.btn.reboot")}
            </Button>
            <Button variant="ghost" size={size} disabled={!!refresh || off} onClick={() => void actions.refresh({ nodeId: row.nodeId ?? "", name })}>
              <Icon name="refresh" size={12} className={actions.refreshing ? "animate-[mg-ui-spin_1s_linear_infinite]" : undefined} />
              {t(actions.refreshing ? "os.btn.refreshing" : "os.btn.refresh")}
            </Button>
          </div>
          <Why keys={blockKeys(row)} />
        </>
      )}
      {dialog && <OsConfirmDialog row={row} mode={dialog} actions={actions} onClose={() => setDialog(null)} />}
    </div>
  );
}
