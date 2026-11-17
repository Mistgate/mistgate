import { useState } from "react";
import { Button, type ButtonVariant } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { plainText, useT } from "@/i18n";

/**
 * Copies `value` to the clipboard. The button says "Copied" for two seconds; if the browser refuses
 * (insecure origin, blocked permission) a toast asks for a manual copy instead of failing silently.
 * `variant` and `full` make it the main action of a window (the install command).
 */
export function CopyButton({
  value,
  label,
  size = "sm",
  variant = "secondary",
  full,
  onFail,
}: {
  value: string;
  label?: string;
  size?: "sm" | "md" | "lg";
  variant?: ButtonVariant;
  full?: boolean;
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
  const now = done ? t("common.copied") : idle;
  // the icon turns into the tick and the words swap with a short rise; the button keeps the width of the longer word
  // (the other word is a hidden ::after sizer, not text of the button)
  return (
    <Button variant={variant} size={size} full={full} onClick={copy}>
      <Icon key={done ? "ok" : "copy"} name={done ? "check" : "copy"} size={16} strokeWidth={done ? 2.6 : 1.5} className="animate-[mg-ui-pop_0.35s_var(--e)_both]" />
      <span
        key={done ? "ok" : "copy"}
        data-w={done ? idle : t("common.copied")}
        className="animate-[mg-up_0.3s_var(--e)_both] after:invisible after:block after:h-0 after:overflow-hidden after:content-[attr(data-w)]"
      >
        {now}
      </span>
    </Button>
  );
}
