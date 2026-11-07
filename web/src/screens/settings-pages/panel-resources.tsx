import { queryOptions, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { useT } from "@/i18n";
import { ops } from "@/lib/api";
import { useFmt } from "@/lib/format";
import { isUnavailable } from "@/lib/ops";
import { plain } from "@/lib/plain";

// Settings -> System, "Panel resources": what the panel itself costs this server (OpsService.GetPanelResources): its
// process, the machine, the database and the admin API's last five minutes. Every admin may read it. The Cloudflare edition
// answers UNIMPLEMENTED: the card is not drawn there (the owner's diagnostics cards say once what the edition lacks).

export const panelResourcesQuery = queryOptions({
  queryKey: ["ops", "panel-resources"],
  queryFn: async ({ signal }) => plain(await ops.getPanelResources({}, { signal })),
  refetchInterval: 15_000,
  retry: false,
});

const card = "flex min-w-0 flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]";

/** One figure: its name, the value, and a line under it. */
function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="font-mono text-sm font-semibold text-fg">{value}</dd>
      {sub && <dd className="text-[11px] leading-snug text-muted">{sub}</dd>}
    </div>
  );
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-2.5">
      <h3 className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{title}</h3>
      <dl className="grid gap-3 rounded-field border border-line bg-canvas p-3.5 sm:grid-cols-2 lg:grid-cols-4">{children}</dl>
    </div>
  );
}

export function PanelResourcesCard() {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(panelResourcesQuery);
  if (q.isError && isUnavailable(q.error)) return null;
  const r = q.data;
  if (!r) return null;
  const p = r.process!;
  const h = r.host!;
  const d = r.database!;
  const api = r.rpc!;
  const b = (n: number) => fmt.bytes(n, 1024);
  const pct = (v: number) => `${fmt.num(v, v < 10 ? 1 : 0)}%`;
  const free = d.pageCount > 0 ? (d.freelistCount / d.pageCount) * 100 : 0;
  const machine = h.ramTotalBytes > 0; // not a Linux host: the machine's figures are not read
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="server" tone="sage">
        {t("pres.title")}
      </SectionLabel>
      <p className="max-w-2xl text-[13px] leading-relaxed text-pretty text-muted">{t("pres.intro")}</p>

      <Group title={t("pres.process")}>
        <Stat label={t("pres.cpu")} value={pct(p.cpuPct)} sub={t("pres.cpu.sub")} />
        <Stat label={t("pres.mem")} value={p.rssBytes > 0 ? b(p.rssBytes) : "—"} sub={t("pres.mem.sub", { heap: b(p.goHeapBytes), total: b(p.goTotalBytes) })} />
        <Stat
          label={t("pres.fds")}
          value={p.fdLimit > 0 ? t("res.of", { n: fmt.num(p.openFds, 0), max: fmt.num(p.fdLimit, 0) }) : fmt.num(p.openFds, 0)}
          sub={t("pres.threads", { goroutines: fmt.num(p.goroutines, 0), threads: fmt.num(p.threads, 0) })}
        />
        <Stat label={t("pres.uptime")} value={fmt.duration(p.uptimeS)} />
      </Group>

      {machine ? (
        <Group title={t("pres.host")}>
          <Stat
            label={t("pres.host.cpu")}
            value={pct(h.cpuBusyPct)}
            sub={t("pres.host.cpu.sub", { steal: pct(h.stealPct), iowait: pct(h.iowaitPct), cpus: String(h.cpus), load: fmt.num(h.load1, 2) })}
          />
          <Stat label={t("pres.host.ram")} value={t("node.m.of", { used: b(h.ramUsedBytes), total: b(h.ramTotalBytes) })} />
          <Stat label={t("pres.host.swap")} value={h.swapTotalBytes > 0 ? t("node.m.of", { used: b(h.swapUsedBytes), total: b(h.swapTotalBytes) }) : t("res.swap.none")} />
          <Stat label={t("pres.host.disk")} value={h.diskTotalBytes > 0 ? t("node.m.of", { used: b(h.diskUsedBytes), total: b(h.diskTotalBytes) }) : "—"} />
        </Group>
      ) : (
        <p className="text-xs text-muted">{t("pres.host.none")}</p>
      )}

      <Group title={t("pres.db")}>
        <Stat label={t("pres.db.size")} value={b(d.fileBytes + d.walBytes)} sub={t("pres.db.size.sub", { file: b(d.fileBytes), wal: b(d.walBytes) })} />
        <Stat label={t("pres.db.free")} value={b(d.freelistCount * d.pageSize)} sub={t("pres.db.free.sub", { pct: pct(free) })} />
        <Stat label={t("pres.agents")} value={t("res.of", { n: fmt.num(r.agentsConnected, 0), max: fmt.num(r.nodes, 0) })} />
        <Stat label={t("pres.measured")} value={d.measuredUnix > 0 ? fmt.clock(d.measuredUnix) : "—"} />
      </Group>

      <Group title={t("pres.api")}>
        <Stat label={t("pres.api.requests")} value={fmt.num(api.requests, 0)} />
        <Stat label={t("pres.api.errors")} value={fmt.num(api.serverErrors, 0)} sub={t("pres.api.refused", { n: fmt.num(api.clientErrors, 0) })} />
        <Stat label={t("pres.api.p95")} value={api.requests > 0 ? t("pres.api.ms", { ms: fmt.num(api.p95Ms, api.p95Ms < 10 ? 1 : 0) }) : "—"} sub={t("pres.api.p95.sub")} />
      </Group>
    </section>
  );
}
