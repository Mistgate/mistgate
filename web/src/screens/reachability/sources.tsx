import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { QueryError } from "@/components/ui/query-error";
import { Flag } from "@/components/ui/flag";
import { Select } from "@/components/ui/select";
import { walkTabs } from "@/components/ui/master-detail";
import { useT } from "@/i18n";
import { blocklistQuery } from "@/lib/blocklist";
import { bsbordQuery } from "@/lib/bsbord";
import { cheburcheckQuery } from "@/lib/cheburcheck";
import { clientProbeQuery } from "@/lib/clientprobe";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { countryOptions, countryQuery, globalpingQuery, useCountryActions } from "@/lib/globalping";
import { ownProbeQuery } from "@/lib/ownprobe";
import { tspuQuery } from "@/lib/tspu";
import { BlocklistCard, BsbordCard, CheburcheckCard, GlobalpingCard, KlickCard, ProbesCard, TspuCard } from "./source-cards";
import { chipOf, meta, ReportLook, SrcPill, type Look } from "./source-ui";
import { useReachNav } from "./reach-nav";
import { sourceIds, type SourceId } from "./search";

export { BlocklistCard, BsbordCard, CheburcheckCard, GlobalpingCard, KlickCard, ProbesCard, TspuCard };

const cards: Record<SourceId, () => ReactNode> = {
  globalping: GlobalpingCard,
  lists: BlocklistCard,
  tspu: TspuCard,
  cheburcheck: CheburcheckCard,
  bsbord: BsbordCard,
  klick: KlickCard,
  probes: ProbesCard,
};

export type Looks = Partial<Record<SourceId, Look>>;

/**
 * What each source says of itself, kept by the page: the cards say it as they draw (source-ui `useLook`), and the list, the notes over
 * the nodes and the counts of the tabs read it, whichever tab is open. Every card stays mounted, so the words are always there.
 */
export function useSourceLooks() {
  const [looks, setLooks] = useState<Looks>({});
  const reporters = useMemo(
    () =>
      Object.fromEntries(
        sourceIds.map((id) => [
          id,
          (look: Look) => setLooks((all) => (all[id] && all[id].state === look.state && all[id].kind === look.kind && all[id].word === look.word && all[id].detail === look.detail ? all : { ...all, [id]: look })),
        ]),
      ) as Record<SourceId, (look: Look) => void>,
    [],
  );
  return { looks, reporters };
}

/** The sources that are on say so in their look: off and "not available" are the quiet ones. */
export const isOn = (look: Look | undefined) => !!look && look.kind !== "off";
/** A source whose state is a problem: it errors, or it is on and says what is wrong. */
export const isProblem = (look: Look | undefined) => !!look && (look.kind === "bad" || look.kind === "warn");

/**
 * The sources of the Reachability page as a list and the open one: Globalping, the block lists, the TSPU probe, cheburcheck, bsbord,
 * the reports of kl!ck users, the own probes. The list is on the left (a row of tiles above the card on a narrow window) and the open
 * source on the right. The open source is `?source=` in the URL; without it, the first one that is on, else Globalping. Every source
 * stays mounted, only the open one is shown: a key half typed, a test under way and its answer survive a look at another. Owner only,
 * like the services behind them; the country they all look from is the picker in the page's head (the kl!ck reports come from wherever
 * the people are). `looks` and `reporters` come from the page when it keeps the words of the sources for its other tabs.
 */
