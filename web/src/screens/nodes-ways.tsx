import { useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type CSSProperties, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Flag } from "@/components/ui/flag";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { filterExits, relayPairRefusal, sortExits, type FleetNodeIn, type FleetWays, type NodeWays, type WayFilter, type WayTone } from "@/lib/fleet-map";
import { useFmt } from "@/lib/format";
import { countryIn } from "@/lib/health";
import { nodeLoad } from "@/lib/node-load";
import type { Plain } from "@/lib/plain";
import { nodeQuery } from "@/lib/queries";
import { entryOffered, entryOf, entryState, ipv6FactOf } from "@/lib/ways";
import { PairSheet, type PairStart } from "./nodes-ways-pair";
import { WayGlyph } from "./nodes-ways-parts";
import { pairCell, verdictWord, waysSummary } from "./nodes-ways-text";
import "./nodes-ways.css";

type Node = Plain<NodeMsg>;

// The Ways view of the Nodes screen, as the board draws it (PnNodes, the rw- rules): an exit per row, an entry per column, what
// each pair does, a sheet for a pair. The verdicts are lib/fleet-map.ts's (fleetWays); the owner's view, a helper has none.

/** What the view stands on: the relays and the checks have come, the relays did not load, or they have not yet. */
export type WaysData = "ready" | "load" | "error";

export type WaysViewProps = {
  nodes: readonly Node[];
  nodesIn: readonly FleetNodeIn[];
  model: FleetWays;
  data: WaysData;
  /** The Cloudflare edition: no country measurements, so no "Direct" column. */
  edge: boolean;
  /** The audience country, as its code. */
  country: string;
  nowS: number;
  /** The one function that starts a way: this exit (or any), through this entry (or the best one). The wizard takes it over. */
  openWay: (exitId?: string, entryId?: string) => void;
  /** The entry an exit would best go through, other than `except`; undefined when none can take it. */
  entryFor: (exitId: string, except?: string) => string | undefined;
  onRetry: () => void;
  onAddNode: () => void;
  /** The dev kit starts the view with a filter, an open pair and what its sheet starts with; the app starts with none. */
  demo?: WaysDemo;
};

export type WaysDemo = { filter?: WayFilter; open?: string; sheet?: PairStart };

const tile = "grid size-11 place-items-center rounded-[14px] bg-step text-sec";
const noteBox = "m-0 my-3 flex items-center gap-2 rounded-[12px] border border-line px-4 py-3 text-[13px] text-sec";

