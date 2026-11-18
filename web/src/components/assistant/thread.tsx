import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { buttonClass } from "@/components/ui/button";
import { ChatRole, PlanState } from "@/gen/mistgate/admin/v1/assistant_pb";
import { InlineApproval } from "./inline-approval";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { accessQuery, followUpReasonKey, splitText, type FollowUp, type LiveMsg, type LiveTool, type Msg, type Note } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { dayLabel, minutesText, useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { useNow } from "@/lib/time";
import { actionOf } from "@/lib/ui-actions";
import { ActionCard } from "./action-card";
import { I } from "./as-icons";
import { CatMark } from "./cat";
import type { ChatApi } from "./chat";
import { ChatMarkdown, useSmoothText, type ChatEntity } from "./markdown";
import { PlanCardView } from "./plan-card";
import { SecretCard } from "./secret-card";
import { Thinking, ToolGroup, toolWords } from "./tool-cards";

// The conversation as the board draws it: a date line between days, the person's words on the right in the dark bubble with the page and the time
// around it, the assistant's under its cat with the time, its working above the answer (the seconds it thought, one group of tool calls), then its
// words, the plan cards and the notes; a trouble is a card of its own. Logical properties only, so right-to-left languages mirror.

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

/** The assistant's own words, tools, cards or notes: what makes its bubble (a follow-up that did not run has none of them). */
const hasAnswer = (m: Msg) => !!m.text || m.tools.length > 0 || m.plans.length > 0 || m.remembered.length > 0;

const hasContent = (m: Msg) => m.role === ChatRole.USER || hasAnswer(m) || !!endText(m.end) || !!m.followUp?.outcome;

function dayKey(unix: number) {
  const d = new Date(unix * 1000);
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/** A thin line each side of a centred muted word: the date, or the way a turn ended (.as-day). */
export function Rule({ children, tone = "muted", role }: { children: ReactNode; tone?: "muted" | "danger"; role?: "status" | "alert" }) {
  return (
    <div role={role} className={cx("as-day", tone === "danger" && "bad")}>
      <span>{children}</span>
    </div>
  );
}

/** The assistant's round mark: its cat on the mint wash (.as-av). */
export function Avatar() {
  return (
    <span data-slot="avatar" aria-hidden className="as-av">
      <CatMark size={20} />
    </span>
  );
}

/** The assistant's name line over its words: the cat, the name, the time. */
function Header({ time, label }: { time: string; label?: ReactNode }) {
  const t = useT();
  return (
    <div className="as-ah">
      <Avatar />
      <b>{t("ai.chat.title")}</b>
      {label}
      {time && <span className="mono">{time}</span>}
    </div>
  );
}

/** The dots and what the assistant is doing, while it works (.as-thw). */
export function Typing({ label, avatar = true }: { label: ReactNode; avatar?: boolean }) {
  const t = useT();
  return (
    <div role="status" data-slot="typing" className="as-a">
      {avatar && (
        <div className="as-ah">
          <Avatar />
          <b>{t("ai.chat.title")}</b>
        </div>
      )}
      <div className="as-thw">
        <span aria-hidden className="as-th">
          <i />
          <i />
          <i />
        </span>
        {label}
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
      <code>{tool}</code>
      {after}
    </>
  );
}

/** "On a timer": a turn a follow-up started, not one the person asked for. */
export function TimerLabel() {
  const t = useT();
  return (
    <span data-slot="timer" className="as-tm">
      <I g="clock" />
      {t("ai.fu.label")}
    </span>
  );
}

/** The line a follow-up's turn starts from: not the person's words but the assistant's own note, and when it was set. */
function FollowUpLine({ m, tz }: { m: Msg; tz: number }) {
  const t = useT();
  const fmt = useFmt();
  const set = Number(m.followUp?.setUnix ?? 0);
  return (
    <div data-role="follow-up" className="as-uw fu">
      <span className="as-mt flex items-center gap-1.5">
        <TimerLabel />
        {set > 0 && <span>{t("ai.fu.line", { time: fmt.stampAt(set, tz) })}</span>}
      </span>
      <div className="as-u">{m.text}</div>
    </div>
  );
}

/** What a follow-up that did not run (or whose turn failed) leaves: for when it was, why, and what it was to do. */
function FollowUpNote({ m, tz, withNote }: { m: Msg; tz: number; withNote: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const fu = m.followUp!;
  const time = fmt.stampAt(Number(fu.dueUnix), tz);
  const key = followUpReasonKey(fu.reason);
  const reason = key ? t(key) : fu.reason;
  return (
    <div role="status" data-role="follow-up-note" className="as-fu">
      <TimerLabel />
      <span>{fu.outcome === "missed" ? t("ai.fu.missed", { time }) : t("ai.fu.failed", { time, reason })}</span>
      {withNote && fu.note && <span className="quote">{t("common.quoted", { name: fu.note })}</span>}
    </div>
  );
}

/** A follow-up that waits: when it comes back, in the panel's time zone and from now, with Cancel. */
function PendingFollowUp({ f, tz, onCancel }: { f: FollowUp; tz: number; onCancel: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const now = useNow(30_000);
  const due = Number(f.dueUnix);
  const coming = f.running || (due > 0 && due * 1000 <= now);
  const text = coming
    ? t("ai.fu.now")
    : due > 0
      ? t("ai.fu.pending", { time: fmt.stampAt(due, tz), left: minutesText(t, Math.ceil((due * 1000 - now) / 60_000)) })
      : t("ai.fu.afterPlan", { left: minutesText(t, Math.ceil(f.delayS / 60)) });
  return (
    <div role="status" data-role="follow-up-pending" className="as-fu">
      <I g="clock" />
      <span>{text}</span>
      {!coming && (
        <>
          <span aria-hidden>·</span>
          <button type="button" onClick={onCancel}>
            {t("ai.fu.cancel")}
          </button>
        </>
      )}
    </div>
  );
}

function Remembered({ note, onForget }: { note: Note; onForget: () => void }) {
  const t = useT();
  return (
    <div className="as-rm">
      <I g="bookmark" />
      <span>{t("ai.mem.saved", { text: note.text })}</span>
      <button type="button" onClick={onForget}>
        {t("ai.mem.undo")}
      </button>
    </div>
  );
}

function UserBubble({ m }: { m: Msg }) {
  const t = useT();
  const fmt = useFmt();
  return (
    <div data-role="user" className="as-uw">
      {/* a scheduled task's run (internal/panel/assistant/scheduled.go) names its task and when it ran, not a page */}
      {m.context?.label && <span className="as-mt">{m.context.kind === "task" ? t("ai.chat.scheduledRun", { label: m.context.label, time: fmt.stamp(m.createdUnix) }) : t("ai.chat.page", { label: m.context.label })}</span>}
      <div className="as-u">{m.text}</div>
      {m.createdUnix > 0 && <span className="as-mt mono">{fmt.clock(m.createdUnix)}</span>}
    </div>
  );
}

function AssistantBubble({ m, first, running, owner, chat, onNavigate }: { m: Msg; first: boolean; running: boolean; owner: boolean; chat: ChatApi; onNavigate: () => void }) {
  const { interim, answer } = splitText(m);
  const [shown, streamed] = useSmoothText(answer, running);
  const fmt = useFmt();
  const [busy, setBusy] = useState("");
  const live = m as LiveMsg;
  const navigate = useNavigate();
  // the navigation tools that passed the whitelist are cards of their own; a refused one stays in the tool group with its reason
  const actions = (m.tools as LiveTool[]).flatMap((x) => {
    const action = actionOf(x);
    return action ? [{ id: x.id, action }] : [];
  });
  const shownTools = (m.tools as LiveTool[]).filter((x) => !actions.some((a) => a.id === x.id));
  const entities = useNodeEntities();
  return (
    <div data-role="assistant" className="as-a">
      {(first || m.followUp) && <Header time={m.createdUnix > 0 ? fmt.clock(m.createdUnix) : ""} label={m.followUp ? <TimerLabel /> : undefined} />}
      {(interim || (running && shownTools.length > 0)) && <Thinking m={live} running={running} interim={interim} />}
      {shownTools.length > 0 && <ToolGroup tools={shownTools} running={running} workedMs={interim ? undefined : live.workedMs} />}
      {shown && (
        <div data-slot="bubble" className="max-w-full">
          <ChatMarkdown text={shown} onNavigate={onNavigate} entities={entities} words={streamed} />
        </div>
      )}
      {actions.map((a) => (a.action.kind === "secret" ? <SecretCard key={a.id} toolId={a.id} action={a.action} owner={owner} /> : <ActionCard key={a.id} toolId={a.id} action={a.action} onNavigate={onNavigate} />))}
      {m.plans.map((p) => (
        <PlanCardView
          key={p.planId}
          plan={p}
          owner={owner}
          busy={busy === p.planId}
          inline={owner && (p.state === PlanState.WAITING_OWNER || (p.state === PlanState.PROPOSED && p.inboxOnly)) ? <InlineApproval planId={p.planId} onDecided={() => chat.refresh?.()} /> : undefined}
          onOpenApproval={() => {
            void navigate({ to: "/ai", search: { tab: "approvals", plan: p.planId } } as never);
            onNavigate();
          }}
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
    </div>
  );
}

/** Errors the owner can mend in the AI section (a key, a budget, a provider): the card has the way there. */
const mendable = new Set(["ai.err.budget_reached", "ai.err.bad_key", "ai.err.no_balance", "ai.err.no_provider", "ai.err.assistant_off"]);

/** A trouble of the turn (.as-er): what happened, what to do about the budget, and the way to the settings for the owner. */
function Trouble({ error, owner, onNavigate }: { error: string; owner: boolean; onNavigate: () => void }) {
  const t = useT();
  const known = error.startsWith("ai.");
  return (
    <div role="alert" data-slot="error" className="as-er">
      <b>{known ? t(error as MessageKey) : error}</b>
      {error === "ai.err.budget_reached" && <span>{t("as.err.budget")}</span>}
      {owner && mendable.has(error) && (
        <Link to="/ai" onClick={onNavigate} className={buttonClass("secondary", "md")}>
          {t("as.err.go")}
        </Link>
      )}
    </div>
  );
}

export function Thread({ chat, owner, onNavigate }: { chat: ChatApi; owner: boolean; onNavigate: () => void }) {
  const t = useT();
  const fmt = useFmt();
  // follow-up times are the panel's, like the scheduled tasks'
  const tz = useQuery(accessQuery).data?.timezoneOffsetMinutes ?? 0;
  const all = chat.messages;
  const last = all[all.length - 1];
  const shown = all.filter(hasContent);
  const openTool = last?.role === ChatRole.ASSISTANT ? last.tools.find((x) => !x.done) : undefined;
  const typing = chat.running && !!last && (last.role === ChatRole.USER || !splitText(last).answer || !!openTool);
  const lastShown = shown[shown.length - 1];

  const out: ReactNode[] = [];
  shown.forEach((m, i) => {
    const prev = shown[i - 1];
    const day = m.createdUnix > 0 ? dayKey(m.createdUnix) : "";
    const newDay = day !== "" && (!prev || prev.createdUnix <= 0 || dayKey(prev.createdUnix) !== day);
    if (newDay) out.push(<Rule key={`day-${m.id}`}>{dayLabel(t, fmt, m.createdUnix)}</Rule>);
    const first = !prev || prev.role !== m.role || newDay;
    if (m.role === ChatRole.USER) {
      out.push(m.followUp ? <FollowUpLine key={m.id} m={m} tz={tz} /> : <UserBubble key={m.id} m={m} />);
    } else {
      const isTail = m === lastShown;
      // a follow-up that did not run has no bubble, only its note
      const silent = !!m.followUp?.outcome && !hasAnswer(m);
      if (!silent) out.push(<AssistantBubble key={m.id} m={m} first={first} running={chat.running && isTail} owner={owner} chat={chat} onNavigate={onNavigate} />);
      const end = endText(m.end);
      if (end) out.push(<Rule key={`end-${m.id}`} role="status">{t(end)}</Rule>);
      if (m.followUp?.outcome) out.push(<FollowUpNote key={`fu-${m.id}`} m={m} tz={tz} withNote={silent} />);
    }
  });

  return (
    <>
      {out}
      {(chat.followUps ?? []).map((f) => (
        <PendingFollowUp key={f.id} f={f} tz={tz} onCancel={() => void chat.cancelFollowUp?.(f.id)} />
      ))}
      {typing && <Typing avatar={lastShown?.role !== ChatRole.ASSISTANT} label={openTool ? <CheckingLabel tool={openTool.tool} /> : t("ai.chat.typing")} />}
      {chat.error && <Trouble error={chat.error} owner={owner} onNavigate={onNavigate} />}
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
