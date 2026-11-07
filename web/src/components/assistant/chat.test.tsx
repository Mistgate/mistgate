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
const approve = vi.fn();
const setProviderKey = vi.fn();
let approvalList: unknown[] = [];
// the saved access of other nodes: one with panelKeyFingerprint makes the panel key the default way in
let accessList: { nodeId: string; panelKeyFingerprint: string }[] = [];
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  approvals: { listApprovals: async () => ({ approvals: approvalList, awaiting: approvalList.length }), approve: (...a: unknown[]) => approve(...a), reject: async () => ({}) },
  provisioning: { listNodeServerAccess: async () => ({ access: accessList }), getPanelSSHKey: async () => ({ publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7cYk4m1x mistgate-panel", fingerprint: "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl" }) },
  assistant: {
    chat: (...a: unknown[]) => chat(...a),
    applyPlan: (...a: unknown[]) => applyPlan(...a),
    dismissPlan: (...a: unknown[]) => dismissPlan(...a),
    deleteMemory: (...a: unknown[]) => deleteMemory(...a),
    stopTurn: async () => ({}),
    setProviderKey: (...a: unknown[]) => setProviderKey(...a),
    listConversations: async () => ({ conversations: [] }),
    getAssistantAccess: async () => ({
      available: true,
      reason: "",
      provider: 1,
      model: "claude-opus-5-5",
      readOnly: false,
      effort: 2,
      choices: [
        { provider: 1, model: "claude-opus-5-5", inputUsdPerMtok: 4, outputUsdPerMtok: 20, budgetReached: false },
        { provider: 2, model: "deepseek-v4.1-flash", inputUsdPerMtok: 0.3, outputUsdPerMtok: 1.2, budgetReached: false },
        { provider: 4, model: "gpt-oss", inputUsdPerMtok: 0, outputUsdPerMtok: 0, budgetReached: true },
      ],
    }),
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
let owner = true;
vi.mock("@/lib/updates", () => ({ useIsOwner: () => owner }));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => navigate,
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
  for (const m of [chat, applyPlan, dismissPlan, deleteMemory, navigate]) m.mockReset();
  localStorage.clear();
  owner = true;
  accessList = [];
  for (const m of [approve, setProviderKey]) m.mockReset();
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
    // a provider whose budget is spent is listed but cannot be picked
    expect(radios.find((r) => r.textContent?.includes("budget spent"))?.getAttribute("aria-disabled")).toBe("true");
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
  it("groups by sender: name and time on the first, the avatar beside the first only, a date line between days", async () => {
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
    expect(rows[0]!.querySelector("[data-slot=avatar]")).not.toBeNull();
    expect(rows[1]!.textContent).not.toContain("Assistant");
    expect(rows[1]!.querySelector("[data-slot=avatar]")).toBeNull();
    expect(text()).toContain("Yesterday");
    expect(text()).toContain("Today");
    // an assistant does not "read": only the time is under the bubbles
    expect(document.querySelectorAll("[data-role=user]")).toHaveLength(3);
    expect(text()).not.toMatch(/Read|Sent/);
  });

  it("folds the tool calls into one quiet group; a failure is a small glyph and a count, not a red box", async () => {
    const tool = (id: string, ok: boolean, error = "", args = '{"node":"de1","limit":5}') => ({ id, tool: "node_journal", argsJson: args, done: true, ok, count: ok ? 2 : 0, error, ms: 1400 });
    await mountThread(
      fakeChat([
        message(ChatRole.USER, ts(0), "q"),
        message(ChatRole.ASSISTANT, ts(0, 1), "answer", { tools: [tool("t1", true), tool("t2", false, "tool input rejected: a very long reason")], t0: Date.now() - 6000, workedMs: 6000, interimEnd: 9, text: "Checking.\nanswer" }),
      ]),
    );
    // one quiet group, folded to a summary once the turn is done
    const group = document.querySelector("[data-slot=tools]")!;
    const summary = group.querySelector("button")!;
    expect(summary.textContent).toContain("Used 2 tools");
    expect(summary.textContent).toContain("1 failed");
    expect(summary.getAttribute("aria-expanded")).toBe("false");
    await click(summary);
    expect(summary.getAttribute("aria-expanded")).toBe("true");
    const cards = [...document.querySelectorAll("[data-slot=tool]")];
    expect(cards).toHaveLength(2);
    // friendly name and the telling argument as a chip; the raw id only in the details
    expect(cards[0]!.querySelector("button")!.textContent).toContain("Node journal");
    expect(cards[0]!.querySelector("button")!.textContent).toContain("de1");
    expect(cards[0]!.querySelector("button")!.textContent).not.toContain("node_journal");
    expect(cards[1]!.getAttribute("data-state")).toBe("failed");
    // closed until asked; the input is coloured JSON and the output the error
    const head = cards[1]!.querySelector("button")!;
    expect(head.getAttribute("aria-expanded")).toBe("false");
    await click(head);
    expect(head.getAttribute("aria-expanded")).toBe("true");
    expect(cards[1]!.textContent).toContain("tool input rejected: a very long reason");
    expect(cards[1]!.textContent).toContain('"node"');
    expect(cards[1]!.textContent).toContain("node_journal");
    expect(cards[1]!.textContent).toContain("1.4s");
    // thinking header with the working time, over what was said between rounds; the answer stays the bubble
    const thinking = document.querySelector("[data-slot=thinking]")!;
    expect(thinking.textContent).toContain("Thought for");
    expect(thinking.textContent).toContain("6s");
    expect(thinking.querySelector("button")?.getAttribute("aria-expanded")).toBe("false");
    await click(thinking.querySelector("button"));
    expect(thinking.textContent).toContain("Checking.");
    expect(document.querySelector("[data-slot=bubble]")?.textContent).toBe("answer");
    expect(document.querySelector("[data-slot=bubble]")?.className).not.toMatch(/bg-/);
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

describe("the assistant's interface cards", () => {
  const call = (id: string, tool: string, args: unknown, over: Record<string, unknown> = {}) => ({ id, tool, argsJson: JSON.stringify(args), done: true, ok: true, count: 0, error: "", ...over });
  const thread = (tools: unknown[], extra: Record<string, unknown> = {}) => fakeChat([message(ChatRole.USER, ts(0), "q"), message(ChatRole.ASSISTANT, ts(0, 1), "Here you go.", { tools, ...extra })]);
  const card = () => document.querySelector("[data-slot=action]");
  const field = () => document.querySelector<HTMLInputElement>('[data-slot=secure-field] input, [data-slot=secure-field] textarea');
  const typeInto = (el: HTMLInputElement | HTMLTextAreaElement | null, value: string) =>
    act(async () => {
      Object.getOwnPropertyDescriptor(el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el!.dispatchEvent(new Event("input", { bubbles: true }));
    });

  it("draws ui_open as a card with the page's name and a button that navigates and is then marked used", async () => {
    await mountThread(thread([call("c1", "ui_open", { page: "node_ways", id: "nod_de1" })]));
    expect(card()?.textContent).toContain("Nodes");
    expect(card()?.textContent).toContain("nod_de1");
    expect(document.querySelector("[data-slot=tools]")).toBeNull();
    expect(navigate).not.toHaveBeenCalled();
    await click(button("Open"));
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_de1" }, search: { tab: "ways" } });
    expect(card()?.getAttribute("data-state")).toBe("used");
    expect(card()?.textContent).toContain("Opened");
    expect(button("Open")).toBeUndefined();
  });

  it("keeps a dismissed card quiet, and draws nothing for a page off the whitelist, a refused call or ui_prefill", async () => {
    await mountThread(thread([call("c3", "ui_open", { page: "users" })]));
    await click(button("Dismiss"));
    expect(card()?.getAttribute("data-state")).toBe("dismissed");
    expect(navigate).not.toHaveBeenCalled();
    act(() => root?.unmount());
    host?.remove();
    await mountThread(thread([call("c4", "ui_open", { page: "https://evil.example" }), call("c5", "ui_prefill", { form: "add_node", fields: { host: "h" } }), call("c6", "ui_open", { page: "nodes" }, { ok: false, error: "unknown page" })]));
    expect(card()).toBeNull();
    expect(document.querySelector("[data-slot=tools]")).not.toBeNull();
  });

  it("opens a page of the turn being written by itself only when 'open without asking' is on", async () => {
    localStorage.setItem("assistant-ui-auto", "1");
    chat.mockImplementation(async function* () {
      yield ev("started", { conversationId: "cnv_n", userMessageId: "m1", assistantMessageId: "m2" });
      yield ev("toolFinished", call("n1", "ui_open", { page: "health" }));
      yield ev("done", { end: "end", title: "" });
    });
    await mount();
    await send("where is health?");
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(navigate).toHaveBeenCalledWith({ to: "/health" });
    expect(card()?.getAttribute("data-state")).toBe("used");
    navigate.mockReset();
    localStorage.removeItem("assistant-ui-auto");
    await send("again");
    expect(navigate).not.toHaveBeenCalled();
  });

  const install = (extra: Record<string, unknown> = {}) => ({ ...plan, planId: "pln_7", tool: "node_install", inboxOnly: true, needsApproval: true, ...extra });
  const fact = (key: string, value: string) => ({ key, value, untrusted: false, code: "", params: {}, untrustedParams: [] });
  const approval = (facts: unknown[]) => ({ id: "pln_7", tokenId: "tok_1", tokenName: "assistant", tokenProfile: 1, tool: "node_install", facts, danger: [], reason: "", state: 1, createdUnix: 0n, expiresUnix: BigInt(Math.floor(Date.now() / 1000) + 500), decidedByName: "", decidedUnix: 0n, appliedUnix: 0n, result: "", error: "", outcomeCode: "", outcomeParams: {} });

  it("approves a node install right in the plan's card: the host key is compared and the password goes only to the approval call", async () => {
    approvalList = [approval([fact("host", "203.0.113.9"), fact("host_key", "SHA256:srvkey123")])];
    approve.mockResolvedValue({});
    await mountThread(thread([], { plans: [install()] }));
    await settle();
    expect(document.querySelector("[data-slot=inline-approval]")).not.toBeNull();
    expect(text()).toContain("SHA256:srvkey123");
    expect(button("Open in Waiting for you")).toBeDefined();
    // not yet: the key is not confirmed and no password is typed
    const approveButton = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label")?.startsWith("Approve"))!;
    expect(approveButton().disabled).toBe(true);
    await typeInto(field(), "s3cr3t-pass");
    await click(document.querySelector('input[type="checkbox"]'));
    expect(approveButton().disabled).toBe(false);
    await click(approveButton());
    await settle();
    expect(approve).toHaveBeenCalledWith({ id: "pln_7", sshPassword: "s3cr3t-pass", confirmedFingerprint: "SHA256:srvkey123" });
    // the typed value is gone from the form and was never part of a chat request
    expect(field()?.value ?? "").toBe("");
    expect(chat).not.toHaveBeenCalled();
    expect(JSON.stringify(chat.mock.calls)).not.toContain("s3cr3t-pass");
    expect(document.body.textContent).not.toContain("s3cr3t-pass");
  });

  it("approves a node install with the panel's own key, without any password", async () => {
    accessList = [{ nodeId: "nod_1", panelKeyFingerprint: "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl" }];
    approvalList = [approval([fact("host", "203.0.113.9"), fact("host_key", "SHA256:srvkey123"), fact("panel_ssh_key", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7cYk4m1x mistgate-panel")])];
    approve.mockResolvedValue({});
    await mountThread(thread([], { plans: [install()] }));
    await settle();
    await settle();
    expect(field()).toBeNull();
    await click(document.querySelector('input[type="checkbox"]'));
    await click([...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.getAttribute("aria-label")?.startsWith("Approve")));
    await settle();
    expect(approve).toHaveBeenCalledWith({ id: "pln_7", usePanelKey: true, confirmedFingerprint: "SHA256:srvkey123" });
  });

  it("goes to that plan's place in Waiting for you from the secondary button", async () => {
    approvalList = [approval([fact("host_key", "SHA256:srvkey123")])];
    await mountThread(thread([], { plans: [install()] }));
    await settle();
    await click(button("Open in Waiting for you"));
    expect(navigate).toHaveBeenCalledWith({ to: "/ai", search: { tab: "approvals", plan: "pln_7" } });
  });

  it("shows a secure field for a provider key; the value goes to the panel's own call, the card forgets it, and only a hint is kept", async () => {
    setProviderKey.mockResolvedValue({ status: { keyHint: "Q7wA" } });
    await mountThread(thread([call("s1", "ui_secret_form", { kind: "provider_key", provider: "anthropic" })]));
    expect(document.querySelector("[data-slot=secret]")?.textContent).toContain("Anthropic");
    expect(document.querySelector("[data-slot=secret]")?.textContent).toContain("never sees");
    expect(field()?.type).toBe("password");
    expect(field()?.getAttribute("autocomplete")).toBe("new-password");
    expect(button("Save")?.disabled).toBe(true);
    await typeInto(field(), "sk-ant-api03-REALVALUE0123456789");
    await click(button("Show"));
    expect(field()?.type).toBe("text");
    await click(button("Save"));
    await settle();
    expect(setProviderKey).toHaveBeenCalledWith({ provider: 1, apiKey: "sk-ant-api03-REALVALUE0123456789" });
    expect(field()).toBeNull();
    expect(document.querySelector("[data-slot=secret]")?.getAttribute("data-state")).toBe("done");
    expect(document.querySelector("[data-slot=secret]")?.textContent).toContain("●●●● Q7wA");
    expect(document.body.textContent).not.toContain("REALVALUE");
    expect(JSON.stringify(localStorage)).not.toContain("REALVALUE");
    expect(chat).not.toHaveBeenCalled();
  });

  it("gives the secure form to the owner only", async () => {
    owner = false;
    await mountThread(thread([call("s2", "ui_secret_form", { kind: "telegram_token" })]));
    expect(document.querySelector("[data-slot=secret]")?.textContent).toContain("Only the owner can enter this.");
    expect(field()).toBeNull();
  });

  it("warns, as the message is typed, that it looks like a secret", async () => {
    await mount();
    const area = document.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "my key is sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(text()).toContain("This looks like a secret. Do not send it in the chat");
  });
});
