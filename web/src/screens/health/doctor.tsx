import { Link } from "@tanstack/react-router";
import { Button } from "@/components/ui/button";
import { StatusDot } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetDoctorResponse } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { useFmt } from "@/lib/format";
import { doctorTotals, fleetIssues, useCan, type FleetIssue, type NodeDoctor } from "@/lib/health";
import type { Plain } from "@/lib/plain";
import { DoctorRow, NodeDoctorLink, useRunDoctor } from "./doctor-parts";
import type { FixFlow } from "./fix";

const keyOf = (x: Pick<FleetIssue, "nodeId" | "item">) => `${x.nodeId}/${x.item.id}/${x.item.params.inbound_id ?? ""}`;

/** The fleet doctor: every problem the nodes report, worst first, each with its explanation and, where there is one, a fix. */
export function DoctorTab({ data, flow }: { data: Plain<GetDoctorResponse>; flow: FixFlow }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const run = useRunDoctor();
  const issues = fleetIssues(data.nodes);
  const totals = doctorTotals(data.nodes);

  // A row whose fix just worked leaves the report at once; it stays as "Fixed" until the fix's 30 s are over.
  const live = new Set(issues.map(keyOf));
  const kept = flow
    .pending()
    .filter((p) => p.item && !live.has(p.key))
    .map((p) => ({ nodeId: p.nodeId, nodeName: p.nodeName, item: p.item!, stale: false, ageS: 0 }));
  const rows: FleetIssue[] = [...issues, ...kept];

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-center gap-2.5">
        <span className="min-w-[200px] flex-1 text-[13px] text-muted">
          {run.isPending ? t("hl.doctor.rerunning") : issues.length > 0 ? t("hl.doctor.sum", { items: totals.items, nodes: totals.nodes, n: issues.length }) : t("hl.doctor.allGreen")}
        </span>
        {can.run && (
          <Button variant="secondary" size="md" onClick={() => run.mutate()} disabled={run.isPending}>
            {run.isPending ? t("hl.doctor.rerunning") : t("hl.doctor.rerun")}
          </Button>
        )}
      </div>
      {rows.length > 0 ? (
        <section className="rounded-card-lg border border-line bg-surface px-4 py-1">
          {rows.map((x, i) => (
            <DoctorRow key={keyOf(x)} flow={flow} nodeId={x.nodeId} nodeName={x.nodeName} item={x.item} showNode first={i === 0} stale={x.stale ? fmt.duration(x.ageS) : undefined} />
          ))}
        </section>
      ) : (
        totals.nodes > 0 && (
          <div className="flex items-center gap-2.5 rounded-card border border-line bg-surface px-4 py-3.5 text-[13px]">
            <StatusDot kind="ok" />
            <b>{t("hl.doctor.allGreen")}</b>
          </div>
        )
      )}
      <QuietNodes nodes={data.nodes} />
    </div>
  );
}

/**
 * The nodes the list above cannot speak for, one line each and each for its own reason: an agent too old for the doctor
 * (with the way to Updates), a node that is offline (its last report is what is shown), one whose first report has not
 * come. The name opens the node's Doctor tab.
 */
function QuietNodes({ nodes }: { nodes: readonly NodeDoctor[] }) {
  const t = useT();
  const fmt = useFmt();
  const lines = nodes.flatMap((n) => {
    const reported = n.hasReport && n.items.length > 0;
    const offline = n.nodeStatus !== NodeStatus.ONLINE && n.nodeStatus !== NodeStatus.NO_TRAFFIC;
    if (!n.agentSupported) return [{ n, text: n.agentVersion ? t("hl.doctor.line.tooOld", { version: n.agentVersion }) : t("hl.doctor.line.tooOldBare"), updates: true }];
    if (offline && n.lastSeenUnix > 0) return [{ n, text: t(reported ? "hl.doctor.line.offline" : "hl.doctor.line.offlineNone", { time: fmt.stamp(n.lastSeenUnix) }), updates: false }];
    if (!reported) return [{ n, text: t("hl.doctor.line.waiting"), updates: false }];
    return [];
  });
  if (lines.length === 0) return null;
  return (
    <ul className="flex flex-col gap-1.5 text-xs leading-snug text-muted">
      {lines.map(({ n, text, updates }) => (
        <li key={n.nodeId} className="text-pretty">
          <NodeDoctorLink id={n.nodeId} name={n.nodeName} />
          {" — "}
          {text}
          {updates && (
            <>
              {" · "}
              <Link to="/updates" className="font-bold text-accent-text underline decoration-dotted underline-offset-2">
                {t("hl.doctor.openUpdates")}
              </Link>
            </>
          )}
        </li>
      ))}
    </ul>
  );
}
