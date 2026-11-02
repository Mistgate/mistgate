import { Menu } from "@base-ui/react/menu";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useId, useMemo, useState, type ReactNode } from "react";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { EditionNotice } from "@/components/edition-notice";
import { Chip, SectionLabel } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusDot, kindTextClass } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { health, nodes as nodesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { quoteNames } from "@/lib/doctor-detail";
import { errorText } from "@/lib/errors";
import { dayLabel, useFmt } from "@/lib/format";
import {
  NETS_INLINE,
  alertFix,
  alertIpv6,
  alertNetworks,
  alertTitle,
  alertWhy,
  groupAlerts,
  groupFact,
  groupHistory,
  groupSeverity,
  groupTitle,
  isPeopleAlert,
  muteChoices,
  networksByOperator,
  resolutionWord,
  restartConsequence,
  severityKind,
  severityWord,
  useCan,
  type Alert,
} from "@/lib/health";
import { plain } from "@/lib/plain";
import { lossPct, senderLabel } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { entryOffered } from "@/lib/ways";
import { useAcceptDoctor } from "./doctor-parts";
import { FixControl, type FixFlow } from "./fix";

/** Mute for a while, or unmute, one alert or a card's several at once; the toast offers the opposite as "Undo". */
export function useMute() {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const call = useMutation({
    mutationFn: (v: { ids: string[]; seconds: number }) => Promise.all(v.ids.map(async (id) => plain(await health.muteAlert({ alertId: id, durationS: v.seconds })))),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ["health", "alerts"] });
      void qc.invalidateQueries({ queryKey: ["overview"] });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (alerts: readonly Alert[], seconds: number) => {
    const a = alerts[0]!;
    const ids = alerts.map((x) => x.id);
    const n = ids.length;
    call.mutate(
      { ids, seconds },
      {
        onSuccess: (res) => {
          const node = a.nodeName || a.params.user_name || t("hl.alerts.fleetWide");
          if (seconds > 0) {
            const time = fmt.stamp(res[0]?.alert?.mutedUntilUnix || Math.floor(Date.now() / 1000) + seconds);
            const text = n > 1 ? t.n("hl.alerts.mutedToastMany", n, { node, time }) : t("hl.alerts.mutedToast", { node, time });
            toast(text, { undo: () => call.mutate({ ids, seconds: 0 }) });
          } else toast(n > 1 ? t.n("hl.alerts.unmutedToastMany", n, { node }) : t("hl.alerts.unmutedToast", { node }));
        },
      },
    );
  };
}

/**
 * The IPv6 side of a country alert, after its title: "IPv6" on the IPv6 alert itself, "reachable over IPv6" on an IPv4 one whose
 * node the IPv6 rounds reach, "IPv6 entry on" on one whose node has its IPv6 entry on. Nothing on any other alert.
 */
export function Ipv6Tag({ alert, className }: { alert: Alert; className?: string }) {
  const t = useT();
  const v6 = alertIpv6(alert);
  if (v6 === "alert") return <Chip mono className={cx("text-fg", className)}>IPv6</Chip>;
  if (v6 === "open" || v6 === "entry")
    return (
      <Chip className={cx("tone-ok tone-text gap-1", className)}>
        <Icon name="check" size={11} aria-hidden className="flex-none" />
        {t(v6 === "open" ? "hl.alerts.v6open" : "hl.alerts.v6entry")}
      </Chip>
    );
  return null;
}

/** A link from Reachability (/health#alert-<id>, a history row's #alert-<id>-<closed>) opens what it points at. */
const targeted = (ids: readonly string[]) => typeof location !== "undefined" && ids.some((id) => location.hash === `#${id}`);
const cardBorder = (bad: boolean) => (bad ? "border-[color-mix(in_oklch,var(--danger)_40%,var(--border))]" : "border-line");

/**
 * The alerts about nodes: the alerts about people (access ended, a connection that looks broken) are in the People tab, active or
 * closed. One folded card per problem (the per-profile alerts of one node together), the muted ones in their own fold at the end,
 * then the history by event. The Cloudflare edition evaluates them too, but has no tunnel checks (so none of their alerts): one
 * line says so above them.
 */
export function AlertsTab({ active: all, history: closed, now, flow }: { active: Alert[]; history: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const fmt = useFmt();
  const active = all.filter((a) => !isPeopleAlert(a));
  const live = groupAlerts(active.filter((a) => a.mutedUntilUnix <= now));
  const muted = groupAlerts(active.filter((a) => a.mutedUntilUnix > now));
  const history = closed.filter((a) => !isPeopleAlert(a));
  const lastClosed = history[0]?.resolvedAtUnix;
  return (
    // the cards and rows lay out by the width of the tab, not of the window (the sidebar takes a share of a mid screen)
    <div className="@container flex flex-col gap-3.5">
      <EditionNotice feature="health_checks">{t("hl.alerts.edgeHint")}</EditionNotice>
      {live.length === 0 ? (
        <div className="flex flex-wrap items-center gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5 text-[13px]">
          <StatusDot kind="ok" />
          <b>{t("hl.alerts.quiet")}</b>
          <span className="text-muted">{lastClosed ? t("hl.alerts.quietLast", { ago: fmt.ago(lastClosed) }) : t("hl.alerts.quietNone")}</span>
        </div>
      ) : (
        <div className="flex flex-col gap-2">
          {live.map((g) => (
            <AlertGroup key={g[0]!.id} alerts={g} now={now} flow={flow} />
          ))}
        </div>
      )}
      {muted.length > 0 && <MutedAlerts groups={muted} now={now} flow={flow} />}
      <HistoryList rows={history} />
    </div>
  );
}

function AlertGroup({ alerts, now, flow }: { alerts: Alert[]; now: number; flow: FixFlow }) {
  return alerts.length === 1 ? <AlertCard alert={alerts[0]!} now={now} flow={flow} fold /> : <GroupCard alerts={alerts} now={now} flow={flow} />;
}

/** "Muted · 2": folded under the live alerts, opened by a click (or a link to one of them). */
function MutedAlerts({ groups, now, flow }: { groups: Alert[][]; now: number; flow: FixFlow }) {
  const t = useT();
  const id = useId();
  const [open, setOpen] = useState(() => targeted(groups.flat().map((a) => `alert-${a.id}`)));
  return (
    <section className="flex flex-col gap-2">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        data-muted-toggle
        onClick={() => setOpen(!open)}
        className="group flex min-h-9 w-fit items-center gap-1.5 rounded-ctl pe-1.5 transition-colors"
      >
        <SectionLabel as="span" icon="bell" tone="lavender" className="group-hover:text-fg">
          {t("hl.alerts.mutedSection", { n: groups.flat().length })}
        </SectionLabel>
        <Icon name="chevronDown" size={12} aria-hidden className={cx("flex-none text-muted transition-transform duration-200", open && "rotate-180")} />
      </button>
      {open && (
        <div id={id} className="flex flex-col gap-2">
          {groups.map((g) => (
            <AlertGroup key={g[0]!.id} alerts={g} now={now} flow={flow} />
          ))}
        </div>
      )}
    </section>
  );
}

/**
 * The line of a folded card: severity, node, title, the card's number and its age (or until when it is muted); the whole line
 * opens and closes the card. On a narrow tab the title takes the second line.
 */
function FoldHead({ alerts, title, tag, open, onToggle, controls, now }: { alerts: readonly Alert[]; title: string; tag?: ReactNode; open: boolean; onToggle: () => void; controls: string; now: number }) {
  const t = useT();
  const fmt = useFmt();
  const a = alerts[0]!;
  const sev = groupSeverity(alerts);
  const kind = severityKind(sev);
  const fact = groupFact(t, alerts);
  const since = Math.min(...alerts.map((x) => x.openedUnix || x.firstSeenUnix));
  const when =
    a.mutedUntilUnix > now
      ? t("hl.alerts.until", { time: fmt.stamp(Math.max(...alerts.map((x) => x.mutedUntilUnix))) })
      : t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - since)) });
  return (
    <h3>
      <button
        type="button"
        aria-expanded={open}
        aria-controls={controls}
        data-alert-toggle
        onClick={onToggle}
        className="group flex min-h-12 w-full flex-wrap items-center gap-x-3 gap-y-1.5 rounded-[inherit] px-4 py-2.5 text-start"
      >
        <span className={cx("flex w-[84px] flex-none items-center gap-[7px] text-xs font-bold", kindTextClass[kind])}>
          <StatusDot kind={kind} />
          {t(severityWord(sev))}
        </span>
        {/* a short node name takes the same width, so the titles of the cards start in one line */}
        {a.nodeName ? (
          <Chip mono className="min-w-11 justify-center text-fg">
            {a.nodeName}
          </Chip>
        ) : (
          <Chip className="min-w-11 justify-center">{t("hl.alerts.fleetWide")}</Chip>
        )}
        <span className="order-last min-w-0 basis-full text-sm font-bold text-pretty @2xl:order-none @2xl:flex-1 @2xl:basis-0">
          {title}
          {tag}
          {fact && <span className="ms-2 font-mono text-[11px] font-normal whitespace-nowrap text-muted @2xl:hidden">{fact}</span>}
        </span>
        {fact && <span className="font-mono text-[11px] whitespace-nowrap text-muted @max-2xl:hidden">{fact}</span>}
        <span className="ms-auto font-mono text-[11px] whitespace-nowrap text-muted @2xl:ms-0">{when}</span>
        <Icon name="chevronDown" size={14} aria-hidden className={cx("flex-none text-muted transition-transform duration-200 group-hover:text-fg", open && "rotate-180")} />
      </button>
    </h3>
  );
}

