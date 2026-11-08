import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useId, useMemo, useState, type ReactNode } from "react";
import { Card } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon, NavIcon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { StatusPill } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
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
  runWord,
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
import { openInChat } from "./memory";
import { Heading } from "./parts";

// The owner's scheduled tasks (mistgate.tasks.v1, ADR 0017 section 5): the list with each task's schedule, last and next run,
// its run history, and the editor of both kinds. The owner and the helpers read and run them; only the owner writes them. The
// Cloudflare edition has none: the tab says so.

export function TasksTab({ focus }: { focus?: string }) {
  const t = useT();
  const edition = useEdition();
  const role = useQuery(meQuery).data?.admin?.role;
  if (edition.off("assistant")) return <Notice tone="quiet" title={t("err.unavailable_in_edition", { feature: t("task.feature") })} />;
  if (role !== Role.OWNER && role !== Role.HELPER) {
    return (
      <Card lg className="border-dashed">
        <EmptyState icon={<NavIcon name="ai" size={20} />} title={t("task.ownerHelper")} />
      </Card>
    );
  }
  return <TaskList owner={role === Role.OWNER} focus={focus} />;
}

type Editing = { task?: TaskP; kind: Draft["kind"] };

function TaskList({ owner, focus }: { owner: boolean; focus?: string }) {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const q = useQuery(tasksQuery);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [open, setOpen] = useState<string | null>(focus ?? null);
  const [deleting, setDeleting] = useState<TaskP | null>(null);
  const refresh = () => void qc.invalidateQueries({ queryKey: ["tasks"] });
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
      setDeleting(null);
      toast(t("task.deleted"));
      refresh();
    },
    onError: (e) => toast.error(taskErrorText(e, t)),
  });
  const list = q.data?.tasks ?? [];
  const actions = owner && (
    <div className="flex flex-none flex-wrap gap-2">
      <Button type="button" size="sm" variant="secondary" onClick={() => setEditing({ kind: "check" })}>
        <Icon name="plus" size={13} />
        {t("task.newCheck")}
      </Button>
      <Button type="button" size="sm" variant="secondary" onClick={() => setEditing({ kind: "assistant" })}>
        <Icon name="sparkle" size={13} />
        {t("task.newAssistant")}
      </Button>
    </div>
  );

  return (
    <div className="flex flex-col gap-3.5">
      <Heading title={t("task.title")} desc={t("task.lead")} right={actions} />
      {q.isPending && <Pending />}
      {q.isError && <QueryError error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && list.length === 0 && (
        <Card lg className="border-dashed">
          <EmptyState icon={<Icon name="clock" size={20} />} title={t("task.empty")}>
            {t("task.emptyText")}
          </EmptyState>
        </Card>
      )}
      {list.length > 0 && (
        <Card lg className="flex flex-col divide-y divide-line px-4 py-0">
          {list.map((task) => (
            <TaskRow
              key={task.id}
              tz={q.data?.timezoneOffsetMinutes ?? 0}
              task={task}
              owner={owner}
              expanded={open === task.id}
              onToggle={() => setOpen((cur) => (cur === task.id ? null : task.id))}
              onRun={() => runNow.mutate(task.id)}
              running={task.running || (runNow.isPending && runNow.variables === task.id)}
              onEdit={() => setEditing({ task, kind: task.kind === TaskKind.ASSISTANT ? "assistant" : "check" })}
              onDelete={() => setDeleting(task)}
            />
          ))}
        </Card>
      )}
      {q.data && <p className="text-xs text-muted">{t("task.tz", { tz: tzLabel(q.data.timezoneOffsetMinutes) })}</p>}
      {editing && <TaskEditor key={editing.task?.id ?? editing.kind} editing={editing} tz={q.data?.timezoneOffsetMinutes ?? 0} onClose={() => setEditing(null)} onSaved={refresh} />}
      <Modal
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={t("task.deleteT", { name: t("common.quoted", { name: deleting?.name ?? "" }) })}
        description={t("task.deleteBody")}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={() => setDeleting(null)}>
              {t("task.cancel")}
            </Button>
            <Button type="button" variant="danger" disabled={del.isPending} onClick={() => deleting && del.mutate(deleting.id)}>
              {t("task.delete")}
            </Button>
          </>
        }
      />
    </div>
  );
}

