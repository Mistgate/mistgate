import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { navKey, sections, type SectionId } from "@/components/nav";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { assistant } from "@/lib/api";
import { accessQuery, assistantErrorKey, conversationsQuery, looksSecret, providerName, type Context, type Msg, type Note, type Plan, type Tool } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { useIsOwner } from "@/lib/updates";
import { ChatMarkdown } from "./markdown";
import { PlanCardView } from "./plan-card";

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
  ({ id: `local-${Math.random().toString(36).slice(2)}`, role, text, createdUnix: Math.floor(Date.now() / 1000), context, tools: [], plans: [], remembered: [], end: "" }) as unknown as Msg;

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

  async function send(text: string, context: Context | null) {
    const body = text.trim();
    if (!body || s.running) return;
    const ctl = new AbortController();
    abort.current = ctl;
    setS((cur) => ({ ...cur, running: true, error: null, messages: [...cur.messages, newMessage(ChatRole.USER, body, context ?? undefined), newMessage(ChatRole.ASSISTANT, "")] }));
    try {
      for await (const res of assistant.chat({ conversationId: convRef.current, text: body, context: context ?? undefined }, { signal: ctl.signal })) {
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
            const tool = plain(ev.value) as Tool;
            patchLast((m) => ({ ...m, tools: [...m.tools.filter((x) => x.id !== tool.id), tool] }));
            break;
          }
          case "toolFinished": {
            const tool = plain(ev.value) as Tool;
            patchLast((m) => ({ ...m, tools: m.tools.some((x) => x.id === tool.id) ? m.tools.map((x) => (x.id === tool.id ? tool : x)) : [...m.tools, tool] }));
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

function ChatButton({ onClick, running }: { onClick: () => void; running: boolean }) {
  const t = useT();
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={t("ai.chat.open")}
      title={`${t("ai.chat.open")} (Ctrl+J)`}
      className="fixed end-4 bottom-[92px] z-20 grid size-[52px] place-items-center rounded-full bg-accent text-on-accent shadow-(--shadow-dock) transition-transform duration-200 hover:scale-105 md:end-6 md:bottom-6"
    >
      <Icon name="sparkle" size={22} />
      {running && <span aria-hidden className="absolute -top-0.5 -end-0.5 size-3.5 animate-pulse rounded-full border-2 border-canvas bg-warn" />}
    </button>
  );
}

export function ChatPanel({ chat, readOnly, providerLabel, provider, onClose, initialContext }: { chat: ChatApi; readOnly: boolean; providerLabel: string; provider?: number; onClose: () => void; initialContext?: Context | null }) {
  const t = useT();
  const owner = useIsOwner();
  const pageContext = usePageContext();
  const [attach, setAttach] = useState(true);
  const [text, setText] = useState("");
  const [askSecret, setAskSecret] = useState(false);
  const [history, setHistory] = useState(false);
  const list = useRef<HTMLDivElement>(null);
  const context = initialContext !== undefined ? initialContext : pageContext;

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
    void chat.send(body, attach ? context : null);
  }

  const last = chat.messages[chat.messages.length - 1];
  const waiting = chat.running && last?.role === ChatRole.ASSISTANT && !last.text && last.tools.length === 0;
  const provName = provider !== undefined ? t(providerName(provider)) : "";

  return (
    <section
      role="dialog"
      aria-label={t("ai.chat.title")}
      className="fixed inset-0 z-30 flex flex-col overflow-hidden bg-surface md:inset-auto md:end-4 md:top-4 md:bottom-4 md:w-[440px] md:rounded-card-lg md:border md:border-line md:shadow-(--shadow-dock)"
    >
      <header className="flex items-center gap-2 border-b border-line px-3.5 py-2.5">
        <span className="grid size-8 flex-none place-items-center rounded-xl bg-accent-soft text-accent-text">
          <Icon name="sparkle" size={16} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col">
          <b className="text-[14px] leading-tight">{t("ai.chat.title")}</b>
          {providerLabel && <span className="truncate text-[11px] text-muted">{provName ? `${provName} · ${providerLabel}` : providerLabel}</span>}
        </div>
        <IconButton aria-label={t("ai.chat.history")} title={t("ai.chat.history")} onClick={() => setHistory((v) => !v)}>
          <Icon name="list" size={16} />
        </IconButton>
        <IconButton aria-label={t("ai.chat.new")} title={t("ai.chat.new")} onClick={() => chat.reset()}>
          <Icon name="plus" size={16} />
        </IconButton>
        {owner && (
          <Link to="/ai" aria-label={t("ai.chat.settings")} title={t("ai.chat.settings")} className="grid size-9 place-items-center rounded-ctl text-muted hover:bg-surface-2 hover:text-fg">
            <Icon name="gear" size={16} />
          </Link>
        )}
        <IconButton aria-label={t("ai.chat.close")} title={t("ai.chat.close")} onClick={onClose}>
          <Icon name="x" size={16} />
        </IconButton>
      </header>

      {history ? (
        <History
          onPick={(id) => {
            setHistory(false);
            void chat.open(id);
          }}
        />
      ) : (
        <div ref={list} className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto px-3.5 py-4">
          {chat.messages.length === 0 && <Hello onPick={(q) => void chat.send(q, attach ? context : null)} />}
          {chat.messages.map((m) => (m.role === ChatRole.USER ? <UserMessage key={m.id} m={m} /> : <AssistantMessage key={m.id} m={m} owner={owner} chat={chat} onNavigate={() => window.innerWidth < 768 && onClose()} />))}
          {waiting && (
            <p className="flex items-center gap-2 text-[13px] text-muted">
              <span aria-hidden className="size-3.5 animate-spin rounded-full border-2 border-faint border-t-accent" />
              {t("ai.chat.thinking")}
            </p>
          )}
          {chat.error && <p className="rounded-field bg-danger-soft px-3 py-2 text-[13px] text-danger-text">{chat.error.startsWith("ai.") ? t(chat.error as MessageKey) : chat.error}</p>}
        </div>
      )}

      <footer className="flex flex-col gap-2 border-t border-line px-3.5 pt-2.5 pb-3">
        {readOnly && <p className="text-[11px] text-muted">{t("ai.chat.readOnly")}</p>}
        {askSecret && (
          <div className="tone-warn tint flex flex-wrap items-center gap-2 rounded-field px-3 py-2 text-xs">
            <span className="min-w-0 flex-1">{t("ai.chat.secretWarn", { provider: provName || "—" })}</span>
            <Button type="button" size="xs" variant="secondary" onClick={() => submit(true)}>
              {t("ai.chat.sendAnyway")}
            </Button>
          </div>
        )}
        {context && attach && (
          <span className="inline-flex max-w-full items-center gap-1.5 self-start rounded-ctl bg-surface-2 py-0.5 ps-2 pe-1 text-[11px] font-semibold text-muted">
            <Icon name="link" size={11} />
            <span className="truncate">{t("ai.chat.page", { label: context.label })}</span>
            <button type="button" aria-label={t("ai.chat.unpage")} title={t("ai.chat.unpage")} onClick={() => setAttach(false)} className="grid size-4 place-items-center rounded hover:text-fg">
              <Icon name="x" size={10} />
            </button>
          </span>
        )}
        <div className="flex items-end gap-2">
          <textarea
            aria-label={t("ai.chat.ph")}
            placeholder={t("ai.chat.ph")}
            value={text}
            rows={Math.min(6, Math.max(1, text.split("\n").length))}
            onChange={(e) => setText(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
                e.preventDefault();
                submit();
              }
            }}
            className="max-h-40 min-h-10 min-w-0 flex-1 resize-none rounded-field border border-line bg-canvas px-3 py-2 text-[13.5px] leading-snug outline-none transition-colors focus:border-accent"
          />
          {chat.running ? (
            <Button type="button" variant="secondary" onClick={() => void chat.stop()} aria-label={t("ai.chat.stop")}>
              <Icon name="x" size={14} />
              {t("ai.chat.stop")}
            </Button>
          ) : (
            <Button type="button" variant="primary" disabled={!text.trim()} onClick={() => submit()} aria-label={t("ai.chat.send")}>
              <Icon name="arrowUp" size={15} />
            </Button>
          )}
        </div>
      </footer>
    </section>
  );
}

