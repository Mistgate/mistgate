import { useLayoutEffect, useRef, useState } from "react";
import { Icon } from "@/components/ui/icons";
import { Tip } from "@/components/ui/tooltip";
import { useT } from "@/i18n";
import { looksSecret, type Context } from "@/lib/assistant";
import { AgentPicker, type Pick } from "./agent-picker";
import { I } from "./as-icons";
import { Ghost, suggest } from "./ghost";

/**
 * The message box (the board's .as-in): the page the message goes with as a chip on top, the text (grows to about six lines, then scrolls
 * softly), and under it the agent picker and the send button, which is the stop button while a turn runs. A "+" brings the page back after it
 * was let go. Enter sends, Shift+Enter breaks the line. A question started like an earlier one (`suggestions`) is finished in a dim colour;
 * Tab, or → at the end, takes it. With `off` (no provider) the box is closed and says what to do first.
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
  pickerOpen,
  suggestions,
  off,
  placeholder,
  focusOnMount,
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
  pickerOpen?: boolean;
  suggestions?: readonly string[];
  off?: boolean;
  placeholder?: string;
  focusOnMount?: boolean;
}) {
  const t = useT();
  const area = useRef<HTMLTextAreaElement>(null);
  const mirror = useRef<HTMLDivElement>(null);
  // the suggestion just taken, while its letters land: the box's own text is hidden under them for that moment
  const [taken, setTaken] = useState<{ base: string; rest: string } | null>(null);
  const landing = taken && taken.base + taken.rest === text ? taken : null;
  const rest = landing || running || off ? "" : suggest(text, suggestions ?? []);
  const ghost = landing ?? (rest ? { base: text, rest } : null);
  const accept = () => {
    if (!rest) return;
    setTaken({ base: text, rest });
    onText(text + rest);
    area.current?.focus();
  };

  // the window opens with the cursor in the box (the board focuses it): not on a phone, where that would raise the keyboard over the answer
  useLayoutEffect(() => {
    if (focusOnMount && !off && typeof matchMedia === "function" && !matchMedia("(max-width: 720px)").matches) area.current?.focus({ preventScroll: true });
  }, [focusOnMount, off]);

  // grow with the text up to six lines; past that the box scrolls with a soft fade at the edge that has more
  useLayoutEffect(() => {
    const el = area.current;
    if (!el) return;
    el.style.height = "auto";
    el.dataset.empty = text ? "0" : "1";
    el.dataset.ghost = ghost?.rest ? "1" : "0";
    const max = 6 * 20 + 8;
    // the suggestion may wrap onto a line the typed text has not reached
    el.style.height = `${Math.min(Math.max(el.scrollHeight, mirror.current?.scrollHeight ?? 0), max)}px`;
    el.dataset.top = el.scrollTop > 1 ? "1" : "0";
    el.dataset.bottom = el.scrollTop + el.clientHeight < el.scrollHeight - 1 ? "1" : "0";
  }, [text, ghost?.rest]);

  return (
    <div className="as-in">
      {context && attached && (
        <div className="as-cr">
          <span className="as-tag as-cx">
            <I g="link" />
            <span>{t("ai.chat.page", { label: context.label })}</span>
            <button type="button" aria-label={t("ai.chat.unpage")} title={t("ai.chat.unpage")} onClick={() => onAttach(false)}>
              <I g="x" />
            </button>
          </span>
        </div>
      )}
      <div className="as-tw">
        {ghost && <Ghost base={ghost.base} rest={ghost.rest} accepted={!!landing} onAccept={accept} onDone={() => setTaken(null)} label={t("ai.chat.accept")} className="as-gh" />}
        <textarea
          ref={area}
          aria-label={placeholder ?? t("as.ph")}
          placeholder={placeholder ?? t("as.ph")}
          aria-disabled={off || undefined}
          readOnly={off}
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
            } else if (rest && (e.key === "Tab" || e.key === "ArrowRight") && !e.shiftKey && !e.altKey && !e.ctrlKey && !e.metaKey && e.currentTarget.selectionStart === text.length) {
              e.preventDefault();
              accept();
            }
          }}
          className={`as-ti scroll-soft ${landing ? "[-webkit-text-fill-color:transparent]" : ""}`}
        />
      </div>
      {looksSecret(text) && (
        <p role="status" className="as-sw">
          <Icon name="lock" size={12} className="mt-0.5 flex-none" />
          <span>{t("ai.secret.warning")}</span>
        </p>
      )}
      <div className="as-ir">
        <div className="flex min-w-0 items-center gap-2">
          {context && !attached && (
            <Tip label={t("ai.chat.attach")}>
              <button type="button" aria-label={t("ai.chat.attach")} onClick={() => onAttach(true)} className="as-ad">
                <I g="plus" />
              </button>
            </Tip>
          )}
          <AgentPicker pick={pick} owner={owner} onNavigate={onNavigate} defaultOpen={pickerOpen} disabled={off} />
        </div>
        {running ? (
          <button type="button" className="as-go stop" aria-label={t("ai.chat.stop")} title={t("ai.chat.stop")} onClick={onStop}>
            <i aria-hidden />
          </button>
        ) : (
          <button type="button" className="as-go" aria-label={t("ai.chat.send")} title={t("ai.chat.send")} disabled={off || !text.trim()} onClick={onSubmit}>
            <I g="send" />
          </button>
        )}
      </div>
    </div>
  );
}