export function WaysView(p: WaysViewProps) {
  const t = useT();
  const fmt = useFmt();
  const { model, data } = p;
  const country = countryIn(t, p.country);
  const [filter, setFilter] = useState<WayFilter>(p.demo?.filter ?? "all");
  const [open, setOpen] = useState(p.demo?.open ?? "");
  const entries = model.entries;
  // the load of each entry: the node page's own number (its metrics against its capacity)
  const loads = useQueries({ queries: entries.map((e) => ({ ...nodeQuery(e.nodeId), enabled: p.data === "ready" })) });
  const loadOf = (i: number) => {
    const d = loads[i]?.data;
    return d ? nodeLoad(d.metrics, d.node?.bandwidthMbps ?? 0, p.nowS)?.percent ?? null : null;
  };
  const nodeOf = (id: string) => p.nodes.find((n) => n.id === id);

  const dead = entries.find((e) => e.entry && (e.entry.offline || e.entry.hidden));
  const old = entries.filter((e) => e.entry?.oldAgent && !e.entry.offline && !e.entry.hidden);
  const hasPairs = model.exits.some((x) => x.pairs.length > 0);
  const allDirect = model.exits.length > 0 && model.exits.every((x) => x.tone === "ok" && x.kind === "direct");
  // what stands in place of the table
  const empty: "nomeas" | "empty" | "noent" | null = !p.edge && model.unmeasured ? "nomeas" : allDirect && !hasPairs ? "empty" : entries.length === 0 && model.exits.some((x) => x.tone !== "ok") ? "noent" : null;
  const ready = data === "ready" && empty === null;
  const canWiz = data === "ready" && empty !== "noent" && entries.length > 0;
  const stale = Math.max(0, ...model.exits.map((x) => x.staleDays), ...entries.map((x) => x.staleDays));
  const rows = filterExits(sortExits(model.exits), filter);
  const flt: [WayFilter, string, number][] = [
    ["all", t("nw.f.all"), model.exits.length],
    ["noway", t("nw.f.noway"), model.noWayN],
    ["part", t("nw.f.part"), model.partN],
    ["bridge", t("nw.f.bridge"), model.bridgeN],
  ];

  const sum = data === "load" ? t("nw.sum.load") : data === "error" ? t("nw.sum.err") : empty ? "" : waysSummary(t, model, { country, deadEntry: dead ? { name: dead.name, why: dead.entry!.offline ? "offline" : "hidden" } : undefined });

  // the open pair
  const [exitId, entryId] = open.split(">");
  const exitW = model.byNode.get(exitId ?? "");
  const pair = exitW?.pairs.find((x) => x.entryId === entryId);
  const openPair = ready && exitW && pair ? { exit: exitW, pair } : null;

  const valid = (exit: NodeWays, entry: NodeWays) => !relayPairRefusal(entry.nodeId, exit.nodeId, p.nodesIn) && nodeOf(entry.nodeId)?.countryCode !== nodeOf(exit.nodeId)?.countryCode;

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center justify-between gap-3">
        <p className="m-0 text-sm text-sec">{sum}</p>
        {canWiz && (
          <Button variant="primary" size="lg" className="max-md:flex-[1_1_100%]" onClick={() => p.openWay()}>
            {t("nw.wiz", { country })}
          </Button>
        )}
      </div>
      {ready && (
        <FilterChips
          className="mt-3 max-md:flex-nowrap max-md:overflow-x-auto"
          aria-label={t("nw.view")}
          value={filter}
          onValueChange={setFilter}
          options={flt.filter((f) => f[0] === "all" || f[2] > 0).map(([value, name, n]) => ({ value, label: <>{name}<span className="font-mono text-[11px] font-normal text-muted">{n}</span></> }))}
        />
      )}
      {data === "ready" &&
        entries.map((e) => {
          const note = e.entry?.offline ? "off" : e.entry?.hidden ? "hid" : null;
          if (!note) return null;
          return (
            <Note key={e.nodeId} tone="bad" link={<Link to="/nodes/$id" params={{ id: e.nodeId }} className="an-lk">{t(note === "off" ? "nw.note.off.btn" : "nw.note.hid.btn")}</Link>}>
              {t(note === "off" ? "nw.note.off" : "nw.note.hid", { entry: e.name, country })}
            </Note>
          );
        })}
      {data === "ready" &&
        old.map((e) => (
          <Note key={e.nodeId} tone="part" link={<Link to="/updates" className="an-lk">{t("nw.note.old.btn")}</Link>}>
            {t("nw.note.old", { entry: e.name })}
          </Note>
        ))}
      {ready && stale > 0 && (
        <Note tone="none" link={<Link to="/reachability" className="an-lk">{t("nw.emp.src")}</Link>}>
          {t.n("nw.note.stale", stale, { country })}
        </Note>
      )}
      {ready && p.edge && <Note tone="none">{t("nw.note.cf")}</Note>}

      {data === "load" && (
        <section aria-busy="true" aria-label={t("nw.sk")} className="relative mt-3 overflow-hidden rounded-card border border-line bg-surface p-0">
          {["64px", "56px", "72px", "56px", "64px", "48px"].map((w, i) => (
            <div key={i} className="rw-skr">
              <span className="flex items-center gap-3">
                <Sk w="36px" className="h-9 flex-none rounded-[10px]" />
                <Sk w={w} className="h-3 rounded-md" />
              </span>
              <Sk w="96px" className="h-[22px] rounded-[11px]" />
              <Sk w="100%" className="h-9 rounded-[10px]" />
              <Sk w="40px" className="h-3 rounded-md" />
            </div>
          ))}
        </section>
      )}
      {data === "error" && (
        <section role="alert" className="relative mt-4 flex flex-col items-center gap-1 rounded-card border border-line bg-surface px-6 py-10 text-center">
          <span className={cx(tile, "mb-2.5 bg-danger-soft text-danger-text")} aria-hidden>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M12 8v5M12 16.5v.01M10.3 4l-7.3 12.5a2 2 0 0 0 1.7 3h14.6a2 2 0 0 0 1.7-3L13.7 4a2 2 0 0 0-3.4 0z" />
            </svg>
          </span>
          <b className="text-base font-medium">{t("nw.err.t")}</b>
          <span className="max-w-[420px] text-sm leading-5 text-sec">{t("nw.err.s")}</span>
          <Button size="lg" className="mt-3.5" onClick={p.onRetry}>
            {t("common.retry")}
          </Button>
        </section>
      )}
      {data === "ready" && empty && (
        <section className="relative mt-3 flex flex-col items-center gap-1 rounded-card border border-line bg-surface px-6 py-10 text-center">
          <span className={cx(tile, "mb-2.5", empty === "empty" && "bg-ok-soft text-ok-text")} aria-hidden>
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d={empty === "empty" ? "M5 12.5l4.5 4.5L19 7.5" : empty === "noent" ? "M4 5h16v6H4zM4 13h16v6H4zM8 8h.01M8 16h.01" : "M4 7h16M4 12h16M4 17h10"} />
            </svg>
          </span>
          <b className="text-base font-medium">{t(`nw.emp.${empty}.t` as const, { country })}</b>
          <span className="max-w-[420px] text-sm leading-5 text-sec">{t(`nw.emp.${empty}.s` as const)}</span>
          {empty === "empty" && entries.length > 0 && (
            <Button size="lg" className="mt-3.5" onClick={() => p.openWay()}>
              {t("nw.emp.route")}
            </Button>
          )}
          {empty === "noent" && (
            <Button size="lg" className="mt-3.5" onClick={p.onAddNode}>
              {t("nw.emp.add")}
            </Button>
          )}
          {empty === "nomeas" && (
            <Link to="/reachability" className="mt-3.5 inline-flex h-10 items-center justify-center rounded-field border border-line bg-step px-4 text-sm font-medium text-fg hover:bg-(--step-hover)">
              {t("nw.emp.src")}
            </Link>
          )}
        </section>
      )}

      {ready && (
        <div className={cx("rw-wrap relative mt-3 grid grid-cols-[minmax(0,1fr)] items-start gap-4", openPair && "open")}>
          <section className={cx("rw-mx relative rounded-card border border-line bg-surface", p.edge && "cf")} style={{ "--ec": entries.length } as CSSProperties}>
            <div role="row" className="rw-row rw-hd">
              <span>{t("nw.t.exit")}</span>
              <span>{t("nw.t.direct")}</span>
              {entries.map((e, i) => {
                const st = e.entry!;
                const tone: WayTone = st.offline || st.hidden ? "bad" : st.oldAgent ? "part" : e.direct.tone;
                const sub = st.offline
                  ? t("nw.entry.off")
                  : st.hidden
                    ? t("nw.entry.hidden", { country })
                    : st.oldAgent
                      ? t("nw.eh.old")
                      : [e.direct.tone === "ok" ? t("nw.entry.seen") : e.direct.tone === "part" ? t("nw.d.part", { networks: e.direct.dark.join(", ") }) : t("nw.nodata"), t.n("nw.eh.relays", relaysThrough(model, e.nodeId))].join(" · ");
                const pct = loadOf(i);
                const node = nodeOf(e.nodeId);
                return (
                  <div key={e.nodeId} data-node={e.nodeId} className={cx("rw-eh gap-0.5 border-s border-line", tone === "bad" && "bg-danger-soft")}>
                    <b className="flex min-w-0 items-center gap-1.5 text-sm font-medium text-fg">
                      <u className="text-xs font-normal text-muted no-underline">{t("nw.t.via")}</u>
                      <Flag code={node?.countryCode ?? ""} size={14} />
                      <span className="font-mono font-medium tabular-nums">{e.name}</span>
                      <i aria-hidden className={cx("rw-ld", pct === null ? "" : pct > 60 ? "l3" : pct > 25 ? "l2" : "l1")}>
                        <i />
                        <i />
                        <i />
                      </i>
                      <em className="font-mono text-xs font-normal whitespace-nowrap text-muted not-italic" title={t("nw.t.load")}>
                        {pct === null ? "—" : `${pct} %`}
                      </em>
                    </b>
                    <span title={sub} className="flex min-w-0 items-center gap-1.5 text-xs whitespace-nowrap">
                      <WayGlyph kind={tone} />
                      <span className="overflow-hidden text-ellipsis">{sub}</span>
                    </span>
                  </div>
                );
              })}
              <span>IPv6</span>
            </div>
            {rows.map((x) => {
              const d = x.direct;
              const dk = d.tone === "bad" && x.tone !== "bad" ? "shut" : d.tone;
              const best = p.entryFor(x.nodeId);
              const node = nodeOf(x.nodeId);
              const v6 = node ? entryOf(node.ipv6, ipv6FactOf(undefined)) : undefined;
              const v6k = v6 && entryState(v6) === "on" ? "on" : v6 && entryOffered(v6) ? "can" : "none";
              return (
                <div key={x.nodeId} data-node={x.nodeId} role="row" className="rw-row">
                  <Link to="/nodes/$id" params={{ id: x.nodeId }} className="group grid grid-cols-[14px_20px_minmax(0,1fr)] items-center gap-2 text-inherit no-underline">
                    <WayGlyph kind={x.tone} />
                    <Flag code={node?.countryCode ?? ""} size={14} />
                    <span className="flex flex-col">
                      <b className="font-mono font-medium tabular-nums group-hover:underline group-hover:underline-offset-[3px]">{x.name}</b>
                      <em className={cx("text-xs leading-4 text-muted not-italic", x.tone === "bad" ? "text-danger-text" : x.tone === "part" && "text-warn-text")}>{verdictWord(t, x.tone)}</em>
                    </span>
                  </Link>
                  <span data-l={t("nw.t.direct")} className={cx("rw-d overflow-hidden text-[13px] text-ellipsis whitespace-nowrap text-sec", dk === "shut" && "text-muted")}>
                    <WayGlyph kind={dk} />
                    {d.tone === "ok" ? t("nw.allNets") : d.tone === "part" ? t("nw.d.part", { networks: d.dark.join(", ") }) : d.tone === "bad" ? t("nw.d.shut") : t("nw.d.nodata")}
                  </span>
                  {entries.map((e) => {
                    const st = e.entry!;
                    const key = `${x.nodeId}>${e.nodeId}`;
                    const pr = x.pairs.find((q) => q.entryId === e.nodeId);
                    // an entry that is down or old shows its trouble once, in its head: its cells are dashes
                    if (st.offline || st.hidden || st.oldAgent)
                      return (
                        <div key={key} data-l={e.name} className="rw-c">
                          <span className="rw-dash block px-4 text-muted" aria-label={t("nw.nodata")}>
                            —
                          </span>
                        </div>
                      );
                    if (pr) {
                      const c = pairCell(t, pr, x.name, fmt);
                      return (
                        <div key={key} data-l={e.name} className="rw-c">
                          <button
                            type="button"
                            aria-pressed={open === key}
                            onClick={() => setOpen(open === key ? "" : key)}
                            className={cx(
                              "rw-pb flex min-w-0 flex-col items-start gap-0.5 rounded-ctl px-2 py-1.5 text-start transition-[background-color] duration-150 ease-out hover:bg-step",
                              open === key && "bg-step shadow-[inset_0_0_0_1px_var(--accent-line)]",
                            )}
                          >
                            <span className="flex items-center gap-2 text-sm leading-5">
                              <WayGlyph kind={c.tone} />
                              <b className="font-medium">{c.word}</b>
                            </span>
                            {c.sub && (
                              <em className={cx("max-w-full ps-4 text-xs leading-4 not-italic", c.tone === "part" ? "text-warn-text whitespace-normal" : c.tone === "bad" ? "text-danger-text whitespace-normal" : "overflow-hidden text-ellipsis whitespace-nowrap text-muted")}>{c.sub}</em>
                            )}
                          </button>
                        </div>
                      );
                    }
                    const can = valid(x, e);
                    if (can && x.noWay && best === e.nodeId)
                      return (
                        <div key={key} data-l={e.name} className="rw-c">
                          <Button size="sm" className="max-w-full px-2 text-xs" onClick={() => p.openWay(x.nodeId, e.nodeId)}>
                            {t("nw.c.need", { entry: e.name })}
                          </Button>
                        </div>
                      );
                    if (can && d.tone !== "ok")
                      return (
                        <div key={key} data-l={e.name} className="rw-c">
                          <button type="button" className="rw-add" onClick={() => p.openWay(x.nodeId, e.nodeId)}>
                            {t("nw.c.add", { entry: e.name })}
                          </button>
                        </div>
                      );
                    return <div key={key} data-l={e.name} className="rw-c" />;
                  })}
                  <span data-l="IPv6" className={cx("rw-v6", v6k === "on" && "ok")}>
                    <WayGlyph kind={v6k === "on" ? "ok" : v6k === "can" ? "none" : "hid"} />
                    {v6k === "on" ? t("nw.v6.on") : v6k === "can" ? t("nw.v6.can") : "—"}
                  </span>
                </div>
              );
            })}
            {rows.length === 0 && <p className="rw-foot">{model.exits.length > 0 ? t("nw.none.filter") : t("nw.none.exits")}</p>}
            <p className="rw-foot">
              <span>
                <b>{t("nw.c.fwd")}</b> — {t("nw.foot.fwd")}
              </span>
              <span>
                <b>{t("nw.c.bridge")}</b> — {t("nw.foot.bridge")}
              </span>
              <span>{t("nw.foot.twice")}</span>
            </p>
          </section>
          {openPair && (
            <PairSheet
              key={open}
              start={p.demo?.open === open ? p.demo.sheet : undefined}
              exit={openPair.exit}
              pair={openPair.pair}
              exitCc={nodeOf(openPair.exit.nodeId)?.countryCode ?? ""}
              entryCc={nodeOf(openPair.pair.entryId)?.countryCode ?? ""}
              entryDirect={model.byNode.get(openPair.pair.entryId)?.direct ?? openPair.exit.direct}
              country={p.country}
              nowS={p.nowS}
              other={p.entryFor(openPair.exit.nodeId, openPair.pair.entryId)}
              onClose={() => setOpen("")}
              onMore={() => p.openWay(openPair.exit.nodeId, openPair.pair.entryId)}
              onOther={() => p.openWay(openPair.exit.nodeId, p.entryFor(openPair.exit.nodeId, openPair.pair.entryId))}
            />
          )}
        </div>
      )}
    </>
  );
}

/** The relays that pass through an entry: the head of its column counts them. */
const relaysThrough = (model: FleetWays, entryId: string) => model.exits.reduce((a, x) => a + x.pairs.filter((q) => q.entryId === entryId).reduce((s, q) => s + q.relays.length, 0), 0);

/** A line above the table: what to know (an entry that is down, an old agent, an old measurement), and where to go. */
function Note({ tone, children, link }: { tone: WayTone; children: ReactNode; link?: ReactNode }) {
  return (
    <p className={noteBox}>
      <WayGlyph kind={tone} />
      <span className="min-w-0 flex-1 [overflow-wrap:anywhere]">{children}</span>
      {link && <span className="flex-none">{link}</span>}
    </p>
  );
}

/** A bar of the skeleton: the project's pulse, a width of its own. */
const Sk = ({ w, className }: { w: string; className?: string }) => <span aria-hidden className={cx("block animate-[mg-skp_1.4s_ease-in-out_infinite] bg-step", className)} style={{ width: w }} />;
