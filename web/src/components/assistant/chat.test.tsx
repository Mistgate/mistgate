import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { setLang } from "@/i18n";
import type { Msg } from "@/lib/assistant";
import { ChatPanel, useChat, type ChatApi } from "./chat";

const chat = vi.fn();
const applyPlan = vi.fn();
const dismissPlan = vi.fn();
const deleteMemory = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  assistant: {
    chat: (...a: unknown[]) => chat(...a),
    applyPlan: (...a: unknown[]) => applyPlan(...a),
    dismissPlan: (...a: unknown[]) => dismissPlan(...a),
    deleteMemory: (...a: unknown[]) => deleteMemory(...a),
    stopTurn: async () => ({}),
    listConversations: async () => ({ conversations: [] }),
    getAssistantSettings: async () => ({
      settings: { enabled: true, provider: 1, effort: 2, audience: 1, panelNotes: "", retentionDays: 90 },
      providers: [
        { provider: 1, connected: true, keyHint: "a", addedUnix: 0n, choice: { provider: 1, model: "claude-opus-5-5", budgetUsdMonth: 30 }, spentUsdMonth: 1, lastErrorCode: "", lastErrorUnix: 0n },
        { provider: 2, connected: true, keyHint: "b", addedUnix: 0n, choice: { provider: 2, model: "deepseek-v4.1-flash", budgetUsdMonth: 20 }, spentUsdMonth: 0, lastErrorCode: "", lastErrorUnix: 0n },
        { provider: 3, connected: false, keyHint: "", addedUnix: 0n, choice: { provider: 3, model: "", budgetUsdMonth: 20 }, spentUsdMonth: 0, lastErrorCode: "", lastErrorUnix: 0n },
      ],
      usage: [],
    }),
  },
}));
vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));
vi.mock("@/lib/updates", () => ({ useIsOwner: () => true }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname: "/nodes/nod_1" } }),
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
  for (const m of [chat, applyPlan, dismissPlan, deleteMemory]) m.mockReset();
});

const ev = (c: string, value: unknown) => ({ event: { case: c, value } });
const plan = { planId: "pln_1", tool: "user_disable", summary: "Disable 1 user: their connections end.", facts: [], danger: [], needsApproval: false, inboxOnly: false, expiresUnix: 0n, state: PlanState.PROPOSED, result: "", error: "", outcomeCode: "" };

function Harness() {
  const c = useChat();
  return <ChatPanel chat={c} readOnly={false} providerLabel="claude-opus-5-5" provider={1} onClose={() => {}} />;
}

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Harness />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
async function send(value: string) {
  const area = document.querySelector("textarea")!;
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(area, value);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await click(button("Send"));
  await settle();
}

