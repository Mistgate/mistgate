import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useCallback, useMemo, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { PageTitle } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { kindTextClass, StatusDot } from "@/components/ui/status";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import type { Plain } from "@/lib/plain";

type Node = Plain<NodeMsg>;
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { isProblem, nodeKind, useNodeStatus } from "@/lib/node-status";
import { maxBulkNodes } from "@/lib/decoy";
import { nodesQuery } from "@/lib/queries";
import { protocolName, protocolShort, sortProtocols } from "@/lib/series";
import { meQuery } from "@/lib/session";
import { useOlderNodes } from "@/lib/updates";
import { useBatch } from "@/lib/batch";
import { PickMenu } from "@/components/bulk";
import { NodesBulk } from "./nodes-bulk";
import { Fresh } from "@/components/fx/fresh";
import { useListEntrance } from "@/components/fx/page";
import { passwordOnly, useBulkRun } from "./nodes-bulk-actions";
import { PanelKeyOffer } from "./nodes-panel-key";

type StatusFilter = "all" | "problems" | "working";
const all = "all";

const onlineOf = (n: Node) => n.online.reduce((a, o) => a + o.users, 0);

export function NodesScreen() {
  const t = useT();
  const fmt = useFmt();
  const addNode = useAddNode();
  const q = useQuery(nodesQuery);
  const entrance = useListEntrance(q.data !== undefined);
  const [status, setStatus] = useState<StatusFilter>("all");
  const [protocol, setProtocol] = useState(all);
  const [country, setCountry] = useState(all);
  // ticked nodes and the bulk bar (owner only: SSH, rollouts and the decoy site are owner calls)
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const [ticked, setTicked] = useState<ReadonlySet<string>>(new Set());
  const batch = useBatch();
  const run = useBulkRun(batch, owner);
  const older = useOlderNodes();
  const clear = useCallback(() => setTicked(new Set()), []);

  const nodes = useMemo(() => q.data?.nodes ?? [], [q.data]);
  const problems = nodes.filter(isProblem).length;
  const working = nodes.filter((n) => nodeKind(n) === "ok").length;
  const onlineTotal = nodes.reduce((a, n) => a + onlineOf(n), 0);

  const protocols = useMemo(() => sortProtocols(nodes.flatMap((n) => n.protocols)), [nodes]);
  const countries = useMemo(
    () => [...new Set(nodes.map((n) => n.countryCode).filter(Boolean))].sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.locale)),
    [nodes, fmt],
  );

  const rows = nodes.filter(
    (n) =>
      (status === "all" || (status === "problems" ? isProblem(n) : nodeKind(n) === "ok")) &&
      (protocol === all || n.protocols.includes(protocol)) &&
      (country === all || n.countryCode === country),
  );

  const pickable = (n: Node) => n.status !== NodeStatus.RETIRED;
  const picked = nodes.filter((n) => ticked.has(n.id));
  const toggle = (id: string) => setTicked((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : s.size < maxBulkNodes ? new Set([...s, id]) : s));
  const shownIds = rows.filter(pickable).map((n) => n.id);
  const allShown = shownIds.length > 0 && shownIds.every((id) => ticked.has(id));
  const toggleAll = () => setTicked(allShown ? new Set([...ticked].filter((id) => !shownIds.includes(id))) : new Set([...ticked, ...shownIds].slice(0, maxBulkNodes)));
  const select: Select | undefined = owner ? { ticked, toggle, toggleAll, allShown, someShown: shownIds.some((id) => ticked.has(id)), full: ticked.size >= maxBulkNodes, pickable } : undefined;
  // every quick pick replaces the selection; "Password only" is the offer's own rule
  const live = nodes.filter(pickable);
  const picks = [
    { label: "bulk.pick.shown", ids: shownIds },
    { label: "bulk.pick.problems", ids: live.filter(isProblem).map((n) => n.id) },
    { label: "bulk.pick.password", ids: live.filter((n) => passwordOnly(run.ctx.access?.get(n.id))).map((n) => n.id) },
    { label: "bulk.pick.outdated", ids: live.filter((n) => older.has(n.id)).map((n) => n.id) },
  ] as const;
  const pick = (ids: readonly string[]) => setTicked(new Set(ids.slice(0, maxBulkNodes)));

  if (!q.data) {
    return (
      <div className="flex flex-col gap-3.5">
        <PageTitle>{t("nav.nodes")}</PageTitle>
        {q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />}
      </div>
    );
  }

  const chip = (label: string, n: number) => (
    <span className="flex items-center gap-1.5">
      {label}
      <span className="font-mono text-[11px] text-muted">{n}</span>
    </span>
  );

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-[3px]">
          <PageTitle>{t("nav.nodes")}</PageTitle>
          <span className="text-xs text-muted">
            {nodes.length > 0 &&
              `${t.n("nodes.summary", nodes.length, { o: onlineTotal })}${problems ? ` · ${t.n("nodes.withProblems", problems)}` : ""}`}
          </span>
        </div>
        <Button variant="primary" size="md" onClick={() => addNode()}>
          <Icon name="plus" size={14} />
          <span className="max-md:hidden">{t("node.add.button")}</span>
          <span className="md:hidden">{t("node.add.short")}</span>
        </Button>
      </div>

      {nodes.length === 0 ? (
        <div className="rounded-card-lg border border-dashed border-line">
          <EmptyState title={t("nodes.emptyTitle")} action={<Button variant="primary" size="lg" onClick={() => addNode()}>{t("node.add.button")}</Button>}>
            {t("nodes.emptyText")}
          </EmptyState>
        </div>
      ) : (
        <>
          {owner && (
            <PanelKeyOffer
              busy={batch.running}
              onStart={(ids) => {
                const set = new Set(ids.slice(0, maxBulkNodes));
                setTicked(set);
                void run.run("panelKey", nodes.filter((n) => set.has(n.id)));
              }}
            />
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Segmented
              variant="flat"
              aria-label={t("nodes.filterStatus")}
              value={status}
              onValueChange={setStatus}
              options={[
                { value: "all", label: chip(t("nodes.all"), nodes.length) },
                { value: "problems", label: chip(t("nodes.problems"), problems) },
                { value: "working", label: chip(t("nodes.working"), working) },
              ]}
            />
            {owner && <PickMenu picks={picks.map((p) => ({ label: t(p.label), ids: p.ids }))} any={ticked.size > 0} onPick={pick} />}
            <span className="flex-1" />
            <div className="w-[150px]">
              <Select
                compact
                aria-label={t("nodes.filterProtocol")}
                value={protocol}
                onValueChange={setProtocol}
                options={[{ value: all, label: t("nodes.allProtocols") }, ...protocols.map((p) => ({ value: p, label: protocolName(p) }))]}
              />
            </div>
            <div className="w-[150px]">
              <Select
                compact
                aria-label={t("nodes.filterCountry")}
                value={country}
                onValueChange={setCountry}
                options={[{ value: all, label: t("nodes.allCountries") }, ...countries.map((c) => ({ value: c, label: fmt.country(c) }))]}
              />
            </div>
          </div>

          <Table rows={rows} fmt={fmt} select={select} entrance={entrance} />
          <Cards rows={rows} fmt={fmt} select={select} entrance={entrance} />
          {rows.length === 0 && <p className="p-8 text-center text-[13px] text-muted">{t("nodes.noRows")}</p>}
          {owner && <NodesBulk nodes={nodes} picked={picked} shown={new Set(rows.map((n) => n.id))} batch={batch} run={run} onClear={clear} />}
        </>
      )}
    </div>
  );
}

