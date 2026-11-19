import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryQuery } from "@/lib/globalping";
import type { CountryNow } from "@/lib/country-now";
import { checksQuery, countryIn, listOf, sourceName, type CountryCheck } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { darkEverywhere, history6Query, historyQuery, ipv6Helps, tallyOf, type DarkEverywhere, type Lane } from "@/lib/reachability";
import { useIsOwner } from "@/lib/updates";
import { Calm, Dot, Gl, LoadCards, LoadFailed, P, TabLabel } from "@/screens/health/page-parts";
import { EventsCard, EventsTab, useEvents } from "./events";
import { NodesTable, stateKinds, stateOf, stateWord, type NodeRow, type State } from "./nodes";
import { useReachNav } from "./reach-nav";
import { sourceIds, tabOf, tabOfHash, type ReachTab, type SourceId } from "./search";
import { chipOf, InPage, meta } from "./source-ui";
import { CountryPicker, isOn, isProblem, SourceCards, useSourceLooks, type Looks } from "./sources";
import "./reach.css";

/**
 * Reachability: are the nodes reachable from one country (Russia by default), and since when, in the board's three tabs. The head names the
 * country (the owner picks it there); Nodes counts the nodes by their verdict now, lists them with each source's word and week, and brings
 * the last events and the sources; Events lists the week's alerts of the country; Sources is the list of the sources and the open one.
 * Everybody who sees Health sees this; the sources and the country are the owner's. The tab is `?tab=`; the old anchors of the one-page
 * version (`#sources`, `#reach-nodes`, `#reach-events`) lead to the same tabs.
 */
