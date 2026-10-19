import { useQuery } from "@tanstack/react-query";
import { Card, PageTitle } from "@/components/ui/bits";
import { NavIcon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { QueryError } from "@/components/ui/query-error";
import { useT } from "@/i18n";
import { approvalsQuery } from "@/lib/integrations";
import { useIsOwner } from "@/lib/updates";
import { ApprovalHistory, ApprovalQueue } from "./approvals";
import { McpCard } from "./mcp";
import { TelegramCard } from "./telegram";
import { TokensCard } from "./tokens";

/**
 * Integrations: API tokens (scripts and MCP share one model), how to connect an agent over MCP, the Telegram alerts and the
 * owner's inbox of what an agent planned that needs a human. Tokens and approvals are owner-only on the server; anyone else
 * gets a calm line, and their own Telegram link (every admin links their own chat).
 */
export function IntegrationsScreen() {
  const t = useT();
  const owner = useIsOwner();
  const q = useQuery({ ...approvalsQuery, enabled: owner });
  const waiting = q.data?.awaiting ?? 0;

  const header = (
    <div className="flex flex-col gap-[3px]">
      <PageTitle>{t("int.title")}</PageTitle>
      <span className="text-xs text-muted">{waiting > 0 ? t.n("int.sub.awaiting", waiting) : t("int.sub")}</span>
    </div>
  );

  if (!owner) {
    return (
      <div className="flex flex-col gap-3.5">
        {header}
        <TelegramCard owner={false} />
        <Card lg className="border-dashed">
          <EmptyState icon={<NavIcon name="integrations" size={20} />} title={t("int.ownerOnly.title")}>
            {t("int.ownerOnly.text")}
          </EmptyState>
        </Card>
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-3.5">
      {header}
      {q.isError && !q.data && <QueryError error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && <ApprovalQueue data={q.data} />}
      <div className="grid items-start gap-3.5 xl:grid-cols-2">
        <TokensCard />
        <McpCard />
      </div>
      <TelegramCard owner />
      {q.data && <ApprovalHistory data={q.data} />}
    </div>
  );
}
