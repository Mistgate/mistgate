import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Card, PageTitle } from "@/components/ui/bits";
import { NavIcon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { QueryError } from "@/components/ui/query-error";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { EditionNotice } from "@/components/edition-notice";
import { useT } from "@/i18n";
import { useEdition } from "@/lib/edition";
import { approvalsQuery } from "@/lib/integrations";
import { useIsOwner } from "@/lib/updates";
import { ApprovalHistory, ApprovalQueue } from "@/screens/integrations/approvals";
import { McpCard } from "@/screens/integrations/mcp";
import { TokensCard } from "@/screens/integrations/tokens";
import { MemoryTab } from "./memory";
import { AssistantTab } from "./settings";
import type { AiTab } from "./tabs";

/**
 * /ai (ADR 0014): the assistant (its providers and settings, the owner's), what it remembers and the conversations (each
 * admin's own), MCP with the API tokens for outside agents, and the changes waiting for the owner's decision.
 */
export function AiScreen() {
  const t = useT();
  const owner = useIsOwner();
  const edition = useEdition();
  const navigate = useNavigate();
  const tab = (useSearch({ strict: false }) as { tab?: AiTab }).tab ?? (owner ? "assistant" : "memory");
  const approvals = useQuery({ ...approvalsQuery, enabled: owner });
  const waiting = approvals.data?.awaiting ?? 0;
  const go = (next: string) => void navigate({ to: "/ai", search: next === "assistant" ? {} : { tab: next as AiTab }, replace: true } as never);

  const ownerOnly = (
    <Card lg className="border-dashed">
      <EmptyState icon={<NavIcon name="ai" size={20} />} title={t("ai.ownerOnly")} />
    </Card>
  );

  // the Cloudflare edition has no assistant yet (it comes with MCP, in a Worker of its own): its tabs say so
  const noAssistant = <EditionNotice feature="assistant" />;
  const items: TabItem[] = [
    { value: "assistant", label: t("ai.tab.assistant"), content: edition.off("assistant") ? noAssistant : owner ? <AssistantTab /> : ownerOnly },
    { value: "memory", label: t("ai.tab.memory"), content: edition.off("assistant") ? noAssistant : <MemoryTab /> },
    {
      value: "mcp",
      label: t("ai.tab.mcp"),
      content: owner ? (
        <div className="grid items-start gap-3.5 xl:grid-cols-2">
          <TokensCard />
          <McpCard />
        </div>
      ) : (
        ownerOnly
      ),
    },
    {
      value: "approvals",
      label: t("ai.tab.approvals"),
      badge: waiting || undefined,
      content: owner ? (
        <div className="flex flex-col gap-3.5">
          {approvals.isError && !approvals.data && <QueryError error={approvals.error} onRetry={() => void approvals.refetch()} />}
          {approvals.data && <ApprovalQueue data={approvals.data} />}
          {approvals.data && <ApprovalHistory data={approvals.data} />}
        </div>
      ) : (
        ownerOnly
      ),
    },
  ];

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-1">
        <PageTitle>{t("ai.title")}</PageTitle>
        <span className="text-xs text-muted">{waiting > 0 ? t.n("int.sub.awaiting", waiting) : t("ai.sub")}</span>
      </div>
      <Tabs aria-label={t("ai.title")} value={tab} onValueChange={go} items={items} />
    </div>
  );
}
