import { Link } from "@tanstack/react-router";
import { Card, PageTitle } from "@/components/ui/bits";
import { Icon, NavIcon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { useIsOwner } from "@/lib/updates";
import { TelegramCard } from "./telegram";

/**
 * Integrations: the Telegram alerts (every admin links their own chat). MCP, the API tokens and the approval inbox moved to
 * the AI section (ADR 0014); the owner gets a line that says so, with the way there.
 */
export function IntegrationsScreen() {
  const t = useT();
  const owner = useIsOwner();
  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-[3px]">
        <PageTitle>{t("int.title")}</PageTitle>
        <span className="text-xs text-muted">{t("int.sub")}</span>
      </div>
      {owner && (
        <Card lg className="flex flex-wrap items-center gap-3 p-4">
          <span className="grid size-9 flex-none place-items-center rounded-xl bg-accent-soft">
            <NavIcon name="ai" size={18} />
          </span>
          <span className="min-w-[14rem] flex-1 text-[13px] leading-snug text-pretty">{t("int.moved")}</span>
          <Link to="/ai" search={{ tab: "mcp" } as never} className="inline-flex items-center gap-1 text-[13px] font-bold text-accent-text">
            {t("int.moved.go")}
            <Icon name="chevronRight" size={14} className="rtl:-scale-x-100" />
          </Link>
        </Card>
      )}
      <TelegramCard owner={owner} />
    </div>
  );
}