const cols = "md:grid-cols-[minmax(220px,2.4fr)_minmax(150px,1fr)_minmax(90px,0.8fr)_56px_76px_92px_60px]";
const colsSelect = "md:grid-cols-[18px_minmax(220px,2.4fr)_minmax(150px,1fr)_minmax(90px,0.8fr)_56px_76px_92px_60px]";

/** Ticking nodes for a bulk change (owner only): which are ticked, and how the page ticks them. */
type Entrance = ReturnType<typeof useListEntrance>;
type Select = {
  ticked: ReadonlySet<string>;
  toggle: (id: string) => void;
  toggleAll: () => void;
  allShown: boolean;
  someShown: boolean;
  /** 50 are ticked: the rest cannot be (SetNodesDecoyMode takes at most 50; a batch of more would run for minutes). */
  full: boolean;
  pickable: (n: Node) => boolean;
};

/**
 * One tick box: above the row's overlay link (z-10), so ticking does not open the node. The label around it is the hit
 * area, a padded square: the 16px box alone was easy to miss, and a miss opened the node.
 */
function Tick({ select, node, className }: { select: Select; node: Node; className?: string }) {
  const t = useT();
  const on = select.ticked.has(node.id);
  return (
    <label className={cx("relative z-10 -m-2 grid flex-none cursor-pointer p-2", className)}>
      <input
        type="checkbox"
        className="size-4 accent-accent"
        aria-label={t("bulk.select", { name: node.name })}
        checked={on}
        disabled={!select.pickable(node) || (select.full && !on)}
        onChange={() => select.toggle(node.id)}
      />
    </label>
  );
}

