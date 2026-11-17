import { Link } from "@tanstack/react-router";
import type { CSSProperties, ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Glyph, glyphs, CardHead } from "@/screens/overview-parts";
import { Tip } from "@/components/ui/tooltip";
import type { NodeMetrics } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { conntrackFill, cpuSplit, fdFill, hasResources, linkUse, memSplit } from "@/lib/resources";
import { useNow } from "@/lib/time";

// "Resources" on the node's Overview (the board's .nd-rs): CPU, memory, disk and the link as four blocks, each with a bar of what
// took it (Mistgate itself, the kernel's packet work, the hypervisor's steal, everything else) and a legend; under them the meters
// of connection tracking, open files, sockets and the uptime, and one line about the Mistgate process. From an agent that predates
// the resource fields there is nothing to split: the blocks show the plain readings and the card says to update it.

/** One part of a bar, with its legend entry. `cls` is one of the bar's colours (mg, net, st, dn, up, oth). */
type Part = { key: string; label: string; tip?: string; pct: number; cls: string; value: string };

const valueTone = (pct: number) => (pct >= 90 ? "text-danger-text" : pct >= 60 ? "text-warn-text" : "");

/** A block of the card: the name and the big number, a line under it, the bar of its parts and the legend. */
function Block({ label, value, unit, note, tone, parts }: { label: string; value: string; unit?: string; note: string; tone: number; parts: readonly Part[] }) {
  return (
    <div className="flex min-w-0 flex-col bg-surface px-6 pt-4 pb-[18px] max-[720px]:px-4 max-[720px]:pt-3.5 max-[720px]:pb-4">
      <div className="flex items-baseline justify-between gap-3">
        <b className="font-medium">{label}</b>
        <span className={cx("font-mono text-[22px] leading-7 font-medium tabular-nums", valueTone(tone))}>
          {value}
          {unit && <small className="ms-0.5 font-sans text-[13px] font-normal text-sec">{unit}</small>}
        </span>
      </div>
      <span className="truncate text-[13px] text-muted">{note}</span>
      <div role="img" aria-label={`${label}: ${value}${unit ?? ""}`} className="nd-sbar">
        {parts.map((p) => (
          <i key={p.key} className={p.cls} style={{ "--w": `${Math.max(0.6, Math.min(100, p.pct))}%` } as CSSProperties} />
        ))}
      </div>
      <ul className="mt-2.5 flex flex-wrap gap-x-4 gap-y-1 text-[13px] text-sec">
        {parts.map((p) => (
          <li key={p.key} className="nd-slg inline-flex items-center gap-1.5 whitespace-nowrap">
            <i aria-hidden className={cx("size-2 rounded-full bg-muted", p.cls)} />
            {p.tip ? (
              <Tip label={p.tip}>
                <span tabIndex={0} className="cursor-help underline decoration-faint decoration-dotted underline-offset-[3px] outline-none focus-visible:text-fg">
                  {p.label}
                </span>
              </Tip>
            ) : (
              p.label
            )}{" "}
            <b className="font-mono font-medium text-fg">
              <Scramble value={p.value} className="tabular-nums" />
            </b>
          </li>
        ))}
      </ul>
    </div>
  );
}

/** A meter under the blocks: its name, the number with what it is of, a thin bar in the tone of the fill. */
function Meter({ label, tip, value, sub, pct }: { label: string; tip?: string; value: ReactNode; sub?: string; pct: number | null }) {
  const name = tip ? (
    <Tip label={tip}>
      <span tabIndex={0} className="cursor-help outline-none focus-visible:text-fg">
        {label}
      </span>
    </Tip>
  ) : (
    label
  );
  return (
    <div>
      <dt className="text-xs text-muted">{name}</dt>
      <dd className="mt-1 mb-2 flex min-w-0 flex-wrap content-start gap-x-1.5 gap-y-0.5">
        <b className="font-mono text-[13px] font-medium whitespace-nowrap text-fg tabular-nums">{value}</b>
        {sub && <span className="font-mono text-[13px] whitespace-nowrap text-muted">{sub}</span>}
      </dd>
      <i
        aria-hidden
        className="nd-bar thin"
        style={{ "--w": `${pct === null ? 0 : Math.max(1, pct)}%`, "--fill": pct !== null && pct >= 95 ? "var(--danger)" : pct !== null && pct >= 80 ? "var(--warning)" : "var(--accent-mark)", visibility: pct === null ? "hidden" : undefined } as CSSProperties}
      />
    </div>
  );
}

