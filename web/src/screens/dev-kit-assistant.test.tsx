import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpProvider } from "@/components/step-up";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang, type Lang } from "@/i18n";
import { frames, parse, seeded, Window } from "./dev-kit-assistant";

// The scenes of /dev-kit?only=pn-assistant draw the real window on made-up data: every state a scene names must draw, in every language, and leave
// nothing behind in the browser's storage.

// the scene's own calls are put on these objects by `seeded`; the pages behind it are not drawn here (the window is)
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, assistant: {}, approvals: {}, provisioning: {}, nodes: {}, telegram: {}, warp: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className, params: _p, search: _s, to, ...rest }: { children?: ReactNode; className?: string; params?: unknown; search?: unknown; to?: string }) => (
    <a className={className} href={to} {...rest}>
      {children}
    </a>
  ),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
  useRouterState: ({ select }: { select: (s: { location: { pathname: string } }) => unknown }) => select({ location: { pathname: "/" } }),
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
  setLang("en");
  localStorage.clear();
  document.body.innerHTML = "";
});

const say = (lang: string) => (en: string, ru: string) => (lang === "ru" ? ru : en);

async function draw(state: string, role = Role.OWNER, lang = "en") {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const s = say(lang);
  const qc = seeded(state, role, s);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <StepUpProvider>
            <Window state={state} say={s} />
          </StepUpProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function clear() {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
}

const body = () => document.body.textContent ?? "";
const $ = (sel: string) => document.querySelector<HTMLElement>(sel);
const all = (sel: string) => [...document.querySelectorAll<HTMLElement>(sel)];
const roleOf = (params = "") => (params.includes("role=helper") ? Role.HELPER : params.includes("role=readonly") ? Role.READONLY : Role.OWNER);
const scenes = [...new Map(frames.map((f) => [`${f.state}|${roleOf(f.params)}`, f])).values()];

describe("the scenes of the assistant's window", () => {
  it.each(scenes.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    await draw(f.state, roleOf(f.params));
    expect($("section.as[role=dialog]")).not.toBeNull();
    expect(body()).not.toContain("undefined");
    expect(body()).not.toMatch(/\b(as|ai|aip)\.[a-z]+\.?[a-zA-Z]/);
    // nothing of the scene stays in the browser: no key of this app was written
    expect(Object.keys(localStorage)).toEqual([]);
    const { view } = parse(f.state);
    if (view === "hist") expect($(".as-hs")).not.toBeNull();
    else expect($(".as-b")).not.toBeNull();
  });

  it("draws every state in every language, with no raw key", async () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as Lang[]) {
      for (const state of ["empty", "empty.md", "empty.md.seen-2", "think", "answer", "answer.tools", "done", "err", "off", "hist", "hist.ask", "hist.wipe", "hist.hq", "hist.none", "empty.fu", "empty.cards", "empty.install", "empty.warn"]) {
        setLang(lang);
        await draw(state, Role.OWNER, lang);
        expect(body(), `${lang} ${state}`).not.toMatch(/\b(as|ai|aip|approval|int)\.[a-zA-Z]+[.a-zA-Z]*/);
        expect(body()).not.toContain("undefined");
        await clear();
      }
    }
  }, 180_000);
});

