import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useAddNode } from "@/components/add-node";
import { Button, buttonClass } from "@/components/ui/button";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { problemCount, useFleetSummary } from "@/lib/fleet";
import { alertsQuery, checksQuery, doctorQuery, fleetIssues, isLoud, isPeopleAlert } from "@/lib/health";
import { useNow } from "@/lib/time";
import { AlertsTab } from "./alerts";
import { ChecksTab } from "./checks";
import { DoctorTab } from "./doctor";
import { useFixFlow } from "./fix";
import { Calm, Dot, LoadCards, LoadFailed, P, PageHead, TabLabel } from "./page-parts";
import { PeopleTab } from "./people";
import type { HealthTab } from "./tabs";
import "./health.css";

/** The first round of the checks has not run: nodes and profiles are there, and no cell has a result yet. */
export function isFirstRound(checks: { columns: readonly unknown[]; rows: readonly { cells: readonly { last?: unknown }[] }[] } | undefined): boolean {
  return !!checks && checks.columns.length > 0 && checks.rows.length > 0 && checks.rows.every((r) => r.cells.every((c) => !c.last));
}

/** "Health": Alerts (what is wrong with the nodes now and what was), People (the same for people), Checks (the client's view of every profile), Doctor (host checks with fixes). */
export function HealthScreen() {
  const t = useT();
  const addNode = useAddNode();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: HealthTab };
  const tab = search.tab ?? "alerts";
  const fleet = useFleetSummary();
  const flow = useFixFlow({ inline: true }); // one flow for the Alerts and Doctor tabs: a fix started on one shows its progress on the other

  const alerts = useQuery(alertsQuery);
  const checks = useQuery(checksQuery());
  const doctor = useQuery(doctorQuery());
  const setTab = (next: string) => void navigate({ to: "/health", search: next === "alerts" ? {} : { tab: next as HealthTab }, replace: true });

  const clock = useNow();
  const now = alerts.data?.nowUnix ?? Math.floor(clock / 1000);
  const active = alerts.data?.active ?? [];
  // alerts about people are not problems of a node: they have their own tab and count, and stay out of the node count and the nodes named below
  const loudAlerts = active.filter((a) => !isPeopleAlert(a) && isLoud(a, now));
  const loudPeople = active.filter((a) => isPeopleAlert(a) && isLoud(a, now)).length;
  const critical = Math.max(fleet.critical, loudAlerts.filter((a) => a.severity === AlertSeverity.CRITICAL).length);
  const min = Math.max(1, Math.round((checks.data?.intervalS ?? 300) / 60));
  // the number of the header and the menu; the alerts tab counts its own rows
  const problems = problemCount(fleet);
  // the Cloudflare edition runs no checker: no check interval, but its alert pass runs every minute (internal/panel/app/edge_tick.go)
  const noChecks = useEdition().off("health_checks");
  const first = !noChecks && isFirstRound(checks.data);
  const issues = doctor.data ? fleetIssues(doctor.data.nodes).length : 0;

  const loading = fleet.loading;
  const failed = !loading && alerts.isError && !alerts.data;
  const bare = !loading && !failed && fleet.nodes === 0;

  const sum = loading
    ? t("hl.sum.loading")
    : failed
      ? t("hlp.sum.err")
      : bare
        ? t("hlp.sum.nonodes")
        : first
          ? t("hlp.sum.first")
          : noChecks
            ? problems > 0
              ? t("hl.sum.edgeActive", { problems: t.n("health.problems", problems) })
              : t("hl.sum.edgeQuiet")
            : problems > 0
              ? t("hl.sum.active", { problems: t.n("health.problems", problems) + (critical > 0 ? ` · ${t("hlp.sum.crit", { n: critical })}` : ""), min })
              : t("hl.sum.quiet", { min });

  // a tab whose own query has not come, has failed, or the state of the whole page (the board's skeleton, error card and "no nodes")
  const state = (q: { data?: unknown; isError: boolean; refetch: () => unknown }, ready: () => ReactNode): ReactNode => {
    if (loading) return <LoadCards label={t("hlp.loading")} />;
    if (failed) return <LoadFailed title={t("hlp.err.title")} text={t("hlp.err.text")} retry={t("hlp.retry")} onRetry={() => void alerts.refetch()} />;
    if (bare)
      return (
        <Calm
          className="hl-calm hl-top rc-emp"
          icon={P.server}
          title={t("hlp.nonodes.title")}
          text={t("hlp.nonodes.text")}
          action={
            <span className="hl-two">
              <Button variant="primary" size="lg" onClick={() => addNode()}>
                {t("node.add.button")}
              </Button>
              <Link to="/nodes" className={cx(buttonClass("secondary", "lg"), "btn")}>
                {t("hlp.nonodes.open")}
              </Link>
            </span>
          }
        />
      );
    if (q.data) return ready();
    if (q.isError) return <LoadFailed title={t("hlp.err.title")} text={t("hlp.err.text")} retry={t("hlp.retry")} onRetry={() => void q.refetch()} />;
    return <LoadCards label={t("hlp.loading")} />;
  };
  const note = first && (
    <p className="rc-nt">
      <Dot kind="busy" />
      <span>{t("hlp.first.note")}</span>
    </p>
  );
  const on = !loading && !failed;

  const items: TabItem[] = [
    {
      value: "alerts",
      label: <TabLabel text={t("hl.tab.alerts")} n={on ? loudAlerts.length : 0} tone="warn" />,
      content: state(alerts, () => (
        <>
          {note}
          <AlertsTab active={active} history={alerts.data!.history} now={now} flow={flow} first={first} onChecks={noChecks ? undefined : () => setTab("checks")} />
        </>
      )),
    },
    {
      value: "people",
      label: <TabLabel text={t("hl.tab.people")} n={on ? loudPeople : 0} />,
      content: state(alerts, () => (
        <>
          {note}
          <PeopleTab active={active} now={now} flow={flow} first={first} />
        </>
      )),
    },
    {
      value: "checks",
      label: t("hl.tab.checks"),
      content: tab !== "checks" ? null : state(checks, () => <ChecksTab data={checks.data!} />),
    },
    {
      value: "doctor",
      label: <TabLabel text={t("hl.tab.doctor")} n={on ? issues : 0} />,
      content: tab !== "doctor" ? null : state(doctor, () => <DoctorTab data={doctor.data!} flow={flow} first={first} />),
    },
  ];

  return (
    <div className="hlp">
      <PageHead title={t("hl.title")} sub={sum} />
      <Tabs aria-label={t("hl.tabs")} items={items} value={tab} onValueChange={setTab} />
      {flow.modal}
    </div>
  );
}
