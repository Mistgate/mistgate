import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusDot } from "@/components/ui/status";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertKind } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryQuery } from "@/lib/globalping";
import { alertsQuery, checksQuery, countryIn, severityKind, sourceName, type Alert, type CountryCheck, type CountryLook } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { darkEverywhere, history6Query, historyQuery, ipv6Helps, tallyOf, type DarkEverywhere, type Lane, type Tally } from "@/lib/reachability";
import { useIsOwner } from "@/lib/updates";
import { Ipv6Tag } from "@/screens/health/alerts";
import { CountryMark } from "@/screens/health/country";
import { NodesTable, type NodeRow } from "./nodes";
import { CountryPicker, SourceCards } from "./sources";

const WEEK = 7 * 86400;

/**
 * Reachability: are the nodes reachable from one country (Russia by default), and since when. The header names the country
 * (the owner picks it there); a strip counts the nodes by their verdict now; the nodes follow with each source's word and week;
 * then the country_blocked alerts of the week and, at the bottom, the sources themselves. Everybody who sees Health sees this;
 * the sources and the country are the owner's.
 */
export function ReachabilityScreen() {
  const t = useT();
  const owner = useIsOwner();
  const history = useQuery(historyQuery);
  const checks = useQuery(checksQuery());
  const nodesQ = useQuery(nodesQuery);
  const country = useQuery({ ...countryQuery, enabled: owner });
  const code = country.data?.country ?? history.data?.country ?? "";
  const h = history.data;
  // the IPv6 week only while Globalping checks IPv6: then every row carries CountryCheck.ipv6
  const v6On = (checks.data?.countryChecks ?? []).some((c) => c.ipv6);
  const h6 = useQuery({ ...history6Query, enabled: v6On });

  const nodes: NodeRow[] = (nodesQ.data?.nodes ?? [])
    .filter((n) => n.status !== NodeStatus.PENDING && n.status !== NodeStatus.RETIRED)
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode }));
  const byNode = new Map((checks.data?.countryChecks ?? []).map((c) => [c.nodeId, c]));
  const lanes = new Map((h?.nodes ?? []).map((n) => [n.nodeId, new Map<string, Lane>(n.lanes.map((l) => [l.source, l]))]));
  const lanes6 = new Map((v6On ? (h6.data?.nodes ?? []) : []).flatMap((n) => n.lanes.filter((l) => l.source === "globalping").map((l) => [n.nodeId, l] as const)));
  const sources = h?.sources ?? [];

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-wrap items-end gap-x-6 gap-y-2.5">
        <div className="flex min-w-[220px] flex-1 flex-col gap-[3px]">
          <PageTitle>{t("nav.reachability")}</PageTitle>
          <span className="text-xs text-muted">{code ? t("rc.sub", { country: countryIn(t, code) }) : " "}</span>
        </div>
        <PageNav nodes={!!h && sources.length > 0} />
        {owner && <CountryPicker />}
      </div>

      {history.isError && !h ? (
        <QueryError error={history.error} onRetry={() => void history.refetch()} />
      ) : !h ? (
        <Pending />
      ) : sources.length === 0 ? (
        <AllOff owner={owner} />
      ) : (
        <>
          <Summary tally={tallyOf(nodes.map((n) => n.id), (id) => byNode.get(id)?.verdict)} />
          <Ipv6Helps n={nodes.filter((n) => ipv6Helps(byNode.get(n.id))).length} />
          <DarkEverywhereLines rows={darkEverywhere(nodes.map((n) => byNode.get(n.id)).filter((c): c is CountryCheck => !!c))} />
          <NodesTable nodes={nodes} checks={byNode} lanes={lanes} lanes6={lanes6} sources={sources} probeNode={checks.data?.tspuRun?.nodeId ?? ""} country={code} since={h.sinceUnix} now={h.nowUnix} />
        </>
      )}

      <Events code={code} />

      <section id="sources" aria-labelledby="reach-sources" tabIndex={-1} className="flex scroll-mt-20 flex-col gap-3 pt-1.5 outline-none">
        <div className="flex flex-col gap-1">
          <h2 id="reach-sources" className="text-[15px] font-extrabold tracking-[-0.02em]">
            {t("rc.sources")}
          </h2>
          <p className="text-xs leading-normal text-muted">{owner ? t("rc.sources.sub") : t("rc.sources.owner")}</p>
        </div>
        {owner && <SourceCards />}
      </section>
    </div>
  );
}

