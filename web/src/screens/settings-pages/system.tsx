import { PageTitle, SectionLabel } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useT, useLang } from "@/i18n";
import { useFmt } from "@/lib/format";
import { useIsOwner, useUpdateActions, useUpdates, type Updates } from "@/lib/updates";
import { PanelCard } from "@/screens/updates/cards";

const card = "flex min-w-0 flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]";

export function SystemPage() {
  const t = useT();
  const q = useUpdates();
  const owner = useIsOwner();
  const actions = useUpdateActions();

  return (
    <div className="flex flex-col gap-3.5">
      <PageTitle>{t("settings.system")}</PageTitle>
      {q.data ? (
        <PanelCard data={q.data} owner={owner} actions={actions} />
      ) : q.isError ? (
        <QueryError error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <Pending />
      )}
      <AboutCard panel={q.data?.panel} />
    </div>
  );
}

function AboutCard({ panel }: { panel: Updates["panel"] }) {
  const t = useT();
  const lang = useLang();
  const fmt = useFmt();
  const docs = lang === "ru" ? "https://mistgate.app/ru/" : "https://mistgate.app/";

  return (
    <section className={card}>
      <SectionLabel as="h2" icon="info" tone="sky">
        {t("about.title")}
      </SectionLabel>
      <p className="max-w-2xl text-[13px] leading-relaxed text-pretty text-muted">{t("about.body")}</p>
      <dl className="grid gap-2.5 rounded-field border border-line bg-canvas p-3.5 sm:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1">
          <dt className="text-xs text-muted">{t("about.version")}</dt>
          <dd className="font-mono text-sm font-semibold text-fg">{panel?.version || "—"}</dd>
        </div>
        <div className="flex min-w-0 flex-col gap-1">
          <dt className="text-xs text-muted">{t("about.built")}</dt>
          <dd className="text-sm font-semibold text-fg">{panel?.built ? fmt.dateTime(panel.built) : "—"}</dd>
        </div>
      </dl>
      <div className="flex flex-wrap gap-2 border-t border-line pt-3">
        <a className="inline-flex h-9 cursor-pointer items-center rounded-ctl border border-line bg-surface-2 px-3 text-[13px] font-semibold text-fg transition-colors duration-200 hover:bg-canvas focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" href={docs}>
          {t("about.docs")}
        </a>
        <a className="inline-flex h-9 cursor-pointer items-center rounded-ctl border border-line bg-surface-2 px-3 text-[13px] font-semibold text-fg transition-colors duration-200 hover:bg-canvas focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" href="https://github.com/Mistgate/mistgate" target="_blank" rel="noreferrer">
          {t("about.source")}
        </a>
        <a className="inline-flex h-9 cursor-pointer items-center rounded-ctl border border-line bg-surface-2 px-3 text-[13px] font-semibold text-fg transition-colors duration-200 hover:bg-canvas focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent" href="https://github.com/Mistgate/mistgate/blob/main/LICENSE" target="_blank" rel="noreferrer">
          {t("about.license")}
        </a>
      </div>
    </section>
  );
}
