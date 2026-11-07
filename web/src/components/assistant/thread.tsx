import { useState, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { ChatRole } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { splitText, type LiveMsg, type LiveTool, type Msg, type Note } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { dayLabel, useFmt } from "@/lib/format";
import { ChatMarkdown } from "./markdown";
import { PlanCardView } from "./plan-card";
import type { ChatApi } from "./chat";
import { Thinking, ToolCards } from "./tool-cards";

// The conversation as a messenger thread: a date line between days, messages grouped by sender (the name and time over the
// first of a group, the round avatar beside the last), the assistant's tools folded into one muted line per turn, and a
// typing indicator while it works. Logical properties only, so right-to-left languages mirror.

const toolNames: Record<string, MessageKey> = {
  fleet_status: "ai.toolname.fleet_status",
  health_summary: "ai.toolname.health_summary",
  alerts_list: "ai.toolname.alerts_list",
  node_journal: "ai.toolname.node_journal",
  nodes_list: "ai.toolname.nodes_list",
  user_get: "ai.toolname.user_get",
  user_devices: "ai.toolname.user_devices",
  events_search: "ai.toolname.events_search",
};

/** The tool in words when we have them, else null (the caller shows the id itself). */
const toolWords = (t: T, id: string): string | null => (toolNames[id] ? t(toolNames[id]) : null);

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

const hasContent = (m: Msg) => m.role === ChatRole.USER || !!m.text || m.tools.length > 0 || m.plans.length > 0 || m.remembered.length > 0 || !!endText(m.end);

function dayKey(unix: number) {
  const d = new Date(unix * 1000);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/** A thin line each side of a centred muted word: the date, or the way a turn ended. */
export function Rule({ children, tone = "muted", role }: { children: ReactNode; tone?: "muted" | "danger"; role?: "status" | "alert" }) {
  return (
    <div role={role} className={cx("my-3 flex items-center gap-3 text-[11px] font-medium", tone === "danger" ? "text-danger-text" : "text-muted")}>
      <span aria-hidden className="h-px flex-1 bg-line" />
      <span className="max-w-[80%] text-center text-pretty">{children}</span>
      <span aria-hidden className="h-px flex-1 bg-line" />
    </div>
  );
}

export function Avatar() {
  return (
    <span data-slot="avatar" aria-hidden className="grid size-7 flex-none place-items-center rounded-full bg-accent-soft text-accent-text">
      <Icon name="sparkle" size={14} />
    </span>
  );
}

/** The avatar's column: the avatar itself on the last message of a group, an empty slot of the same width on the others. */
const Slot = ({ show }: { show: boolean }) => <div className="flex size-7 flex-none items-end">{show && <Avatar />}</div>;

export function Typing({ label }: { label: ReactNode }) {
  return (
    <div role="status" data-slot="typing" className="mt-3 flex items-end gap-2">
      <Slot show />
      <div className="flex items-center gap-2">
        <span aria-hidden className="flex items-center gap-1 rounded-[18px] bg-surface-2 px-3.5 py-3">
          <i className="chat-dot size-1.5 rounded-full bg-muted" />
          <i className="chat-dot size-1.5 rounded-full bg-muted" />
          <i className="chat-dot size-1.5 rounded-full bg-muted" />
        </span>
        <span className="text-xs text-muted">{label}</span>
      </div>
    </div>
  );
}

/** "Checking <tool>…" with the tool in words, or its id in small monospace. */
function CheckingLabel({ tool }: { tool: string }) {
  const t = useT();
  const words = toolWords(t, tool);
  if (words) return <>{t("ai.chat.checking", { tool: words })}</>;
  const [before, after] = t("ai.chat.checking", { tool: "\u0000" }).split("\u0000");
  return (
    <>
      {before}
      <code className="font-mono text-[11px]">{tool}</code>
      {after}
    </>
  );
}

function Header({ name, time }: { name: string; time: string }) {
  return (
    <div className="flex items-baseline gap-2 px-1 text-xs">
      <b className="font-medium text-fg">{name}</b>
      {time && <span className="text-muted">{time}</span>}
    </div>
  );
}

function Remembered({ note, onForget }: { note: Note; onForget: () => void }) {
  const t = useT();
  return (
    <div className="flex max-w-full items-center gap-1.5 rounded-full bg-accent-soft py-1 ps-2.5 pe-2 text-xs">
      <Icon name="bolt" size={12} className="flex-none text-accent-text" />
      <span className="min-w-0 truncate text-muted">{t("ai.mem.saved", { text: note.text })}</span>
      <button type="button" onClick={onForget} className="flex-none font-semibold text-accent-text hover:underline">
        {t("ai.mem.undo")}
      </button>
    </div>
  );
}

function UserBubble({ m, first, tail }: { m: Msg; first: boolean; tail: ReactNode }) {
  const t = useT();
  return (
    <div data-role="user" className={cx("flex flex-col items-end gap-1", first ? "mt-4" : "mt-1")}>
      {m.context?.label && <span className="px-1 text-[11px] text-faint">{t("ai.chat.page", { label: m.context.label })}</span>}
      <div className="max-w-[80%] rounded-[18px] bg-fg px-3.5 py-2.5 text-[13.5px] leading-relaxed break-words whitespace-pre-wrap text-canvas">{m.text}</div>
      {tail}
    </div>
  );
}

function AssistantBubble({ m, first, avatar, running, owner, chat, onNavigate }: { m: Msg; first: boolean; avatar: boolean; running: boolean; owner: boolean; chat: ChatApi; onNavigate: () => void }) {
  const t = useT();
  const { interim, answer } = splitText(m);
  const fmt = useFmt();
  const [busy, setBusy] = useState("");
  return (
    <div data-role="assistant" className={cx("flex items-end gap-2", first ? "mt-4" : "mt-1")}>
      <Slot show={avatar} />
      <div className="flex min-w-0 flex-1 flex-col items-start gap-1.5">
        {first && <Header name={t("ai.chat.title")} time={m.createdUnix > 0 ? fmt.clock(m.createdUnix) : ""} />}
        {(m.tools.length > 0 || interim) && <Thinking m={m as LiveMsg} running={running} interim={interim} />}
        {m.tools.length > 0 && <ToolCards tools={m.tools as LiveTool[]} />}
        {answer && (
          <div data-slot="bubble" className="max-w-full rounded-[18px] bg-surface-2 px-3.5 py-2.5">
            <ChatMarkdown text={answer} onNavigate={onNavigate} />
          </div>
        )}
        {m.plans.map((p) => (
          <div key={p.planId} className="w-full">
            <PlanCardView
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
          </div>
        ))}
        {m.remembered.map((n) => (
          <Remembered key={n.id} note={n} onForget={() => void chat.forget(n.id)} />
        ))}
      </div>
    </div>
  );
}

export function Thread({ chat, owner, onNavigate }: { chat: ChatApi; owner: boolean; onNavigate: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const all = chat.messages;
  const last = all[all.length - 1];
  const shown = all.filter(hasContent);
  const openTool = last?.role === ChatRole.ASSISTANT ? last.tools.find((x) => !x.done) : undefined;
  const typing = chat.running && !!last && (last.role === ChatRole.USER || !splitText(last).answer || !!openTool);
  const lastShown = shown[shown.length - 1];
  const lastUser = [...shown].reverse().find((m) => m.role === ChatRole.USER);
  const answered = !!lastShown && lastShown.role === ChatRole.ASSISTANT && lastUser !== undefined && shown.indexOf(lastShown) > shown.indexOf(lastUser);

  const out: ReactNode[] = [];
  shown.forEach((m, i) => {
    const prev = shown[i - 1];
    const next = shown[i + 1];
    const day = m.createdUnix > 0 ? dayKey(m.createdUnix) : "";
    const newDay = day !== "" && (!prev || prev.createdUnix <= 0 || dayKey(prev.createdUnix) !== day);
    if (newDay) out.push(<Rule key={`day-${m.id}`}>{dayLabel(t, fmt, m.createdUnix)}</Rule>);
    const first = !prev || prev.role !== m.role || newDay;
    const nextDay = next && next.createdUnix > 0 && day !== "" && dayKey(next.createdUnix) !== day;
    const lastOfGroup = !next || next.role !== m.role || !!nextDay;
    if (m.role === ChatRole.USER) {
      const status =
        m === lastUser ? (
          <span className="flex items-center gap-1 px-1 text-[11px] text-muted">
            <span aria-hidden className="inline-flex">
              <Icon name="check" size={11} />
              {answered && <Icon name="check" size={11} className="-ms-1.5" />}
            </span>
            {t(answered ? "ai.chat.read" : "ai.chat.sent", { time: m.createdUnix > 0 ? fmt.clock(answered && lastShown ? lastShown.createdUnix || m.createdUnix : m.createdUnix) : "" }).trim()}
          </span>
        ) : null;
      out.push(<UserBubble key={m.id} m={m} first={first} tail={status} />);
    } else {
      const isTail = m === lastShown;
      out.push(<AssistantBubble key={m.id} m={m} first={first} avatar={lastOfGroup && !(isTail && typing)} running={chat.running && isTail} owner={owner} chat={chat} onNavigate={onNavigate} />);
      const end = endText(m.end);
      if (end) out.push(<Rule key={`end-${m.id}`} role="status">{t(end)}</Rule>);
    }
  });

  return (
    <>
      {out}
      {typing && <Typing label={openTool ? <CheckingLabel tool={openTool.tool} /> : t("ai.chat.typing")} />}
      {chat.error && (
        <Rule tone="danger" role="alert">
          {chat.error.startsWith("ai.") ? t(chat.error as MessageKey) : chat.error}
        </Rule>
      )}
    </>
  );
}
