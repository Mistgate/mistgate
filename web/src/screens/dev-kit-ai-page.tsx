import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { StepUpProvider } from "@/components/step-up";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { AssistantAudience, AssistantEffort, AssistantProvider } from "@/gen/mistgate/admin/v1/assistant_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { ApprovalState, TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { RunStatus, ScheduleKind, TaskKind } from "@/gen/mistgate/tasks/v1/tasks_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { approvals as approvalsApi, apiTokens, assistant as assistantApi, provisioning, tasks as tasksApi } from "@/lib/api";
import { accessQuery, conversationsQuery, memoryQuery, settingsQuery } from "@/lib/assistant";
import { setEffectsEnabled } from "@/lib/effects";
import { approvalsQuery, awaitingQuery, tokensQuery } from "@/lib/integrations";
import { nodesQuery, overviewQuery, userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { checkToolsQuery, tasksQuery } from "@/lib/tasks";
import { setTheme, useTheme } from "@/lib/theme";
import { Shot } from "@/screens/dev-kit-redesign";
import { AiView, type AiScene } from "@/screens/ai";
import { SceneProvider } from "@/screens/ai/bits";
import type { AiTab } from "@/screens/ai/tabs";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-ai: the AI screen of the redesign (PnAI) in every state of the board, each in a frame of its own width (an
// iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the side-by-side
// with the board:
//   /dev-kit?only=pn-ai-page&state=<tab>.<what>[.<what>…][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly]
//                            [&fleet=0|1|4|12][&issues=0|1|4|9][&fx=1]
// <tab>: as (the assistant) | mem (memory) | tk (scheduled tasks) | mcp (tokens and MCP) | ap (waiting for you).
// what: the board's switches — "Данные": load | err; "Ассистент": off | noprov | key | bud; "Память": empty; "Задачи": empty | fail;
//   "Токены": empty | nocopy; "Одобрение": soon | exp | none; and what is open — as: dd-chat | dd-fall | dlg-connect-<p> | dlg-replace-<p> |
//   dlg-remove-<p> | keyerr | keyfilled | open-<p> | shut-<p> | dirty (<p>: anthropic | deepseek | zai | ollama | custom1); mem: new-note | edit-note |
//   ask-note | fresh-note | dlg-forget | ask-conv | dlg-wipe; tk: runs | running | ask-task | dlg-task | dlg-task-as | dlg-task-edit | tried; mcp: dlg-tok |
//   op | tried | dlg-secret | dlg-rev | dlg-lim | old; ap: install | many; any: edge (the Cloudflare edition).
// Made-up data of the board: the nodes de1, nl1, fi1 and msk1, the admin "owner", documentation addresses and not-a-real-token secrets.

export type Tab = "as" | "mem" | "tk" | "mcp" | "ap";
export type Parsed = { tab: Tab; mods: Set<string> };
const tabOf: Record<Tab, AiTab> = { as: "assistant", mem: "memory", tk: "tasks", mcp: "mcp", ap: "approvals" };

export function parse(state: string): Parsed {
  const [t = "as", ...mods] = state.split(".");
  return { tab: (["as", "mem", "tk", "mcp", "ap"].includes(t) ? t : "as") as Tab, mods: new Set(mods) };
}

const NOW = Math.floor(Date.now() / 1000);
const H = 3600;
const D = 86_400;

// ---- the assistant
type Status = Record<string, unknown>;
const status = (provider: AssistantProvider, over: Status = {}): Status => ({
  provider,
  connected: false,
  keyHint: "",
  addedUnix: 0,
  choice: { provider, model: "", budgetUsdMonth: 20 },
  spentUsdMonth: 0,
  lastErrorCode: "",
  lastErrorUnix: 0,
  customName: "",
  customUrl: "",
  allowLocal: false,
  inputUsdPerMtok: 0,
  outputUsdPerMtok: 0,
  ...over,
});

export function settingsData(mods: ReadonlySet<string>) {
  const noProv = mods.has("noprov");
  const bud = mods.has("bud");
  const key = mods.has("key");
  const on = (p: AssistantProvider, hint: string, over: Status = {}) => status(p, { connected: !noProv, keyHint: hint, addedUnix: NOW - 3 * D, ...over });
  return {
    settings: { enabled: !mods.has("off"), provider: AssistantProvider.ANTHROPIC, fallbackProvider: AssistantProvider.UNSPECIFIED, effort: AssistantEffort.NORMAL, audience: AssistantAudience.OWNER, panelNotes: "nl1 is the test node for new profiles; msk1 sits in Moscow and is only for people in Russia.", retentionDays: 90 },
    providers: [
      on(AssistantProvider.ANTHROPIC, "Qx7d", { choice: { provider: AssistantProvider.ANTHROPIC, model: "claude-sonnet-5-5", budgetUsdMonth: 20 }, spentUsdMonth: bud ? 20 : 3.41, lastErrorCode: key ? "bad_key" : "" }),
      on(AssistantProvider.DEEPSEEK, "k3Rt"),
      status(AssistantProvider.ZAI),
      status(AssistantProvider.OLLAMA_CLOUD),
      status(AssistantProvider.CUSTOM_1),
    ],
    usage: [{ adminId: "adm_1", adminName: "owner", usd: bud ? 20 : 4.03, turns: bud ? 612 : 118 }],
  };
}

const models = [
  { id: "claude-opus-5-5", name: "Claude Opus 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 5, outputUsdPerMtok: 25, recommended: false },
  { id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 3, outputUsdPerMtok: 15, recommended: true },
  { id: "claude-haiku-5-5", name: "Claude Haiku 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 1, outputUsdPerMtok: 5, recommended: false },
];

// ---- memory
export function memoryData(mods: ReadonlySet<string>) {
  if (mods.has("empty")) return { notes: [], conversations: [] };
  const note = (id: string, text: string, byAssistant: boolean, ago: number) => ({ id, text, createdUnix: NOW - ago, updatedUnix: NOW - ago, byAssistant, conversationId: byAssistant ? "cnv_1" : "" });
  const conv = (id: string, title: string, ago: number, messages: number, over: Record<string, unknown> = {}) => ({ id, title, createdUnix: NOW - ago - 600, updatedUnix: NOW - ago, messages, taskId: "", unread: false, followUpUnix: 0, ...over });
  return {
    notes: [
      note("mem_1", "nl1 is a test node, its UDP alerts can be muted for a day.", false, 9 * D),
      note("mem_2", "Alice and Bob are friends of the owner: do not touch their quota without a request.", true, 5 * D),
      note("mem_3", "The systemd journal on de1 swells every two weeks; clean it with journald_vacuum.", true, 2 * D),
    ],
    conversations: [
      conv("cnv_1", "Why de1 filled its journal", 2 * D, 8),
      conv("cnv_2", "Weekly fleet digest", 4 * D, 2, { taskId: "tsk_digest" }),
      conv("cnv_3", "Alice's quota and the device limit", 21 * D, 6),
      conv("cnv_4", "How to add the node fi1", 40 * D, 12),
    ],
  };
}

// ---- tasks
const run = (id: string, taskId: string, st: RunStatus, ago: number, took: number, over: Record<string, unknown> = {}) => ({
  id, taskId, trigger: "schedule", status: st, reason: "", summary: "", startedUnix: NOW - ago, finishedUnix: NOW - ago + took, steps: [], conversationId: "", provider: "", model: "", usd: 0, fellBack: false, actor: "", ...over,
});
const step = (tool: string, over: Record<string, unknown> = {}) => ({ tool, ok: true, problem: false, error: "", matches: [], excerpt: "", durationMs: 1800, ...over });

export function tasksData(mods: ReadonlySet<string>) {
  if (mods.has("empty")) return { list: { tasks: [], timezoneOffsetMinutes: 180, nowUnix: NOW, maxTasks: 50 }, runs: {} as Record<string, unknown[]> };
  const fail = mods.has("fail");
  const base = { enabled: true, notifyTelegram: true, prompt: "", conversationId: "", version: 3, createdUnix: NOW - 30 * D, updatedUnix: NOW - 5 * D, failStreak: 0, problem: false, problemSummary: "", running: false };
  const check = (id: string, name: string, schedule: Record<string, unknown>, over: Record<string, unknown>) => ({
    ...base, id, name, kind: TaskKind.CHECK, schedule,
    steps: [{ tool: "fleet_digest", argsJson: '{"hours":24}', conditions: [{ path: "totals.nodes_problem_now", op: "gt", value: "0" }], errorIsProblem: false }],
    ...over,
  });
  const list = [
    check("tsk_morning", "Morning fleet round", { kind: ScheduleKind.DAILY, everyMinutes: 0, minuteOfDay: 480, weekday: 0 }, {
      nextRunUnix: NOW + 11 * H, lastRun: run("run_m1", "tsk_morning", RunStatus.OK, 6 * H, 2, { summary: "" }), running: mods.has("running"),
    }),
    check("tsk_doctor", "Node doctor every 6 hours", { kind: ScheduleKind.EVERY, everyMinutes: 360, minuteOfDay: 0, weekday: 0 }, {
      nextRunUnix: NOW + 3 * H, problem: true, lastRun: run("run_d1", "tsk_doctor", RunStatus.PROBLEM, 2 * H, 2, { summary: "de1: journald_size" }), notifyTelegram: true,
    }),
    {
      ...base, id: "tsk_digest", name: "Weekly digest", kind: TaskKind.ASSISTANT, schedule: { kind: ScheduleKind.WEEKLY, everyMinutes: 0, minuteOfDay: 540, weekday: 1 }, steps: [], prompt: "Collect the week's digest: which nodes fell, whose traffic is running out.",
      nextRunUnix: NOW + 5 * D, failStreak: fail ? 3 : 0, problem: fail,
      lastRun: fail
        ? run("run_w1", "tsk_digest", RunStatus.FAILED, 4 * D, 30, { summary: "The provider did not answer. No fallback provider is set for tasks", reason: "provider_unreachable" })
        : run("run_w1", "tsk_digest", RunStatus.OK, 4 * D, 41, { summary: "", conversationId: "cnv_2", provider: "Anthropic", usd: 0.06 }),
    },
  ];
  const runs: Record<string, unknown[]> = {
    tsk_morning: mods.has("running") ? [] : [],
    tsk_doctor: [
      run("run_d1", "tsk_doctor", RunStatus.PROBLEM, 2 * H, 2.4, { summary: "de1: journald_size", steps: [step("fleet_digest", { problem: true, matches: ["problem_nodes = de1: journald_size"], excerpt: '{"problem_nodes":["de1"]}' })] }),
      run("run_d2", "tsk_doctor", RunStatus.OK, 8 * H, 2.1),
      run("run_d3", "tsk_doctor", RunStatus.OK, 14 * H, 2.2),
    ],
    tsk_digest: fail
      ? [run("run_w1", "tsk_digest", RunStatus.FAILED, 4 * D, 30, { summary: "The provider did not answer", reason: "provider_unreachable" }), run("run_w0", "tsk_digest", RunStatus.FAILED, 11 * D, 30, { summary: "The provider did not answer", reason: "provider_unreachable" })]
      : [run("run_w1", "tsk_digest", RunStatus.OK, 4 * D, 41, { conversationId: "cnv_2", provider: "Anthropic", usd: 0.06 }), run("run_w0", "tsk_digest", RunStatus.OK, 11 * D, 38, { conversationId: "cnv_5", provider: "Anthropic", usd: 0.05 })],
  };
  return { list: { tasks: list, timezoneOffsetMinutes: 180, nowUnix: NOW, maxTasks: 50 }, runs };
}

const tools = [{ name: "fleet_digest", description: "The fleet's problems in one call.", inputSchemaJson: '{"type":"object","properties":{"hours":{"type":"integer","description":"How many hours back"}}}' }, { name: "fleet_status", description: "Where each node stands now.", inputSchemaJson: '{"type":"object","properties":{}}' }];

// ---- tokens and approvals
export function tokensData(mods: ReadonlySet<string>) {
  if (mods.has("empty")) return { tokens: [], nowUnix: NOW };
  const tok = (id: string, name: string, profile: TokenProfile, hint: string, over: Record<string, unknown> = {}) => ({
    id, name, profile, createdUnix: NOW - 20 * D, expiresUnix: NOW + 45 * D, lastUsedUnix: NOW - 12 * 60, lastUsedIp: "198.51.100.77", lastUsedVia: TokenChannel.MCP, revokedUnix: 0, createdByName: "owner", rateLimitPerMin: 120, hint, allowBulkUserChanges: false, ...over,
  });
  return {
    tokens: [
      tok("tok_1", "claude-ops", TokenProfile.OPERATOR, "x4Qe"),
      tok("tok_2", "grafana-readonly", TokenProfile.READONLY, "m9Lc", { lastUsedUnix: NOW - 5, lastUsedIp: "203.0.113.90", lastUsedVia: TokenChannel.API, rateLimitPerMin: 60, expiresUnix: NOW + 250 * D }),
      tok("tok_3", "backup-cron", TokenProfile.READONLY, "r7Tz", { expiresUnix: NOW - 46 * D, lastUsedUnix: 0, lastUsedIp: "", lastUsedVia: TokenChannel.UNSPECIFIED }),
    ],
    nowUnix: NOW,
  };
}

const fact = (key: string, value: string, over: Record<string, unknown> = {}) => ({ key, value, untrusted: false, code: "", params: {}, untrustedParams: [], ...over });
const plan = (id: string, tool: string, state: ApprovalState, over: Record<string, unknown> = {}) => ({
  id, tokenId: "tok_1", tokenName: "claude-ops", tokenProfile: TokenProfile.OPERATOR, tool, facts: [], danger: [], reason: "", state, createdUnix: NOW - 60, expiresUnix: NOW + 400, decidedByName: "", decidedUnix: 0, appliedUnix: 0, result: "", error: "", outcomeCode: "", outcomeParams: {}, ...over,
});

export function approvalsData(mods: ReadonlySet<string>) {
  const expiresUnix = mods.has("soon") ? NOW + 48 : mods.has("exp") ? NOW - 1 : NOW + 417;
  const waiting = !mods.has("none") && !mods.has("empty");
  const list = [
    ...(waiting && mods.has("install")
      ? [plan("pln_in", "node_install", ApprovalState.AWAITING, { facts: [fact("host", "203.0.113.9"), fact("host_key", "SHA256:Qm9y3xk2Jv0eVwUQxk1v2Tn8sY4GmR7pL5dXc1aF0hE"), fact("host_key_algorithm", "ssh-ed25519")], expiresUnix, createdUnix: expiresUnix - 420 })]
      : waiting
        ? [
            plan("pln_1", "node_fix", ApprovalState.AWAITING, {
              facts: [fact("node", "de1", { untrusted: true }), fact("fix", "journald_vacuum"), fact("effect", "journal 3870 MB to 500 MB")],
              reason: "The systemd journal on de1 is eight times over its cap; vacuuming is safe and frees 3.3 GB.",
              expiresUnix,
              createdUnix: expiresUnix - 420,
            }),
          ]
        : []),
    plan("pln_h1", "alert_mute", ApprovalState.APPLIED, { decidedByName: "owner", decidedUnix: NOW - 4 * H, appliedUnix: NOW - 4 * H, result: "muted for 6 h", createdUnix: NOW - 4 * H - 120 }),
    plan("pln_h2", "rollout_start", ApprovalState.APPLIED, { decidedByName: "owner", decidedUnix: NOW - 4 * H, appliedUnix: NOW - 4 * H, result: "rollout rol_1 started", createdUnix: NOW - 4 * H - 200 }),
    plan("pln_h3", "user_disable", ApprovalState.REJECTED, { decidedByName: "owner", decidedUnix: NOW - D, createdUnix: NOW - D - 100 }),
    plan("pln_h4", "node_rollback", ApprovalState.EXPIRED, { createdUnix: NOW - 6 * D }),
    plan("pln_h5", "user_reset_traffic", ApprovalState.APPLIED, { decidedByName: "owner", decidedUnix: NOW - 8 * D, appliedUnix: NOW - 8 * D, result: "traffic reset for 1 user", createdUnix: NOW - 8 * D - 100 }),
    ...(mods.has("many") ? Array.from({ length: 14 }, (_, i) => plan(`pln_x${i}`, "node_fix", ApprovalState.APPLIED, { decidedByName: "owner", decidedUnix: NOW - (9 + i) * D, appliedUnix: NOW - (9 + i) * D, result: `done ${i + 1}`, createdUnix: NOW - (9 + i) * D - 100 })) : []),
  ];
  return { approvals: list, awaiting: list.filter((a) => a.state === ApprovalState.AWAITING).length, nowUnix: NOW, receivedMs: Date.now() };
}

// ---- the page

const POOL = [["de1", "DE"], ["fi1", "FI"], ["nl1", "NL"], ["msk1", "RU"], ["de2", "DE"], ["fr1", "FR"], ["se1", "SE"], ["pl1", "PL"], ["no1", "NO"], ["lv1", "LV"], ["lt1", "LT"], ["at1", "AT"]] as const;

/** The nodes of the shell (the menu's counter, the fleet button) and the alerts under it: the board's "fleet" and "issues". */
function shellSeed(qc: QueryClient, fleet: number, issues: number) {
  const cards = POOL.slice(0, fleet).map(([name, cc], i) => ({ id: `nod_${name}`, name, countryCode: cc, location: "", status: i < Math.min(issues, fleet) && i === 0 ? NodeStatus.DOWN : NodeStatus.ONLINE, online: [], sparkBytes: [] }));
  qc.setQueryData(nodesQuery.queryKey, { nodes: cards.map((c) => ({ ...c, address: `${c.name}.example.com`, version: "v0.1.71" })), expectedAgentVersion: "0.1.71" } as never);
  qc.setQueryData(overviewQuery().queryKey, { nodesTotal: fleet, nodes: cards, alertsActive: issues, alertsCritical: issues > 0 ? 1 : 0 } as never);
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const never = () => new Promise<never>(() => {});

/** The panel's calls of the scene: what a page asks for is answered from the scene's data, what a button asks for is answered after a breath and changes nothing. */
export function fakeCalls(p: Parsed, data: { settings: unknown; memory: ReturnType<typeof memoryData>; tasks: ReturnType<typeof tasksData>; tokens: unknown; approvals: unknown }) {
  const { tab, mods } = p;
  const load = mods.has("load");
  const broken = mods.has("err");
  // "load" never answers, "err" refuses: the tab of the scene is the only one that asks
  const answer = <T,>(v: T, mine: Tab): Promise<T> => (tab === mine && load ? never() : tab === mine && broken ? Promise.reject(new Error("panel did not answer")) : Promise.resolve(v));
  const set = (api: unknown, fns: Record<string, (...a: never[]) => unknown>) => Object.assign(api as object, fns);
  set(assistantApi, {
    getAssistantSettings: () => answer(data.settings, "as"),
    listProviderModels: () => Promise.resolve({ models }),
    listMemory: () => answer({ notes: data.memory.notes }, "mem"),
    listConversations: () => answer({ conversations: data.memory.conversations }, "mem"),
    getAssistantAccess: () => Promise.resolve({ available: true, timezoneOffsetMinutes: 180, choices: [] }),
    updateAssistantSettings: async () => (await sleep(700), data.settings),
    setProviderKey: async () => (await sleep(900), {}),
    removeProviderKey: async () => (await sleep(600), {}),
    saveMemory: async (r: { id: string; text: string }) => (await sleep(400), { note: { id: r.id || "mem_new" } }),
    deleteMemory: async () => (await sleep(300), {}),
    forgetAll: async () => (await sleep(300), {}),
    deleteConversation: async () => (await sleep(300), {}),
    deleteAllConversations: async () => (await sleep(300), {}),
  });
  set(tasksApi, {
    listTasks: () => answer(data.tasks.list, "tk"),
    listTaskRuns: (r: { taskId: string }) => Promise.resolve({ runs: data.tasks.runs[r.taskId] ?? [] }),
    listCheckTools: () => Promise.resolve({ tools }),
    createTask: async () => (await sleep(700), {}),
    updateTask: async () => (await sleep(700), {}),
    deleteTask: async () => (await sleep(500), {}),
    runTaskNow: async () => (await sleep(500), { runId: "run_now" }),
  });
  set(apiTokens, {
    listApiTokens: () => answer(data.tokens, "mcp"),
    createApiToken: async () => (await sleep(800), { token: { id: "tok_9", name: "claude-ops" }, secret: "not-a-real-token-Zk4Tq9Wm2Xc7Lp5Rb8Hd3Ns6Vy1Fj0Ge" }),
    revokeApiToken: async () => (await sleep(600), {}),
    setApiTokenBulk: async () => (await sleep(600), {}),
  });
  set(approvalsApi, {
    listApprovals: () => answer(data.approvals, "ap"),
    approve: async () => (await sleep(1500), {}),
    reject: async () => (await sleep(400), {}),
  });
  set(provisioning, {
    listNodeServerAccess: async () => ({ access: [] }),
    getPanelSSHKey: async () => ({ publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7cYk4m1x mistgate-panel", fingerprint: "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl" }),
  });
  // a browser that refuses to copy ("Токены: копирование не сработало")
  if (mods.has("nocopy") && typeof navigator !== "undefined") {
    try {
      Object.defineProperty(navigator, "clipboard", { value: { writeText: () => Promise.reject(new Error("blocked")) }, configurable: true });
    } catch {
      /* a browser that does not let it be replaced keeps its own */
    }
  }
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role, fleet = 4, issues = 1) {
  const p = parse(state);
  const { mods } = p;
  const data = { settings: settingsData(mods), memory: memoryData(mods), tasks: tasksData(mods), tokens: tokensData(mods), approvals: approvalsData(mods) };
  fakeCalls(p, data);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  const edge = mods.has("edge") ? { edition: "edge", unavailable: ["backups", "mcp", "assistant", "health_checks", "agent_logs", "panel_update", "decoy_own", "tspu_probe"] } : {};
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: NOW + 3600, ...edge } as never);
  shellSeed(c, fleet, issues);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  const seedTab = (tab: Tab) => !(p.tab === tab && (mods.has("load") || mods.has("err")));
  if (seedTab("as")) {
    c.setQueryData(settingsQuery.queryKey, data.settings as never);
    for (const s of data.settings.providers) c.setQueryData(["assistant", "models", (s as { provider: number }).provider], models as never);
  }
  if (seedTab("mem")) {
    c.setQueryData(memoryQuery.queryKey, data.memory.notes as never);
    c.setQueryData(conversationsQuery("").queryKey, data.memory.conversations as never);
  }
  c.setQueryData(accessQuery.queryKey, { available: true, timezoneOffsetMinutes: 180, choices: [] } as never);
  if (seedTab("tk")) {
    c.setQueryData(tasksQuery.queryKey, data.tasks.list as never);
    c.setQueryData(checkToolsQuery.queryKey, tools as never);
    for (const [id, runs] of Object.entries(data.tasks.runs)) c.setQueryData(["tasks", "runs", id], runs as never);
  }
  if (seedTab("mcp")) c.setQueryData(tokensQuery.queryKey, data.tokens as never);
  if (seedTab("ap")) c.setQueryData(approvalsQuery.queryKey, data.approvals as never);
  return c;
}

const roleOf = (r: string | null) => (r === "helper" ? Role.HELPER : r === "readonly" ? Role.READONLY : Role.OWNER);
const num = (v: string | null, otherwise: number) => (v !== null && !Number.isNaN(Number(v)) ? Number(v) : otherwise);

/** What the page opens with: the tab, and the flags of what is open. */
export const sceneOf = (state: string): AiScene & { flags: ReadonlySet<string> } => {
  const p = parse(state);
  return { tab: tabOf[p.tab], flags: p.mods };
};

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function AiScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "as";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return seeded(state, roleOf(params.get("role")), num(params.get("fleet"), 4), num(params.get("issues"), 1));
  });
  const scene = sceneOf(state);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <StepUpProvider>
          <AddNodeProvider>
            <ShellLayout current="ai" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <SceneProvider value={scene.flags}>
                <AiView scene={{ tab: scene.tab }} />
              </SceneProvider>
            </ShellLayout>
          </AddNodeProvider>
        </StepUpProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const f = (id: string, state: string, title: string, w = 1440, h = 1100, params = ""): Frame => ({ id: `ai-${id}`, state, title, w, h, params });
export const frames: Frame[] = [
  // the assistant ("Раздел: 1")
  f("as-on", "as", "Assistant: on, Anthropic and DeepSeek connected, the provider in use is open", 1440, 1900),
  f("as-on-light", "as", "The same on the light theme", 1440, 1900, "&theme=light"),
  f("as-on-1000", "as", "A 1000 window", 1000, 2200),
  f("as-on-phone", "as", "A phone, 400 wide", 400, 3400),
  f("as-on-fa", "as", "Right to left (fa)", 1440, 1900, "&lang=fa"),
  f("as-on-ru", "as", "Russian", 1440, 1900, "&lang=ru"),
  f("as-off", "as.off", "Assistant: off (the body dims)", 1440, 1900),
  f("as-noprov", "as.noprov", "No provider connected", 1440, 1900),
  f("as-key", "as.key", "The provider did not accept the key", 1440, 1900),
  f("as-bud", "as.bud", "The month's budget is spent", 1440, 1900),
  f("as-dd-chat", "as.dd-chat", "The chat provider dropdown open", 1440, 1500),
  f("as-dd-fall", "as.dd-fall", "The fallback provider dropdown open", 1440, 1500),
  f("as-shut", "as.shut-anthropic", "Every provider closed", 1440, 1500),
  f("as-open-ds", "as.open-deepseek", "A second provider open", 1440, 2000),
  f("as-dirty", "as.dirty", "Something changed: the save bar", 1440, 1900),
  f("as-dlg-connect", "as.dlg-connect-zai", "The key window: connect Z.ai", 1440, 1000),
  f("as-dlg-connect-phone", "as.dlg-connect-zai", "The same window on a phone (a sheet)", 400, 900),
  f("as-dlg-keyfilled", "as.dlg-connect-zai.keyfilled", "A key pasted", 1440, 1000),
  f("as-dlg-keyerr", "as.dlg-connect-zai.keyerr.keyfilled", "The provider refused the key: the error under the field", 1440, 1000),
  f("as-dlg-replace", "as.dlg-replace-anthropic", "Replace the key of the provider in use", 1440, 1000),
  f("as-dlg-remove", "as.dlg-remove-anthropic", "Delete the key: the question", 1440, 1000),
  f("as-dlg-custom", "as.dlg-connect-custom1", "A custom provider: its name, address, model, prices", 1440, 1200),
  f("as-load", "as.load", "Loading", 1440, 800),
  f("as-err", "as.err", "Did not load", 1440, 700),
  f("as-helper", "as", "A helper: the tab is the owner's", 1440, 700, "&role=helper"),
  f("as-readonly", "as", "An observer: the tab is the owner's", 1440, 700, "&role=readonly"),
  f("as-edge", "as.edge", "The Cloudflare edition: no assistant yet", 1440, 700),
  // memory ("Раздел: 2")
  f("mem-has", "mem", "Memory: notes and conversations", 1440, 1500),
  f("mem-light", "mem", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("mem-phone", "mem", "A phone", 400, 2400),
  f("mem-fa", "mem", "Right to left (fa)", 1440, 1500, "&lang=fa"),
  f("mem-empty", "mem.empty", "Nothing remembered, no conversations", 1440, 900),
  f("mem-new", "mem.new-note", "A new note being written", 1440, 1500),
  f("mem-edit", "mem.edit-note", "A note being edited", 1440, 1500),
  f("mem-ask", "mem.ask-note", "Delete the note? (the row asks)", 1440, 1500),
  f("mem-fresh", "mem.fresh-note", "A note that has just been saved flashes", 1440, 1500),
  f("mem-dlg-forget", "mem.dlg-forget", "Forget all: the window", 1440, 900),
  f("mem-ask-conv", "mem.ask-conv", "Delete the conversation? (the row asks)", 1440, 1500),
  f("mem-dlg-wipe", "mem.dlg-wipe", "Delete all conversations: the window", 1440, 900),
  f("mem-load", "mem.load", "Loading", 1440, 700),
  f("mem-err", "mem.err", "Did not load", 1440, 700),
  f("mem-readonly", "mem", "An observer sees their own memory", 1440, 1500, "&role=readonly"),
  // scheduled tasks ("Раздел: 3")
  f("tk-has", "tk", "Tasks: a check that is fine, one with a problem, an assistant task", 1440, 1200),
  f("tk-light", "tk", "The same on the light theme", 1440, 1200, "&theme=light"),
  f("tk-phone", "tk", "A phone", 400, 2200),
  f("tk-fa", "tk", "Right to left (fa)", 1440, 1200, "&lang=fa"),
  f("tk-empty", "tk.empty", "No tasks", 1440, 700),
  f("tk-fail", "tk.fail.runs", "The assistant task keeps failing: the reason, its runs open", 1440, 1500),
  f("tk-runs", "tk.runs", "Runs of a task open", 1440, 1500),
  f("tk-running", "tk.running", "A run under way", 1440, 1200),
  f("tk-ask", "tk.ask-task", "Delete the task? (the card asks)", 1440, 1200),
  f("tk-dlg-new", "tk.dlg-task", "New check: the window", 1440, 1400),
  f("tk-dlg-as", "tk.dlg-task-as", "New assistant task: the window", 1440, 1200),
  f("tk-dlg-tried", "tk.dlg-task.tried", "The window asks for a name", 1440, 1400),
  f("tk-dlg-edit", "tk.dlg-task-edit", "Edit a check", 1440, 1400),
  f("tk-dlg-phone", "tk.dlg-task", "The window on a phone (a sheet)", 400, 1000),
  f("tk-load", "tk.load", "Loading", 1440, 700),
  f("tk-err", "tk.err", "Did not load", 1440, 700),
  f("tk-helper", "tk", "A helper: reads and runs, no switch, no editing", 1440, 1200, "&role=helper"),
  f("tk-readonly", "tk", "An observer: closed", 1440, 700, "&role=readonly"),
  f("tk-edge", "tk.edge", "The Cloudflare edition", 1440, 700),
  // tokens and MCP ("Раздел: 4")
  f("mcp-has", "mcp", "MCP and tokens: an operator, a read-only token, the address and the snippet", 1440, 1500),
  f("mcp-light", "mcp", "The same on the light theme", 1440, 1500, "&theme=light"),
  f("mcp-phone", "mcp", "A phone", 400, 2800),
  f("mcp-fa", "mcp", "Right to left (fa)", 1440, 1500, "&lang=fa"),
  f("mcp-empty", "mcp.empty", "No tokens", 1440, 1100),
  f("mcp-nocopy", "mcp.nocopy", "The browser refuses to copy (click Copy: the toast)", 1440, 1500),
  f("mcp-old", "mcp.old", "The expired and revoked tokens shown", 1440, 1500),
  f("mcp-dlg-tok", "mcp.dlg-tok", "New token: the window", 1440, 1100),
  f("mcp-dlg-tok-op", "mcp.dlg-tok.op", "An operator picked: the exemption switch", 1440, 1200),
  f("mcp-dlg-tok-tried", "mcp.dlg-tok.tried", "The window asks for a name", 1440, 1100),
  f("mcp-dlg-secret", "mcp.dlg-secret", "The token made: the secret is shown once", 1440, 1000),
  f("mcp-dlg-rev", "mcp.dlg-rev", "Revoke a token: the window", 1440, 900),
  f("mcp-dlg-lim", "mcp.dlg-lim", "Lift the limit of user changes: the window", 1440, 1000),
  f("mcp-dlg-phone", "mcp.dlg-tok", "New token on a phone (a sheet)", 400, 1100),
  f("mcp-load", "mcp.load", "Loading", 1440, 700),
  f("mcp-err", "mcp.err", "Did not load", 1440, 700),
  f("mcp-helper", "mcp", "A helper: the tab is the owner's", 1440, 700, "&role=helper"),
  f("mcp-edge", "mcp.edge", "The Cloudflare edition: no MCP", 1440, 900),
  // waiting for you ("Раздел: 5")
  f("ap-wait", "ap", "One change waits: 7 minutes left", 1440, 1300),
  f("ap-wait-light", "ap", "The same on the light theme", 1440, 1300, "&theme=light"),
  f("ap-wait-phone", "ap", "A phone", 400, 2200),
  f("ap-wait-fa", "ap", "Right to left (fa)", 1440, 1300, "&lang=fa"),
  f("ap-soon", "ap.soon", "Under a minute left: amber", 1440, 1300),
  f("ap-exp", "ap.exp", "The request ran out", 1440, 800),
  f("ap-none", "ap.none", "Nothing waits", 1440, 800),
  f("ap-install", "ap.install", "A node install: the host key and the login (the Integrations card)", 1440, 1500),
  f("ap-many", "ap.many", "A long history: a pager", 1440, 1500),
  f("ap-load", "ap.load", "Loading", 1440, 700),
  f("ap-err", "ap.err", "Did not load", 1440, 700),
  f("ap-readonly", "ap", "An observer: the tab is the owner's", 1440, 700, "&role=readonly"),
];

export function PnAiKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>AI</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((fr) => (
        <Shot key={fr.id} id={fr.id} title={fr.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={fr.title} src={`${path}?only=pn-ai-page&state=${fr.state}${fr.params ?? ""}`} width={fr.w} height={fr.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
