import { Menu } from "@base-ui/react/menu";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useMemo, useState, type ReactNode } from "react";
import { useAddNode } from "@/components/add-node";
import { useEscClear } from "@/components/bulk";
import { useToast } from "@/components/ui/toast";
import { Button, buttonClass } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { PageTitle } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Popover } from "@/components/ui/popover";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select, selectItemClass, selectPopupClass } from "@/components/ui/select";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { useBatch } from "@/lib/batch";
import { cx } from "@/lib/cx";
import { maxBulkNodes } from "@/lib/decoy";
import { useEdition } from "@/lib/edition";
import { fleetWays, relayPairRefusal, type FleetNodeIn } from "@/lib/fleet-map";
import { countryQuery } from "@/lib/globalping";
import { checksQuery, countryColumn, countryIn } from "@/lib/health";
import { useFmt } from "@/lib/format";
import { useIsPhone } from "@/lib/media";
import { isProblem, nodeKind } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { protocolName, sortProtocols } from "@/lib/series";
import { meQuery } from "@/lib/session";
import { readPref, writePref } from "@/lib/storage";
import { useNow } from "@/lib/time";
import { useOlderNodes } from "@/lib/updates";
import { useOpenWizard } from "./node/relay-open";
import { passwordOnly, useBulkRun } from "./nodes-bulk-actions";
import { NodesBulk } from "./nodes-bulk";
import { NodesFirst } from "./nodes-first";
import { FleetLoad } from "./nodes-load";
import { FleetMap } from "./nodes-map";
import { firstSort, NodeList, onlineOf, sortNodes, type Select as Ticking, type Sort, type SortKey } from "./nodes-list";
import { PanelKeyOffer } from "./nodes-panel-key";
import { WaysView, type WaysData, type WaysDemo } from "./nodes-ways";
import "./nodes.css";

type Node = Plain<NodeMsg>;
type StatusFilter = "all" | "problems" | "working" | "noway";
type NodesView = "list" | "ways" | "map" | "load";
const all = "all";

/** The 36px buttons of the bar: a secondary button, the accent's wash while it is pressed or holds a choice. */
const barButton =
  "inline-flex h-9 flex-none items-center justify-center gap-2 rounded-field border border-line bg-step px-4 text-sm font-medium whitespace-nowrap text-fg transition-[background-color,border-color,transform] duration-150 ease-out hover:bg-(--step-hover) active:scale-[0.98] aria-pressed:border-accent-line aria-pressed:bg-accent-soft data-popup-open:border-accent-line";

/** What the dev kit starts the screen with (a filter, a search, ticked nodes, a selected node on the map); the app starts with none. */
export type NodesDemo = { status?: StatusFilter; find?: string; selecting?: boolean; ticked?: readonly string[]; focus?: string; /** The view it opens on (the app opens on the browser's last choice). */ view?: NodesView; ways?: WaysDemo };

