import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { playWave } from "@/components/fx/wave";
import { Scramble } from "@/components/fx/scramble-react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { agentLinked, nodeKind } from "@/lib/node-status";
import { countNodes, healthKind, problemCount } from "@/lib/fleet";
import { scaleBytes, minutesText, useFmt } from "@/lib/format";
import { fixLabel } from "@/lib/health";
import { tail, toSeries } from "@/lib/series";
import { useNow } from "@/lib/time";
import type { FixFlow } from "./health/fix";
import { glyphs, Glyph, OvDot } from "./overview-parts";
import type { TodoAction, TodoItem } from "./overview-todo";
import type { OverviewResponse } from "./overview-types";

// The first card of the Overview: the state of the fleet in a sentence and three numbers, and, beside it, what needs the owner.

/** "checked 10 s ago": the age of the last answer, in seconds up to a minute. */
function checkedAgo(t: T, fmt: ReturnType<typeof useFmt>, updatedAt: number, now: number): string {
  const sec = Math.max(0, Math.floor((now - updatedAt) / 1000));
  if (sec < 5) return t("common.justNow");
  if (sec < 60) return t("ov.hero.agoS", { n: sec });
  return fmt.ago(Math.floor(updatedAt / 1000), now);
}

const SHOWN = 4;

