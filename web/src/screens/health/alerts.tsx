import { Menu } from "@base-ui/react/menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { AlertKind } from "@/gen/mistgate/admin/v1/health_pb";
import { EditionNotice } from "@/components/edition-notice";
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
import { alertFix, alertIpv6, alertTitle, alertWhy, isPeopleAlert, muteChoices, resolutionWord, restartConsequence, severityKind, severityWord, useCan, type Alert } from "@/lib/health";
import { useIsPhone } from "@/lib/media";
import { clampPage, usePaging } from "@/lib/paging";
import { plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { useAcceptDoctor } from "./doctor-parts";
import { FixControl, type FixFlow } from "./fix";

/** Mute for a while, or unmute; the toast offers the opposite as "Undo". */
export function useMute() {
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
          const node = a.nodeName || a.params.user_name || t("hl.alerts.fleetWide");
          if (seconds > 0) {
            const until = res.alert?.mutedUntilUnix || Math.floor(Date.now() / 1000) + seconds;
            toast(t("hl.alerts.mutedToast", { node, time: fmt.stamp(until) }), { undo: () => call.mutate({ id: a.id, seconds: 0 }) });
          } else toast(t("hl.alerts.unmutedToast", { node }));
        },
      },
    );
}

/**
 * The IPv6 side of a country alert, after its title: "IPv6" on the IPv6 alert itself, "reachable over IPv6" on an IPv4 one whose
 * node the IPv6 rounds reach. Nothing on any other alert.
 */
export function Ipv6Tag({ alert, className }: { alert: Alert; className?: string }) {
  const t = useT();
  const v6 = alertIpv6(alert);
  if (v6 === "alert") return <Chip mono className={cx("text-fg", className)}>IPv6</Chip>;
  if (v6 === "open")
    return (
      <Chip className={cx("tone-ok tone-text gap-1", className)}>
        <Icon name="check" size={11} aria-hidden className="flex-none" />
        {t("hl.alerts.v6open")}
      </Chip>
    );
  return null;
}

/**
 * The alerts about nodes: the alerts about people (access ended, a connection that looks broken) are in the People tab, active or
 * closed. The Cloudflare edition evaluates them too, but has no tunnel checks (so none of their alerts): one line says so above them.
 */
export function AlertsTab({ active: all, history: closed, now, flow }: { active: Alert[]; history: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const fmt = useFmt();
  const active = all.filter((a) => !isPeopleAlert(a));
  const history = closed.filter((a) => !isPeopleAlert(a));
  const lastClosed = history[0]?.resolvedAtUnix;
  return (
    <div className="flex flex-col gap-3.5">
      <EditionNotice feature="health_checks">{t("hl.alerts.edgeHint")}</EditionNotice>
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
  // outbound IPv6 is broken on the node: the remedy is the node's "IPv6 for clients" setting, which only the owner changes
  const ipv6Off = Boolean(a.nodeId) && a.params.suggest === "client_ipv6_off" && can.fix;
  const [turningOff, setTurningOff] = useState(false);
  // the main action is the first one this role can take
  const goMain = !(target && can.fix) && !restart && !ipv6Off;
  // an alert about a person: their name stands where a node's would, and the button leads to their page
  const person = isPeopleAlert(a) ? (a.params.user_name ?? "") : "";
  const userId = isPeopleAlert(a) && a.actions.includes("open_user") ? (a.params.user_id ?? "") : "";

  return (
    <article
      id={`alert-${a.id}`}
      className={cx(
        "flex scroll-mt-20 flex-col gap-2.5 rounded-card-lg border bg-surface p-4",
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
        ) : person ? (
          <b className="min-w-0 text-xs font-bold break-words">{person}</b>
        ) : (
          <Chip>{t("hl.alerts.fleetWide")}</Chip>
        )}
        <h3 className="min-w-40 flex-1 text-sm font-bold">
          {alertTitle(t, a)}
          {alertIpv6(a) === "alert" && <Ipv6Tag alert={a} className="ml-2 align-[1px]" />}
        </h3>
        <span className="font-mono text-[11px] text-muted">{t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix))) })}</span>
      </div>
      {why && <p className="text-[13px] leading-normal text-pretty text-muted">{why}</p>}
      {alertIpv6(a) === "open" && (
        <p data-testid="alert-v6-open" className="tone-ok tone-text flex items-center gap-1.5 text-[13px] font-semibold">
          <Icon name="check" size={13} aria-hidden className="flex-none" />
          {t("hl.alerts.v6openLong")}
        </p>
      )}
      {muted && <p className="font-mono text-[11px] text-faint">{t("hl.alerts.muted", { time: fmt.stamp(a.mutedUntilUnix) })}</p>}
      <div className="flex flex-wrap items-center gap-1.5">
        {target && <FixControl flow={flow} target={target} variant="primary" />}
        {restart && (
          <Button variant={target && can.fix ? "secondary" : "primary"} size="md" onClick={() => setRestarting(true)}>
            {restart === "one" ? t("hl.fix.restartNamed", { profile: a.params.profile || a.params.inbound || "" }) : t("hl.alerts.restartAll")}
          </Button>
        )}
        {ipv6Off && (
          <Button variant={(target && can.fix) || restart ? "secondary" : "primary"} size="md" onClick={() => setTurningOff(true)}>
            {t("hl.ipv6Off.button")}
          </Button>
        )}
        {go === "profiles" && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "profiles" }} className={buttonClass(goMain ? "primary" : "secondary", "md")}>
            {t("hl.alerts.openProfiles")}
          </Link>
        )}
        {go === "warp" && (
          <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "exit" }} hash="warp" className={buttonClass(goMain ? "primary" : "secondary", "md")}>
            {t("warp.open")}
          </Link>
        )}
        {userId && (
          <Link to="/users/$id" params={{ id: userId }} className={buttonClass("primary", "md")}>
            {t("hl.alerts.openUser")}
          </Link>
        )}
        {a.kind === AlertKind.RELAY_DOWN && a.nodeId && <RelayWayLink a={a} primary={goMain} />}
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
      {ipv6Off && <ClientIPv6OffDialog open={turningOff} onOpenChange={setTurningOff} alert={a} />}
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
      toast(t("common.labeled", { label: a.nodeName, value: t.n("node.restarted", r.restarted) }));
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

