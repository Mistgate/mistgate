import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { RunStatus, TaskKind } from "@/gen/mistgate/tasks/v1/tasks_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { tasks as tasksApi } from "@/lib/api";
import { looksSecret } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { meQuery } from "@/lib/session";
import {
  argFields,
  checkToolsQuery,
  conditionPresets,
  draftOf,
  draftProblem,
  emptyDraft,
  emptyStep,
  inputOf,
  opNeedsValue,
  ops,
  pathHints,
  reasonText,
  runKind,
  scheduleText,
  severities,
  taskErrorText,
  taskRunsQuery,
  tasksQuery,
  tzLabel,
  weekdayKeys,
  type CondDraft,
  type Draft,
  type RunP,
  type StepDraft,
  type TaskP,
} from "@/lib/tasks";
import { Dlg, Empty, ErrCard, Gl, ICONS, OwnerOnly, Pill, Skeleton, SwRow, useFresh, type Kind } from "./bits";
import { openInChat } from "./memory";

// The owner's scheduled tasks (mistgate.tasks.v1, ADR 0017 section 5), as the board draws them (PnAI "Задачи по расписанию"): the list
// with each task's schedule, last and next run, its run history, and the editor of both kinds. The owner and the helpers read and run
// them; only the owner writes them. The Cloudflare edition has none: the tab says so.

const pillOf = (k: ReturnType<typeof runKind>): Kind => (k === "ok" ? "ok" : k === "warn" ? "part" : k === "bad" ? "bad" : k === "busy" ? "busy" : "");
const runWordKey = (s: RunStatus): MessageKey =>
  s === RunStatus.OK ? "aip.task.st.ok" : s === RunStatus.PROBLEM ? "aip.task.st.problem" : s === RunStatus.FAILED ? "aip.task.st.failed" : s === RunStatus.RUNNING ? "aip.task.st.running" : "task.run.skipped";

export function TasksTab({ focus }: { focus?: string }) {
  const t = useT();
  const edition = useEdition();
  const role = useQuery(meQuery).data?.admin?.role;
  if (edition.off("assistant")) return <Notice tone="quiet" title={t("err.unavailable_in_edition", { feature: t("task.feature") })} />;
  if (role !== Role.OWNER && role !== Role.HELPER) return <OwnerOnly title={t("task.ownerHelper")} icon={ICONS.clock} />;
  return <TaskList owner={role === Role.OWNER} focus={focus} />;
}

type Editing = { task?: TaskP; kind: Draft["kind"] };