/**
 * One active alert: severity, node, what is wrong and why, then what to do about it. `fold` (the Alerts tab) shows it as one line
 * that opens to the rest; a critical alert, or one a link points at, starts open. `onNodePage`: it sits on its own node's page, so
 * the node is not named or linked.
 */
export function AlertCard({ alert: a, now, flow, onNodePage = false, fold = false }: { alert: Alert; now: number; flow: FixFlow; onNodePage?: boolean; fold?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const body = useId();
  const [open, setOpen] = useState(() => !fold || a.severity === AlertSeverity.CRITICAL || targeted([`alert-${a.id}`]));
  const kind = severityKind(a.severity);
  const muted = a.mutedUntilUnix > now;
  const v6tag = alertIpv6(a) === "alert";
  // an alert about a person: their name stands where a node's would
  const person = isPeopleAlert(a) ? (a.params.user_name ?? "") : "";
  return (
    <article
      id={`alert-${a.id}`}
      className={cx("flex scroll-mt-20 flex-col rounded-card-lg border bg-surface", !fold && "gap-2.5 p-4", cardBorder(kind === "bad"), !fold && muted && "opacity-75")}
    >
      {fold ? (
        <FoldHead alerts={[a]} title={alertTitle(t, a)} tag={v6tag && <Ipv6Tag alert={a} className="ms-2 align-[1px]" />} open={open} onToggle={() => setOpen(!open)} controls={body} now={now} />
      ) : (
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
            {v6tag && <Ipv6Tag alert={a} className="ms-2 align-[1px]" />}
          </h3>
          <span className="font-mono text-[11px] text-muted">{t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix))) })}</span>
        </div>
      )}
      {open && <AlertBody id={body} a={a} now={now} flow={flow} onNodePage={onNodePage} part={fold ? "fold" : "card"} className={fold ? "px-4 pb-4" : undefined} />}
    </article>
  );
}

