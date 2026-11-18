import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type RefObject } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Tip } from "@/components/ui/tooltip";
import { ChatRole } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "@/lib/api";
import { accessQuery, assistantErrorKey, followUpPoll, type Context, type FollowUp, type LiveMsg, type LiveTool, type Msg, type Note, type Plan, type Tool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { readPref } from "@/lib/storage";
import { actionOf, AUTO_OPEN_KEY, markAction } from "@/lib/ui-actions";
import { CatMark } from "./cat";

// The floating chat of the assistant (ADR 0014): a round button at the corner of every admin page (and Ctrl/⌘+J) while the
// assistant is open to this admin; it opens the window on the corner of the page (a card over the phone's screen). The button and the
// conversation's state are here, in the first load of the panel; the window itself (panel.tsx and its CSS) comes with the first opening. One turn streams at a
// time: the answer's Markdown, a line per tool call, plan cards the admin applies or dismisses, "Remembered" notes.

type ChatState = {
  conversationId: string;
  messages: Msg[];
  /** a turn of the conversation runs: this chat's own (live), or one on the server (a follow-up's, another tab's) */
  running: boolean;
  live: boolean;
  error: MessageKey | string | null;
  /** the follow-ups the assistant set in this conversation that have not run yet */
  followUps: FollowUp[];
};

const empty: ChatState = { conversationId: "", messages: [], running: false, live: false, error: null, followUps: [] };

const newMessage = (role: ChatRole, text: string, context?: Context): Msg =>
  ({ id: `local-${Math.random().toString(36).slice(2)}`, role, text, createdUnix: Math.floor(Date.now() / 1000), context, tools: [], plans: [], remembered: [], end: "", ...(role === ChatRole.ASSISTANT ? { t0: Date.now() } : {}) }) as unknown as Msg;

/** The conversation, its turn stream and the actions on its plans and notes. */
export function useChat() {
  const t = useT();
  const qc = useQueryClient();
  const guard = useStepUp();
  const navigate = useNavigate();
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
    setS((cur) => ({ ...cur, running: true, live: true, error: null, messages: [...cur.messages, newMessage(ChatRole.USER, body, context ?? undefined), newMessage(ChatRole.ASSISTANT, "")] }));
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
            // "open without asking": a page the assistant opens in this turn is opened at once (a form to fill never is)
            const act = actionOf({ tool: fin.tool, argsJson: fin.argsJson, done: true, ok: fin.ok });
            if (act?.kind === "open" && readPref(AUTO_OPEN_KEY) === "1") {
              markAction(fin.id, "used");
              void navigate(act.target as never);
            }
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
          case "followUp": {
            const fu = plain(ev.value) as FollowUp;
            setS((cur) => ({ ...cur, followUps: [...cur.followUps.filter((x) => x.id !== fu.id), fu] }));
            break;
          }
          case "followUpCancelled": {
            const id = ev.value;
            setS((cur) => ({ ...cur, followUps: cur.followUps.filter((x) => x.id !== id) }));
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
      setS((cur) => ({ ...cur, running: false, live: false }));
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
        setS({ conversationId: id, messages: r.messages as Msg[], running: r.running, live: false, error: null, followUps: (r.followUps ?? []) as FollowUp[] });
        // opening it reads what a follow-up answered: the dots go
        if ((qc.getQueryData(accessQuery.queryKey)?.unread ?? 0) > 0) {
          void qc.invalidateQueries({ queryKey: ["assistant", "access"] });
          void qc.invalidateQueries({ queryKey: ["assistant", "conversations"] });
        }
      } catch (e) {
        setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
      }
    },
    [t, qc],
  );

  /** Reloads the open conversation (a plan decided elsewhere, a follow-up's turn the server ran, shows its new state). */
  const refresh = useCallback(() => {
    if (convRef.current && !abort.current) void open(convRef.current);
  }, [open]);

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

  async function cancelFollowUp(id: string) {
    try {
      await assistant.cancelFollowUp({ conversationId: convRef.current, id });
      setS((cur) => ({ ...cur, followUps: cur.followUps.filter((x) => x.id !== id) }));
      void qc.invalidateQueries({ queryKey: ["assistant", "access"] });
    } catch (e) {
      setS((cur) => ({ ...cur, error: assistantErrorKey(e) ?? errorText(e, t) }));
      refresh();
    }
  }

  async function forget(noteId: string) {
    await assistant.deleteMemory({ id: noteId }).catch(() => {});
    setS((cur) => ({ ...cur, messages: cur.messages.map((m) => ({ ...m, remembered: m.remembered.filter((n) => n.id !== noteId) })) }));
    void qc.invalidateQueries({ queryKey: ["assistant", "memory"] });
  }

  return { ...s, send, stop, open, refresh, reset, apply, dismiss, forget, cancelFollowUp };
}

export type ChatApi = ReturnType<typeof useChat>;

