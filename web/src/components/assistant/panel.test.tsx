import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { setEffectsEnabled } from "@/lib/effects";
import { ChatDock, useChat } from "./chat";
import { ChatPanel } from "./panel";

// The window of the redesign: the list of conversations (open, delete one after asking, delete all after asking), the button and the states it
// shows for (a provider missing: the window says so; the assistant off: no button), the keyboard of the picker and the slider.

const NOW = Math.floor(Date.now() / 1000);
let conversations = [
  { id: "cnv_a", title: "Node problems today", createdUnix: NOW - 900, updatedUnix: NOW - 300, messages: 4, taskId: "", unread: false, followUpUnix: 0 },
  { id: "cnv_b", title: "Why bob cannot connect", createdUnix: NOW - 900, updatedUnix: NOW - 200, messages: 6, taskId: "", unread: true, followUpUnix: 0 },
];
let access: Record<string, unknown> = {};
const deleteConversation = vi.fn(async (r: { id: string }) => {
  conversations = conversations.filter((c) => c.id !== r.id);
  return {};
});
const deleteAllConversations = vi.fn(async () => {
  conversations = [];
  return {};
});
const getConversation = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  approvals: { listApprovals: async () => ({ approvals: [], awaiting: 0 }) },
  assistant: {
    getAssistantAccess: async () => access,
    listConversations: async (r: { query: string }) => ({ conversations: conversations.filter((c) => c.title.toLowerCase().includes(r.query.toLowerCase())) }),
    recentQuestions: async () => ({ questions: [] }),
    deleteConversation: (r: { id: string }) => deleteConversation(r),
    deleteAllConversations: () => deleteAllConversations(),
    getConversation: (r: unknown) => getConversation(r),
    stopTurn: async () => ({}),
  },
}));
vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));
let owner = true;
vi.mock("@/lib/updates", () => ({ useIsOwner: () => owner }));
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
  Link: ({ children, to, className, "aria-label": label, onClick }: { children?: ReactNode; to: string; className?: string; "aria-label"?: string; onClick?: () => void }) => (
    <a href={to} className={className} aria-label={label} onClick={onClick}>
      {children}
    </a>
  ),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname: "/nodes/nod_1" } }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [deleteConversation, deleteAllConversations, getConversation]) m.mockClear();
  setLang("en");
  setEffectsEnabled(false);
  localStorage.clear();
  owner = true;
  access = {};
  conversations = [
    { id: "cnv_a", title: "Node problems today", createdUnix: NOW - 900, updatedUnix: NOW - 300, messages: 4, taskId: "", unread: false, followUpUnix: 0 },
    { id: "cnv_b", title: "Why bob cannot connect", createdUnix: NOW - 900, updatedUnix: NOW - 200, messages: 6, taskId: "", unread: true, followUpUnix: 0 },
  ];
});

const on = { available: true, reason: "", provider: 1, model: "claude-sonnet-5-5", readOnly: false, effort: 2, unread: 0, nextFollowUpUnix: 0, timezoneOffsetMinutes: 0, choices: [{ provider: 1, model: "claude-sonnet-5-5", inputUsdPerMtok: 3, outputUsdPerMtok: 15, budgetReached: false }] };
const text = () => document.body.textContent ?? "";
const $ = (sel: string) => document.querySelector<HTMLElement>(sel);
const all = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const settle = async () => {
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const press = (el: Element | null | undefined, key: string) => act(async () => void el?.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));

