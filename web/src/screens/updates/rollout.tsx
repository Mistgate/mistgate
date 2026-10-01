import { Link } from "@tanstack/react-router";
import { SectionLabel } from "@/components/ui/bits";
import { StatusDot, StatusPill, kindTextClass } from "@/components/ui/status";
import { StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { isActive, rolloutInfo, stagesOf, stepErrorText, stepInfo, type Rollout, type Stage, type Step } from "@/lib/updates";

const decided = (s: Step) => s.state === StepState.PASSED || s.state === StepState.FAILED || s.state === StepState.ROLLED_BACK || s.state === StepState.SKIPPED;
const bad = (s: Step) => s.state === StepState.FAILED || s.state === StepState.ROLLED_BACK;
const barColor = (s: Step) => (s.state === StepState.PASSED ? "bg-ok" : s.state === StepState.FAILED ? "bg-danger" : s.state === StepState.ROLLED_BACK ? "bg-warn" : "bg-accent");

function stageTexts(t: T, st: Stage): { name: string; what: string } {
  if (st.kind === "skipped") return { name: t("up.ro.skipped"), what: t("up.ro.skipped.what") };
  if (st.kind === "canary") return { name: t("up.ro.canary"), what: t("up.ro.canary.what") };
  return { name: t("up.ro.batch", { n: st.stage }), what: t.n("up.ro.batch.what", st.steps.length) };
}

function StepRow({ step, toVersion }: { step: Step; toVersion: string }) {
  const t = useT();
  const info = stepInfo(step.state);
  const label = step.state === StepState.PASSED ? t("up.step.to", { version: toVersion }) : t(info.key);
  const why = step.errorKey ? stepErrorText(t, step.errorKey, step.params) : "";
  return (
    <li className="flex flex-col gap-1">
      <div className="grid min-h-8 grid-cols-[96px_minmax(0,1fr)] items-center gap-x-2.5 gap-y-1 sm:grid-cols-[96px_minmax(0,1fr)_minmax(150px,auto)]">
        <Link to="/nodes/$id" params={{ id: step.nodeId }} className="truncate font-mono text-[13px] font-bold hover:underline">
          {step.nodeName}
        </Link>
        <div className="h-1 overflow-hidden rounded-[2px] bg-surface-2">
          <div className={cx("h-full rounded-[2px] transition-[width] duration-300", barColor(step))} style={{ width: `${info.pct}%` }} />
        </div>
        <span className={cx("col-span-2 flex items-center gap-1.5 text-xs whitespace-nowrap sm:col-span-1", info.kind === "off" ? "text-muted" : "text-fg")}>
          <StatusDot kind={info.kind} />
          {label}
        </span>
      </div>
      {why && <p className={cx("text-xs leading-snug text-pretty", step.state === StepState.SKIPPED ? "text-muted" : kindTextClass[info.kind])}>{why}</p>}
    </li>
  );
}

/** The plan and the state of a rollout: the canary, then the batches, one row per node; the last finished rollout stays on the page. */
export function RolloutCard({ rollout }: { rollout: Rollout }) {
  const t = useT();
  const fmt = useFmt();
  const stages = stagesOf(rollout.steps);
  const info = rolloutInfo(rollout.status);
  const active = isActive(rollout);
  // the stage being worked on: the first one with a node in flight, else the first that is not decided yet
  const current = active ? (stages.find((s) => s.steps.some((x) => x.state === StepState.SENT || x.state === StepState.GATING)) ?? stages.find((s) => !s.steps.every(decided)))?.stage : undefined;

  return (
    <section className="flex flex-col gap-1.5 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 pb-2">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <SectionLabel as="h2" icon="refresh" tone="mint">
            {active ? t("up.ro.title") : t("up.ro.titleLast")}
          </SectionLabel>
          <span className="font-mono text-[11px] text-muted">{t("up.ro.sub", { version: rollout.toVersion, ago: fmt.ago(rollout.createdUnix) })}</span>
        </div>
        <StatusPill kind={info.kind} label={t(info.key)} sm />
      </div>
      {stages.map((st, i) => {
        const done = st.steps.every(decided);
        const anyBad = st.steps.some(bad);
        const cur = st.stage === current;
        const tx = stageTexts(t, st);
        // nodes that were passed over are not "done": a grey dash, never the tick of an update that happened
        const passed = st.kind === "skipped";
        const circle = passed
          ? "border-line bg-surface-2 text-muted"
          : done
            ? anyBad
              ? "border-warn bg-warn text-on-accent"
              : "border-accent bg-accent text-on-accent"
            : cur
              ? "border-accent bg-accent-soft text-fg"
              : "border-line bg-transparent text-muted";
        const mark = passed ? "–" : done && !anyBad ? "✓" : done && anyBad ? "!" : i + 1;
        return (
          <div key={st.stage} className="flex gap-3.5">
            <div className="flex flex-none flex-col items-center">
              <span className={cx("box-border grid size-7 place-items-center rounded-full border-[1.5px] font-mono text-xs font-bold", circle)}>{mark}</span>
              {i < stages.length - 1 && <span className={cx("my-1 min-h-3 w-0.5 flex-1", done ? "bg-accent" : "bg-line")} />}
            </div>
            <div className="flex min-w-0 flex-1 flex-col gap-2 pb-4">
              <div className="flex flex-col gap-0.5 pt-1">
                <b className="text-sm">{tx.name}</b>
                <span className="text-xs leading-snug text-muted">{tx.what}</span>
              </div>
              <ul className="flex flex-col gap-1.5">
                {st.steps.map((s) => (
                  <StepRow key={s.nodeId} step={s} toVersion={rollout.toVersion} />
                ))}
              </ul>
            </div>
          </div>
        );
      })}
      <p className="border-t border-line pt-3 text-xs leading-normal text-pretty text-muted">{t("up.ro.safety")}</p>
    </section>
  );
}
