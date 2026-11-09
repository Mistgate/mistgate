import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { navKey, sections, type SectionId } from "@/components/nav";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "@/lib/api";
import { accessQuery, assistantErrorKey, conversationsQuery, followUpPoll, looksSecret, providerName, type Context, type FollowUp, type LiveMsg, type LiveTool, type Msg, type Note, type Plan, type Tool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { useIsOwner } from "@/lib/updates";
import { readPref } from "@/lib/storage";
import { actionOf, AUTO_OPEN_KEY, markAction } from "@/lib/ui-actions";
import { usePick } from "./agent-picker";
import { Composer } from "./composer";
import { Thread } from "./thread";

// The floating chat of the assistant (ADR 0014): a round button at the corner of every admin page (and Ctrl/⌘+J) while the
// assistant is open to this admin; it opens a panel beside the page (the whole screen on a phone). One turn streams at a
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

/**
 * While the panel is open the conversation follows what the server does without this chat: a turn running there (a follow-up's,
 * another tab's) is looked at every 4 s until it ends, and a follow-up is looked for just after its time.
 */
function useServerTurns(chat: ChatApi) {
  const { conversationId, running, live, refresh } = chat;
  const followUps = chat.followUps ?? [];
  const next = followUps.reduce((min, f) => (Number(f.dueUnix) > 0 ? Math.min(min, Number(f.dueUnix)) : min), Infinity);
  useEffect(() => {
    if (!conversationId || live || !refresh) return;
    let delay: number;
    const ahead = next * 1000 - Date.now();
    if (running) delay = 4_000;
    else if (!Number.isFinite(next)) return;
    // due: the scheduler takes it within a tick; long overdue (the assistant was off), looked at now and then
    else delay = ahead > 0 ? Math.min(ahead + 3_000, 10 * 60_000) : ahead > -120_000 ? 5_000 : 30_000;
    const id = setTimeout(refresh, delay);
    return () => clearTimeout(id);
  }, [conversationId, running, live, next, refresh]);
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
  // the button looks again soon after the admin's next follow-up is due: its answer lights the dot
  const access = useQuery({ ...accessQuery, refetchInterval: (q) => followUpPoll(q.state.data) });
  const [open, setOpen] = useState(false);
  const chat = useChat();
  const openConversation = chat.open;
  const available = access.data?.available ?? false;

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
      {!open && <ChatButton onClick={() => setOpen(true)} running={chat.running} unread={access.data?.unread ?? 0} />}
      {open && <ChatPanel key={draft?.n ?? 0} chat={chat} readOnly={access.data?.readOnly ?? false} providerLabel={access.data?.model || ""} provider={access.data?.provider} draft={draft} onClose={() => setOpen(false)} />}
    </>
  );
}

export function ChatButton({ onClick, running, unread = 0 }: { onClick: () => void; running: boolean; unread?: number }) {
  const t = useT();
  // a follow-up answered while the admin was away: a dot on the button, and the count in its name
  const label = unread > 0 ? `${t("ai.chat.open")}: ${t.n("ai.chat.unread", unread)}` : t("ai.chat.open");
  return (
    <div className="fixed end-4 bottom-[92px] z-20 md:end-6 md:bottom-6">
      <Tip label={`${label} (Ctrl+J)`}>
        <button type="button" onClick={onClick} aria-label={label} className="chat-fab relative grid size-[52px] place-items-center rounded-full text-on-accent">
          {running && <span aria-hidden className="chat-fab-ring pointer-events-none absolute inset-0 rounded-full border-2 border-accent" />}
          <Icon name="sparkle" size={22} fill="currentColor" strokeWidth={1.6} />
          {unread > 0 && <span aria-hidden data-slot="unread" className="absolute end-0.5 top-0.5 size-3 rounded-full border-2 border-surface bg-danger" />}
        </button>
      </Tip>
    </div>
  );
}