function TaskList({ owner, focus }: { owner: boolean; focus?: string }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const q = useQuery(tasksQuery);
  const tools = useQuery({ ...checkToolsQuery, enabled: owner });
  const [editing, setEditing] = useState<Editing | null>(null);
  const [open, setOpen] = useState<string | null>(focus ?? null);
  const [asking, setAsking] = useState("");
  const refresh = () => void qc.invalidateQueries({ queryKey: ["tasks"] });
  const list = q.data?.tasks ?? [];
  const fresh = useFresh(list.map((x) => x.id));
  const schemas = useMemo(() => Object.fromEntries((tools.data ?? []).map((tool) => [tool.name, tool.inputSchemaJson])), [tools.data]);
  const runNow = useMutation({
    mutationFn: (id: string) => tasksApi.runTaskNow({ id }),
    onSuccess: (_, id) => {
      toast(t("task.started"));
      setOpen(id);
      refresh();
    },
    onError: (e) => toast.error(taskErrorText(e, t)),
  });
  const del = useMutation({
    mutationFn: (id: string) => tasksApi.deleteTask({ id }),
    onSuccess: () => {
      setAsking("");
      toast(t("task.deleted"));
      refresh();
    },
    onError: (e) => toast.error(taskErrorText(e, t)),
  });
  // the switch of a task: the task as it is, with only "enabled" turned over (against the version the list holds)
  const toggle = useMutation({
    mutationFn: (task: TaskP) => tasksApi.updateTask({ id: task.id, task: inputOf({ ...draftOf(task), enabled: !task.enabled }, schemas), expectedVersion: BigInt(task.version) }),
    onSuccess: () => refresh(),
    onError: (e) => toast.error(taskErrorText(e, t)),
  });

  if (q.isPending) return <Skeleton />;
  if (q.isError) return <ErrCard onRetry={() => void q.refetch()} />;
  const tz = q.data?.timezoneOffsetMinutes ?? 0;
  return (
    <div className="sb-tab">
      <div className="ai-top">
        <p>{t("aip.task.lead")}</p>
        {owner && (
          <span className="ai-hb">
            <Button size="lg" onClick={() => setEditing({ kind: "assistant" })}>
              {t("task.newAssistant")}
            </Button>
            <Button size="lg" variant="primary" onClick={() => setEditing({ kind: "check" })}>
              <Gl d={ICONS.plus} />
              {t("task.newCheck")}
            </Button>
          </span>
        )}
      </div>
      {list.length === 0 && (
        <Empty
          calm
          d={ICONS.clock}
          title={t("task.empty")}
          body={t("aip.task.emptyBody")}
          action={
            owner ? (
              <Button size="lg" variant="primary" onClick={() => setEditing({ kind: "check" })}>
                {t("task.newCheck")}
              </Button>
            ) : undefined
          }
        />
      )}
      <div className="ai-tasks">
        {list.map((task) => (
          <TaskCard
            key={task.id}
            tz={tz}
            task={task}
            owner={owner}
            fresh={fresh === task.id}
            expanded={open === task.id}
            asking={asking === task.id}
            toggling={toggle.isPending && toggle.variables?.id === task.id}
            canToggle={!!tools.data || tools.isError || task.kind === TaskKind.ASSISTANT}
            onToggle={() => setOpen((cur) => (cur === task.id ? null : task.id))}
            onSwitch={() => toggle.mutate(task)}
            onRun={() => runNow.mutate(task.id)}
            running={task.running || (runNow.isPending && runNow.variables === task.id)}
            onEdit={() => setEditing({ task, kind: task.kind === TaskKind.ASSISTANT ? "assistant" : "check" })}
            onAsk={(v) => setAsking(v ? task.id : "")}
            onDelete={() => del.mutate(task.id)}
          />
        ))}
      </div>
      {list.length > 0 && <p className="ai-foot">{t("task.tz", { tz: tzLabel(tz) })}</p>}
      {editing && <TaskEditor key={editing.task?.id ?? editing.kind} editing={editing} tz={tz} onClose={() => setEditing(null)} onSaved={refresh} />}
    </div>
  );
}

