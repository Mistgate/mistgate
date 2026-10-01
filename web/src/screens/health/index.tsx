import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useAddNode } from "@/components/add-node";
import { PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusDot } from "@/components/ui/status";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { problemCount, useFleetSummary } from "@/lib/fleet";
import { useNow } from "@/lib/time";
import { alertsQuery, checksQuery, doctorQuery, isLoud } from "@/lib/health";
import { isProblem, nodeKind, useNodeStatus } from "@/lib/node-status";
import { overviewQuery } from "@/lib/queries";
import { AlertsTab } from "./alerts";
import { ChecksTab } from "./checks";
import { DoctorTab } from "./doctor";
import { useFixFlow } from "./fix";
import type { HealthTab } from "./tabs";
import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";

/** Health: Alerts (what is wrong now and what was), Checks (the client's view of every profile), Doctor (host checks with fixes). */
export function HealthScreen() {
  const t = useT();
  const addNode = useAddNode();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: HealthTab };
  const tab = search.tab ?? "alerts";
  const fleet = useFleetSummary();
  const flow = useFixFlow(); // one flow for the Alerts and Doctor tabs: a fix started on one shows its progress on the other

  const alerts = useQuery(alertsQuery);
  const checks = useQuery(checksQuery());
  const doctor = useQuery({ ...doctorQuery(), enabled: tab === "doctor" });
  const setTab = (next: string) => void navigate({ to: "/health", search: next === "alerts" ? {} : { tab: next as HealthTab }, replace: true });

  const clock = useNow();
  const now = alerts.data?.nowUnix ?? Math.floor(clock / 1000);
  const active = alerts.data?.active ?? [];
  const loudAlerts = active.filter((a) => isLoud(a, now));
  const critical = loudAlerts.filter((a) => a.severity === AlertSeverity.CRITICAL).length;
  const min = Math.max(1, Math.round((checks.data?.intervalS ?? 300) / 60));
  // the number of the header and the menu; the alerts tab counts its own rows
  const problems = problemCount(fleet);

  const header = (sub: string) => (
    <div className="flex flex-col gap-[3px]">
      <PageTitle>{t("hl.title")}</PageTitle>
      <span className="text-xs text-muted">{sub}</span>
    </div>
  );

  if (!fleet.loading && fleet.nodes === 0)
    return (
      <div className="flex flex-col gap-3.5">
        {header(t("hl.empty.title"))}
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState
            title={t("hl.empty.title")}
            action={
              <Button variant="primary" size="lg" onClick={() => addNode()}>
                {t("node.add.button")}
              </Button>
            }
          >
            {t("hl.empty.body")}
          </EmptyState>
        </div>
      </div>
    );

  const failed = (q: { error: unknown; refetch: () => unknown }) => <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  const loading = <Pending />;

  const items: TabItem[] = [
    {
      value: "alerts",
      label: (
        <>
          {t("hl.tab.alerts")}
          {loudAlerts.length > 0 && <TabCount n={loudAlerts.length} danger={critical > 0} />}
        </>
      ),
      content: alerts.data ? <AlertsTab active={active} history={alerts.data.history} now={now} flow={flow} /> : alerts.isError ? failed(alerts) : loading,
    },
    {
      value: "checks",
      label: t("hl.tab.checks"),
      content: tab !== "checks" ? null : checks.data ? <ChecksTab data={checks.data} /> : checks.isError ? failed(checks) : loading,
    },
    {
      value: "doctor",
      label: t("hl.tab.doctor"),
      content: tab !== "doctor" ? null : doctor.data ? <DoctorTab data={doctor.data} flow={flow} /> : doctor.isError ? failed(doctor) : loading,
    },
  ];

  return (
    <div className="flex flex-col gap-3.5">
      {header(fleet.loading ? t("hl.sum.loading") : problems > 0 ? t("hl.sum.active", { problems: t.n("health.problems", problems), min }) : t("hl.sum.quiet", { min }))}
      <NodeProblems covered={new Set(loudAlerts.map((a) => a.nodeId))} />
      <Tabs aria-label={t("hl.tabs")} items={items} value={tab} onValueChange={setTab} />
      {flow.modal}
    </div>
  );
}

/** The alerts tab's own count: its rows; red when one of them is critical. */
function TabCount({ n, danger }: { n: number; danger: boolean }) {
  return (
    <span
      className={cx(
        "flex h-4 min-w-4 items-center justify-center rounded-lg px-1 font-mono text-[10px]",
        danger ? "bg-danger-soft text-danger-text" : "bg-warn-soft text-warn-text",
      )}
    >
      {n}
    </span>
  );
}

/**
 * Nodes that count as a problem but have no alert (yet, or by nature: a node without profiles, a profile that did not
 * start). They are in the number of the header, so they are named here, each a link to where it is fixed.
 */
function NodeProblems({ covered }: { covered: ReadonlySet<string> }) {
  const t = useT();
  const st = useNodeStatus();
  const cards = useQuery(overviewQuery()).data?.nodes ?? [];
  const nodes = cards.filter((n) => isProblem(n) && !covered.has(n.id));
  if (nodes.length === 0) return null;
  return (
    <section className="flex flex-col gap-1 rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1.5">
      <SectionLabel as="h2" icon="server" tone="sky" className="pb-1.5">
        {t("hl.nodes.title")}
      </SectionLabel>
      {nodes.map((n) => {
        const toProfiles = n.reason?.code === "no_profiles" || n.reason?.code === "inbound_failed";
        return (
          <Link
            key={n.id}
            to="/nodes/$id"
            params={{ id: n.id }}
            search={toProfiles ? { tab: "profiles" } : {}}
            className="flex min-h-10 items-center gap-2.5 border-t border-line py-2 text-[13px]"
          >
            <StatusDot kind={nodeKind(n)} />
            <span className="min-w-0 flex-1 text-pretty">
              <b className="font-bold">{n.name}</b> <span className="text-muted">· {st.line(n)}</span>
            </span>
            <Icon name="chevronRight" size={14} className="flex-none text-faint" />
          </Link>
        );
      })}
    </section>
  );
}
