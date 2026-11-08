import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { RunStatus, ScheduleKind, TaskKind } from "@/gen/mistgate/tasks/v1/tasks_pb";
import { dictionaries, fill, pickForm, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import {
  argFields,
  argsOfFields,
  draftOf,
  draftProblem,
  emptyDraft,
  fieldsOfArgs,
  inputOf,
  parseHHMM,
  reasonText,
  runKind,
  scheduleText,
  taskErrorText,
  tzLabel,
  type TaskP,
} from "./tasks";

const tFor = (lang: keyof typeof dictionaries): T => {
  const d = dictionaries[lang];
  const t = (k: MessageKey, v?: Record<string, string | number>) => fill(d[k], v);
  return Object.assign(t, { n: (k: MessageKey, n: number, v?: Record<string, string | number>) => fill(pickForm(d[k], n, lang), { n, ...v }) });
};
const en = tFor("en");
const ru = tFor("ru");

const schema = JSON.stringify({
  type: "object",
  properties: {
    hours: { type: "integer", description: "window" },
    node: { type: "string" },
    kind: { type: "string", enum: ["udp", "tcp", "tls"] },
    stored_only: { type: "boolean" },
    sources: { type: "array", items: { type: "string" } },
    nested: { type: "object" },
  },
  required: ["node"],
});

describe("schedules in words", () => {
  it("names the interval, the day and the time", () => {
    expect(scheduleText(en, { kind: ScheduleKind.EVERY, everyMinutes: 15, minuteOfDay: 0, weekday: 0 } as never)).toBe("Every 15 minutes");
    expect(scheduleText(en, { kind: ScheduleKind.EVERY, everyMinutes: 60, minuteOfDay: 0, weekday: 0 } as never)).toBe("Every hour");
    expect(scheduleText(en, { kind: ScheduleKind.EVERY, everyMinutes: 2880, minuteOfDay: 0, weekday: 0 } as never)).toBe("Every 2 days");
    expect(scheduleText(en, { kind: ScheduleKind.DAILY, everyMinutes: 0, minuteOfDay: 450, weekday: 0 } as never)).toBe("Daily at 07:30");
    expect(scheduleText(en, { kind: ScheduleKind.WEEKLY, everyMinutes: 0, minuteOfDay: 540, weekday: 1 } as never)).toBe("Weekly, Monday at 09:00");
    expect(scheduleText(ru, { kind: ScheduleKind.EVERY, everyMinutes: 120, minuteOfDay: 0, weekday: 0 } as never)).toBe("Каждые 2 часа");
    expect(scheduleText(ru, { kind: ScheduleKind.EVERY, everyMinutes: 25, minuteOfDay: 0, weekday: 0 } as never)).toBe("Каждые 25 минут");
  });
  it("writes the panel's zone as people do", () => {
    expect(tzLabel(0)).toBe("UTC");
    expect(tzLabel(180)).toBe("UTC+3");
    expect(tzLabel(330)).toBe("UTC+5:30");
    expect(tzLabel(-270)).toBe("UTC−4:30");
    expect(parseHHMM("7:05")).toBe(425);
    expect(parseHHMM("24:00")).toBeNull();
  });
});

describe("the editor's arguments", () => {
  it("reads the simple fields of a tool's schema", () => {
    const fields = argFields(schema);
    expect(fields.map((f) => `${f.name}:${f.type}`)).toEqual(["hours:integer", "node:string", "kind:enum", "stored_only:boolean", "sources:list"]);
    expect(fields.find((f) => f.name === "node")?.required).toBe(true);
    expect(argFields("not json")).toEqual([]);
  });
  it("turns the fields into arguments and back, leaving empty ones out", () => {
    const fields = argFields(schema);
    const json = argsOfFields(fields, { hours: "24", node: "de1", kind: "", stored_only: true, sources: "agent, xray ," });
    expect(JSON.parse(json)).toEqual({ hours: 24, node: "de1", stored_only: true, sources: ["agent", "xray"] });
    expect(fieldsOfArgs(json)).toEqual({ hours: "24", node: "de1", stored_only: true, sources: "agent, xray" });
    expect(fieldsOfArgs("{bad")).toEqual({});
  });
});

describe("a draft", () => {
  it("starts as a morning check of the fleet and becomes the API's input", () => {
    const d = { ...emptyDraft("check"), name: " Morning check " };
    expect(draftProblem(d)).toBeNull();
    const input = inputOf(d, { fleet_digest: JSON.stringify({ type: "object", properties: { hours: { type: "integer" } } }) });
    expect(input.name).toBe("Morning check");
    expect(input.kind).toBe(TaskKind.CHECK);
    expect(input.schedule).toEqual({ kind: ScheduleKind.DAILY, everyMinutes: 0, minuteOfDay: 450, weekday: 0 });
    expect(input.steps?.[0]).toEqual({ tool: "fleet_digest", argsJson: '{"hours":24}', conditions: [{ path: "totals.nodes_problem_now", op: "gt", value: "0" }], errorIsProblem: false });
  });
  it("says what keeps it from being saved", () => {
    const check = emptyDraft("check");
    expect(draftProblem(check)).toBe("task.need.name");
    expect(draftProblem({ ...check, name: "x", scheduleKind: "every", every: "4" })).toBe("task.need.every5");
    expect(draftProblem({ ...emptyDraft("assistant"), name: "x", scheduleKind: "every", every: "10", unit: "m" })).toBe("task.need.every15");
    expect(draftProblem({ ...emptyDraft("assistant"), name: "x" })).toBe("task.need.prompt");
    expect(draftProblem({ ...check, name: "x", steps: [{ tool: "", args: {}, conditions: [], errorIsProblem: false }] })).toBe("task.need.step");
    expect(draftProblem({ ...check, name: "x", steps: [{ tool: "fleet_digest", args: {}, conditions: [{ path: "a", op: "gt", value: "" }], errorIsProblem: false }] })).toBe("task.need.value");
    expect(draftProblem({ ...check, name: "x", steps: [{ tool: "fleet_digest", args: {}, conditions: [{ path: "a", op: "nonempty", value: "" }], errorIsProblem: false }] })).toBeNull();
  });
  it("of a stored task keeps every field", () => {
    const task = {
      id: "tsk_1", name: "Probe", kind: TaskKind.CHECK, enabled: false, notifyTelegram: true, prompt: "",
      schedule: { kind: ScheduleKind.EVERY, everyMinutes: 120, minuteOfDay: 0, weekday: 0 },
      steps: [{ tool: "path_probe", argsJson: '{"from":"de1","to":"nl1"}', conditions: [{ path: "ports[].verdict", op: "ne", value: "ok" }], errorIsProblem: true }],
    } as unknown as TaskP;
    const d = draftOf(task);
    expect(d).toMatchObject({ name: "Probe", kind: "check", enabled: false, notifyTelegram: true, scheduleKind: "every", every: "2", unit: "h" });
    expect(d.steps[0]).toEqual({ tool: "path_probe", args: { from: "de1", to: "nl1" }, conditions: [{ path: "ports[].verdict", op: "ne", value: "ok" }], errorIsProblem: true });
  });
});

describe("runs and refusals", () => {
  it("words statuses, reasons and the panel's refusals", () => {
    expect(runKind(RunStatus.PROBLEM)).toBe("warn");
    expect(runKind(RunStatus.FAILED)).toBe("bad");
    expect(reasonText(en, "budget_reached")).toBe("This month's budget is spent");
    expect(reasonText(en, "something_new")).toBe("something_new");
    expect(taskErrorText(new ConnectError("schedule_too_often: min=15", Code.InvalidArgument), en)).toBe("This kind of task runs at most every 15 minutes.");
    expect(taskErrorText(new ConnectError("too_soon", Code.FailedPrecondition), ru)).toBe("Задача только что запускалась. Попробуй через несколько минут.");
  });
});
