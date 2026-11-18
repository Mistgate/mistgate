import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton, type ChatApi } from "@/components/assistant/chat";
import { ChatPanel } from "@/components/assistant/panel";
import { SceneContext } from "@/components/assistant/scene";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { StepUpProvider } from "@/components/step-up";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { AssistantEffort, AssistantProvider, ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { isLang, setLang, useLang, useT } from "@/i18n";
import { assistant, approvals } from "@/lib/api";
import { accessQuery, conversationsQuery, recentQuestionsQuery, type Context, type LiveMsg, type LiveTool, type Msg } from "@/lib/assistant";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { navKey } from "@/components/nav";
import { Shot } from "./dev-kit-redesign";
import { seeded as overviewSeeded } from "./dev-kit-overview";
import { seeded as nodesSeeded } from "./dev-kit-nodes";
import { NodesScreen } from "./nodes";
import { OverviewScreen } from "./overview";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-assistant: the assistant's window of the redesign (PnAssistant) in every state of the board's switch, on top of
// a real page of the shell, each in a frame of its own width (an iframe, so the window's breakpoints answer to the frame). Each frame is a page of
// its own, for the screenshot script and for the side-by-side with the board:
//   /dev-kit?only=pn-assistant-page&state=<view>[.<what>…][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly]
//                                  [&behind=overview|nodes][&fx=1]
// <view> (the board's "Состояние"): hist (Разговоры) | empty (Начало) | think (Думает) | answer (Ответ и план) | done (Применено) | err (Ошибка) |
//   off (Не подключён). <what> (a view may come with them: "empty.md"): md (the choice of the agent open) | seen-<provider> (its column in view: 1 Anthropic, 2 DeepSeek, 3 Z.ai, 4 Ollama
//   Cloud, 5 custom) | bud (a provider's budget is spent) | d0 d2 (the depth, with md) | nopage (the page let go) | warn (a secret typed into the box) |
//   busy (the plan waits for the confirmation) | rej (the plan was turned down) | tools (six calls, one failed, open) | row (the calls open to their input) | think-open (what the
//   model said between the calls) | long (a heading, a table, code) | fu (follow-ups) | cards (an opened page, a secret field) | install (the
//   inline approval of a node install) | ask (a row of the list asks) | wipe (delete all asks) | hq (a search with no match) | none (no
//   conversations) | big (the big window: in the middle of the page over a dimmed one; a phone keeps the card) | plans (more plans after the relay: a group, a site on a node, a template,
//   three people: the title says the names).
// Made-up data of the board: the nodes de1, fi1, nl1 and msk1, the people alice and bob, documentation addresses and not-a-real-key secrets.

export type View = "hist" | "empty" | "think" | "answer" | "done" | "err" | "off";
export type Parsed = { view: View; mods: Set<string> };
const views: View[] = ["hist", "empty", "think", "answer", "done", "err", "off"];

export function parse(state: string): Parsed {
  const [v = "empty", ...mods] = state.split(".");
  return { view: (views as string[]).includes(v) ? (v as View) : "empty", mods: new Set(mods) };
}

const NOW = Math.floor(Date.now() / 1000);
const MIN = 60;
const H = 3600;
const D = 86_400;
const fact = (key: string, value: string) => ({ key, value, untrusted: false, code: "", params: {}, untrustedParams: [] });
/** A fact whose value is a name from data (a node, a person, a group): the panel marks it untrusted, the card puts it on a plate of its own. */
const named = (key: string, value: string) => ({ ...fact(key, value), untrusted: true });
const at = (minutesAgo: number) => NOW - minutesAgo * MIN;
/** Midnight of today (the list groups by calendar day, so the rows of "today" and "yesterday" are put on the right side of it at any hour). */
const MIDNIGHT = (() => {
  const d = new Date();
  return Math.floor(new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime() / 1000);
})();
const today = (minutesAgo: number) => Math.max(NOW - minutesAgo * MIN, MIDNIGHT + 30);
const yesterday = (hoursBeforeMidnight: number) => MIDNIGHT - hoursBeforeMidnight * H;
const msg = (m: Partial<LiveMsg> & Pick<Msg, "role" | "text">): Msg => ({ id: `m-${m.role}-${m.createdUnix ?? 0}-${m.text.length}`, createdUnix: NOW, tools: [], plans: [], remembered: [], end: "", ...m }) as Msg;
const tool = (id: string, name: string, args: string, over: Partial<LiveTool> = {}): LiveTool => ({ id, tool: name, argsJson: args, done: true, ok: true, count: 0, error: "", ...over }) as LiveTool;

/** What the model said, in the language of the question: the board's Russian for ru, English for the rest. */
type Say = (en: string, ru: string) => string;

const access = (view: View, mods: ReadonlySet<string>) => ({
  available: view !== "off",
  reason: view === "off" ? "no_provider" : "",
  provider: AssistantProvider.ANTHROPIC,
  model: "claude-sonnet-5-5",
  readOnly: mods.has("ro"),
  effort: AssistantEffort.NORMAL,
  unread: 0,
  nextFollowUpUnix: 0,
  timezoneOffsetMinutes: 180,
  choices: view === "off" ? [] : [
    { provider: AssistantProvider.ANTHROPIC, model: "claude-sonnet-5-5", inputUsdPerMtok: 3, outputUsdPerMtok: 15, budgetReached: false, customName: "", customUrl: "" },
    { provider: AssistantProvider.ZAI, model: "GLM-5.3", inputUsdPerMtok: 0.6, outputUsdPerMtok: 2.2, budgetReached: false, customName: "", customUrl: "" },
    { provider: AssistantProvider.OLLAMA_CLOUD, model: "deepseek-v4.1-flash", inputUsdPerMtok: 0.3, outputUsdPerMtok: 1.2, budgetReached: mods.has("bud"), customName: "", customUrl: "" },
  ],
});

const conversations = (say: Say, mods: ReadonlySet<string>) =>
  mods.has("none")
    ? []
    : [
        { id: "cnv_a", title: say("Node problems today", "Проблемы нод за сегодня"), createdUnix: today(30), updatedUnix: today(5), messages: 4, taskId: "", unread: false, followUpUnix: 0 },
        { id: "cnv_b", title: say("Why bob cannot connect", "Почему bob не подключается"), createdUnix: today(200), updatedUnix: today(190), messages: 6, taskId: "", unread: true, followUpUnix: 0 },
        { id: "cnv_c", title: say("A group for the family", "Группа для семьи"), createdUnix: yesterday(6), updatedUnix: yesterday(4), messages: 8, taskId: "", unread: false, followUpUnix: 0 },
        { id: "cnv_d", title: say("Certificate on nl1", "Сертификат на nl1"), createdUnix: yesterday(15), updatedUnix: yesterday(14), messages: 4, taskId: "tsk_cert", unread: false, followUpUnix: NOW + 2 * H },
        { id: "cnv_e", title: say("What takes the disk on de1", "Что занимает диск на de1"), createdUnix: NOW - 9 * D, updatedUnix: NOW - 9 * D + 600, messages: 10, taskId: "", unread: false, followUpUnix: 0 },
      ];

const relayPlan = (say: Say, state: PlanState, over: Record<string, unknown> = {}) => ({
  planId: "pln_relay",
  tool: "relay",
  summary: say("Turn the relay on for fi1 through msk1", "Включить ретрансляцию fi1 через msk1"),
  facts: [fact("action", "enable"), named("entry", "msk1"), named("exit", "fi1"), fact("node", say("three checks in a row fail from some networks", "три проверки подряд не проходят из части сетей"))],
  danger: ["fleet"],
  needsApproval: true,
  inboxOnly: false,
  expiresUnix: NOW + 10 * MIN,
  state,
  result: say("fi1 goes through msk1", "fi1 идёт через msk1"),
  error: "",
  outcomeCode: "",
  ...over,
});

/** The other plans the board's scripts prepare, each with the names of its facts (a group, a site on a node, a template, three people): the title of the card says them. */
const plain = (id: string, kind: string, summary: string, facts: unknown[], over: Record<string, unknown> = {}) => ({ planId: id, tool: kind, summary, facts, danger: [], needsApproval: false, inboxOnly: false, expiresUnix: NOW + 10 * MIN, state: PlanState.PROPOSED, result: "", error: "", outcomeCode: "", ...over });
const morePlans = (say: Say) => [
  plain("pln_group", "group_create", say("Create the group Family", "Создать группу «Семья»"), [named("name", say("Family", "Семья")), named("profile_ids", "Hysteria2 · 443 de1"), fact("color", say("automatic", "автоматически"))]),
  plain("pln_site", "decoy_site_assign", say("Put a site on nl1", "Поставить на nl1 свой сайт"), [named("site", "Bakery"), fact("state", "ready"), fact("version", "2"), named("nodes", "nl1"), fact("panel", "false")], { needsApproval: true }),
  plain("pln_tpl", "subscription_mihomo_template_set", say("Change the Mihomo template Family", "Изменить шаблон Mihomo «Для семьи»"), [named("template", say("Family", "Для семьи")), fact("action", "update"), fact("bytes", "300")], { needsApproval: true }),
  plain("pln_users", "user_disable", say("Disable 3 users", "Отключить 3 людей"), [fact("count", "3"), named("users", "alice"), named("users", "bob"), named("users", "carol")], { needsApproval: true }),
];

const question = (say: Say, page: string) => msg({ role: ChatRole.USER, createdUnix: at(2), text: say("Which nodes had problems today?", "У каких нод сегодня были проблемы?"), context: { kind: "overview", id: "", label: page, route: "/" } as never });

const answerText = (say: Say) =>
  say(
    "No node went down in the last 24 hours: all 4 are online, no critical alerts. There are three notes, none critical.\n\n## Open notes (3)\n\n- fi1 does not open from some networks, three checks in a row\n- nl1 was gone for 2 minutes at 11:20, its certificate expires in 9 days\n- de1 the systemd journal took 3.8 GB\n\nmsk1 has no notes. For fi1 I can turn on a relay through msk1.",
    "За сутки ни одна нода не падала: все 4 на связи, критичных алертов нет. Замечаний три, все некритичные.\n\n## Открытые замечания (3)\n\n- fi1 не открывается из части сетей, три проверки подряд\n- nl1 в 11:20 пропадала на 2 минуты, сертификат истекает через 9 дней\n- de1 журнал systemd занял 3,8 ГБ\n\nmsk1 без замечаний. Для fi1 могу включить ретрансляцию через msk1.",
  );

const digest = tool("t1", "fleet_status", '{"hours":24}', { count: 4, ms: 4200 });

/** The thread of a view, as the board draws it (and the product's own states the board has no switch for). */
export function messagesOf(view: View, mods: ReadonlySet<string>, say: Say, page = "Overview"): Msg[] {
  const q = question(say, page);
  const more = mods.has("tools")
    ? [
        digest,
        tool("t2", "alerts_list", '{"hours":24}', { count: 3, ms: 1100 }),
        tool("t3", "node_journal", '{"node":"de1"}', { count: 40, ms: 2300 }),
        tool("t4", "events_search", '{"node":"nl1","hours":24}', { count: 5, ms: 900 }),
        tool("t5", "user_devices", '{"user":"bob"}', { ok: false, error: "tool input rejected: unknown field \"since\" (allowed: node, hours, severity, limit)", ms: 300 }),
        tool("t6", "health_summary", "{}", { count: 1, ms: 700 }),
      ]
    : [digest];
  const base = { role: ChatRole.ASSISTANT, createdUnix: at(1), tools: more as never, t0: NOW * 1000 - 4300, workedMs: 4300 } as const;
  const interim = mods.has("think-open") ? say("Let me look at the fleet digest first.", "Сначала посмотрю сводку по флоту.") : "";
  const text = (body: string) => (interim ? `${interim}\n\n${body}` : body);
  const interimEnd = interim ? interim.length + 2 : undefined;
  const plan = (s: PlanState, over: Record<string, unknown> = {}) => [relayPlan(say, s, over), ...(mods.has("plans") ? morePlans(say) : [])] as never;
  const long = mods.has("long")
    ? say(
        "## In short\n\nIn the last 24 hours **three nodes** had notes, nothing is down.\n\n| Node | What happened | Until |\n|---|---|--:|\n| **fi1** | Reachability from Russia: `blocked` for part of the networks | now |\n| **nl1** | Gone for 2 minutes at 11:20 | now |\n| **de1** | The journal took 3.8 GB | 4 days |\n\n1. Turn on the relay for fi1.\n2. Renew the certificate of nl1: the plan is below.\n3. Clean the journal of de1:\n   - `journalctl --vacuum-size=500M`\n   - then look again in a day\n\n```\nnl1  cert  expires in 9 days\n```\n\n> The relay changes what runs on fi1, so it waits for you.",
        "## Коротко\n\nЗа сутки замечания были у **трёх нод**, ничего не лежит.\n\n| Нода | Что произошло | До |\n|---|---|--:|\n| **fi1** | Доступность из РФ: `blocked` для части сетей | сейчас |\n| **nl1** | Пропадала на 2 минуты в 11:20 | сейчас |\n| **de1** | Журнал занял 3,8 ГБ | 4 дня |\n\n1. Включить ретрансляцию для fi1.\n2. Продлить сертификат nl1: план ниже.\n3. Почистить журнал de1:\n   - `journalctl --vacuum-size=500M`\n   - через сутки посмотреть снова\n\n```\nnl1  cert  expires in 9 days\n```\n\n> Ретрансляция меняет то, что работает на fi1, поэтому ждёт тебя.",
      )
    : "";
  switch (view) {
    case "think":
      return [q, msg({ ...base, text: "", tools: [{ ...digest, count: 4 }] as never, end: "", workedMs: undefined, t0: NOW * 1000 - 3000 } as never)];
    case "answer":
      return [q, msg({ ...base, text: text(long || answerText(say)), interimEnd, plans: plan(mods.has("rej") ? PlanState.DISMISSED : PlanState.PROPOSED), end: "end" } as never)];
    case "done":
      return [q, msg({ ...base, text: text(long || answerText(say)), interimEnd, plans: plan(PlanState.APPLIED), end: "end" } as never)];
    case "err":
      return [q, msg({ role: ChatRole.ASSISTANT, createdUnix: at(1), text: "", tools: [], end: "error" })];
    default:
      break;
  }
  if (mods.has("fu")) {
    return [
      msg({ role: ChatRole.USER, createdUnix: at(40), text: say("Capture de1 for 10 minutes", "Сними трафик de1 за 10 минут") }),
      msg({ role: ChatRole.ASSISTANT, createdUnix: at(39), text: say("Started. I will come back with the result when it ends.", "Запустил. Вернусь с результатом, когда закончится."), tools: [digest] as never, end: "end" }),
      msg({ role: ChatRole.USER, createdUnix: at(2), text: say("read node_debug_logs of req_1", "прочитать node_debug_logs запроса req_1"), followUp: { id: "afu_0", note: "", setUnix: at(39), dueUnix: at(2), outcome: "", reason: "", planId: "" } as never }),
      msg({ role: ChatRole.ASSISTANT, createdUnix: at(1), text: say("The capture of de1 is done: 412 packets, nothing unusual.", "Съёмка de1 закончилась: 412 пакетов, ничего необычного."), tools: [tool("t9", "node_journal", '{"node":"de1"}', { count: 40 })] as never, end: "end", followUp: { id: "afu_0", note: "", setUnix: at(39), dueUnix: at(2), outcome: "", reason: "", planId: "" } as never }),
      msg({ role: ChatRole.USER, createdUnix: at(0), text: say("And nl1?", "А nl1?") }),
      msg({ role: ChatRole.ASSISTANT, createdUnix: at(0), text: "", end: "end", followUp: { id: "afu_x", note: say("compare nl1 with the capture", "сравнить nl1 со съёмкой"), setUnix: at(30), dueUnix: at(3), outcome: "missed", reason: "", planId: "" } as never }),
    ];
  }
  if (mods.has("cards")) {
    return [
      msg({ role: ChatRole.USER, createdUnix: at(5), text: say("Where do I see the ways of nl1, and put in the key of DeepSeek", "Где пути nl1, и добавь ключ DeepSeek") }),
      msg({
        role: ChatRole.ASSISTANT,
        createdUnix: at(4),
        text: say("The page is below. For the key I gave you a secure field: the assistant never sees what you type.", "Страница ниже. Для ключа я дал защищённое поле: ассистент не видит того, что ты вводишь."),
        tools: [tool("o1", "ui_open", '{"id":"nl1","page":"node_ways"}'), tool("k1", "ui_secret_form", '{"kind":"provider_key","provider":"deepseek"}')] as never,
        remembered: [{ id: "mem_1", text: say("nl1 is the test node", "nl1 — тестовая нода"), createdUnix: NOW, updatedUnix: NOW, byAssistant: true, conversationId: "cnv_a" }],
        end: "end",
      }),
    ];
  }
  if (mods.has("install")) {
    return [
      msg({ role: ChatRole.USER, createdUnix: at(5), text: say("Add the node de2 at 203.0.113.9", "Добавь ноду de2 на 203.0.113.9") }),
      msg({
        role: ChatRole.ASSISTANT,
        createdUnix: at(4),
        text: say("I prepared the install: compare the host key and type the password on the card below.", "Подготовил установку: сверь ключ сервера и введи пароль на карточке ниже."),
        plans: [{ planId: "pln_install", tool: "node_install", summary: say("Install a node on 203.0.113.9 as de2", "Поставить ноду на 203.0.113.9 как de2"), facts: [fact("host", "203.0.113.9"), fact("name", "de2")], danger: [], needsApproval: true, inboxOnly: true, expiresUnix: NOW + 9 * MIN, state: PlanState.PROPOSED, result: "", error: "", outcomeCode: "" }] as never,
        end: "end",
      }),
    ];
  }
  if (mods.has("tools") || mods.has("row") || mods.has("long")) return [q, msg({ ...base, text: text(long || answerText(say)), interimEnd, end: "end" } as never)];
  return [];
}

const draftOf = (mods: ReadonlySet<string>) => (mods.has("warn") ? { text: "api key: sk-ant-FAKE0000000000000000000000000", n: 1 } : undefined);

const fakeChat = (view: View, mods: ReadonlySet<string>, say: Say, page: string): ChatApi => {
  const none = async () => {};
  const messages = messagesOf(view, mods, say, page);
  const running = view === "think";
  const followUps = mods.has("fu") ? [{ id: "afu_1", note: "check nl1 again", dueUnix: NOW + 9 * MIN, createdUnix: NOW - MIN, planId: "", delayS: 0, running: false }] : [];
  return {
    conversationId: view === "hist" || messages.length > 0 ? "cnv_a" : "",
    messages,
    running,
    live: running,
    error: view === "err" ? "ai.err.budget_reached" : null,
    followUps,
    send: none,
    stop: none,
    open: none,
    refresh: () => {},
    reset: () => {},
    apply: () => new Promise<void>(() => {}),
    dismiss: none,
    forget: none,
    cancelFollowUp: none,
  } as unknown as ChatApi;
};

const roleOf = (r: string | null) => (r === "helper" ? Role.HELPER : r === "readonly" ? Role.READONLY : Role.OWNER);

/** The private query cache of a scene: the real page behind (made-up data) and what the window asks for. */
export function seeded(state: string, role: Role, say: Say, behind = "overview"): QueryClient {
  const { view, mods } = parse(state);
  const qc = behind === "nodes" ? nodesSeeded("list", role, false) : overviewSeeded("issues");
  const acc = access(view, mods);
  qc.setQueryData(accessQuery.queryKey, acc as never);
  const list = conversations(say, mods);
  qc.setQueryData(conversationsQuery("").queryKey, list as never);
  qc.setQueryData(conversationsQuery("zz").queryKey, [] as never);
  qc.setQueryData(recentQuestionsQuery.queryKey, [] as never);
  qc.setQueryData(meQuery.queryKey, (old: unknown) => ({ ...(old as object), admin: { ...((old as { admin?: object })?.admin ?? {}), id: "adm_1", displayName: "owner", role } }) as never);
  // the nodes the answer names wear the colours of the board's chips: fi1, nl1 and de1 have notes, msk1 has none
  qc.setQueryData(nodesQuery.queryKey, (old: unknown) => {
    const o = old as { nodes: { name: string; status: NodeStatus }[] } | undefined;
    return o ? ({ ...o, nodes: o.nodes.map((n) => (["fi1", "nl1", "de1"].includes(n.name) ? { ...n, status: NodeStatus.BLIP } : n)) } as never) : o;
  });
  // what a button asks for is answered after a breath and changes nothing
  Object.assign(assistant as object, {
    getAssistantAccess: async () => acc,
    listConversations: async (r: { query: string }) => ({ conversations: r.query ? [] : list }),
    recentQuestions: async () => ({ questions: [] }),
    deleteConversation: async () => ({}),
    deleteAllConversations: async () => ({}),
    stopTurn: async () => ({}),
    setProviderKey: async () => ({ status: { keyHint: "Q7wA" } }),
  });
  Object.assign(approvals as object, {
    listApprovals: async () => ({ approvals: mods.has("install") ? [installApproval()] : [], awaiting: 1, nowUnix: NOW }),
    approve: async () => ({}),
    reject: async () => ({}),
  });
  if (mods.has("install")) qc.setQueryData(["approvals", "list"], { approvals: [installApproval()], awaiting: 1, nowUnix: NOW, receivedMs: Date.now() } as never);
  return qc;
}

const installApproval = () => ({
  id: "pln_install",
  tokenId: "tok_1",
  tokenName: "Assistant",
  tokenProfile: 1,
  tool: "node_install",
  facts: [fact("host", "203.0.113.9"), fact("host_key", "SHA256:Qm9y3xk2Jv0eVwUQxk1v2Tn8sY4GmR7pL5dXc1aF0hE"), fact("host_key_algorithm", "ssh-ed25519")],
  danger: [],
  reason: "",
  state: 1,
  createdUnix: NOW,
  expiresUnix: NOW + 540,
  decidedByName: "",
  decidedUnix: 0,
  appliedUnix: 0,
  result: "",
  error: "",
  outcomeCode: "",
  outcomeParams: {},
});

/** The window itself, on the page of the scene: the chat of the view, the page it was asked on, the flags. */
export function Window({ state, say, readOnly }: { state: string; say: Say; readOnly?: boolean }) {
  const t = useT();
  const { view, mods } = parse(state);
  // the flags a scene hands to the window: what is open, and the view itself ("hist" opens the list)
  const flags = new Set([view, ...mods]);
  const page = t(navKey("overview"));
  const [chat] = useState(() => fakeChat(view, mods, say, page));
  const context = { kind: "overview", id: "", label: page, route: "/" } as Context;
  return (
    <SceneContext.Provider value={flags}>
      <ChatPanel
        chat={chat}
        readOnly={!!readOnly || mods.has("ro")}
        providerLabel="claude-sonnet-5-5"
        provider={AssistantProvider.ANTHROPIC}
        onClose={() => {}}
        initialContext={context}
        pickerOpen={mods.has("md")}
        draft={draftOf(mods)}
      />
    </SceneContext.Provider>
  );
}

/** One whole page of the scene: the real shell with a real page, the window on top of it. */
export function AssistantScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "empty";
  const lang = params.get("lang");
  const [qc] = useState(() => {
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    const say: Say = (en, ru) => (lang === "ru" ? ru : en);
    return seeded(state, roleOf(params.get("role")), say, params.get("behind") ?? "overview");
  });
  const say: Say = (en, ru) => (lang === "ru" ? ru : en);
  const behind = params.get("behind") === "nodes" ? "nodes" : "overview";
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <StepUpProvider>
          <AddNodeProvider>
            <ShellLayout current={behind} fab={<ChatButton onClick={() => {}} running={false} unread={0} open />}>
              {behind === "nodes" ? <NodesScreen /> : <OverviewScreen />}
            </ShellLayout>
            <Window state={state} say={say} />
          </AddNodeProvider>
        </StepUpProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const f = (id: string, state: string, title: string, w = 1440, h = 900, params = ""): Frame => ({ id: `as-${id}`, state, title, w, h, params });
const P = 400;
const PH = 800;
export const frames: Frame[] = [
  // the board's switch
  f("empty", "empty", "Начало: the cat, what to ask about, the first examples; the page as a chip in the box"),
  f("think", "think", "Думает: a call is done, the model thinks, the dots"),
  f("answer", "answer", "Ответ и план: the answer with the nodes as chips, the change waits (valid for 10 min)"),
  f("done", "done", "Применено: the card quiets down, one line says how it ended"),
  f("err", "err", "Ошибка: the month's budget is spent, the way to the settings"),
  f("off", "off", "Не подключён: no provider, the box is closed"),
  f("hist", "hist", "Разговоры: three groups, the open one framed, one has an unread dot, one is scheduled"),
  // the choice of the agent
  f("md", "empty.md", "The choice of the agent: the provider in use, its model, the depth, the opening of pages"),
  f("md-bud", "empty.md.bud.seen-4", "A provider whose month's budget is spent: its model cannot be picked"),
  f("md-za", "empty.md.seen-3", "Z.ai in view: its model and price"),
  f("md-nokey", "empty.md.seen-2", "DeepSeek has no key: how to add one (the owner)"),
  f("md-nokey-helper", "empty.md.seen-2", "DeepSeek has no key, a helper sees no way to add it", 1440, 900, "&role=helper"),
  f("d0", "empty.md.d0", "The depth: quick"),
  f("d2", "empty.md.d2", "The depth: thorough"),
  // the thread
  f("tools", "answer.tools", "Six calls, one failed: the group opens to its rows"),
  f("row", "answer.row", "The rows open to the call's name, input and output"),
  f("think-open", "answer.think-open", "What the model said between its calls"),
  f("long", "answer.long", "A long answer: a heading, a table, a list, code and a quote"),
  f("busy", "answer.busy", "The plan waits for the confirmation"),
  f("rej", "answer.rej", "Отклонено: the card quiets down, the line says nothing was changed"),
  f("fu", "empty.fu", "Follow-ups: one answered, one missed, one waits"),
  f("cards", "empty.cards", "A page to open, a secure field, a remembered note"),
  f("install", "empty.install", "A node install: the owner confirms right under the card"),
  f("warn", "empty.warn", "A key typed into the box: the warning"),
  f("nopage", "empty.nopage", "The page let go: the plus brings it back"),
  f("ro", "empty.ro", "A read-only role: the line under the box"),
  // the list
  f("hist-ask", "hist.ask", "A row asks before it deletes"),
  f("hist-wipe", "hist.wipe", "Delete all asks"),
  f("hist-hq", "hist.hq", "A search with no match"),
  f("hist-none", "hist.none", "No conversations"),
  // other roles, other pages
  f("off-helper", "off", "Not connected, seen by a helper: whom to ask", 1440, 900, "&role=helper"),
  f("nodes", "answer", "On top of the Nodes page", 1440, 900, "&behind=nodes"),
  // the big window (the board's "Развернуть"): the same states, in the middle of the page over the dimmed one
  f("big-empty", "empty.big", "Big: the start, the column of 720 in a window of 960"),
  f("big-think", "think.big", "Big: it thinks"),
  f("big-answer", "answer.big", "Big: the answer and the plan in the column; the button says Collapse"),
  f("big-plans", "answer.big.plans", "Big: more plans, each titled with the names of its facts"),
  f("big-done", "done.big", "Big: applied"),
  f("big-err", "err.big", "Big: the month's budget is spent"),
  f("big-off", "off.big", "Big: not connected"),
  f("big-hist", "hist.big", "Big: the conversations in the column"),
  f("big-md", "empty.md.big", "Big: the choice of the agent"),
  f("big-short", "answer.big", "Big, a low page: the window is as high as the page less its edges", 1180, 560),
  f("big-tablet", "answer.big", "Big on a tablet, 800 wide", 800, 900),
  f("big-light", "answer.big", "Big, light theme", 1440, 900, "&theme=light"),
  f("big-empty-light", "empty.big", "Big, light theme", 1440, 900, "&theme=light"),
  f("big-ru", "answer.big.plans", "Big, Russian", 1440, 900, "&lang=ru"),
  f("big-fa", "answer.big", "Big, right to left (fa)", 1440, 900, "&lang=fa"),
  f("big-zh", "answer.big", "Big, Chinese", 1440, 900, "&lang=zh"),
  f("big-tr", "answer.big", "Big, Turkish", 1440, 900, "&lang=tr"),
  f("big-fx", "empty.big", "Big, with the reveal animation on: the wave and the glints", 1440, 900, "&fx=1"),
  f("plans", "answer.plans", "More plans, each titled with the names of its facts", 1440, 900),
  f("plans-ru", "answer.plans", "More plans, Russian", 1440, 900, "&lang=ru"),
  // the other theme, the other direction
  f("empty-light", "empty", "Light theme", 1440, 900, "&theme=light"),
  f("answer-light", "answer", "Light theme", 1440, 900, "&theme=light"),
  f("done-light", "done", "Light theme", 1440, 900, "&theme=light"),
  f("md-light", "empty.md", "Light theme", 1440, 900, "&theme=light"),
  f("hist-light", "hist", "Light theme", 1440, 900, "&theme=light"),
  f("off-light", "off", "Light theme", 1440, 900, "&theme=light"),
  f("empty-fa", "empty", "Right to left (fa)", 1440, 900, "&lang=fa"),
  f("answer-fa", "answer", "Right to left (fa)", 1440, 900, "&lang=fa"),
  f("md-fa", "empty.md", "Right to left (fa)", 1440, 900, "&lang=fa"),
  f("hist-fa", "hist", "Right to left (fa)", 1440, 900, "&lang=fa"),
  f("answer-ru", "answer", "Russian", 1440, 900, "&lang=ru"),
  f("empty-ru", "empty", "Russian", 1440, 900, "&lang=ru"),
  f("fx", "empty", "With the reveal animation on: the wave and the glints of the opening, the dotted halo", 1440, 900, "&fx=1"),
  f("fx-d2", "empty.md.d2", "With the reveal animation on: the slider's wave and particles at the deepest place", 1440, 900, "&fx=1"),
  // a phone
  f("phone-empty", "empty", "A phone, 400 wide: a card 12px from the edges", P, PH),
  f("phone-answer", "answer", "A phone: the answer and the plan", P, PH),
  f("phone-md", "empty.md", "A phone: the choice of the agent", P, PH),
  f("phone-hist", "hist", "A phone: the conversations", P, PH),
  f("phone-off", "off", "A phone: not connected", P, PH),
  f("phone-light", "answer", "A phone, light theme", P, PH, "&theme=light"),
  f("phone-fa", "empty.md", "A phone, right to left", P, PH, "&lang=fa"),
  f("phone-big", "answer.big", "A phone asked for the big window: it stays a card, with no button for it", P, PH),
  f("phone-plans", "answer.plans", "A phone: more plans", P, PH),
];

export function PnAssistantKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Assistant</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((fr) => (
        <Shot key={fr.id} id={fr.id} title={fr.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={fr.title} src={`${path}?only=pn-assistant-page&state=${fr.state}${fr.params ?? ""}`} width={fr.w} height={fr.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