export function ChatPanel({ chat, readOnly, providerLabel, provider, onClose, initialContext, pickerOpen, draft }: { chat: ChatApi; readOnly: boolean; providerLabel: string; provider?: number; onClose: () => void; initialContext?: Context | null; pickerOpen?: boolean; draft?: { text: string; n: number } }) {
  const t = useT();
  const owner = useIsOwner();
  const pageContext = usePageContext();
  const access = useQuery(accessQuery);
  const [attach, setAttach] = useState(true);
  const [text, setText] = useState(draft?.text ?? "");
  const [askSecret, setAskSecret] = useState(false);
  // opened with an answer that came while the admin was away and no conversation in front: the list, where it is marked
  const [history, setHistory] = useState(() => !chat.conversationId && (access.data?.unread ?? 0) > 0);
  useServerTurns(chat);
  // opened again on a conversation that had follow-ups waiting (or an unread answer): it may have moved on meanwhile
  const reopen = useRef({ chat, unread: access.data?.unread ?? 0 });
  useEffect(() => {
    const { chat: c, unread } = reopen.current;
    if (c.conversationId && !c.live && ((c.followUps?.length ?? 0) > 0 || unread > 0)) c.refresh?.();
  }, []);
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

  // the answer is revealed word by word after its text has arrived (useSmoothText): stay at the bottom while it grows,
  // unless the admin has scrolled up to read
  useEffect(() => {
    const el = list.current;
    const content = el?.firstElementChild;
    if (!el || !content || history || typeof ResizeObserver === "undefined") return; // the history list does not grow
    let height = content.getBoundingClientRect().height;
    const ro = new ResizeObserver(() => {
      const grew = content.getBoundingClientRect().height - height;
      height += grew;
      if (grew > 0 && el.scrollHeight - el.scrollTop - el.clientHeight - grew < 48) el.scrollTop = el.scrollHeight;
    });
    ro.observe(content);
    return () => ro.disconnect();
  }, [history]);

  // a turn watched to its end: the halo runs out from the end of the answer, inside the panel
  const wasLive = useRef(chat.live);
  useEffect(() => {
    const el = list.current;
    if (wasLive.current && !chat.live && el && tail?.end === "end") {
      const r = el.getBoundingClientRect();
      halo("save", { target: el, origin: { x: r.left + r.width / 2, y: r.bottom - 24 } });
    }
    wasLive.current = chat.live;
  }, [chat.live, tail?.end]);

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
      data-fx-halo
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



type Conversation = NonNullable<ReturnType<typeof useConversations>["data"]>[number];
const useConversations = (query: string) => useQuery(conversationsQuery(query));

/** A conversation's title in a list, with its marks: scheduled, and the dot of an answer that came while the admin was away. */
export function ConversationHead({ c }: { c: Conversation }) {
  const t = useT();
  return (
    <span className="flex w-full items-center gap-1.5">
      {c.unread && <span role="img" aria-label={t("ai.conv.unread")} title={t("ai.conv.unread")} data-slot="unread" className="size-2 flex-none rounded-full bg-danger" />}
      <span className={cx("min-w-0 truncate text-[13px]", c.unread ? "font-bold" : "font-semibold")}>{c.title || t("ai.conv.untitled")}</span>
      {c.taskId && <span className="flex-none rounded-full bg-accent-soft px-1.5 py-px text-[10px] font-semibold text-accent-text">{t("ai.conv.scheduled")}</span>}
    </span>
  );
}

/** When it last moved, its size, and when its next follow-up is due (the panel's time zone). */
export function ConversationMeta({ c, tz }: { c: Conversation; tz: number }) {
  const t = useT();
  const fmt = useFmt();
  const due = Number(c.followUpUnix ?? 0);
  return (
    <span className="text-[11px] text-muted">
      {fmt.ago(c.updatedUnix)} · {t.n("ai.conv.messages", c.messages)}
      {due > 0 && <> · {t("ai.conv.followUp", { time: fmt.stampAt(due, tz) })}</>}
    </span>
  );
}

function History({ onPick }: { onPick: (id: string) => void }) {
  const t = useT();
  const [query, setQuery] = useState("");
  const q = useConversations(query.trim());
  const tz = useQuery(accessQuery).data?.timezoneOffsetMinutes ?? 0;
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
            <ConversationHead c={c} />
            <ConversationMeta c={c} tz={tz} />
          </button>
        ))}
      </div>
    </div>
  );
}

export const planIsOpen = (p: Plan) => p.state === PlanState.PROPOSED;