describe("the assistant's chat", () => {
  it("streams an answer with its tool lines and the page it was asked on", async () => {
    chat.mockImplementation(async function* () {
      yield ev("started", { conversationId: "cnv_1", userMessageId: "m1", assistantMessageId: "m2" });
      yield ev("toolStarted", { id: "t1", tool: "nodes_list", argsJson: "{}", done: false, ok: false, count: 0, error: "" });
      yield ev("toolFinished", { id: "t1", tool: "nodes_list", argsJson: "{}", done: true, ok: true, count: 3, error: "" });
      yield ev("textDelta", "Two nodes are **down**: ");
      yield ev("textDelta", "[de1](/nodes/nod_1) and nl1.");
      yield ev("done", { end: "end", title: "Nodes down" });
    });
    await mount();
    expect(text()).toContain("Page: Nodes · nod_1");
    await send("What is down?");
    expect(chat).toHaveBeenCalledWith(expect.objectContaining({ conversationId: "", text: "What is down?", context: expect.objectContaining({ kind: "node", id: "nod_1" }) }), expect.anything());
    // a card per call, closed until asked, with the time it took (measured here)
    const card = document.querySelector("[data-slot=tool]")!;
    expect(card.textContent).toContain("nodes_list");
    expect(card.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
    expect(card.getAttribute("data-state")).toBe("ok");
    expect(document.querySelector("strong")?.textContent).toBe("down");
    expect(document.querySelector("a[href='/nodes/nod_1']")?.textContent).toBe("de1");
  });

  it("shows a plan as a card that only the click applies, and a remembered note that can be forgotten", async () => {
    chat.mockImplementation(async function* () {
      yield ev("started", { conversationId: "cnv_2", userMessageId: "m1", assistantMessageId: "m2" });
      yield ev("plan", plan);
      yield ev("remembered", { id: "mem_1", text: "nl1 is the test node", createdUnix: 0n, updatedUnix: 0n, byAssistant: true, conversationId: "cnv_2" });
      yield ev("done", { end: "end", title: "" });
    });
    applyPlan.mockResolvedValue({ plan: { ...plan, state: PlanState.APPLIED, result: "1 user disabled" } });
    deleteMemory.mockResolvedValue({});
    await mount();
    await send("Disable anna");
    expect(text()).toContain("Proposed change");
    expect(text()).toContain("Disable 1 user: their connections end.");
    expect(applyPlan).not.toHaveBeenCalled();
    await click(button("Apply"));
    await settle();
    expect(applyPlan).toHaveBeenCalledWith({ conversationId: "cnv_2", planId: "pln_1" });
    expect(text()).toContain("Applied: 1 user disabled");
    expect(text()).toContain("Remembered: nl1 is the test node");
    await click(button("Forget"));
    await settle();
    expect(deleteMemory).toHaveBeenCalledWith({ id: "mem_1" });
    expect(text()).not.toContain("Remembered: nl1 is the test node");
  });

  it("words a refusal that ends the turn", async () => {
    chat.mockImplementation(async function* () {
      yield ev("started", { conversationId: "cnv_3", userMessageId: "m1", assistantMessageId: "m2" });
      yield ev("error", { code: "budget_reached" });
    });
    await mount();
    await send("Hello");
    expect(text()).toContain("This month's budget for the provider is spent.");
  });

  it("sends the agent and the depth picked in the composer with the turn, and remembers them", async () => {
    chat.mockImplementation(async function* () {
      yield ev("done", { end: "end", title: "" });
    });
    await mount();
    await settle();
    // not picked yet: the panel's own setting
    await send("first");
    expect(chat.mock.calls[0]![0]).toMatchObject({ provider: 0, effort: 0 });
    await click(button("Choose the agent"));
    await settle();
    const radios = [...document.querySelectorAll("[role=menuitemradio]")];
    expect(radios.map((r) => r.textContent)).toEqual(expect.arrayContaining([expect.stringContaining("DeepSeek"), expect.stringContaining("Thorough")]));
    await click(radios.find((r) => r.textContent?.includes("DeepSeek")));
    await settle();
    const thorough = [...document.querySelectorAll("[role=menuitemradio]")].find((r) => r.textContent?.includes("Thorough"));
    expect(thorough).toBeDefined();
    await click(thorough);
    await settle();
    await send("second");
    expect(chat.mock.calls[1]![0]).toMatchObject({ provider: 2, effort: 3 });
    expect(localStorage.getItem("assistant-provider")).toBe("2");
    expect(localStorage.getItem("assistant-effort")).toBe("3");
    expect(text()).toContain("DeepSeek · deepseek-v4.1-flash · Thorough");
  });

  it("types while the first token is awaited and names the tool that runs", async () => {
    let release: () => void = () => {};
    chat.mockImplementation(async function* () {
      yield ev("started", { conversationId: "cnv_4", userMessageId: "m1", assistantMessageId: "m2" });
      await new Promise<void>((r) => (release = r));
      yield ev("done", { end: "end", title: "" });
    });
    await mount();
    await send("Hi");
    expect(document.querySelector("[data-slot=typing]")).not.toBeNull();
    expect(text()).toContain("Assistant is thinking…");
    expect(document.querySelectorAll("[data-slot=avatar]").length).toBe(1);
    await act(async () => release());
    await settle();
    expect(document.querySelector("[data-slot=typing]")).toBeNull();
  });
});

const ts = (daysAgo: number, minute = 0) => {
  const d = new Date();
  d.setHours(12, minute, 0, 0);
  return Math.floor(d.getTime() / 1000) - daysAgo * 86400;
};
const message = (role: ChatRole, createdUnix: number, body: string, extra: Record<string, unknown> = {}) =>
  ({ id: `m-${Math.random()}`, role, text: body, createdUnix, tools: [], plans: [], remembered: [], end: "", ...extra }) as unknown as Msg;
const fakeChat = (messages: Msg[], running = false) => ({ conversationId: "c", messages, running, error: null, send: async () => {}, stop: async () => {}, open: async () => {}, reset: () => {}, apply: async () => {}, dismiss: async () => {}, forget: async () => {} }) as unknown as ChatApi;

async function mountThread(c: ChatApi) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <ChatPanel chat={c} readOnly={false} providerLabel="m" onClose={() => {}} initialContext={null} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}