export function SourceCards({ looks: given, reporters: givenReporters }: { looks?: Looks; reporters?: Record<SourceId, (look: Look) => void> } = {}) {
  const t = useT();
  const { search, go } = useReachNav();
  const chosen = search.source;
  const edge = useEdition().off("own_probes");
  const own = useSourceLooks();
  const looks = given ?? own.looks;
  const reporters = givenReporters ?? own.reporters;

  // the first source that is on, once every one has answered; then it stays, so switching it off does not move the page
  const on = [
    useQuery({ ...globalpingQuery, select: (s) => s.enabled }),
    useQuery({ ...blocklistQuery, select: (s) => s.enabled }),
    useQuery({ ...tspuQuery, select: (s) => s.enabled }),
    useQuery({ ...cheburcheckQuery, select: (s) => s.enabled }),
    useQuery({ ...bsbordQuery, select: (s) => s.enabled }),
    useQuery({ ...clientProbeQuery, select: (p) => !!p.settings?.enabled }),
    // the Cloudflare edition has no own probes: nothing is asked there, and a query that is not asked does not hold the page back
    useQuery({ ...ownProbeQuery, enabled: !edge, select: (p) => !!p.settings?.enabled }),
  ];
  const [first, setFirst] = useState<SourceId | null>(null);
  if (first === null && on.every((q) => !q.isPending || q.fetchStatus === "idle")) setFirst(sourceIds.find((_, i) => on[i]!.data) ?? "globalping");
  // the source last opened stays open while another tab is looked at (the cards stay mounted, the address does not keep it)
  const [last, setLast] = useState<SourceId | null>(null);
  if (chosen && chosen !== last) setLast(chosen);
  const open = chosen ?? last ?? first ?? "globalping";

  const pick = (id: SourceId) => go({ source: id });

  return (
    <div className="rc-sg">
      <div role="tablist" aria-label={t("rc.sources")} aria-orientation="vertical" onKeyDown={walkTabs} className="rc-sn">
        <p className="pp-q1">{t("rc.sources.sub")}</p>
        {sourceIds.map((id) => (
          <SourceRow key={id} id={id} look={looks[id]} selected={id === open} tabbable={id === open} onPick={() => pick(id)} />
        ))}
      </div>
      <div className="rc-sp">
        {sourceIds.map((id) => {
          const Card = cards[id];
          return (
            <div key={id} role="tabpanel" id={`src-panel-${id}`} aria-labelledby={`src-tab-${id}`} hidden={id !== open}>
              <ReportLook.Provider value={reporters[id]}>
                <Card />
              </ReportLook.Provider>
            </div>
          );
        })}
      </div>
    </div>
  );
}

/**
 * A row of the list (the board's `.rc-sn button`): the name and the state's pill, what the source is, where it looks from and the state's few
 * words. On a narrow window the rows are tiles side by side (the list scrolls), as on the board.
 */
function SourceRow({ id, look, selected, tabbable, onPick }: { id: SourceId; look?: Look; selected: boolean; tabbable: boolean; onPick: () => void }) {
  const t = useT();
  const m = meta[id];
  const chip = look && chipOf(t, look);
  return (
    <button type="button" role="tab" id={`src-tab-${id}`} aria-selected={selected} aria-controls={`src-panel-${id}`} tabIndex={tabbable ? 0 : -1} onClick={onPick}>
      <span className="rc-st">
        <b>{t(m.title)}</b>
        {/* the pill's place is kept while the source loads: no row grows when it arrives */}
        {look ? <SrcPill look={look} /> : <span className="rc-ph" aria-hidden="true" />}
      </span>
      <span className="rc-sm">{t(m.what)}</span>
      <span className="rc-sm">
        <i>{t(m.ruOnly ? "src.cover.ru" : "src.cover.any")}</i>
        <span title={chip?.note || undefined}>{chip?.note}</span>
      </span>
    </button>
  );
}

/** The shared country: it only says where to look from and sends nothing, so it saves on the spot without a step-up. */
export function CountryPicker() {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(countryQuery);
  const save = useCountryActions();
  if (q.isError && !q.data) return <QueryError compact error={q.error} onRetry={() => void q.refetch()} />;
  if (!q.data) return null;
  const current = q.data.country;
  const codes: readonly string[] = (countryOptions as readonly string[]).includes(current) ? countryOptions : [current, ...countryOptions];
  return (
    <div className="rc-cty" role="group" aria-label={t("cr.country")}>
      <span aria-hidden="true">{t("cr.country")}</span>
      <div className="rc-ctys">
        <Select
          compact
          aria-label={t("cr.country")}
          value={save.isPending && save.variables ? save.variables : current}
          onValueChange={(v) => v !== current && save.mutate(v)}
          options={codes.map((c) => ({ value: c, label: fmt.country(c), icon: <Flag code={c} size={12} className="fl" /> }))}
        />
      </div>
    </div>
  );
}