// The window is its own chunk: its code and its CSS come when it is first opened (or when the pointer comes to the button).
const loadPanel = () => import("./panel");
const ChatPanel = lazy(() => loadPanel().then((m) => ({ default: m.ChatPanel })));

/**
 * The round button and the window. Mounted once in the shell; renders nothing while the assistant is not open to this admin. With the assistant on
 * and allowed but no provider connected the button is there too, and the window says so (the board's "not connected").
 */
export function ChatDock() {
  // the button looks again soon after the admin's next follow-up is due: its answer lights the dot
  const access = useQuery({ ...accessQuery, refetchInterval: (q) => followUpPoll(q.state.data) });
  const [open, setOpen] = useState(false);
  const chat = useChat();
  const button = useRef<HTMLButtonElement>(null);
  const openConversation = chat.open;
  const available = (access.data?.available ?? false) || access.data?.reason === "no_provider";

  const [draft, setDraft] = useState<{ text: string; n: number } | undefined>();
  const resetChat = chat.reset;

  // a conversation opened from AI -> Memory, or a new one with text in the composer (askAssistant)
  useEffect(() => {
    const onOpen = (e: Event) => {
      const detail = (e as CustomEvent<{ id?: string; text?: string }>).detail;
      if (detail?.id) {
        setOpen(true);
        void openConversation(detail.id);
      } else if (detail?.text) {
        resetChat();
        setDraft({ text: detail.text, n: Date.now() });
        setOpen(true);
      }
    };
    window.addEventListener("mistgate:assistant-open", onOpen);
    return () => window.removeEventListener("mistgate:assistant-open", onOpen);
  }, [openConversation, resetChat]);

  // closing from the keyboard (or the window's own button) gives the focus back to the button the window came from
  const close = useCallback(() => {
    const inside = !!document.activeElement?.closest(".as-dock");
    setOpen(false);
    if (inside) setTimeout(() => button.current?.focus(), 0);
  }, []);

  useEffect(() => {
    if (!available) return;
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "j") {
        e.preventDefault();
        if (open) close();
        else setOpen(true);
      }
      if (e.key === "Escape" && open) close();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [available, open, close]);

  if (!available) return null;
  return (
    <>
      <ChatButton buttonRef={button} onClick={() => setOpen(true)} onWarm={() => void loadPanel()} open={open} running={chat.running} unread={access.data?.unread ?? 0} />
      {open && (
        <Suspense fallback={null}>
          <ChatPanel key={draft?.n ?? 0} chat={chat} readOnly={access.data?.readOnly ?? false} providerLabel={access.data?.model || ""} provider={access.data?.provider} draft={draft} onClose={close} />
        </Suspense>
      )}
    </>
  );
}

/**
 * The assistant's open button (.fab): 56px, the accent, the cat. A count of answers that came while the admin was away sits on its corner (and
 * in its name); a turn that is running draws a ring round it. It floats at the end of the window, above the dock on a phone. While the window
 * is open it shrinks away and cannot be reached (.fab.gone).
 */
export function ChatButton({ onClick, running, unread = 0, open = false, onWarm, buttonRef }: { onClick: () => void; running: boolean; unread?: number; open?: boolean; onWarm?: () => void; buttonRef?: RefObject<HTMLButtonElement | null> }) {
  const t = useT();
  // a follow-up answered while the admin was away: the count on the button, and in its name
  const label = unread > 0 ? `${t("ai.chat.open")}: ${t.n("ai.chat.unread", unread)}` : t("ai.chat.open");
  return (
    <div className="fixed end-4 bottom-[104px] z-20 md:end-8 md:bottom-8">
      <Tip label={`${label} (Ctrl+J)`}>
        <button
          ref={buttonRef}
          type="button"
          onClick={onClick}
          onPointerEnter={onWarm}
          onFocus={onWarm}
          aria-label={label}
          aria-expanded={open}
          inert={open || undefined}
          className={cx(
            "cat-fab relative grid size-14 place-items-center rounded-full bg-accent text-on-accent shadow-(--shadow-toast) transition-[transform,background-color,opacity] duration-200 ease-spring hover:-translate-y-0.5 hover:bg-accent-hover active:scale-[0.96]",
            open && "pointer-events-none scale-[0.6] opacity-0",
          )}
        >
          {running && <span aria-hidden className="chat-fab-ring pointer-events-none absolute inset-0 rounded-full border-2 border-accent" />}
          <CatMark />
          {unread > 0 && (
            <span aria-hidden data-slot="unread" className="absolute -end-0.5 -top-0.5 grid h-5 min-w-5 place-items-center rounded-[10px] bg-(--accent-sky) px-1.5 font-mono text-[11px] leading-none font-medium text-on-accent shadow-[0_0_0_3px_var(--bg)]">
              {unread}
            </span>
          )}
        </button>
      </Tip>
    </div>
  );
}