export function Hero({
  data,
  usersTotal,
  items,
  flow,
  updatedAt,
  everyMin,
}: {
  data: OverviewResponse;
  usersTotal: number | null;
  items: TodoItem[];
  flow: FixFlow;
  /** ms: when the overview last arrived. */
  updatedAt: number;
  /** Minutes between the panel's check rounds. */
  everyMin: number;
}) {
  const t = useT();
  const fmt = useFmt();
  const now = useNow(5000);
  const root = useRef<HTMLElement>(null);
  const cards = data.nodes;
  const counts = countNodes(cards);
  const problems = problemCount({ problems: counts.problems, alerts: data.alertsActive });
  const kind = counts.connected === 0 ? "off" : healthKind(counts.broken, problems, data.alertsCritical);
  const up = cards.filter((n) => agentLinked(n.status)).length;

  // the end of the sentence: what is wrong, else what waits, else that all is calm
  const blips = cards.filter((n) => nodeKind(n) === "blip");
  const updating = cards.filter((n) => nodeKind(n) === "busy");
  const tailText =
    problems > 0
      ? t.n("ov.problems", problems)
      : counts.pending > 0
        ? t.n("ov.tail.pending", counts.pending)
        : blips.length
          ? t("ov.tail.blip", { names: blips.map((n) => n.name).join(", ") })
          : updating.length
            ? t.n("ov.tail.updating", updating.length, { names: updating.map((n) => n.name).join(", ") })
            : t("ov.hero.calm");
  const word = { ok: "ov.hero.st.ok", warn: "ov.hero.st.warn", bad: "ov.hero.st.bad", off: "ov.hero.st.off" } as const;

  // the three numbers
  const speed = cards.reduce((a, n) => a + n.downBps, 0);
  const hours = tail(toSeries(data.traffic), 24);
  const total = hours.total.reduce((a, b) => a + b, 0);
  const peakAt = hours.total.indexOf(Math.max(...hours.total));
  const size = scaleBytes(total);
  const peakTime = hours.times[peakAt] !== undefined ? fmt.clock(hours.times[peakAt]!) : "";
  const onNodes = cards.filter((n) => n.online.some((o) => o.users > 0)).length;
  const stats = [
    {
      k: t("ov.stat.online"),
      v: String(data.usersOnline),
      small: usersTotal != null ? t("ov.stat.of", { n: usersTotal }) : "",
      n: data.usersOnline > 0 ? t.n("ov.stat.onNodes", onNodes) : t("ov.onlineNone"),
    },
    { k: t("ov.stat.speed"), v: fmt.num(speed / 1e6, speed >= 1e7 ? 0 : 1), small: t("unit.mbit"), n: t("ov.stat.speedNote") },
    {
      k: t("ov.stat.traffic"),
      v: fmt.num(size.value, size.unit === 0 || size.value >= 100 ? 0 : 1),
      small: t(`unit.${(["B", "KB", "MB", "GB", "TB"] as const)[size.unit]}`),
      n: total > 0 ? t("ov.stat.peak", { time: peakTime }) : t("ov.stat.noTraffic"),
    },
  ];

  // a new row slides in; the first drawing and the rows that were there do not move ("known" catches up a second after a change)
  const [known, setKnown] = useState<ReadonlySet<string> | null>(null);
  const fresh = new Set(known ? items.filter((i) => !known.has(i.id)).map((i) => i.id) : []);
  useEffect(() => {
    const id = setTimeout(() => setKnown(new Set(items.map((i) => i.id))), 1000);
    return () => clearTimeout(id);
  }, [items]);
  // the last open row left because a fix of it finished: the wave runs from the card (with the effects on)
  const fixed = useRef(false);
  const before = useRef(items.length);
  useEffect(() => {
    if (before.current > 0 && items.length === 0 && fixed.current && root.current) {
      const r = root.current.getBoundingClientRect();
      playWave({ x: r.left + r.width * (document.documentElement.dir === "rtl" ? 0.18 : 0.82), y: r.top + r.height * 0.5 }, { light: document.documentElement.dataset.theme === "light" });
      fixed.current = false;
    }
    before.current = items.length;
  }, [items.length]);

  return (
    <section
      ref={root}
      className="ov-hero relative grid overflow-hidden rounded-card border border-line bg-surface"
    >
      <div className="flex min-w-0 flex-col justify-between gap-10 px-8 pt-7 pb-8 max-md:gap-6 max-md:p-5">
        <div>
          <span className="flex flex-wrap items-center gap-2 text-[13px] text-sec">
            <OvDot kind={kind} className={cx(kind === "bad" && "ov-crit-dot")} />
            <b className="font-medium text-fg">{t(word[kind])}</b>
            {` · ${t("ov.hero.checked", { ago: checkedAgo(t, fmt, updatedAt, now) })}`}
          </span>
          <h2 className="mt-3 text-[32px] leading-9 font-medium tracking-[-0.02em] max-md:text-2xl max-md:leading-7">
            {up > 0 ? t.n("ov.hero.up", up) : t("ov.hero.none")}, <span className={kind === "bad" ? "text-danger" : "text-sec"}>{tailText}</span>
          </h2>
        </div>
        <div className="grid grid-cols-3 max-md:grid-cols-1 max-md:gap-3">
          {stats.map((s) => (
            <div
              key={s.k}
              className="flex flex-col gap-1 border-s border-line ps-6 first:border-s-0 first:ps-0 max-md:grid max-md:grid-cols-[1fr_auto] max-md:items-baseline max-md:border-s-0 max-md:border-t max-md:pt-3 max-md:ps-0 max-md:first:border-t-0 max-md:first:pt-0"
            >
              <span className="text-[13px] whitespace-nowrap text-muted">{s.k}</span>
              <span className="text-[28px] leading-8 font-medium whitespace-nowrap tabular-nums max-md:text-[22px] max-md:leading-7">
                <Scramble value={s.v} className="inline-block font-[inherit] tabular-nums" />
                {s.small && (
                  <>
                    {" "}
                    <small className="text-sm font-normal text-sec">{s.small}</small>
                  </>
                )}
              </span>
              <span className="text-[13px] whitespace-nowrap text-sec max-md:col-span-full">{s.n}</span>
            </div>
          ))}
        </div>
      </div>

      <div className="ov-todo flex flex-col bg-step">
        <div className="flex items-center justify-between gap-2 px-6 pt-5 pb-3 text-base leading-6 font-medium max-md:px-4">
          <span>
            {t("ov.todo.title")} {items.length > 0 && <span className="font-normal text-muted">{items.length}</span>}
          </span>
          <Link to="/health" className="text-[13px] font-normal text-sec underline decoration-(--border) underline-offset-4 hover:text-fg">
            {t("ov.todo.all")}
          </Link>
        </div>
        {items.length > 0 && (
          <ul className="flex flex-col">
            {items.slice(0, SHOWN).map((i) => (
              <Row key={i.id} item={i} flow={flow} fresh={fresh.has(i.id)} onDone={() => (fixed.current = true)} />
            ))}
            {items.length > SHOWN && (
              <li className="list-none">
                <Link to="/health" className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-6 py-2.5 text-[13px] text-sec transition-colors duration-150 hover:bg-step hover:text-fg max-md:px-4">
                  <span>{t("ov.todo.more", { n: items.length - SHOWN })}</span>
                  <Icon name="chevronRight" size={16} />
                </Link>
              </li>
            )}
          </ul>
        )}
        {items.length === 0 && (
          <div className="ov-pop flex flex-1 flex-col items-center justify-center gap-2 border-t border-line p-6 text-center text-sec">
            <span className="mb-1 grid size-12 place-items-center rounded-full bg-tone-mint-bg text-tone-mint">
              <Glyph d={glyphs.check} size={20} stroke={1.5} />
            </span>
            <b className="text-base leading-6 font-medium text-fg">{t("ov.todo.calm")}</b>
            <span>{t("ov.todo.calmBody", { every: minutesText(t, everyMin) })}</span>
          </div>
        )}
      </div>
    </section>
  );
}

