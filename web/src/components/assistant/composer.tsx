import { useLayoutEffect, useRef } from "react";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import type { Context } from "@/lib/assistant";
import { AgentPicker, type Pick } from "./agent-picker";

/**
 * The message box: the page the message goes with as a chip on top, the text (grows to about six lines, then scrolls softly),
 * and under it "+" (attach the page again), the agent picker and the send button, which is the stop button while a turn runs.
 * Enter sends, Shift+Enter breaks the line.
 */
export function Composer({
  text,
  onText,
  onSubmit,
  running,
  onStop,
  context,
  attached,
  onAttach,
  pick,
  owner,
  onNavigate,
}: {
  text: string;
  onText: (v: string) => void;
  onSubmit: () => void;
  running: boolean;
  onStop: () => void;
  context: Context | null;
  attached: boolean;
  onAttach: (on: boolean) => void;
  pick: Pick;
  owner: boolean;
  onNavigate?: () => void;
}) {
  const t = useT();
  const area = useRef<HTMLTextAreaElement>(null);

  // grow with the text up to six lines; past that the box scrolls with a soft fade at the edge that has more
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.dataset.empty = text ? "0" : "1";
    const max = 6 * 20;
    el.style.height = `${Math.min(el.scrollHeight, max)}px`;
    el.dataset.top = el.scrollTop > 1 ? "1" : "0";
    el.dataset.bottom = el.scrollTop + el.clientHeight < el.scrollHeight - 1 ? "1" : "0";
  }, [text]);

  return (
    <div className="flex flex-col gap-1.5 rounded-[18px] border border-line bg-canvas px-3 pt-2.5 pb-2 transition-colors focus-within:border-accent-line">
      {context && attached && (
        <div className="flex">
          <span className="inline-flex max-w-full items-center gap-1.5 rounded-lg border border-line bg-surface-2 py-1 ps-2 pe-1 text-[11.5px] font-medium text-muted">
            <Icon name="link" size={12} className="flex-none" />
            <span className="truncate">{t("ai.chat.page", { label: context.label })}</span>
            <button type="button" aria-label={t("ai.chat.unpage")} title={t("ai.chat.unpage")} onClick={() => onAttach(false)} className="grid size-4 flex-none place-items-center rounded-md hover:bg-surface hover:text-fg">
              <Icon name="x" size={10} />
            </button>
          </span>
        </div>
      )}
      <textarea
        ref={area}
        aria-label={t("ai.chat.ph")}
        placeholder={t("ai.chat.ph")}
        value={text}
        rows={1}
        onChange={(e) => onText(e.target.value)}
        onScroll={(e) => {
          const el = e.currentTarget;
          el.dataset.top = el.scrollTop > 1 ? "1" : "0";
          el.dataset.bottom = el.scrollTop + el.clientHeight < el.scrollHeight - 1 ? "1" : "0";
        }}
        onKeyDown={(e) => {
          if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            onSubmit();
          }
        }}
        className="scroll-soft block min-h-5 w-full resize-none bg-transparent px-1 text-[13.5px] leading-5 outline-none placeholder:text-muted"
      />
      <div className="flex items-center gap-1">
        {context && !attached && (
          <Tip label={t("ai.chat.attach")}>
            <button type="button" aria-label={t("ai.chat.attach")} onClick={() => onAttach(true)} className="grid size-8 flex-none place-items-center rounded-[10px] border border-line text-muted transition-colors hover:bg-surface-2 hover:text-fg">
              <Icon name="plus" size={14} />
            </button>
          </Tip>
        )}
        <div className="flex min-w-0 flex-1">
          <AgentPicker pick={pick} owner={owner} onNavigate={onNavigate} />
        </div>
        {running ? (
          <button type="button" onClick={onStop} aria-label={t("ai.chat.stop")} title={t("ai.chat.stop")} className="grid size-8 flex-none place-items-center rounded-full bg-fg text-canvas transition-transform active:scale-95">
            <Icon name="stop" size={14} fill="currentColor" strokeWidth={1.5} />
          </button>
        ) : (
          <button
            type="button"
            disabled={!text.trim()}
            onClick={onSubmit}
            aria-label={t("ai.chat.send")}
            title={t("ai.chat.send")}
            className="grid size-8 flex-none place-items-center rounded-full bg-fg text-canvas transition-all active:scale-95 disabled:bg-surface-2 disabled:text-faint"
          >
            <Icon name="arrowUp" size={15} />
          </button>
        )}
      </div>
    </div>
  );
}
