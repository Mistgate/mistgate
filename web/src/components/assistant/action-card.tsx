import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { navKey } from "@/components/nav";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { markAction, readMarks, type OpenAction } from "@/lib/ui-actions";
import { I } from "./as-icons";

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
    <div data-slot="action" data-state={state ?? "ready"} className="as-cd">
      <div className="as-plh">
        <span>
          <I g="open" />
          {t("ai.ui.open")}
        </span>
      </div>
      <b>{title}</b>
      {action.id && <span className="as-sub">{action.id}</span>}
      {state ? (
        <div className="as-ps">
          <I g={done ? "check" : "info"} />
          <span>{done ? t("ai.ui.opened") : t("ai.plan.dismissed")}</span>
        </div>
      ) : (
        <div className="as-pa">
          <Button
            type="button"
            size="md"
            onClick={() => {
              markAction(toolId, "dismissed");
              setState("dismissed");
            }}
          >
            {t("ai.plan.dismiss")}
          </Button>
          <Button type="button" size="md" variant="primary" onClick={run}>
            {t("ai.ui.open")}
          </Button>
        </div>
      )}
    </div>
  );
}
