import { Link } from "@tanstack/react-router";
import { SectionLabel } from "@/components/ui/bits";
import { StatusDot } from "@/components/ui/status";
import type { Event } from "@/gen/mistgate/admin/v1/fleet_pb";
import { useT } from "@/i18n";
import { describeEvent, eventKind } from "@/lib/events";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";

/**
 * The fleet feed of the Overview: only what answers "what happened" (the server picks it: falls and returns, new nodes,
 * updates, users, every warning). A node's line opens its Events tab, a user's line the user.
 */
export function EventsCard({ events }: { events: readonly Plain<Event>[] }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <section className="flex flex-col gap-1 rounded-card-lg border border-line bg-surface px-4 pt-4 pb-2">
      <SectionLabel as="h2" icon="list" tone="sand" className="pb-1.5">
        {t("ov.events")}
      </SectionLabel>
      {events.length === 0 && <p className="border-t border-line py-3 text-[13px] text-muted">{t("ov.noEvents")}</p>}
      {events.map((e) => {
        const { text } = describeEvent(t, e, fmt.stamp);
        const who = e.userName || e.nodeName;
        const sentence = who ? `${who} · ${text}` : text;
        const row = (
          <>
            <span className="flex h-[18px] flex-none items-center">
              <StatusDot kind={eventKind(e)} />
            </span>
            <span className="line-clamp-2 min-w-0 flex-1 text-[13px] leading-[18px] text-pretty">
              {who && <b className="font-bold">{who}</b>}
              {who && <span className="text-faint"> · </span>}
              <span className="text-muted">{text}</span>
            </span>
            <span className="flex-none font-mono text-[11px] leading-[18px] text-faint">{fmt.stamp(e.timeUnix)}</span>
          </>
        );
        const cls = "flex min-h-10 items-start gap-2.5 border-t border-line py-[11px]";
        if (e.userId)
          return (
            <Link key={e.id} to="/users/$id" params={{ id: e.userId }} title={sentence} className={cls}>
              {row}
            </Link>
          );
        if (e.nodeId)
          return (
            <Link key={e.id} to="/nodes/$id" params={{ id: e.nodeId }} search={{ tab: "events" }} title={sentence} className={cls}>
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