export function ResourcesCard({ metrics, cpus, offlineSince }: { metrics: Plain<NodeMetrics> | undefined; cpus: number; /** The node does not answer: unix time of its last word; the readings are the last ones it sent. */ offlineSince?: number }) {
  const t = useT();
  const fmt = useFmt();
  const now = useNow(5_000);
  const m = metrics;
  if (!m)
    return (
      <section className="rounded-card border border-line bg-surface">
        <CardHead title={t("res.title")} />
        <p className="border-t border-line px-6 py-4 text-[13px] text-muted max-[720px]:px-4">{offlineSince !== undefined ? t("node.m.offline", { time: fmt.stamp(offlineSince) }) : t("node.m.none")}</p>
      </section>
    );
  const dead = offlineSince !== undefined;
  const pct = (v: number) => `${fmt.num(v, v < 10 ? 1 : 0)}%`;
  const b = (n: number) => fmt.bytes(n, 1024);
  const cpu = cpuSplit(m);
  const mem = memSplit(m);
  const full = hasResources(m);
  const used = (total: number, usedBytes: number) => (total > 0 ? Math.min(100, (usedBytes / total) * 100) : 0);
  const diskPct = used(m.diskTotalBytes, m.diskUsedBytes);
  const ramPct = used(m.ramTotalBytes, m.ramUsedBytes);
  const net = m.netRxBps + m.netTxBps;
  const link = m.nicSpeedMbps > 0 ? m.nicSpeedMbps * 1e6 : 0;
  const ago = Math.max(0, Math.floor(now / 1000) - m.atUnix);

  const cpuBusy = cpu ? cpu.busy : Math.min(100, m.cpuPct);
  const cpuParts: Part[] = cpu
    ? [
        { key: "agent", label: t("res.cpu.agent"), tip: t("res.cpu.agent.tip"), pct: cpu.agent, cls: "mg", value: pct(cpu.agent) },
        { key: "softirq", label: t("res.cpu.softirq"), tip: t("res.cpu.softirq.tip"), pct: cpu.softirq, cls: "net", value: pct(cpu.softirq) },
        { key: "steal", label: t("res.cpu.steal"), tip: t("res.cpu.steal.tip"), pct: cpu.steal, cls: "st", value: pct(cpu.steal) },
        { key: "other", label: t("res.cpu.other"), tip: t("res.cpu.other.tip"), pct: cpu.other, cls: "oth", value: pct(cpu.other) },
      ]
    : [{ key: "busy", label: t("np.res.busy"), pct: cpuBusy, cls: "oth", value: pct(cpuBusy) }];
  const memParts: Part[] =
    mem && m.agent
      ? [
          { key: "agent", label: t("res.mem.agent"), tip: t("res.mem.agent.tip"), pct: mem.agent, cls: "mg", value: b(m.agent.rssBytes) },
          { key: "other", label: t("res.cpu.other"), tip: t("res.mem.other.tip"), pct: mem.other, cls: "oth", value: b(Math.max(0, m.ramUsedBytes - m.agent.rssBytes)) },
        ]
      : [{ key: "used", label: t("np.res.used"), pct: ramPct, cls: "oth", value: m.ramTotalBytes > 0 ? b(m.ramUsedBytes) : "—" }];
  const swapLine = m.swapTotalBytes > 0 ? t("res.swap", { used: b(m.swapUsedBytes), total: b(m.swapTotalBytes) }) : t("res.swap.none");
  const memNote = m.ramTotalBytes > 0 ? [t("node.m.of", { used: b(m.ramUsedBytes), total: b(m.ramTotalBytes) }), full ? swapLine : ""].filter(Boolean).join(" · ") : "";
  const cpuNote = [cpus > 0 ? `${cpus} vCPU` : "", `load ${fmt.num(m.load1, 2)}`, cpu && cpu.iowait >= 1 ? t("res.cpu.iowait", { pct: pct(cpu.iowait) }) : ""].filter(Boolean).join(" · ");

  const ct = conntrackFill(m);
  const fds = fdFill(m);
  const lu = linkUse(m);
  const a = m.agent;

  return (
    <section className={cx("overflow-hidden rounded-card border border-line bg-surface", dead && "[&_.nd-sbar]:opacity-35")}>
      <CardHead title={t("res.title")}>
        <span className="text-[13px] text-muted">{dead || ago > 3600 ? "" : t("np.res.ago", { ago: fmt.duration(ago) })}</span>
      </CardHead>
      {dead && (
        <div className="mx-6 mb-4 grid grid-cols-[32px_minmax(0,1fr)] gap-3 rounded-xl bg-danger-soft p-3 max-[720px]:mx-4">
          <span aria-hidden className="grid size-8 place-items-center text-danger">
            <Glyph d={glyphs.warn} />
          </span>
          <div>
            <b className="block font-medium">{t("np.res.dead")}</b>
            <span className="mt-0.5 block text-[13px] leading-[18px] text-sec">{t("node.m.offline", { time: fmt.stamp(offlineSince) })}</span>
          </div>
        </div>
      )}
      <div className="grid grid-cols-2 gap-px border-y border-line bg-line max-[720px]:grid-cols-1">
        <Block label={t("np.res.cpu")} value={String(Math.round(cpuBusy))} unit="%" note={cpuNote} tone={cpuBusy} parts={cpuParts} />
        <Block label={t("np.res.mem")} value={m.ramTotalBytes > 0 ? String(Math.round(ramPct)) : "—"} unit={m.ramTotalBytes > 0 ? "%" : ""} note={memNote} tone={ramPct} parts={memParts} />
        <Block
          label={t("np.res.disk")}
          value={m.diskTotalBytes > 0 ? String(Math.round(diskPct)) : "—"}
          unit={m.diskTotalBytes > 0 ? "%" : ""}
          note={m.diskTotalBytes > 0 ? t("node.m.of", { used: b(m.diskUsedBytes), total: b(m.diskTotalBytes) }) : ""}
          tone={diskPct}
          parts={[{ key: "used", label: t("np.res.used"), pct: diskPct, cls: "oth", value: m.diskTotalBytes > 0 ? b(m.diskUsedBytes) : "—" }]}
        />
        <Block
          label={t("np.res.net")}
          value={fmt.mbitNum(net)}
          unit={` ${t("unit.mbit")}`}
          note={link > 0 ? t("np.res.link", { speed: fmt.mbit(link) }) : ""}
          tone={link > 0 ? (net / link) * 100 : 0}
          parts={[
            { key: "dn", label: t("np.res.rx"), pct: link > 0 ? (m.netRxBps / link) * 100 : 3, cls: "dn", value: fmt.mbit(m.netRxBps) },
            { key: "up", label: t("np.res.tx"), pct: link > 0 ? (m.netTxBps / link) * 100 : 3, cls: "up", value: fmt.mbit(m.netTxBps) },
          ]}
        />
      </div>
      {!dead && full && a && (
        <>
          <dl className="nd-rm">
            <Meter
              label={t("res.ct")}
              tip={t("res.ct.tip")}
              value={fmt.num(m.conntrackCount, 0)}
              sub={ct === null ? t("res.ct.off") : t("np.res.ofMax", { max: fmt.num(m.conntrackMax, 0) })}
              pct={ct}
            />
            <Meter label={t("res.fds")} tip={t("res.fds.tip")} value={fmt.num(a.openFds, 0)} sub={fds === null ? "" : t("np.res.ofMax", { max: fmt.num(a.fdLimit, 0) })} pct={fds} />
            <Meter label={t("res.sockets")} value={t("res.sockets.value", { tcp: fmt.num(m.tcpSockets, 0), udp: fmt.num(m.udpSockets, 0) })} pct={null} />
            {lu !== null && <Meter label={t("res.link")} tip={t("res.link.tip")} value={pct(lu)} sub={t("res.of", { n: fmt.mbit(Math.max(m.netRxBps, m.netTxBps)), max: fmt.mbit(m.nicSpeedMbps * 1e6) })} pct={lu} />}
          </dl>
          <p className="border-t border-line px-6 pt-3 pb-4 text-[13px] text-muted max-[720px]:px-4 max-[720px]:pb-3.5">
            {t("res.agent", { cpu: pct(a.cpuPct), rss: b(a.rssBytes), heap: b(a.goHeapBytes), goroutines: fmt.num(a.goroutines, 0), threads: fmt.num(a.threads, 0) })}
          </p>
        </>
      )}
      {!dead && !full && (
        <p className="border-t border-line px-6 pt-3 pb-4 text-[13px] leading-relaxed text-pretty text-muted max-[720px]:px-4">
          {t("res.updateAgent")}{" "}
          <Link to="/updates" className="text-sec underline decoration-(--border) underline-offset-4 hover:text-fg">
            {t("res.updateLink")}
          </Link>
        </p>
      )}
    </section>
  );
}
