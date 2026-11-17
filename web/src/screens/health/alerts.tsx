import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { EditionNotice } from "@/components/edition-notice";
import { Flag } from "@/components/ui/flag";
import { Button, buttonClass } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import {
  alertFix,
  alertIpv6,
  alertTitle,
  alertWhy,
  checksQuery,
  fixLabel,
  groupAlerts,
  groupFact,
  groupSeverity,
  groupTitle,
  isLoud,
  isPeopleAlert,
  severityWord,
  useCan,
  type Alert,
} from "@/lib/health";
import { nodesQuery, overviewQuery } from "@/lib/queries";
import { entryOffered } from "@/lib/ways";
import { Ipv6Tag, pillOf, targeted } from "./alert-bits";
import { ClientIPv6OffDialog, CloseAll, LossTable, MuteMenu, Networks, PathRuns, RestartDialog, useClose, useMute } from "./alerts-parts";
import { HistoryList } from "./alerts-history";
import { useAcceptDoctor } from "./doctor-parts";
import { NodeProblems, useProblemNodes } from "./node-problems";
import { useFixWave, type FixFlow } from "./fix";
import { Calm, Dot, Gl, P, Sec } from "./page-parts";
import "./health.css";

// The alerts of the Health page in the board's own rows (PnHealth: .hl-al > .hl-alh + .hl-alb). One line per problem that opens into what it says and what can be
// done about it; the open one is one at a time, as on the board. The per-profile alerts of one node are one row, the muted ones their own fold at the end, the
// closed ones a history by event. What other screens take from here keeps its name: AlertBody (a node's Overview), AlertCard, CloseAll, Ipv6Tag, useMute, useClose,
// MuteMenu (Reachability).

export { CloseAll, MuteMenu, useClose, useMute } from "./alerts-parts";
export { Ipv6Tag } from "./alert-bits";

const anchors = (g: readonly Alert[]) => g.map((a) => `alert-${a.id}`);

/** The head of a row: the verdict, who it is about, what, a figure, how long it has been (or until when it is muted), the chevron. The line opens and closes the row. */
function RowHead({ alerts, title, tag, open, onToggle, controls, now }: { alerts: readonly Alert[]; title: string; tag?: ReactNode; open: boolean; onToggle: () => void; controls: string; now: number }) {
  const t = useT();
  const fmt = useFmt();
  const a = alerts[0]!;
  const sev = groupSeverity(alerts);
  const pill = pillOf(sev);
  const fact = groupFact(t, alerts);
  const since = Math.min(...alerts.map((x) => x.openedUnix || x.firstSeenUnix));
  const person = isPeopleAlert(a) ? (a.params.user_name ?? "") : "";
  const when =
    a.mutedUntilUnix > now ? t("hl.alerts.until", { time: fmt.stamp(Math.max(...alerts.map((x) => x.mutedUntilUnix))) }) : t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - since)) });
  return (
    <h3>
      <button type="button" className="hl-alh" aria-expanded={open} aria-controls={controls} data-alert-toggle onClick={onToggle}>
        <span className={cx("pv-pill", pill.kind)}>
          <Dot kind={pill.glyph} />
          {t(severityWord(sev))}
        </span>
        {a.nodeName ? <span className="hl-who mono">{a.nodeName}</span> : person ? <span className="hl-who mono">{person}</span> : <span className="hl-who">{t("hl.alerts.fleetWide")}</span>}
        <b>
          {title}
          {tag}
        </b>
        {fact && <span className="hl-meta mono">{fact}</span>}
        <em>{when}</em>
        <Gl d={P.chev} className="sb-chev" />
      </button>
    </h3>
  );
}

/**
 * One active alert: a line that opens into what it says and what to do. Open or shut is the list's to say (the board opens one row at a time:
 * `open` and `onToggle`); on its own (a scene, a card) it keeps its own and starts open unless `fold`. `onNodePage`: it sits on its node's page, so the
 * node is not named or linked.
 */
