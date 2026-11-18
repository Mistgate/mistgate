import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useId, useRef, useState, type ReactNode } from "react";
import { AlertKind, AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { EditionNotice } from "@/components/edition-notice";
import { Flag } from "@/components/ui/flag";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import {
  alertIpv6,
  alertTitle,
  checksQuery,
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
import { overviewQuery } from "@/lib/queries";
import { Ipv6Tag, pillOf, targeted } from "./alert-bits";
import { CloseAll, LossTable, MuteMenu, useClose, useMute } from "./alerts-parts";
import { HistoryList } from "./alerts-history";
import { AlertBody, linkBtn } from "./alert-body";
import { NodeProblems, useProblemNodes } from "./node-problems";
import type { FixFlow } from "./fix";
import { Calm, Dot, Gl, P, Sec } from "./page-parts";
import "./health.css";

// The alerts of the Health page in the board's own rows (PnHealth: .hl-al > .hl-alh + .hl-alb). One line per problem that opens into what it says and what can be
// done about it; the open one is one at a time, as on the board. The per-profile alerts of one node are one row, the muted ones their own fold at the end, the
// closed ones a history by event. What other screens take from here keeps its name: AlertCard, Ipv6Tag.

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
            {has("open_node") && !has("open_profiles") && !has("open_warp") && (
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
 * (as the board opens its first; on People the board keeps its "open" on a node alert, so no row there: openFirst=false); once chosen, that (or none).
 */
export function useOpenRow(groups: Alert[][], start: string | null = null, openFirst = true): [string, (id: string) => void] {
  const [chosen, setChosen] = useState<string | null>(start);
  const hash = groups.find((g) => targeted(anchors(g)));
  const crit = groups.find((g) => groupSeverity(g) === AlertSeverity.CRITICAL);
  const first = hash?.[0]?.id ?? crit?.[0]?.id ?? (openFirst ? groups[0]?.[0]?.id : "") ?? "";
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
              <Flag code={n.countryCode ?? ""} size={13} />
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
/** What a scene of the dev kit starts the tab with: the node the list is filtered to, the row that is open, the window of "Close all shown". */
export type AlertsInit = { node?: string; open?: string; closeAll?: boolean };

export function AlertsTab({ active: all, history: closed, now, flow, onChecks, first = false, init }: { active: Alert[]; history: Alert[]; now: number; flow: FixFlow; onChecks?: () => void; first?: boolean; init?: AlertsInit }) {
  const t = useT();
  const fmt = useFmt();
  const noChecks = useEdition().off("health_checks");
  const fleet = useQuery(overviewQuery()).data?.nodes;
  const [node, setNode] = useState(init?.node ?? "");
  const active = all.filter((a) => !isPeopleAlert(a));
  const live = groupAlerts(active.filter((a) => a.mutedUntilUnix <= now));
  const muted = groupAlerts(active.filter((a) => a.mutedUntilUnix > now));
  const history = closed.filter((a) => !isPeopleAlert(a));
  const lastClosed = history[0]?.resolvedAtUnix;
  const shown = node ? live.filter((g) => g[0]!.nodeId === node) : live;
  // nodes that count as a problem and have no alert of their own are named above the alerts (they are in the number of the header)
  const problemNodes = useProblemNodes(new Set(active.filter((a) => isLoud(a, now)).map((a) => a.nodeId)));
  const [open, setOpen] = useOpenRow([...shown, ...muted], init?.open ?? null);

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
  const nodes = [...(fleet ?? []).map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode ?? "" }))];
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
            <Sec tone="rose" icon={P.bell} title={t("hlp.open")} count={shown.flat().length} action={<CloseAll alerts={shown.flat()} defaultOpen={init?.closeAll} />}>
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
