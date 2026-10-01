import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { PageTitle } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { kindTextClass, StatusDot } from "@/components/ui/status";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import type { Plain } from "@/lib/plain";

type Node = Plain<NodeMsg>;
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import { isProblem, nodeKind, useNodeStatus } from "@/lib/node-status";
import { nodesQuery } from "@/lib/queries";
import { protocolName, protocolShort, sortProtocols } from "@/lib/series";
import { useOlderNodes } from "@/lib/updates";

type StatusFilter = "all" | "problems" | "working";
const all = "all";

const onlineOf = (n: Node) => n.online.reduce((a, o) => a + o.users, 0);

export function NodesScreen() {
  const t = useT();
  const fmt = useFmt();
  const addNode = useAddNode();
  const q = useQuery(nodesQuery);
  const [status, setStatus] = useState<StatusFilter>("all");
  const [protocol, setProtocol] = useState(all);
  const [country, setCountry] = useState(all);

  const nodes = useMemo(() => q.data?.nodes ?? [], [q.data]);
  const problems = nodes.filter(isProblem).length;
  const working = nodes.filter((n) => nodeKind(n) === "ok").length;
  const onlineTotal = nodes.reduce((a, n) => a + onlineOf(n), 0);

  const protocols = useMemo(() => sortProtocols(nodes.flatMap((n) => n.protocols)), [nodes]);
  const countries = useMemo(
    () => [...new Set(nodes.map((n) => n.countryCode).filter(Boolean))].sort((a, b) => fmt.country(a).localeCompare(fmt.country(b), fmt.lang)),
    [nodes, fmt],
  );

  const rows = nodes.filter(
    (n) =>
      (status === "all" || (status === "problems" ? isProblem(n) : nodeKind(n) === "ok")) &&
      (protocol === all || n.protocols.includes(protocol)) &&
      (country === all || n.countryCode === country),
  );

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

          <Table rows={rows} fmt={fmt} />
          <Cards rows={rows} fmt={fmt} />
          {rows.length === 0 && <p className="p-8 text-center text-[13px] text-muted">{t("nodes.noRows")}</p>}
        </>
      )}
    </div>
  );
}

const cols = "md:grid-cols-[minmax(220px,2.4fr)_minmax(150px,1fr)_minmax(90px,0.8fr)_56px_76px_92px_60px]";

/**
 * The label under a node name; an outdated agent (the Updates module knows by build time, not by version text) turns
 * a healthy line amber and sends to the Updates screen, which says what to do.
 */
