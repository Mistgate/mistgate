import type { CSSProperties } from "react";
import type { NodeMetrics } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { conntrackFill, fdFill, hasResources, linkUse } from "@/lib/resources";
import { CardHead, Glyph } from "@/screens/overview-parts";

// "Resource headroom" (the board's .nd-lm2 card): what can run out on the machine, as rows of an icon, its name, how much is left,
// the number against its limit and a bar: connection tracking, the agent's open files, the link and swap. A row turns amber at 60%
// and red at 85%. The last line says what Mistgate itself takes. Needs the agent's resource fields; without them the card is not drawn.

const paths = {
  conntrack: "M4 6h16M4 12h16M4 18h16M9 6v12M15 6v12",
  files: "M6 3h8l4 4v14H6zM14 3v4h4",
  link: "M4 17l5-5 4 3 7-8M15 7h5v5",
  swap: "M7 7h11l-3-3M17 17H6l3 3",
  doc: "M6 4h12v16H6zM9 8h6M9 12h6M9 16h3",
} as const;

type Row = { key: string; name: string; value: string; of: string; pct: number; icon: string; hue: "" | "sky" | "lav" };

const tint = (pct: number, hue: Row["hue"]) =>
  pct >= 85 ? "bg-danger-soft text-danger" : pct >= 60 ? "bg-warn-soft text-warn" : hue === "sky" ? "bg-tone-sky-bg text-tone-sky" : hue === "lav" ? "bg-tone-lavender-bg text-tone-lavender" : "bg-tone-mint-bg text-accent-mark";
const fill = (pct: number, hue: Row["hue"]) => (pct >= 85 ? "var(--danger)" : pct >= 60 ? "var(--warning)" : hue === "sky" ? "var(--tone-sky)" : hue === "lav" ? "var(--tone-lavender)" : "var(--accent-mark)");

export function NodeLimits({ metrics, bandwidthMbps }: { metrics: Plain<NodeMetrics> | undefined; bandwidthMbps: number }) {
  const t = useT();
  const fmt = useFmt();
  const m = metrics;
  if (!m || !hasResources(m)) return null;
  const a = m.agent;
  const rows: Row[] = [];
  const ct = conntrackFill(m);
  if (ct !== null) rows.push({ key: "ct", name: t("res.ct"), value: fmt.num(m.conntrackCount, 0), of: t("np.lim.of", { max: fmt.num(m.conntrackMax, 0) }), pct: ct, icon: paths.conntrack, hue: "" });
  const fds = fdFill(m);
  if (fds !== null) rows.push({ key: "fd", name: t("res.fds"), value: fmt.num(a.openFds, 0), of: t("np.lim.of", { max: fmt.num(a.fdLimit, 0) }), pct: fds, icon: paths.files, hue: "" });
  // the link: the busier direction against what the owner says the channel is, else against what the driver reports
  const cap = bandwidthMbps > 0 ? bandwidthMbps : m.nicSpeedMbps;
  const lu = cap > 0 ? Math.min(100, (Math.max(m.netRxBps, m.netTxBps) / (cap * 1e6)) * 100) : (linkUse(m) ?? null);
  if (lu !== null && cap > 0) rows.push({ key: "link", name: t("np.res.channel"), value: fmt.mbitNum(Math.max(m.netRxBps, m.netTxBps)), of: t("np.lim.ofMbit", { max: fmt.num(cap, 0) }), pct: lu, icon: paths.link, hue: "sky" });
  if (m.swapTotalBytes > 0) {
    const pct = Math.min(100, (m.swapUsedBytes / m.swapTotalBytes) * 100);
    rows.push({ key: "swap", name: t("np.res.swap"), value: fmt.bytes(m.swapUsedBytes, 1024), of: t("np.lim.of", { max: fmt.bytes(m.swapTotalBytes, 1024) }), pct, icon: paths.swap, hue: "lav" });
  }
  if (rows.length === 0) return null;
  const free = (pct: number) => t("np.lim.free", { pct: `${fmt.num(Math.round((100 - pct) * 10) / 10, 1)}%` });
  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("np.lim.title")} />
      {rows.map((r) => (
        <div key={r.key} className="grid grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5 border-t border-line px-6 pt-3 pb-3.5 max-[720px]:px-4">
          <span aria-hidden className={cx("grid size-8 place-items-center rounded-lg", tint(r.pct, r.hue))}>
            <Glyph d={r.icon} />
          </span>
          <div className="flex min-w-0 flex-col items-start">
            <span className="max-w-full truncate text-sm leading-5">{r.name}</span>
            <em className="text-xs leading-4 text-muted not-italic">{free(r.pct)}</em>
          </div>
          <b className="text-end text-sm font-medium whitespace-nowrap">
            <span className="font-mono tabular-nums">{r.value}</span>
            <small className="block text-[11px] font-normal text-muted">{r.of}</small>
          </b>
          <i aria-hidden className="nd-bar col-start-2 -col-end-1 mt-0.5" style={{ "--w": `${Math.max(1, r.pct)}%`, "--fill": fill(r.pct, r.hue) } as CSSProperties} />
        </div>
      ))}
      <div className="flex items-center gap-3 border-t border-line px-6 py-3 text-[13px] text-muted max-[720px]:px-4">
        <span aria-hidden className="grid size-8 flex-none place-items-center rounded-lg bg-step text-sec">
          <Glyph d={paths.doc} />
        </span>
        <span>
          {t("np.lim.self", { cpu: `${fmt.num(a.cpuPct, a.cpuPct < 10 ? 1 : 0)}%`, rss: fmt.bytes(a.rssBytes, 1024) })}
        </span>
      </div>
    </section>
  );
}