function unmount() {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
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

function Window({ id = "" }: { id?: string }) {
  const chat = useChat();
  // opening a conversation only notes the call: the list gives way to the thread, the rest is the chat's own and is tested with it
  return <ChatPanel chat={{ ...chat, open: async (c: string) => void getConversation({ id: c }) } as never} readOnly={false} providerLabel="claude-sonnet-5-5" provider={1} onClose={() => {}} initialContext={null} pickerOpen={id === "picker"} />;
}

describe("the conversations in the window", () => {
  it("opens as a list from the header: its own title and count, the back arrow, the open one framed", async () => {
    access = on;
    await mount(<Window />);
    expect($(".as-ht b")?.textContent).toBe("Assistant");
    await click($("button[aria-label=Conversations]"));
    await settle();
    expect($(".as-ht b")?.textContent).toBe("Conversations");
    expect($(".as-ht .as-sb")?.textContent).toBe("2 conversations");
    expect(all(".as-hr")).toHaveLength(2);
    expect($(".as-ud")).not.toBeNull();
    // the header holds only the way back, a new conversation and close here
    expect(all(".as-h button, .as-h a").map((b) => b.getAttribute("aria-label"))).toEqual(["Back to the conversation", "New conversation", "Close"]);
    await click($(".as-bk"));
    expect($(".as-ht b")?.textContent).toBe("Assistant");
  });

  it("a row opens its conversation", async () => {
    access = on;
    getConversation.mockResolvedValue({});
    await mount(<Window />);
    await click($("button[aria-label=Conversations]"));
    await settle();
    await click(all(".as-ho")[1]);
    expect(getConversation).toHaveBeenCalledWith({ id: "cnv_b" });
    // and the list gives way to the conversation
    expect($(".as-hs")).toBeNull();
  });

  it("the search narrows the list on the panel's side; with nothing found it says so", async () => {
    access = on;
    await mount(<Window />);
    await click($("button[aria-label=Conversations]"));
    await settle();
    const input = $(".as-hq input") as HTMLInputElement;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "bob");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
    expect(all(".as-hr")).toHaveLength(1);
    expect($(".as-ht .as-sb")?.textContent).toBe("1 conversation");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "zzz");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
    expect($(".as-e b")?.textContent).toBe("No such conversation");
    expect($(".as-hf")).toBeNull();
  });

  it("deleting one asks first; Cancel changes nothing, Delete removes it and the list follows", async () => {
    access = on;
    await mount(<Window />);
    await click($("button[aria-label=Conversations]"));
    await settle();
    await click(all(".as-hx")[0]);
    expect($(".as-ask")?.textContent).toContain("Delete the conversation?");
    await click([...document.querySelectorAll(".as-ask button")].find((b) => b.textContent === "Cancel"));
    expect(deleteConversation).not.toHaveBeenCalled();
    expect(all(".as-hr")).toHaveLength(2);
    await click(all(".as-hx")[0]);
    await click([...document.querySelectorAll(".as-ask button")].find((b) => b.textContent === "Delete"));
    await settle();
    expect(deleteConversation).toHaveBeenCalledTimes(1);
    expect(all(".as-hr")).toHaveLength(1);
  });

  it("Delete all asks first, then wipes the person's conversations and shows the empty list", async () => {
    access = on;
    await mount(<Window />);
    await click($("button[aria-label=Conversations]"));
    await settle();
    expect($(".as-hf")?.textContent).toContain("Encrypted, visible only to you");
    await click([...document.querySelectorAll(".as-hf button")].find((b) => b.textContent === "Delete all"));
    expect($(".as-hf")?.textContent).toContain("Delete all your conversations? This can’t be undone");
    expect(deleteAllConversations).not.toHaveBeenCalled();
    await click([...document.querySelectorAll(".as-hf button")].find((b) => b.textContent === "Delete all"));
    await settle();
    expect(deleteAllConversations).toHaveBeenCalledTimes(1);
    expect($(".as-e b")?.textContent).toBe("No conversations yet");
    expect($(".as-ht .as-sb")?.textContent).toBe("empty");
  });
});

