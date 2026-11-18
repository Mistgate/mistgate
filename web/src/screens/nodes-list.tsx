import { Link, useNavigate } from "@tanstack/react-router";
import { type CSSProperties, type ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Fresh } from "@/components/fx/fresh";
import { Check } from "@/components/ui/bits";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { StatusDot } from "@/components/ui/status";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import type { Fmt } from "@/lib/format";
import { kindRank, nodeKind, useNodeStatus } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { sortProtocols } from "@/lib/series";
import { useOlderNodes } from "@/lib/updates";
import type { FleetWays } from "@/lib/fleet-map";
import { countryIn } from "@/lib/health";
import { ruCell, type RuState } from "./nodes-ways-text";
import { toneWord, WayGlyph } from "./nodes-ways-parts";

type Node = Plain<NodeMsg>;

// The list of the nodes, as the redesign board draws it (PnNodes, the fz-r rows): one card, a head of sortable columns and a
// row per node. The columns follow the width of the card itself, not the window's: a wide table shows every column, then the
// uptime goes, then the processor's bar, then the traffic and the processor, then the profiles. In the selecting mode a
// check column opens at the start of every row (a click on a row ticks it instead of opening the node).

export type SortKey = "name" | "state" | "online" | "today" | "cpu";
export type Sort = { key: SortKey; dir: 1 | -1 };
export const firstSort: Sort = { key: "state", dir: -1 };

export const onlineOf = (n: Node) => n.online.reduce((a, o) => a + o.users, 0);

/** The value a sort key reads from a node; the state is "how bad" (a larger number is worse), so the default order puts problems first. */
const sortValue: Record<SortKey, (n: Node) => string | number> = {
  name: (n) => n.name,
  state: (n) => 5 - kindRank(nodeKind(n)),
  online: onlineOf,
  today: (n) => n.trafficTodayBytes,
  cpu: (n) => (n.hasMetrics ? n.cpuPct : -1),
};

export function sortNodes(rows: readonly Node[], sort: Sort): Node[] {
  const read = sortValue[sort.key];
  // a stable sort: equals keep the order the panel sent them in
  return [...rows].sort((a, b) => {
    const x = read(a);
    const y = read(b);
    return (typeof x === "string" && typeof y === "string" ? x.localeCompare(y, undefined, { numeric: true }) : x > y ? 1 : x < y ? -1 : 0) * sort.dir;
  });
}

/** Ticking nodes for a bulk change (owner only): which are ticked, and how the page ticks them. */
export type Select = {
  /** The check column is open. */
  on: boolean;
  ticked: ReadonlySet<string>;
  toggle: (id: string) => void;
  /** 50 are ticked: the rest cannot be (SetNodesDecoyMode takes at most 50; a batch of more would run for minutes). */
  full: boolean;
  pickable: (n: Node) => boolean;
};

// The columns: the check (0 or 20px, --cb), the node, the state, the profiles, online, today, the processor, the uptime, the
// arrow. The widths the card's own width picks (rows and head share them).
const grid =
  "grid-cols-[var(--cb)_minmax(170px,1.2fr)_minmax(130px,.9fr)_minmax(190px,1fr)_150px_56px_78px_96px_52px_16px] " +
  "@max-[1120px]/fzt:grid-cols-[var(--cb)_minmax(160px,1.2fr)_minmax(120px,.8fr)_minmax(180px,1fr)_150px_48px_76px_88px_16px] " +
  "@max-[1000px]/fzt:grid-cols-[var(--cb)_minmax(170px,1.3fr)_minmax(130px,1fr)_150px_56px_80px_96px_16px] " +
  "@max-[780px]/fzt:grid-cols-[var(--cb)_minmax(0,1.3fr)_minmax(0,1fr)_150px_44px_16px] " +
  "@max-[600px]/fzt:grid-cols-[var(--cb)_minmax(0,1.3fr)_minmax(0,1fr)_44px_16px]";
const rowBase =
  "grid items-center gap-4 px-6 [transition:background-color_.12s_var(--e),grid-template-columns_.25s_var(--e)] @max-[940px]/fzt:gap-3 max-md:gap-3 max-md:px-4 " + grid;
// the columns that give way as the card narrows
const upCell = "@max-[1120px]/fzt:hidden";
// "From Russia" has a column of its own down to 1000; narrower, its word goes under the node's name
const ruCol = "@max-[1000px]/fzt:hidden";
const sumCell = "@max-[780px]/fzt:hidden";
const chipsCell = "@max-[600px]/fzt:hidden";

