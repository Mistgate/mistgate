import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertSeverity, DoctorStatus, type GetDoctorResponse } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { doctorTotals, doctorWord, fleetIssues, isIssue, itemTitle, useCan, type FleetIssue, type NodeDoctor } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { overviewQuery } from "@/lib/queries";
import { pillOf } from "./alert-bits";
import { ManualSteps, NodeDoctorLink, useDoctorItem, useRunDoctor } from "./doctor-parts";
import { fixLine, useFixWave, type FixFlow } from "./fix";
import { Calm, Dot, P, Sec, type DotKind } from "./page-parts";
import "./health.css";

const keyOf = (x: Pick<FleetIssue, "nodeId" | "item">) => `${x.nodeId}/${x.item.id}/${x.item.params.inbound_id ?? ""}`;

/** The walk of "Run again" over the nodes (the board's hlDoc): the one asked is "run", the rest "wait"; it keeps to the last while the call is on. */
function useDoctorWalk(count: number, pending: boolean, frozen = false) {
  const [ticks, setTicks] = useState(0);
  useEffect(() => {
    if (!pending || frozen) return;
    const id = setInterval(() => setTicks((k) => k + 1), 700);
    return () => {
      clearInterval(id);
      setTicks(0);
    };
  }, [pending, frozen]);
  const step = pending ? Math.min(Math.max(1, count), 1 + (frozen ? 1 : ticks)) : 0;
  const phase = (i: number): "" | "run" | "wait" | "done" => (step === 0 ? "" : i === step - 1 ? "run" : i > step - 1 ? "wait" : "done");
  return { phase, width: step > 0 && count > 1 ? `${Math.round(((step - 1) / (count - 1)) * 100)}%` : "0%", busy: step > 0 };
}

/**
 * One finding of the doctor, in the board's card (.hl-find): how bad, the node, the check, why, the node's own fact, what can be done (the fix, the command to
 * copy, the WARP card, the steps by hand) and "this is normal for this node". A fix that runs says so in place of its button; one that fails shakes the card and says why.
 */
function Finding({ flow, issue }: { flow: FixFlow; issue: FleetIssue }) {
  const { nodeId, nodeName, item } = issue;
  const fmt = useFmt();
  const stale = issue.stale ? fmt.duration(issue.ageS) : undefined;
  const d = useDoctorItem({ flow, nodeId, nodeName, item, stale });
  const { t, can, accept, open, why, fact, command, steps, phase, canAccept, who, action, fixKey, target } = d;
  const root = useRef<HTMLElement>(null);
  useFixWave(flow, fixKey, root);
  const failed = fixKey && phase === "idle" ? flow.error(fixKey) : "";
  const raw = fact || item.detail;
  const pill = pillOf(item.status === DoctorStatus.FAIL ? AlertSeverity.CRITICAL : AlertSeverity.WARNING);
  return (
    <section ref={root} title={stale ? t("hl.doctor.stale", { age: stale }) : undefined} className={cx("card pp-sec sb-sec hl-find", phase === "busy" && "busy", stale && "stale")}>
      <div className="pp-sh">
        <span className={cx("pv-pill", pill.kind)}>
          <Dot kind={pill.glyph} />
          {t(doctorWord(item.status))}
        </span>
        <span className="hl-who mono">
          <NodeDoctorLink id={nodeId} name={nodeName} />
        </span>
        <h3 className="hl-ft">{itemTitle(t, item)}</h3>
      </div>
      <div className="hl-alb hlx">
        {why && <p>{why}</p>}
        {raw && <code className="mono hl-raw">{raw}</code>}
        {command && <p>{t("hl.doctor.runCommand")}</p>}
        {phase === "busy" && (
          <p className="hl-run" role="status">
            <Dot kind="busy" />
            {(target && fixLine(t, "run", target)) || t("hl.fix.fixing")}
          </p>
        )}
        {phase === "done" && (
          <p className="hl-run ok" role="status">
            <Dot kind="ok" />
            {t("hl.fix.fixed")}
          </p>
        )}
        {failed && (
          <div key={flow.failures(fixKey)} className="rc-pb bad hl-fail hl-shake" role="alert">
            <Dot kind="bad" />
            <div>
              <b>{t("hlp.fail.title")}</b>
              <span>{failed}</span>
            </div>
          </div>
        )}
        {open && steps.length > 0 && (
          <div className="hl-steps">
            <ManualSteps steps={steps} nodeId={nodeId} />
          </div>
        )}
        {phase === "idle" && (action || canAccept) && (
          <div className="hl-act">
            {action}
            {canAccept && (
              <Button variant="secondary" size="md" className="btn sm" disabled={accept.busy} title={t("hl.doctor.acceptHint")} onClick={() => accept.run(who, true)}>
                {t("hl.doctor.accept")}
              </Button>
            )}
          </div>
        )}
        {!can.run && !action && !steps.length && <p className="hl-ro">{t("hl.doctor.manual")}</p>}
      </div>
    </section>
  );
}

