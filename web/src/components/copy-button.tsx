import { useState } from "react";
import { Button, type ButtonVariant } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { plainText, useT } from "@/i18n";
import { cx } from "@/lib/cx";

/**
 * Copies `value` to the clipboard (the board's .cpb). Both words ("Copy" and "Copied") are always in the button, stacked in
 * one grid cell (the one that is not showing as a pseudo-element, so the button's text is the shown word only), so it keeps
 * the width of the longer one and the swap never moves the layout; the idle word slides up and out
 * while the other rises in, the icon turns into the tick (which draws itself), all on the board's springs. A framed button
 * also takes the accent frame and tint while it says "Copied" (two seconds). If the browser refuses (insecure origin, blocked
 * permission) a toast asks for a manual copy instead of failing silently. `variant` and `full` make it the main action of a
 * window (the install command).
 */
export function CopyButton({
  value,
  label,
  size = "sm",
  variant = "secondary",
  full,
  iconOnly,
  onFail,
}: {
  value: string;
  label?: string;
  size?: "sm" | "md" | "lg";
  variant?: ButtonVariant;
  full?: boolean;
  /** Only the glyph (36 wide, the board's .btn.sm.ic2.cpb next to a value): `label` is its name, and it says "Copied" while it does. */
  iconOnly?: boolean;
  /** The browser refused: the caller can select the text for the person to copy by hand. */
  onFail?: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(plainText(value));
      setDone(true);
      setTimeout(() => setDone(false), 2000);
    } catch {
      onFail?.();
      toast(t("common.copyFailed"));
    }
  }

  const idle = label ?? t("common.copy");
  const name = done ? t("common.copied") : idle;
  const icon = "absolute inset-0 size-4 transition-[scale,rotate,opacity] duration-(--sp-pop-t) ease-(--sp-pop)";
  const word = "col-start-1 row-start-1 whitespace-nowrap transition-[translate,opacity] duration-(--sp-settle-t) ease-(--sp-settle)";
  return (
    <Button
      variant={variant}
      size={size}
      full={full}
      onClick={copy}
      data-copied={done ? "" : undefined}
      {...(iconOnly ? { "aria-label": name, title: name } : {})}
      className={cx(
        iconOnly && "w-9 px-0",
        // these win over the button's own quick transition and press (the board's .btn.cpb)
        "transition-[border-color,color,background-color,transform]! duration-(--sp-fade-t)! ease-(--sp-fade)! active:scale-[0.97]!",
        (variant === "secondary" || variant === "outline") && "data-copied:border-accent-line data-copied:bg-[color-mix(in_srgb,var(--accent-mark)_8%,transparent)] data-copied:text-accent-text",
      )}
    >
      <span aria-hidden className="relative size-4 flex-none">
        <Icon name="copy" size={16} className={cx(icon, done && "scale-30 rotate-[30deg] opacity-0")} />
        <svg
          width="16"
          height="16"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2.6"
          strokeLinecap="round"
          strokeLinejoin="round"
          className={cx(icon, !done && "scale-30 -rotate-[30deg] opacity-0")}
        >
          <path d="M5 12.5l4.5 4.5L19 7.5" strokeDasharray="24" strokeDashoffset={done ? 0 : 24} style={{ transition: "stroke-dashoffset var(--sp-settle-t) var(--sp-settle)" }} />
        </svg>
      </span>
      {!iconOnly && (
        <span className="grid text-start [clip-path:inset(-3px_-2px)]">
          {/* the word that is not showing is drawn by its ::before (not text of the button): it holds the width, and a reader or a test sees one word */}
          <span data-t={idle} aria-hidden={done || undefined} className={cx(word, done ? "-translate-y-[90%] opacity-0 before:content-[attr(data-t)]" : undefined)}>
            {done ? null : idle}
          </span>
          <span data-t={t("common.copied")} aria-hidden={!done || undefined} className={cx(word, done ? undefined : "translate-y-[90%] opacity-0 before:content-[attr(data-t)]")}>
            {done ? t("common.copied") : null}
          </span>
        </span>
      )}
    </Button>
  );
}
