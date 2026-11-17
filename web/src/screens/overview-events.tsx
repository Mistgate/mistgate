import { Link } from "@tanstack/react-router";
import type { Event } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { describeEvent, eventKind, userAnchorOf } from "@/lib/events";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { CardHead, OvDot } from "./overview-parts";

/**
 * The fleet feed of the Overview: only what answers "what happened" (the server picks it: falls and returns, new nodes,
 * updates, users, every warning). A node's line opens its Events tab, a user's line the user (the logs of their app: down to the "Logs for support" of their devices).
 */
export function EventsCard({ events }: { events: readonly Plain<Event>[] }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <section className="rounded-card border border-line bg-surface">
      <CardHead title={t("ov.events")} />
      {events.length === 0 && <p className="border-t border-line px-6 py-3 text-[13px] text-muted max-md:px-4">{t("ov.noEvents")}</p>}
      {events.map((e) => {
        const { text } = describeEvent(t, e, fmt.stamp);
        const who = e.userName || e.nodeName;
        const sentence = who ? `${who} · ${text}` : text;
        const row = (
          <>
            <span className="font-mono text-xs font-medium whitespace-nowrap text-muted tabular-nums">{fmt.stamp(e.timeUnix)}</span>
            <span className="flex items-center self-center">
              <OvDot kind={eventKind(e)} />
            </span>
            <span className="line-clamp-2 min-w-0 text-pretty">
              {who && <b className="me-1.5 font-mono font-medium">{who}</b>}
              <span className="text-sec">{text}</span>
            </span>
          </>
        );
        const cls = "grid animate-[mg-up_0.35s_var(--e)_both] grid-cols-[minmax(44px,auto)_8px_minmax(0,1fr)] items-baseline gap-3 border-t border-line px-6 py-2.5 max-md:px-4";
        if (e.userId)
          return (
            <Link key={e.id} to="/users/$id" params={{ id: e.userId }} hash={userAnchorOf(e.code)} title={sentence} className={cls}>
              {row}
            </Link>
          );
        if (e.nodeId)
          return (
            <Link key={e.id} to="/nodes/$id" params={{ id: e.nodeId }} search={{ tab: "history" }} title={sentence} className={cls}>
              {row}
            </Link>
          );
        return (
          <div key={e.id} title={sentence} className={cls}>
            {row}
          </div>
        );
      })}
    </section>
  );
}
