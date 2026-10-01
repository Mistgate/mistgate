import { useState } from "react";
import { Button, type ButtonVariant } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";

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
}: {
  value: string;
  label?: string;
  size?: "sm" | "md" | "lg";
  variant?: ButtonVariant;
  full?: boolean;
}) {
  const t = useT();
  const toast = useToast();
  const [done, setDone] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setDone(true);
      setTimeout(() => setDone(false), 2000);
    } catch {
      toast(t("common.copyFailed"));
    }
  }

  return (
    <Button variant={variant} size={size} full={full} onClick={copy}>
      <Icon name={done ? "check" : "copy"} size={size === "sm" ? 12 : 14} />
      {done ? t("common.copied") : (label ?? t("common.copy"))}
    </Button>
  );
}