describe("what a scene shows", () => {
  it("the start: the cat, what to ask about, five examples, the page as a chip, the footer line", async () => {
    await draw("empty");
    expect($(".as-e b")?.textContent).toBe("Ask about nodes and people");
    expect(all(".as-sg button")).toHaveLength(5);
    expect($(".as-cx")?.textContent).toContain("Page: Overview");
    expect($(".as-fm")?.textContent).toBe("The assistant only prepares changes, you apply them");
    expect($(".as-lg .cat")).not.toBeNull();
    // the model button names the provider and its model, the header says the same
    expect($(".as-md")?.textContent).toContain("Anthropic · claude-sonnet-5-5");
    expect($(".as-ht .as-mn")?.textContent).toBe("Anthropic · claude-sonnet-5-5");
    expect($(".as-go")?.hasAttribute("disabled")).toBe(true);
  });

  it("it thinks: a call is done, the seconds, the dots", async () => {
    await draw("think");
    expect($("[data-slot=thinking]")?.textContent).toContain("Thinking for");
    expect($("[data-slot=tools]")?.textContent).toContain("Using 1 tool…");
    expect($("[data-slot=typing]")?.textContent).toContain("Assistant is thinking…");
    expect($(".as-th")).not.toBeNull();
    // the send button is the stop button while it runs
    expect($(".as-go.stop")).not.toBeNull();
  });

  it("the answer: the nodes named in it are chips, the plan waits for the owner with the time it keeps", async () => {
    await draw("answer");
    expect(all("a[data-entity=node]").map((a) => a.textContent)).toEqual(["fi1", "nl1", "de1", "msk1", "fi1", "msk1"].slice(0, all("a[data-entity=node]").length));
    expect(all("a[data-entity=node]").length).toBeGreaterThanOrEqual(4);
    const card = $("[data-slot=plan]")!;
    expect(card.textContent).toContain("Proposed change");
    expect(card.textContent).toContain("expires in");
    expect(card.textContent).toContain("Needs the owner's approval");
    expect(all("[data-slot=plan] button").map((b) => b.textContent)).toEqual(["Dismiss", "Approve and apply"]);
    expect($(".as-day")?.textContent).toBe("Today");
  });

  it("applied: the card quiets down and says how it ended", async () => {
    await draw("done");
    expect($(".as-pl.ok")).not.toBeNull();
    expect($(".as-ps")?.textContent).toBe("Applied: fi1 goes through msk1");
    expect(all("[data-slot=plan] button")).toHaveLength(0);
  });

  it("a plan being confirmed shows its wait", async () => {
    await draw("answer.busy");
    expect(all("[data-slot=plan] button").find((b) => b.textContent?.includes("Waiting for confirmation"))).toBeDefined();
    expect($("[data-slot=plan] .as-sp")).not.toBeNull();
  });

  it("the trouble: the budget, what to do, the way to the settings for the owner and not for a helper", async () => {
    await draw("err");
    expect($("[data-slot=error]")?.textContent).toContain("This month's budget for the provider is spent.");
    expect($("[data-slot=error]")?.textContent).toContain("Raise “Monthly budget, $” or wait for next month");
    expect($("[data-slot=error] a")?.getAttribute("href")).toBe("/ai");
    await clear();
    await draw("err", Role.HELPER);
    expect($("[data-slot=error]")).not.toBeNull();
    expect($("[data-slot=error] a")).toBeNull();
  });

  it("not connected: the box is closed; the owner has the way to the settings, a helper is told whom to ask", async () => {
    await draw("off");
    expect($("[data-slot=off]")?.textContent).toContain("The assistant isn’t connected");
    expect($("[data-slot=off] a")?.getAttribute("href")).toBe("/ai");
    expect($("textarea")?.getAttribute("aria-disabled")).toBe("true");
    expect($("textarea")?.getAttribute("placeholder")).toBe("Connect a provider first");
    expect($(".as-md")?.textContent).toContain("No provider selected");
    expect($(".as-md")?.hasAttribute("disabled")).toBe(true);
    expect($(".as-ht .as-mn")?.textContent).toBe("no provider selected");
    await clear();
    await draw("off", Role.HELPER);
    expect($("[data-slot=off] a")).toBeNull();
    expect($("[data-slot=off]")?.textContent).toContain("Ask the owner to connect a provider");
  });

  it("the conversations: three groups, the open one framed, an unread dot, a scheduled mark, one line at the foot", async () => {
    await draw("hist");
    expect($(".as-ht b")?.textContent).toBe("Conversations");
    expect($(".as-ht .as-sb")?.textContent).toBe("5 conversations");
    expect(all(".as-hd").map((h) => h.textContent)).toEqual(["Today", "Yesterday", "Earlier"]);
    expect(all(".as-hr")).toHaveLength(5);
    expect($(".as-hr.on")?.textContent).toContain("Node problems today");
    expect($(".as-hr.on")?.textContent).toContain("now");
    expect($(".as-ud")).not.toBeNull();
    expect(body()).toContain("Scheduled");
    expect($(".as-hf")?.textContent).toContain("Encrypted, visible only to you");
    expect($(".as-hf")?.textContent).toContain("Delete all");
    // the way back, the search and the trash can are named
    expect($(".as-bk")?.getAttribute("aria-label")).toBe("Back to the conversation");
    expect($(".as-hq input")?.getAttribute("aria-label")).toBe("Find a conversation");
    expect(all(".as-hx")[0]?.getAttribute("aria-label")).toBe("Delete conversation");
  });

  it("a row asks before it deletes; Delete all asks too; an empty list and a search with no match say so", async () => {
    await draw("hist.ask");
    const ask = $(".as-hr.as-ask")!;
    expect(ask.textContent).toContain("Delete the conversation?");
    expect([...ask.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Cancel", "Delete"]);
    await clear();
    await draw("hist.wipe");
    expect($(".as-hf.as-ask")?.textContent).toContain("Delete all your conversations?");
    await clear();
    await draw("hist.hq");
    expect($(".as-e b")?.textContent).toBe("No such conversation");
    expect($(".as-ht .as-sb")?.textContent).toBe("empty");
    await clear();
    await draw("hist.none");
    expect($(".as-e b")?.textContent).toBe("No conversations yet");
    expect($(".as-e p")?.textContent).toBe("Conversations are encrypted and visible only to you");
    expect($(".as-hf")).toBeNull();
  });

  it("the choice of the agent: a column of providers (the one in use marked), its model, the depth, the opening of pages", async () => {
    await draw("empty.md");
    expect($(".as-md")?.getAttribute("aria-expanded")).toBe("true");
    const tabs = all("[role=tab]");
    expect(tabs.map((x) => x.textContent)).toEqual(["Anthropic", "Z.ai", "Ollama Cloud", "DeepSeek"]);
    expect($("[role=tab][aria-selected=true]")?.textContent).toBe("Anthropic");
    expect($("[role=tab].as-cur")?.textContent).toBe("Anthropic");
    expect($("[role=option]")?.getAttribute("aria-selected")).toBe("true");
    expect($(".as-m2n")?.textContent).toContain("Processed in the United States");
    expect(all("[role=radio]").map((x) => x.getAttribute("aria-label"))).toEqual(["Quick", "Normal", "Thorough"]);
    expect($(".as-sl")?.className).toContain("d1");
    expect($(".as-dpl b")?.textContent).toBe("Normal");
    expect($("[role=switch]")?.getAttribute("aria-checked")).toBe("false");
    await clear();
    await draw("empty.md.d0");
    expect($(".as-sl")?.className).toContain("d0");
    expect($(".as-dph")?.textContent).toBe("answers at once");
    await clear();
    await draw("empty.md.d2");
    expect($(".as-sl")?.className).toContain("d2");
    expect($(".as-dpl b")?.textContent).toBe("Thorough");
  });

  it("a provider seen: one without a key says so, one whose budget is spent cannot be picked", async () => {
    await draw("empty.md.seen-2");
    expect($("[role=tab][aria-selected=true]")?.textContent).toBe("DeepSeek");
    expect($("[role=listbox]")?.textContent).toContain("No key added");
    expect($("[role=listbox] a")?.getAttribute("href")).toBe("/ai");
    await clear();
    await draw("empty.md.bud.seen-4");
    expect($("[role=option]")?.hasAttribute("disabled")).toBe(true);
    expect($("[role=option]")?.textContent).toContain("budget spent");
  });

  it("the reveal animation is the only thing that brings the wave, the glints and the halo", async () => {
    await draw("empty");
    expect($(".as-wv")).toBeNull();
    expect($(".as-eg")).toBeNull();
    expect($("section.as")?.hasAttribute("data-fx")).toBe(false);
  });

  it("the thread's own states: six calls with one failed, a long answer, follow-ups, cards, an inline approval", async () => {
    await draw("answer.tools");
    expect($("[data-slot=tools]")?.textContent).toContain("Used 6 tools · 1 failed");
    expect(all("[data-slot=tool]")).toHaveLength(3);
    expect($("[data-slot=tools]")?.textContent).toContain("+3 earlier tool calls");
    await clear();
    await draw("answer.long");
    expect($(".as-tb table")).not.toBeNull();
    expect($(".as-mk pre")).not.toBeNull();
    expect($(".as-mk blockquote")).not.toBeNull();
    expect($(".as-mk ol")).not.toBeNull();
    await clear();
    await draw("empty.fu");
    expect($("[data-role=follow-up]")).not.toBeNull();
    expect($("[data-role=follow-up-note]")?.textContent).toContain("was dropped");
    expect($("[data-role=follow-up-pending]")?.textContent).toContain("Cancel");
    await clear();
    await draw("empty.cards");
    expect($("[data-slot=action]")?.textContent).toContain("Nodes");
    expect($("[data-slot=secret]")?.textContent).toContain("DeepSeek");
    expect($("[data-slot=secret] input")?.getAttribute("type")).toBe("password");
    expect(body()).toContain("Remembered: nl1 is the test node");
    await clear();
    await draw("empty.install");
    expect($("[data-slot=inline-approval]")).not.toBeNull();
    expect(body()).toContain("SHA256:Qm9y3xk2");
  });

  it("a secret typed into the box is warned about; a read-only role is told; the page can be let go", async () => {
    await draw("empty.warn");
    expect($(".as-sw")?.textContent).toContain("This looks like a secret");
    await clear();
    await draw("empty.ro");
    expect($(".as-fm")?.textContent).toBe("Your role lets the assistant read, not change.");
    await clear();
    await draw("empty.nopage");
    expect($(".as-cx")).toBeNull();
    expect($(".as-ad")).not.toBeNull();
  });

  it("the Russian scenes speak Russian, with «ты»", async () => {
    setLang("ru");
    await draw("empty", Role.OWNER, "ru");
    expect($(".as-e b")?.textContent).toBe("Спроси про ноды и людей");
    expect($(".as-e p")?.textContent).toContain("применяешь их ты");
    expect($("textarea")?.getAttribute("placeholder")).toBe("Спроси или попроси что-то изменить");
    await clear();
    await draw("answer", Role.OWNER, "ru");
    expect($("[data-slot=plan]")?.textContent).toContain("Включить ретрансляцию fi1 через msk1");
    expect($("[data-slot=plan]")?.textContent).toContain("истекает через");
  });
});