function useSub() {
  const t = useT();
  const st = useNodeStatus();
  const older = useOlderNodes();
  return (n: Node): { text: string; cls: string; update: boolean } => {
    const kind = nodeKind(n);
    if (kind === "ok" && older.has(n.id)) {
      return { text: `${t("nodes.agentOld")} ${n.agentVersion}`.trim(), cls: kindTextClass.warn, update: true };
    }
    return { text: st.line(n), cls: kindTextClass[kind], update: false };
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

function Table({ rows, fmt }: { rows: Node[]; fmt: Fmt }) {
  const t = useT();
  const sub = useSub();
  const heads = [t("nodes.col.node"), t("nodes.col.address"), t("nodes.col.protocols"), t("nodes.col.online"), t("nodes.col.today"), t("nodes.col.cpu"), t("nodes.col.uptime")];
  if (rows.length === 0) return null;
  return (
    <div className="hidden overflow-hidden rounded-card-lg border border-line bg-surface md:block">
      <div className={cx("grid h-10 items-center gap-4 border-b border-line px-5 text-xs font-semibold text-muted", cols)}>
        {heads.map((h, i) => (
          <span
            key={h}
            // "today" is a UTC day, not the owner's: say so where it is read
            title={i === 4 ? t("nodes.todayHint") : undefined}
            className={cx("whitespace-nowrap", i >= 3 && "text-right", i === 4 && "cursor-help underline decoration-faint decoration-dotted underline-offset-[3px]")}
          >
            {h}
          </span>
        ))}
      </div>
      {rows.map((n) => {
        const kind = nodeKind(n);
        const s = sub(n);
        return (
          <div
            key={n.id}
            className={cx("relative grid min-h-[60px] items-center gap-4 border-b border-line px-5 py-2 text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2", cols)}
          >
            <Link to="/nodes/$id" params={{ id: n.id }} aria-label={n.name} className="absolute inset-0" />
            <div className="flex min-w-0 items-center gap-3">
              <StatusDot kind={kind} />
              <div className="flex min-w-0 flex-col gap-0.5">
                <span className="flex items-baseline gap-2">
                  <b className="text-sm">{n.name}</b>
                  <span className="truncate text-xs text-muted">{[n.location || fmt.country(n.countryCode), n.provider].filter(Boolean).join(" · ")}</span>
                </span>
                <SubLine s={s} className="line-clamp-2 text-xs" />
              </div>
            </div>
            <span title={n.address} className="font-mono text-xs break-all text-muted">
              {n.address}
            </span>
            <span title={n.protocols.map(protocolName).join(", ")} className="truncate font-mono text-xs text-muted">
              {n.protocols.map(protocolShort).join(", ") || "—"}
            </span>
            <span className="text-right font-mono">{onlineOf(n)}</span>
            <span className="text-right font-mono">{fmt.bytes(n.trafficTodayBytes)}</span>
            <div className="flex items-center justify-end gap-2">
              <span className="h-1 w-11 overflow-hidden rounded-[2px] bg-surface-2">
                <span className="block h-full bg-accent" style={{ width: `${n.hasMetrics ? Math.min(100, n.cpuPct) : 0}%` }} />
              </span>
              <span className="w-8 text-right font-mono text-xs">{n.hasMetrics ? `${Math.round(n.cpuPct)}%` : "—"}</span>
            </div>
            <span className="text-right font-mono text-xs text-muted">{n.uptimeS > 0 ? fmt.duration(n.uptimeS) : "—"}</span>
          </div>
        );
      })}
    </div>
  );
}

function Cards({ rows, fmt }: { rows: Node[]; fmt: Fmt }) {
  const t = useT();
  const st = useNodeStatus();
  const sub = useSub();
  return (
    <div className="flex flex-col gap-2 md:hidden">
      {rows.map((n, i) => {
        const kind = nodeKind(n);
        const s = sub(n);
        const stat = (value: string, label: string, hint?: string) => (
          <div className="flex min-w-0 flex-col gap-0.5" title={hint}>
            <span className="truncate font-mono text-[15px] font-bold">{value}</span>
            <span className="text-[11px] text-muted">{label}</span>
          </div>
        );
        return (
          <div key={n.id} className="screen-enter relative flex flex-col gap-2.5 rounded-card border border-line bg-surface p-3.5" style={{ animationDelay: `${i * 45}ms` }}>
            <Link to="/nodes/$id" params={{ id: n.id }} aria-label={n.name} className="absolute inset-0 rounded-card" />
            <div className="flex items-center gap-2">
              <span className="flex h-5 items-center rounded-md bg-surface-2 px-1.5 font-mono text-[10px] font-bold text-muted">{n.countryCode || "—"}</span>
              <b className="text-[15px]">{n.name}</b>
              <span className="min-w-0 flex-1 truncate text-xs text-muted">{[n.location || fmt.country(n.countryCode), n.provider].filter(Boolean).join(" · ")}</span>
              <span className={cx("flex items-center gap-1.5 text-xs font-semibold whitespace-nowrap", kindTextClass[kind])}>
                <StatusDot kind={kind} />
                {st.word(n)}
              </span>
            </div>
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
              <span className="min-w-0 text-right font-mono text-[11px] break-all text-muted">{n.address}</span>
            </div>
            {(kind !== "ok" || s.update) && <SubLine s={s} className="text-xs text-pretty" />}
          </div>
        );
      })}
    </div>
  );
}