function Hello({ onPick }: { onPick: (q: string) => void }) {
  const t = useT();
  return (
    <div className="flex flex-col gap-3 py-6">
      <p className="text-[13.5px] leading-relaxed text-pretty text-muted">{t("ai.chat.hello")}</p>
      <div className="flex flex-col gap-1.5">
        {(["ai.chat.ex1", "ai.chat.ex2", "ai.chat.ex3"] as const).map((k) => (
          <button key={k} type="button" onClick={() => onPick(t(k))} className="rounded-field border border-line bg-surface-2 px-3 py-2 text-start text-[13px] transition-colors hover:border-accent-line hover:bg-accent-soft">
            {t(k)}
          </button>
        ))}
      </div>
    </div>
  );
}

function UserMessage({ m }: { m: Msg }) {
  const t = useT();
  return (
    <div className="flex flex-col items-end gap-1">
      {m.context?.label && <span className="text-[11px] text-faint">{t("ai.chat.page", { label: m.context.label })}</span>}
      <div className="max-w-[85%] rounded-[16px] rounded-ee-[6px] bg-accent-soft px-3.5 py-2 text-[13.5px] leading-relaxed whitespace-pre-wrap break-words">{m.text}</div>
    </div>
  );
}

function AssistantMessage({ m, owner, chat, onNavigate }: { m: Msg; owner: boolean; chat: ChatApi; onNavigate: () => void }) {
  const t = useT();
  const [busy, setBusy] = useState("");
  const end = endText(m.end);
  return (
    <div className="flex flex-col gap-2">
      {m.tools.length > 0 && (
        <div className="flex flex-col gap-0.5">
          {m.tools.map((tool) => (
            <ToolRow key={tool.id} tool={tool} />
          ))}
        </div>
      )}
      {m.text && <ChatMarkdown text={m.text} onNavigate={onNavigate} />}
      {m.plans.map((p) => (
        <PlanCardView
          key={p.planId}
          plan={p}
          owner={owner}
          busy={busy === p.planId}
          onApply={() => {
            setBusy(p.planId);
            void chat.apply(p.planId).finally(() => setBusy(""));
          }}
          onDismiss={() => {
            setBusy(p.planId);
            void chat.dismiss(p.planId).finally(() => setBusy(""));
          }}
        />
      ))}
      {m.remembered.map((n) => (
        <Remembered key={n.id} note={n} onForget={() => void chat.forget(n.id)} />
      ))}
      {end && <p className="text-xs text-muted">{t(end)}</p>}
    </div>
  );
}

