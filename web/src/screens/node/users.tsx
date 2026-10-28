import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Avatar, Chip, SectionLabel } from "@/components/ui/bits";
import type { IconName, Tone } from "@/components/ui/icons";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { protocolName, protocolShort } from "@/lib/series";

type Online = Plain<GetNodeResponse>["onlineUsers"][number];

/** Rows a card shows before its "All N" opens the rest in place. */
const peopleRows = 6;
const topRows = 5;

/**
 * One row per person: the node reports one entry per connection (a Hysteria2 app can hold several), each with the
 * person's whole rate, so listing them as they come repeats the same name and speed. Protocols and device models are
 * merged, the earliest connection keeps the order.
 */
export function onlinePeople(online: readonly Online[]) {
  const people = new Map<string, { userId: string; userName: string; protocols: string[]; models: string[]; connections: number; downBps: number }>();
  for (const o of online) {
    const p = people.get(o.userId) ?? { userId: o.userId, userName: o.userName, protocols: [], models: [], connections: 0, downBps: o.downBps };
    if (!p.protocols.includes(o.protocol)) p.protocols.push(o.protocol);
    if (o.deviceModel && !p.models.includes(o.deviceModel)) p.models.push(o.deviceModel);
    p.connections++;
    people.set(o.userId, p);
  }
  return [...people.values()];
}

/** A card of the Overview with its heading and, when there are more rows than it shows, "All N" that opens them in place. */
function PeopleCard({ id, icon, tone, title, total, shown, onAll, children }: { id?: string; icon: IconName; tone: Tone; title: ReactNode; total: number; shown: number; onAll: () => void; children: ReactNode }) {
  const t = useT();
  const ref = useRef<HTMLElement>(null);
  // the old Users tab (?tab=users) lands on the Overview at #people
  useEffect(() => {
    if (id && window.location.hash === `#${id}`) ref.current?.scrollIntoView?.({ block: "start" });
  }, [id]);
  return (
    <section ref={ref} id={id} className="flex scroll-mt-4 flex-col gap-2.5 rounded-card-lg border border-line bg-surface p-4">
      <div className="flex items-center gap-2">
        <SectionLabel as="h2" icon={icon} tone={tone} className="flex-1">
          {title}
        </SectionLabel>
        {total > shown && (
          <button type="button" onClick={onAll} className="text-xs font-bold text-muted hover:text-fg">
            {t("node.showAll", { n: total })}
          </button>
        )}
      </div>
      {children}
    </section>
  );
}

/** "On this node now": people online per protocol, then who they are and how fast they go. */
export function OnlineNow({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const offline = node.status === NodeStatus.DOWN || node.status === NodeStatus.BLIP;
  const people = onlinePeople(data.onlineUsers);
  const [all, setAll] = useState(false);
  const shown = all ? people : people.slice(0, peopleRows);
  return (
    <PeopleCard id="people" icon="people" tone="mint" title={t("node.onlineHere")} total={people.length} shown={shown.length} onAll={() => setAll(true)}>
      <div className="flex flex-wrap gap-x-6 gap-y-2 pb-1">
        {node.online.length === 0 && (
          <div className="flex flex-col gap-0.5">
            <span className="font-mono text-[22px] leading-tight font-bold">0</span>
            <span className="text-xs text-muted">{offline ? t("node.onlineOffline") : t("node.onlineNone")}</span>
          </div>
        )}
        {node.online.map((o) => (
          <div key={o.protocol} className="flex flex-col gap-0.5">
            <span className="font-mono text-[22px] leading-tight font-bold">{o.users}</span>
            <span className="text-xs text-muted">{protocolName(o.protocol)}</span>
          </div>
        ))}
      </div>
      {shown.map((u, i) => {
        const detail = [...u.models, u.connections > 1 ? t.n("node.connections", u.connections) : ""].filter(Boolean).join(" · ");
        return (
          <Link key={u.userId} to="/users/$id" params={{ id: u.userId }} className="flex min-h-9 items-center gap-2.5 border-t border-line pt-2.5">
            <Avatar name={u.userName} index={i} />
            <span className="flex min-w-0 flex-1 flex-col">
              <b className="truncate text-[13px]">{u.userName}</b>
              {detail && <span className="truncate text-[11px] text-muted">{detail}</span>}
            </span>
            <span className="flex gap-1">
              {u.protocols.map((p) => (
                <span key={p} title={protocolName(p)}>
                  <Chip mono>{protocolShort(p)}</Chip>
                </span>
              ))}
            </span>
            <span className="w-[84px] text-right font-mono text-xs">{fmt.mbit(u.downBps)}</span>
          </Link>
        );
      })}
    </PeopleCard>
  );
}

/** "Top today": who moved the most through this node since midnight UTC. */
export function TopToday({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const [all, setAll] = useState(false);
  const best = Math.max(1, ...data.topToday.map((u) => u.bytes));
  const shown = all ? data.topToday : data.topToday.slice(0, topRows);
  return (
    <PeopleCard
      icon="traffic"
      tone="sky"
      title={
        <>
          {t("node.topToday")} <span className="font-medium tracking-normal normal-case">· {t("nodes.todayHint")}</span>
        </>
      }
      total={data.topToday.length}
      shown={shown.length}
      onAll={() => setAll(true)}
    >
      {shown.map((u) => (
        <Link key={u.userId} to="/users/$id" params={{ id: u.userId }} className="flex flex-col gap-[5px]">
          <div className="flex text-[13px]">
            <b className="min-w-0 flex-1 truncate">{u.userName}</b>
            <span className="font-mono text-xs">{fmt.bytes(u.bytes)}</span>
          </div>
          <div className="h-1 overflow-hidden rounded-[2px] bg-surface-2">
            <div className="h-full bg-accent" style={{ width: `${Math.max(3, Math.round((u.bytes / best) * 100))}%` }} />
          </div>
        </Link>
      ))}
      {data.topToday.length === 0 && <p className="text-[13px] text-muted">{t("node.noTopToday")}</p>}
    </PeopleCard>
  );
}
