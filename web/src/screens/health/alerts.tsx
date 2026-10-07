import { Menu } from "@base-ui/react/menu";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { AlertKind } from "@/gen/mistgate/admin/v1/health_pb";
import { Chip, SectionLabel } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pagination } from "@/components/ui/pagination";
import { StatusDot, kindTextClass } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT, type T } from "@/i18n";
import { health, nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { quoteNames } from "@/lib/doctor-detail";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { alertFix, alertTitle, alertWhy, muteChoices, resolutionWord, restartConsequence, severityKind, severityWord, useCan, type Alert } from "@/lib/health";
import { useIsPhone } from "@/lib/media";
import { clampPage, usePaging } from "@/lib/paging";
import { plain } from "@/lib/plain";
import { useAcceptDoctor } from "./doctor-parts";
import { FixControl, type FixFlow } from "./fix";

/** Mute for a while, or unmute; the toast offers the opposite as "Undo". */
function useMute() {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const call = useMutation({
    mutationFn: async (v: { id: string; seconds: number }) => plain(await health.muteAlert({ alertId: v.id, durationS: v.seconds })),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["health", "alerts"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (a: Alert, seconds: number) =>
    call.mutate(
      { id: a.id, seconds },
      {
        onSuccess: (res) => {
          const node = a.nodeName || t("hl.alerts.fleetWide");
          if (seconds > 0) {
            const until = res.alert?.mutedUntilUnix || Math.floor(Date.now() / 1000) + seconds;
            toast(t("hl.alerts.mutedToast", { node, time: fmt.stamp(until) }), { undo: () => call.mutate({ id: a.id, seconds: 0 }) });
          } else toast(t("hl.alerts.unmutedToast", { node }));
        },
      },
    );
}

export function AlertsTab({ active, history, now, flow }: { active: Alert[]; history: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const fmt = useFmt();
  const lastClosed = history[0]?.resolvedAtUnix;
  return (
    <div className="flex flex-col gap-3.5">
      {active.length === 0 && (
        <div className="flex flex-wrap items-center gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5 text-[13px]">
          <StatusDot kind="ok" />
          <b>{t("hl.alerts.quiet")}</b>
          <span className="text-muted">{lastClosed ? t("hl.alerts.quietLast", { ago: fmt.ago(lastClosed) }) : t("hl.alerts.quietNone")}</span>
        </div>
      )}
      {active.map((a) => (
        <AlertCard key={a.id} alert={a} now={now} flow={flow} />
      ))}
      <HistoryList rows={history} t={t} fmt={fmt} />
    </div>
  );
}

/**
 * One active alert: severity, node, what is wrong and why, then what to do about it. The first thing the owner can do
 * is the primary button (the fix, the restart, or the place where it is fixed); the rest are quieter. A warning of the
 * doctor can be accepted as normal for the node; any alert can be muted for a while. `onNodePage`: it sits on its own
 * node's page, so the node is not named or linked.
 */
export function AlertCard({ alert: a, now, flow, onNodePage = false }: { alert: Alert; now: number; flow: FixFlow; onNodePage?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const mute = useMute();
  const accept = useAcceptDoctor();
  const [restarting, setRestarting] = useState(false);
  const kind = severityKind(a.severity);
  const fix = alertFix(a);
  const muted = a.mutedUntilUnix > now;
  const why = alertWhy(t, fmt, a);
  const inbound = a.params.inbound_id ?? "";
  // the same key as the doctor row of this check, so a fix started from either shows its progress on both
  const check = a.params.check ?? a.subject;
  const target = fix && a.nodeId ? { ...fix, nodeId: a.nodeId, nodeName: a.nodeName, key: `${a.nodeId}/${check}/${inbound}`, profile: a.params.profile } : null;
  const restart = a.nodeId && can.run ? (a.actions.includes("restart_inbound") && a.params.inbound ? "one" : a.actions.includes("restart_inbounds") ? "all" : null) : null;
  const go = a.nodeId ? (a.actions.includes("open_profiles") ? "profiles" : a.actions.includes("open_warp") ? "warp" : null) : null;
  // the main action is the first one this role can take
  const goMain = !(target && can.fix) && !restart;

  return (
    <article
      className={cx(
        "flex flex-col gap-2.5 rounded-card-lg border bg-surface p-4",
        kind === "bad" ? "border-[color-mix(in_oklch,var(--danger)_40%,var(--border))]" : "border-line",
        muted && "opacity-75",
      )}
    >
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1">
        <span className={cx("flex items-center gap-[7px] text-xs font-bold", kindTextClass[kind])}>
          <StatusDot kind={kind} />
          {t(severityWord(a.severity))}
        </span>
        {onNodePage ? null : a.nodeName ? (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} className="font-mono text-xs font-bold underline decoration-dotted underline-offset-2 hover:text-accent-text">
            {a.nodeName}
          </Link>
        ) : (
          <Chip>{t("hl.alerts.fleetWide")}</Chip>
        )}
        <h3 className="min-w-40 flex-1 text-sm font-bold">{alertTitle(t, a)}</h3>
        <span className="font-mono text-[11px] text-muted">{t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix))) })}</span>
      </div>
      {why && <p className="text-[13px] leading-normal text-pretty text-muted">{why}</p>}
      {muted && <p className="font-mono text-[11px] text-faint">{t("hl.alerts.muted", { time: fmt.stamp(a.mutedUntilUnix) })}</p>}
      <div className="flex flex-wrap items-center gap-1.5">
        {target && <FixControl flow={flow} target={target} variant="primary" />}
        {restart && (
          <Button variant={target && can.fix ? "secondary" : "primary"} size="md" onClick={() => setRestarting(true)}>
            {restart === "one" ? t("hl.fix.restartNamed", { profile: a.params.profile || a.params.inbound || "" }) : t("hl.alerts.restartAll")}
          </Button>
        )}
        {go === "profiles" && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "profiles" }} className={buttonClass(goMain ? "primary" : "secondary", "md")}>
            {t("hl.alerts.openProfiles")}
          </Link>
        )}
        {go === "warp" && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "settings" }} hash="warp" className={buttonClass(goMain ? "primary" : "secondary", "md")}>
            {t("warp.open")}
          </Link>
        )}
        {a.kind === AlertKind.UPDATE_FAILED && (
          <Link to="/updates" className={buttonClass("secondary", "md")}>
            {t("up.alert.open")}
          </Link>
        )}
        {a.nodeId && a.actions.includes("open_node") && !go && !onNodePage && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} className={buttonClass("secondary", "md")}>
            {t("hl.alerts.openNode")}
          </Link>
        )}
        {a.nodeId && a.params.check && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "doctor" }} className={buttonClass("secondary", "md")}>
            {t("hl.alerts.openDoctor")}
          </Link>
        )}
        {can.run && a.actions.includes("accept") && a.params.check && (
          <Button
            variant="ghost"
            size="md"
            disabled={accept.busy}
            title={t("hl.doctor.acceptHint")}
            onClick={() => accept.run({ nodeId: a.nodeId, nodeName: a.nodeName, checkId: a.params.check! }, true)}
          >
            {t("hl.doctor.accept")}
          </Button>
        )}
        {can.run &&
          a.actions.includes("mute") &&
          (muted ? (
            <Button variant="ghost" size="md" onClick={() => mute(a, 0)}>
              {t("hl.alerts.unmute")}
            </Button>
          ) : (
            <MuteMenu onMute={(seconds) => mute(a, seconds)} />
          ))}
      </div>
      {restart && <RestartDialog open={restarting} onOpenChange={setRestarting} alert={a} scope={restart} />}
    </article>
  );
}