export function AlertCard({
  alert: a,
  now,
  flow,
  onNodePage = false,
  fold = false,
  open,
  onToggle,
  hit,
}: {
  alert: Alert;
  now: number;
  flow: FixFlow;
  onNodePage?: boolean;
  fold?: boolean;
  open?: boolean;
  onToggle?: () => void;
  hit?: boolean;
}) {
  const t = useT();
  const body = useId();
  const [own, setOwn] = useState(() => !fold || a.severity === AlertSeverity.CRITICAL || targeted([`alert-${a.id}`]));
  const isOpen = open ?? own;
  const toggle = onToggle ?? (() => setOwn(!own));
  const muted = a.mutedUntilUnix > now;
  return (
    <article id={`alert-${a.id}`} className={cx("hl-al", isOpen && "open", muted && "mu", hit && "hit")}>
      <RowHead alerts={[a]} title={alertTitle(t, a)} tag={alertIpv6(a) === "alert" ? <Ipv6Tag alert={a} /> : undefined} open={isOpen} onToggle={toggle} controls={body} now={now} />
      {isOpen && <AlertBody id={body} a={a} now={now} flow={flow} onNodePage={onNodePage} part={fold ? "fold" : "card"} />}
    </article>
  );
}

/** A link of the row's own, in the board's secondary button (.btn.sm). */
const linkBtn = (primary = false) => cx(buttonClass(primary ? "primary" : "secondary", "md"), "btn sm");

/**
 * What an alert says and what can be done about it, in the board's body (.hl-alb): the words, then the line of buttons: the fix itself is the one
 * primary button, where to fix it and the "this is normal" are secondary, "Mute" and "Close" quiet links at the end. While a fix runs the line says so,
 * and a failed one stands in a red box with "Try again". `part`: "card" everything; "fold" the same without the muted line (the row says until when); "row"
 * an alert inside a row of several: only its own buttons, the row has the shared ones. A role that cannot write (an observer) reads, and gets the links.
 */