export function ReachabilityScreen({ scene }: { scene?: { filter?: State; open?: string } } = {}) {
  const t = useT();
  const { search, go } = useReachNav();
  const owner = useIsOwner();
  const hash = typeof window === "undefined" ? "" : window.location.hash;
  const hashTab = tabOfHash(hash);
  const tab: ReachTab = !search.tab && !search.source && hashTab ? hashTab : tabOf(search);
  // an old anchor leads to its tab and is taken off the address
  // (once per anchor: the address is rewritten, and a page that cannot rewrite it must not ask again and again)
  const source = search.source;
  const handled = useRef("");
  useEffect(() => {
    if (!hashTab || handled.current === hash) return;
    handled.current = hash;
    go(hashTab === "nodes" ? {} : hashTab === "sources" && source ? { source } : { tab: hashTab });
  }, [hash, hashTab, source, go]);

  const history = useQuery(historyQuery);
  const checks = useQuery(checksQuery());
  const nodesQ = useQuery(nodesQuery);
  const country = useQuery({ ...countryQuery, enabled: owner });
  const code = country.data?.country ?? history.data?.country ?? "";
  const h = history.data;
  // the IPv6 week only while Globalping checks IPv6: then every row carries CountryCheck.ipv6
  const v6On = (checks.data?.countryChecks ?? []).some((c) => c.ipv6);
  const h6 = useQuery({ ...history6Query, enabled: v6On });
  const { q: alerts, events } = useEvents();
  const { looks, reporters } = useSourceLooks();

  const nodes: NodeRow[] = (nodesQ.data?.nodes ?? [])
    .filter((n) => n.status !== NodeStatus.PENDING && n.status !== NodeStatus.RETIRED)
    .sort((a, b) => a.name.localeCompare(b.name))
    .map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, ipv6: n.ipv6 }));
  const byNode = new Map((checks.data?.countryChecks ?? []).map((c) => [c.nodeId, c]));
  // the rounds "Check now" asked of the country check, per node: the row's radar and the words under the verdict
  const rounds = new Map((checks.data?.countryNow ?? []).map((c) => [c.nodeId, c]));
  const lanes = new Map((h?.nodes ?? []).map((n) => [n.nodeId, new Map<string, Lane>(n.lanes.map((l) => [l.source, l]))]));
  const lanes6 = new Map((v6On ? (h6.data?.nodes ?? []) : []).flatMap((n) => n.lanes.filter((l) => l.source === "globalping").map((l) => [n.nodeId, l] as const)));
  const sources = h?.sources ?? [];

  const goTab = (next: string) => go(next === "nodes" ? {} : { tab: next as "events" | "sources" });
  const openSource = (id: SourceId) => go({ source: id });

  const loading = !h && !history.isError;
  const failed = !h && history.isError;
  const ready = !!h;
  // the board's words of an empty week: no sources, the first round still running, else "every node opened" (no nodes yet: the same last one)
  const none =
    alerts.data && sources.length === 0
      ? t("rp.ev.none.off")
      : checks.data && checks.data.countryChecks.length === 0 && sources.length > 0 && (nodes.length > 0 || nodesQ.isPending)
        ? t("rp.ev.none.first")
        : t("rc.ev.none", { country: countryIn(t, code) });
  const onSources = owner ? Object.values(looks).filter(isOn).length : sources.length;
  const problem = owner && Object.values(looks).some(isProblem);

  const retry = (q: { refetch: () => unknown }) => <LoadFailed title={t("rp.err.title")} text={t("rp.err.text")} retry={t("common.retry")} onRetry={() => void q.refetch()} />;

  const items: TabItem[] = [
    {
      value: "nodes",
      label: <TabLabel text={t("rc.nodes.title")} />,
      content: tab !== "nodes" ? null : (
        <div className="rc-tab">
          {failed ? (
            retry(history)
          ) : loading || nodesQ.isPending ? (
            <LoadCards label={t("common.loading")} />
          ) : (
            <NodesTab
              nodes={nodes}
              byNode={byNode}
              rounds={rounds}
              lanes={lanes}
              lanes6={lanes6}
              sources={sources}
              probeNode={checks.data?.tspuRun?.nodeId ?? ""}
              country={code}
              since={h!.sinceUnix}
              now={h!.nowUnix}
              owner={owner}
              looks={looks}
              first={sources.length > 0 && nodes.length > 0 && !!checks.data && checks.data.countryChecks.length === 0}
              events={events}
              noEvents={none}
              scene={scene}
              onOpenSource={openSource}
              onSources={() => goTab("sources")}
            />
          )}
        </div>
      ),
    },
    {
      value: "events",
      label: <TabLabel text={t("rc.nav.events")} n={ready ? events.length : 0} tone="warn" />,
      content: tab !== "events" ? null : (
        <div className="rc-tab">{alerts.isError && !alerts.data ? retry(alerts) : !alerts.data ? <LoadCards label={t("common.loading")} /> : <EventsTab code={code} events={events} noneText={none} />}</div>
      ),
    },
    {
      value: "sources",
      label: <TabLabel text={t("rc.sources")} n={ready ? onSources : 0} tone={problem ? "warn" : "mut"} />,
      // the owner's cards stay in the page while another tab is open: their words are the counts and notes of the other tabs
      keepMounted: owner,
      content: <div className="rc-tab">{owner ? <SourceCards looks={looks} reporters={reporters} /> : <SourcesRead sources={sources} loading={loading} />}</div>,
    },
  ];

  return (
    <InPage.Provider value>
      <div className="hlp rcp">
        <header className="sb-h rc-hd">
          <div>
            <h1>{t("nav.reachability")}</h1>
            <p>{code ? t("rc.sub", { country: countryIn(t, code) }) : " "}</p>
          </div>
          {owner && <CountryPicker />}
        </header>
        <Tabs aria-label={t("rp.tabs")} items={items} value={tab} onValueChange={goTab} />
      </div>
    </InPage.Provider>
  );
}

