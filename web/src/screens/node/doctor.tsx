import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Button, buttonClass } from "@/components/ui/button";
import { Notice, EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { doctorQuery, groupDoctor, useCan } from "@/lib/health";
import { useFmt } from "@/lib/format";
import { DoctorCompact, DoctorRow, useRunDoctor } from "@/screens/health/doctor-parts";
import { useFixFlow } from "@/screens/health/fix";

const eyebrow = "text-[11px] font-bold tracking-[0.1em] text-muted uppercase";

/** This node's doctor: its checks grouped by status (problems with their explanation and fix, then what is fine, then what was not checked). */
export function NodeDoctorTab({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const q = useQuery(doctorQuery(nodeId));
  const run = useRunDoctor(nodeId);
  const flow = useFixFlow();
  const node = q.data?.nodes.find((n) => n.nodeId === nodeId);

  if (!node) {
    return q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />;
  }

  const runButton = (
    <Button variant="secondary" size="md" onClick={() => run.mutate()} disabled={run.isPending}>
      {run.isPending ? t("hl.doctor.rerunning") : t("hl.doctor.rerun")}
    </Button>
  );

  if (!node.agentSupported)
    return (
      <div className="rounded-card-lg border border-dashed border-line">
        <EmptyState
          title={t("hl.doctor.tooOld.title")}
          action={
            <Link to="/updates" className={buttonClass("secondary", "md")}>
              {t("hl.doctor.openUpdates")}
            </Link>
          }
        >
          {t("hl.doctor.tooOld.body")}
        </EmptyState>
      </div>
    );
  if (!node.hasReport)
    return (
      <div className="rounded-card-lg border border-dashed border-line">
        <EmptyState title={t("hl.doctor.noReport.title")} action={can.run && node.nodeStatus === NodeStatus.ONLINE ? runButton : undefined}>
          {t("hl.doctor.noReport.body")}
        </EmptyState>
      </div>
    );

  const g = groupDoctor(node.items);
  const offline = node.nodeStatus !== NodeStatus.ONLINE && node.nodeStatus !== NodeStatus.NO_TRAFFIC;
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-center gap-2.5">
        <div className="flex min-w-[200px] flex-1 flex-col gap-0.5">
          <span className="text-[13px] font-bold">
            {g.issues.length > 0 ? t("hl.doctor.sumNode", { bad: g.issues.length, ok: g.ok.length }) : t("hl.doctor.sumNodeOk")}
          </span>
          <span className="text-xs text-muted">{run.isPending ? t("hl.doctor.rerunning") : t("hl.doctor.checkedAgo", { ago: fmt.ago(node.receivedUnix) })}</span>
        </div>
        {can.run && !offline && runButton}
      </div>
      {offline ? (
        <Notice>{t("hl.doctor.offline", { ago: fmt.ago(node.receivedUnix) })}</Notice>
      ) : (
        node.stale && <Notice>{t("hl.doctor.stale", { age: fmt.duration(node.ageS) })}</Notice>
      )}

      {g.issues.length > 0 && (
        <section className="rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1">
          <h2 className={eyebrow}>{t("hl.doctor.groupBad")}</h2>
          {g.issues.map((i, n) => (
            <DoctorRow key={`${i.id}/${i.params.inbound_id ?? ""}`} flow={flow} nodeId={nodeId} nodeName={nodeName} item={i} first={n === 0} />
          ))}
        </section>
      )}
      {g.accepted.length > 0 && (
        <section className="rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1">
          <h2 className={eyebrow}>{t("hl.doctor.groupAccepted")}</h2>
          {g.accepted.map((i, n) => (
            <DoctorRow key={i.id} flow={flow} nodeId={nodeId} nodeName={nodeName} item={i} first={n === 0} />
          ))}
        </section>
      )}
      {g.ok.length > 0 && (
        <section className="rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1">
          <h2 className={eyebrow}>{t("hl.doctor.groupOk")}</h2>
          <DoctorCompact items={g.ok} />
        </section>
      )}
      {g.skipped.length > 0 && (
        <section className="rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1">
          <h2 className={eyebrow}>{t("hl.doctor.groupSkip")}</h2>
          <DoctorCompact items={g.skipped} />
        </section>
      )}
      {flow.modal}
    </div>
  );
}

