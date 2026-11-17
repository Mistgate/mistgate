import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState, type CSSProperties } from "react";
import { useAddNode } from "@/components/add-node";
import { Avatar, PageTitle } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Skeleton } from "@/components/ui/query-error";
import { useT } from "@/i18n";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { errorText } from "@/lib/errors";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { alertsQuery, checksQuery, isLoud, isPeopleAlert, useCan } from "@/lib/health";
import { awaitingQuery } from "@/lib/integrations";
import { overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useNow } from "@/lib/time";
import { FirstRunView, SoonCards, useAddNodeFromUrl, useFirstRun } from "./first-run";
import { useFixFlow } from "./health/fix";
import { ChartCard, type ChartRange } from "./overview-chart";
import { EventsCard } from "./overview-events";
import { Hero } from "./overview-hero";
import { NodesCard } from "./overview-nodes";
import { CardFoot, CardHead, footLink } from "./overview-parts";
import { todoItems } from "./overview-todo";
import type { OverviewResponse } from "./overview-types";
import "./overview.css";

/**
 * The Overview, as the redesign board draws it: the state of the fleet with what needs the owner, the nodes with their checks,
 * the chart and the feed, who downloads now. A panel with no node shows the first-run card and ghost previews; while a step of
 * the first run is left the card hangs above everything else.
 */
export function OverviewScreen() {
  useAddNodeFromUrl();
  const [range, setRange] = useState<ChartRange>("24h");
  const q = useQuery(overviewQuery(range === "24h" ? OverviewRange.OVERVIEW_RANGE_24H : OverviewRange.OVERVIEW_RANGE_7D));
  const data = q.data;

  if (!data) return q.isError ? <OverviewFailure error={q.error} onRetry={() => void q.refetch()} /> : <OverviewLoading />;
  return <Loaded data={data} range={range} onRange={setRange} updatedAt={q.dataUpdatedAt} />;
}

/** The load failed: the title, and a card that says why, with "Try again". */
export function OverviewFailure({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const t = useT();
  return (
    <div className="ov flex flex-col gap-4">
      <Head />
      <section className="rounded-card border border-line bg-surface">
        <EmptyState
          bad
          icon={<Icon name="warn" size={20} />}
          title={t("ov.error.title")}
          action={
            <Button variant="secondary" size="lg" onClick={onRetry}>
              {t("common.retry")}
            </Button>
          }
        >
          {errorText(error, t)}
        </EmptyState>
      </section>
    </div>
  );
}

/** The title row: the screen's name, "refreshes every 10 s", and the two things an owner adds from here. */
function Head() {
  const t = useT();
  const addNode = useAddNode();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const can = useCan();
  return (
    <div className="mb-1 flex items-center gap-3 max-md:flex-wrap">
      <PageTitle>{t("nav.overview")}</PageTitle>
      <span className="me-auto inline-flex items-center gap-2 text-[13px] text-muted max-md:flex-none">
        <i aria-hidden className="ov-live-dot block size-2 flex-none rounded-full bg-accent" />
        <span className="max-md:sr-only">{t("ov.live")}</span>
      </span>
      {can.run && (
        <Link to="/users" search={{ create: true }} className={buttonClass("secondary", "lg")}>
          <Icon name="plus" size={16} />
          {t("ov.btn.person")}
        </Link>
      )}
      {owner && (
        <Button variant="primary" size="lg" onClick={() => addNode()}>
          <Icon name="plus" size={16} />
          {t("ov.btn.node")}
        </Button>
      )}
    </div>
  );
}

/** The page while the first answer is on its way: the board's skeleton of the same shape, so nothing jumps when it comes. */
export function OverviewLoading() {
  const t = useT();
  return (
    <div aria-busy="true" data-fx-pending="" className="ov flex flex-col gap-4">
      <Head />
      <span className="pending-delay sr-only" role="status">
        {t("common.loading")}
      </span>
      <section className="ov-hero grid overflow-hidden rounded-card border border-line bg-surface">
        <div className="flex flex-col justify-between gap-10 px-8 pt-7 pb-8 max-md:gap-6 max-md:p-5">
          <div className="flex flex-col gap-3">
            <Skeleton className="w-40" />
            <Skeleton className="h-9 w-3/4 max-w-[420px] rounded-lg" />
          </div>
          <div className="grid grid-cols-3 gap-6 max-md:grid-cols-1">
            {[0, 1, 2].map((i) => (
              <div key={i} className="flex flex-col gap-2">
                <Skeleton className="w-24" />
                <Skeleton className="h-7 w-20 rounded-lg" />
                <Skeleton className="h-2.5 w-28 opacity-70" />
              </div>
            ))}
          </div>
        </div>
        <div className="ov-todo flex flex-col gap-4 bg-step px-6 py-5 max-md:px-4">
          <Skeleton className="h-4 w-28" />
          {[0, 1].map((i) => (
            <div key={i} className="flex items-center gap-3">
              <Skeleton className="size-8 flex-none rounded-lg" />
              <div className="flex flex-1 flex-col gap-2">
                <Skeleton className="w-3/5" />
                <Skeleton className="h-2.5 w-4/5 opacity-70" />
              </div>
            </div>
          ))}
        </div>
      </section>
      <section className="rounded-card border border-line bg-surface">
        <CardHead title={t("nav.nodes")} />
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex min-h-14 items-center gap-4 border-t border-line px-6 max-md:px-4">
            <Skeleton className="h-4 w-6 flex-none rounded" />
            <Skeleton className="w-16" />
            <Skeleton className="w-24" />
            <Skeleton className="ms-auto h-5 w-1/3 max-md:hidden" />
          </div>
        ))}
      </section>
    </div>
  );
}