function TaskRow({
  tz,
  task,
  owner,
  expanded,
  running,
  onToggle,
  onRun,
  onEdit,
  onDelete,
}: {
  /** The panel's schedule offset in minutes: task times are shown in it, like the schedule itself. */
  tz: number;
  task: TaskP;
  owner: boolean;
  expanded: boolean;
  running: boolean;
  onToggle: () => void;
  onRun: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const last = task.lastRun;
  const runsId = useId();
  return (
    <div className="flex flex-col gap-3 py-3.5" data-task={task.id}>
      <div className="flex flex-col gap-3 sm:flex-row sm:items-start">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <div className="flex flex-wrap items-center gap-2">
            <b className="text-[13.5px] font-semibold break-words">{task.name}</b>
            <span className="rounded-full bg-surface-2 px-2 py-0.5 text-[10.5px] font-semibold text-muted">
              {t(task.kind === TaskKind.ASSISTANT ? "task.kind.assistant" : "task.kind.check")}
            </span>
            {task.notifyTelegram && <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[10.5px] font-semibold text-accent-text">{t("task.telegramOn")}</span>}
          </div>
          <span className="text-xs text-muted">
            {scheduleText(t, task.schedule)} · {task.enabled ? (task.nextRunUnix > 0 ? t("task.next", { time: fmt.stampAt(task.nextRunUnix, tz) }) : "") : t("task.off")}
          </span>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            {running ? (
              <StatusPill sm kind="busy" label={t("task.run.running")} />
            ) : last ? (
              <>
                <StatusPill sm kind={runKind(last.status)} label={t(runWord(last.status))} />
                <span className="text-muted">{t("task.lastRun", { time: fmt.ago(last.startedUnix) })}</span>
              </>
            ) : (
              <span className="text-muted">{t("task.never")}</span>
            )}
            {task.failStreak >= 2 && <span className="text-warn-text">{t.n("task.failStreak", task.failStreak)}</span>}
          </div>
          {!running && last && (last.summary || last.reason) && (
            <p className="line-clamp-2 text-xs leading-snug break-words text-muted">{last.summary || reasonText(t, last.reason)}</p>
          )}
        </div>
        <div className="flex flex-none flex-wrap items-center gap-1">
          <Button type="button" size="sm" variant="secondary" disabled={running} onClick={onRun}>
            <Icon name="refresh" size={12} />
            {t("task.runNow")}
          </Button>
          <Button type="button" size="sm" variant="ghost" aria-expanded={expanded} aria-controls={runsId} onClick={onToggle}>
            {t(expanded ? "task.hideRuns" : "task.runs")}
            <Icon name="chevronDown" size={12} className={cx("transition-transform", expanded && "rotate-180")} />
          </Button>
          {owner && (
            <>
              <Button type="button" size="sm" variant="ghost" onClick={onEdit}>
                {t("task.edit")}
              </Button>
              <Button type="button" size="sm" variant="ghostDanger" onClick={onDelete}>
                {t("task.delete")}
              </Button>
            </>
          )}
        </div>
      </div>
      {expanded && (
        <div id={runsId}>
          <Runs taskId={task.id} running={task.running} tz={tz} />
        </div>
      )}
    </div>
  );
}

function Runs({ taskId, running, tz }: { taskId: string; running: boolean; tz: number }) {
  const t = useT();
  const q = useQuery(taskRunsQuery(taskId, running));
  if (q.isPending) return <Pending compact />;
  if (q.isError) return <QueryError compact error={q.error} onRetry={() => void q.refetch()} />;
  if (q.data.length === 0) return <p className="text-[13px] text-muted">{t("task.noRuns")}</p>;
  return (
    <ul className="flex flex-col gap-2 rounded-field bg-surface-2 p-2.5">
      {q.data.map((run) => (
        <RunItem key={run.id} run={run} tz={tz} />
      ))}
    </ul>
  );
}

function RunItem({ run, tz }: { run: RunP; tz: number }) {
  const t = useT();
  const fmt = useFmt();
  // a run or a step takes seconds: say them, and the coarse units from a minute and a half on
  const span = (seconds: number) => (seconds < 90 ? `${fmt.num(seconds, seconds < 10 ? 1 : 0)} ${t("unit.s")}` : fmt.duration(seconds));
  const fmtMoney = (n: number) => n.toLocaleString(fmt.locale, { maximumFractionDigits: 3 });
  const trigger =
    run.trigger === "manual" ? t("task.trigger.manual", { actor: run.actor || "?" }) : run.trigger === "missed" ? t("task.trigger.missed") : t("task.trigger.schedule");
  const took = run.finishedUnix > 0 ? span(Math.max(0, run.finishedUnix - run.startedUnix)) : "";
  return (
    <li className="flex flex-col gap-1.5 rounded-ctl bg-surface px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
        <StatusPill sm kind={runKind(run.status)} label={t(runWord(run.status))} />
        <span className="font-medium">{fmt.stampAt(run.startedUnix, tz)}</span>
        <span className="text-muted">
          {trigger}
          {took && ` · ${took}`}
        </span>
      </div>
      {run.reason && run.status !== RunStatus.PROBLEM && <p className="text-xs text-muted">{reasonText(t, run.reason)}</p>}
      {run.summary && <p className="text-xs leading-snug break-words">{run.summary}</p>}
      {run.steps.map((st, i) => (
        <div key={i} className="flex flex-col gap-1 border-s-2 border-line ps-2.5 text-xs">
          <span className="flex items-center gap-1.5 font-mono text-[11.5px]">
            <span aria-hidden className={cx("size-1.5 rounded-full", st.problem ? "bg-warn" : st.ok ? "bg-ok" : "bg-danger")} />
            {st.tool}
            <span className="font-sans text-muted">{span(st.durationMs / 1000)}</span>
          </span>
          {st.matches.length > 0 && (
            <span className="text-muted">
              {t("common.labeled", { label: t("task.step.matched"), value: "" })}
              <span className="font-mono text-fg">{st.matches.join("; ")}</span>
            </span>
          )}
          {st.error && (
            <span className="text-danger-text">
              {t("common.labeled", { label: t("task.step.error"), value: st.error })}
            </span>
          )}
          {st.excerpt && (
            <details>
              <summary className="cursor-pointer text-muted select-none">{t("task.step.result")}</summary>
              <pre dir="ltr" className="mt-1 max-h-48 overflow-auto rounded-ctl bg-surface-2 p-2 font-mono text-[11px] leading-snug break-all whitespace-pre-wrap">
                {st.excerpt}
              </pre>
            </details>
          )}
        </div>
      ))}
      {run.conversationId && (
        <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
          {run.provider && <span>{t("task.run.cost", { provider: run.provider, usd: fmtMoney(run.usd) })}</span>}
          {run.fellBack && <span className="text-warn-text">{t("task.run.fellBack")}</span>}
          <Button type="button" size="xs" variant="ghost" onClick={() => openInChat(run.conversationId)}>
            <Icon name="chat" size={11} />
            {t("task.openChat")}
          </Button>
        </div>
      )}
    </li>
  );
}

// ---------------------------------------------------------------------------------------------------------------------
// the editor

function Field({ label, hint, children }: { label: ReactNode; hint?: ReactNode; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{label}</span>
      {children}
      {hint && <span className="text-xs leading-snug text-pretty text-muted">{hint}</span>}
    </div>
  );
}

function ToggleRow({ label, hint, checked, onChange }: { label: string; hint?: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-start gap-3">
      <span className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-[13px] font-medium">{label}</span>
        {hint && <span className="text-xs leading-snug text-muted">{hint}</span>}
      </span>
      <Switch checked={checked} onCheckedChange={onChange} aria-label={label} />
    </label>
  );
}

function TaskEditor({ editing, tz, onClose, onSaved }: { editing: Editing; tz: number; onClose: () => void; onSaved: () => void }) {
  const t = useT();
  const toast = useToast();
  const [draft, setDraft] = useState<Draft>(() => (editing.task ? draftOf(editing.task) : emptyDraft(editing.kind)));
  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const tools = useQuery({ ...checkToolsQuery, enabled: draft.kind === "check" });
  const schemas = useMemo(() => Object.fromEntries((tools.data ?? []).map((tool) => [tool.name, tool.inputSchemaJson])), [tools.data]);
  const save = useMutation({
    mutationFn: async () => {
      const task = inputOf(draft, schemas);
      if (editing.task) await tasksApi.updateTask({ id: editing.task.id, task });
      else await tasksApi.createTask({ task });
    },
    onSuccess: () => {
      toast(t(editing.task ? "task.saved" : "task.created"));
      onSaved();
      onClose();
    },
    onError: (e) => toast.error(taskErrorText(e, t)),
  });
  const problem = draftProblem(draft);
  const setStep = (i: number, step: StepDraft) => set({ steps: draft.steps.map((s, j) => (j === i ? step : s)) });

  return (
    <Modal
      open
      wide
      onOpenChange={(v) => !v && onClose()}
      title={editing.task ? t("task.editor.edit", { name: t("common.quoted", { name: editing.task.name }) }) : t("task.editor.new")}
      closeLabel={t("task.cancel")}
      footer={
        <div className="flex w-full flex-wrap items-center justify-end gap-2">
          {problem && <span className="me-auto text-xs text-muted">{t(problem)}</span>}
          <Button type="button" variant="secondary" onClick={onClose}>
            {t("task.cancel")}
          </Button>
          <Button type="button" variant="primary" disabled={!!problem || save.isPending} onClick={() => save.mutate()}>
            {t(editing.task ? "task.save" : "task.create")}
          </Button>
        </div>
      }
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <TextField label={t("task.f.name")} placeholder={t("task.f.namePh")} value={draft.name} maxLength={80} onChange={(e) => set({ name: e.target.value })} />
        <Field label={t("task.f.kind")} hint={t(draft.kind === "assistant" ? "task.f.assistantHint" : "task.f.checkHint")}>
          <Segmented
            aria-label={t("task.f.kind")}
            value={draft.kind}
            onValueChange={(v) => !editing.task && setDraft({ ...emptyDraft(v), name: draft.name })}
            options={[
              { value: "check", label: t("task.kind.check") },
              { value: "assistant", label: t("task.kind.assistant") },
            ]}
          />
        </Field>
      </div>

      <Field label={t("task.f.when")} hint={t("task.tz", { tz: tzLabel(tz) })}>
        <div className="flex flex-wrap items-end gap-2.5">
          <Segmented
            aria-label={t("task.f.when")}
            value={draft.scheduleKind}
            onValueChange={(v) => set({ scheduleKind: v })}
            options={[
              { value: "every", label: t("task.f.every") },
              { value: "daily", label: t("task.f.daily") },
              { value: "weekly", label: t("task.f.weekly") },
            ]}
          />
          {draft.scheduleKind === "every" ? (
            <div className="flex items-center gap-2">
              <TextField aria-label={t("task.f.count")} inputMode="numeric" className="w-20" value={draft.every} onChange={(e) => set({ every: e.target.value.replace(/\D/g, "").slice(0, 4) })} />
              <Select
                aria-label={t("task.f.count")}
                className="min-w-[120px]"
                value={draft.unit}
                onValueChange={(v) => set({ unit: v as Draft["unit"] })}
                options={[
                  { value: "m", label: t("task.f.unit.m") },
                  { value: "h", label: t("task.f.unit.h") },
                ]}
              />
            </div>
          ) : (
            <div className="flex items-center gap-2">
              {draft.scheduleKind === "weekly" && (
                <Select
                  aria-label={t("task.f.day")}
                  className="min-w-[150px]"
                  value={String(draft.weekday)}
                  onValueChange={(v) => set({ weekday: Number(v) })}
                  options={[1, 2, 3, 4, 5, 6, 0].map((d) => ({ value: String(d), label: t(weekdayKeys[d]!) }))}
                />
              )}
              <TextField aria-label={t("task.f.time")} type="time" className="w-32" value={draft.time} onChange={(e) => set({ time: e.target.value })} />
            </div>
          )}
        </div>
      </Field>

      {draft.kind === "assistant" ? (
        <Field label={t("task.f.prompt")} hint={t("task.f.promptHint")}>
          <textarea
            aria-label={t("task.f.prompt")}
            value={draft.prompt}
            maxLength={4000}
            rows={5}
            placeholder={t("task.f.promptPh")}
            onChange={(e) => set({ prompt: e.target.value })}
            className="min-h-28 w-full resize-y rounded-field border border-line bg-canvas px-3 py-2 text-[13px] leading-snug outline-none focus:border-accent"
          />
          {looksSecret(draft.prompt) && <span className="text-xs text-warn-text">{t("ai.secretHint")}</span>}
        </Field>
      ) : (
        <Field label={t("task.f.steps")} hint={t("task.f.stepsHint")}>
          {tools.isError && <QueryError compact error={tools.error} onRetry={() => void tools.refetch()} />}
          <div className="flex flex-col gap-2.5">
            {draft.steps.map((step, i) => (
              <StepEditor
                key={i}
                n={i + 1}
                step={step}
                tools={(tools.data ?? []).map((tool) => ({ value: tool.name, label: tool.name, hint: tool.description.split(". ")[0]?.slice(0, 90) }))}
                schema={schemas[step.tool] ?? ""}
                onChange={(s) => setStep(i, s)}
                onRemove={draft.steps.length > 1 ? () => set({ steps: draft.steps.filter((_, j) => j !== i) }) : undefined}
              />
            ))}
            {draft.steps.length < 8 && (
              <Button type="button" size="sm" variant="ghost" className="self-start" onClick={() => set({ steps: [...draft.steps, emptyStep()] })}>
                <Icon name="plus" size={12} />
                {t("task.f.addStep")}
              </Button>
            )}
          </div>
        </Field>
      )}

      <div className="flex flex-col gap-3 rounded-field bg-surface-2 p-3">
        <ToggleRow label={t("task.f.telegram")} hint={t("task.f.telegramHint")} checked={draft.notifyTelegram} onChange={(v) => set({ notifyTelegram: v })} />
        <ToggleRow label={t("task.f.enabled")} hint={t("task.f.enabledHint")} checked={draft.enabled} onChange={(v) => set({ enabled: v })} />
      </div>
    </Modal>
  );
}

function StepEditor({
  n,
  step,
  tools,
  schema,
  onChange,
  onRemove,
}: {
  n: number;
  step: StepDraft;
  tools: { value: string; label: string; hint?: string }[];
  schema: string;
  onChange: (s: StepDraft) => void;
  onRemove?: () => void;
}) {
  const t = useT();
  const fields = argFields(schema);
  const listId = useId();
  const options = tools.some((o) => o.value === step.tool) || !step.tool ? tools : [{ value: step.tool, label: step.tool }, ...tools];
  const setCond = (i: number, c: CondDraft) => onChange({ ...step, conditions: step.conditions.map((x, j) => (j === i ? c : x)) });
  return (
    <div className="flex flex-col gap-3 rounded-field border border-line p-3">
      <div className="flex items-center gap-2">
        <b className="text-xs font-semibold text-muted">{t("task.f.step", { n })}</b>
        <Select
          aria-label={t("task.f.tool")}
          className="min-w-0 flex-1"
          value={step.tool}
          onValueChange={(v) => onChange({ ...step, tool: v, args: {}, conditions: step.tool === v ? step.conditions : [] })}
          options={options}
        />
        {onRemove && (
          <Button type="button" size="sm" variant="ghostDanger" aria-label={t("task.f.removeStep")} title={t("task.f.removeStep")} onClick={onRemove}>
            <Icon name="trash" size={13} />
          </Button>
        )}
      </div>
      {fields.length > 0 && (
        <div className="grid gap-2.5 sm:grid-cols-2">
          {fields.map((f) =>
            f.type === "boolean" ? (
              <label key={f.name} className="flex items-center gap-2 text-[13px]" title={f.description}>
                <Switch checked={step.args[f.name] === true} onCheckedChange={(v) => onChange({ ...step, args: { ...step.args, [f.name]: v } })} aria-label={f.name} />
                <span className="font-mono text-xs">{f.name}</span>
              </label>
            ) : f.type === "enum" ? (
              <div key={f.name} className="flex flex-col gap-1.5" title={f.description}>
                <span className="font-mono text-[11px] text-muted">{f.name}</span>
                <Select
                  aria-label={f.name}
                  value={String(step.args[f.name] ?? "")}
                  onValueChange={(v) => onChange({ ...step, args: { ...step.args, [f.name]: v } })}
                  options={[{ value: "", label: "—" }, ...f.options.map((o) => ({ value: o, label: o }))]}
                />
              </div>
            ) : (
              <TextField
                key={f.name}
                label={<span className="font-mono normal-case tracking-normal">{f.name}</span>}
                title={f.description}
                placeholder={f.description.slice(0, 60)}
                inputMode={f.type === "integer" || f.type === "number" ? "decimal" : undefined}
                value={String(step.args[f.name] ?? "")}
                onChange={(e) => onChange({ ...step, args: { ...step.args, [f.name]: e.target.value } })}
              />
            ),
          )}
        </div>
      )}
      <div className="flex flex-col gap-2">
        <span className="text-xs font-semibold">{t("task.f.conditions")}</span>
        {step.conditions.length === 0 && <span className="text-xs text-muted">{t("task.f.noConditions")}</span>}
        <datalist id={listId}>
          {(pathHints[step.tool] ?? []).map((p) => (
            <option key={p} value={p} />
          ))}
        </datalist>
        {step.conditions.map((c, i) => (
          <div key={i} className="grid items-end gap-2 sm:grid-cols-[minmax(0,1.3fr)_minmax(0,1fr)_minmax(0,1fr)_auto]">
            <TextField aria-label={t("task.f.path")} placeholder={t("task.f.path")} mono list={listId} value={c.path} onChange={(e) => setCond(i, { ...c, path: e.target.value })} />
            <Select aria-label={t("task.f.op")} value={c.op} onValueChange={(v) => setCond(i, { ...c, op: v, value: v === "severity_at_least" ? "warning" : c.value })} options={ops.map((op) => ({ value: op, label: t(`task.op.${op}` as MessageKey) }))} />
            {c.op === "severity_at_least" ? (
              <Select aria-label={t("task.f.value")} value={c.value || "warning"} onValueChange={(v) => setCond(i, { ...c, value: v })} options={severities.map((s) => ({ value: s, label: s }))} />
            ) : opNeedsValue(c.op) ? (
              <TextField aria-label={t("task.f.value")} placeholder={t("task.f.value")} value={c.value} onChange={(e) => setCond(i, { ...c, value: e.target.value })} />
            ) : (
              <span />
            )}
            <Button type="button" size="sm" variant="ghost" aria-label={t("task.f.removeCondition")} title={t("task.f.removeCondition")} onClick={() => onChange({ ...step, conditions: step.conditions.filter((_, j) => j !== i) })}>
              <Icon name="x" size={12} />
            </Button>
          </div>
        ))}
        {step.conditions.length < 5 && (
          <div className="flex flex-wrap gap-1.5">
            {conditionPresets.map((p) => (
              <Button key={p.key} type="button" size="xs" variant="secondary" onClick={() => onChange({ ...step, conditions: [...step.conditions, { ...p.cond }] })}>
                <Icon name="plus" size={10} />
                {t(p.key)}
              </Button>
            ))}
          </div>
        )}
      </div>
      <ToggleRow label={t("task.f.errorIsProblem")} checked={step.errorIsProblem} onChange={(v) => onChange({ ...step, errorIsProblem: v })} />
    </div>
  );
}
