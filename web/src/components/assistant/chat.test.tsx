import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { setLang } from "@/i18n";
import { ChatPanel, useChat } from "./chat";

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
    expect(text()).toContain("nodes_list · 3");
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