/** The Nodes tab: the notes, the summary, the table, then the last events and the sources side by side. */
function NodesTab(p: {
  nodes: NodeRow[];
  byNode: Map<string, CountryCheck>;
  rounds: Map<string, CountryNow>;
  lanes: Map<string, Map<string, Lane>>;
  lanes6: Map<string, Lane>;
  sources: string[];
  probeNode: string;
  country: string;
  since: number;
  now: number;
  owner: boolean;
  looks: Looks;
  first: boolean;
  events: ReturnType<typeof useEvents>["events"];
  noEvents: string;
  /** a scene of the dev kit: the chip pressed and the row opened from the start */
  scene?: { filter?: State; open?: string };
  onOpenSource: (id: SourceId) => void;
  onSources: () => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const [filter, setFilter] = useState<State | "">(p.scene?.filter ?? "");
  if (p.nodes.length === 0)
    return (
      <Calm
        className="hl-calm rc-emp"
        icon={P.server}
        title={t("rp.nonodes.title")}
        text={t("rp.nonodes.text", { country: countryIn(t, p.country) })}
        action={
          <Link to="/nodes" className={buttonClass("secondary", "lg")}>
            {t("rp.nonodes.go")}
          </Link>
        }
      />
    );
  if (p.sources.length === 0)
    return (
      <Calm
        className="hl-calm rc-emp"
        icon={P.list}
        title={t("rp.nosrc.title")}
        text={p.owner ? t("rp.nosrc.text", { country: countryIn(t, p.country) }) : t("rc.off.other")}
        action={
          p.owner && (
            <Button variant="primary" size="lg" onClick={p.onSources}>
              {t("rp.nosrc.go")}
            </Button>
          )
        }
      />
    );

  const tally = tallyOf(
    p.nodes.map((n) => n.id),
    (id) => p.byNode.get(id)?.verdict,
  );
  const count = (s: State) => Object.entries(tally).reduce((a, [k, v]) => a + (stateOf[k as keyof typeof tally] === s ? v : 0), 0);
  const total = p.nodes.length;
  const ok = count("ok");
  const present = stateKinds.filter((s) => count(s) > 0);
  const zero = stateKinds.filter((s) => s !== "ok" && s !== "nd" && count(s) === 0);
  const sub = p.first ? t("rp.sum.first") : ok === total ? t("rp.sum.all", { country: countryIn(t, p.country) }) : t.n("rp.sum.some", total, { country: countryIn(t, p.country) });
  const warns = Object.entries(p.looks).filter(([, l]) => isProblem(l)) as [SourceId, NonNullable<Looks[SourceId]>][];
  const ipv6n = p.nodes.filter((n) => ipv6Helps(p.byNode.get(n.id))).length;
  const dark = darkEverywhere(p.nodes.map((n) => p.byNode.get(n.id)).filter((c): c is CountryCheck => !!c));
  return (
    <>
      {p.first && (
        <p className="rc-nt" data-testid="first-round">
          <Dot kind="busy" />
          <span>{t("rp.first")}</span>
        </p>
      )}
      {warns.map(([id, look]) => (
        <p key={id} className={cx("rc-nt", look.kind === "bad" ? "bad" : "part")} data-testid="source-warn">
          <Dot kind={look.kind === "bad" ? "bad" : "part"} />
          <span>
            <b>{t(meta[id].title)}</b> · {t("rp.warn", { why: chipOf(t, look).note || look.word })}
          </span>
          <button type="button" className="an-lk" onClick={() => p.onOpenSource(id)}>
            {t("rp.warn.open")}
          </button>
        </p>
      ))}
      <section className="card rc-sum">
        <div className="rc-sumh">
          <b>{p.first ? "—" : t("rp.sum.of", { n: ok, total })}</b>
          <span>{sub}</span>
        </div>
        <div className="rc-sumf" role="group" aria-label={t("rp.state")}>
          {present.map((s) => (
            <button key={s} type="button" className={s} aria-pressed={filter === s} data-kind={s} onClick={() => setFilter(filter === s ? "" : s)}>
              <i aria-hidden="true" />
              {t(stateWord[s])}
              <span className="mono">{count(s)}</span>
            </button>
          ))}
        </div>
        <div className="rc-bar" aria-hidden="true">
          {present.map((s) => (
            <i key={s} className={s} style={{ flexGrow: count(s) } as CSSProperties} />
          ))}
        </div>
        {!p.first && zero.length > 0 && <p className="rc-zero">{t("rp.zero", { list: listOf(fmt.locale, zero.map((s) => t(stateWord[s]))) })}</p>}
      </section>
      <Ipv6Helps n={ipv6n} />
      <DarkEverywhereLines rows={dark} />
      <NodesTable
        nodes={p.nodes}
        checks={p.byNode}
        rounds={p.rounds}
        lanes={p.lanes}
        lanes6={p.lanes6}
        sources={p.sources}
        probeNode={p.probeNode}
        country={p.country}
        since={p.since}
        now={p.now}
        owner={p.owner}
        filter={filter}
        onShowAll={() => setFilter("")}
        alerts={p.events}
        open={p.scene?.open}
        first={p.first}
        stale={new Set(warns.map(([id]) => id))}
      />
      <div className="rc-two">
        <EventsCard code={p.country} events={p.events} noneText={p.noEvents} />
        <SourcesCard sources={p.sources} looks={p.looks} owner={p.owner} onOpen={p.onOpenSource} />
      </div>
    </>
  );
}

/** The nodes cut over IPv4 that IPv6 still reaches, under the summary: one quiet line, nothing when there are none. */
function Ipv6Helps({ n }: { n: number }) {
  const t = useT();
  if (n === 0) return null;
  return (
    <p className="rc-nt" data-testid="ipv6-helps">
      <Dot kind="ok" />
      <span>
        <b className="mono">IPv6</b> · {t("rc.sum.v6", { n })}
      </span>
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
    <section aria-label={t("rc.darkAll.title")} className="rc-nt part rc-dark">
      <Dot kind="part" />
      <span>
        {rows.map((r) => (
          <span key={`${r.source}|${r.name}`} data-testid="dark-everywhere" className="rc-dl">
            <b>{t("rc.darkAll", { network: r.name })}</b> · {sourceName(t, r.source)}
          </span>
        ))}
        <em>{t("rc.darkAll.hint")}</em>
      </span>
    </section>
  );
}

/** The card "Sources" of the Nodes tab: each source with its state and when it last looked, a click leads to it. */
function SourcesCard({ sources, looks, owner, onOpen }: { sources: readonly string[]; looks: Looks; owner: boolean; onOpen: (id: SourceId) => void }) {
  const t = useT();
  return (
    <section className="card pp-sec sb-sec">
      <div className="pp-sh">
        <span className="pp-ic mint" aria-hidden="true">
          <Gl d={P.list} />
        </span>
        <h3>{t("rc.sources")}</h3>
      </div>
      <div className="rc-sl">
        {sourceIds.filter((id) => owner || id !== "klick").map((id) => {
          const look = looks[id];
          const chip = look && chipOf(t, look);
          // without the cards (anyone but the owner) the page only knows which sources deliver data
          const on = look ? isOn(look) : sources.includes(id);
          return (
            <button key={id} type="button" onClick={() => onOpen(id)} data-source={id}>
              <Dot kind={look ? (chip!.kind === "ok" ? "ok" : chip!.kind === "off" ? "none" : chip!.kind === "bad" ? "bad" : chip!.kind === "warn" ? "part" : "busy") : on ? "ok" : "none"} />
              <b>{t(meta[id].title)}</b>
              <em>{look ? chip!.note || chip!.word : on ? t("cr.state.on") : t("rc.st.off")}</em>
            </button>
          );
        })}
      </div>
      {!owner && <p className="sb-lead rc-ownr">{t("rc.sources.owner")}</p>}
    </section>
  );
}

/** The Sources tab for anyone but the owner: which sources deliver data, and who changes them. */
function SourcesRead({ sources, loading }: { sources: readonly string[]; loading: boolean }): ReactNode {
  const t = useT();
  if (loading) return <LoadCards label={t("common.loading")} />;
  return (
    <section className="card pp-sec sb-sec">
      <div className="pp-sh">
        <span className="pp-ic mint" aria-hidden="true">
          <Gl d={P.list} />
        </span>
        <h3>{t("rc.sources")}</h3>
      </div>
      <p className="sb-lead">{t("rc.sources.owner")}</p>
      <div className="rc-sl">
        {sourceIds.filter((id) => id !== "klick").map((id) => {
          const on = sources.includes(id);
          return (
            <div key={id} data-source={id}>
              <Dot kind={on ? "ok" : "none"} />
              <b>{t(meta[id].title)}</b>
              <em>{on ? t("cr.state.on") : t("rc.st.off")}</em>
            </div>
          );
        })}
      </div>
    </section>
  );
}
