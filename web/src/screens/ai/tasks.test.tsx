import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { RunStatus, ScheduleKind, TaskKind } from "@/gen/mistgate/tasks/v1/tasks_pb";
import { setLang } from "@/i18n";
import { meQuery } from "@/lib/session";
import { TasksTab } from "./tasks";

const listTasks = vi.fn();
const listTaskRuns = vi.fn();
const listCheckTools = vi.fn();
const createTask = vi.fn();
const runTaskNow = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  tasks: {
    listTasks: (...a: unknown[]) => listTasks(...a),
    listTaskRuns: (...a: unknown[]) => listTaskRuns(...a),
    listCheckTools: (...a: unknown[]) => listCheckTools(...a),
    createTask: (...a: unknown[]) => createTask(...a),
    updateTask: vi.fn(),
    deleteTask: vi.fn(),
    runTaskNow: (...a: unknown[]) => runTaskNow(...a),
  },
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const f of [listTasks, listTaskRuns, listCheckTools, createTask, runTaskNow]) f.mockReset();
});

const morning = {
  id: "tsk_morning", name: "Morning check", kind: TaskKind.CHECK, enabled: true, notifyTelegram: true, prompt: "", conversationId: "",
  schedule: { kind: ScheduleKind.DAILY, everyMinutes: 0, minuteOfDay: 450, weekday: 0 },
  steps: [{ tool: "fleet_digest", argsJson: '{"hours":24}', conditions: [{ path: "totals.nodes_problem_now", op: "gt", value: "0" }], errorIsProblem: false }],
  createdUnix: 1n, updatedUnix: 1n, nextRunUnix: 1_800_000_000n, failStreak: 0, problem: true, problemSummary: "fleet_digest: totals.nodes_problem_now = 2", running: false,
  lastRun: { id: "run_1", taskId: "tsk_morning", trigger: "schedule", status: RunStatus.PROBLEM, reason: "", summary: "fleet_digest: totals.nodes_problem_now = 2", startedUnix: 1_700_000_000n, finishedUnix: 1_700_000_005n, steps: [], conversationId: "", provider: "", model: "", usd: 0, fellBack: false, actor: "" },
};

async function mount(role: Role, opts: { edge?: boolean; focus?: string } = {}) {
  listTasks.mockResolvedValue({ tasks: [morning], timezoneOffsetMinutes: 180, nowUnix: 1_700_000_100n, maxTasks: 50 });
  listTaskRuns.mockResolvedValue({
    runs: [
      { ...morning.lastRun, steps: [{ tool: "fleet_digest", ok: true, problem: true, error: "", matches: ["totals.nodes_problem_now = 2"], excerpt: '{"totals":{}}', durationMs: 40 }] },
      { id: "run_0", taskId: "tsk_morning", trigger: "missed", status: RunStatus.FAILED, reason: "interrupted", summary: "", startedUnix: 1_699_990_000n, finishedUnix: 1_699_990_010n, steps: [], conversationId: "", provider: "", model: "", usd: 0, fellBack: false, actor: "" },
    ],
  });
  listCheckTools.mockResolvedValue({ tools: [{ name: "fleet_digest", description: "The fleet's problems in one call.", inputSchemaJson: '{"type":"object","properties":{"hours":{"type":"integer"}}}' }] });
  createTask.mockResolvedValue({ task: { ...morning, id: "tsk_new" } });
  runTaskNow.mockResolvedValue({ runId: "run_now" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role }, edition: opts.edge ? "edge" : "vps", unavailable: opts.edge ? ["assistant", "mcp"] : [] } as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <TasksTab focus={opts.focus} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const button = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === text);
const typeInto = (el: HTMLInputElement | null, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el!.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the scheduled tasks", () => {
  it("list each task with its schedule, its last run and the panel's time zone", async () => {
    await mount(Role.OWNER);
    const text = document.body.textContent ?? "";
    for (const want of ["Morning check", "Check", "Daily at 07:30", "Problem", "fleet_digest: totals.nodes_problem_now = 2", "Telegram", "UTC+3"]) expect(text).toContain(want);
    expect(button("New check")).toBeDefined();
    expect(button("Edit")).toBeDefined();
  });

  it("show a helper the tasks and Run now, but no way to write them", async () => {
    await mount(Role.HELPER);
    expect(document.body.textContent).toContain("Morning check");
    expect(button("New check")).toBeUndefined();
    expect(button("Edit")).toBeUndefined();
    await click(button("Run now"));
    await settle();
    expect(runTaskNow).toHaveBeenCalledWith({ id: "tsk_morning" });
  });

  it("are closed to a read-only admin and explained on the Cloudflare edition", async () => {
    await mount(Role.READONLY);
    expect(document.body.textContent).toContain("Only the owner and helpers see the scheduled tasks.");
    expect(listTasks).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();
    await mount(Role.OWNER, { edge: true });
    expect(document.body.textContent).toContain("Not available in the Cloudflare edition yet: Scheduled tasks.");
    expect(listTasks).not.toHaveBeenCalled();
  });

  it("open a task's runs with what each step matched", async () => {
    await mount(Role.OWNER, { focus: "tsk_morning" });
    const text = document.body.textContent ?? "";
    expect(listTaskRuns).toHaveBeenCalled();
    expect(text).toContain("Matched");
    expect(text).toContain("Missed while the panel was down");
    expect(text).toContain("The panel stopped during the run");
  });

  it("create a check from the editor's starting point", async () => {
    await mount(Role.OWNER);
    await click(button("New check"));
    await settle();
    // nothing is sent before the task has a name: the editor says what is missing
    expect(document.body.textContent).toContain("Give the task a name.");
    await click(button("Create"));
    expect(createTask).not.toHaveBeenCalled();
    await typeInto(document.querySelector<HTMLInputElement>('input[placeholder="Morning check"]'), "Fleet each morning");
    await click(button("Create"));
    await settle();
    expect(createTask).toHaveBeenCalledTimes(1);
    const req = createTask.mock.calls[0]![0] as { task: { name: string; kind: TaskKind; enabled: boolean; schedule: { kind: ScheduleKind; minuteOfDay: number }; steps: { tool: string; argsJson: string; conditions: unknown[] }[] } };
    expect(req.task.name).toBe("Fleet each morning");
    expect(req.task.kind).toBe(TaskKind.CHECK);
    expect(req.task.enabled).toBe(true);
    expect(req.task.schedule).toMatchObject({ kind: ScheduleKind.DAILY, minuteOfDay: 450 });
    expect(req.task.steps).toEqual([{ tool: "fleet_digest", argsJson: '{"hours":24}', conditions: [{ path: "totals.nodes_problem_now", op: "gt", value: "0" }], errorIsProblem: false }]);
  });
});
