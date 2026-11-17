import { useQuery } from "@tanstack/react-query";
import { meQuery } from "@/lib/session";
import { Link } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill } from "@/components/ui/status";
import { DoctorStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { itemDetail, itemParams } from "@/lib/doctor-detail";
import { useFmt } from "@/lib/format";
import { doctorKind, doctorQuery, doctorWord, groupDoctor, itemTitle, useCan, type DoctorItem } from "@/lib/health";
import { checkMarks, ManualSteps, useAcceptDoctor, useDoctorItem, useRunDoctor } from "@/screens/health/doctor-parts";
import { useFixFlow, type FixFlow } from "@/screens/health/fix";
import { Glyph, glyphs } from "@/screens/overview-parts";
import { Spin, Wa } from "./parts";

// The Doctor tab (the board's .dr): one card that says how the node is (an icon, a sentence, a cell per check that fills while the node
// is looked at) and runs the inspection; what needs attention as cards with the reason, the fact and what can be done; what is fine,
// what was accepted as normal and what was not checked as quiet lists. The checks come from the node's stored report: nothing here
// calls the node except "Check again".

const shield = "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6zM9 12l2 2 4-4";
const pulse = "M3 12h4l3-7 4 14 3-7h4";
const shieldPlain = "M12 3l7 3v5c0 4.5-3 8-7 10-4-2-7-5.5-7-10V6z";

type Tone = "ok" | "warn" | "crit" | "idle" | "run";

const iconTone: Record<Tone, string> = {
  ok: "bg-tone-mint-bg text-accent-mark",
  warn: "bg-warn-soft text-warn",
  crit: "bg-danger-soft text-danger",
  idle: "bg-step text-sec",
  run: "bg-tone-sky-bg text-tone-sky",
};

/** The name of a check's family, for the line under a problem's title. */
function useCategory() {
  const t = useT();
  return (id: string) => t(`np.doc.cat.${familyOf(id)}`);
}

/** The three groups of the board: the server (its disk, memory, clock), the network, and Mistgate itself (certificates, the agent, the engines). */
const familyOf = (id: string): "server" | "net" | "mg" => {
  const tone = checkMarks[id]?.tone;
  return tone === "sand" ? "server" : tone === "sky" ? "net" : "mg";
};
const families = ["server", "net", "mg"] as const;

/** What needs attention: the title and its family, how bad it is, why, the node's own fact, and what can be done. */
function IssueCard({ flow, nodeId, nodeName, item, stale }: { flow: FixFlow; nodeId: string; nodeName: string; item: DoctorItem; stale?: string }) {
  const { t, can, accept, open, why, fact, command, steps, phase, canAccept, who, action } = useDoctorItem({ flow, nodeId, nodeName, item, stale });
  const category = useCategory();
  const crit = item.status === DoctorStatus.FAIL;
  const raw = fact || item.detail;
  return (
    <section
      title={stale ? t("hl.doctor.stale", { age: stale }) : undefined}
      className={cx(
        "flex animate-[mg-up_0.3s_var(--e)_both] flex-col gap-3 rounded-card border bg-surface px-6 py-5 max-[720px]:p-4",
        crit ? "border-[color-mix(in_srgb,var(--danger)_55%,transparent)]" : "border-[color-mix(in_srgb,var(--warning)_40%,transparent)]",
        stale && "opacity-60",
      )}
    >
      <div className="grid grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3">
        <span aria-hidden className={cx("grid size-9 place-items-center rounded-[10px]", crit ? "bg-danger-soft text-danger" : "bg-warn-soft text-warn")}>
          <Glyph d={crit ? glyphs.bad : glyphs.warn} />
        </span>
        <div className="min-w-0">
          <h4 className="text-base font-medium">{itemTitle(t, item)}</h4>
          <span className="text-xs text-muted">{category(item.id)}</span>
        </div>
        {phase === "idle" && <StatusPill kind={doctorKind(item.status)} label={t(doctorWord(item.status))} />}
      </div>
      {why && <p className="max-w-[640px] text-sm leading-5 text-sec">{why}</p>}
      {raw && <div className="self-start rounded-md bg-step px-2.5 py-1 font-mono text-xs font-medium text-sec">{raw}</div>}
      {command && <span className="text-xs leading-snug text-muted">{t("hl.doctor.runCommand")}</span>}
      {(action || canAccept) && (
        <div className="mt-auto flex flex-wrap items-center gap-x-3 gap-y-2 pt-1">
          {action}
          {canAccept && (
            <button
              type="button"
              disabled={accept.busy}
              title={t("hl.doctor.acceptHint")}
              onClick={() => accept.run(who, true)}
              className="ms-auto text-[13px] text-sec underline-offset-[3px] transition-colors duration-150 hover:text-fg hover:underline disabled:opacity-50 max-[720px]:ms-0"
            >
              {t("hl.doctor.accept")}
            </button>
          )}
        </div>
      )}
      {open && steps.length > 0 && <ManualSteps steps={steps} nodeId={nodeId} />}
      {!can.run && !action && !steps.length && <span className="text-xs text-muted">{t("hl.doctor.manual")}</span>}
    </section>
  );
}

/** One line of a quiet list: a mark, the title, the node's fact in words and in the agent's own; accepted ones say by whom and bring it back. */
function QuietRow({ nodeId, nodeName, item, mute, skipped, fresh }: { nodeId: string; nodeName: string; item: DoctorItem; mute?: boolean; skipped?: boolean; fresh?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const accept = useAcceptDoctor();
  const me = useQuery(meQuery).data?.admin?.id ?? "";
  const params = itemParams(t, fmt, item);
  const fact = skipped ? t("np.doc.notChecked") : itemDetail(t, item, params) ?? "";
  const raw = skipped ? "" : item.detail;
  return (
    <li
      className={cx(
        "grid min-h-11 grid-cols-[20px_minmax(160px,240px)_minmax(0,1fr)_auto_auto] items-center gap-3 px-6 transition-[opacity,background-color] duration-200 ease-out max-[1200px]:grid-cols-[20px_minmax(140px,220px)_minmax(0,1fr)_auto] max-[720px]:grid-cols-[20px_minmax(0,1fr)_auto] max-[720px]:gap-x-2.5 max-[720px]:gap-y-0.5 max-[720px]:px-4 max-[720px]:py-2",
        "[&:not(:first-child)]:border-t [&:not(:first-child)]:border-line",
        fresh && "nd-dr-fresh",
      )}
    >
      <span aria-hidden className={cx("grid size-5 place-items-center rounded-full", mute || skipped ? "bg-step text-muted" : "bg-tone-mint-bg text-accent-mark")}>
        <Icon name="check" size={12} strokeWidth={2.5} />
      </span>
      <b className={cx("truncate font-medium", (mute || skipped) && "text-sec")}>{itemTitle(t, item)}</b>
      <span className="truncate text-[13px] text-sec max-[720px]:col-start-2 max-[720px]:row-start-2 max-[720px]:whitespace-normal">
        {item.acceptedUnix > 0 ? (item.acceptedBy === me ? t("hl.doctor.acceptedByYou", { date: fmt.date(item.acceptedUnix) }) : t("hl.doctor.acceptedBy", { name: item.acceptedByName || item.acceptedBy, date: fmt.date(item.acceptedUnix) })) : fact}
      </span>
      {raw ? <span className="max-w-80 truncate text-end font-mono text-[11px] text-muted max-[1200px]:hidden">{raw}</span> : <span className="max-[1200px]:hidden" />}
      {mute && can.run ? (
        <button
          type="button"
          disabled={accept.busy}
          onClick={() => accept.run({ nodeId, nodeName, checkId: item.id }, false)}
          className="text-[13px] text-sec underline decoration-(--border) underline-offset-4 hover:text-fg disabled:opacity-50 max-[720px]:col-start-3 max-[720px]:row-span-2 max-[720px]:row-start-1"
        >
          {t("hl.doctor.unaccept")}
        </button>
      ) : (
        <span />
      )}
    </li>
  );
}

function Group({ title, count, children }: { title: string; count: number; children: ReactNode }) {
  return (
    <>
      <div className="mx-1 mt-3 flex items-baseline gap-2.5">
        <h4 className="text-sm font-medium">{title}</h4>
        <span className="text-xs text-muted">{count}</span>
      </div>
      <section className="rounded-card border border-line bg-surface py-1">
        <ul>{children}</ul>
      </section>
    </>
  );
}

/** This node's doctor: its checks as the board draws them (a summary with a cell per check, problems as cards, the rest as quiet lists). */
export function NodeDoctorTab({ nodeId, nodeName }: { nodeId: string; nodeName: string }) {
  const t = useT();
  const fmt = useFmt();
  const can = useCan();
  const q = useQuery(doctorQuery(nodeId));
  const run = useRunDoctor(nodeId);
  const flow = useFixFlow();
  const node = q.data?.nodes.find((n) => n.nodeId === nodeId);
  const card = useRef<HTMLElement>(null);
  const running = run.isPending;
  const wasRunning = useRef(false);
  // the ids of the problems before a run: one that is fine afterwards gets the quiet wash that fades
  const before = useRef<Set<string>>(new Set());
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const items = useMemo(() => node?.items ?? [], [node]);
  const n = items.length;

  // while the node is looked at one cell after another takes the colour of its check; the last waits for the answer
  const started = useRef(0);
  const [turn, setTurn] = useState(1);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => setTurn(Math.min(Math.max(1, n), 1 + Math.floor((Date.now() - started.current) / 320))), 160);
    return () => clearInterval(id);
  }, [running, n]);
  const step = running ? turn : 0;

  // the answer is in: the halo of a save plays over the card, and what came back fine is marked
  useEffect(() => {
    if (running) {
      wasRunning.current = true;
      before.current = new Set(groupDoctor(items).issues.map((i) => i.id));
      return;
    }
    if (!wasRunning.current) return;
    wasRunning.current = false;
    halo("save", { target: card.current });
    const now = new Set(groupDoctor(items).issues.map((i) => i.id));
    setFresh(new Set([...before.current].filter((id) => !now.has(id))));
    const timer = setTimeout(() => setFresh(new Set()), 2000);
    return () => clearTimeout(timer);
  }, [running, items]);

  if (!node) return q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <Pending />;

  if (!node.agentSupported)
    return (
      <div className="rounded-card border border-dashed border-line">
        <EmptyState
          title={t("hl.doctor.tooOld.title")}
          action={
            <Link to="/updates" className={buttonClass("secondary", "lg")}>
              {t("hl.doctor.openUpdates")}
            </Link>
          }
        >
          {t("hl.doctor.tooOld.body")}
        </EmptyState>
      </div>
    );

  const g = groupDoctor(items);
  const offline = node.nodeStatus !== NodeStatus.ONLINE && node.nodeStatus !== NodeStatus.NO_TRAFFIC;
  const never = !node.hasReport;
  const crit = g.issues.some((i) => i.status === DoctorStatus.FAIL);
  const tone: Tone = running ? "run" : never ? "idle" : offline || crit ? "crit" : g.issues.length > 0 ? "warn" : "ok";
  const cat = items.length;
  const okN = cat - g.issues.length;
  const when = fmt.ago(node.receivedUnix);
  const title = running
    ? t("np.doc.running")
    : never
      ? t("np.doc.never")
      : offline
        ? t("np.doc.last", { time: fmt.clock(node.receivedUnix) })
        : crit
          ? t("np.doc.crit", { what: itemTitle(t, g.issues.find((i) => i.status === DoctorStatus.FAIL)!) })
          : g.issues.length > 0
            ? t.n("np.doc.bad", g.issues.length)
            : t("np.doc.ok");
  const sub = running ? t("np.doc.runSub", { n: Math.max(n, 14) }) : never ? t("np.doc.neverSub") : [t("np.doc.okOf", { ok: okN, n: cat }), offline ? t("np.doc.offline") : t("hl.doctor.checkedAgo", { ago: when })].join(" · ");
  const icon = tone === "run" ? pulse : tone === "ok" ? shield : tone === "idle" ? shieldPlain : glyphs.warn;
  // the cells: the colour of each check; while it runs the ones passed keep what they were, the one in turn is sky, the rest wait
  const cellKind = (i: DoctorItem) => (i.status === DoctorStatus.FAIL && !i.acceptedUnix ? "crit" : i.status === DoctorStatus.WARN && !i.acceptedUnix ? "warn" : i.acceptedUnix || i.status === DoctorStatus.SKIP ? "mute" : "");
  const cells = (never && !running ? Array.from({ length: 14 }) : items).map((it, i) => {
    const item = it as DoctorItem | undefined;
    const cls = running ? (i < step - 1 ? (item ? cellKind(item) : "") : i === step - 1 ? "now" : "wait") : never || !item ? "wait" : cellKind(item);
    return { cls, label: item ? itemTitle(t, item) : "" };
  });
  const stale = node.stale ? fmt.duration(node.ageS) : undefined;
  const staleCls = node.stale || offline;

  return (
    <div className="flex flex-col gap-3">
      <section ref={card} className={cx("nd-dr-sum rounded-card border border-line bg-surface", tone === "ok" && "ok", tone, "grid grid-cols-[48px_minmax(0,1fr)_auto] items-center gap-4 px-6 py-5 max-[720px]:grid-cols-[40px_minmax(0,1fr)] max-[720px]:p-4")}>
        <span aria-hidden className={cx("nd-dr-ic grid size-12 place-items-center rounded-[14px] transition-[background-color,color] duration-300 ease-out max-[720px]:size-10 max-[720px]:rounded-xl", iconTone[tone], tone === "run" && "run")}>
          <Glyph d={icon} size={24} stroke={1.5} />
        </span>
        <div className="min-w-0">
          <h3 className="text-xl leading-7 font-medium max-[720px]:text-[17px] max-[720px]:leading-6">{title}</h3>
          <span className="block truncate text-[13px] text-sec max-[720px]:whitespace-normal">{sub}</span>
        </div>
        {can.run && !offline && (
          <Button variant={never && !running ? "primary" : "secondary"} size="lg" disabled={running} onClick={() => {
              started.current = Date.now();
              setTurn(1);
              run.mutate();
            }} className={cx("max-[720px]:col-span-full", running && "pointer-events-none")}>
            {running && <Spin />}
            {running ? t("np.doc.inspecting") : never ? t("np.doc.inspect") : t("hl.doctor.rerun")}
          </Button>
        )}
        <div role="img" aria-label={sub} className="nd-dr-strip col-span-full" style={{ "--n": Math.max(cells.length, 1) } as CSSProperties}>
          {cells.map((c, i) => (
            <i key={i} className={c.cls} data-t={c.label} />
          ))}
        </div>
      </section>

      {offline && !never && (
        <Wa tone="crit" title={t("np.doc.deadTitle")}>
          {t("np.doc.deadBody", { silent: fmt.ago(node.receivedUnix), time: fmt.clock(node.receivedUnix) })}
        </Wa>
      )}
      {!offline && stale && <Wa tone="warn" title={t("hl.doctor.stale", { age: stale })} />}

      {!never && g.issues.length > 0 && (
        <>
          <div className="mx-1 mt-3 flex items-baseline gap-2.5">
            <h4 className="text-sm font-medium">{t("np.doc.attention")}</h4>
            <span className="text-xs text-muted">{t("np.doc.of", { bad: g.issues.length, n: cat })}</span>
          </div>
          <div className="grid grid-cols-[repeat(auto-fit,minmax(340px,1fr))] gap-3 max-[720px]:grid-cols-1">
            {g.issues.map((i) => (
              <IssueCard key={`${i.id}/${i.params.inbound_id ?? ""}`} flow={flow} nodeId={nodeId} nodeName={nodeName} item={i} stale={staleCls ? (stale ?? fmt.duration(node.ageS)) : undefined} />
            ))}
          </div>
        </>
      )}
      {!never &&
        families.map((f) => {
          const rows = items.filter((i) => familyOf(i.id) === f && !g.issues.includes(i));
          if (rows.length === 0) return null;
          return (
            <Group key={f} title={t(`np.doc.cat.${f}`)} count={rows.length}>
              {rows.map((i) => (
                <QuietRow key={i.id} nodeId={nodeId} nodeName={nodeName} item={i} mute={g.accepted.includes(i)} skipped={g.skipped.includes(i)} fresh={fresh.has(i.id)} />
              ))}
            </Group>
          );
        })}
      <p className="mx-1 mt-2 text-[13px] text-muted">{t("np.doc.foot")}</p>
      {flow.modal}
    </div>
  );
}