function endText(end: string): MessageKey | null {
  switch (end) {
    case "stopped":
      return "ai.end.stopped";
    case "limit":
      return "ai.end.limit";
    case "refused":
      return "ai.end.refused";
    case "budget":
      return "ai.end.budget";
    default:
      return null;
  }
}

function ToolRow({ tool }: { tool: Tool }) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const name = <code className="font-mono">{tool.tool}</code>;
  let line: ReactNode;
  if (!tool.done) line = <>{name}…</>;
  else if (!tool.ok) line = <>{name}: {tool.error || "—"}</>;
  else if (tool.count > 0) line = <>{name} · {tool.count}</>;
  else line = name;
  return (
    <div className="flex flex-col">
      <button type="button" onClick={() => setOpen((v) => !v)} aria-expanded={open} className={cx("flex items-center gap-1.5 self-start rounded-ctl px-1.5 py-0.5 text-[11.5px] hover:bg-surface-2", !tool.done ? "text-muted" : tool.ok ? "text-faint" : "text-danger-text")}>
        {!tool.done ? <span aria-hidden className="size-2.5 animate-spin rounded-full border-[1.5px] border-faint border-t-accent" /> : <Icon name={tool.ok ? "check" : "warn"} size={11} />}
        <span>{line}</span>
        <Icon name="chevronDown" size={10} className={cx("transition-transform", open && "rotate-180")} />
      </button>
      {open && tool.argsJson && <pre className="mt-1 ms-5 max-h-40 overflow-auto rounded-field bg-canvas px-2.5 py-1.5 font-mono text-[11px] text-muted">{pretty(tool.argsJson)}</pre>}
      <span className="sr-only">{t("ai.tool.done", { tool: tool.tool })}</span>
    </div>
  );
}

const pretty = (json: string) => {
  try {
    return JSON.stringify(JSON.parse(json), null, 2);
  } catch {
    return json;
  }
};

function Remembered({ note, onForget }: { note: Note; onForget: () => void }) {
  const t = useT();
  return (
    <div className="flex items-start gap-2 self-start rounded-field border border-dashed border-line px-2.5 py-1.5 text-xs">
      <Icon name="bolt" size={12} className="mt-0.5 flex-none text-accent-text" />
      <span className="min-w-0 flex-1 text-muted">{t("ai.mem.saved", { text: note.text })}</span>
      <button type="button" onClick={onForget} className="font-semibold text-accent-text">
        {t("ai.mem.undo")}
      </button>
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
