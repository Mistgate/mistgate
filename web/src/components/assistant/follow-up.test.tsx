import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, useEffect, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { ChatRole } from "@/gen/mistgate/admin/v1/assistant_pb";
import { setLang } from "@/i18n";
import { followUpPoll } from "@/lib/assistant";
import { stampAtOffsetOf } from "@/lib/format";
import { ChatButton, ChatPanel, useChat } from "./chat";

// The assistant's follow-ups in the chat (assistant.proto, FOLLOW-UPS): the quiet line of one that waits, with Cancel, in the
// panel's time zone; the "On a timer" label of the turn it started; the note of one that did not run; the unread dot.

const chat = vi.fn();
const getConversation = vi.fn();
const cancelFollowUp = vi.fn();
let conversations: unknown[] = [];
// the panel's time zone is UTC+3; the tests check the times are the panel's, not the browser's
const tz = 180;
let unread = 0;
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  approvals: { listApprovals: async () => ({ approvals: [], awaiting: 0 }), approve: async () => ({}), reject: async () => ({}) },
  assistant: {
    chat: (...a: unknown[]) => chat(...a),
    getConversation: (...a: unknown[]) => getConversation(...a),
    cancelFollowUp: (...a: unknown[]) => cancelFollowUp(...a),
    stopTurn: async () => ({}),
    listConversations: async () => ({ conversations }),
    getAssistantAccess: async () => ({ available: true, reason: "", provider: 1, model: "claude-opus-5-5", readOnly: false, effort: 2, choices: [], unread, nextFollowUpUnix: 0n, timezoneOffsetMinutes: tz }),
  },
}));
vi.mock("@/components/step-up", () => ({ useStepUp: () => async <T,>(call: () => Promise<T>) => call(), isStepUpCancelled: () => false }));
vi.mock("@/lib/updates", () => ({ useIsOwner: () => true }));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to }: { children?: ReactNode; to: string }) => <a href={to}>{children}</a>,
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname: "/nodes" } }),
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
  for (const m of [chat, getConversation, cancelFollowUp]) m.mockReset();
  conversations = [];
  unread = 0;
  localStorage.clear();
});

const nowS = () => Math.floor(Date.now() / 1000);
const at = (unix: number) => stampAtOffsetOf(unix, tz, "en");
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label || b.getAttribute("aria-label") === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const msg = (role: ChatRole, body: string, extra: Record<string, unknown> = {}) => ({ id: `m-${Math.random()}`, role, text: body, createdUnix: BigInt(nowS() - 600), tools: [], plans: [], remembered: [], end: "", ...extra });

function Harness({ open }: { open?: string }) {
  const c = useChat();
  const load = c.open;
  useEffect(() => {
    if (open) void load(open);
  }, [open, load]);
  return <ChatPanel chat={c} readOnly={false} providerLabel="claude-opus-5-5" provider={1} onClose={() => {}} initialContext={null} />;
}

async function mount(node: ReactNode) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{node}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}

