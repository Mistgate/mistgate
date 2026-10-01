import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { PageTitle } from "@/components/ui/bits";
import { Tabs, type TabItem } from "@/components/ui/tabs";
import { useTx } from "@/screens/users/t";
import { Pending, QueryError } from "@/components/ui/query-error";
import { DnsTab } from "./dns";
import { FormatsTab } from "./formats";
import { PageTab } from "./page";
import { settingsQuery } from "./queries";
import { RulesTab } from "./rules";
import { subsTabs, type SubsTab } from "./tabs";
import { TextsTab } from "./texts";

/** /subscriptions: formats and clients, serving rules, headers and texts, the user page and the DNS presets. */
export function SubscriptionsScreen() {
  const t = useTx();
  const navigate = useNavigate();
  const tab = (useSearch({ strict: false }) as { tab?: SubsTab }).tab ?? "formats";
  const q = useQuery(settingsQuery);

  const go = (next: string) => void navigate({ to: "/subscriptions", search: next === "formats" ? {} : { tab: next as SubsTab }, replace: true } as never);

  const body = (content: (s: NonNullable<typeof q.data>) => React.ReactNode) => (q.isPending ? <Pending /> : q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : content(q.data));

  const items: TabItem[] = subsTabs.map((id) => ({
    value: id,
    label: t(`subs.tab.${id}`),
    content:
      id === "dns"
        ? <DnsTab />
        : body((d) =>
            id === "formats" ? <FormatsTab go={go} /> : id === "rules" ? <RulesTab settings={d.settings} /> : id === "texts" ? <TextsTab data={d} /> : <PageTab settings={d.settings} />,
          ),
  }));

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex flex-col gap-1">
        <PageTitle>{t("subs.title")}</PageTitle>
        <span className="text-xs text-muted">{t("subs.sub")}</span>
      </div>
      <Tabs aria-label={t("subs.tabs")} value={tab} onValueChange={go} items={items} />
    </div>
  );
}
