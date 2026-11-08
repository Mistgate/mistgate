import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Tip } from "@/components/ui/tooltip";
import type { NodeMetrics } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { conntrackFill, cpuSplit, fdFill, fillTone, hasResources, linkUse, memSplit } from "@/lib/resources";

// "Resources" on the node's Overview: what took the machine's CPU and memory (Mistgate itself, the kernel's packet work,
// the hypervisor's steal, everything else), connection tracking, sockets, the agent's open files and the link. From an agent
// that predates these fields there is nothing to split: the card says to update it instead of showing zeros.

/** One part of a stacked bar, with its legend entry. */
type Part = { key: string; label: string; tip: string; pct: number; cls: string; value: string };

const toneBar = { ok: "bg-accent", warn: "bg-warn", bad: "bg-danger" } as const;

/** A thin bar of parts, in order, on the empty track. */
function Bar({ parts, label }: { parts: readonly Pick<Part, "key" | "pct" | "cls">[]; label: string }) {
  return (
    <div role="img" aria-label={label} className="flex h-2 gap-px overflow-hidden rounded-[3px] bg-surface-2">
      {parts.map((p) => (
        <span key={p.key} className={p.cls} style={{ width: `${Math.max(0, Math.min(100, p.pct))}%` }} />
      ))}
    </div>
  );
}

/** The legend of a stacked bar: a dot, the part's name (its tooltip says what it is) and its value. */
function Legend({ parts }: { parts: readonly Part[] }) {
  return (
    <ul className="flex flex-wrap gap-x-3.5 gap-y-1 text-xs text-muted">
      {parts.map((p) => (
        <li key={p.key} className="flex items-center gap-1.5">
          <span aria-hidden className={cx("size-2 rounded-full", p.cls)} />
          <Tip label={p.tip}>
            <span tabIndex={0} className="cursor-help underline decoration-faint decoration-dotted underline-offset-[3px] outline-none focus-visible:text-fg">
              {p.label}
            </span>
          </Tip>
          <b className="font-mono font-bold text-fg">{p.value}</b>
        </li>
      ))}
    </ul>
  );
}

/** A row of the card: its name, the value on the right, an optional bar and a line under it. */
function Row({ label, value, tip, children }: { label: string; value: ReactNode; tip?: string; children?: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-baseline gap-2 text-[13px]">
        {tip ? (
          <Tip label={tip}>
            <span tabIndex={0} className="min-w-0 flex-1 cursor-help truncate font-semibold outline-none focus-visible:text-accent-text">
              {label}
            </span>
          </Tip>
        ) : (
          <span className="min-w-0 flex-1 truncate font-semibold">{label}</span>
        )}
        <span className="font-mono text-xs whitespace-nowrap text-muted">{value}</span>
      </div>
      {children}
    </div>
  );
}

/** A fill meter: the doctor's tones at 80% and 95%. */
function Meter({ pct, label }: { pct: number; label: string }) {
  return <Bar label={label} parts={[{ key: "fill", pct, cls: toneBar[fillTone(pct)] }]} />;
}