describe("the assistant's follow-ups", () => {
  it("shows a waiting follow-up as a quiet line in the panel's time zone, and cancels it", async () => {
    const due = nowS() + 9 * 60 - 5;
    getConversation.mockResolvedValue({
      conversation: { id: "cnv_1", title: "Debug de1", unread: false },
      messages: [msg(ChatRole.USER, "Capture de1 for 10 minutes"), msg(ChatRole.ASSISTANT, "Started; I will come back at the end.")],
      running: false,
      followUps: [{ id: "afu_1", note: "read node_debug_logs of req_1", dueUnix: BigInt(due), createdUnix: BigInt(nowS() - 60), planId: "", delayS: 0, running: false }],
    });
    cancelFollowUp.mockResolvedValue({});
    await mount(<Harness open="cnv_1" />);
    const line = document.querySelector("[data-role=follow-up-pending]");
    expect(line?.textContent).toContain(`I'll come back with the result at ${at(due)} (in 9 min)`);
    await click(button("Cancel"));
    await settle();
    expect(cancelFollowUp).toHaveBeenCalledWith({ conversationId: "cnv_1", id: "afu_1" });
    expect(document.querySelector("[data-role=follow-up-pending]")).toBeNull();
  });

  it("waits for a plan's apply, and has no Cancel once its time has come", async () => {
    getConversation.mockResolvedValue({
      conversation: { id: "cnv_1", title: "", unread: false },
      messages: [msg(ChatRole.USER, "Measure de1 to nl1"), msg(ChatRole.ASSISTANT, "Apply the plan and I will read it.")],
      running: false,
      followUps: [
        { id: "afu_plan", note: "read the speed", dueUnix: 0n, createdUnix: BigInt(nowS()), planId: "pln_1", delayS: 120, running: false },
        { id: "afu_due", note: "read the capture", dueUnix: BigInt(nowS() - 5), createdUnix: BigInt(nowS() - 600), planId: "", delayS: 0, running: false },
      ],
    });
    await mount(<Harness open="cnv_1" />);
    const lines = [...document.querySelectorAll("[data-role=follow-up-pending]")].map((l) => l.textContent ?? "");
    expect(lines[0]).toContain("I'll come back with the result 2 min after you apply the plan");
    expect(lines[0]).toContain("Cancel");
    expect(lines[1]).toContain("Coming back with the result…");
    expect(lines[1]).not.toContain("Cancel");
  });

  it("labels the turn a follow-up started, shows its line as the note, and words a follow-up that did not run", async () => {
    const set = nowS() - 15 * 60;
    const due = nowS() - 5 * 60;
    const mark = { id: "afu_1", setUnix: BigInt(set), dueUnix: BigInt(due), outcome: "", reason: "", note: "" };
    getConversation.mockResolvedValue({
      conversation: { id: "cnv_1", title: "Debug de1", unread: false },
      messages: [
        msg(ChatRole.USER, "Capture de1 for 10 minutes"),
        msg(ChatRole.ASSISTANT, "Started."),
        msg(ChatRole.USER, "read node_debug_logs of req_1 and give the verdict", { followUp: mark }),
        msg(ChatRole.ASSISTANT, "The capture shows TLS alerts from one network.", { followUp: mark }),
        msg(ChatRole.ASSISTANT, "", { followUp: { ...mark, id: "afu_2", outcome: "failed", reason: "budget_reached", note: "check the rollout" } }),
        msg(ChatRole.ASSISTANT, "", { followUp: { ...mark, id: "afu_3", outcome: "missed", note: "read the speed" } }),
      ],
      running: false,
      followUps: [],
    });
    await mount(<Harness open="cnv_1" />);
    const line = document.querySelector("[data-role=follow-up]");
    expect(line?.textContent).toContain("On a timer");
    expect(line?.textContent).toContain(`set at ${at(set)}`);
    expect(line?.textContent).toContain("read node_debug_logs of req_1");
    // the line is not a bubble of the person's
    expect([...document.querySelectorAll("[data-role=user]")].map((u) => u.textContent)).toEqual([expect.stringContaining("Capture de1")]);
    const answers = [...document.querySelectorAll("[data-role=assistant]")];
    expect(answers).toHaveLength(2);
    expect(answers[1]!.querySelector("[data-slot=timer]")?.textContent).toBe("On a timer");
    expect(answers[0]!.querySelector("[data-slot=timer]")).toBeNull();
    const notes = [...document.querySelectorAll("[data-role=follow-up-note]")].map((n) => n.textContent ?? "");
    expect(notes[0]).toContain(`The follow-up for ${at(due)} did not run: This month's budget for the provider is spent.`);
    expect(notes[0]).toContain("“check the rollout”");
    expect(notes[1]).toContain(`The follow-up for ${at(due)} was dropped: the panel was off for more than a day after it.`);
  });

  it("shows the line of a follow-up the model sets while the answer streams", async () => {
    chat.mockImplementation(async function* () {
      yield { event: { case: "started", value: { conversationId: "cnv_9", userMessageId: "m1", assistantMessageId: "m2" } } };
      yield { event: { case: "followUp", value: { id: "afu_9", note: "read the result", dueUnix: BigInt(nowS() + 30 * 60), createdUnix: BigInt(nowS()), planId: "", delayS: 0, running: false } } };
      yield { event: { case: "textDelta", value: "I will come back in half an hour." } };
      yield { event: { case: "done", value: { end: "end", title: "" } } };
    });
    await mount(<Harness />);
    const area = document.querySelector("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "Run the speed test and tell me");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(button("Send"));
    await settle();
    expect(document.querySelector("[data-role=follow-up-pending]")?.textContent).toContain("(in 30 min)");
  });

  it("marks an answer that came while the admin was away: a dot on the button and in the list", async () => {
    await mount(<ChatButton onClick={() => {}} running={false} unread={2} />);
    const fab = document.querySelector("button")!;
    expect(fab.getAttribute("aria-label")).toBe("Open the assistant: 2 new answers while you were away");
    expect(fab.querySelector("[data-slot=unread]")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();

    await mount(<ChatButton onClick={() => {}} running={false} unread={0} />);
    expect(document.querySelector("[data-slot=unread]")).toBeNull();
    act(() => root?.unmount());
    host?.remove();

    // the panel opens on the list when an answer waits and no conversation is open
    const due = nowS() + 3600;
    unread = 1;
    conversations = [
      { id: "cnv_1", title: "Debug de1", createdUnix: 1n, updatedUnix: BigInt(nowS()), messages: 4, taskId: "", unread: true, followUpUnix: 0n },
      { id: "cnv_2", title: "Rollout", createdUnix: 1n, updatedUnix: BigInt(nowS()), messages: 2, taskId: "", unread: false, followUpUnix: BigInt(due) },
    ];
    await mount(<Harness />);
    if (!document.querySelector("[data-slot=unread]")) await click(button("Conversations"));
    await settle();
    const dots = document.querySelectorAll("[data-slot=unread]");
    expect(dots).toHaveLength(1);
    expect(dots[0]!.getAttribute("aria-label")).toBe("New answer");
    expect(text()).toContain(`back at ${at(due)}`);
  });

  it("asks the button to look again soon after the next follow-up, and often while one is due", () => {
    const now = 1_800_000_000_000;
    expect(followUpPoll({ nextFollowUpUnix: 0 }, now)).toBe(false);
    expect(followUpPoll(undefined, now)).toBe(false);
    expect(followUpPoll({ nextFollowUpUnix: now / 1000 + 60 }, now)).toBe(65_000);
    expect(followUpPoll({ nextFollowUpUnix: now / 1000 + 6 * 3600 }, now)).toBe(10 * 60_000);
    expect(followUpPoll({ nextFollowUpUnix: now / 1000 - 10 }, now)).toBe(15_000);
  });
});