export function AlertBody({ a, now, flow, onNodePage, part, id, className }: { a: Alert; now: number; flow: FixFlow; onNodePage: boolean; part: "card" | "fold" | "row"; id?: string; className?: string }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const mute = useMute();
  const close = useClose();
  const accept = useAcceptDoctor();
  const root = useRef<HTMLDivElement>(null);
  const [restarting, setRestarting] = useState(false);
  const fix = alertFix(a);
  const muted = a.mutedUntilUnix > now;
  const why = alertWhy(t, fmt, a);
  const inbound = a.params.inbound_id ?? "";
  const shared = part !== "row";
  // the same key as the doctor row of this check, so a fix started from either shows its progress on both
  const check = a.params.check ?? a.subject;
  const target = fix && a.nodeId ? { ...fix, nodeId: a.nodeId, nodeName: a.nodeName, key: `${a.nodeId}/${check}/${inbound}`, profile: a.params.profile } : null;
  const phase = target ? flow.phase(target.key) : "idle";
  const failed = target && phase === "idle" ? flow.error(target.key) : "";
  useFixWave(flow, target?.key ?? "", root);
  const restart = a.nodeId && can.run ? (a.actions.includes("restart_inbound") && a.params.inbound ? "one" : a.actions.includes("restart_inbounds") ? "all" : null) : null;
  const go = a.nodeId && shared ? (a.actions.includes("open_profiles") ? "profiles" : a.actions.includes("open_warp") ? "warp" : null) : null;
  // outbound IPv6 is broken on the node: the remedy is the node's "IPv6 for clients" setting, which only the owner changes
  const ipv6Off = Boolean(a.nodeId) && a.params.suggest === "client_ipv6_off" && can.fix;
  const [turningOff, setTurningOff] = useState(false);
  // IPv4 is cut from the country while IPv6 gets through: the owner may add the node's IPv6 entry (ipv6 plan §3.8)
  const v6 = alertIpv6(a);
  const entryNode = useQuery({ ...nodesQuery, enabled: v6 === "open" && can.fix && !!a.nodeId }).data?.nodes.find((n) => n.id === a.nodeId);
  const addEntry = v6 === "open" && can.fix && !!entryNode?.ipv6 && entryOffered(entryNode.ipv6);
  const acceptable = can.run && a.actions.includes("accept") && !!a.params.check;
  // an alert about a person: the button leads to their page
  const userId = isPeopleAlert(a) && a.actions.includes("open_user") ? (a.params.user_id ?? "") : "";
  const doFix = !!target && can.fix;
  // the one primary button: the first remedy this role can take
  const lead = doFix ? "fix" : restart ? "restart" : ipv6Off ? "ipv6" : addEntry ? "entry" : userId ? "user" : "";
  const quiet = phase !== "idle" ? null : failed ? "failed" : "";
  const hasRow = shared || doFix || !!restart || ipv6Off || addEntry || acceptable;

  return (
    <div id={id} ref={root} className={cx("hl-alb hlx @container", className)}>
      {why && <p>{why}</p>}
      {a.kind === AlertKind.COUNTRY_BLOCKED && <Networks list={a.params.networks ?? ""} />}
      {a.kind === AlertKind.PATH_LOSSY && <PathRuns a={a} />}
      {(v6 === "open" || v6 === "entry") && (
        <p data-testid={`alert-v6-${v6}`} className="hl-v6">
          <Gl d={P.check} />
          {t(v6 === "open" ? "hl.alerts.v6openLong" : "hl.alerts.v6entryLong")}
        </p>
      )}
      {muted && part === "card" && <p className="hl-mu">{t("hl.alerts.muted", { time: fmt.stamp(a.mutedUntilUnix) })}</p>}
      {phase === "busy" && (
        <p className="hl-run" role="status">
          <Dot kind="busy" />
          {t("hl.fix.fixing")}
        </p>
      )}
      {phase === "done" && (
        <p className="hl-run ok" role="status">
          <Dot kind="ok" />
          {t("hl.fix.fixed")}
        </p>
      )}
      {failed && target && (
        <div className="rc-pb bad hl-fail hl-shake" role="alert">
          <Dot kind="bad" />
          <div>
            <b>{t("hlp.fail.title")}</b>
            <span>{failed}</span>
          </div>
          <Button variant="secondary" size="md" className="btn sm" onClick={() => flow.start(target)}>
            {t("hlp.retry")}
          </Button>
        </div>
      )}
      {!can.run && shared && <p className="hl-ro">{t("hlp.ro")}</p>}
      {quiet !== null && hasRow && (
        <div className="hl-act">
          {doFix && target && (
            <Button variant="primary" size="md" className="btn sm pri" onClick={() => flow.start(target)}>
              {fixLabel(t, target.fixId, target.profile)}
            </Button>
          )}
          {restart && (
            <Button variant={lead === "restart" ? "primary" : "secondary"} size="md" className="btn sm" onClick={() => setRestarting(true)}>
              {restart === "one" ? t("hl.fix.restartNamed", { profile: a.params.profile || a.params.inbound || "" }) : t("hl.alerts.restartAll")}
            </Button>
          )}
          {ipv6Off && (
            <Button variant={lead === "ipv6" ? "primary" : "secondary"} size="md" className="btn sm" onClick={() => setTurningOff(true)}>
              {t("hl.ipv6Off.button")}
            </Button>
          )}
          {addEntry && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "ways", way: "ipv6", add: "ipv6" }} className={linkBtn(lead === "entry")}>
              {t("ways.ipv6.add")}
            </Link>
          )}
          {go === "profiles" && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "profiles" }} className={linkBtn()}>
              {t("hl.alerts.openProfiles")}
            </Link>
          )}
          {go === "warp" && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "exit" }} hash="warp" className={linkBtn()}>
              {t("warp.open")}
            </Link>
          )}
          {userId && (
            <Link to="/users/$id" params={{ id: userId }} className={linkBtn(lead === "user")}>
              {t("hl.alerts.openUser")}
            </Link>
          )}
          {shared && a.kind === AlertKind.RELAY_DOWN && a.nodeId && <RelayWayLink a={a} />}
          {shared && a.kind === AlertKind.UPDATE_FAILED && (
            <Link to="/updates" className={linkBtn()}>
              {t("up.alert.open")}
            </Link>
          )}
          {shared && a.kind === AlertKind.OS_UPDATE_FAILED && (
            <Link to="/updates" search={{ tab: "os" }} className={linkBtn()}>
              {t("up.alert.open")}
            </Link>
          )}
          {shared && (a.kind === AlertKind.SCHEDULED_CHECK || a.kind === AlertKind.SCHEDULED_TASK_FAILING) && (
            <Link to="/ai" search={{ tab: "tasks", task: a.subject }} className={linkBtn()}>
              {t("task.open")}
            </Link>
          )}
          {shared && a.nodeId && a.actions.includes("open_node") && !go && !onNodePage && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} className={linkBtn()}>
              {t("hl.alerts.openNode")}
            </Link>
          )}
          {shared && a.nodeId && a.params.check && (
            <Link to="/nodes/$id" params={{ id: a.nodeId }} search={{ tab: "doctor" }} className={linkBtn()}>
              {t("hl.alerts.openDoctor")}
            </Link>
          )}
          {acceptable && (
            <Button variant="secondary" size="md" className="btn sm" disabled={accept.busy} title={t("hl.doctor.acceptHint")} onClick={() => accept.run({ nodeId: a.nodeId, nodeName: a.nodeName, checkId: a.params.check! }, true)}>
              {t("hl.doctor.accept")}
            </Button>
          )}
          {shared && can.run && (
            <>
              <span className="hl-sp" />
              {muted ? (
                <button type="button" className="an-lk" onClick={() => mute([a], 0)}>
                  {t("hl.alerts.unmute")}
                </button>
              ) : (
                a.actions.includes("mute") && <MuteMenu onMute={(seconds) => mute([a], seconds)} />
              )}
              <button type="button" className="an-lk" title={t("hl.alerts.closeHint")} onClick={() => close([a])}>
                {t("hl.alerts.close")}
              </button>
            </>
          )}
        </div>
      )}
      {restart && <RestartDialog open={restarting} onOpenChange={setRestarting} alert={a} scope={restart} />}
      {ipv6Off && <ClientIPv6OffDialog open={turningOff} onOpenChange={setTurningOff} alert={a} />}
    </div>
  );
}