export function ResourcesCard({ metrics, cpus }: { metrics: Plain<NodeMetrics> | undefined; cpus: number }) {
  const t = useT();
  const fmt = useFmt();
  const m = metrics;
  if (!m) return null;
  const card = "flex min-w-0 flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4";
  const title = (
    <SectionLabel as="h2" icon="gauge" tone="lavender">
      {t("res.title")}
    </SectionLabel>
  );
  if (!hasResources(m)) {
    return (
      <section className={card}>
        {title}
        <p className="text-[13px] leading-relaxed text-pretty text-muted">
          {t("res.updateAgent")}{" "}
          <Link to="/updates" className="font-bold text-fg underline decoration-faint underline-offset-[3px] hover:text-accent-text">
            {t("res.updateLink")}
          </Link>
        </p>
      </section>
    );
  }
  const pct = (v: number) => `${fmt.num(v, v < 10 ? 1 : 0)}%`;
  const cpu = cpuSplit(m)!;
  const cpuParts: Part[] = [
    { key: "agent", label: t("res.cpu.agent"), tip: t("res.cpu.agent.tip"), pct: cpu.agent, cls: "bg-accent", value: pct(cpu.agent) },
    { key: "softirq", label: t("res.cpu.softirq"), tip: t("res.cpu.softirq.tip"), pct: cpu.softirq, cls: "bg-warn", value: pct(cpu.softirq) },
    { key: "steal", label: t("res.cpu.steal"), tip: t("res.cpu.steal.tip"), pct: cpu.steal, cls: "bg-danger", value: pct(cpu.steal) },
    { key: "other", label: t("res.cpu.other"), tip: t("res.cpu.other.tip"), pct: cpu.other, cls: "bg-them", value: pct(cpu.other) },
  ];
  const mem = memSplit(m);
  const ct = conntrackFill(m);
  const fds = fdFill(m);
  const link = linkUse(m);
  const a = m.agent;
  const b = (n: number) => fmt.bytes(n, 1024);

  return (
    <section className={card}>
      {title}
      <Row label={t("node.m.cpu")} value={[t("res.cpu.busy", { pct: pct(cpu.busy) }), cpus > 0 ? `${cpus} vCPU` : ""].filter(Boolean).join(" · ")}>
        <Bar parts={cpuParts} label={t("common.labeled", { label: t("node.m.cpu"), value: pct(cpu.busy) })} />
        <Legend parts={cpuParts} />
        {cpu.iowait >= 1 && <span className="text-[11px] text-muted">{t("res.cpu.iowait", { pct: pct(cpu.iowait) })}</span>}
      </Row>

      {mem && (
        <Row label={t("res.mem")} value={t("node.m.of", { used: b(m.ramUsedBytes), total: b(m.ramTotalBytes) })}>
          <Bar
            label={t("common.labeled", { label: t("res.mem"), value: pct(mem.used) })}
            parts={[
              { key: "agent", pct: mem.agent, cls: "bg-accent" },
              { key: "other", pct: mem.other, cls: "bg-them" },
            ]}
          />
          <Legend
            parts={[
              { key: "agent", label: t("res.mem.agent"), tip: t("res.mem.agent.tip"), pct: mem.agent, cls: "bg-accent", value: b(a.rssBytes) },
              { key: "other", label: t("res.cpu.other"), tip: t("res.mem.other.tip"), pct: mem.other, cls: "bg-them", value: b(Math.max(0, m.ramUsedBytes - a.rssBytes)) },
            ]}
          />
          <span className="text-[11px] text-muted">
            {m.swapTotalBytes > 0 ? t("res.swap", { used: b(m.swapUsedBytes), total: b(m.swapTotalBytes) }) : t("res.swap.none")}
          </span>
        </Row>
      )}

      <div className="grid gap-3.5 sm:grid-cols-2">
        <Row label={t("res.ct")} tip={t("res.ct.tip")} value={ct === null ? t("res.ct.off") : t("res.of", { n: fmt.num(m.conntrackCount, 0), max: fmt.num(m.conntrackMax, 0) })}>
          {ct !== null && <Meter pct={ct} label={t("common.labeled", { label: t("res.ct"), value: pct(ct) })} />}
        </Row>
        <Row label={t("res.fds")} tip={t("res.fds.tip")} value={fds === null ? fmt.num(a.openFds, 0) : t("res.of", { n: fmt.num(a.openFds, 0), max: fmt.num(a.fdLimit, 0) })}>
          {fds !== null && <Meter pct={fds} label={t("common.labeled", { label: t("res.fds"), value: pct(fds) })} />}
        </Row>
        {link !== null && (
          <Row label={t("res.link")} tip={t("res.link.tip")} value={t("res.of", { n: fmt.mbit(Math.max(m.netRxBps, m.netTxBps)), max: fmt.mbit(m.nicSpeedMbps * 1e6) })}>
            <Meter pct={link} label={t("common.labeled", { label: t("res.link"), value: pct(link) })} />
          </Row>
        )}
        <Row label={t("res.sockets")} value={t("res.sockets.value", { tcp: fmt.num(m.tcpSockets, 0), udp: fmt.num(m.udpSockets, 0) })} />
      </div>

      <p className="border-t border-line pt-3 text-xs leading-relaxed text-muted">
        {t("res.agent", { cpu: pct(a.cpuPct), rss: b(a.rssBytes), heap: b(a.goHeapBytes), goroutines: fmt.num(a.goroutines, 0), threads: fmt.num(a.threads, 0) })}
      </p>
    </section>
  );
}
