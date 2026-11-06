import { ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { AssistantErrorSchema, AssistantProvider, ChatRole, PlanState, type ChatMessage, type MemoryNote, type PageContext, type PlanCard, type ToolCall } from "@/gen/mistgate/admin/v1/assistant_pb";
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

export const accessQuery = queryOptions({
  queryKey: ["assistant", "access"],
  queryFn: async ({ signal }) => plain(await assistant.getAssistantAccess({}, { signal })),
  staleTime: 30_000,
});

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
]);

/** The words for an assistant refusal (the AssistantError detail of a Connect error, or a code from the stream), or null. */
export function assistantErrorKey(e: unknown): MessageKey | null {
  const code = typeof e === "string" ? e : e instanceof Error ? (ConnectError.from(e).findDetails(AssistantErrorSchema)[0]?.code ?? "") : "";
  return errorCodes.has(code) ? (`ai.err.${code}` as MessageKey) : null;
}

/** The text looks like a secret someone pasted by mistake: a long token-like run, a private key block, a URL with a password. */
export function looksSecret(text: string): boolean {
  return (
    /-----BEGIN [A-Z ]*PRIVATE KEY-----/.test(text) ||
    /\b(sk|pk|rk|ghp|gho|xox[abp]|AKIA|AGE-SECRET-KEY)[-_A-Za-z0-9]{12,}/.test(text) ||
    /[a-z]+:\/\/[^\s/:@]+:[^\s/@]+@/i.test(text) ||
    /\b[A-Za-z0-9+/_-]{40,}={0,2}\b/.test(text)
  );
}

export const isAssistant = (m: Msg) => m.role === ChatRole.ASSISTANT;
export const planOpen = (p: Plan) => p.state === PlanState.PROPOSED;