/**
 * The line under a node's state. A node that works but runs an outdated agent says so there and the line is a link to the
 * Updates screen, which says what to do (the Updates module knows by build time, not by version text); otherwise it is the
 * reason of the state.
 */
function useSub() {
  const t = useT();
  const st = useNodeStatus();
  const older = useOlderNodes();
  return (n: Node): { text: string; update: boolean } => {
    if (nodeKind(n) === "ok" && older.has(n.id)) return { text: `${t("nodes.agentOld")} ${n.agentVersion}`.trim(), update: true };
    return { text: st.reason(n.reason), update: false };
  };
}

function SortHead({ id, label, sort, onSort, right, title, className }: { id: SortKey | null; label: string; sort: Sort; onSort: (k: SortKey) => void; right?: boolean; title?: string; className?: string }) {
  const on = id !== null && sort.key === id;
  return (
    <div role="columnheader" aria-sort={on ? (sort.dir > 0 ? "ascending" : "descending") : id ? "none" : undefined} className={cx("flex min-w-0 text-[13px]", right && "justify-end", className)}>
      {id ? (
        <button
          type="button"
          title={title}
          onClick={() => onSort(id)}
          className={cx("inline-flex h-7 items-center gap-1 text-start whitespace-nowrap transition-colors duration-150 ease-out hover:text-fg", on ? "text-fg" : "text-muted")}
        >
          {label}
          {on && (
            <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
              <path d={sort.dir > 0 ? "M7 14l5-5 5 5" : "M7 10l5 5 5-5"} />
            </svg>
          )}
        </button>
      ) : (
        <span title={title} className="inline-flex h-7 items-center whitespace-nowrap text-muted">
          {label}
        </span>
      )}
    </div>
  );
}

const numCell = "font-mono text-[13px] font-medium text-end tabular-nums";

/** What the "From Russia" column stands on: the model, the state of its data, the country, whether the viewer is a helper, and what "Get around…" does. */
export type ListWays = { model: FleetWays; state: RuState; country: string; helper: boolean; onFix: (nodeId: string) => void };

