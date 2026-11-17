import { queryOptions, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Icon } from "@/components/ui/icons";
import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { useT } from "@/i18n";
import { health } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { alertTitle, groupFact, resolutionWord, severityWord, type Alert } from "@/lib/health";
import { plain } from "@/lib/plain";
import { pollMs } from "@/lib/queries";
import { useNow } from "@/lib/time";
import { AlertBody } from "@/screens/health/alerts";
import { useFixFlow, type FixFlow } from "@/screens/health/fix";
import { CardFoot, CardHead, Glyph, glyphs, OvDot } from "@/screens/overview-parts";

/** The alerts of one node, open and of the last 7 days; under "health", so a mute or a fix refreshes it with the Health page. */
export const nodeAlertsQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["health", "alerts", nodeId],
    queryFn: async ({ signal }) => plain(await health.listAlerts({ nodeId }, { signal })),
    refetchInterval: pollMs,
  });

/** The rows a node's list shows before "Show N more". */
const rows = 4;

// the glyph of a row: a triangle in amber for a warning, a ring in red for a critical one, a spark in sky for a note
const glyphOf = (s: AlertSeverity) => (s === AlertSeverity.CRITICAL ? glyphs.bad : s === AlertSeverity.INFO ? glyphs.ai : glyphs.warn);
const glyphTone = (s: AlertSeverity) =>
  s === AlertSeverity.CRITICAL
    ? "bg-danger-soft text-danger"
    : s === AlertSeverity.INFO
      ? "bg-tone-sky-bg text-tone-sky"
      : "bg-[color-mix(in_srgb,var(--warning)_16%,var(--surface))] text-warn";