describe("the assistant's thread", () => {
  it("groups by sender: name and time on the first, the avatar on the last only, a date line between days", async () => {
    await mountThread(
      fakeChat([
        message(ChatRole.USER, ts(1), "yesterday question"),
        message(ChatRole.ASSISTANT, ts(1, 1), "first answer"),
        message(ChatRole.ASSISTANT, ts(1, 2), "second answer"),
        message(ChatRole.USER, ts(0), "today one"),
        message(ChatRole.USER, ts(0, 1), "today two"),
      ]),
    );
    const rows = [...document.querySelectorAll("[data-role=assistant]")];
    expect(rows).toHaveLength(2);
    expect(rows[0]!.textContent).toContain("Assistant");
    expect(rows[0]!.querySelector("[data-slot=avatar]")).toBeNull();
    expect(rows[1]!.textContent).not.toContain("Assistant");
    expect(rows[1]!.querySelector("[data-slot=avatar]")).not.toBeNull();
    expect(text()).toContain("Yesterday");
    expect(text()).toContain("Today");
    // two own messages in a row: one status line, under the last
    expect(document.querySelectorAll("[data-role=user]")).toHaveLength(3);
    expect(text().match(/Sent|Read/g)).toHaveLength(1);
  });

  it("shows each tool call as a card: icon, name, the telling argument, state; a failure is tinted, not shouted", async () => {
    const tool = (id: string, ok: boolean, error = "", args = '{"node":"de1","limit":5}') => ({ id, tool: "node_journal", argsJson: args, done: true, ok, count: ok ? 2 : 0, error, ms: 1400 });
    await mountThread(
      fakeChat([
        message(ChatRole.USER, ts(0), "q"),
        message(ChatRole.ASSISTANT, ts(0, 1), "answer", { tools: [tool("t1", true), tool("t2", false, "tool input rejected: a very long reason")], t0: Date.now() - 6000, workedMs: 6000, interimEnd: 9, text: "Checking.\nanswer" }),
      ]),
    );
    const cards = [...document.querySelectorAll("[data-slot=tool]")];
    expect(cards).toHaveLength(2);
    expect(cards[0]!.textContent).toContain("de1");
    expect(cards[0]!.textContent).toContain("1.4s");
    expect(cards[1]!.getAttribute("data-state")).toBe("failed");
    // closed until asked; the input is coloured JSON and the output the error
    const head = cards[1]!.querySelector("button")!;
    expect(head.getAttribute("aria-expanded")).toBe("false");
    await click(head);
    expect(head.getAttribute("aria-expanded")).toBe("true");
    expect(cards[1]!.textContent).toContain("tool input rejected: a very long reason");
    expect(cards[1]!.textContent).toContain('"node"');
    // thinking header with the working time, over what was said between rounds; the answer stays the bubble
    const thinking = document.querySelector("[data-slot=thinking]")!;
    expect(thinking.textContent).toContain("Thought for");
    expect(thinking.textContent).toContain("6s");
    expect(thinking.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
    await click(thinking.querySelector("button"));
    expect(thinking.textContent).toContain("Checking.");
    expect(document.querySelector("[data-slot=bubble]")?.textContent).toBe("answer");
  });

  it("keeps the last three cards of a long turn and folds the rest under a muted line", async () => {
    const tools = Array.from({ length: 7 }, (_, i) => ({ id: `t${i}`, tool: `alerts_list`, argsJson: `{"n":${i}}`, done: true, ok: true, count: 1, error: "" }));
    await mountThread(fakeChat([message(ChatRole.USER, ts(0), "q"), message(ChatRole.ASSISTANT, ts(0, 1), "answer", { tools })]));
    expect(document.querySelectorAll("[data-slot=tool]")).toHaveLength(3);
    expect(text()).toContain("+4 earlier tool calls");
    await click(button("+4 earlier tool calls"));
    expect(document.querySelectorAll("[data-slot=tool]")).toHaveLength(7);
  });

  it("shows the typing line with the running tool in words, and ends a turn with one muted line", async () => {
    const running = { id: "t1", tool: "node_journal", argsJson: "{}", done: false, ok: false, count: 0, error: "" };
    await mountThread(fakeChat([message(ChatRole.USER, ts(0), "q"), message(ChatRole.ASSISTANT, ts(0, 1), "", { tools: [running] })], true));
    expect(text()).toContain("Checking node journal…");
    act(() => root?.unmount());
    host?.remove();
    await mountThread(fakeChat([message(ChatRole.USER, ts(0), "q"), message(ChatRole.ASSISTANT, ts(0, 1), "partial", { end: "budget" })]));
    expect(text()).toContain("This month's budget is spent.");
    expect(document.querySelector("[data-slot=typing]")).toBeNull();
  });
});

describe("the assistant's chat, more", () => {
  it("asks before sending something that looks like a key", async () => {
    chat.mockImplementation(async function* () {
      yield ev("done", { end: "end", title: "" });
    });
    await mount();
    await send("my key is sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789");
    expect(chat).not.toHaveBeenCalled();
    expect(text()).toContain("This looks like a key or a password, and the message goes to Anthropic.");
    await click(button("Send anyway"));
    await settle();
    expect(chat).toHaveBeenCalledTimes(1);
  });
});
