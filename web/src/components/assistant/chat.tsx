import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { navKey, sections, type SectionId } from "@/components/nav";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "@/lib/api";
import { accessQuery, assistantErrorKey, conversationsQuery, looksSecret, providerName, type Context, type LiveMsg, type LiveTool, type Msg, type Note, type Plan, type Tool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { useIsOwner } from "@/lib/updates";
import { usePick } from "./agent-picker";
import { Composer } from "./composer";
import { Thread } from "./thread";

// The floating chat of the assistant (ADR 0014): a round button at the corner of every admin page (and Ctrl/⌘+J) while the
// assistant is open to this admin; it opens a panel beside the page (the whole screen on a phone). One turn streams at a
// time: the answer's Markdown, a line per tool call, plan cards the admin applies or dismisses, "Remembered" notes.

type ChatState = {
  conversationId: string;
  messages: Msg[];
  running: boolean;
  error: MessageKey | string | null;
};

const empty: ChatState = { conversationId: "", messages: [], running: false, error: null };

const newMessage = (role: ChatRole, text: string, context?: Context): Msg =>
  ({ id: `local-${Math.random().toString(36).slice(2)}`, role, text, createdUnix: Math.floor(Date.now() / 1000), context, tools: [], plans: [], remembered: [], end: "", ...(role === ChatRole.ASSISTANT ? { t0: Date.now() } : {}) }) as unknown as Msg;

/** The conversation, its turn stream and the actions on its plans and notes. */
export function useChat() {
  const t = useT();
  const qc = useQueryClient();
  const guard = useStepUp();
  const [s, setS] = useState<ChatState>(empty);
  const abort = useRef<AbortController | null>(null);
  const convRef = useRef("");

  const patchLast = useCallback((f: (m: Msg) => Msg) => {
    setS((cur) => {
      const i = cur.messages.length - 1;
      if (i < 0 || cur.messages[i]!.role !== ChatRole.ASSISTANT) return cur;
      const messages = cur.messages.slice();
      messages[i] = f(messages[i]!);
      return { ...cur, messages };
    });
  }, []);

  const patchPlan = useCallback((planId: string, f: (p: Plan) => Plan) => {
    setS((cur) => ({ ...cur, messages: cur.messages.map((m) => (m.plans.some((p) => p.planId === planId) ? { ...m, plans: m.plans.map((p) => (p.planId === planId ? f(p) : p)) } : m)) }));
  }, []);

  async function send(text: string, context: Context | null, turn?: { provider: number; effort: number }) {
    const body = text.trim();
    if (!body || s.running) return;
    const ctl = new AbortController();
    abort.current = ctl;
    setS((cur) => ({ ...cur, running: true, error: null, messages: [...cur.messages, newMessage(ChatRole.USER, body, context ?? undefined), newMessage(ChatRole.ASSISTANT, "")] }));
    try {
      for await (const res of assistant.chat({ conversationId: convRef.current, text: body, context: context ?? undefined, provider: turn?.provider ?? 0, effort: turn?.effort ?? 0 }, { signal: ctl.signal })) {
        const ev = res.event;
        switch (ev.case) {
          case "started":
            convRef.current = ev.value.conversationId;
            setS((cur) => ({ ...cur, conversationId: ev.value.conversationId }));
            break;
          case "textDelta":
            patchLast((m) => ({ ...m, text: m.text + ev.value }));
            break;
          case "toolStarted": {
            const tool = { ...(plain(ev.value) as Tool), startedAt: Date.now() } as LiveTool;
            // what the turn said so far is it thinking aloud; the answer is what comes after its last tool round
            patchLast((m) => ({ ...m, interimEnd: m.text.length, tools: [...m.tools.filter((x) => x.id !== tool.id), tool] }) as LiveMsg);
            break;
          }
          case "toolFinished": {
            const fin = plain(ev.value) as Tool;
            const at = Date.now();
            const done = (old?: LiveTool) => ({ ...fin, startedAt: old?.startedAt, ms: old?.startedAt ? at - old.startedAt : undefined }) as LiveTool;
            patchLast((m) => ({ ...m, tools: m.tools.some((x) => x.id === fin.id) ? m.tools.map((x) => (x.id === fin.id ? done(x as LiveTool) : x)) : [...m.tools, done()] }));
            break;
          }
          case "plan": {
            const plan = plain(ev.value) as Plan;
            patchLast((m) => ({ ...m, plans: [...m.plans.filter((p) => p.planId !== plan.planId), plan] }));
            break;
          }
          case "remembered": {
            const note = plain(ev.value) as Note;
            patchLast((m) => ({ ...m, remembered: [...m.remembered, note] }));
            void qc.invalidateQueries({ queryKey: ["assistant", "memory"] });
            break;
          }
          case "done":
            patchLast((m) => ({ ...m, end: ev.value.end }));
            break;
          case "error":
            patchLast((m) => ({ ...m, end: "error" }));
            setS((cur) => ({ ...cur, error: assistantErrorKey(ev.value.code) ?? ev.value.code }));
            break;
        }
      }
    } catch (e) {
      if (!ctl.signal.aborted) setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
    } finally {
      abort.current = null;
      patchLast((m) => ((m as LiveMsg).t0 && (m as LiveMsg).workedMs === undefined ? ({ ...m, workedMs: Date.now() - (m as LiveMsg).t0! } as LiveMsg) : m));
      setS((cur) => ({ ...cur, running: false }));
      void qc.invalidateQueries({ queryKey: ["assistant", "conversations"] });
      void qc.invalidateQueries({ queryKey: ["assistant", "access"] });
    }
  }

  async function stop() {
    const id = convRef.current;
    if (id) await assistant.stopTurn({ conversationId: id }).catch(() => {});
    // the stream ends by itself with "stopped"; the abort is the fallback when it does not within a few seconds
    const ctl = abort.current;
    setTimeout(() => ctl?.abort(), 4000);
  }

  const open = useCallback(
    async (id: string) => {
      abort.current?.abort();
      try {
        const r = plain(await assistant.getConversation({ id }));
        convRef.current = id;
        setS({ conversationId: id, messages: r.messages as Msg[], running: r.running, error: null });
      } catch (e) {
        setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
      }
    },
    [t],
  );

  function reset() {
    abort.current?.abort();
    convRef.current = "";
    setS(empty);
  }

  async function apply(planId: string) {
    try {
      const r = await guard(() => assistant.applyPlan({ conversationId: convRef.current, planId }));
      const plan = plain(r.plan!) as Plan;
      patchPlan(planId, () => plan);
    } catch (e) {
      if (isStepUpCancelled(e)) return;
      setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
    }
  }

  async function dismiss(planId: string) {
    try {
      const r = await assistant.dismissPlan({ conversationId: convRef.current, planId });
      const plan = plain(r.plan!) as Plan;
      patchPlan(planId, () => plan);
    } catch (e) {
      setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
    }
  }

  async function forget(noteId: string) {
    await assistant.deleteMemory({ id: noteId }).catch(() => {});
    setS((cur) => ({ ...cur, messages: cur.messages.map((m) => ({ ...m, remembered: m.remembered.filter((n) => n.id !== noteId) })) }));
    void qc.invalidateQueries({ queryKey: ["assistant", "memory"] });
  }

  return { ...s, send, stop, open, reset, apply, dismiss, forget };
}

export type ChatApi = ReturnType<typeof useChat>;

const sectionKinds: Record<string, string> = { nodes: "node", users: "user", profiles: "profile", groups: "group" };

/** The page the admin is on, as the chip that goes with the message. */
function usePageContext(): Context | null {
  const t = useT();
  const path = useRouterState({ select: (st) => st.location.pathname });
  const seg = path.split("/").filter(Boolean);
  const section = seg[0] ?? "overview";
  // only the admin's own sections go with a message (not the AI page itself, not the dev kit or a sign-in page)
  if (section === "ai" || !sections.some((x) => x.id === section)) return null;
  const id = seg[1] ?? "";
  const label = t(navKey(section as SectionId));
  return { kind: id ? (sectionKinds[section] ?? section) : section, id, label: id ? `${label} · ${id}` : label, route: path } as Context;
}

/** The round button and the panel. Mounted once in the shell; renders nothing while the assistant is not open to this admin. */
export function ChatDock() {
  const access = useQuery(accessQuery);
  const [open, setOpen] = useState(false);
  const chat = useChat();
  const openConversation = chat.open;
  const available = access.data?.available ?? false;

  // a conversation opened from AI -> Memory
  useEffect(() => {
    const onOpen = (e: Event) => {
      const id = (e as CustomEvent<{ id: string }>).detail?.id;
      if (!id) return;
      setOpen(true);
      void openConversation(id);
    };
    window.addEventListener("mistgate:assistant-open", onOpen);
    return () => window.removeEventListener("mistgate:assistant-open", onOpen);
  }, [openConversation]);

  useEffect(() => {
    if (!available) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "j") {
        e.preventDefault();
        setOpen((v) => !v);
      }
      if (e.key === "Escape") setOpen(false);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [available]);

  if (!available) return null;
  return (
    <>
      {!open && <ChatButton onClick={() => setOpen(true)} running={chat.running} />}
      {open && <ChatPanel chat={chat} readOnly={access.data?.readOnly ?? false} providerLabel={access.data?.model || ""} provider={access.data?.provider} onClose={() => setOpen(false)} />}
    </>
  );
}

export function ChatButton({ onClick, running }: { onClick: () => void; running: boolean }) {
  const t = useT();
  return (
    <div className="fixed end-4 bottom-[92px] z-20 md:end-6 md:bottom-6">
      <Tip label={`${t("ai.chat.open")} (Ctrl+J)`}>
        <button type="button" onClick={onClick} aria-label={t("ai.chat.open")} className="chat-fab relative grid size-[52px] place-items-center rounded-full text-on-accent">
          {running && <span aria-hidden className="chat-fab-ring pointer-events-none absolute inset-0 rounded-full border-2 border-accent" />}
          <Icon name="sparkle" size={22} fill="currentColor" strokeWidth={1.6} />
        </button>
      </Tip>
    </div>
  );
}

export function ChatPanel({ chat, readOnly, providerLabel, provider, onClose, initialContext, pickerOpen }: { chat: ChatApi; readOnly: boolean; providerLabel: string; provider?: number; onClose: () => void; initialContext?: Context | null; pickerOpen?: boolean }) {
  const t = useT();
  const owner = useIsOwner();
  const pageContext = usePageContext();
  const [attach, setAttach] = useState(true);
  const [text, setText] = useState("");
  const [askSecret, setAskSecret] = useState(false);
  const [history, setHistory] = useState(false);
  const list = useRef<HTMLDivElement>(null);
  const [scrolled, setScrolled] = useState(false);
  const context = initialContext !== undefined ? initialContext : pageContext;
  const pick = usePick({ provider, model: providerLabel });

  // the list follows the answer as it grows: new messages, text, tool lines, plan cards
  const tail = chat.messages[chat.messages.length - 1];
  const growth = chat.messages.length * 100_000 + (tail ? tail.text.length + tail.tools.length + tail.plans.length * 10 : 0);
  useEffect(() => {
    const el = list.current;
    if (el && growth >= 0) el.scrollTop = el.scrollHeight;
  }, [growth]);

  function submit(force = false) {
    const body = text.trim();
    if (!body || chat.running) return;
    if (!force && looksSecret(body)) return setAskSecret(true);
    setAskSecret(false);
    setText("");
    void chat.send(body, attach ? context : null, { provider: pick.provider, effort: pick.effort });
  }

  const provName = provider !== undefined ? t(providerName(provider)) : "";
  const headBtn = "grid size-8 place-items-center rounded-full text-muted transition-colors hover:bg-surface-2 hover:text-fg";

  return (
    <section
      role="dialog"
      aria-label={t("ai.chat.title")}
      className="fixed inset-0 z-30 flex flex-col overflow-hidden bg-surface md:inset-auto md:end-4 md:top-4 md:bottom-4 md:w-[440px] md:rounded-card-lg md:border md:border-line md:shadow-(--shadow-dock)"
    >
      <header className={cx("flex items-center gap-1 border-b px-5 pt-4 pb-3 transition-colors duration-200", scrolled ? "border-line/70" : "border-transparent")}>
        <div className="flex min-w-0 flex-1 flex-col">
          <b className="text-[15px] leading-tight font-semibold">{t("ai.chat.title")}</b>
          {(pick.label || providerLabel) && <span className="truncate text-[11px] text-muted">{pick.label || (provName ? `${provName} · ${providerLabel}` : providerLabel)}</span>}
        </div>
        <button type="button" className={cx(headBtn, history && "bg-surface-2 text-fg")} aria-label={t("ai.chat.history")} title={t("ai.chat.history")} aria-pressed={history} onClick={() => setHistory((v) => !v)}>
          <Icon name="list" size={16} />
        </button>
        <button type="button" className={headBtn} aria-label={t("ai.chat.new")} title={t("ai.chat.new")} onClick={() => chat.reset()}>
          <Icon name="plus" size={16} />
        </button>
        {owner && (
          <Link to="/ai" aria-label={t("ai.chat.settings")} title={t("ai.chat.settings")} className={headBtn}>
            <Icon name="gear" size={16} />
          </Link>
        )}
        <button type="button" className={headBtn} aria-label={t("ai.chat.close")} title={t("ai.chat.close")} onClick={onClose}>
          <Icon name="x" size={16} />
        </button>
      </header>

      {history ? (
        <History
          onPick={(id) => {
            setHistory(false);
            void chat.open(id);
          }}
        />
      ) : (
        <div ref={list} onScroll={(e) => setScrolled(e.currentTarget.scrollTop > 2)} className="flex min-h-0 flex-1 flex-col overflow-y-auto px-5 pt-1 pb-5">
          {chat.messages.length === 0 && <Hello onPick={(q) => void chat.send(q, attach ? context : null)} />}
          <Thread chat={chat} owner={owner} onNavigate={() => window.innerWidth < 768 && onClose()} />
        </div>
      )}

      <footer className="flex flex-col gap-2 px-4 pt-1 pb-4">
        {readOnly && <p className="text-[11px] text-muted">{t("ai.chat.readOnly")}</p>}
        {askSecret && (
          <div className="tone-warn tint flex flex-wrap items-center gap-2 rounded-field px-3 py-2 text-xs">
            <span className="min-w-0 flex-1">{t("ai.chat.secretWarn", { provider: provName || "—" })}</span>
            <Button type="button" size="xs" variant="secondary" onClick={() => submit(true)}>
              {t("ai.chat.sendAnyway")}
            </Button>
          </div>
        )}
        <Composer text={text} onText={setText} onSubmit={() => submit()} running={chat.running} onStop={() => void chat.stop()} context={context} attached={attach} onAttach={setAttach} pick={pick} owner={owner} onNavigate={() => window.innerWidth < 768 && onClose()} pickerOpen={pickerOpen} />
      </footer>
    </section>
  );
}

function Hello({ onPick }: { onPick: (q: string) => void }) {
  const t = useT();
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-5 py-8 text-center">
      <span aria-hidden className="grid size-14 place-items-center rounded-full bg-accent-soft text-accent-text">
        <Icon name="sparkle" size={26} />
      </span>
      <p className="max-w-[300px] text-[13.5px] leading-relaxed text-pretty text-muted">{t("ai.chat.hello")}</p>
      <div className="flex flex-wrap justify-center gap-2">
        {(["ai.chat.ex1", "ai.chat.ex2", "ai.chat.ex3"] as const).map((k) => (
          <button key={k} type="button" onClick={() => onPick(t(k))} className="max-w-full rounded-full bg-surface-2 px-3.5 py-2 text-[12.5px] transition-colors hover:bg-accent-soft">
            {t(k)}
          </button>
        ))}
      </div>
    </div>
  );
}



function History({ onPick }: { onPick: (id: string) => void }) {
  const t = useT();
  const fmt = useFmt();
  const [query, setQuery] = useState("");
  const q = useQuery(conversationsQuery(query.trim()));
  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 px-3.5 py-3">
      <input
        aria-label={t("ai.conv.search")}
        placeholder={t("ai.conv.search")}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="h-9 rounded-field border border-line bg-canvas px-3 text-[13px] outline-none focus:border-accent"
      />
      <div className="flex min-h-0 flex-1 flex-col gap-0.5 overflow-y-auto">
        {q.data?.length === 0 && <p className="py-6 text-center text-[13px] text-muted">{t("ai.conv.empty")}</p>}
        {q.data?.map((c) => (
          <button key={c.id} type="button" onClick={() => onPick(c.id)} className="flex flex-col items-start gap-0.5 rounded-field px-2.5 py-2 text-start hover:bg-surface-2">
            <span className="w-full truncate text-[13px] font-semibold">{c.title || t("ai.conv.untitled")}</span>
            <span className="text-[11px] text-muted">
              {fmt.ago(c.updatedUnix)} · {t.n("ai.conv.messages", c.messages)}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}

export const planIsOpen = (p: Plan) => p.state === PlanState.PROPOSED;