/**
 * What an alert says and what can be done about it. The one primary button is the fix itself (apply it, restart, turn client
 * IPv6 off, add the IPv6 entry); where to fix it is a secondary button, the rest are quiet, "Mute" a menu on the right.
 * `part`: "card" everything; "fold" the same without the muted line (the fold's line says until when); "row" an alert inside a
 * card of several: only its own buttons, the card has the shared ones.
 */
function AlertBody({ a, now, flow, onNodePage, part, id, className }: { a: Alert; now: number; flow: FixFlow; onNodePage: boolean; part: "card" | "fold" | "row"; id?: string; className?: string }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const mute = useMute();
  const accept = useAcceptDoctor();
  const [restarting, setRestarting] = useState(false);
  const fix = alertFix(a);
  const muted = a.mutedUntilUnix > now;
  const why = alertWhy(t, fmt, a);
  const inbound = a.params.inbound_id ?? "";
  const shared = part !== "row";
  // the same key as the doctor row of this check, so a fix started from either shows its progress on both
  const check = a.params.check ?? a.subject;
  const target = fix && a.nodeId ? { ...fix, nodeId: a.nodeId, nodeName: a.nodeName, key: `${a.nodeId}/${check}/${inbound}`, profile: a.params.profile } : null;
  const restart = a.nodeId && can.run ? (a.actions.includes("restart_inbound") && a.params.inbound ? "one" : a.actions.includes("restart_inbounds") ? "all" : null) : null;
  const go = a.nodeId && shared ? (a.actions.includes("open_profiles") ? "profiles" : a.actions.includes("open_warp") ? "warp" : null) : null;
  // outbound IPv6 is broken on the node: the remedy is the node's "IPv6 for clients" setting, which only the owner changes
  const ipv6Off = Boolean(a.nodeId) && a.params.suggest === "client_ipv6_off" && can.fix;
  const [turningOff, setTurningOff] = useState(false);
  // IPv4 is cut from the country while IPv6 gets through: the owner may add the node's IPv6 entry (ipv6 plan §3.8)
  const v6 = alertIpv6(a);
  const entryNode = useQuery({ ...nodesQuery, enabled: v6 === "open" && can.fix && !!a.nodeId }).data?.nodes.find((n) => n.id === a.nodeId);
  const addEntry = v6 === "open" && can.fix && !!entryNode?.ipv6 && entryOffered(entryNode.ipv6);
  // the fix that is the main action: the first one this role can take
  const fixMain = !(target && can.fix) && !restart && !ipv6Off;
  const acceptable = can.run && a.actions.includes("accept") && !!a.params.check;
  // an alert about a person: the button leads to their page
  const userId = isPeopleAlert(a) && a.actions.includes("open_user") ? (a.params.user_id ?? "") : "";
  const ownButtons = !!target || !!restart || ipv6Off || addEntry || acceptable;

  return (
    <div id={id} className={cx("flex flex-col gap-2.5", className)}>
      {why && <p className="text-[13px] leading-normal text-pretty text-muted">{why}</p>}
      {a.kind === AlertKind.COUNTRY_BLOCKED && <Networks list={a.params.networks ?? ""} />}
      {(v6 === "open" || v6 === "entry") && (
        <p data-testid={`alert-v6-${v6}`} className="tone-ok tone-text flex items-center gap-1.5 text-[13px] font-semibold">
          <Icon name="check" size={13} aria-hidden className="flex-none" />
          {t(v6 === "open" ? "hl.alerts.v6openLong" : "hl.alerts.v6entryLong")}
        </p>
      )}
      {muted && part === "card" && <p className="font-mono text-[11px] text-faint">{t("hl.alerts.muted", { time: fmt.stamp(a.mutedUntilUnix) })}</p>}
      {(shared || ownButtons) && (
        <div className={actionRow}>
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
          {addEntry && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "ways", way: "ipv6", add: "ipv6" }} className={buttonClass(fixMain ? "primary" : "secondary", "md")}>
              {t("ways.ipv6.add")}
            </Link>
          )}
          {go === "profiles" && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "profiles" }} className={buttonClass("secondary", "md")}>
              {t("hl.alerts.openProfiles")}
            </Link>
          )}
          {go === "warp" && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "exit" }} hash="warp" className={buttonClass("secondary", "md")}>
              {t("warp.open")}
            </Link>
          )}
          {userId && (
            <Link to="/users/$id" params={{ id: userId }} className={buttonClass("primary", "md")}>
              {t("hl.alerts.openUser")}
            </Link>
          )}
          {shared && a.kind === AlertKind.RELAY_DOWN && a.nodeId && <RelayWayLink a={a} />}
          {shared && a.kind === AlertKind.UPDATE_FAILED && (
            <Link to="/updates" className={buttonClass("secondary", "md")}>
              {t("up.alert.open")}
            </Link>
          )}
          {shared && a.nodeId && a.actions.includes("open_node") && !go && !onNodePage && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} className={buttonClass("ghost", "md")}>
              {t("hl.alerts.openNode")}
            </Link>
          )}
          {shared && a.nodeId && a.params.check && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "doctor" }} className={buttonClass("ghost", "md")}>
              {t("hl.alerts.openDoctor")}
            </Link>
          )}
          {acceptable && (
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
          {shared && can.run && a.actions.includes("mute") && <MuteControl muted={muted} onMute={(seconds) => mute([a], seconds)} />}
        </div>
      )}
      {restart && <RestartDialog open={restarting} onOpenChange={setRestarting} alert={a} scope={restart} />}
      {ipv6Off && <ClientIPv6OffDialog open={turningOff} onOpenChange={setTurningOff} alert={a} />}
    </div>
  );
}