const itemClass = "flex min-h-10 cursor-pointer items-center rounded-ctl px-2.5 py-1.5 text-[13px] font-semibold text-fg outline-none select-none data-highlighted:bg-surface-2";
const muteKeys = muteChoices(new Date(0)).map((c) => c.key);

/** "Mute ▾": for an hour, until the morning, a day or a week, with what muting means under the choices. */
export function MuteMenu({ onMute }: { onMute: (seconds: number) => void }) {
  const t = useT();
  return (
    <Menu.Root>
      <Menu.Trigger className={cx(buttonClass("ghost", "md"), "gap-1.5")}>
        {t("hl.alerts.mute")}
        <Icon name="chevronRight" size={12} className="block flex-none rotate-90" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="start" className="z-50 outline-none">
          <Menu.Popup className="w-64 origin-(--transform-origin) rounded-card border border-line bg-surface p-1.5 shadow-(--shadow-toast) outline-none transition-[scale,opacity] duration-150 data-ending-style:scale-[0.98] data-ending-style:opacity-0 data-starting-style:scale-[0.98] data-starting-style:opacity-0">
            {muteKeys.map((key, i) => (
              // "until morning" is counted at the click, from the clock of that moment
              <Menu.Item key={key} className={itemClass} onClick={() => onMute(muteChoices(new Date())[i]!.seconds)}>
                {t(key)}
              </Menu.Item>
            ))}
            <p className="mt-1 border-t border-line px-2.5 pt-2 pb-1 text-[11px] leading-snug text-pretty text-muted">{t("hl.mute.hint")}</p>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

/**
 * Restarting from an alert asks first, by name and with who drops now (params.online, counted by the panel when it
 * sent the alert). One profile (a check that does not answer) or every profile of the node (none does). A failure
 * stays in the window, above its buttons.
 */
function RestartDialog({ open, onOpenChange, alert: a, scope }: { open: boolean; onOpenChange: (o: boolean) => void; alert: Alert; scope: "one" | "all" }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const call = useMutation({
    mutationFn: () => nodesApi.restartInbounds({ nodeId: a.nodeId, inboundId: scope === "one" ? (a.params.inbound ?? "") : "" }),
    onSuccess: (r) => {
      onOpenChange(false);
      toast(`${a.nodeName}: ${t.n("node.restarted", r.restarted)}`);
      for (const k of ["health", "node", "nodes"]) void qc.invalidateQueries({ queryKey: [k] });
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const title = scope === "one" ? t("hl.restart.title", { profiles: quoteNames(t, a.params.profile || a.params.inbound || ""), node: a.nodeName }) : t("hl.restart.titleAll", { node: a.nodeName });
  const body = restartConsequence(t, scope, a.params.online) ?? t("hl.fix.disruptive");
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!call.isPending) onOpenChange(o);
        if (!o) setError("");
      }}
      title={title}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={call.isPending} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={call.isPending} onClick={() => call.mutate()}>
            {t("hl.restart.do")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-normal text-pretty">{body}</p>
      {error && <Notice tone="danger">{error}</Notice>}
    </Modal>
  );
}

const historySizes = [20, 50, 100] as const;

function HistoryList({ rows, t, fmt }: { rows: Alert[]; t: T; fmt: Fmt }) {
  const phone = useIsPhone();
  // the whole window (at most 200 rows) arrives at once: the pages are cut here, the position is in the URL
  const paging = usePaging({ sizes: historySizes, defaultSize: 20 });
  const page = clampPage(paging.page, rows.length, paging.size);
  const shown = rows.slice((page - 1) * paging.size, page * paging.size);
  return (
    <section className="rounded-card-lg border border-line bg-surface px-4 py-1.5">
      <SectionLabel as="h2" icon="clock" tone="sand" className="pt-2.5 pb-1.5">
        {t("hl.alerts.history")}
      </SectionLabel>
      {rows.length === 0 && <p className="border-t border-line py-3 text-[13px] text-muted">{t("hl.alerts.historyNone")}</p>}
      {shown.map((h) => {
        const kind = severityKind(h.severity);
        const lasted = Math.max(0, h.resolvedAtUnix - (h.openedUnix || h.firstSeenUnix));
        const sev = (
          <span className="flex items-center gap-[7px] text-xs text-muted">
            <StatusDot kind={kind} />
            {t(severityWord(h.severity))}
          </span>
        );
        const what = (
          <span className="min-w-0">
            {h.nodeName && <b className="mr-1.5 font-mono text-xs">{h.nodeName}</b>}
            {alertTitle(t, h)}
            {lasted > 0 && <span className="ml-1.5 text-xs text-muted">· {t("hl.alerts.lasted", { duration: fmt.duration(lasted) })}</span>}
          </span>
        );
        const how = <span className="text-xs text-muted">{t(resolutionWord(h.resolution))}</span>;
        const when = <span className="text-right font-mono text-[11px] text-muted">{fmt.stamp(h.resolvedAtUnix)}</span>;
        const key = `${h.id}/${h.resolvedAtUnix}`;
        // the phone stacks the row: severity and time on one line, then what happened, then how it ended
        return phone ? (
          <div key={key} className="flex flex-col gap-1 border-t border-line py-2.5 text-[13px]">
            <div className="flex items-center justify-between gap-3">
              {sev}
              {when}
            </div>
            {what}
            {how}
          </div>
        ) : (
          <div key={key} className="grid min-h-[46px] grid-cols-[110px_minmax(0,1fr)_180px_110px] items-center gap-x-3 border-t border-line py-1.5 text-[13px]">
            {sev}
            {what}
            {how}
            {when}
          </div>
        );
      })}
      <Pagination total={rows.length} page={page} size={paging.size} sizes={historySizes} onPage={paging.setPage} onSize={paging.setSize} label={t("hl.alerts.history")} />
    </section>
  );
}
