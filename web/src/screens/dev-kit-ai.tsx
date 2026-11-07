import { useEffect } from "react";
import { setTheme } from "@/lib/theme";
import { AssistantAudience, AssistantEffort, AssistantProvider, ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { ChatButton, ChatPanel, type ChatApi } from "@/components/assistant/chat";
import type { LiveMsg, Msg } from "@/lib/assistant";
import { Shot } from "./dev-kit-redesign";
import { MemoryTab } from "./ai/memory";
import { AssistantTab } from "./ai/settings";

// Development only, /dev-kit?only=ai&state=settings|memory|chat|chat-typing|chat-empty: the AI section (ADR 0014) and the floating
// chat on made-up data, one state per address (for screenshots).

const NOW = Math.floor(Date.now() / 1000);

const settings = {
  settings: { enabled: true, provider: AssistantProvider.ANTHROPIC, effort: AssistantEffort.NORMAL, audience: AssistantAudience.ADMINS, panelNotes: "nl1 is the test node. Never touch the group Family without asking.", retentionDays: 90 },
  providers: [
    { provider: AssistantProvider.ANTHROPIC, connected: true, keyHint: "Q7wA", addedUnix: NOW - 86400 * 3, choice: { provider: AssistantProvider.ANTHROPIC, model: "claude-opus-5-5", budgetUsdMonth: 30 }, spentUsdMonth: 4.18, lastErrorCode: "", lastErrorUnix: 0 },
    { provider: AssistantProvider.DEEPSEEK, connected: false, keyHint: "", addedUnix: 0, choice: { provider: AssistantProvider.DEEPSEEK, model: "", budgetUsdMonth: 20 }, spentUsdMonth: 0, lastErrorCode: "", lastErrorUnix: 0 },
    { provider: AssistantProvider.ZAI, connected: false, keyHint: "", addedUnix: 0, choice: { provider: AssistantProvider.ZAI, model: "", budgetUsdMonth: 20 }, spentUsdMonth: 0, lastErrorCode: "", lastErrorUnix: 0 },
    { provider: AssistantProvider.OLLAMA_CLOUD, connected: true, keyHint: "x9Lk", addedUnix: NOW - 86400, choice: { provider: AssistantProvider.OLLAMA_CLOUD, model: "", budgetUsdMonth: 10 }, spentUsdMonth: 0.42, lastErrorCode: "rate_limited", lastErrorUnix: NOW - 3600 },
  ],
  usage: [
    { adminId: "adm_1", adminName: "Owner", usd: 3.71, turns: 48 },
    { adminId: "adm_2", adminName: "Anna (helper)", usd: 0.89, turns: 12 },
  ],
};

const models = [
  { id: "claude-opus-5-5", name: "Claude Opus 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 4, outputUsdPerMtok: 20, recommended: true },
  { id: "claude-sonnet-5-5", name: "Claude Sonnet 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 2, outputUsdPerMtok: 10, recommended: false },
  { id: "claude-haiku-5-5", name: "Claude Haiku 5.5", contextTokens: 1_000_000, inputUsdPerMtok: 0.1, outputUsdPerMtok: 0.5, recommended: false },
];

const notes = [
  { id: "mem_1", text: "nl1 is the test node: changes go there first.", createdUnix: NOW - 86400 * 5, updatedUnix: NOW - 86400 * 5, byAssistant: false, conversationId: "" },
  { id: "mem_2", text: "The owner prefers Hysteria2 for people on mobile networks in Russia.", createdUnix: NOW - 7200, updatedUnix: NOW - 7200, byAssistant: true, conversationId: "cnv_1" },
  { id: "mem_3", text: "Anna's devices: a Pixel and a MacBook; she uses kl!ck on the laptop.", createdUnix: NOW - 3600, updatedUnix: NOW - 3600, byAssistant: true, conversationId: "cnv_2" },
];

const conversations = [
  { id: "cnv_2", title: "Anna over the device limit", createdUnix: NOW - 3700, updatedUnix: NOW - 3500, messages: 6 },
  { id: "cnv_1", title: "Why de1 dropped connections this morning", createdUnix: NOW - 7300, updatedUnix: NOW - 7000, messages: 4 },
  { id: "cnv_0", title: "A group for the family with Hysteria2", createdUnix: NOW - 86400, updatedUnix: NOW - 86000, messages: 8 },
];

const msg = (m: Partial<LiveMsg> & Pick<Msg, "role" | "text">): Msg => ({ id: Math.random().toString(36), createdUnix: NOW, tools: [], plans: [], remembered: [], end: "", ...m }) as Msg;

const at = (minutesAgo: number) => NOW - minutesAgo * 60;
const failure = "tool input rejected: unknown field \"since\" (allowed: node, hours, severity, limit) in the arguments of alerts_list";

const conversation: Msg[] = [
  msg({ role: ChatRole.USER, createdUnix: NOW - 86400 - 600, text: "Which nodes had problems yesterday?" }),
  msg({ role: ChatRole.ASSISTANT, createdUnix: NOW - 86400 - 580, text: "Only **nl1**: it restarted twice around 22:10, both times on its own.", end: "end" }),
  msg({ role: ChatRole.USER, createdUnix: at(12), text: "Anna says her new phone does not connect. What is going on?", context: { kind: "user", id: "usr_anna", label: "Users · anna", route: "/users/usr_anna" } as never }),
  msg({ role: ChatRole.USER, createdUnix: at(12), text: "She is on the home wifi, if that matters." }),
  msg({
    role: ChatRole.ASSISTANT,
    createdUnix: at(11),
    tools: [
      { id: "t1", tool: "user_get", argsJson: '{"user":"anna"}', done: true, ok: true, count: 0, error: "", ms: 1400 },
      { id: "t2", tool: "user_devices", argsJson: '{"user":"usr_anna"}', done: true, ok: true, count: 3, error: "", ms: 1400 },
      { id: "t3", tool: "events_search", argsJson: '{"user":"usr_anna","hours":24}', done: true, ok: true, count: 5, error: "", ms: 1400 },
      { id: "t4", tool: "alerts_list", argsJson: '{"since":"24h"}', done: true, ok: false, count: 0, error: failure, ms: 1400 },
      { id: "t5", tool: "node_journal", argsJson: '{"node":"de1"}', done: true, ok: false, count: 0, error: "node de1 is offline: no journal to read", ms: 1400 },
    ] as never,
    text: "Let me check her devices and the recent refusals first.\n\nAnna is at her device limit: **3 of 3**, and the new Pixel was refused at **09:41** (the panel's *Limit* mode).\n\n1. `iPhone 13` — last seen 2 days ago\n2. `MacBook` — kl!ck, online now\n3. `Pixel 7` — the old phone, silent for **41 days**\n\nThe old Pixel is the one to let go. I prepared that change; apply it and the new phone connects at its next refresh. See [anna](/users/usr_anna).",
    plans: [
      {
        planId: "pln_1",
        tool: "device_revoke",
        summary: "Remove Anna's device “Pixel 7”: its keys stop working at once.",
        facts: [
          { key: "user", value: "anna", untrusted: true, code: "", params: {}, untrustedParams: [] },
          { key: "device", value: "Pixel 7", untrusted: true, code: "", params: {}, untrustedParams: [] },
        ],
        danger: [],
        needsApproval: false,
        inboxOnly: false,
        expiresUnix: NOW + 540,
        state: PlanState.PROPOSED,
        result: "",
        error: "",
        outcomeCode: "",
      },
    ] as never,
    remembered: [{ id: "mem_3", text: "Anna's devices: a Pixel and a MacBook; she uses kl!ck on the laptop.", createdUnix: NOW, updatedUnix: NOW, byAssistant: true, conversationId: "cnv_2" }],
    interimEnd: 57,
    t0: NOW * 1000 - 7000,
    workedMs: 7000,
    end: "budget",
  }),
];

/** The same thread with the answer still being written: a tool is running and nothing is said yet. */
const working: Msg[] = [
  ...conversation.slice(0, 4),
  msg({ role: ChatRole.ASSISTANT, createdUnix: at(1), text: "", tools: [{ id: "t1", tool: "user_devices", argsJson: "{}", done: true, ok: true, count: 3, error: "" }, { id: "t2", tool: "node_journal", argsJson: "{}", done: false, ok: false, count: 0, error: "" }] }),
];

const access = {
  available: true,
  reason: "",
  provider: AssistantProvider.ANTHROPIC,
  model: "claude-opus-5-5",
  readOnly: false,
  effort: AssistantEffort.NORMAL,
  choices: [
    { provider: AssistantProvider.ANTHROPIC, model: "claude-opus-5-5", inputUsdPerMtok: 4, outputUsdPerMtok: 20, budgetReached: false },
    { provider: AssistantProvider.DEEPSEEK, model: "deepseek-v4.1-flash", inputUsdPerMtok: 0.3, outputUsdPerMtok: 1.2, budgetReached: false },
    { provider: AssistantProvider.OLLAMA_CLOUD, model: "gpt-oss:120b", inputUsdPerMtok: 0, outputUsdPerMtok: 0, budgetReached: true },
  ],
};

function fakeChat(messages: Msg[], running = false): ChatApi {
  const none = async () => {};
  return { conversationId: "cnv_2", messages, running, error: null, send: none, stop: none, open: none, reset: () => {}, apply: none, dismiss: none, forget: none } as unknown as ChatApi;
}

export function AiKit() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "settings";
  const theme = params.get("theme");
  // ?theme=light|dark picks the theme for a screenshot
  useEffect(() => {
    if (theme === "light" || theme === "dark") setTheme(theme);
  }, [theme]);
  if (state === "settings") {
    return (
      <Shot id="ai-settings" title="AI → Assistant" seed={[[["assistant", "settings"], settings], [["assistant", "models", AssistantProvider.ANTHROPIC], models], [["assistant", "models", AssistantProvider.OLLAMA_CLOUD], []]]}>
        <AssistantTab />
      </Shot>
    );
  }
  if (state === "memory") {
    return (
      <Shot id="ai-memory" title="AI → Memory" seed={[[["assistant", "memory"], notes], [["assistant", "conversations", ""], conversations]]}>
        <MemoryTab />
      </Shot>
    );
  }
  if (state === "fab" || state === "fab-busy") {
    return (
      <Shot id={`ai-${state}`} title="The open button" seed={[]}>
        <div className="relative h-[200px]">
          <ChatButton onClick={() => {}} running={state === "fab-busy"} />
        </div>
      </Shot>
    );
  }
  const messages = state === "chat-empty" ? [] : state === "chat-typing" ? working : conversation;
  return (
    <Shot id={`ai-${state}`} title="The floating chat" seed={[[["assistant", "conversations", ""], conversations], [["assistant", "access"], access]]}>
      <div className="relative h-[1000px]">
        <ChatPanel chat={fakeChat(messages, state === "chat-typing")} readOnly={false} providerLabel="claude-opus-5-5" provider={AssistantProvider.ANTHROPIC} onClose={() => {}} pickerOpen={params.get("menu") === "1"} initialContext={{ kind: "user", id: "usr_anna", label: "Users · anna", route: "/users/usr_anna" } as never} />
      </div>
    </Shot>
  );
}
