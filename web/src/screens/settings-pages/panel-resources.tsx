import { queryOptions, useQuery } from "@tanstack/react-query";
import type { CSSProperties } from "react";
import { useT } from "@/i18n";
import { ops } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { isUnavailable } from "@/lib/ops";
import { plain } from "@/lib/plain";
import { ICONS, Sec } from "./sx-bits";
import "./settings-sys.css";

// Settings -> System, "Panel resources": what the panel itself costs this server (OpsService.GetPanelResources): its process, the
// machine, the database and the admin API's last five minutes, as four columns of figures with a thin bar where there is a share.
// Every admin may read it. The Cloudflare edition answers UNIMPLEMENTED: the card is not drawn there (the owner's diagnostics cards
// say once what the edition lacks).

export const panelResourcesQuery = queryOptions({
  queryKey: ["ops", "panel-resources"],
  queryFn: async ({ signal }) => plain(await ops.getPanelResources({}, { signal })),
  refetchInterval: 15_000,
  retry: false,
});

/** How loaded a share looks: the bar and the figure turn amber, then red (the board's "under load" state of the server). */
export type Load = "" | "warn" | "bad";
export const loadOf = (pct: number, warn: number, bad: number): Load => (pct >= bad ? "bad" : pct >= warn ? "warn" : "");

type Tile = { k: string; v: string; sub?: string; w?: number; c?: Load };

/** One figure: its name, the value, a thin bar when it is a share, and a line under it (sx-r). */
function Stat({ tile }: { tile: Tile }) {
  return (
    <div className={cx("sx-r", tile.c)}>
      <span>{tile.k}</span>
      <b className="mono">{tile.v}</b>
      {tile.w !== undefined && <i className="sx-bar" style={{ "--w": `${Math.min(100, Math.max(0, tile.w))}%` } as CSSProperties} aria-hidden />}
      {tile.sub && <em>{tile.sub}</em>}
    </div>
  );
}

function Group({ title, tiles, note }: { title: string; tiles: Tile[]; note?: string }) {
  return (
    <div className="sx-rg">
      <span className="rw-sl">{title}</span>
      <div className="sx-rr">
        {tiles.map((x) => (
          <Stat key={x.k} tile={x} />
        ))}
        {note && <p className="sx-rn">{note}</p>}
      </div>
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
  const share = (used: number, total: number) => (total > 0 ? (used / total) * 100 : 0);
  const free = d.pageCount > 0 ? (d.freelistCount / d.pageCount) * 100 : 0;
  const machine = h.ramTotalBytes > 0; // not a Linux host: the machine's figures are not read
  const heapShare = share(p.goHeapBytes, p.goTotalBytes);
  const ramShare = share(h.ramUsedBytes, h.ramTotalBytes);
  const swapShare = share(h.swapUsedBytes, h.swapTotalBytes);
  const diskShare = share(h.diskUsedBytes, h.diskTotalBytes);
  const proc: Tile[] = [
    { k: t("pres.cpu"), v: pct(p.cpuPct), sub: t("pres.cpu.sub"), w: p.cpuPct, c: loadOf(p.cpuPct, 50, 85) },
    { k: t("pres.mem"), v: p.rssBytes > 0 ? b(p.rssBytes) : "—", sub: t("pres.mem.sub", { heap: b(p.goHeapBytes), total: b(p.goTotalBytes) }), w: heapShare, c: loadOf(heapShare, 75, 92) },
    { k: t("pres.fds"), v: p.fdLimit > 0 ? t("res.of", { n: fmt.num(p.openFds, 0), max: fmt.num(p.fdLimit, 0) }) : fmt.num(p.openFds, 0), sub: t("pres.threads", { goroutines: fmt.num(p.goroutines, 0), threads: fmt.num(p.threads, 0) }) },
    { k: t("pres.uptime"), v: fmt.duration(p.uptimeS) },
  ];
  const srv: Tile[] = machine
    ? [
        { k: t("pres.host.cpu"), v: pct(h.cpuBusyPct), sub: t("pres.host.cpu.sub", { steal: pct(h.stealPct), iowait: pct(h.iowaitPct), cpus: String(h.cpus), load: fmt.num(h.load1, 2) }), w: h.cpuBusyPct, c: loadOf(h.cpuBusyPct, 70, 90) },
        { k: t("pres.host.ram"), v: t("node.m.of", { used: b(h.ramUsedBytes), total: b(h.ramTotalBytes) }), w: ramShare, c: loadOf(ramShare, 80, 90) },
        { k: t("pres.host.swap"), v: h.swapTotalBytes > 0 ? t("node.m.of", { used: b(h.swapUsedBytes), total: b(h.swapTotalBytes) }) : t("res.swap.none"), w: h.swapTotalBytes > 0 ? swapShare : undefined, c: loadOf(swapShare, 50, 85) },
        { k: t("pres.host.disk"), v: h.diskTotalBytes > 0 ? t("node.m.of", { used: b(h.diskUsedBytes), total: b(h.diskTotalBytes) }) : "—", w: h.diskTotalBytes > 0 ? diskShare : undefined, c: loadOf(diskShare, 85, 95) },
      ]
    : [];
  const db: Tile[] = [
    { k: t("pres.db.size"), v: b(d.fileBytes + d.walBytes), sub: t("pres.db.size.sub", { file: b(d.fileBytes), wal: b(d.walBytes) }) },
    { k: t("pres.db.free"), v: b(d.freelistCount * d.pageSize), sub: t("pres.db.free.sub", { pct: pct(free) }) },
    { k: t("pres.agents"), v: t("res.of", { n: fmt.num(r.agentsConnected, 0), max: fmt.num(r.nodes, 0) }) },
    { k: t("pres.measured"), v: d.measuredUnix > 0 ? fmt.clock(d.measuredUnix) : "—" },
  ];
  const rpc: Tile[] = [
    { k: t("pres.api.requests"), v: fmt.num(api.requests, 0) },
    { k: t("pres.api.errors"), v: fmt.num(api.serverErrors, 0), sub: t("pres.api.refused", { n: fmt.num(api.clientErrors, 0) }), c: api.serverErrors >= 10 ? "bad" : api.serverErrors > 0 ? "warn" : "" },
    { k: t("pres.api.p95"), v: api.requests > 0 ? t("pres.api.ms", { ms: fmt.num(api.p95Ms, api.p95Ms < 10 ? 1 : 0) }) : "—", sub: t("pres.api.p95.sub"), c: api.requests > 0 ? loadOf(api.p95Ms, 500, 2000) : "" },
  ];
  return (
    <Sec tone="sky" d={ICONS.gauge} title={t("pres.title")} lead={t("sx.sys.res.lead")}>
      <div className="sx-res">
        <Group title={t("pres.process")} tiles={proc} />
        <Group title={t("pres.host")} tiles={srv} note={machine ? undefined : t("pres.host.none")} />
        <Group title={t("pres.db")} tiles={db} />
        <Group title={t("pres.api")} tiles={rpc} />
      </div>
    </Sec>
  );
}