/** What a node says in the list on the side: the state as a dot and a word, in the board's colours. */
function nodeLine(t: ReturnType<typeof useT>, n: NodeDoctor, never: boolean): { text: string; tone: string; dot: DotKind } {
  const offline = n.nodeStatus !== NodeStatus.ONLINE && n.nodeStatus !== NodeStatus.NO_TRAFFIC;
  const found = n.items.filter(isIssue).length;
  if (offline) return { text: t("hlp.dn.offline"), tone: "crit", dot: "bad" };
  if (!n.agentSupported) return { text: t("hlp.dn.tooOld"), tone: "mut", dot: "none" };
  if (never || !n.hasReport || n.items.length === 0) return { text: t("hlp.dn.never"), tone: "mut", dot: "none" };
  if (found > 0) return { text: t("hlp.dn.found", { n: found }), tone: "warn", dot: "part" };
  return { text: t("hlp.dn.clean"), tone: "", dot: "ok" };
}

/** The fleet doctor: the sum, then every problem the nodes report, worst first, each with its explanation and, where there is one, a fix; the nodes at the side. */
/** What a scene of the dev kit starts the tab with: "Run again" going, fixed at its second node. */
export type DoctorInit = { busy?: boolean };

export function DoctorTab({ data, flow, first = false, init }: { data: Plain<GetDoctorResponse>; flow: FixFlow; first?: boolean; init?: DoctorInit }) {
  const t = useT();
  const can = useCan();
  const run = useRunDoctor();
  const flags = new Map((useQuery(overviewQuery()).data?.nodes ?? []).map((n) => [n.id, n.countryCode]));
  const issues = fleetIssues(data.nodes);
  const totals = doctorTotals(data.nodes);
  const never = first || totals.nodes === 0;
  const busy = run.isPending || !!init?.busy;
  const walk = useDoctorWalk(data.nodes.length, busy, !!init?.busy);

  // A row whose fix just worked leaves the report at once; it stays as "Fixed" until the fix's 30 s are over.
  const live = new Set(issues.map(keyOf));
  const kept = flow
    .pending()
    .filter((p) => p.item && !live.has(p.key))
    .map((p) => ({ nodeId: p.nodeId, nodeName: p.nodeName, item: p.item!, stale: false, ageS: 0 }));
  const rows: FleetIssue[] = [...issues, ...kept];

  return (
    <div className="sb-tab">
      <section className="card hl-dsum">
        <div>
          <span>{t("hlp.doc.items")}</span>
          <b className="mono">{never ? "—" : totals.items}</b>
        </div>
        <div>
          <span>{t("hlp.doc.nodes")}</span>
          <b className="mono">{first ? data.nodes.length : totals.nodes}</b>
        </div>
        <div>
          <span>{t("hlp.doc.found")}</span>
          <b className={cx("mono", issues.length > 0 && !walk.busy && "warn")}>{walk.busy ? "…" : never ? "—" : issues.length}</b>
        </div>
        {can.run && (
          <Button variant="secondary" size="md" className={cx("btn", busy && "busy")} aria-disabled={busy} onClick={() => !busy && run.mutate()}>
            {busy && <span className="an-sp" aria-hidden="true" />}
            {busy ? t("hlp.ck.busy") : t("hl.doctor.rerun")}
          </Button>
        )}
        <i className="rw-pg" style={{ "--w": walk.width } as React.CSSProperties} aria-hidden="true" />
      </section>
      <div className="pf-g pp-grid sb-grid">
        <div className="col">
          {rows.map((x) => (
            <Finding key={keyOf(x)} flow={flow} issue={x} />
          ))}
          {rows.length === 0 && (
            <Calm
              kind={never ? "" : "ok"}
              className="hl-calm"
              title={never ? t("hlp.doc.neverTitle") : t("hlp.doc.cleanTitle")}
              text={never ? t("hlp.doc.neverText") : t("hlp.doc.cleanText", { items: totals.items, nodes: totals.nodes })}
            />
          )}
        </div>
        <aside className="col pp-as">
          <Sec tone="sky" icon={P.server} title={t("hlp.doc.nodesTitle")}>
            <div className="hl-nodes hl-dn">
              {data.nodes.map((n, i) => {
                const ph = walk.phase(i);
                const line = ph === "run" ? { text: t("hlp.ck.checking"), tone: "mut", dot: "busy" as DotKind } : ph === "wait" ? { text: t("hlp.ck.queued"), tone: "mut", dot: "none" as DotKind } : nodeLine(t, n, never);
                return (
                  <div key={n.nodeId} className={ph === "run" ? "cking" : ""}>
                    <Flag code={flags.get(n.nodeId) ?? ""} size={13} />
                    <b className="mono">{n.nodeName}</b>
                    <span className={line.tone}>
                      <Dot kind={line.dot} />
                      {line.text}
                    </span>
                  </div>
                );
              })}
            </div>
            <QuietNodes nodes={data.nodes} never={never} />
          </Sec>
        </aside>
      </div>
    </div>
  );
}