/**
 * The line under a node's name. A table row has only the dot beside the name, so its line says the status too
 * (`withWord`: the reason, else the word); a card has the word beside the name, so its line is the reason alone. An
 * outdated agent (the Updates module knows by build time, not by version text) turns the line into an amber link to the
 * Updates screen, which says what to do.
 */
function useSub() {
  const t = useT();
  const st = useNodeStatus();
  const older = useOlderNodes();
  return (n: Node, withWord: boolean): { text: string; cls: string; update: boolean } => {
    const kind = nodeKind(n);
    if (kind === "ok" && older.has(n.id)) {
      return { text: `${t(withWord ? "nodes.agentOldLine" : "nodes.agentOld")} ${n.agentVersion}`.trim(), cls: kindTextClass.warn, update: true };
    }
    return { text: withWord ? st.line(n) : st.reason(n.reason), cls: kindTextClass[kind], update: false };
  };
}

/** The sub-line text; the outdated-agent mark is its own link above the row's overlay link (an anchor cannot sit inside an anchor). */
function SubLine({ s, className }: { s: { text: string; cls: string; update: boolean }; className?: string }) {
  if (!s.update)
    return (
      <span title={s.text} className={cx(className, s.cls)}>
        {s.text}
      </span>
    );
  return (
    <Link to="/updates" className={cx("relative z-10 w-fit underline decoration-dotted underline-offset-2", className, s.cls)}>
      {s.text}
    </Link>
  );
}

/** Keep the status dot and its word together, independently of the height of the notes below. */
function NodeStatusLabel({ node }: { node: Node }) {
  const st = useNodeStatus();
  const kind = nodeKind(node);
  return (
    <span className={cx("inline-flex shrink-0 items-center gap-1.5 text-xs font-semibold whitespace-nowrap", kindTextClass[kind])}>
      <StatusDot kind={kind} />
      {st.word(node)}
    </span>
  );
}

function Table({ rows, fmt, select, entrance }: { rows: Node[]; fmt: Fmt; select: Select | undefined; entrance: Entrance }) {
  const t = useT();
  const sub = useSub();
  const heads = [t("nodes.col.node"), t("nodes.col.address"), t("nodes.col.protocols"), t("nodes.col.online"), t("nodes.col.today"), t("nodes.col.cpu"), t("nodes.col.uptime")];
  if (rows.length === 0) return null;
  return (
    <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
      <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", select ? colsSelect : cols)}>
        {select && (
          <input
            type="checkbox"
            className="size-4 accent-accent"
            aria-label={t("bulk.selectAll")}
            checked={select.allShown}
            ref={(el) => {
              if (el) el.indeterminate = select.someShown && !select.allShown;
            }}
            onChange={select.toggleAll}
          />
        )}
        {heads.map((h, i) => (
          <span
            key={h}
            // "today" is a UTC day, not the owner's: say so where it is read
            title={i === 4 ? t("nodes.todayHint") : undefined}
            className={cx("whitespace-nowrap", i >= 3 && "text-end", i === 4 && "cursor-help underline decoration-faint decoration-dotted underline-offset-[3px]")}
          >
            {h}
          </span>
        ))}
      </div>
      {rows.map((n, i) => {
        const kind = nodeKind(n);
        const s = sub(n, true);
        return (
          <Fresh kind="node" id={n.id}
            key={n.id}
            style={entrance(i)}
            className={cx("relative grid min-h-[60px] items-start gap-4 border-b border-line px-5 py-2 text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2", select ? colsSelect : cols)}
          >
            <Link to="/nodes/$id" params={{ id: n.id }} aria-label={n.name} className="absolute inset-0" />
            {/* -mt-1.5 instead of the hit area's -m-2 at the top: the 16px box centred on the name's 20px line */}
            {select && <Tick select={select} node={n} className="-mt-1.5" />}
            <div className="flex min-w-0 items-start gap-3">
              <span className="flex h-5 flex-none items-center">
                <StatusDot kind={kind} />
              </span>
              <div className="flex min-w-0 flex-col gap-0.5">
                <span className="flex min-w-0 items-baseline gap-2">
                  <b className="text-sm">{n.name}</b>
                  <span className="min-w-0 truncate text-xs text-muted">{[n.location || fmt.country(n.countryCode), n.provider].filter(Boolean).join(" · ")}</span>
                </span>
                <SubLine s={s} className="line-clamp-2 text-xs" />
                <NodeNotes node={n} className="mt-1" />
              </div>
            </div>
            <span title={n.address} className="self-start pt-0.5 font-mono text-xs break-all text-muted">
              {n.address}
            </span>
            <span title={n.protocols.map(protocolName).join(", ")} className="self-start pt-0.5 truncate font-mono text-xs text-muted">
              {n.protocols.map(protocolShort).join(", ") || "—"}
            </span>
            <span className="self-start pt-0.5 text-end font-mono">{onlineOf(n)}</span>
            <span className="self-start pt-0.5 text-end font-mono">{fmt.bytes(n.trafficTodayBytes)}</span>
            <div className="flex items-center justify-end gap-2 self-start pt-1">
              <span className="h-1 w-11 overflow-hidden rounded-[2px] bg-surface-2">
                <span className="block h-full bg-accent" style={{ width: `${n.hasMetrics ? Math.min(100, n.cpuPct) : 0}%` }} />
              </span>
              <span className="w-8 text-end font-mono text-xs">{n.hasMetrics ? `${Math.round(n.cpuPct)}%` : "—"}</span>
            </div>
            <span className="self-start pt-0.5 text-end font-mono text-xs text-muted">{n.uptimeS > 0 ? fmt.duration(n.uptimeS) : "—"}</span>
          </Fresh>
        );
      })}
    </div>
  );
}