/** A card's row of buttons; a quiet one that leads it starts in line with the text above (its padding pulled back). */
const actionRow = "flex flex-wrap items-center gap-1.5 [&>.bg-transparent:first-child]:-ms-3.5";

/** "Mute ▾", or "Unmute" on a muted alert; pushed to the right end of its row of buttons. */
function MuteControl({ muted, onMute }: { muted: boolean; onMute: (seconds: number) => void }) {
  const t = useT();
  return (
    <span className="ms-auto flex">
      {muted ? (
        <Button variant="ghost" size="md" onClick={() => onMute(0)}>
          {t("hl.alerts.unmute")}
        </Button>
      ) : (
        <MuteMenu onMute={onMute} />
      )}
    </span>
  );
}

/**
 * Several alerts of one kind on one node (a port per profile that loses packets, profiles that fail the check): one line, then a
 * row per profile, the buttons they share once.
 */
function GroupCard({ alerts, now, flow }: { alerts: Alert[]; now: number; flow: FixFlow }) {
  const t = useT();
  const can = useCan();
  const mute = useMute();
  const body = useId();
  const first = alerts[0]!;
  const [open, setOpen] = useState(() => groupSeverity(alerts) === AlertSeverity.CRITICAL || targeted(alerts.map((a) => `alert-${a.id}`)));
  const has = (action: string) => alerts.some((a) => a.actions.includes(action));
  const muted = first.mutedUntilUnix > now;
  return (
    <article id={`alert-${first.id}`} className={cx("flex scroll-mt-20 flex-col rounded-card-lg border bg-surface", cardBorder(severityKind(groupSeverity(alerts)) === "bad"))}>
      {/* the other alerts of the card keep their anchors */}
      {alerts.slice(1).map((a) => (
        <span key={a.id} id={`alert-${a.id}`} className="scroll-mt-20" />
      ))}
      <FoldHead alerts={alerts} title={groupTitle(t, alerts)} open={open} onToggle={() => setOpen(!open)} controls={body} now={now} />
      {open && (
        <div id={body} className="flex flex-col gap-3 px-4 pb-4">
          {first.kind === AlertKind.PORT_LOSSY ? (
            <>
              <p className="text-[13px] leading-normal text-pretty text-muted">{t("hl.alerts.group.port_lossy.why")}</p>
              <LossTable alerts={alerts} />
            </>
          ) : (
            <ul className="flex flex-col divide-y divide-line rounded-field border border-line">
              {alerts.map((a) => (
                <li key={a.id} className="flex flex-col gap-1.5 px-3 py-2.5">
                  <b className="text-[13px]">{alertTitle(t, a)}</b>
                  <AlertBody a={a} now={now} flow={flow} onNodePage={false} part="row" />
                </li>
              ))}
            </ul>
          )}
          <div className={actionRow}>
            {has("open_profiles") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} search={{ tab: "profiles" }} className={buttonClass("secondary", "md")}>
                {t("hl.alerts.openProfiles")}
              </Link>
            )}
            {has("open_warp") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} search={{ tab: "exit" }} hash="warp" className={buttonClass("secondary", "md")}>
                {t("warp.open")}
              </Link>
            )}
            {has("open_node") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} className={buttonClass("ghost", "md")}>
                {t("hl.alerts.openNode")}
              </Link>
            )}
            {can.run && has("mute") && <MuteControl muted={muted} onMute={(seconds) => mute(alerts, seconds)} />}
          </div>
        </div>
      )}
    </article>
  );
}

