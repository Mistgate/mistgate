import type { MessageInitShape } from "@bufbuild/protobuf";
import { ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import type { StatusKind } from "@/components/ui/status";
import { RunStatus, ScheduleKind, TaskKind, type ListTasksResponse, type Task, type TaskInputSchema, type TaskRun } from "@/gen/mistgate/tasks/v1/tasks_pb";
import type { T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { tasks } from "./api";
import { errorCode, errorText, errorVars } from "./errors";
import { plain, type Plain } from "./plain";

// The owner's scheduled tasks (mistgate.tasks.v1, the VPS edition): the queries of the Tasks tab of /ai, and the pure helpers
// behind its list and its editor. Query keys sit under "tasks".

export type TaskP = Plain<Task>;
export type RunP = Plain<TaskRun>;
export type TasksList = Plain<ListTasksResponse>;

/** A run is going on somewhere: the list polls faster. */
export const tasksPollMs = (d?: TasksList) => (d?.tasks.some((t) => t.running) ? 3_000 : 30_000);

export const tasksQuery = queryOptions({
  queryKey: ["tasks", "list"],
  queryFn: async ({ signal }) => plain(await tasks.listTasks({}, { signal })),
  refetchInterval: (q) => tasksPollMs(q.state.data),
});

export const taskRunsQuery = (taskId: string, running: boolean) =>
  queryOptions({
    queryKey: ["tasks", "runs", taskId],
    queryFn: async ({ signal }) => plain(await tasks.listTaskRuns({ taskId, limit: 50 }, { signal })).runs,
    refetchInterval: running ? 3_000 : 30_000,
  });

export const checkToolsQuery = queryOptions({
  queryKey: ["tasks", "tools"],
  queryFn: async ({ signal }) => plain(await tasks.listCheckTools({}, { signal })).tools,
  staleTime: 10 * 60_000,
});

/** The panel's time zone as people write it: "UTC", "UTC+3", "UTC−4:30". */
export function tzLabel(offsetMin: number): string {
  if (!offsetMin) return "UTC";
  const sign = offsetMin > 0 ? "+" : "−";
  const abs = Math.abs(offsetMin);
  const h = Math.floor(abs / 60);
  const m = abs % 60;
  return `UTC${sign}${h}${m ? `:${String(m).padStart(2, "0")}` : ""}`;
}

export const hhmm = (minuteOfDay: number) => `${String(Math.floor(minuteOfDay / 60)).padStart(2, "0")}:${String(minuteOfDay % 60).padStart(2, "0")}`;

/** "07:30" -> 450; anything else -> null. */
export function parseHHMM(s: string): number | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(s.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  return h < 24 && min < 60 ? h * 60 + min : null;
}

export const weekdayKeys = ["task.day.0", "task.day.1", "task.day.2", "task.day.3", "task.day.4", "task.day.5", "task.day.6"] as const satisfies readonly MessageKey[];

/** A task's schedule in words: "Every 15 minutes", "Daily at 07:30", "Mondays at 09:00". */
export function scheduleText(t: T, s: TaskP["schedule"]): string {
  if (!s) return "";
  switch (s.kind) {
    case ScheduleKind.EVERY:
      if (s.everyMinutes % 1440 === 0) return t.n("task.sched.everyDays", s.everyMinutes / 1440);
      if (s.everyMinutes % 60 === 0) return t.n("task.sched.everyHours", s.everyMinutes / 60);
      return t.n("task.sched.everyMinutes", s.everyMinutes);
    case ScheduleKind.DAILY:
      return t("task.sched.dailyAt", { time: hhmm(s.minuteOfDay) });
    case ScheduleKind.WEEKLY:
      return t("task.sched.weeklyAt", { day: t(weekdayKeys[s.weekday] ?? "task.day.1"), time: hhmm(s.minuteOfDay) });
    default:
      return "";
  }
}

/** What a run's status looks like. */
export function runKind(s: RunStatus): StatusKind {
  switch (s) {
    case RunStatus.OK:
      return "ok";
    case RunStatus.PROBLEM:
      return "warn";
    case RunStatus.FAILED:
      return "bad";
    case RunStatus.RUNNING:
      return "busy";
    default:
      return "off";
  }
}

export const runWord = (s: RunStatus): MessageKey =>
  s === RunStatus.OK
    ? "task.run.ok"
    : s === RunStatus.PROBLEM
      ? "task.run.problem"
      : s === RunStatus.FAILED
        ? "task.run.failed"
        : s === RunStatus.RUNNING
          ? "task.run.running"
          : "task.run.skipped";

/** The words of a run's reason code ("provider_unreachable"); an unknown code reads as sent. */
export function reasonText(t: T, code: string): string {
  if (!code) return "";
  const key = `task.reason.${code}`;
  return hasKey(key) ? t(key) : code;
}

/** The words of a TasksService refusal ("schedule_too_often: min=15"), else the panel's own sentence. */
export function taskErrorText(e: unknown, t: T): string {
  const raw = ConnectError.from(e).rawMessage;
  const key = `task.err.${errorCode(raw)}`;
  return hasKey(key) ? t(key, errorVars(raw)) : errorText(e, t);
}

const hasKey = (k: string): k is MessageKey => Object.hasOwn(en, k);

// ---------------------------------------------------------------------------------------------------------------------
// the editor

export type CondDraft = { path: string; op: string; value: string };
export type StepDraft = { tool: string; args: Record<string, string | boolean>; conditions: CondDraft[]; errorIsProblem: boolean };
export type Draft = {
  name: string;
  kind: "check" | "assistant";
  enabled: boolean;
  scheduleKind: "every" | "daily" | "weekly";
  every: string; // the count of `unit`, as typed
  unit: "m" | "h";
  time: string; // "07:30"
  weekday: number;
  steps: StepDraft[];
  prompt: string;
  notifyTelegram: boolean;
};

export const ops = ["severity_at_least", "gt", "gte", "lt", "lte", "eq", "ne", "in", "not_in", "nonempty", "contains"] as const;
export const opNeedsValue = (op: string) => op !== "nonempty";
export const severities = ["info", "warning", "error", "critical"] as const;

export const emptyStep = (tool = ""): StepDraft => ({ tool, args: {}, conditions: [], errorIsProblem: false });

export function emptyDraft(kind: Draft["kind"]): Draft {
  return {
    name: "", kind, enabled: true, scheduleKind: "daily", every: kind === "assistant" ? "60" : "15", unit: "m", time: "07:30", weekday: 1,
    steps: kind === "check" ? [{ ...emptyStep("fleet_digest"), args: { hours: "24" }, conditions: [{ path: "totals.nodes_problem_now", op: "gt", value: "0" }] }] : [],
    prompt: "", notifyTelegram: false,
  };
}

/** The draft of a stored task. */
export function draftOf(task: TaskP): Draft {
  const s = task.schedule;
  const every = s?.everyMinutes ?? 15;
  const hours = every % 60 === 0;
  const kind = s?.kind === ScheduleKind.EVERY ? "every" : s?.kind === ScheduleKind.WEEKLY ? "weekly" : "daily";
  return {
    name: task.name, kind: task.kind === TaskKind.ASSISTANT ? "assistant" : "check", enabled: task.enabled, scheduleKind: kind,
    every: String(hours ? every / 60 : every), unit: hours ? "h" : "m", time: hhmm(s?.minuteOfDay ?? 450), weekday: s?.weekday ?? 1,
    steps: task.steps.map((st) => ({ tool: st.tool, args: fieldsOfArgs(st.argsJson), conditions: st.conditions.map((c) => ({ ...c })), errorIsProblem: st.errorIsProblem })),
    prompt: task.prompt, notifyTelegram: task.notifyTelegram,
  };
}

/** One argument of a tool as the editor shows it, from the tool's JSON schema. */
export type ArgField = { name: string; type: "string" | "integer" | "number" | "boolean" | "list" | "enum"; options: string[]; description: string; required: boolean };

type JsonSchema = { type?: string | string[]; properties?: Record<string, JsonSchema>; required?: string[]; enum?: unknown[]; description?: string; items?: JsonSchema };

const typeOf = (s: JsonSchema) => (Array.isArray(s.type) ? s.type.find((x) => x !== "null") : s.type) ?? "";

/** The arguments the editor can fill: strings (a list when the schema enumerates them), numbers, true or false, and lists of strings. */
export function argFields(schemaJson: string): ArgField[] {
  let schema: JsonSchema;
  try {
    schema = JSON.parse(schemaJson) as JsonSchema;
  } catch {
    return [];
  }
  const required = new Set(schema.required ?? []);
  const out: ArgField[] = [];
  for (const [name, p] of Object.entries(schema.properties ?? {})) {
    const ty = typeOf(p);
    const base = { name, options: [] as string[], description: p.description ?? "", required: required.has(name) };
    if (p.enum?.every((v) => typeof v === "string")) out.push({ ...base, type: "enum", options: p.enum as string[] });
    else if (ty === "string") out.push({ ...base, type: "string" });
    else if (ty === "integer" || ty === "number") out.push({ ...base, type: ty });
    else if (ty === "boolean") out.push({ ...base, type: "boolean" });
    else if (ty === "array" && typeOf(p.items ?? {}) === "string") out.push({ ...base, type: "list" });
  }
  return out;
}

/** A step's stored arguments as the editor's fields: numbers and lists as text. */
export function fieldsOfArgs(argsJson: string): Record<string, string | boolean> {
  let obj: Record<string, unknown>;
  try {
    obj = JSON.parse(argsJson || "{}") as Record<string, unknown>;
  } catch {
    return {};
  }
  const out: Record<string, string | boolean> = {};
  for (const [k, v] of Object.entries(obj ?? {})) {
    if (typeof v === "boolean") out[k] = v;
    else if (Array.isArray(v)) out[k] = v.join(", ");
    else if (v !== null && typeof v !== "object") out[k] = String(v);
  }
  return out;
}

/** The fields as the tool's arguments, JSON: empty fields left out, numbers as numbers, a list split at its commas. */
export function argsOfFields(fields: ArgField[], values: Record<string, string | boolean>): string {
  const out: Record<string, unknown> = {};
  for (const f of fields) {
    const v = values[f.name];
    if (v === undefined || v === "" || v === false) continue;
    if (f.type === "boolean") out[f.name] = v === true;
    else if (f.type === "integer" || f.type === "number") {
      const n = Number(String(v).replace(",", "."));
      out[f.name] = Number.isFinite(n) ? (f.type === "integer" ? Math.trunc(n) : n) : String(v);
    } else if (f.type === "list")
      out[f.name] = String(v)
        .split(",")
        .map((s) => s.trim())
        .filter(Boolean);
    else out[f.name] = String(v);
  }
  return JSON.stringify(out);
}

/** The draft as the API takes it; schemas give each step's tool its fields. */
export function inputOf(d: Draft, schemas: Record<string, string>): MessageInitShape<typeof TaskInputSchema> {
  const minuteOfDay = parseHHMM(d.time) ?? 0;
  const count = Math.max(0, Math.trunc(Number(d.every) || 0));
  const schedule =
    d.scheduleKind === "every"
      ? { kind: ScheduleKind.EVERY, everyMinutes: d.unit === "h" ? count * 60 : count, minuteOfDay: 0, weekday: 0 }
      : d.scheduleKind === "weekly"
        ? { kind: ScheduleKind.WEEKLY, everyMinutes: 0, minuteOfDay, weekday: d.weekday }
        : { kind: ScheduleKind.DAILY, everyMinutes: 0, minuteOfDay, weekday: 0 };
  return {
    name: d.name.trim(),
    kind: d.kind === "assistant" ? TaskKind.ASSISTANT : TaskKind.CHECK,
    enabled: d.enabled,
    schedule,
    steps:
      d.kind === "check"
        ? d.steps.map((st) => ({
            tool: st.tool,
            argsJson: argsOfFields(argFields(schemas[st.tool] ?? "{}"), st.args),
            conditions: st.conditions.map((c) => ({ path: c.path.trim(), op: c.op, value: opNeedsValue(c.op) ? c.value.trim() : "" })),
            errorIsProblem: st.errorIsProblem,
          }))
        : [],
    prompt: d.kind === "assistant" ? d.prompt.trim() : "",
    notifyTelegram: d.notifyTelegram,
  };
}

/** The shortest interval of a kind, in minutes (spec.MinCheckEvery / MinAssistantEvery). */
export const minEvery = (kind: Draft["kind"]) => (kind === "assistant" ? 15 : 5);

/** What keeps the draft from being saved, as a message key, or null. The panel checks everything again. */
export function draftProblem(d: Draft): MessageKey | null {
  if (!d.name.trim()) return "task.need.name";
  if (d.scheduleKind === "every") {
    const n = Math.trunc(Number(d.every) || 0) * (d.unit === "h" ? 60 : 1);
    if (n < minEvery(d.kind)) return d.kind === "assistant" ? "task.need.every15" : "task.need.every5";
  } else if (parseHHMM(d.time) === null) return "task.need.time";
  if (d.kind === "assistant") return d.prompt.trim() ? null : "task.need.prompt";
  if (d.steps.length === 0 || d.steps.some((s) => !s.tool)) return "task.need.step";
  if (d.steps.some((s) => s.conditions.some((c) => opNeedsValue(c.op) && !c.value.trim()))) return "task.need.value";
  return null;
}

/** Conditions a step can start from: the editor offers them as one click. */
export const conditionPresets = [
  { key: "task.preset.severity", cond: { path: "**.severity", op: "severity_at_least", value: "warning" } },
  { key: "task.preset.notOnline", cond: { path: "nodes[].status", op: "not_in", value: "online, updating" } },
  { key: "task.preset.above", cond: { path: "", op: "gt", value: "0" } },
  { key: "task.preset.nonempty", cond: { path: "", op: "nonempty", value: "" } },
] as const satisfies readonly { key: MessageKey; cond: CondDraft }[];

/** Fields of a tool's result worth a condition, offered as suggestions in the path box. */
export const pathHints: Record<string, string[]> = {
  fleet_status: ["nodes_problem", "alerts_critical", "nodes[].status"],
  fleet_digest: ["totals.nodes_problem_now", "totals.alerts_critical", "totals.outages", "problem_nodes"],
  access_report: ["**.severity"],
  block_diagnosis: ["**.class", "**.confidence"],
  path_probe: ["ports[].verdict"],
  alerts_list: ["**.severity"],
  user_diagnose: ["verdict", "**.severity"],
};