function Cards({ rows, fmt, select, entrance }: { rows: Node[]; fmt: Fmt; select: Select | undefined; entrance: Entrance }) {
  const t = useT();
  const sub = useSub();
  return (
    <div className="flex flex-col gap-2 md:hidden">
      {rows.map((n, i) => {
        const s = sub(n, false);
        const stat =(value: string, label: string, hint?: string) => (
          <div className="flex min-w-0 flex-col gap-0.5" title={hint}>
            <span className="truncate font-mono text-[15px] font-bold">{value}</span>
            <span className="text-[11px] text-muted">{label}</span>
          </div>
        );
        return (
          <Fresh key={n.id} kind="node" id={n.id} className="relative flex flex-col gap-2.5 rounded-card border border-line bg-surface p-3.5" style={entrance(i)}>
            <Link to="/nodes/$id" params={{ id: n.id }} aria-label={n.name} className="absolute inset-0 rounded-card" />
            <div className="flex items-center gap-2">
              {select && <Tick select={select} node={n} />}
              <span className="flex h-5 items-center rounded-md bg-surface-2 px-1.5 font-mono text-[10px] font-bold text-muted">{n.countryCode || "—"}</span>
              <b className="text-[15px]">{n.name}</b>
              <span className="min-w-0 flex-1 truncate text-xs text-muted">{[n.location || fmt.country(n.countryCode), n.provider].filter(Boolean).join(" · ")}</span>
              <NodeStatusLabel node={n} />
            </div>
            {(n.reason?.code || s.update) && <SubLine s={s} className="line-clamp-2 text-xs" />}
            <NodeNotes node={n} />
            <div className="grid grid-cols-4 gap-2">
              {stat(String(onlineOf(n)), t("nodes.online"))}
              {stat(fmt.bytes(n.trafficTodayBytes), t("nodes.today"), t("nodes.todayHint"))}
              {stat(n.hasMetrics ? `${Math.round(n.cpuPct)}%` : "—", "CPU")}
              {stat(n.uptimeS > 0 ? fmt.duration(n.uptimeS) : "—", t("nodes.uptime"))}
            </div>
            <div className="flex items-center gap-1.5">
              {n.protocols.map((p) => (
                <span key={p} title={protocolName(p)} className="flex h-5 flex-none items-center rounded-md bg-surface-2 px-1.5 font-mono text-[10px] font-bold text-muted">
                  {protocolShort(p)}
                </span>
              ))}
              <span className="flex-1" />
              <span className="min-w-0 text-end font-mono text-[11px] break-all text-muted">{n.address}</span>
            </div>
          </Fresh>
        );
      })}
    </div>
  );
}

function NodeNotes({ node, className }: { node: Node; className?: string }) {
  const notes = node.notes?.trim();
  if (!notes) return null;
  // Above the row link: only the notes show this title, and clicking them still opens the node. The icon's own
  // flex-none is replaced by a className, so shrink-0 keeps long notes from squeezing it to a dot.
  return (
    <Link to="/nodes/$id" params={{ id: node.id }} title={notes} tabIndex={-1} className={cx("relative z-10 flex w-fit min-w-0 max-w-full items-start gap-1.5 border-s-2 border-line ps-2 text-xs text-muted", className)}>
      <Icon name="text" size={12} className="mt-0.5 shrink-0 text-faint" />
      <p className="line-clamp-2 whitespace-pre-line break-words">{notes}</p>
    </Link>
  );
}