/** The ports of a node that lose packets: port, profile, the share lost and where it was checked from. */
function LossTable({ alerts }: { alerts: Alert[] }) {
  const t = useT();
  const th = "px-3 py-2 font-semibold";
  return (
    <div className="overflow-x-auto rounded-field border border-line">
      <table className="w-full text-[13px]">
        <thead className="text-start text-[11px] text-muted">
          <tr className="border-b border-line">
            <th className={th}>{t("hl.alerts.col.port")}</th>
            <th className={th}>{t("hl.alerts.col.profile")}</th>
            <th className={cx(th, "text-end")}>{t("hl.alerts.col.lost")}</th>
            <th className={th}>{t("hl.alerts.col.from")}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-line">
          {alerts.map((a) => (
            <tr key={a.id}>
              <td className="px-3 py-2 font-mono font-bold">{a.params.port}</td>
              <td className="px-3 py-2">{a.params.profile}</td>
              <td className="px-3 py-2 text-end font-mono whitespace-nowrap text-warn-text tabular-nums">{lossPct(Number(a.params.sent), Number(a.params.got))} %</td>
              <td className="px-3 py-2 text-muted">{senderLabel(t, a.params.sender ?? "")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/** Every network of a country alert, by operator, behind "Show all N" when the sentence names only the first operators. */
function Networks({ list }: { list: string }) {
  const t = useT();
  const id = useId();
  const [open, setOpen] = useState(false);
  const nets = alertNetworks(list);
  if (nets.length <= NETS_INLINE) return null;
  return (
    <div className="flex flex-col gap-2">
      <button
        type="button"
        aria-expanded={open}
        aria-controls={id}
        data-nets-toggle
        onClick={() => setOpen(!open)}
        className="w-fit rounded-sm text-xs font-semibold text-muted underline decoration-dotted underline-offset-2 hover:text-fg"
      >
        {open ? t("cr.part.fewer") : t("hl.nets.all", { n: nets.length })}
      </button>
      {open && (
        // a phone puts the operator above its networks, so a network's tag keeps one line
        <dl id={id} className="grid grid-cols-1 items-baseline gap-x-4 gap-y-1 rounded-field bg-surface-2 px-3 py-2.5 text-xs @md:grid-cols-[max-content_minmax(0,1fr)] @md:gap-y-2">
          {networksByOperator(nets).map((o) => (
            <Fragment key={o.name}>
              <dt className="font-semibold @max-md:pt-1.5 @max-md:first:pt-0">{o.name}</dt>
              <dd className="flex flex-wrap gap-1">
                {o.nets.map((n, i) => (
                  <span key={i} className="max-w-full rounded-[6px] bg-surface px-1.5 py-0.5 font-mono text-[11px] break-words text-muted">
                    {n.tag || "—"}
                  </span>
                ))}
              </dd>
            </Fragment>
          ))}
        </dl>
      )}
    </div>
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
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
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

const HISTORY_STEP = 20;
const historyId = (h: Alert) => `alert-${h.id}-${h.resolvedAtUnix}`;

/**
 * The closed alerts of the window (at most 200), one row per event under its day, newest first: the latest 20, "Show N more"
 * for the rest. A link to a row further down shows enough to reach it.
 */
function HistoryList({ rows }: { rows: Alert[] }) {
  const t = useT();
  const fmt = useFmt();
  const groups = useMemo(() => groupHistory(rows), [rows]);
  const [count, setCount] = useState(() => Math.max(HISTORY_STEP, groups.findIndex((g) => targeted(g.map(historyId))) + 1));
  const days: { label: string; groups: Alert[][] }[] = [];
  for (const g of groups.slice(0, count)) {
    const label = dayLabel(t, fmt, g[0]!.resolvedAtUnix);
    const day = days.at(-1);
    if (day?.label === label) day.groups.push(g);
    else days.push({ label, groups: [g] });
  }
  const more = Math.min(HISTORY_STEP, groups.length - count);
  return (
    <section className="rounded-card-lg border border-line bg-surface px-4 pt-1.5 pb-1">
      <SectionLabel as="h2" icon="clock" tone="sand" className="pt-2.5 pb-1.5">
        {t("hl.alerts.history")}
      </SectionLabel>
      {rows.length === 0 && <p className="border-t border-line py-3 text-[13px] text-muted">{t("hl.alerts.historyNone")}</p>}
      {days.map((d) => (
        <div key={d.label} className="border-t border-line">
          <h3 className="pt-3 pb-0.5 text-xs font-bold text-muted">{d.label}</h3>
          <ul>
            {d.groups.map((g) => (
              <HistoryRow key={historyId(g[0]!)} g={g} />
            ))}
          </ul>
        </div>
      ))}
      {more > 0 && (
        <div className="border-t border-line py-2">
          {/* the label lines up with the times of the rows */}
          <Button variant="ghost" size="sm" className="-ms-[11px]" onClick={() => setCount(count + HISTORY_STEP)}>
            {t("hl.alerts.more", { n: more })}
          </Button>
        </div>
      )}
    </section>
  );
}

/** How an episode ended, as a quiet glyph with its word in the tooltip; "fixed" in the colour of what works. */
const endings: Record<string, { icon: IconName; cls: string }> = {
  cleared: { icon: "refresh", cls: "text-muted" },
  node_returned: { icon: "refresh", cls: "text-muted" },
  fix_applied: { icon: "check", cls: "tone-ok tone-text" },
  accepted: { icon: "check", cls: "text-muted" },
  superseded: { icon: "layers", cls: "text-muted" },
  node_retired: { icon: "x", cls: "text-muted" },
};

function Ending({ resolution }: { resolution: string }) {
  const t = useT();
  const word = t(resolutionWord(resolution));
  const e = endings[resolution] ?? { icon: "info", cls: "text-muted" };
  return (
    <Tip label={word}>
      <span tabIndex={0} role="img" aria-label={word} className={cx("flex size-6 items-center justify-center rounded-ctl", e.cls)}>
        <Icon name={e.icon} size={13} aria-hidden />
      </span>
    </Tip>
  );
}

/** The nodes of a history row as chips, the first four and "+N". */
function NodeChips({ names }: { names: string[] }) {
  const t = useT();
  return (
    <>
      {names.slice(0, 4).map((n) => (
        <Chip key={n} mono className="min-w-11 justify-center text-fg">
          {n}
        </Chip>
      ))}
      {names.length > 4 && <Chip mono>{t("hl.alerts.nodesMore", { n: names.length - 4 })}</Chip>}
    </>
  );
}

/**
 * One event of the history: when it closed, severity, the nodes, what it was and how long, how it ended. The episodes of an event
 * on several nodes or ports open under it, each with its own anchor.
 */
function HistoryRow({ g }: { g: Alert[] }) {
  const t = useT();
  const fmt = useFmt();
  const id = useId();
  const many = g.length > 1;
  const [open, setOpen] = useState(() => many && targeted(g.map(historyId)));
  const h = g[0]!;
  const sev = groupSeverity(g);
  const lasted = (x: Alert) => Math.max(0, x.resolvedAtUnix - (x.openedUnix || x.firstSeenUnix));
  const longest = Math.max(...g.map(lasted));
  // the episodes under the row name what differs: the port when the titles differ, else only the node
  const oneTitle = new Set(g.map((x) => alertTitle(t, x))).size === 1;
  const line = (
    <>
      <span className="pt-[3px] font-mono text-[11px] text-muted tabular-nums">{fmt.clock(h.resolvedAtUnix)}</span>
      {/* on a phone the chips take the first line and the title the next */}
      <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <StatusDot kind={severityKind(sev)} />
        <span className="sr-only">{t(severityWord(sev))}</span>
        <NodeChips names={[...new Set(g.map((x) => x.nodeName).filter(Boolean))]} />
        <span className="min-w-0 @max-md:basis-full">
          {groupTitle(t, g)}
          <Ipv6Tag alert={h} className="ms-1.5 align-[1px]" />
          {longest > 0 && <span className="ms-2 text-xs whitespace-nowrap text-muted">{t("hl.alerts.lasted", { duration: fmt.duration(longest) })}</span>}
        </span>
      </span>
      {many && <Icon name="chevronDown" size={14} aria-hidden className={cx("mt-1 flex-none text-muted transition-transform duration-200 group-hover:text-fg", open && "rotate-180")} />}
    </>
  );
  const cls = "group grid min-h-[42px] min-w-0 flex-1 grid-cols-[40px_minmax(0,1fr)_auto] items-start gap-x-3 py-2.5 text-start text-[13px]";
  return (
    <li id={many ? undefined : historyId(h)} className="scroll-mt-20 border-t border-line first:border-t-0">
      <div className="flex items-start gap-2">
        {many ? (
          <button type="button" aria-expanded={open} aria-controls={id} data-history-toggle onClick={() => setOpen(!open)} className={cls}>
            {line}
          </button>
        ) : (
          <div className={cls}>{line}</div>
        )}
        <span className="flex flex-none items-center pt-[9px]">
          {[...new Set(g.map((x) => x.resolution))].map((r) => (
            <Ending key={r} resolution={r} />
          ))}
        </span>
      </div>
      {many &&
        (open ? (
          <ul id={id} className="mb-2 ms-[52px] flex flex-col border-s border-line ps-3">
            {g.map((x) => (
              <li key={historyId(x)} id={historyId(x)} className="flex scroll-mt-20 flex-wrap items-center gap-x-2 gap-y-0.5 py-1 text-xs">
                {x.nodeName && (
                  <Chip mono className="text-fg">
                    {x.nodeName}
                  </Chip>
                )}
                {!oneTitle && <span className="min-w-0">{alertTitle(t, x)}</span>}
                <span className="text-muted">{[lasted(x) > 0 && t("hl.alerts.lasted", { duration: fmt.duration(lasted(x)) }), t(resolutionWord(x.resolution))].filter(Boolean).join(" · ")}</span>
                <span className="ms-auto font-mono text-[11px] text-muted tabular-nums">{fmt.clock(x.resolvedAtUnix)}</span>
              </li>
            ))}
          </ul>
        ) : (
          g.map((x) => <span key={historyId(x)} id={historyId(x)} className="block scroll-mt-20" />)
        ))}
    </li>
  );
}

/**
 * A relay_down alert's way to the fix: the exit's Ways in, on the lane through this entry (the alert's node). The alert
 * names the exit, and node names are unique; without the list yet, the entry's own ways in.
 */
function RelayWayLink({ a }: { a: Alert }) {
  const t = useT();
  const name = (a.params.exit ?? "").toLowerCase();
  const exit = useQuery(nodesQuery).data?.nodes.find((n) => n.name.toLowerCase() === name);
  return (
    <Link
      to="/nodes/$id"
      params={{ id: exit?.id ?? a.nodeId }}
      search={exit ? { tab: "ways", way: `via:${a.nodeId}` } : { tab: "ways" }}
      className={buttonClass("secondary", "md")}
    >
      {t("hl.alerts.openWay")}
    </Link>
  );
}