export function NodesScreen({ demo }: { demo?: NodesDemo } = {}) {
  const t = useT();
  const fmt = useFmt();
  const addNode = useAddNode();
  const q = useQuery(nodesQuery);
  const [status, setStatus] = useState<StatusFilter>(demo?.status ?? "all");
  const [protocol, setProtocol] = useState(all);
  const [country, setCountry] = useState(all);
  const [find, setFind] = useState(demo?.find ?? "");
  const [sort, setSort] = useState<Sort>(firstSort);
  // ticked nodes and the bulk bar (owner only: SSH, rollouts and the decoy site are owner calls)
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const [selecting, setSelecting] = useState(!!demo?.selecting);
  const [ticked, setTicked] = useState<ReadonlySet<string>>(new Set(demo?.ticked));
  const batch = useBatch();
  const run = useBulkRun(batch, owner);
  const older = useOlderNodes();
  const clear = useCallback(() => setTicked(new Set()), []);
  // List, the fleet map or the load: the URL's (?view=map, ?view=load), else the one this browser used last. A phone has no
  // map; the Cloudflare edition has no load (OpsService is the VPS edition's).
  const navigate = useNavigate();
  const phone = useIsPhone();
  const { edge, off } = useEdition();
  const search = useSearch({ strict: false }) as { view?: NodesView; focus?: string };
  // the ways are the owner's (RelayService is); a helper has the list, the map and the load
  const wanted = demo?.view ?? search.view ?? readPref("nodes.view");
  const view: NodesView = wanted === "ways" && owner ? "ways" : wanted === "map" && !phone ? "map" : wanted === "load" && !edge ? "load" : "list";
  const setView = (v: NodesView) => {
    writePref("nodes.view", v);
    void navigate({ to: "/nodes", search: v === "list" ? {} : { view: v }, replace: true });
  };

  const nodes = useMemo(() => q.data?.nodes ?? [], [q.data]);

  // The ways out of the country: the relays (the owner's), what the country sees of each node, and the one verdict they make
  const toast = useToast();
  const relaysQ = useQuery({ ...relaysQuery(""), enabled: owner });
  // no round to read where the checks are off (a Cloudflare panel); whatever the cache still holds is not one
  const checksOn = !edge && !off("health_checks");
  const checksQ = useQuery({ ...checksQuery(""), enabled: checksOn });
  const asked = useQuery({ ...countryQuery, enabled: owner }).data?.country;
  const checksData = checksQ.data;
  const column = useMemo(() => (checksOn ? countryColumn(checksData?.countryChecks) : null), [checksOn, checksData]);
  const audience = column?.code || asked || "RU";
  const nowS = Math.floor(useNow() / 1000);
  const nodesIn: FleetNodeIn[] = useMemo(
    () => nodes.map((n) => ({ id: n.id, name: n.name, countryCode: n.countryCode, location: n.location, status: n.status, protocols: n.protocols, online: onlineOf(n), cpu: n.hasMetrics ? n.cpuPct : null, warp: n.warp?.state ?? 0 })),
    [nodes],
  );
  const relays = relaysQ.data;
  const ways = useMemo(
    () => fleetWays({ nodes: nodesIn, relays: owner ? (relays ?? []) : undefined, checks: column?.byNode ?? new Map(), country: audience, nowS }),
    [nodesIn, owner, relays, column, audience, nowS],
  );
  const checking = checksQ.isPending && checksQ.fetchStatus !== "idle";
  const waysData: WaysData = owner && relaysQ.isError && relays === undefined ? "error" : (owner && relays === undefined) || checking ? "load" : "ready";
  // "Get around…" and "＋ Pass through …": the wizard of the exit's page ("Make reachable from…")
  const openWizard = useOpenWizard();
  const byId = (id: string) => nodes.find((n) => n.id === id);
  /** The entry an exit would best go through, other than `except`: one that is up and seen, in another country than the exit, that the panel accepts for it. */
  const entryFor = (exitId?: string, except?: string): string | undefined => {
    const ok = ways.entries.filter((e) => e.nodeId !== except && !e.entry!.offline && !e.entry!.hidden && (!exitId || (!relayPairRefusal(e.nodeId, exitId, nodesIn) && byId(e.nodeId)?.countryCode !== byId(exitId)?.countryCode)));
    return (ok.find((e) => e.direct.tone === "ok") ?? ok[0])?.nodeId;
  };
  // the entry is the owner's pick or, with none, left to the wizard's first step; with no exit it is the one that needs a way most
  const openWay = (exitId?: string, entryId?: string) => {
    const exit = exitId ?? (ways.exits.find((x) => x.tone === "bad") ?? ways.exits.find((x) => x.tone !== "ok"))?.nodeId;
    if (!exit || !(entryId ?? entryFor(exit))) return void toast.error(t("nw.noEntry"));
    openWizard(exit, entryId);
  };
  // the exits with no way in: closed, and nothing carries them
  const noWayIds = useMemo(() => new Set(ways.exits.filter((x) => x.tone === "bad").map((x) => x.nodeId)), [ways]);
  const noWayN = owner && waysData === "ready" ? noWayIds.size : 0;
  const waysList = { model: ways, state: waysData, country: audience, helper: !owner, onFix: (id: string) => openWay(id) };
  const shownStatus = status === "noway" && noWayN === 0 ? "all" : status;

  const problems = nodes.filter(isProblem).length;
  const working = nodes.filter((n) => nodeKind(n) === "ok").length;
  const onlineTotal = nodes.reduce((a, n) => a + onlineOf(n), 0);

  const protocols = useMemo(() => sortProtocols(nodes.flatMap((n) => n.protocols)), [nodes]);
  const countries = useMemo(
    () => [...new Set(nodes.map((n) => n.countryCode).filter(Boolean))].sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.locale)),
    [nodes, fmt],
  );

  const needle = find.trim().toLowerCase();
  const rows = useMemo(
    () =>
      sortNodes(
        nodes.filter(
          (n) =>
            (shownStatus === "all" || (shownStatus === "noway" ? noWayIds.has(n.id) : shownStatus === "problems" ? isProblem(n) : nodeKind(n) === "ok")) &&
            (protocol === all || n.protocols.includes(protocol)) &&
            (country === all || n.countryCode === country) &&
            (!needle || [n.name, n.location, fmt.country(n.countryCode), n.address].some((s) => s.toLowerCase().includes(needle))),
        ),
        sort,
      ),
    [nodes, shownStatus, noWayIds, protocol, country, needle, sort, fmt],
  );
  const narrowed = shownStatus !== all || protocol !== all || country !== all || needle !== "";
  const reset = () => {
    setStatus("all");
    setProtocol(all);
    setCountry(all);
    setFind("");
  };
  const onSort = (key: SortKey) => setSort((s) => (s.key === key ? { key, dir: s.dir > 0 ? -1 : 1 } : { key, dir: key === "name" ? 1 : -1 }));

  const pickable = (n: Node) => n.status !== NodeStatus.RETIRED;
  const picked = nodes.filter((n) => ticked.has(n.id));
  const toggle = (id: string) => setTicked((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : s.size < maxBulkNodes ? new Set([...s, id]) : s));
  const shownIds = rows.filter(pickable).map((n) => n.id);
  const allShown = shownIds.length > 0 && shownIds.every((id) => ticked.has(id));
  const toggleAll = () => setTicked(allShown ? new Set([...ticked].filter((id) => !shownIds.includes(id))) : new Set([...ticked, ...shownIds].slice(0, maxBulkNodes)));
  const select: Ticking | undefined = owner ? { on: selecting, ticked, toggle, full: ticked.size >= maxBulkNodes, pickable } : undefined;
  // every quick pick replaces the selection; "Password only" is the offer's own rule
  const live = nodes.filter(pickable);
  const picks = [
    { label: "bulk.pick.shown", ids: shownIds },
    { label: "bulk.pick.problems", ids: live.filter(isProblem).map((n) => n.id) },
    { label: "bulk.pick.password", ids: live.filter((n) => passwordOnly(run.ctx.access?.get(n.id))).map((n) => n.id) },
    { label: "bulk.pick.outdated", ids: live.filter((n) => older.has(n.id)).map((n) => n.id) },
  ] as const;
  const pick = (ids: readonly string[]) => setTicked(new Set(ids.slice(0, maxBulkNodes)));
  const finishSelecting = () => {
    setSelecting(false);
    clear();
  };
  useEscClear(selecting && picked.length === 0, finishSelecting);

  if (!q.data) {
    return (
      <div className="flex flex-col gap-4">
        <PageTitle>{t("nav.nodes")}</PageTitle>
        {q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />}
      </div>
    );
  }

  const count = (label: string, n: number) => (
    <>
      {label}
      <span className="font-mono text-[11px] font-normal text-muted">{n}</span>
    </>
  );
  const filtersOn = (protocol !== all ? 1 : 0) + (country !== all ? 1 : 0);
  // List, Ways (the owner's), the map (not on a phone), the load (not on the Cloudflare edition)
  const viewOptions = [
    { value: "list" as const, label: t("wg.view.list") },
    ...(owner ? [{ value: "ways" as const, label: t("nw.view") }] : []),
    ...(phone ? [] : [{ value: "map" as const, label: t("fm.view.map") }]),
    ...(edge ? [] : [{ value: "load" as const, label: t("load.view") }]),
  ];

  return (
    <div className="flex flex-col gap-4">
      <div className="mb-1 flex items-center gap-3 max-md:flex-wrap">
        <PageTitle>{t("nav.nodes")}</PageTitle>
        <span className="me-auto inline-flex items-center gap-2 text-[13px] text-muted max-md:flex-none max-md:text-[0px]">
          <i aria-hidden className="fz-live-dot size-2 flex-none rounded-full bg-accent-mark" />
          {nodes.length > 0 ? `${t.n("nodes.summary", nodes.length, { o: onlineTotal })}${problems ? ` · ${t.n("nodes.withProblems", problems)}` : ""}${noWayN > 0 ? ` · ${t("nw.sum.noway", { n: noWayN, country: countryIn(t, audience) })}` : ""}${owner && waysData === "ready" && ways.downN > 0 ? ` · ${t.n("nw.sum.down", ways.downN)}` : ""}` : t("nv.none")}
        </span>
        <Link to="/decoys" className={cx(buttonClass("secondary", "lg"), "no-underline max-md:flex-none")}>
          {t("nav.decoys")}
        </Link>
        <Button variant="primary" size="lg" className="max-md:flex-none" onClick={() => addNode()}>
          <Icon name="plus" size={16} />
          {t("nv.addShort")}
        </Button>
      </div>

      {nodes.length === 0 ? (
        <NodesFirst onAdd={() => addNode()} />
      ) : (
        <>
          {owner && (
            <PanelKeyOffer
              busy={batch.running}
              onStart={(ids) => {
                const set = new Set(ids.slice(0, maxBulkNodes));
                setSelecting(true);
                setTicked(set);
                void run.run("panelKey", nodes.filter((n) => set.has(n.id)));
              }}
            />
          )}
          <div className="fz-bar flex flex-wrap items-center gap-3">
            {viewOptions.length > 1 && <Segmented variant="flat" className="fz-seg" aria-label={t("fm.view")} value={view} onValueChange={setView} options={viewOptions} />}
            {view === "list" && (
              <>
                <FilterChips
                  className="fz-ch flex-nowrap!"
                  aria-label={t("nodes.filterStatus")}
                  value={shownStatus}
                  onValueChange={setStatus}
                  options={[
                    { value: "all", label: count(t("nodes.all"), nodes.length) },
                    { value: "problems", label: count(t("nv.f.problems"), problems) },
                    { value: "working", label: count(t("nodes.working"), working) },
                    ...(noWayN > 0 ? [{ value: "noway" as const, label: count(t("nw.f.noway"), noWayN) }] : []),
                  ]}
                />
                <label
                  data-has={find ? "" : undefined}
                  className="fz-q flex h-9 items-center gap-2 rounded-field border border-line bg-surface px-3 text-muted focus-within:border-accent-line"
                >
                  <Icon name="search" size={16} />
                  <input
                    value={find}
                    onChange={(e) => setFind(e.target.value)}
                    placeholder={t("nv.search")}
                    aria-label={t("nv.searchLabel")}
                    autoComplete="off"
                    className="min-w-0 flex-1 border-0 bg-transparent text-fg outline-none placeholder:text-muted"
                  />
                </label>
                <div className="fz-sbw flex flex-none items-center gap-3">
                  <Popover
                    align="end"
                    aria-label={t("nv.filters")}
                    trigger={
                      <button type="button" aria-pressed={filtersOn > 0} className={barButton}>
                        <Icon name="filter" size={16} className="block flex-none text-sec" />
                        {t("nv.filters")}
                        {filtersOn > 0 && <span className="font-mono text-[11px] font-normal text-muted">{filtersOn}</span>}
                      </button>
                    }
                  >
                    <div className="flex flex-col gap-3 p-1.5">
                      <Field label={t("nodes.filterProtocol")}>
                        <Select
                          aria-label={t("nodes.filterProtocol")}
                          value={protocol}
                          onValueChange={setProtocol}
                          options={[{ value: all, label: t("nodes.allProtocols") }, ...protocols.map((p) => ({ value: p, label: protocolName(p) }))]}
                        />
                      </Field>
                      <Field label={t("nodes.filterCountry")}>
                        <Select
                          aria-label={t("nodes.filterCountry")}
                          value={country}
                          onValueChange={setCountry}
                          options={[{ value: all, label: t("nodes.allCountries") }, ...countries.map((c) => ({ value: c, label: fmt.country(c) }))]}
                        />
                      </Field>
                    </div>
                  </Popover>
                  {owner && selecting && <PickButton picks={picks.map((p) => ({ label: t(p.label), ids: p.ids }))} any={ticked.size > 0} onPick={pick} />}
                  {owner && (
                    <button type="button" aria-pressed={selecting} onClick={() => (selecting ? finishSelecting() : setSelecting(true))} className={barButton}>
                      {selecting ? t("nv.done") : t("nv.select")}
                    </button>
                  )}
                </div>
              </>
            )}
          </div>
          {view === "map" ? (
            <FleetMap nodes={nodes} owner={owner} focus={search.focus ?? demo?.focus} />
          ) : view === "load" ? (
            <FleetLoad />
          ) : view === "ways" ? (
            <WaysView
              nodes={nodes}
              nodesIn={nodesIn}
              model={ways}
              data={waysData}
              edge={edge}
              country={audience}
              nowS={nowS}
              openWay={openWay}
              entryFor={entryFor}
              onRetry={() => void relaysQ.refetch()}
              onAddNode={() => addNode()}
              demo={demo?.ways}
            />
          ) : (
            <>
              <NodeList rows={rows} fmt={fmt} select={select} sort={sort} onSort={onSort} filtered={narrowed} onReset={reset} onAdd={() => addNode()} ways={waysList} />
              {owner && <NodesBulk nodes={nodes} picked={picked} shown={new Set(rows.map((n) => n.id))} batch={batch} run={run} onClear={clear} onAll={allShown ? clear : toggleAll} allShown={allShown} total={shownIds.length} />}
            </>
          )}
        </>
      )}
    </div>
  );
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5 text-[13px] text-sec">
      {label}
      {children}
    </div>
  );
}

/** "Select ▾" by the filters: every pick replaces the selection, and the last one clears it. */
function PickButton({ picks, any, onPick }: { picks: readonly { label: string; ids: readonly string[] }[]; any: boolean; onPick: (ids: readonly string[]) => void }) {
  const t = useT();
  return (
    <Menu.Root>
      <Menu.Trigger className={cx(barButton, "gap-1.5")}>
        {t("bulk.pick")}
        <Icon name="chevronDown" size={12} className="block flex-none text-muted" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup className={cx(selectPopupClass, "min-w-52")}>
            {picks.map((p) => (
              <Menu.Item key={p.label} disabled={p.ids.length === 0} onClick={() => onPick(p.ids)} className={cx(selectItemClass, "justify-between gap-4 data-disabled:cursor-default data-disabled:text-muted")}>
                {p.label}
                <span className="font-mono text-xs font-medium text-muted tabular-nums">{p.ids.length}</span>
              </Menu.Item>
            ))}
            <Menu.Separator className="mx-2.5 my-1 h-px bg-line" />
            <Menu.Item disabled={!any} onClick={() => onPick([])} className={cx(selectItemClass, "data-disabled:cursor-default data-disabled:text-muted")}>
              {t("bulk.clear")}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}