const tallyKinds: { kind: CountryLook["kind"]; key: MessageKey; cls: string }[] = [
  { kind: "ok", key: "rc.sum.ok", cls: "border border-line bg-surface" },
  { kind: "partial", key: "rc.sum.partial", cls: "tone-warn tint tone-text" },
  { kind: "warn", key: "rc.sum.warn", cls: "tone-warn tint tone-text" },
  { kind: "bad", key: "rc.sum.bad", cls: "tone-bad tint tone-text" },
  { kind: "listed", key: "rc.sum.listed", cls: "tone-warn tint tint-dashed tone-text" },
  { kind: "unknown", key: "rc.sum.unknown", cls: "border border-dashed border-line bg-surface text-muted" },
];

/** Nodes per verdict now, in the colours and glyphs of the Health column; a kind nobody is in stays quiet. Two, three or six to a row. */
function Summary({ tally }: { tally: Tally }) {
  const t = useT();
  return (
    <ul aria-label={t("rc.sum.title")} className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6">
      {tallyKinds.map(({ kind, key, cls }) => {
        const n = tally[kind];
        return (
          <li
            key={kind}
            data-kind={kind}
            className={cx("flex min-w-0 flex-col gap-1 rounded-card px-3 py-2.5", n > 0 ? cls : "border border-line bg-surface text-faint")}
          >
            <span className={cx("font-mono text-[22px] leading-none font-extrabold tracking-[-0.03em]", n > 0 && kind === "ok" && "text-fg")}>{n}</span>
            <span className={cx("flex min-w-0 items-center gap-1.5 text-xs font-bold", n > 0 && (kind === "ok" || kind === "unknown") && "text-muted")}>
              <span className={cx("flex", n === 0 && "opacity-50")}>
                <CountryMark kind={kind} />
              </span>
              <span className="min-w-0 leading-tight">{t(key)}</span>
            </span>
          </li>
        );
      })}
    </ul>
  );
}

/** The nodes cut over IPv4 that IPv6 still reaches, under the IPv4 tally: one quiet line, nothing when there are none. */
function Ipv6Helps({ n }: { n: number }) {
  const t = useT();
  if (n === 0) return null;
  return (
    <p data-testid="ipv6-helps" className="flex items-center gap-2 rounded-card border border-line bg-surface px-3.5 py-2.5 text-[13px]">
      <span className="tone-ok tone-text flex">
        <CountryMark kind="ok" />
      </span>
      <span className="font-mono text-[10px] font-bold text-muted">IPv6</span>
      <span className="min-w-0 font-semibold text-pretty">{t("rc.sum.v6", { n })}</span>
    </p>
  );
}

/**
 * A network that reaches none of the nodes ("MTS (КРЫМ) reaches none of your nodes", with the source): an operator that cuts the
 * whole fleet alike, which no alert covers. Nothing when there is none.
 */
function DarkEverywhereLines({ rows }: { rows: readonly DarkEverywhere[] }) {
  const t = useT();
  if (rows.length === 0) return null;
  return (
    <section aria-label={t("rc.darkAll.title")} className="flex flex-col gap-1.5 rounded-card tone-warn tint px-3.5 py-2.5 text-[13px]">
      <ul className="flex flex-col gap-1">
        {rows.map((r) => (
          <li key={`${r.source}|${r.name}`} data-testid="dark-everywhere" className="flex items-baseline gap-2">
            <span aria-hidden className="tone-bad tone-text text-xs font-extrabold">
              ✕
            </span>
            <span className="min-w-0 text-pretty">
              <b className="font-bold">{t("rc.darkAll", { network: r.name })}</b>
              <span className="text-muted"> · {sourceName(t, r.source)}</span>
            </span>
          </li>
        ))}
      </ul>
      <p className="pl-5 text-xs text-muted">{t("rc.darkAll.hint")}</p>
    </section>
  );
}

/** Every source off: nothing is checked. The owner gets the way to the sources below; anyone else learns who switches them on. */
function AllOff({ owner }: { owner: boolean }) {
  const t = useT();
  return (
    <section data-testid="all-off" className="flex flex-col items-start gap-2.5 rounded-card-lg border border-dashed border-line bg-surface p-4 md:p-[18px]">
      <div className="flex items-center gap-2.5 text-sm font-bold">
        <StatusDot kind="off" />
        {t("rc.off.title")}
      </div>
      <p className="max-w-2xl text-[13px] leading-normal text-pretty text-muted">{owner ? t("rc.off.owner") : t("rc.off.other")}</p>
      {owner && (
        <Button variant="secondary" size="sm" onClick={() => jump("sources")}>
          {t("rc.off.go")}
        </Button>
      )}
    </section>
  );
}

/** To a part of the page, with the focus: on a big fleet the sources sit far below the nodes. */
function jump(id: string) {
  const to = document.getElementById(id)?.closest("section");
  to?.scrollIntoView({ behavior: "smooth", block: "start" });
  to?.focus({ preventScroll: true });
}