const tileGlyph = { bad: glyphs.bad, warn: glyphs.warn, ai: glyphs.ai } as const;

function Row({ item, flow, fresh, onDone }: { item: TodoItem; flow: FixFlow; fresh: boolean; onDone: () => void }) {
  const t = useT();
  const phase = item.action.kind === "fix" ? flow.phase(item.action.target.key) : "idle";
  useEffect(() => {
    if (phase === "done") onDone();
  }, [phase, onDone]);
  return (
    <li
      className={cx(
        "ov-td grid list-none grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-6 py-3 max-md:grid-cols-[32px_minmax(0,1fr)] max-md:px-4",
        fresh && "inn",
        phase === "done" && "out",
        phase === "busy" && "opacity-60",
      )}
    >
      <span aria-hidden className={cx("ov-g", item.tone)}>
        <Glyph d={phase === "done" ? glyphs.check : tileGlyph[item.tone]} />
      </span>
      <div className="min-w-0">
        <b className="block truncate font-medium max-md:whitespace-normal">
          {item.node && <span className="me-1 font-mono font-medium text-sec">{item.node}</span>}
          {item.title}
        </b>
        {item.sub && (
          <span className="block truncate text-[13px] text-sec max-md:whitespace-normal" title={item.sub}>
            {item.sub}
          </span>
        )}
      </div>
      {phase === "busy" ? (
        <span role="status" aria-label={t("hl.fix.fixing")} className="mx-2 size-4 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-tone-sky border-t-transparent" />
      ) : phase === "idle" ? (
        <Act action={item.action} flow={flow} />
      ) : null}
    </li>
  );
}

const cls = cx(buttonClass("secondary", "md"), "max-md:col-start-2 max-md:justify-self-start");

function Act({ action, flow }: { action: TodoAction; flow: FixFlow }) {
  const t = useT();
  switch (action.kind) {
    case "fix":
      return (
        <Button variant="secondary" size="md" className="max-md:col-start-2 max-md:justify-self-start" onClick={() => flow.start(action.target)}>
          {fixLabel(t, action.target.fixId, action.target.profile)}
        </Button>
      );
    case "node":
      return action.tab ? (
        <Link to="/nodes/$id" params={{ id: action.id }} search={{ tab: action.tab }} className={cls}>
          {action.label}
        </Link>
      ) : (
        <Link to="/nodes/$id" params={{ id: action.id }} className={cls}>
          {action.label}
        </Link>
      );
    case "health":
      return (
        <Link to="/health" className={cls}>
          {action.label}
        </Link>
      );
    case "approvals":
      return (
        <Link to="/integrations" className={cls}>
          {action.label}
        </Link>
      );
  }
}
