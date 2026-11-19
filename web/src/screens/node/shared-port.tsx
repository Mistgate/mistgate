import { Link } from "@tanstack/react-router";
import { Scramble } from "@/components/fx/scramble-react";
import { buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { InboundState } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { agoOf, useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { sharedPortViews, type PortSnapshot, type SharedPortView } from "@/lib/tcpshare";
import { useNow } from "@/lib/time";
import { networkOf } from "@/lib/ways-graph";

// "TCP 443 is shared" on the node's Profiles tab: which services stand on one TCP port, the name that leads to each, who is the default
// (it gets a visitor with no name or an unknown one), what the node counted since its agent started, and the states a page must not
// leave unsaid: the agent cannot share a port yet, the node could not take the port, a flood of unfinished handshakes.

/** The views of the node's shared ports, from the node's detail (the inbounds the page lists and the snapshot the agent reported). */
export function useSharedPorts(data: Plain<GetNodeResponse>): SharedPortView[] {
  const inbounds = data.inbounds.map((i) => ({
    id: i.id,
    profileId: i.profileId,
    profileName: i.profileName,
    protocol: i.protocol,
    port: i.port,
    tlsServerName: i.tlsServerName,
    on: i.state !== InboundState.DISABLED,
    lastError: i.lastError,
  }));
  return sharedPortViews(inbounds, (data.sharedPorts ?? []) as PortSnapshot[], (p) => networkOf(p) === "tcp");
}

export function SharedPortCards({ data }: { data: Plain<GetNodeResponse> }) {
  const views = useSharedPorts(data);
  if (views.length === 0) return null;
  return (
    <div data-testid="shared-ports" className="flex flex-col gap-3">
      {views.map((v) => (
        <SharedPortCard key={v.port} view={v} reportedUnix={data.sharedPortsUnix} />
      ))}
    </div>
  );
}

/** What the front sent to the default service, and why (the counters that say it). */
const toDefault = (s: PortSnapshot) => s.defaultNoSni + s.defaultUnknownName + s.defaultNotTls + s.defaultBadHello + s.defaultHelloTooLarge + s.defaultRouteDown;

function SharedPortCard({ view, reportedUnix }: { view: SharedPortView; reportedUnix: number }) {
  const t = useT();
  const fmt = useFmt();
  const now = Math.floor(useNow(60_000) / 1000);
  const { snap } = view;
  const def = view.rows.find((r) => r.isDefault)?.inbound.profileName ?? view.rows.find((r) => r.inbound.id === view.running)?.inbound.profileName ?? "";
  const stats: { key: string; value: number; tone?: "warn" | "bad" }[] = snap
    ? [
        { key: "accepted", value: snap.accepted },
        { key: "default", value: toDefault(snap) },
        { key: "refused", value: snap.refused + snap.noRoute, tone: snap.refused + snap.noRoute > 0 ? "warn" : undefined },
        { key: "down", value: snap.defaultRouteDown, tone: snap.defaultRouteDown > 0 ? "warn" : undefined },
        { key: "timeouts", value: snap.timeouts },
        { key: "evicted", value: snap.evicted, tone: snap.evicted > 0 ? "warn" : undefined },
      ]
    : [];
  return (
    <section data-testid={`shared-port-${view.port}`} className="screen-enter flex min-w-0 flex-col rounded-card border border-line bg-surface">
      <header className="flex items-start gap-3 px-6 pt-4 pb-3 max-[720px]:px-4">
        <span aria-hidden className="mt-0.5 grid size-8 flex-none place-items-center rounded-[10px] bg-step text-sec">
          <Icon name="sliders" size={16} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <h3 className="text-[15px] leading-6 font-medium tracking-[-0.01em]">{t("sp.title", { port: view.port })}</h3>
          <p className="text-[13px] leading-snug text-pretty text-muted">{t("sp.sub", { n: view.rows.length, default: def })}</p>
        </div>
      </header>

      <ul className="m-0 flex list-none flex-col border-t border-line p-0">
        {view.rows.map((r) => (
          <li key={r.inbound.id} className="grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-b border-line px-6 py-3 last:border-b-0 max-[720px]:px-4 min-[721px]:grid-cols-[28px_minmax(140px,220px)_minmax(0,1fr)_auto_72px]">
            <ProtocolTag protocol={r.inbound.protocol} compact className="w-7 justify-center px-0" />
            <Link to="/profiles/$id" params={{ id: r.inbound.profileId }} className="min-w-0 truncate font-medium hover:text-accent-text hover:underline">
              {r.inbound.profileName}
            </Link>
            <span className="col-start-2 row-start-2 min-w-0 truncate font-mono text-xs text-muted min-[721px]:col-span-1 min-[721px]:col-start-3 min-[721px]:row-start-1 min-[721px]:text-[13px]" title={r.inbound.tlsServerName}>
              {r.inbound.tlsServerName || "—"}
            </span>
            <span className="min-[721px]:justify-self-start">
              {r.isDefault && (
                <span title={t("sp.default.hint")} className="inline-flex h-6 items-center rounded-chip border border-accent-line bg-accent-soft px-2 text-xs font-medium whitespace-nowrap text-accent-text">
                  {t("sp.default")}
                </span>
              )}
            </span>
            <span className={cx("text-end font-mono text-[13px] font-medium tabular-nums max-[720px]:col-start-3 max-[720px]:row-start-2 max-[720px]:text-xs", r.delivered === null && "text-muted")} title={t("sp.col.got")}>
              {r.delivered === null ? "—" : <Scramble value={fmt.num(r.delivered, 0)} once />}
            </span>
          </li>
        ))}
      </ul>

      {view.old && (
        <div className="px-6 pt-1 pb-4 max-[720px]:px-4">
          <Callout
            kind="warn"
            action={
              <Link to="/updates" className={buttonClass("secondary", "sm")}>
                {t("sp.old.go")}
              </Link>
            }
          >
            <b className="font-medium">{t("sp.old.title")}. </b>
            {t("sp.old.body", { port: view.port, default: def, others: view.waiting.join(", ") })}
          </Callout>
        </div>
      )}
      {snap?.bindError && (
        <div className="px-6 pt-1 pb-4 max-[720px]:px-4">
          <Callout kind="bad" alert>
            {t("sp.bind", { port: view.port, error: snap.bindError })}
          </Callout>
        </div>
      )}
      {view.noReport && <p className="px-6 pb-4 text-[13px] text-muted max-[720px]:px-4">{t("sp.waiting")}</p>}

      {snap && (
        <div className="flex flex-col gap-2.5 rounded-b-[15px] border-t border-line bg-step px-6 pt-3 pb-3.5 max-[720px]:px-4">
          <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1.5">
            {stats.map((s) => (
              <span key={s.key} title={t(`sp.hint.${s.key}` as "sp.hint.accepted")} className="inline-flex items-baseline gap-1.5 text-[13px] text-sec">
                <b className={cx("font-mono text-[13px] font-medium text-fg tabular-nums", s.tone === "warn" && "text-warn-text", s.tone === "bad" && "text-danger-text")}>
                  <Scramble value={fmt.num(s.value, 0)} once />
                </b>
                {t(`sp.stat.${s.key}` as "sp.stat.accepted")}
              </span>
            ))}
          </div>
          {snap.unknownNames.length > 0 && (
            <div className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-xs text-muted">
              <span>{t("sp.unknown")}:</span>
              {snap.unknownNames.map((u) => (
                <span key={u.name} className="font-mono text-[12px] text-sec">
                  {u.name} <span className="text-muted">×{fmt.num(u.count, 0)}</span>
                </span>
              ))}
            </div>
          )}
          {reportedUnix > 0 && <span className="text-[11px] text-muted">{t("sp.since", { when: agoOf(t, reportedUnix, now) })}</span>}
        </div>
      )}
    </section>
  );
}