export function NodeList({
  rows,
  fmt,
  select,
  sort,
  onSort,
  filtered,
  onReset,
  onAdd,
  ways,
}: {
  rows: Node[];
  fmt: Fmt;
  select: Select | undefined;
  sort: Sort;
  onSort: (k: SortKey) => void;
  /** Nodes exist, the filters hide them all. */
  filtered: boolean;
  onReset: () => void;
  onAdd: () => void;
  /** The ways out of the country (the "From Russia" column); without it the column says nothing. */
  ways?: ListWays;
}) {
  const t = useT();
  const sub = useSub();
  const navigate = useNavigate();
  const selecting = !!select?.on;
  return (
    <section data-select={selecting} className="@container/fzt relative rounded-card border border-line bg-surface [--cb:0px] data-[select=true]:[--cb:20px]">
      {rows.length > 0 && (
        <div role="row" className={cx("min-h-10 text-[13px] text-muted max-md:hidden", rowBase)}>
          <span aria-hidden />
          <SortHead id="name" label={t("nodes.col.node")} sort={sort} onSort={onSort} />
          <SortHead id="state" label={t("nv.col.state")} sort={sort} onSort={onSort} />
          <SortHead id={null} label={t("nw.col", { country: countryIn(t, ways?.country || "RU") })} sort={sort} onSort={onSort} className={ruCol} />
          <SortHead id={null} label={t("nodes.col.protocols")} sort={sort} onSort={onSort} className={chipsCell} />
          <SortHead id="online" label={t("nodes.col.online")} sort={sort} onSort={onSort} right />
          <SortHead id="today" label={t("nodes.col.today")} sort={sort} onSort={onSort} right title={t("nodes.todayHint")} className={sumCell} />
          <SortHead id="cpu" label={t("nodes.col.cpu")} sort={sort} onSort={onSort} className={sumCell} />
          <SortHead id={null} label={t("nodes.col.uptime")} sort={sort} onSort={onSort} right className={upCell} />
          <span aria-hidden />
        </div>
      )}
      {rows.map((n) => {
        const kind = nodeKind(n);
        const s = sub(n);
        const on = !!select?.ticked.has(n.id);
        const dead = n.status === NodeStatus.DOWN;
        const where = [n.location || fmt.country(n.countryCode), n.provider].filter(Boolean).join(" · ");
        const protocols = sortProtocols(n.protocols);
        const w = ways?.model.byNode.get(n.id);
        const cell = ways ? ruCell(t, w, { country: countryIn(t, ways.country), unmeasured: ways.model.unmeasured, state: ways.state, helper: ways.helper }) : null;
        // an entry-only node passes relays on: people are not counted on it, and what it carries is what it relays to exits
        const entryOnly = w?.role === "entry";
        const open = () => {
          // selecting: the row is a tick box, not a door
          if (select?.on) return select.pickable(n) && !(select.full && !on) ? select.toggle(n.id) : undefined;
          void navigate({ to: "/nodes/$id", params: { id: n.id } });
        };
        return (
          <Fresh
            kind="node"
            id={n.id}
            key={n.id}
            className={cx(
              "group relative min-h-[60px] border-t border-line transition-colors max-md:min-h-16 max-md:[[role=row]+&]:border-t-0",
              rowBase,
              on ? "bg-accent-soft" : "hover:bg-step",
            )}
          >
            <span aria-hidden={!selecting} inert={!selecting} className="relative z-10 grid place-items-center overflow-hidden">
              {select && <Check aria-label={t("bulk.select", { name: n.name })} checked={on} disabled={!select.pickable(n) || (select.full && !on)} onCheckedChange={() => select.toggle(n.id)} />}
            </span>
            <div className={cx("flex min-w-0 items-center gap-3", dead && "opacity-50")}>
              <Flag code={n.countryCode} size={16} />
              <span className="flex min-w-0 flex-col">
                <span className="flex min-w-0 flex-col">
                  <b className="truncate font-mono font-medium tabular-nums">
                    {n.name}
                    {n.sharing?.mode === NodeShareMode.NONE && n.status !== NodeStatus.RETIRED && (
                      <i title={t(n.sharing.idle ? "ns.label.tipIdle" : "ns.label.tip")} className="ms-2 inline-flex h-[18px] items-center rounded-[9px] bg-tone-lavender-bg px-1.5 align-[1px] font-sans text-[11px] leading-none font-medium text-tone-lavender not-italic">
                        {t("ns.label")}
                      </i>
                    )}
                    {(w?.role === "entry" || w?.role === "mixed") && (
                      <i className="ms-2 inline-flex h-[18px] items-center rounded-[9px] bg-tone-sand-bg px-1.5 align-[1px] font-sans text-[11px] leading-none font-medium text-tone-sand not-italic">{t("nw.role")}</i>
                    )}
                  </b>
                  {where && <span className="truncate text-[13px] text-sec">{where}</span>}
                  {cell && (
                    <span className={cx("mt-1 hidden min-w-0 items-center gap-1.5 text-xs whitespace-nowrap @max-[1000px]/fzt:flex", cell.tone === "bad" ? "text-danger-text" : cell.tone === "part" ? "text-warn-text" : "text-sec")}>
                      <WayGlyph kind={cell.tone} />
                      <span className="truncate">{cell.word}</span>
                    </span>
                  )}
                </span>
                <NodeNotes node={n} />
              </span>
            </div>
            <div className="flex min-w-0 flex-col">
              <span className={cx("inline-flex items-center gap-2 whitespace-nowrap", `tone-${kind}`, kind !== "ok" && "tone-text")}>
                <StatusDot kind={kind} />
                <StateWord n={n} />
              </span>
              {s.text &&
                (s.update ? (
                  <Link to="/updates" className="relative z-10 w-fit max-w-full truncate text-[12px] text-muted underline decoration-dotted underline-offset-2 transition-colors hover:text-fg">
                    {s.text}
                  </Link>
                ) : (
                  <span title={s.text} className="truncate text-[12px] text-muted">
                    {s.text}
                  </span>
                ))}
            </div>
            <div data-ru={n.id} className={cx("relative z-10 flex min-w-0 cursor-pointer flex-col text-xs font-normal text-sec", ruCol)} title={cell?.tip || undefined} onClick={open}>
              {cell ? (
                <>
                  <span className="flex min-w-0 items-center gap-2 text-sm leading-5 text-fg">
                    <WayGlyph kind={cell.tone} />
                    <b className={cx("truncate font-medium", cell.tone === "none" && "font-normal text-muted", toneWord[cell.tone])}>{cell.word}</b>
                    {cell.noWay && ways && (
                      <button
                        type="button"
                        onClick={(e) => {
                          e.stopPropagation();
                          ways.onFix(n.id);
                        }}
                        className="ms-1 text-[13px] font-medium whitespace-nowrap text-accent-text hover:underline hover:underline-offset-[3px]"
                      >
                        {t("nw.fix")}
                      </button>
                    )}
                  </span>
                  {cell.sub && <span className="truncate text-xs text-muted">{cell.sub}</span>}
                </>
              ) : (
                <span className="text-muted">—</span>
              )}
            </div>
            <div className={cx("relative z-10 w-max max-w-none cursor-pointer", chipsCell, dead && "opacity-50")} onClick={() => void navigate({ to: "/nodes/$id", params: { id: n.id } })}>
              <span className="flex gap-1">
                {protocols.length > 0 ? protocols.map((p) => <ProtocolTag key={p} protocol={p} compact />) : <span className="text-muted">—</span>}
              </span>
            </div>
            <span className={numCell}>
              <Scramble once value={entryOnly ? "—" : String(onlineOf(n))} />
            </span>
            <span className={cx(numCell, sumCell, entryOnly && "cursor-help text-tone-lavender")} title={entryOnly ? t("nw.tip.today") : undefined}>
              <Scramble once value={fmt.bytes(entryOnly ? (n.relayTodayBytes ?? 0) : n.trafficTodayBytes)} />
            </span>
            <span className={cx("grid grid-cols-[36px_minmax(0,1fr)] items-center gap-2", sumCell)}>
              <span className={cx(numCell)}>
                <Scramble once value={n.hasMetrics ? `${Math.round(n.cpuPct)}%` : "—"} />
              </span>
              <i
                aria-hidden
                className="block h-1 rounded-[2px] @max-[940px]/fzt:hidden"
                style={{ background: `linear-gradient(90deg, var(--accent-mark) ${n.hasMetrics ? Math.min(100, Math.round(n.cpuPct)) : 0}%, var(--step) 0)` } as CSSProperties}
              />
            </span>
            <span className={cx(numCell, upCell)}>
              <Scramble once value={n.uptimeS > 0 ? fmt.duration(n.uptimeS) : "—"} />
            </span>
            <Icon name="chevronRight" size={16} className="block flex-none text-muted transition-[transform,color] duration-150 ease-out group-hover:translate-x-0.5 group-hover:text-fg rtl:group-hover:-translate-x-0.5" />
            {/* last, so it lies over what is not a control of its own (the row is one big target) */}
            <Link
              to="/nodes/$id"
              params={{ id: n.id }}
              aria-label={n.name}
              className="absolute inset-0 rounded-[inherit] focus-visible:outline-offset-[-2px]"
              onClick={(e) => {
                // selecting: the row is a tick box, not a door
                if (!select?.on || !select.pickable(n) || (select.full && !on)) return;
                e.preventDefault();
                select.toggle(n.id);
              }}
            />
          </Fresh>
        );
      })}
      {filtered && rows.length === 0 && (
        <div className="flex flex-col items-center gap-2 border-t border-line px-6 py-10 text-sec">
          <b className="font-medium text-fg">{t("nv.noMatch")}</b>
          <button type="button" onClick={onReset} className="text-fg underline decoration-line underline-offset-4 hover:decoration-current">
            {t("nv.resetFilter")}
          </button>
        </div>
      )}
      <button
        type="button"
        onClick={onAdd}
        className={cx(
          "flex min-h-14 w-full items-center justify-center gap-2 rounded-b-card border-t border-dashed border-line px-6 font-medium text-sec transition-colors duration-150 ease-out hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)] hover:text-fg max-md:px-4 max-md:py-3.5",
          rows.length === 0 && !filtered && "border-t-0",
        )}
      >
        <Icon name="plus" size={16} />
        {t("node.add.button")}
      </button>
    </section>
  );
}

/** The state's word ("Healthy", "Attention", "Unreachable"), in the colour of the state. */
function StateWord({ n }: { n: Node }) {
  const st = useNodeStatus();
  return <>{st.word(n)}</>;
}

/**
 * The operator's notes of a node (a line or two), when it has any. Above the row's link: only the notes show this title, and
 * clicking them still opens the node.
 */
function NodeNotes({ node }: { node: Node }): ReactNode {
  const notes = node.notes?.trim();
  if (!notes) return null;
  return (
    <Link
      to="/nodes/$id"
      params={{ id: node.id }}
      title={notes}
      tabIndex={-1}
      className="relative z-10 mt-1 flex w-fit min-w-0 max-w-full items-start gap-1.5 border-s-2 border-line ps-2 text-xs font-normal text-muted"
    >
      <Icon name="text" size={12} className="mt-0.5 block shrink-0 text-muted" />
      <p className="line-clamp-2 whitespace-pre-line break-words">{notes}</p>
    </Link>
  );
}
