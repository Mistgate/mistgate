import { ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { AssistantErrorSchema, AssistantProvider, ChatRole, PlanState, type ChatMessage, type FollowUp as FollowUpPb, type GetAssistantAccessResponse, type MemoryNote, type PageContext, type PlanCard, type ToolCall } from "@/gen/mistgate/admin/v1/assistant_pb";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "./api";
import { plain, type Plain } from "./plain";

// The assistant (ADR 0014): what the admin may use, the owner's settings, the admin's own conversations and memory.
// Query keys sit under "assistant". The chat itself streams (useChat in components/assistant).

export type Msg = Plain<ChatMessage>;
export type Plan = Plain<PlanCard>;
export type Tool = Plain<ToolCall>;
export type Note = Plain<MemoryNote>;
export type Context = Plain<PageContext>;
export type FollowUp = Plain<FollowUpPb>;

export const accessQuery = queryOptions({
  queryKey: ["assistant", "access"],
  queryFn: async ({ signal }) => plain(await assistant.getAssistantAccess({}, { signal })),
  staleTime: 30_000,
});

/**
 * When the chat button should ask again: soon after the admin's next follow-up is due (its answer makes the unread dot), every
 * 15 s while one is due and not answered yet (it runs), never when none waits.
 */
export function followUpPoll(access: Pick<Plain<GetAssistantAccessResponse>, "nextFollowUpUnix"> | undefined, now = Date.now()): number | false {
  const next = Number(access?.nextFollowUpUnix ?? 0);
  if (!next) return false;
  const ms = next * 1000 - now;
  return ms > 0 ? Math.min(ms + 5_000, 10 * 60_000) : 15_000;
}

export const settingsQuery = queryOptions({
  queryKey: ["assistant", "settings"],
  queryFn: async ({ signal }) => plain(await assistant.getAssistantSettings({}, { signal })),
  staleTime: 10_000,
});

export const memoryQuery = queryOptions({
  queryKey: ["assistant", "memory"],
  queryFn: async ({ signal }) => plain(await assistant.listMemory({}, { signal })).notes,
});

export const conversationsQuery = (query: string) =>
  queryOptions({
    queryKey: ["assistant", "conversations", query],
    queryFn: async ({ signal }) => plain(await assistant.listConversations({ query, limit: 50 }, { signal })).conversations,
  });

export const conversationQuery = (id: string) =>
  queryOptions({
    queryKey: ["assistant", "conversation", id],
    queryFn: async ({ signal }) => plain(await assistant.getConversation({ id }, { signal })),
    enabled: id !== "",
  });

/** The provider's i18n name key and data line, in the order the settings show them. */
export const providers = [
  { id: AssistantProvider.ANTHROPIC, name: "ai.prov.anthropic", data: "ai.prov.anthropic.data" },
  { id: AssistantProvider.DEEPSEEK, name: "ai.prov.deepseek", data: "ai.prov.deepseek.data" },
  { id: AssistantProvider.ZAI, name: "ai.prov.zai", data: "ai.prov.zai.data" },
  { id: AssistantProvider.OLLAMA_CLOUD, name: "ai.prov.ollama", data: "ai.prov.ollama.data" },
] as const satisfies readonly { id: AssistantProvider; name: MessageKey; data: MessageKey }[];

export const providerName = (p: AssistantProvider): MessageKey => providers.find((x) => x.id === p)?.name ?? "ai.prov.anthropic";

const errorCodes = new Set([
  "assistant_off", "not_allowed", "no_provider", "edition", "turn_running", "plan_not_pending", "budget_reached", "rate_limited",
  "provider_unreachable", "provider_overloaded", "bad_key", "no_balance", "text_too_long", "note_too_long", "too_many_notes",
  "follow_up_not_pending",
]);

/** Why a follow-up did not run, in words: the reasons of its own, then the codes the chat's errors word. */
export function followUpReasonKey(reason: string): MessageKey | null {
  if (reason === "interrupted" || reason === "internal") return `ai.fu.reason.${reason}`;
  return assistantErrorKey(reason);
}

/** The words for an assistant refusal (the AssistantError detail of a Connect error, or a code from the stream), or null. */
export function assistantErrorKey(e: unknown): MessageKey | null {
  const code = typeof e === "string" ? e : e instanceof Error ? (ConnectError.from(e).findDetails(AssistantErrorSchema)[0]?.code ?? "") : "";
  return errorCodes.has(code) ? (`ai.err.${code}` as MessageKey) : null;
}

const secretLabel = /(?:^|[^A-Za-z0-9_])(?:password|passwd|pwd|pass|secret|token|api[\s_-]*key|private[\s_-]*key|key|license(?:[\s_-]*key)?|licence(?:[\s_-]*key)?|пароль|токен|ключ|лицензия)\s*[:=]\s*(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)/iu;
const secretShape = /\b(?:sk-ant-[A-Za-z0-9_-]{10,}|sk-proj-[A-Za-z0-9_-]{10,}|sk-[A-Za-z0-9_-]{20,}|(?:ghp|gho)_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|xox[abpr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|AIza[A-Za-z0-9_-]{30,}|AGE-SECRET-KEY-1[0-9A-Z]{30,})\b|\b[0-9]{6,}:[A-Za-z0-9_-]{30,}|eyJ[A-Za-z0-9_-]*\.eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]{8,}|\bBearer\s+\S+/i;
const uuidValue = /^[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}$/i;
const uuidInText = /\b[\da-f]{8}(?:-[\da-f]{4}){3}-[\da-f]{12}\b/gi;

function highEntropyKey(value: string): boolean {
  if (value.length < 32 || uuidValue.test(value)) return false;
  if (value.length === 43 && /^[A-Za-z0-9+/_-]{43}$/.test(value)) return true;
  const hexKey = /^[\da-f]{32,}$/i.test(value);
  const counts = new Map<string, number>();
  const classes = new Set<string>();
  for (const char of value) {
    counts.set(char, (counts.get(char) ?? 0) + 1);
    classes.add(/[a-z]/.test(char) ? "lower" : /[A-Z]/.test(char) ? "upper" : /\d/.test(char) ? "digit" : "punctuation");
  }
  if (classes.size < 3 && !hexKey) return false;
  let entropy = 0;
  for (const count of counts.values()) {
    const share = count / value.length;
    entropy -= share * Math.log2(share);
  }
  return entropy >= 3.5;
}

/** The text looks like a secret someone pasted by mistake: a credential label, token, private key, or URL password. */
export function looksSecret(text: string): boolean {
  return (
    secretLabel.test(text) ||
    /-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----/i.test(text) ||
    secretShape.test(text) ||
    /[a-z]+:\/\/[^\s/:@]+:[^\s/@]+@/i.test(text) ||
    [...text.replace(uuidInText, " ").matchAll(/[A-Za-z0-9+/_=-]{32,}/g)].some(([value]) => highEntropyKey(value))
  );
}

/** A tool call as the chat shows it: what the server sends, plus how long it took (measured here, only for a live turn). */
export type LiveTool = Tool & { startedAt?: number; ms?: number };
/** A turn as the chat shows it: `interimEnd` is where in the text the last tool round began (what came before it is the
 *  model thinking aloud, not the answer), `t0`/`workedMs` the turn's working time. All three only exist for a live turn. */
export type LiveMsg = Msg & { interimEnd?: number; t0?: number; workedMs?: number };

/** The text a turn said before its last tool round, and the answer after it. History has no split: all of it is the answer. */
export function splitText(m: Msg): { interim: string; answer: string } {
  const end = (m as LiveMsg).interimEnd ?? 0;
  return end > 0 ? { interim: m.text.slice(0, end).trim(), answer: m.text.slice(end).trim() } : { interim: "", answer: m.text };
}

/** Opens the assistant chat as a new conversation with `text` in the composer (the person reads it and sends it). */
export const askAssistant = (text: string) => window.dispatchEvent(new CustomEvent("mistgate:assistant-open", { detail: { text } }));

export const isAssistant =(m: Msg) => m.role === ChatRole.ASSISTANT;
export const planOpen = (p: Plan) => p.state === PlanState.PROPOSED;