/** One alert of the node (the board's .nd-al): the glyph, what and a line under it, its age and a chevron; it opens into what to do. */
function AlertRow({ a, now, flow, open, onToggle }: { a: Alert; now: number; flow: FixFlow; open: boolean; onToggle: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const fact = groupFact(t, [a]);
  const age = fmt.duration(Math.max(0, now - (a.openedUnix || a.firstSeenUnix)));
  const body = `alert-body-${a.id}`;
  return (
    <div id={`alert-${a.id}`} className={cx("scroll-mt-20 border-t border-line transition-colors duration-200", open && "bg-step")}>
      <h3>
        <button
          type="button"
          aria-expanded={open}
          aria-controls={body}
          onClick={onToggle}
          className="grid w-full grid-cols-[32px_minmax(0,1fr)_auto_16px] items-center gap-3 px-6 py-3 text-start max-[720px]:grid-cols-[32px_minmax(0,1fr)_16px] max-[720px]:px-4"
        >
          <span aria-hidden className={cx("grid size-8 place-items-center rounded-lg", glyphTone(a.severity))}>
            <Glyph d={glyphOf(a.severity)} />
          </span>
          <span className="flex min-w-0 flex-col">
            <b className="truncate font-medium max-[720px]:whitespace-normal">{alertTitle(t, a)}</b>
            <span className="truncate text-[13px] text-sec max-[720px]:whitespace-normal">{fact || t(severityWord(a.severity))}</span>
          </span>
          <span className="font-mono text-xs font-normal whitespace-nowrap text-muted max-[720px]:hidden">{age}</span>
          <Icon name="chevronDown" size={16} aria-hidden className={cx("text-muted transition-transform duration-[250ms] ease-out", open && "rotate-180")} />
        </button>
      </h3>
      {open && (
        <div id={body} className="nd-alb px-6 pb-4 ps-[68px] max-[720px]:px-4 [&_p]:max-w-[640px]">
          <AlertBody a={a} now={now} flow={flow} onNodePage part="fold" />
        </div>
      )}
    </div>
  );
}

/** "Needs you on de1": what is open on this node now, with the buttons of the Health page, or "All quiet" when nothing is. */
export function NodeAlerts({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const t = useT();
  const q = useQuery(nodeAlertsQuery(nodeId));
  const flow = useFixFlow();
  const clock = useNow();
  const [all, setAll] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  if (!q.data) return null;
  const now = q.data.nowUnix || Math.floor(clock / 1000);
  const active = q.data.active;
  const shown = all ? active : active.slice(0, rows);
  // nothing chosen: a critical one starts open; a link to an alert opens that alert
  const linked = typeof window === "undefined" ? "" : window.location.hash.replace(/^#alert-/, "");
  const isOpen = (a: Alert) => (open === null ? a.id === linked || (a.severity === AlertSeverity.CRITICAL && !linked) : open === a.id);
  return (
    <section className="overflow-hidden rounded-card border border-line bg-surface">
      <CardHead title={t("np.alerts.title", { name: nodeName })} count={active.length > 0 ? active.length : undefined}>
        {active.length > 0 && (
          <Link to="/health" className="text-[13px] text-sec underline decoration-(--border) underline-offset-4 hover:text-fg">
            {t("node.alertsAll")}
          </Link>
        )}
      </CardHead>
      {shown.map((a) => (
        <AlertRow key={a.id} a={a} now={now} flow={flow} open={isOpen(a)} onToggle={() => setOpen(isOpen(a) ? "" : a.id)} />
      ))}
      {active.length > rows && (
        <button
          type="button"
          onClick={() => setAll(!all)}
          className="flex h-11 w-full items-center justify-center gap-2 border-t border-line text-[13px] text-sec transition-[color,background-color] duration-150 ease-out hover:bg-step hover:text-fg"
        >
          {all ? t("np.alerts.fold") : t("np.alerts.more", { n: active.length - rows })}
          <Icon name="chevronDown" size={16} aria-hidden className={cx("transition-transform duration-[250ms] ease-out", all && "rotate-180")} />
        </button>
      )}
      {active.length === 0 && (
        <div className="grid grid-cols-[40px_minmax(0,1fr)] items-center gap-3 border-t border-line px-6 pt-4 pb-5 max-[720px]:px-4">
          <span aria-hidden className="grid size-10 place-items-center rounded-full bg-tone-mint-bg text-accent-mark">
            <Icon name="check" size={20} />
          </span>
          <div>
            <b className="block font-medium">{t("np.alerts.calm")}</b>
            <span className="text-[13px] text-sec">{t("np.alerts.calmBody")}</span>
          </div>
        </div>
      )}
      {flow.modal}
    </section>
  );
}

/** "Recently closed": the last three alerts of this node that closed, as lines of time, a dot and what it was. */
export function NodeClosed({ nodeId }: { nodeId: string }) {
  const t = useT();
  const fmt = useFmt();
  const q = useQuery(nodeAlertsQuery(nodeId));
  if (!q.data) return null;
  const closed = q.data.history.slice(0, 3);
  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("node.alertsClosed")} />
      {closed.length === 0 && <p className="border-t border-line px-6 py-4 text-[13px] text-muted max-[720px]:px-4">{t("np.closed.none")}</p>}
      {closed.map((h) => (
        <div key={`${h.id}/${h.resolvedAtUnix}`} className="grid animate-[mg-up_0.35s_var(--e)_both] grid-cols-[minmax(44px,auto)_8px_minmax(0,1fr)] items-baseline gap-3 border-t border-line px-6 py-2.5 max-[720px]:px-4">
          <span className="font-mono text-xs font-medium whitespace-nowrap text-muted tabular-nums">{fmt.stamp(h.resolvedAtUnix)}</span>
          <span className="flex items-center self-center">
            <OvDot kind="blip" />
          </span>
          <div className="flex min-w-0 flex-col">
            <b className="me-1.5 font-mono font-medium">{alertTitle(t, h)}</b>
            <span className="text-sec">
              {t("hl.alerts.lasted", { duration: fmt.duration(Math.max(0, h.resolvedAtUnix - (h.openedUnix || h.firstSeenUnix))) })} · {t(resolutionWord(h.resolution))}
            </span>
          </div>
        </div>
      ))}
      <CardFoot>
        <Link to="/nodes/$id" params={{ id: nodeId }} search={{ tab: "history" }} className="text-sec underline decoration-(--border) underline-offset-4 hover:text-fg">
          {t("np.closed.history")}
        </Link>
      </CardFoot>
    </section>
  );
}