/**
 * Several alerts of one kind on one node (a port per profile that loses packets, profiles that fail the check): one row, then a line per profile, the
 * buttons they share once.
 */
function GroupCard({ alerts, now, flow, open, onToggle, hit }: { alerts: Alert[]; now: number; flow: FixFlow; open: boolean; onToggle: () => void; hit: boolean }) {
  const t = useT();
  const can = useCan();
  const mute = useMute();
  const close = useClose();
  const body = useId();
  const first = alerts[0]!;
  const has = (action: string) => alerts.some((a) => a.actions.includes(action));
  const muted = first.mutedUntilUnix > now;
  return (
    <article id={`alert-${first.id}`} className={cx("hl-al", open && "open", muted && "mu", hit && "hit")}>
      {/* the other alerts of the row keep their anchors */}
      {alerts.slice(1).map((a) => (
        <span key={a.id} id={`alert-${a.id}`} className="block scroll-mt-20" />
      ))}
      <RowHead alerts={alerts} title={groupTitle(t, alerts)} open={open} onToggle={onToggle} controls={body} now={now} />
      {open && (
        <div id={body} className="hl-alb hlx @container">
          {first.kind === AlertKind.PORT_LOSSY ? (
            <>
              {/* every port confirmed from a second sender: the node's ports lose them, not the way there */}
              <p>{t(alerts.every((a) => a.params.confirm_sender) ? "hl.alerts.group.port_lossy.why.confirmed" : "hl.alerts.group.port_lossy.why")}</p>
              <div className="hl-tbl">
                <LossTable alerts={alerts} />
              </div>
            </>
          ) : (
            <ul className="hl-sub">
              {alerts.map((a) => (
                <li key={a.id}>
                  <b>{alertTitle(t, a)}</b>
                  <AlertBody a={a} now={now} flow={flow} onNodePage={false} part="row" className="hl-flush" />
                </li>
              ))}
            </ul>
          )}
          <div className="hl-act">
            {has("open_profiles") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} search={{ tab: "profiles" }} className={linkBtn()}>
                {t("hl.alerts.openProfiles")}
              </Link>
            )}
            {has("open_warp") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} search={{ tab: "exit" }} hash="warp" className={linkBtn()}>
                {t("warp.open")}
              </Link>
            )}
            {has("open_node") && (
              <Link to="/nodes/$id" params={{ id: first.nodeId }} className={linkBtn()}>
                {t("hl.alerts.openNode")}
              </Link>
            )}
            {can.run && (
              <>
                <span className="hl-sp" />
                {muted ? (
                  <button type="button" className="an-lk" onClick={() => mute(alerts, 0)}>
                    {t("hl.alerts.unmute")}
                  </button>
                ) : (
                  has("mute") && <MuteMenu onMute={(seconds) => mute(alerts, seconds)} />
                )}
                <button type="button" className="an-lk" title={t("hl.alerts.closeHint")} onClick={() => close(alerts)}>
                  {t("hl.alerts.close")}
                </button>
              </>
            )}
          </div>
        </div>
      )}
    </article>
  );
}

