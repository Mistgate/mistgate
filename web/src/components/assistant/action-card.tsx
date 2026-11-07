import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { navKey } from "@/components/nav";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { markAction, readMarks, type OpenAction } from "@/lib/ui-actions";

// The assistant's ui_open as a card in the thread: which page will open and one button. The card navigates nothing by itself.
// Whether it was used or dismissed is this browser's own note (the card itself is the persisted tool call).

const sectionOf = (page: string) => (page.startsWith("node") ? "nodes" : page.startsWith("user") || page === "groups" ? "users" : page.startsWith("profile") ? "profiles" : page.startsWith("health") ? "health" : page.startsWith("settings") ? "settings" : page.startsWith("ai") ? "ai" : page);

export function ActionCard({ toolId, action, onNavigate }: { toolId: string; action: OpenAction; onNavigate?: () => void }) {
  const t = useT();
  const navigate = useNavigate();
  const [state, setState] = useState<"used" | "dismissed" | undefined>(() => readMarks()[toolId]);

  const run = () => {
    void navigate(action.target as never);
    markAction(toolId, "used");
    setState("used");
    onNavigate?.();
  };

  const title = action.page.startsWith("settings_") ? t(`settings.${action.page.slice(9)}` as MessageKey) : t(navKey(sectionOf(action.page) as never));
  const done = state === "used";

  return (
    <div data-slot="action" data-state={state ?? "ready"} className="chat-in flex w-full flex-col gap-3 rounded-2xl bg-fg/[0.04] p-4 ring-1 ring-line/70">
      <div className="flex items-start gap-3">
        <span aria-hidden className="grid size-8 flex-none place-items-center rounded-[10px] bg-accent-soft text-accent-text">
          <Icon name="externalLink" size={15} />
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-[11px] font-medium text-muted">{t("ai.ui.open")}</span>
          <b className="text-[14px] leading-snug font-semibold">{title}</b>
          {action.id && <span className="font-mono text-xs text-muted">{action.id}</span>}
        </div>
      </div>
      {state ? (
        <p className={cx("flex items-center gap-2 text-[13px]", done ? "text-ok-text" : "text-muted")}>
          <Icon name={done ? "check" : "info"} size={14} className="flex-none" />
          {done ? t("ai.ui.opened") : t("ai.plan.dismissed")}
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-2 pt-0.5">
          <Button type="button" size="sm" variant="primary" onClick={run}>
            {t("ai.ui.open")}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            onClick={() => {
              markAction(toolId, "dismissed");
              setState("dismissed");
            }}
          >
            {t("ai.plan.dismiss")}
          </Button>
        </div>
      )}
    </div>
  );
}
