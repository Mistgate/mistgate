import { Link } from "@tanstack/react-router";
import { Avatar, Chip, SectionLabel } from "@/components/ui/bits";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { protocolName, protocolShort } from "@/lib/series";

const cardCls = "flex flex-col gap-2.5 rounded-card-lg border border-line bg-surface p-4";

type Online = Plain<GetNodeResponse>["onlineUsers"][number];

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

/** "On this node now" and "Top today". */
export function UsersTab({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const best = Math.max(1, ...data.topToday.map((u) => u.bytes));
  return (
    <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)]">
      <section className={cardCls}>
        <SectionLabel as="h2" icon="people" tone="mint">
          {t("node.onlineHere")}
        </SectionLabel>
        {onlinePeople(data.onlineUsers).map((u, i) => {
          const detail = [...u.models, u.connections > 1 ? t.n("node.connections", u.connections) : ""].filter(Boolean).join(" · ");
          return (
            <Link key={u.userId} to="/users/$id" params={{ id: u.userId }} className="flex min-h-9 items-center gap-2.5">
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
              <span className="w-[90px] text-right font-mono text-xs">{fmt.mbit(u.downBps)}</span>
            </Link>
          );
        })}
        {data.onlineUsers.length === 0 && <p className="text-[13px] text-muted">{t("node.nobody")}</p>}
      </section>
      <section className={cardCls}>
        <SectionLabel as="h2" icon="traffic" tone="sky">
          {t("node.topToday")} <span className="font-medium tracking-normal normal-case">· {t("nodes.todayHint")}</span>
        </SectionLabel>
        {data.topToday.map((u) => (
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
      </section>
    </div>
  );
}
