import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { useToast } from "@/components/ui/toast";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { nodes as nodesApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { doctorQuery, isIssue } from "@/lib/health";
import { agentLinked } from "@/lib/node-status";
import { nodeQuery, nodesQuery } from "@/lib/queries";
import "./node.css";
import { StateBanner } from "./banner";
import { NodeDoctorTab } from "./doctor";
import { ExitTab } from "./exit";
import { HistoryTab } from "./history";
import { NodeHeader } from "./header";
import { OverviewTab } from "./overview";
import { ProfilesTab } from "./profiles";
import { ServerTab } from "./server";
import type { HistoryView, NodeSearch, NodeTab, ServerSection } from "./tabs";
import { useWays, WaysTab } from "./ways";

export function NodeScreen() {
  const t = useT();
  const { id = "" } = useParams({ strict: false });
  const q = useQuery(nodeQuery(id));
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as NodeSearch;
  // an address with the node's name instead of its id (a link made by hand, the assistant's old card): go to the node whose name it is
  const list = useQuery({ ...nodesQuery, enabled: q.isError });
  const byName = q.isError ? list.data?.nodes.find((n) => n.name.toLowerCase() === id.toLowerCase())?.id : undefined;
  useEffect(() => {
    if (byName && byName !== id) void navigate({ to: "/nodes/$id", params: { id: byName }, search, replace: true });
  }, [byName, id, navigate, search]);

  if (q.data) return <NodeDetail data={q.data} />;
  return (
    <div className="flex flex-col gap-4">
      <Link to="/nodes" aria-label={t("node.back")} className={buttonClass("secondary", "md") + " w-fit"}>
        <Icon name="back" size={14} />
        {t("nav.nodes")}
      </Link>
      {q.isError && !byName && !list.isLoading ? (
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState title={t("node.notFound")}>{t("node.notFoundBody")}</EmptyState>
        </div>
      ) : (
        <p className="text-muted">{t("common.loading")}</p>
      )}
    </div>
  );
}

export function NodeDetail({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as NodeSearch;
  const node = data.node!;
  const doctor = useQuery(doctorQuery(node.id));
  const [restart, setRestart] = useState(false);
  const go = (next: NodeSearch) => void navigate({ to: "/nodes/$id", params: { id: node.id }, search: next, replace: true });
  const setTab = (next: string) => go(next === "overview" ? {} : { tab: next as NodeTab });
  const setSection = (sv: ServerSection) => go(sv === "node" ? { tab: "server" } : { tab: "server", sv });
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
      content: search.tab === "history" ? <HistoryTab data={data} view={search.view === "journal" || search.view === "logs" ? search.view : "events"} onView={setView} /> : null,
    },
    {
      value: "doctor",
      label: t("node.tab.doctor"),
      badge: issues,
      content: search.tab === "doctor" ? <NodeDoctorTab nodeId={node.id} nodeName={node.name} /> : null,
    },
    { value: "server", label: t("node.tab.server"), content: search.tab === "server" ? <ServerTab data={data} section={search.sv ?? "node"} onSection={setSection} /> : null },
  ];
  // a retired node has nothing left to run or route: what it was, what happened, and the server (SSH access stays)
  const items = retired ? all.filter((i) => i.value === "overview" || i.value === "history" || i.value === "server") : all;
  const tab = items.some((i) => i.value === search.tab) ? search.tab! : "overview";

  return (
    <div className="nd flex flex-col gap-4">
      {search.from === "map" && (
        <nav aria-label="breadcrumb" className="flex items-center gap-1.5 text-[13px] text-muted">
          <Link to="/nodes" className="text-sec hover:text-fg">
            {t("fm.crumb.nodes")}
          </Link>
          <Icon name="chevronRight" size={12} aria-hidden className="rtl:rotate-180" />
          <Link to="/nodes" search={{ view: "map", focus: node.id }} className="text-sec hover:text-fg">
            {t("fm.crumb.map")}
          </Link>
          <Icon name="chevronRight" size={12} aria-hidden className="rtl:rotate-180" />
          <span className="font-mono font-normal text-fg">{node.name}</span>
        </nav>
      )}
      <NodeHeader data={data} doctorN={issues} restartOff={restartOff} onRestart={() => setRestart(true)} onDoctor={() => setTab("doctor")} />

      <StateBanner data={data} tab={tab} onTab={setTab} onRestart={() => setRestart(true)} />

      <Tabs
        aria-label={node.name}
        items={items}
        value={tab}
        onValueChange={setTab}
        className="mt-1 [&>div:first-child]:-mx-4 [&>div:first-child]:px-4 md:[&>div:first-child]:mx-0 md:[&>div:first-child]:px-0"
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
