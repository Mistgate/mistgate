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
import type { Plain } from "@/lib/plain";
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
import { EventsTab } from "./events";
import { LogsTab } from "./logs";
import { OverviewTab } from "./overview";
import { ProfilesTab } from "./profiles";
import { SettingsTab } from "./settings";
import type { NodeTab } from "./tabs";
import { UsersTab } from "./users";

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
      search={{ tab: "settings" }}
      hash="warp"
      aria-label={t("node.warpChip", { state: t(warpWord[state]) })}
      title={t("node.warpChip", { state: t(warpWord[state]) })}
      className="rounded-xl transition-transform duration-200 hover:-translate-y-px"
    >
      <StatusPill kind={warpKind[state]} label={label} sm />
    </Link>
  );
}

export function NodeDetail({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: NodeTab; add?: string };
  const tab = search.tab ?? "overview";
  const node = data.node!;
  const kind = nodeKind(node);
  const older = useOlderNodes().has(node.id);
  const doctor = useQuery(doctorQuery(node.id));
  const [restart, setRestart] = useState(false);
  const setTab = (next: string) =>
    void navigate({
      to: "/nodes/$id",
      params: { id: node.id },
      search: next === "overview" ? {} : { tab: next as NodeTab },
      replace: true,
    });

  const retired = node.status === NodeStatus.RETIRED;
  // the agent is on the line (also when traffic does not flow): restarting works then, and only then
  const restartOff = !agentLinked(node.status) ? t("node.restartOffline") : data.inbounds.length === 0 ? t("node.restartNoProfiles") : "";
  const failed = data.inbounds.filter((i) => i.state === InboundState.FAILED).length;
  const issues = doctor.data?.nodes.find((n) => n.nodeId === node.id)?.items.filter(isIssue).length ?? 0;

  const items: TabItem[] = [
    { value: "overview", label: t("node.tab.overview"), content: <OverviewTab data={data} /> },
    {
      value: "profiles",
      label: t("node.tab.profiles"),
      badge: failed,
      content: <ProfilesTab data={data} addProfile={search.add} onAddClosed={() => search.add && setTab("profiles")} />,
    },
    { value: "users", label: t("node.tab.users"), content: <UsersTab data={data} /> },
    { value: "logs", label: t("node.tab.logs"), content: tab === "logs" ? <LogsTab data={data} /> : null },
    { value: "events", label: t("node.tab.events"), content: tab === "events" ? <EventsTab nodeId={node.id} /> : null },
    {
      value: "doctor",
      label: t("node.tab.doctor"),
      badge: issues,
      content: tab === "doctor" ? <NodeDoctorTab nodeId={node.id} nodeName={node.name} /> : null,
    },
    { value: "settings", label: t("node.tab.settings"), content: <SettingsTab data={data} /> },
  ];

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
          </div>
          <p className="text-xs text-pretty text-muted">
            {[node.location || fmt.country(node.countryCode), node.provider].filter(Boolean).join(" · ")}
            {(node.location || node.countryCode || node.provider) && " · "}
            <span className="font-mono">{node.address}</span>
            {node.agentVersion && (
              <>
                {" · "}
                {t("node.agent")} <span className="font-mono">{node.agentVersion}</span>
              </>
            )}
            {older && (
              <>
                {" · "}
                <Link to="/updates" className="font-semibold text-warn-text underline decoration-dotted underline-offset-2">
                  {t("up.node.outdated")}
                </Link>
              </>
            )}
          </p>
        </div>
        {!retired && <div className="flex gap-2 max-md:hidden">{actions}</div>}
      </div>
      {!retired && (
        <div className="flex flex-col gap-1.5 md:hidden">
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