function TaskCard({
  tz,
  task,
  owner,
  fresh,
  expanded,
  asking,
  running,
  toggling,
  canToggle,
  onToggle,
  onSwitch,
  onRun,
  onEdit,
  onAsk,
  onDelete,
}: {
  /** The panel's schedule offset in minutes: task times are shown in it, like the schedule itself. */
  tz: number;
  task: TaskP;
  owner: boolean;
  fresh: boolean;
  expanded: boolean;
  asking: boolean;
  running: boolean;
  toggling: boolean;
  canToggle: boolean;
  onToggle: () => void;
  onSwitch: () => void;
  onRun: () => void;
  onEdit: () => void;
  onAsk: (v: boolean) => void;
  onDelete: () => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const last = task.lastRun;
  const runsId = useId();
  // a run that has just ended: its button ticks once, as the board's does
  const was = useRef(running);
  const [done, setDone] = useState(false);
  useEffect(() => {
    if (was.current && !running) {
      setDone(true);
      const h = setTimeout(() => setDone(false), 1600);
      was.current = running;
      return () => clearTimeout(h);
    }
    was.current = running;
  }, [running]);
  const off = !task.enabled;
  const kind = last ? pillOf(runKind(last.status)) : "";
  const found = !running && last && last.status !== RunStatus.OK ? last.summary || reasonText(t, last.reason) : "";
  return (
    <section className={cx("card ai-task", off && "off", fresh && "fresh")} data-task={task.id}>
      <div className="ai-th">
        <div className="ai-tn">
          <b>{task.name}</b>
          <span className="ai-tm">
            <i className={cx("ai-kind", task.kind === TaskKind.ASSISTANT ? "asst" : "chk")}>{t(task.kind === TaskKind.ASSISTANT ? "task.kind.assistant" : "task.kind.check")}</i>
            {task.notifyTelegram && <i className="ai-kind tg">{t("task.telegramOn")}</i>}
            <em>
              {scheduleText(t, task.schedule)} · {task.enabled ? (task.nextRunUnix > 0 ? t("task.next", { time: fmt.stampAt(task.nextRunUnix, tz) }) : "") : t("task.off")}
            </em>
          </span>
        </div>
        {owner && <Switch checked={task.enabled} disabled={toggling || !canToggle} onCheckedChange={onSwitch} aria-label={t("aip.task.enabled")} />}
      </div>
      <div className="ai-ts">
        {running ? <Pill kind="busy">{t("aip.task.st.running")}</Pill> : last ? <Pill kind={kind}>{t(runWordKey(last.status))}</Pill> : <Pill kind="">{t("aip.task.st.never")}</Pill>}
        <em>{running ? t("aip.task.startedNow") : last ? t("task.lastRun", { time: fmt.ago(last.startedUnix) }) : t("task.never")}</em>
        {!running && task.failStreak >= 2 && <em>{t.n("task.failStreak", task.failStreak)}</em>}
        {found && <code className={cx("mono ai-find", last?.status === RunStatus.FAILED && "bad")}>{found}</code>}
      </div>
      <div className="ai-ta">
        <Button size="md" className={cx(done && "done")} disabled={running} onClick={onRun}>
          {running && <span className="an-sp" aria-hidden="true" />}
          {running ? t("aip.task.running") : t("task.runNow")}
        </Button>
        <Button size="md" aria-expanded={expanded} aria-controls={runsId} onClick={onToggle}>
          {t(expanded ? "task.hideRuns" : "task.runs")}
        </Button>
        {owner &&
          (asking ? (
            <span className="ai-ask">
              <em>{t("aip.task.ask")}</em>
              <Button size="md" onClick={() => onAsk(false)}>
                {t("task.cancel")}
              </Button>
              <Button size="md" className="rw-dng" onClick={onDelete}>
                {t("task.delete")}
              </Button>
            </span>
          ) : (
            <>
              <Button size="md" onClick={onEdit}>
                {t("task.edit")}
              </Button>
              <Button size="md" onClick={() => onAsk(true)}>
                {t("task.delete")}
              </Button>
            </>
          ))}
      </div>
      {expanded && (
        <div id={runsId}>
          <Runs taskId={task.id} running={task.running} tz={tz} />
        </div>
      )}
    </section>
  );
}

function Runs({ taskId, running, tz }: { taskId: string; running: boolean; tz: number }) {
  const t = useT();
  const q = useQuery(taskRunsQuery(taskId, running));
  if (q.isPending) return <p className="ai-nr">{t("aip.loading")}</p>;
  if (q.isError) return <p className="ai-err">{t("aip.err.body")}</p>;
  if (q.data.length === 0) return <p className="ai-nr">{t("aip.task.noRuns")}</p>;
  return (
    <div className="ai-runs">
      {q.data.map((run) => (
        <RunItem key={run.id} run={run} tz={tz} />
      ))}
    </div>
  );
}

function RunItem({ run, tz }: { run: RunP; tz: number }) {
  const t = useT();
  const fmt = useFmt();
  // a run or a step takes seconds: say them, and the coarse units from a minute and a half on
  const span = (seconds: number) => (seconds < 90 ? `${fmt.num(seconds, seconds < 10 ? 1 : 0)} ${t("unit.s")}` : fmt.duration(seconds));
  const money = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 3 });
  const trigger = run.trigger === "manual" ? t("task.trigger.manual", { actor: run.actor || "?" }) : run.trigger === "missed" ? t("task.trigger.missed") : t("task.trigger.schedule");
  const took = run.finishedUnix > 0 ? span(Math.max(0, run.finishedUnix - run.startedUnix)) : "";
  const said = run.status === RunStatus.OK ? "" : run.summary || reasonText(t, run.reason);
  const details = run.steps.length > 0 || !!run.conversationId || (!!run.reason && run.status !== RunStatus.PROBLEM && run.status !== RunStatus.OK) || !!run.summary;
  return (
    <div className="ai-run">
      <div>
        <em>{fmt.stampAt(run.startedUnix, tz)}</em>
        <span>
          <i className={cx("rw-gl", pillOf(runKind(run.status)))} aria-hidden="true" />
          {t(runWordKey(run.status))}
          {said && `: ${said}`}
        </span>
        <em className="mono">{[took, run.usd > 0 ? `$${money(run.usd)}` : ""].filter(Boolean).join(" · ")}</em>
      </div>
      {details && (
        <div className="ai-rx">
          <p>{[trigger, run.reason && run.status === RunStatus.OK ? reasonText(t, run.reason) : ""].filter(Boolean).join(" · ")}</p>
          {run.summary && run.status === RunStatus.OK && <p>{run.summary}</p>}
          {run.steps.map((st, i) => (
            <div key={i} className="ai-st">
              <span>
                <i className={cx("rw-gl", st.problem ? "part" : st.ok ? "ok" : "bad")} aria-hidden="true" />
                {st.tool}
                <em>{span(st.durationMs / 1000)}</em>
              </span>
              {st.matches.length > 0 && (
                <p>
                  {t("common.labeled", { label: t("task.step.matched"), value: "" })}
                  <span className="mono">{st.matches.join("; ")}</span>
                </p>
              )}
              {st.error && <p className="bad">{t("common.labeled", { label: t("task.step.error"), value: st.error })}</p>}
              {st.excerpt && (
                <details>
                  <summary>{t("task.step.result")}</summary>
                  <pre dir="ltr">{st.excerpt}</pre>
                </details>
              )}
            </div>
          ))}
          {run.conversationId && (
            <div className="ai-co">
              {run.provider && <span>{t("task.run.cost", { provider: run.provider, usd: money(run.usd) })}</span>}
              {run.fellBack && <span>{t("task.run.fellBack")}</span>}
              <Button size="md" onClick={() => openInChat(run.conversationId)}>
                {t("task.openChat")}
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------------------------------------------------
// the editor

function TaskEditor({ editing, tz, onClose, onSaved }: { editing: Editing; tz: number; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const toast = useToast();
  const [draft, setDraft] = useState<Draft>(() => (editing.task ? draftOf(editing.task) : emptyDraft(editing.kind)));
  const [tried, setTried] = useState(false);
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const tools = useQuery({ ...checkToolsQuery, enabled: draft.kind === "check" });
  const schemas = useMemo(() => Object.fromEntries((tools.data ?? []).map((tool) => [tool.name, tool.inputSchemaJson])), [tools.data]);
  const save = useMutation({
    mutationFn: async () => {
      const task = inputOf(draft, schemas);
      if (editing.task) await tasksApi.updateTask({ id: editing.task.id, task, expectedVersion: BigInt(editing.task.version) });
      else await tasksApi.createTask({ task });
    },
    onSuccess: () => {
      toast(t(editing.task ? "task.saved" : "task.created"));
      halo(editing.task ? "save" : "create");
      onSaved();
      onClose();
    },
    onError: (e) => toast.error(taskErrorText(e, t)),
  });
  const problem = draftProblem(draft);
  const setStep = (i: number, step: StepDraft) => set({ steps: draft.steps.map((s, j) => (j === i ? step : s)) });
  const nameErr = tried && !draft.name.trim();
  const go = () => {
    setTried(true);
    if (problem || save.isPending) return;
    save.mutate();
  };

  return (
    <Dlg
      className="ai-nq"
      shot="ai-task-dialog"
      title={editing.task ? t("task.editor.edit", { name: t("common.quoted", { name: editing.task.name }) }) : t("aip.task.new")}
      onClose={onClose}
      busy={save.isPending}
      footer={
        <>
          {tried && problem && !nameErr && <span className="ai-tz me-auto">{t(problem)}</span>}
          <Button size="lg" onClick={onClose}>
            {t("task.cancel")}
          </Button>
          <Button size="lg" variant="primary" disabled={save.isPending} onClick={go}>
            {save.isPending && <span className="an-sp" aria-hidden="true" />}
            {t(editing.task ? "task.save" : "task.create")}
          </Button>
        </>
      }
    >
      <label className={cx("an-f", nameErr && "err")}>
        <span>{t("task.f.name")}</span>
        <input className="an-i" type="text" autoComplete="off" maxLength={80} placeholder={t("aip.task.namePh")} value={draft.name} onChange={(e) => set({ name: e.target.value })} />
        <small>{nameErr ? t("task.need.name") : ""}</small>
      </label>

      <span className="rw-sl">{t("aip.task.kind")}</span>
      <Segmented
        className="us-seg"
        aria-label={t("aip.task.kind")}
        value={draft.kind}
        onValueChange={(v) => !editing.task && setDraft({ ...emptyDraft(v), name: draft.name })}
        options={[
          { value: "check", label: t("task.kind.check") },
          { value: "assistant", label: t("task.kind.assistant") },
        ]}
      />
      <p className="ai-cs">{t(draft.kind === "assistant" ? "aip.task.kindAs" : "aip.task.kindChk")}</p>

      <span className="rw-sl">{t("task.f.when")}</span>
      <Segmented
        className="us-seg"
        aria-label={t("task.f.when")}
        value={draft.scheduleKind}
        onValueChange={(v) => set({ scheduleKind: v })}
        options={[
          { value: "every", label: t("task.f.every") },
          { value: "daily", label: t("task.f.daily") },
          { value: "weekly", label: t("task.f.weekly") },
        ]}
      />
      <div className="ai-when">
        {draft.scheduleKind === "every" ? (
          <>
            <label className="an-f">
              <span>{t("aip.task.count")}</span>
              <input className="an-i mono" inputMode="numeric" aria-label={t("task.f.count")} value={draft.every} onChange={(e) => set({ every: e.target.value.replace(/\D/g, "").slice(0, 4) })} />
            </label>
            <label className="an-f">
              <span>{t("aip.task.unit")}</span>
              <select className="an-i" aria-label={t("aip.task.unit")} value={draft.unit} onChange={(e) => set({ unit: e.target.value as Draft["unit"] })}>
                <option value="m">{t("task.f.unit.m")}</option>
                <option value="h">{t("task.f.unit.h")}</option>
              </select>
            </label>
          </>
        ) : (
          <>
            {draft.scheduleKind === "weekly" && (
              <label className="an-f">
                <span>{t("task.f.day")}</span>
                <select className="an-i" aria-label={t("task.f.day")} value={String(draft.weekday)} onChange={(e) => set({ weekday: Number(e.target.value) })}>
                  {[1, 2, 3, 4, 5, 6, 0].map((d) => (
                    <option key={d} value={d}>
                      {t(weekdayKeys[d]!)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <label className="an-f">
              <span>{t("task.f.time")}</span>
              <input className="an-i mono" type="time" aria-label={t("task.f.time")} value={draft.time} onChange={(e) => set({ time: e.target.value })} />
            </label>
          </>
        )}
      </div>
      <small className="ai-tz">{t("task.tz", { tz: tzLabel(tz) })}</small>

      {draft.kind === "assistant" ? (
        <label className="an-f">
          <span>{t("aip.task.prompt")}</span>
          <textarea className="an-i sb-ta" rows={3} maxLength={4000} aria-label={t("task.f.prompt")} placeholder={t("aip.task.promptPh")} value={draft.prompt} onChange={(e) => set({ prompt: e.target.value })} />
          <small>{t("aip.task.promptHint")}</small>
          {looksSecret(draft.prompt) && <small className="ai-err">{t("ai.secretHint")}</small>}
        </label>
      ) : (
        <>
          {tools.isError && <p className="ai-err">{t("aip.err.body")}</p>}
          {draft.steps.map((step, i) => (
            <StepEditor
              key={i}
              n={i + 1}
              step={step}
              tools={(tools.data ?? []).map((tool) => ({ value: tool.name, label: tool.name }))}
              schema={schemas[step.tool] ?? ""}
              onChange={(s) => setStep(i, s)}
              onRemove={draft.steps.length > 1 ? () => set({ steps: draft.steps.filter((_, j) => j !== i) }) : undefined}
            />
          ))}
          {draft.steps.length < 8 && (
            <Button size="md" className="self-start" onClick={() => set({ steps: [...draft.steps, emptyStep()] })}>
              <Gl d={ICONS.plus} />
              {t("task.f.addStep")}
            </Button>
          )}
          <small className="ai-tz">{t("aip.task.stepsHint")}</small>
        </>
      )}

      <div className="ai-sws">
        <SwRow title={t("aip.task.tg")} hint={t("aip.task.tgHint")}>
          <Switch checked={draft.notifyTelegram} onCheckedChange={(v) => set({ notifyTelegram: v })} aria-label={t("task.f.telegram")} />
        </SwRow>
        <SwRow title={t("aip.task.enabled")} hint={t("aip.task.enabledHint")}>
          <Switch checked={draft.enabled} onCheckedChange={(v) => set({ enabled: v })} aria-label={t("task.f.enabled")} />
        </SwRow>
      </div>
    </Dlg>
  );
}

function StepEditor({ n, step, tools, schema, onChange, onRemove }: { n: number; step: StepDraft; tools: { value: string; label: string }[]; schema: string; onChange: (s: StepDraft) => void; onRemove?: () => void }) {
  const t = useT();
  const fields = argFields(schema);
  const listId = useId();
  const options = tools.some((o) => o.value === step.tool) || !step.tool ? tools : [{ value: step.tool, label: step.tool }, ...tools];
  const setCond = (i: number, c: CondDraft) => onChange({ ...step, conditions: step.conditions.map((x, j) => (j === i ? c : x)) });
  return (
    <div className="ai-sx">
      <span className="rw-sl">{t("aip.task.step", { n })}</span>
      <div className="ai-step">
        <select className="an-i mono" aria-label={t("task.f.tool")} value={step.tool} onChange={(e) => onChange({ ...step, tool: e.target.value, args: {}, conditions: step.tool === e.target.value ? step.conditions : [] })}>
          {!step.tool && <option value="">{t("task.f.tool")}</option>}
          {options.map((o) => (
            <option key={o.value} value={o.value}>
              {o.label}
            </option>
          ))}
        </select>
        {onRemove && (
          <button type="button" className="ai-ic" aria-label={t("task.f.removeStep")} title={t("task.f.removeStep")} onClick={onRemove}>
            <Gl d={ICONS.x} />
          </button>
        )}
      </div>
      {fields.map((f) =>
        f.type === "boolean" ? (
          <label key={f.name} className="ai-chk" title={f.description}>
            <input type="checkbox" checked={step.args[f.name] === true} onChange={(e) => onChange({ ...step, args: { ...step.args, [f.name]: e.target.checked } })} />
            <span className="mono">{f.name}</span>
          </label>
        ) : f.type === "enum" ? (
          <label key={f.name} className="an-f" title={f.description}>
            <span className="mono">{f.name}</span>
            <select className="an-i" aria-label={f.name} value={String(step.args[f.name] ?? "")} onChange={(e) => onChange({ ...step, args: { ...step.args, [f.name]: e.target.value } })}>
              <option value="">—</option>
              {f.options.map((o) => (
                <option key={o} value={o}>
                  {o}
                </option>
              ))}
            </select>
          </label>
        ) : (
          <label key={f.name} className="an-f" title={f.description}>
            <span className="mono">{f.name}</span>
            <input
              className="an-i"
              placeholder={f.description.slice(0, 60)}
              inputMode={f.type === "integer" || f.type === "number" ? "decimal" : undefined}
              value={String(step.args[f.name] ?? "")}
              onChange={(e) => onChange({ ...step, args: { ...step.args, [f.name]: e.target.value } })}
            />
          </label>
        ),
      )}
      <span className="rw-sl">{t("aip.task.problemIf")}</span>
      {step.conditions.length === 0 && <small className="ai-tz">{t("task.f.noConditions")}</small>}
      <datalist id={listId}>
        {(pathHints[step.tool] ?? []).map((p) => (
          <option key={p} value={p} />
        ))}
      </datalist>
      {step.conditions.map((c, i) => (
        <div key={i} className="ai-cd">
          <input className="an-i mono" aria-label={t("task.f.path")} placeholder={t("task.f.path")} list={listId} value={c.path} onChange={(e) => setCond(i, { ...c, path: e.target.value })} />
          <select className="an-i" aria-label={t("task.f.op")} value={c.op} onChange={(e) => setCond(i, { ...c, op: e.target.value, value: e.target.value === "severity_at_least" ? "warning" : c.value })}>
            {ops.map((op) => (
              <option key={op} value={op}>
                {t(`task.op.${op}` as MessageKey)}
              </option>
            ))}
          </select>
          {c.op === "severity_at_least" ? (
            <select className="an-i" aria-label={t("task.f.value")} value={c.value || "warning"} onChange={(e) => setCond(i, { ...c, value: e.target.value })}>
              {severities.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          ) : opNeedsValue(c.op) ? (
            <input className="an-i" aria-label={t("task.f.value")} placeholder={t("task.f.value")} value={c.value} onChange={(e) => setCond(i, { ...c, value: e.target.value })} />
          ) : (
            <span />
          )}
          <button type="button" className="ai-ic" aria-label={t("task.f.removeCondition")} title={t("task.f.removeCondition")} onClick={() => onChange({ ...step, conditions: step.conditions.filter((_, j) => j !== i) })}>
            <Gl d={ICONS.x} />
          </button>
        </div>
      ))}
      {step.conditions.length < 5 && (
        <div className="ai-ch">
          {conditionPresets.map((p) => (
            <button key={p.key} type="button" onClick={() => onChange({ ...step, conditions: [...step.conditions, { ...p.cond }] })}>
              + {t(p.key)}
            </button>
          ))}
        </div>
      )}
      <label className="ai-chk">
        <input type="checkbox" checked={step.errorIsProblem} onChange={(e) => onChange({ ...step, errorIsProblem: e.target.checked })} />
        {t("task.f.errorIsProblem")}
      </label>
    </div>
  );
}