/** The row of an alert or of a group of them, as the list asks it: `open` is the id of the one open row. */
export function AlertRows({ groups, now, flow, open, setOpen, onNodePage = false }: { groups: Alert[][]; now: number; flow: FixFlow; open: string; setOpen: (id: string) => void; onNodePage?: boolean }) {
  return (
    <>
      {groups.map((g) => {
        const id = g[0]!.id;
        const on = open === id;
        const hit = targeted(anchors(g));
        return g.length === 1 ? (
          <AlertCard key={id} alert={g[0]!} now={now} flow={flow} onNodePage={onNodePage} fold open={on} onToggle={() => setOpen(on ? "" : id)} hit={hit} />
        ) : (
          <GroupCard key={id} alerts={g} now={now} flow={flow} open={on} onToggle={() => setOpen(on ? "" : id)} hit={hit} />
        );
      })}
    </>
  );
}

/**
 * Which row is open: the board opens one at a time. Until the person chooses, it is the one a link points at, else the first critical, else the first
 * (as the board opens its first); once chosen, that (or none).
 */
export function useOpenRow(groups: Alert[][]): [string, (id: string) => void] {
  const [chosen, setChosen] = useState<string | null>(null);
  const hash = groups.find((g) => targeted(anchors(g)));
  const crit = groups.find((g) => groupSeverity(g) === AlertSeverity.CRITICAL);
  const first = hash?.[0]?.id ?? crit?.[0]?.id ?? groups[0]?.[0]?.id ?? "";
  return [chosen ?? first, setChosen];
}

/** "Muted · 2": folded under the live alerts, opened by a click (or a link to one of them), in the board's fold (.sb-mv2). */
function MutedAlerts({ groups, now, flow, open, setOpen }: { groups: Alert[][]; now: number; flow: FixFlow; open: string; setOpen: (id: string) => void }) {
  const t = useT();
  const id = useId();
  const [shown, setShown] = useState(() => targeted(groups.flat().map((a) => `alert-${a.id}`)));
  return (
    <section className="card sb-mv2">
      <button type="button" className="sb-mvh" aria-expanded={shown} aria-controls={id} data-muted-toggle onClick={() => setShown(!shown)}>
        <Gl d={P.chev} />
        <span>
          <b>{t("hl.alerts.mutedSection", { n: groups.flat().length })}</b>
        </span>
      </button>
      {shown && (
        <div id={id} className="hl-als">
          <AlertRows groups={groups} now={now} flow={flow} open={open} setOpen={setOpen} />
        </div>
      )}
    </section>
  );
}

/** The side card "By node": a line a node with how many alerts it has, a press picks the node (the list shows only it), a second press lets go. */
function NodesSide({ nodes, counts, node, onPick }: { nodes: { id: string; name: string; countryCode: string }[]; counts: ReadonlyMap<string, number>; node: string; onPick: (id: string) => void }) {
  const t = useT();
  return (
    <Sec tone="sky" icon={P.server} title={t("hlp.nodes.title")}>
      <div className="hl-nodes">
        {nodes.map((n) => {
          const k = counts.get(n.id) ?? 0;
          return (
            <button key={n.id} type="button" aria-pressed={node === n.id} onClick={() => onPick(node === n.id ? "" : n.id)}>
              <Flag code={n.countryCode} size={13} />
              <b className="mono">{n.name}</b>
              <span className={k ? "warn" : ""}>{k ? t.n("hlp.nodes.alerts", k) : t("hlp.nodes.clean")}</span>
            </button>
          );
        })}
      </div>
    </Sec>
  );
}