function Loaded({ data, range, onRange, updatedAt }: { data: OverviewResponse; range: ChartRange; onRange: (r: ChartRange) => void; updatedAt: number }) {
  const t = useT();
  const addNode = useAddNode();
  const fmt = useFmt();
  const hasNodes = data.nodesTotal > 0;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const can = useCan();
  const edition = useEdition();
  const checksOn = hasNodes && !edition.off("health_checks");
  const firstRun = useFirstRun();
  const flow = useFixFlow();
  const nowMs = useNow();
  const users = useQuery(userCountQuery);
  const alertsQ = useQuery({ ...alertsQuery, enabled: data.alertsActive > 0 });
  const checks = useQuery({ ...checksQuery(""), enabled: checksOn }).data;
  const askedData = useQuery({ ...awaitingQuery, enabled: owner }).data;
  const asked = owner ? askedData : undefined; // the procedures are the owner's: nothing a cache may hold from before is shown to anyone else
  const now = alertsQ.data?.nowUnix ?? Math.floor(nowMs / 1000);
  // the alerts about nodes that count: the same ones as the badge (not people's, not muted, not info)
  const alerts = useMemo(() => (alertsQ.data?.active ?? []).filter((a) => !isPeopleAlert(a) && isLoud(a, now)), [alertsQ.data, now]);
  const items = useMemo(
    () => todoItems(t, fmt, { alerts, cards: data.nodes, approvals: asked?.approvals ?? [], canFix: can.fix }),
    [t, fmt, alerts, data.nodes, asked, can.fix],
  );

  return (
    <div className="ov flex flex-col gap-4">
      <Head />
      {/* the checklist hangs above the fleet until everything is done; on an empty panel it is the whole page */}
      {firstRun && <FirstRunView steps={firstRun.steps} onHide={firstRun.hide} empty={!hasNodes} />}
      {!hasNodes && firstRun && !firstRun.steps.every((s) => s.done) && <SoonCards />}
      {/* the list was hidden and the last node is gone: the panel is empty again, so say it and offer the node */}
      {!hasNodes && !firstRun && owner && (
        <section className="rounded-card border border-line bg-surface">
          <EmptyState
            title={t("nodes.emptyTitle")}
            action={
              <Button variant="primary" size="lg" onClick={() => addNode()}>
                {t("node.add.button")}
              </Button>
            }
          >
            {t("nodes.emptyText")}
          </EmptyState>
        </section>
      )}
      {hasNodes && (
        <>
          <Hero data={data} usersTotal={users.data?.counts?.all ?? null} items={items} flow={flow} updatedAt={updatedAt} everyMin={Math.round((checks?.intervalS || 300) / 60)} />
          <NodesCard cards={data.nodes} checks={checks} checksOn={checksOn} canAdd={owner} />
          <div className="ov-row2 grid items-start gap-4">
            <div className="flex min-w-0 flex-col gap-4">
              <ChartCard data={data} range={range} onRange={onRange} />
              <EventsCard events={data.events.slice(0, 5)} />
            </div>
            <div className="flex min-w-0 flex-col gap-4">
              <TopCard data={data} />
            </div>
          </div>
        </>
      )}
      {flow.modal}
    </div>
  );
}

/** Who downloads right now: the top five by speed, each with a bar against the fastest. */
function TopCard({ data }: { data: OverviewResponse }) {
  const t = useT();
  const fmt = useFmt();
  const top = data.topConsumers;
  const best = Math.max(1, ...top.map((u) => u.downBps));
  const rate = (bps: number) => {
    if (bps >= 1_000_000) return fmt.mbit(bps);
    if (bps >= 1_000) return `${fmt.num(bps / 1_000, bps >= 10_000 ? 0 : 1)} ${t("unit.kbit")}`;
    return `${fmt.num(bps, 0)} ${t("unit.bit")}`;
  };
  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("ov.top.title")} count={data.usersOnline} />
      {top.map((u, i) => (
        <div key={`${u.userId}/${u.nodeId}`} className="grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-6 py-2.5 max-md:px-4">
          <Avatar name={u.userName} index={i} size={28} />
          <div className="min-w-0">
            <Link to="/users/$id" params={{ id: u.userId }} className="block truncate font-medium transition-colors hover:text-accent-text">
              {u.userName}
            </Link>
            <div className="flex items-center gap-2 text-xs text-muted">
              <Link to="/nodes/$id" params={{ id: u.nodeId }} title={u.nodeName} className="font-mono font-medium transition-colors hover:text-fg">
                {u.nodeName}
              </Link>
            </div>
          </div>
          <div className="text-end font-mono text-[13px] font-medium whitespace-nowrap tabular-nums">
            {rate(u.downBps)}
            <i
              className="ov-bar mt-1.5 ms-auto h-[3px] w-24"
              style={{ "--w": `${Math.max(4, Math.round((u.downBps / best) * 100))}%`, "--fill": "var(--accent)" } as CSSProperties}
              data-end
            />
          </div>
        </div>
      ))}
      {top.length === 0 && <p className="border-t border-line px-6 py-3 text-[13px] text-muted max-md:px-4">{t("ov.noTop")}</p>}
      <CardFoot>
        <Link to="/users" className={footLink}>
          {t("ov.top.all")}
        </Link>
      </CardFoot>
    </section>
  );
}
