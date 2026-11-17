import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Avatar } from "@/components/ui/bits";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import type { Plain } from "@/lib/plain";
import { useFmt } from "@/lib/format";
import { protocolName, protocolShort } from "@/lib/series";
import { CardHead } from "@/screens/overview-parts";

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

/** A card of the Overview with its heading and, under the rows, "All N" that opens the rest in place (the board's foot link). */
function PeopleCard({ id, title, count, hint, total, shown, onAll, children }: { id?: string; title: ReactNode; count?: ReactNode; hint?: ReactNode; total: number; shown: number; onAll: () => void; children: ReactNode }) {
  const t = useT();
  const ref = useRef<HTMLElement>(null);
  // the old Users tab (?tab=users) lands on the Overview at #people
  useEffect(() => {
    if (id && window.location.hash === `#${id}`) ref.current?.scrollIntoView?.({ block: "start" });
  }, [id]);
  return (
    <section ref={ref} id={id} className="scroll-mt-4 rounded-card border border-line bg-surface">
      <CardHead title={title} count={count}>
        {hint}
      </CardHead>
      {children}
      {total > shown && (
        <div className="border-t border-line px-6 py-3 text-[13px] max-[720px]:px-4">
          <button type="button" onClick={onAll} className="text-sec underline decoration-(--border) underline-offset-4 hover:text-fg">
            {t("node.showAll", { n: total })}
          </button>
        </div>
      )}
    </section>
  );
}

const rowPad = "px-6 max-[720px]:px-4";

/** "On this node now": who is connected and how fast they go; the people online per protocol sit in the head. */
export function OnlineNow({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const node = data.node!;
  const offline = node.status === NodeStatus.DOWN || node.status === NodeStatus.BLIP;
  const people = onlinePeople(data.onlineUsers);
  const [all, setAll] = useState(false);
  const shown = all ? people : people.slice(0, peopleRows);
  const fastest = Math.max(1, ...people.map((p) => p.downBps));
  return (
    <PeopleCard
      id="people"
      title={t("node.onlineHere")}
      count={people.length || node.online.length ? Math.max(people.length, node.online.reduce((n, o) => n + o.users, 0)) : undefined}
      hint={node.online.length > 0 && <span className="font-mono text-xs text-muted">{node.online.map((o) => `${protocolShort(o.protocol)} ${o.users}`).join(" · ")}</span>}
      total={people.length}
      shown={shown.length}
      onAll={() => setAll(true)}
    >
      {shown.map((u, i) => {
        const detail = [...u.models, ...u.protocols.map(protocolName), u.connections > 1 ? t.n("node.connections", u.connections) : ""].filter(Boolean).join(" · ");
        return (
          <Link key={u.userId} to="/users/$id" params={{ id: u.userId }} className={cx("grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line py-2.5 transition-colors duration-150 hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]", rowPad)}>
            <Avatar name={u.userName} index={i} />
            <div className="min-w-0 overflow-hidden">
              <b className="font-medium">{u.userName}</b>
              <span className="block truncate text-xs text-muted">{detail}</span>
            </div>
            <div className="text-end font-mono text-[13px] tabular-nums">
              {fmt.mbit(u.downBps)}
              <i className="nd-bar thin ms-auto mt-1.5 block w-24" style={{ "--w": `${Math.max(4, Math.round((u.downBps / fastest) * 100))}%`, "--nd-track": "var(--surface-2)" } as CSSProperties} />
            </div>
          </Link>
        );
      })}
      {people.length === 0 && <p className={cx("border-t border-line py-4 text-[13px] text-muted", rowPad)}>{offline ? t("node.onlineOffline") : t("node.onlineNone")}</p>}
    </PeopleCard>
  );
}

/** "Traffic today": who moved the most through this node since midnight UTC. */
export function TopToday({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const fmt = useFmt();
  const [all, setAll] = useState(false);
  const best = Math.max(1, ...data.topToday.map((u) => u.bytes));
  const shown = all ? data.topToday : data.topToday.slice(0, topRows);
  return (
    <PeopleCard title={t("np.today.title")} hint={<span className="text-[13px] text-muted">{t("nodes.todayHint")}</span>} total={data.topToday.length} shown={shown.length} onAll={() => setAll(true)}>
      {shown.map((u) => (
        <Link key={u.userId} to="/users/$id" params={{ id: u.userId }} className={cx("grid grid-cols-[64px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line py-2.5 last:pb-4", rowPad)}>
          <span className="truncate">{u.userName}</span>
          <i className="nd-bar tall" style={{ "--w": `${Math.max(3, Math.round((u.bytes / best) * 100))}%` } as CSSProperties} />
          <b className="font-mono text-[13px] font-medium tabular-nums">{fmt.bytes(u.bytes)}</b>
        </Link>
      ))}
      {data.topToday.length === 0 && <p className={cx("border-t border-line py-4 text-[13px] text-muted", rowPad)}>{t("node.noTopToday")}</p>}
    </PeopleCard>
  );
}