/**
 * The nodes the list above cannot speak for, one line each and each for its own reason: an agent too old for the doctor (with the way to Updates), a node
 * that is offline (its last report is what is shown), one whose first report has not come (not before the first round: the list says it of all). The name opens the node's Doctor tab.
 */
function QuietNodes({ nodes, never }: { nodes: readonly NodeDoctor[]; never: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const lines = nodes.flatMap((n) => {
    const reported = n.hasReport && n.items.length > 0;
    const offline = n.nodeStatus !== NodeStatus.ONLINE && n.nodeStatus !== NodeStatus.NO_TRAFFIC;
    if (!n.agentSupported) return [{ n, text: n.agentVersion ? t("hl.doctor.line.tooOld", { version: n.agentVersion }) : t("hl.doctor.line.tooOldBare"), updates: true }];
    if (offline && n.lastSeenUnix > 0) return [{ n, text: t(reported ? "hl.doctor.line.offline" : "hl.doctor.line.offlineNone", { time: fmt.stamp(n.lastSeenUnix) }), updates: false }];
    // before the first round the list above already says "not checked" of every node: no second line of the same
    if (!reported && !never) return [{ n, text: t("hl.doctor.line.waiting"), updates: false }];
    return [];
  });
  if (lines.length === 0) return null;
  return (
    <ul className="hl-quiet">
      {lines.map(({ n, text, updates }) => (
        <li key={n.nodeId}>
          <NodeDoctorLink id={n.nodeId} name={n.nodeName} />
          {" — "}
          {text}
          {updates && (
            <>
              {" · "}
              <Link to="/updates" className="underline decoration-dotted underline-offset-2">
                {t("hl.doctor.openUpdates")}
              </Link>
            </>
          )}
        </li>
      ))}
    </ul>
  );
}
