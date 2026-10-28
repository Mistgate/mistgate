import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { StatusPill } from "@/components/ui/status";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { useToast } from "@/components/ui/toast";
import { InboundState, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { nodeLoad } from "@/lib/node-load";
import type { Plain } from "@/lib/plain";
import { useNow } from "@/lib/time";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { doctorQuery, isIssue } from "@/lib/health";
import { agentLinked, nodeKind, useNodeStatus } from "@/lib/node-status";
import { nodeQuery } from "@/lib/queries";
import { useOlderNodes } from "@/lib/updates";
import { warpKind, warpWord } from "@/lib/warp";
import { StateBanner } from "./banner";
import { NodeDoctorTab } from "./doctor";
import { ExitTab } from "./exit";
import { HistoryTab } from "./history";
import { OverviewTab } from "./overview";
import { ProfilesTab } from "./profiles";
import { ServerTab } from "./server";
import type { HistoryView, NodeSearch, NodeTab } from "./tabs";
import { useWays, WaysTab } from "./ways";

export function NodeScreen() {
  const t = useT();
  const { id = "" } = useParams({ strict: false });
  const q = useQuery(nodeQuery(id));

  if (q.data) return <NodeDetail data={q.data} />;
  return (
    <div className="flex flex-col gap-4">
      <Link to="/nodes" aria-label={t("node.back")} className={buttonClass("secondary", "md") + " w-fit"}>
        <Icon name="back" size={14} />
        {t("nav.nodes")}
      </Link>
      {q.isError ? (
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState title={t("node.notFound")}>{t("node.notFoundBody")}</EmptyState>
        </div>
      ) : (
        <p className="text-muted">{t("common.loading")}</p>
      )}
    </div>
  );
}

/** The WARP chip of the header: coloured by the state like a status pill, and the way to the WARP card. */
function WarpChip({ node }: { node: NonNullable<Plain<GetNodeResponse>["node"]> }) {
  const t = useT();
  const state = node.warp?.state ?? WarpState.UNSPECIFIED;
  if (state === WarpState.NOT_CONFIGURED || state === WarpState.UNSPECIFIED) return null;
  const label = node.warp?.colo && state === WarpState.UP ? `WARP · ${node.warp.colo}` : `WARP · ${t(warpWord[state])}`;
  return (
    <Link
      to="/nodes/$id"
      params={{ id: node.id }}
      search={{ tab: "exit" }}
      hash="warp"
      aria-label={t("node.warpChip", { state: t(warpWord[state]) })}
      title={t("node.warpChip", { state: t(warpWord[state]) })}
      className="rounded-xl transition-transform duration-200 hover:-translate-y-px"
    >
      <StatusPill kind={warpKind[state]} label={label} sm />
    </Link>
  );
}

/** The load chip of the header: the three-step meter of the user's page, plus the percentage only the admin sees. */
function LoadChip({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const now = useNow(10_000);
  const load = nodeLoad(data.metrics, data.node!.bandwidthMbps, Math.floor(now / 1000));
  if (!load || !agentLinked(data.node!.status)) return null; // the page counts connected nodes only
  const fill = { low: 1, medium: 2, high: 3 }[load.level];
  const high = load.level === "high";
  return (
    <span
      title={t(data.node!.bandwidthMbps > 0 ? "node.loadHint" : "node.loadHintCpu")}
      className="inline-flex h-[26px] flex-none items-center gap-2 rounded-xl border border-line bg-surface pr-2.5 pl-2 text-xs leading-none font-bold whitespace-nowrap"
    >
      <span className="font-semibold text-muted">{t("node.load")}</span>
      <span aria-hidden className="flex gap-[3px]">
        {[1, 2, 3].map((n) => (
          <i key={n} className={cx("block h-[5px] w-3.5 rounded-[3px]", n > fill ? "bg-surface-2" : high ? "bg-warn" : "bg-accent")} />
        ))}
      </span>
      <span className={high ? "text-warn-text" : undefined}>{t(`node.loadLevel.${load.level}`)}</span>
      <span className="font-mono text-[11px] text-muted">{load.percent}%</span>
    </span>
  );
}

export function NodeDetail({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as NodeSearch;
  const node = data.node!;
  const kind = nodeKind(node);
  const older = useOlderNodes().has(node.id);
  const doctor = useQuery(doctorQuery(node.id));
  const [restart, setRestart] = useState(false);
  const go = (next: NodeSearch) => void navigate({ to: "/nodes/$id", params: { id: node.id }, search: next, replace: true });
  const setTab = (next: string) => go(next === "overview" ? {} : { tab: next as NodeTab });
  const setView = (view: HistoryView) => go(view === "events" ? { tab: "history" } : { tab: "history", view });

  const retired = node.status === NodeStatus.RETIRED;
  // the agent is on the line (also when traffic does not flow): restarting works then, and only then
  const restartOff = !agentLinked(node.status) ? t("node.restartOffline") : data.inbounds.length === 0 ? t("node.restartNoProfiles") : "";
  const failed = data.inbounds.filter((i) => i.state === InboundState.FAILED).length;
  const issues = doctor.data?.nodes.find((n) => n.nodeId === node.id)?.items.filter(isIssue).length ?? 0;
  const ways = useWays(data);

  const all: TabItem[] = [
    { value: "overview", label: t("node.tab.overview"), content: <OverviewTab data={data} ways={ways} /> },
    // the badge counts the ways broken on our side; a block by a country is an alert and the lane's colour, not a count
    { value: "ways", label: t("node.tab.ways"), badge: ways.badge, content: search.tab === "ways" ? <WaysTab data={data} ways={ways} /> : null },
    {
      value: "profiles",
      label: t("node.tab.profiles"),
      badge: failed,
      content: <ProfilesTab data={data} addProfile={search.add} onAddClosed={() => search.add && setTab("profiles")} />,
    },
    { value: "exit", label: t("node.tab.exit"), content: <ExitTab data={data} /> },
    {
      value: "history",
      label: t("node.tab.history"),
      content: search.tab === "history" ? <HistoryTab data={data} view={search.view ?? "events"} onView={setView} /> : null,
    },
    {
      value: "doctor",
      label: t("node.tab.doctor"),
      badge: issues,
      content: search.tab === "doctor" ? <NodeDoctorTab nodeId={node.id} nodeName={node.name} /> : null,
    },
    { value: "server", label: t("node.tab.server"), content: <ServerTab data={data} /> },
  ];
  // a retired node has nothing left to run or route: what it was, what happened, and the server (SSH access stays)
  const items = retired ? all.filter((i) => i.value === "overview" || i.value === "history" || i.value === "server") : all;
  const tab = items.some((i) => i.value === search.tab) ? search.tab! : "overview";

  const actions = (
    <>
      {/* a disabled button shows no tooltip: the reason sits on a wrapper */}
      <span title={restartOff || undefined} className="flex">
        <Button variant="secondary" size="md" disabled={!!restartOff} onClick={() => setRestart(true)}>
          {t("node.restart")}
        </Button>
      </span>
      <Button variant="secondary" size="md" onClick={() => setTab("doctor")}>
        {t("node.doctor")}
      </Button>
    </>
  );

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-start gap-3">
        <Link
          to="/nodes"
          aria-label={t("node.back")}
          className="inline-flex size-9 flex-none items-center justify-center rounded-ctl border border-line bg-surface text-fg transition-transform duration-200 hover:-translate-x-0.5"
        >
          <Icon name="back" />
        </Link>
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <div className="flex flex-wrap items-center gap-2.5">
            <span className="flex h-[22px] items-center rounded-[7px] bg-surface-2 px-[7px] font-mono text-[11px] font-bold text-muted">
              {node.countryCode || "—"}
            </span>
            <h1 className="text-2xl leading-none font-extrabold tracking-[-0.04em] md:text-[28px]">{node.name}</h1>
            <StatusPill kind={kind} label={st.word(node)} />
            <WarpChip node={node} />
            <LoadChip data={data} />
          </div>
          {/* each piece keeps together; a piece that starts a line has its "·" clipped, so no line starts or ends with one */}
          <p className="overflow-hidden text-xs text-muted">
            <span className="-ml-3.5 flex flex-wrap">
              {[
                node.location || fmt.country(node.countryCode),
                node.provider,
                <span key="address" className="font-mono">
                  {node.address}
                </span>,
                node.agentVersion && (
                  <span key="agent">
                    {t("node.agent")} <span className="font-mono">{node.agentVersion}</span>
                  </span>
                ),
                older && (
                  <Link key="outdated" to="/updates" className="font-semibold text-warn-text underline decoration-dotted underline-offset-2">
                    {t("up.node.outdated")}
                  </Link>
                ),
              ]
                .filter(Boolean)
                .map((piece, i) => (
                  <span key={i} className="whitespace-nowrap before:inline-block before:w-3.5 before:text-center before:content-['·']">
                    {piece}
                  </span>
                ))}
            </span>
          </p>
        </div>
        {!retired && <div className="flex gap-2 max-lg:hidden">{actions}</div>}
      </div>
      {/* below 1024 px the buttons take a row of their own under the meta line, as on the phone */}
      {!retired && (
        <div className="flex flex-col gap-1.5 lg:hidden">
          <div className="flex gap-2">{actions}</div>
          {restartOff && <p className="text-xs text-muted">{restartOff}</p>}
        </div>
      )}

      <StateBanner data={data} tab={tab} onTab={setTab} onRestart={() => setRestart(true)} />

      <Tabs
        aria-label={node.name}
        items={items}
        value={tab}
        onValueChange={setTab}
        className="[&>div:first-child]:-mx-4 [&>div:first-child]:px-4 md:[&>div:first-child]:mx-0 md:[&>div:first-child]:px-0"
      />
      {restart && <RestartModal onClose={() => setRestart(false)} data={data} />}
    </div>
  );
}

/** Restart every profile of the node, saying who notices: the people connected to it right now. */
function RestartModal({ onClose, data }: { onClose: () => void; data: Plain<GetNodeResponse> }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const node = data.node!;
  const people = node.online.reduce((n, o) => n + o.users, 0);
  const restart = useMutation({
    mutationFn: () => nodesApi.restartInbounds({ nodeId: node.id }),
    onSuccess: (r) => {
      onClose();
      toast(t.n("node.restarted", r.restarted));
      void qc.invalidateQueries({ queryKey: ["node", node.id] });
    },
  });
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("node.restartTitle", { name: node.name })}
      description={people > 0 ? t.n("node.restartPeople", people) : t("node.restartNobody")}
      footer={
        <>
          <Button variant="ghost" size="md" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="md" disabled={restart.isPending} onClick={() => restart.mutate()}>
            {t("node.restartDo")}
          </Button>
        </>
      }
    >
      {restart.isError && (
        <Notice tone="danger" className="items-start">
          {errorText(restart.error, t)}
        </Notice>
      )}
    </Modal>
  );
}
