import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { Pagination } from "@/components/ui/pagination";
import { connVia, deviceConnParts, userConnectionQuery, type DeviceConn, type Part } from "@/lib/connections";
import { cx } from "@/lib/cx";
import { clampPage } from "@/lib/paging";
import { useFmt } from "@/lib/format";
import { useT } from "@/i18n";
import { alertIsAbout, alertTitle, alertWhy, alertsQuery, severityWord } from "@/lib/health";
import { platformLabel } from "./awg-devices";
import { nowSec } from "./format";
import type { DeviceN as Device } from "./model";
import { useTx } from "./t";
import { CardHead } from "./user-bits";

/**
 * The facts of a counter line, joined by middle dots; trouble in the warning colour. In a narrow column the line breaks only
 * after a dot: a fact ("задержка 182 мс") stays whole and no line starts with the dot.
 */
export function PartsLine({ parts }: { parts: Part[] }) {
  return (
    <p className="text-xs leading-normal text-pretty text-muted">
      {parts.map((p, i) => (
        <span key={i}>
          {i > 0 && " "}
          <span className="whitespace-nowrap">
            <span className={cx(p.warn && "font-medium text-warn-text", p.hint && "cursor-help underline decoration-dotted underline-offset-2")} title={p.hint}>
              {p.text}
            </span>
            {i < parts.length - 1 && " ·"}
          </span>
        </span>
      ))}
    </p>
  );
}

/**
 * "Connection": the open ACCESS_ENDED and USER_CONNECTION alerts of this person, from the same query as Health (no new call): what the
 * panel sees, why, and since when. Nothing open, nothing shown (also while the alerts load or fail). The Overview draws it.
 */
export function ConnectionAlerts({ userId }: { userId: string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(alertsQuery);
  const rows = (q.data?.active ?? []).filter((a) => alertIsAbout(a, userId)).sort((a, b) => b.severity - a.severity);
  if (rows.length === 0) return null;
  const now = q.data?.nowUnix ?? nowSec();
  return (
    <section className="up-card" data-testid="conn-alerts">
      <CardHead title={t("users.connection")} count={rows.length} />
      <div className="up-alerts">
        {rows.map((a) => {
          const why = alertWhy(t, fmt, a);
          return (
            <div key={a.id}>
              <div className="flex flex-wrap items-center gap-x-2.5 gap-y-1 text-[13px]">
                <span className={cx("up-gs", a.severity >= 2 ? "crit" : "warn")}>
                  <i aria-hidden />
                  {t(severityWord(a.severity))}
                </span>
                <b className="min-w-40 flex-1 font-medium">{alertTitle(t, a)}</b>
                <span className="font-mono text-[11px] text-muted">{t("hl.alerts.since", { duration: fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix))) })}</span>
              </div>
              {why && <p>{why}</p>}
            </div>
          );
        })}
      </div>
    </section>
  );
}

const connPageSize = 6;

/** The grid cell of a column: a figure, or a dash when the agent counts nothing there. */
const dash = "—";

/**
 * What the nodes counted of the person's connections in the last 24 hours (the board's .up-q table): a line per device and profile,
 * connections, drops (amber from three), the delay, the loss and the time online; what else the node saw (refusals, freezes, the reasons
 * handshakes fail) under the line. While the nodes have counted nothing the card says so.
 */
export function ConnectionCounts({ userId, devices = [], nodeName = (id) => id }: { userId: string; devices?: Device[]; nodeName?: (id: string) => string }) {
  const t = useT();
  const tx = useTx();
  const fmt = useFmt();
  const conn = useQuery(userConnectionQuery(userId)).data;
  const [wanted, setPage] = useState(1);
  if (!conn) return null;
  const page = clampPage(wanted, conn.rows.length, connPageSize);
  const name = (id: string) => {
    const d = devices.find((x) => x.id === id);
    if (!d) return t("conn.deviceGone");
    if (d.kind === DeviceKind.LINK_SHARED) return tx("users.dev.shared");
    if (d.kind === DeviceKind.TELEGRAM_KEY) return tx("tgproxy.key");
    return d.model || platformLabel(d.platform || "other", tx);
  };
  const missing = conn.nodesWithoutCounters.map(nodeName);
  const rows = conn.rows.slice((page - 1) * connPageSize, page * connPageSize);
  const cols = (r: DeviceConn) => [
    { l: t("up.q.connects"), v: String(r.connects), cls: "" },
    { l: t("up.q.drops"), v: String(r.drops), cls: r.drops >= 3 ? "up-warn" : "" },
    { l: t("up.q.rtt"), v: r.rttSamples > 0 ? t("up.q.ms", { n: r.rttMs }) : dash, cls: "" },
    { l: t("up.q.loss"), v: r.pktsSent >= 1000 ? `${(r.lossPermille / 10).toFixed(1)} %` : dash, cls: r.pktsSent >= 1000 && r.lossPermille >= 50 ? "up-warn" : "" },
    { l: t("up.q.online"), v: r.onlineS > 0 ? fmt.duration(r.onlineS) : dash, cls: "" },
  ];
  return (
    <section className="up-card" data-testid="conn-counts">
      <CardHead title={t("conn.title")} />
      {conn.rows.length === 0 && missing.length === 0 && <p className="up-empty">{t("conn.none")}</p>}
      {conn.rows.length > 0 && (
        <div role="table" aria-label={t("conn.title")}>
          <div role="row" className="up-q up-qh">
            <span role="columnheader">{t("up.q.device")}</span>
            <span role="columnheader">{t("up.q.nodeProfile")}</span>
            <span role="columnheader">{t("up.q.connects")}</span>
            <span role="columnheader">{t("up.q.drops")}</span>
            <span role="columnheader">{t("up.q.rtt")}</span>
            <span role="columnheader">{t("up.q.loss")}</span>
            <span role="columnheader">{t("up.q.online")}</span>
          </div>
          {rows.map((r) => {
            const extras = deviceConnParts(t, fmt, r).filter((p) => p.warn || p.hint);
            return (
              <div key={`${r.deviceId}/${r.inboundId}/${r.viaNodeId}`} data-testid="conn-row">
                <div role="row" className="up-q">
                  <b role="cell">{name(r.deviceId)}</b>
                  <span role="cell">
                    <b>{r.nodeName || r.nodeId}</b> {r.profileName}
                    {connVia(t, r) && (
                      <span data-testid="conn-via" className="up-tag up-q-via">
                        {connVia(t, r)}
                      </span>
                    )}
                  </span>
                  {cols(r).map((c) => (
                    <span key={c.l} role="cell" data-l={c.l} className={cx("mono", c.cls)}>
                      {c.v}
                    </span>
                  ))}
                </div>
                {extras.length > 0 && (
                  <div className="up-qx">
                    <PartsLine parts={extras} />
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
      <Pagination total={conn.rows.length} page={page} size={connPageSize} onPage={setPage} label={t("conn.title")} className="pb-3" />
      {missing.length > 0 && <p className="up-empty">{t("conn.oldAgents", { nodes: missing.join(", ") })}</p>}
    </section>
  );
}

/** The alerts about the person, then what the nodes counted (both cards, as the older single panel did). */
export function ConnectionPanel({ userId, devices = [], nodeName = (id) => id }: { userId: string; devices?: Device[]; nodeName?: (id: string) => string }) {
  return (
    <div className="up-one">
      <ConnectionAlerts userId={userId} />
      <ConnectionCounts userId={userId} devices={devices} nodeName={nodeName} />
    </div>
  );
}