/** Turns the node's "IPv6 for clients" off (the same switch as in the node's settings); the agent re-runs its IPv6 check. */
function ClientIPv6OffDialog({ open, onOpenChange, alert: a }: { open: boolean; onOpenChange: (o: boolean) => void; alert: Alert }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const call = useMutation({
    mutationFn: () => nodesApi.updateNode({ nodeId: a.nodeId, clientIpv6: false }),
    onSuccess: () => {
      onOpenChange(false);
      toast(t("hl.ipv6Off.done", { node: a.nodeName }));
      for (const k of ["health", "node", "nodes"]) void qc.invalidateQueries({ queryKey: [k] });
    },
    onError: (e) => setError(errorText(e, t)),
  });
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!call.isPending) onOpenChange(o);
        if (!o) setError("");
      }}
      title={t("hl.ipv6Off.title", { node: a.nodeName })}
      footer={
        <>
          <Button variant="ghost" size="md" disabled={call.isPending} onClick={() => onOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={call.isPending} onClick={() => call.mutate()}>
            {t("hl.ipv6Off.do")}
          </Button>
        </>
      }
    >
      <p className="text-[13px] leading-normal text-pretty">{t("hl.ipv6Off.body")}</p>
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
            <Ipv6Tag alert={h} className="ml-1.5 align-[1px]" />
            {lasted > 0 && <span className="ml-1.5 text-xs text-muted">· {t("hl.alerts.lasted", { duration: fmt.duration(lasted) })}</span>}
          </span>
        );
        const how = <span className="text-xs text-muted">{t(resolutionWord(h.resolution))}</span>;
        const when = <span className="text-right font-mono text-[11px] text-muted">{fmt.stamp(h.resolvedAtUnix)}</span>;
        const key = `${h.id}/${h.resolvedAtUnix}`;
        // the phone stacks the row: severity and time on one line, then what happened, then how it ended
        return phone ? (
          <div key={key} id={`alert-${h.id}-${h.resolvedAtUnix}`} className="flex scroll-mt-20 flex-col gap-1 border-t border-line py-2.5 text-[13px]">
            <div className="flex items-center justify-between gap-3">
              {sev}
              {when}
            </div>
            {what}
            {how}
          </div>
        ) : (
          <div
            key={key}
            id={`alert-${h.id}-${h.resolvedAtUnix}`}
            className="grid min-h-[46px] scroll-mt-20 grid-cols-[110px_minmax(0,1fr)_180px_110px] items-center gap-x-3 border-t border-line py-1.5 text-[13px]"
          >
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

/**
 * A relay_down alert's way to the fix: the exit's Ways in, on the lane through this entry (the alert's node). The alert
 * names the exit, and node names are unique; without the list yet, the entry's own ways in.
 */
function RelayWayLink({ a, primary }: { a: Alert; primary: boolean }) {
  const t = useT();
  const name = (a.params.exit ?? "").toLowerCase();
  const exit = useQuery(nodesQuery).data?.nodes.find((n) => n.name.toLowerCase() === name);
  return (
    <Link
      to="/nodes/$id"
      params={{ id: exit?.id ?? a.nodeId }}
      search={exit ? { tab: "ways", way: `via:${a.nodeId}` } : { tab: "ways" }}
      className={buttonClass(primary ? "primary" : "secondary", "md")}
    >
      {t("hl.alerts.openWay")}
    </Link>
  );
}
