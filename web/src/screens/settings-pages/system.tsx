import { Chip, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useT, useLang } from "@/i18n";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { updateTimezoneName, useIsOwner, useUpdateActions, useUpdates, type Updates } from "@/lib/updates";
import { PanelCard } from "@/screens/updates/cards";
import { PanelResourcesCard } from "./panel-resources";
import { OpsCards } from "./system-ops";

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
        <>
          <PanelCard data={q.data} owner={owner} actions={actions} />
          <UpdateTimezoneCard data={q.data} owner={owner} busy={actions.busy} onChange={actions.setUpdateTimezone} />
          <UpdateDownloadTimeoutCard data={q.data} owner={owner} busy={actions.busy} onChange={actions.setDownloadTimeout} />
          <UpdateDownloadSourcesCard data={q.data} owner={owner} busy={actions.busy} onChange={actions.setDownloadSources} />
        </>
      ) : q.isError ? (
        <QueryError error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <Pending />
      )}
      <PanelResourcesCard />
      <OpsCards />
      <AboutCard panel={q.data?.panel} />
    </div>
  );
}

const updateTimezoneOffsets = [-720, -660, -600, -570, -540, -480, -420, -360, -300, -270, -240, -210, -180, -120, -60, 0, 60, 120, 180, 210, 240, 270, 300, 330, 345, 360, 390, 420, 480, 525, 540, 570, 600, 630, 660, 690, 720, 765, 780, 840];

function UpdateTimezoneCard({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (offset: number) => Promise<boolean> }) {
  const t = useT();
  const current = data.scheduleTimezoneOffsetMinutes;
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="clock" tone="lavender">
        {t("up.timezone.title")}
      </SectionLabel>
      <p className="max-w-2xl text-[13px] leading-relaxed text-pretty text-muted">{t("up.timezone.body")}</p>
      {owner ? (
        <label className="flex max-w-sm flex-col gap-1.5 text-xs font-semibold">
          {t("up.timezone.label")}
          <select
            value={current}
            disabled={busy}
            onChange={(event) => void onChange(Number(event.target.value))}
            className="h-10 rounded-ctl border border-line bg-surface px-3 text-sm text-fg disabled:opacity-60"
          >
            {updateTimezoneOffsets.map((offset) => (
              <option key={offset} value={offset}>
                {updateTimezoneName(offset)}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <p className="font-mono text-sm font-semibold">{updateTimezoneName(current)}</p>
      )}
    </section>
  );
}

export function UpdateDownloadTimeoutCard({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (seconds: number) => Promise<boolean> }) {
  const t = useT();
  const current = data.updateDownloadTimeoutSeconds || 1800;
  const options = [300, 600, 1800, 3600, 7200];
  if (!options.includes(current)) options.push(current);
  options.sort((a, b) => a - b);
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="refresh" tone="sky">
        {t("up.downloadTimeout")}
      </SectionLabel>
      <p className="max-w-2xl text-[13px] leading-relaxed text-pretty text-muted">{t("up.downloadTimeout.body")}</p>
      {owner ? (
        <label className="flex max-w-sm flex-col gap-1.5 text-xs font-semibold">
          {t("up.downloadTimeout.label")}
          <select
            value={current}
            disabled={busy}
            onChange={(event) => void onChange(Number(event.target.value))}
            className="h-10 rounded-ctl border border-line bg-surface px-3 text-sm text-fg disabled:opacity-60"
          >
            {options.map((seconds) => <option key={seconds} value={seconds}>{t(seconds < 3600 ? "up.downloadTimeout.minutes" : "up.downloadTimeout.hours", { n: seconds < 3600 ? seconds / 60 : seconds / 3600 })}</option>)}
          </select>
        </label>
      ) : (
        <p className="font-mono text-sm font-semibold">{t(current < 3600 ? "up.downloadTimeout.minutes" : "up.downloadTimeout.hours", { n: current < 3600 ? current / 60 : current / 3600 })}</p>
      )}
    </section>
  );
}

/**
 * Where a node may fetch the update file besides the panel. The panel is always first and has no switch; GitHub is the one
 * optional fallback. The Cloudflare edition has no such setting (the call answers UNIMPLEMENTED), so the card is not drawn there.
 */
export function UpdateDownloadSourcesCard({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (sources: string[]) => Promise<boolean> }) {
  const t = useT();
  const edge = useEdition().edge;
  if (edge) return null;
  const github = data.updateDownloadSources.includes("github");
  const row = "flex min-h-14 items-center gap-3 px-3.5 py-2.5";
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="cloud" tone="sky">
        {t("up.downloadSources")}
      </SectionLabel>
      <p className="max-w-2xl text-[13px] leading-relaxed text-pretty text-muted">{t("up.downloadSources.body")}</p>
      <ul className="max-w-2xl divide-y divide-line overflow-hidden rounded-field border border-line bg-canvas">
        <li className={row}>
          <span className="flex size-5 flex-none items-center justify-center rounded-full bg-surface-2 font-mono text-[11px] font-bold text-muted" aria-hidden>
            1
          </span>
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <b className="text-[13px]">{t("up.downloadSources.panel")}</b>
            <span className="text-xs leading-snug text-pretty text-muted">{t("up.downloadSources.panel.hint")}</span>
          </div>
          <Chip>{t("up.downloadSources.always")}</Chip>
        </li>
        <li className={row}>
          <span className="flex size-5 flex-none items-center justify-center rounded-full bg-surface-2 font-mono text-[11px] font-bold text-muted" aria-hidden>
            2
          </span>
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <b className="text-[13px]">{t("up.downloadSources.github")}</b>
            <span className="text-xs leading-snug text-pretty text-muted">{t("up.downloadSources.github.hint")}</span>
          </div>
          {owner ? (
            <Switch checked={github} aria-label={t("up.downloadSources.github")} disabled={busy} onCheckedChange={(on) => void onChange(on ? ["github"] : [])} />
          ) : (
            <Chip>{t(github ? "up.downloadSources.on" : "up.downloadSources.off")}</Chip>
          )}
        </li>
      </ul>
    </section>
  );
}

function AboutCard({ panel }: { panel: Updates["panel"] }) {
  const t = useT();
  const lang = useLang();
  const fmt = useFmt();
  const docs = lang === "ru" || lang === "zh" ? `https://mistgate.app/${lang}/` : "https://mistgate.app/";

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
