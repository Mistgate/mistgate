import { useQuery } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { Icon } from "@/components/ui/icons";
import { ChatRole } from "@/gen/mistgate/admin/v1/assistant_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { splitText, type LiveMsg, type LiveTool, type Msg, type Note } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { dayLabel, useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { ChatMarkdown, type ChatEntity } from "./markdown";
import { PlanCardView } from "./plan-card";
import type { ChatApi } from "./chat";
import { Thinking, ToolGroup, toolWords } from "./tool-cards";

// The conversation as a messenger thread: a date line between days, messages grouped by sender (the name and time over the
// first of a group, the round avatar beside the first), the assistant's words straight on the canvas and its tools in one quiet group, and a
// typing indicator while it works. Logical properties only, so right-to-left languages mirror.

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
    <div role={role} className={cx("my-5 flex items-center gap-3 text-[11px] font-medium", tone === "danger" ? "text-danger-text" : "text-muted")}>
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

/** The avatar's column: the avatar itself beside the first message of a group, an empty slot of the same width beside the others. */
const Slot = ({ show }: { show: boolean }) => <div className="flex size-7 flex-none items-start">{show && <Avatar />}</div>;

export function Typing({ label, avatar = true }: { label: ReactNode; avatar?: boolean }) {
  return (
    <div role="status" data-slot="typing" className="chat-in mt-4 flex items-center gap-3">
      <Slot show={avatar} />
      <div className="flex items-center gap-2">
        <span aria-hidden className="flex items-center gap-1 rounded-full bg-fg/[0.06] px-3 py-2.5">
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
    <div className="flex h-7 items-center gap-2 text-xs">
      <b className="font-medium text-fg">{name}</b>
      {time && <span className="text-muted">{time}</span>}
    </div>
  );
}

function Remembered({ note, onForget }: { note: Note; onForget: () => void }) {
  const t = useT();
  return (
    <div className="chat-in flex max-w-full items-center gap-2 rounded-full bg-fg/[0.05] py-1.5 ps-3 pe-3 text-xs">
      <Icon name="bookmark" size={12} className="flex-none text-accent-text" />
      <span className="min-w-0 truncate text-muted">{t("ai.mem.saved", { text: note.text })}</span>
      <button type="button" onClick={onForget} className="flex-none font-medium text-accent-text hover:underline">
        {t("ai.mem.undo")}
      </button>
    </div>
  );
}

function UserBubble({ m, first, last }: { m: Msg; first: boolean; last: boolean }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <div data-role="user" className={cx("chat-in group flex flex-col items-end gap-1", first ? "mt-6" : "mt-2")}>
      {m.context?.label && <span className="px-1 text-[11px] text-faint">{t("ai.chat.page", { label: m.context.label })}</span>}
      <div className="max-w-[82%] rounded-[18px] bg-fg px-4 py-2.5 text-[14px] leading-[1.55] break-words whitespace-pre-wrap text-canvas">{m.text}</div>
      {m.createdUnix > 0 && <span className={cx("px-1 text-[11px] text-muted transition-opacity", last ? "opacity-100" : "opacity-0 group-hover:opacity-100")}>{fmt.clock(m.createdUnix)}</span>}
    </div>
  );
}

function AssistantBubble({ m, first, avatar, running, owner, chat, onNavigate }: { m: Msg; first: boolean; avatar: boolean; running: boolean; owner: boolean; chat: ChatApi; onNavigate: () => void }) {
  const t = useT();
  const { interim, answer } = splitText(m);
  const fmt = useFmt();
  const [busy, setBusy] = useState("");
  const live = m as LiveMsg;
  const entities = useNodeEntities();
  return (
    <div data-role="assistant" className={cx("chat-in flex items-start gap-3", first ? "mt-6" : "mt-3")}>
      <Slot show={avatar} />
      <div className="flex min-w-0 flex-1 flex-col items-start gap-2">
        {first && <Header name={t("ai.chat.title")} time={m.createdUnix > 0 ? fmt.clock(m.createdUnix) : ""} />}
        {(interim || (running && m.tools.length > 0)) && <Thinking m={live} running={running} interim={interim} />}
        {m.tools.length > 0 && <ToolGroup tools={m.tools as LiveTool[]} running={running} workedMs={interim ? undefined : live.workedMs} />}
        {answer && (
          <div data-slot="bubble" className="max-w-full">
            <ChatMarkdown text={answer} onNavigate={onNavigate} entities={entities} />
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

  const out: ReactNode[] = [];
  shown.forEach((m, i) => {
    const prev = shown[i - 1];
    const day = m.createdUnix > 0 ? dayKey(m.createdUnix) : "";
    const newDay = day !== "" && (!prev || prev.createdUnix <= 0 || dayKey(prev.createdUnix) !== day);
    if (newDay) out.push(<Rule key={`day-${m.id}`}>{dayLabel(t, fmt, m.createdUnix)}</Rule>);
    const first = !prev || prev.role !== m.role || newDay;
    if (m.role === ChatRole.USER) {
      out.push(<UserBubble key={m.id} m={m} first={first} last={m === lastUser} />);
    } else {
      const isTail = m === lastShown;
      out.push(<AssistantBubble key={m.id} m={m} first={first} avatar={first} running={chat.running && isTail} owner={owner} chat={chat} onNavigate={onNavigate} />);
      const end = endText(m.end);
      if (end) out.push(<Rule key={`end-${m.id}`} role="status">{t(end)}</Rule>);
    }
  });

  return (
    <>
      {out}
      {typing && <Typing avatar={lastShown?.role !== ChatRole.ASSISTANT} label={openTool ? <CheckingLabel tool={openTool.tool} /> : t("ai.chat.typing")} />}
      {chat.error && (
        <Rule tone="danger" role="alert">
          {chat.error.startsWith("ai.") ? t(chat.error as MessageKey) : chat.error}
        </Rule>
      )}
    </>
  );
}

/** The fleet's nodes as names an answer may mention: each becomes a chip with the node's status that opens its page. */
function useNodeEntities(): ChatEntity[] {
  const nodes = useQuery(nodesQuery).data?.nodes;
  return useMemo(
    () =>
      (nodes ?? [])
        .filter((n) => n.status !== NodeStatus.RETIRED && n.name.trim() !== "")
        .map((n) => ({
          name: n.name,
          id: n.id,
          dot: n.status === NodeStatus.ONLINE ? "ok" : n.status === NodeStatus.BLIP ? "warn" : n.status === NodeStatus.DOWN ? "bad" : "off",
        })),
    [nodes],
  );
}
