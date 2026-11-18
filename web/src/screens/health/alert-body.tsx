import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useRef, useState } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { AlertKind } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { alertFix, alertIpv6, alertWhy, fixLabel, isPeopleAlert, useCan, type Alert } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { entryOffered } from "@/lib/ways";
import { ClientIPv6OffDialog, MuteMenu, Networks, PathRuns, RestartDialog, useClose, useMute } from "./alerts-parts";
import { useAcceptDoctor } from "./doctor-parts";
import { fixLine, useFixWave, type FixFlow } from "./fix";
import { Dot, Gl, P } from "./page-parts";
import "./alert-body.css";

// The body of an alert: what it says and what can be done about it (the board's .hl-alb). A node's Overview shows it too, so it stands apart from the rows of the
// Health page (alerts.tsx), which carry the page's own stylesheet.

/** A link of the row's own, in the board's secondary button (.btn.sm). */
export const linkBtn = (primary = false) => cx(buttonClass(primary ? "primary" : "secondary", "md"), "btn sm");

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
  const reachFirst = a.kind === AlertKind.COUNTRY_BLOCKED;
  const reachLink = shared && !onNodePage && (a.kind === AlertKind.COUNTRY_BLOCKED || a.kind === AlertKind.NODE_DOWN) && (
    <Link to="/reachability" className={linkBtn()}>
      {t("rc.open")}
    </Link>
  );
  const nodeLink = shared && a.nodeId && a.actions.includes("open_node") && !go && !onNodePage && (
    <Link to="/nodes/$id" params={{ id: a.nodeId }} className={linkBtn()}>
      {t("hl.alerts.openNode")}
    </Link>
  );

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
          {(target && fixLine(t, "run", target)) || t("hl.fix.fixing")}
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
          {/* the board lists the way to Reachability first on a country alert and the node first on a node that is down */}
          {reachFirst && reachLink}
          {nodeLink}
          {!reachFirst && reachLink}
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