describe("the button and the window it opens", () => {
  it("is there while the assistant is open to the admin; with no provider it is there too and the window says so", async () => {
    access = { ...on, available: false, reason: "no_provider", choices: [] };
    await mount(<ChatDock />);
    const fab = $("button.cat-fab")!;
    expect(fab.getAttribute("aria-expanded")).toBe("false");
    await click(fab);
    await settle();
    expect($("[data-slot=off]")?.textContent).toContain("The assistant isn’t connected");
    expect($("[data-slot=off] a")?.getAttribute("href")).toBe("/ai");
    // the button shrinks away, cannot be reached, and says it is open
    expect($("button.cat-fab")?.getAttribute("aria-expanded")).toBe("true");
    expect($("button.cat-fab")?.hasAttribute("inert")).toBe(true);
    expect($("button.cat-fab")?.className).toContain("opacity-0");
  });

  it("is not there when the assistant is off, not allowed or not in the edition", async () => {
    for (const reason of ["assistant_off", "not_allowed", "edition", "budget_reached"]) {
      access = { ...on, available: false, reason, choices: [] };
      await mount(<ChatDock />);
      expect($("button.cat-fab"), reason).toBeNull();
      unmount();
    }
  });

  it("Esc closes the window and the focus goes back to the button; Ctrl+J opens it and shuts it", async () => {
    access = on;
    await mount(<ChatDock />);
    await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", ctrlKey: true, bubbles: true })));
    await settle();
    expect($(".as-dock")).not.toBeNull();
    // the cursor is in the box, so the focus is inside the window
    $("textarea")?.focus();
    await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await act(async () => void (await new Promise((r) => setTimeout(r, 10))));
    expect($(".as-dock")).toBeNull();
    expect(document.activeElement).toBe($("button.cat-fab"));
    await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", metaKey: true, bubbles: true })));
    await settle();
    expect($(".as-dock")).not.toBeNull();
    await act(async () => void window.dispatchEvent(new KeyboardEvent("keydown", { key: "j", metaKey: true, bubbles: true })));
    expect($(".as-dock")).toBeNull();
  });

  it("a conversation asked for from another page opens in the window", async () => {
    access = on;
    getConversation.mockResolvedValue({ messages: [], running: false, followUps: [] });
    await mount(<ChatDock />);
    await act(async () => void window.dispatchEvent(new CustomEvent("mistgate:assistant-open", { detail: { id: "cnv_a" } })));
    await settle();
    expect($(".as-dock")).not.toBeNull();
    expect(getConversation).toHaveBeenCalledWith({ id: "cnv_a" });
  });
});

describe("the picker and the slider by keyboard", () => {
  it("Esc shuts the picker and not the window; the arrows move the depth; a press outside shuts it too", async () => {
    access = on;
    await mount(<Window id="picker" />);
    expect($(".as-mp")).not.toBeNull();
    const slider = $(".as-sl")!;
    expect($("[role=radio][aria-checked=true]")?.getAttribute("aria-label")).toBe("Normal");
    await press(slider.querySelector("[role=radio][aria-checked=true]"), "ArrowRight");
    expect($("[role=radio][aria-checked=true]")?.getAttribute("aria-label")).toBe("Thorough");
    await press($("[role=radio][aria-checked=true]"), "ArrowLeft");
    await press($("[role=radio][aria-checked=true]"), "ArrowLeft");
    expect($("[role=radio][aria-checked=true]")?.getAttribute("aria-label")).toBe("Quick");
    expect(localStorage.getItem("assistant-effort")).toBe("1");
    // the arrows of the column of providers
    await press($("[role=tab][aria-selected=true]"), "ArrowDown");
    expect($("[role=tab][aria-selected=true]")?.textContent).toBe("DeepSeek");
    await press($(".as-mw"), "Escape");
    expect($(".as-mp")).toBeNull();
    expect($("section.as")).not.toBeNull();
    await click($(".as-md"));
    expect($(".as-mp")).not.toBeNull();
    await act(async () => void document.body.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true })));
    expect($(".as-mp")).toBeNull();
  });

  it("the switch of opening pages without asking is this browser's note", async () => {
    access = on;
    await mount(<Window id="picker" />);
    expect($("[role=switch]")?.getAttribute("aria-checked")).toBe("false");
    await click($("[role=switch]"));
    expect($("[role=switch]")?.getAttribute("aria-checked")).toBe("true");
    expect(text()).toContain("Open pages without asking");
    expect(Object.values(localStorage).length).toBeGreaterThan(0);
  });
});

describe("the reveal animation", () => {
  it("brings the wave, the glints at the edge and the dotted halo only when it is on, and not with reduced motion", async () => {
    access = on;
    await mount(<Window />);
    expect($(".as-wv")).toBeNull();
    expect($(".as-eg")).toBeNull();
    expect($("section.as")?.hasAttribute("data-fx")).toBe(false);
    unmount();
    act(() => setEffectsEnabled(true));
    await mount(<Window />);
    expect($(".as-wv i")).not.toBeNull();
    expect(all(".as-eg")).toHaveLength(2);
    expect($("section.as")?.getAttribute("data-fx")).toBe("1");
    unmount();
    // the system asks for calm: the effects are off whatever the switch says
    const real = window.matchMedia;
    window.matchMedia = ((q: string) => ({ matches: q.includes("reduce"), media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, onchange: null, dispatchEvent: () => false })) as never;
    try {
      await mount(<Window />);
      expect($(".as-wv")).toBeNull();
      expect($("section.as")?.hasAttribute("data-fx")).toBe(false);
    } finally {
      window.matchMedia = real;
    }
  });
});