/** The side card "Checks": how often, when the last round was, and the way to the tab. */
function ChecksSide({ onOpen }: { onOpen?: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const checks = useQuery(checksQuery()).data;
  if (!checks) return null;
  const newest = Math.max(0, ...checks.rows.flatMap((r) => r.cells.map((c) => c.last?.atUnix ?? 0)));
  const min = Math.max(1, Math.round(checks.intervalS / 60));
  return (
    <Sec tone="mint" icon={P.eye} title={t("hl.tab.checks")} className="hl-side-ck">
      <div className="sb-pad">
        <p className="pp-q1">{newest > 0 ? t("hlp.ck.side", { min, ago: fmt.ago(newest) }) : t("hlp.ck.sideNone", { min })}</p>
        {onOpen && (
          <p className="pp-q1">
            <button type="button" className="an-lk" onClick={onOpen}>
              {t("hlp.ck.open")}
            </button>
          </p>
        )}
      </div>
    </Sec>
  );
}

/**
 * The alerts about nodes: the alerts about people are in the People tab. Two columns (the board's): the open ones as rows, the muted ones in a fold, the
 * history of the week; at the side the filter "By node" and the card "Checks". The Cloudflare edition evaluates the alerts too, but has no tunnel checks (so
 * none of their alerts, and no card of checks): one line says so above them.
 */
export function AlertsTab({ active: all, history: closed, now, flow, onChecks, first = false }: { active: Alert[]; history: Alert[]; now: number; flow: FixFlow; onChecks?: () => void; first?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const noChecks = useEdition().off("health_checks");
  const fleet = useQuery(overviewQuery()).data?.nodes;
  const [node, setNode] = useState("");
  const active = all.filter((a) => !isPeopleAlert(a));
  const live = groupAlerts(active.filter((a) => a.mutedUntilUnix <= now));
  const muted = groupAlerts(active.filter((a) => a.mutedUntilUnix > now));
  const history = closed.filter((a) => !isPeopleAlert(a));
  const lastClosed = history[0]?.resolvedAtUnix;
  const shown = node ? live.filter((g) => g[0]!.nodeId === node) : live;
  // nodes that count as a problem and have no alert of their own are named above the alerts (they are in the number of the header)
  const problemNodes = useProblemNodes(new Set(active.filter((a) => isLoud(a, now)).map((a) => a.nodeId)));
  const [open, setOpen] = useOpenRow([...shown, ...muted]);

  // a link to an alert (/health#alert-<id>) opens its row (above) and brings it into view once the list is there
  const scrolled = useRef(false);
  useEffect(() => {
    if (scrolled.current || typeof location === "undefined" || !location.hash.startsWith("#alert-")) return;
    const el = document.getElementById(location.hash.slice(1));
    if (!el) return;
    scrolled.current = true;
    el.scrollIntoView?.({ block: "center" });
  });

  // the nodes of the side card: the fleet, and any node an alert names that the fleet does not know yet
  const nodes = [...(fleet ?? []).map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode }))];
  for (const a of active) if (a.nodeId && !nodes.some((n) => n.id === a.nodeId)) nodes.push({ id: a.nodeId, name: a.nodeName, countryCode: "" });
  const counts = new Map<string, number>();
  for (const a of active) if (a.nodeId && a.mutedUntilUnix <= now) counts.set(a.nodeId, (counts.get(a.nodeId) ?? 0) + 1);

  return (
    <div className="sb-tab">
      <EditionNotice feature="health_checks" className="mb-4">
        {t("hl.alerts.edgeHint")}
      </EditionNotice>
      <div className="pf-g pp-grid sb-grid">
        <div className="col">
          <NodeProblems nodes={problemNodes} />
          {live.length === 0 && !first && problemNodes.length === 0 && (
            <Calm kind="ok" className="hl-calm" title={t("hlp.calm.title")} text={lastClosed ? t("hl.alerts.quietLast", { ago: fmt.ago(lastClosed) }) : t("hl.alerts.quietNone")} />
          )}
          {live.length > 0 && (
            <Sec tone="rose" icon={P.bell} title={t("hlp.open")} count={shown.flat().length} action={<CloseAll alerts={shown.flat()} />}>
              <div className="hl-als">
                <AlertRows groups={shown} now={now} flow={flow} open={open} setOpen={setOpen} />
              </div>
              {shown.length === 0 && (
                <div className="pv-nf">
                  <p>{t("hlp.nf.text")}</p>
                  <Button variant="secondary" size="md" className="btn sm" onClick={() => setNode("")}>
                    {t("hlp.nf.all")}
                  </Button>
                </div>
              )}
            </Sec>
          )}
          {muted.length > 0 && <MutedAlerts groups={muted} now={now} flow={flow} open={open} setOpen={setOpen} />}
          <HistoryList rows={history} />
        </div>
        <aside className="col pp-as">
          <NodesSide nodes={nodes} counts={counts} node={node} onPick={setNode} />
          {!noChecks && <ChecksSide onOpen={onChecks} />}
        </aside>
      </div>
    </div>
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
    <Link to="/nodes/$id" params={{ id: exit?.id ?? a.nodeId }} search={exit ? { tab: "ways", way: `via:${a.nodeId}` } : { tab: "ways" }} className={linkBtn()}>
      {t("hl.alerts.openWay")}
    </Link>
  );
}