/** The page's own links in its header: the nodes (when there are), the events, the sources. The summary is right under it. */
function PageNav({ nodes }: { nodes: boolean }) {
  const t = useT();
  const links: [string, string][] = [...(nodes ? [["reach-nodes", t("rc.nodes.title")] as [string, string]] : []), ["reach-events", t("rc.nav.events")], ["sources", t("rc.sources")]];
  return (
    <nav aria-label={t("rc.nav")} className="flex flex-wrap items-center gap-0.5">
      {links.map(([id, label]) => (
        <a
          key={id}
          href={`#${id}`}
          onClick={(e) => {
            e.preventDefault();
            jump(id);
          }}
          className="flex h-8 items-center gap-1 rounded-ctl px-2 text-xs font-bold text-muted transition-colors duration-200 hover:bg-surface-2 hover:text-fg"
        >
          <Icon name="arrowDown" size={12} aria-hidden />
          {label}
        </a>
      ))}
    </nav>
  );
}

/** The alert's verdict in a word: "blocked on some networks", "TCP blocked" or "unreachable". */
const eventVerdict: Record<string, MessageKey> = { partial: "cr.v.partialLong", tcp_blocked: "cr.v.tcp", blocked: "cr.v.blocked" };

/** The country_blocked alerts of the week, open and closed, newest first; each leads to its row on Health → Alerts. */
function Events({ code }: { code: string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(alertsQuery);
  if (!q.data) return null;
  const now = q.data.nowUnix;
  const mine = (a: Alert) => a.kind === AlertKind.COUNTRY_BLOCKED;
  const rows = [
    ...q.data.active.filter(mine).sort((a, b) => b.firstSeenUnix - a.firstSeenUnix),
    ...q.data.history.filter((a) => mine(a) && a.resolvedAtUnix >= now - WEEK),
  ].slice(0, 8);
  return (
    <section aria-labelledby="reach-events" tabIndex={-1} className="flex scroll-mt-20 flex-col rounded-card-lg border border-line bg-surface px-4 pt-3.5 pb-1.5 outline-none">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1 pb-2.5">
        <SectionLabel as="h2" icon="bell" tone="rose">
          <span id="reach-events">{t("rc.ev.title")}</span>
        </SectionLabel>
        {code && <span className="text-xs text-muted">{t("rc.ev.sub", { country: countryIn(t, code) })}</span>}
        <Link to="/health" className="ml-auto text-xs font-semibold text-muted hover:text-fg hover:underline">
          {t("rc.ev.all")}
        </Link>
      </div>
      {rows.length === 0 && <p className="border-t border-line py-3 text-[13px] text-muted">{t("rc.ev.none")}</p>}
      {rows.map((a) => {
        const open = a.resolvedAtUnix === 0;
        const lasted = Math.max(0, a.resolvedAtUnix - (a.openedUnix || a.firstSeenUnix));
        const verdict = eventVerdict[a.params.verdict ?? ""];
        const when = open
          ? t("rc.ev.open", { ago: fmt.ago(a.openedUnix || a.firstSeenUnix) })
          : [t("rc.ev.closed", { ago: fmt.ago(a.resolvedAtUnix) }), lasted > 0 ? t("hl.alerts.lasted", { duration: fmt.duration(lasted) }) : ""].filter(Boolean).join(" · ");
        return (
          <Link
            key={`${a.id}/${a.resolvedAtUnix}`}
            to="/health"
            hash={open ? `alert-${a.id}` : `alert-${a.id}-${a.resolvedAtUnix}`}
            data-testid="reach-event"
            className="flex min-h-11 items-center gap-2.5 border-t border-line py-2 text-[13px] transition-colors hover:text-accent-text"
          >
            <StatusDot kind={open ? severityKind(a.severity) : "off"} />
            <span className="min-w-0 flex-1 text-pretty">
              <b className="mr-1.5 font-mono text-xs">{a.nodeName}</b>
              <span className={cx("font-semibold", !open && "text-muted")}>{verdict ? t(verdict) : a.params.verdict}</span>
              <Ipv6Tag alert={a} className="ml-1.5 align-[1px]" />
              <span className="text-muted"> · {when}</span>
            </span>
            <span className="font-mono text-[11px] whitespace-nowrap text-muted max-sm:hidden">{fmt.stamp(open ? a.openedUnix || a.firstSeenUnix : a.resolvedAtUnix)}</span>
            <Icon name="chevronRight" size={14} className="flex-none text-faint" />
          </Link>
        );
      })}
    </section>
  );
}
