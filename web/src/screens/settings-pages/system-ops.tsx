import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import type { PanelLogEntry, RetentionItem } from "@/gen/mistgate/ops/v1/ops_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import {
  changedRetention,
  defaultLogFilter,
  isLogLevel,
  isUnavailable,
  logLevels,
  logSettingsQuery,
  logsQuery,
  metricsQuery,
  olderLogs,
  retentionProblem,
  retentionQuery,
  useOpsActions,
  type LogFilter,
  type LogLevel,
} from "@/lib/ops";
import { useIsOwner } from "@/lib/updates";

// Settings -> System, the panel's own diagnostics: logs and their level, retention, export, metrics. Owner only (the procedures
// are the owner's); on the Cloudflare edition the first read answers UNIMPLEMENTED and the page says so once.

const card = "flex min-w-0 flex-col gap-3.5 rounded-card-lg border border-line bg-surface p-4 md:p-[18px]";
const intro = "max-w-2xl text-[13px] leading-relaxed text-pretty text-muted";

export function OpsCards() {
  const t = useT();
  const owner = useIsOwner();
  const settings = useQuery({ ...logSettingsQuery, enabled: owner });
  if (!owner) return null;
  if (settings.isError) {
    return isUnavailable(settings.error) ? <Notice tone="quiet">{t("obs.unavailable")}</Notice> : <QueryError error={settings.error} onRetry={() => void settings.refetch()} />;
  }
  if (!settings.data) return <Pending />;
  return (
    <>
      <LogsCard level={settings.data.level} defaultLevel={settings.data.defaultLevel} />
      <RetentionCard />
      <ExportCard />
      <MetricsCard />
    </>
  );
}

const levelLabel = (t: T, l: string) => t(`obs.level.${isLogLevel(l) ? l : "info"}` as MessageKey);
const levelTone: Record<string, string> = { debug: "text-muted", info: "text-fg", warn: "text-warn-text", error: "text-danger-text" };

const windows = [
  { value: "15", key: "obs.logs.w15" },
  { value: "60", key: "obs.logs.w60" },
  { value: "360", key: "obs.logs.w360" },
  { value: "1440", key: "obs.logs.w1440" },
  { value: "0", key: "obs.logs.wAll" },
] as const;

function LogsCard({ level, defaultLevel }: { level: string; defaultLevel: string }) {
  const t = useT();
  const fmt = useFmt();
  const actions = useOpsActions();
  const [filter, setFilter] = useState<LogFilter>(defaultLogFilter);
  const [live, setLive] = useState(false);
  // older pages belong to the filter they were loaded with: another filter (or live refresh) starts again from the newest
  const [loaded, setLoaded] = useState<{ key: string; entries: PanelLogEntry[]; cursor: bigint } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const logs = useQuery(logsQuery(filter, live));
  const key = `${live}:${JSON.stringify(filter)}`;
  const more = loaded?.key === key ? loaded : null;

  const patch = (p: Partial<LogFilter>) => setFilter((f) => ({ ...f, ...p }));
  const entries = [...(logs.data?.entries ?? []), ...(more?.entries ?? [])];
  const cursor = more ? more.cursor : (logs.data?.nextCursor ?? 0n);
  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const r = await olderLogs(filter, cursor);
      setLoaded({ key, entries: [...(more?.entries ?? []), ...r.entries], cursor: r.nextCursor });
    } finally {
      setLoadingMore(false);
    }
  };

  return (
    <section className={card}>
      <SectionLabel as="h2" icon="terminal" tone="sage">
        {t("obs.logs.title")}
      </SectionLabel>
      <p className={intro}>{t("obs.logs.intro")}</p>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex w-44 flex-col gap-1.5 text-xs font-semibold">
          {t("obs.logs.level")}
          <Select
            aria-label={t("obs.logs.level")}
            value={isLogLevel(level) ? level : "info"}
            onValueChange={(v) => isLogLevel(v) && v !== level && actions.setLevel.mutate(v)}
            options={logLevels.map((l) => ({ value: l, label: levelLabel(t, l) }))}
          />
        </label>
        <p className="max-w-md pb-2 text-xs leading-snug text-muted">{t("obs.logs.levelNote", { default: levelLabel(t, defaultLevel) })}</p>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <label className="flex flex-col gap-1.5 text-xs font-semibold">
          {t("obs.logs.show")}
          <Select
            aria-label={t("obs.logs.show")}
            value={filter.minLevel}
            onValueChange={(v) => isLogLevel(v) && patch({ minLevel: v })}
            options={logLevels.map((l) => ({ value: l, label: levelLabel(t, l) }))}
          />
        </label>
        <label className="flex flex-col gap-1.5 text-xs font-semibold">
          {t("obs.logs.window")}
          <Select
            aria-label={t("obs.logs.window")}
            value={String(filter.minutes)}
            onValueChange={(v) => patch({ minutes: Number(v) })}
            options={windows.map((w) => ({ value: w.value, label: t(w.key) }))}
          />
        </label>
        <TextField label={t("obs.logs.search")} value={filter.query} maxLength={100} onChange={(e) => patch({ query: e.target.value })} />
        <TextField label={t("obs.logs.component")} mono value={filter.component} maxLength={24} onChange={(e) => patch({ component: e.target.value })} />
        <TextField label={t("obs.logs.code")} mono value={filter.code} maxLength={64} onChange={(e) => patch({ code: e.target.value })} />
        <TextField label={t("obs.logs.request")} mono value={filter.requestId} maxLength={8} onChange={(e) => patch({ requestId: e.target.value })} />
      </div>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted">
        <label className="flex items-center gap-2 font-semibold text-fg">
          <Switch checked={live} onCheckedChange={setLive} aria-label={t("obs.logs.live")} />
          {t("obs.logs.live")}
        </label>
        {logs.data && <span>{t("obs.logs.held", { n: logs.data.buffered, cap: logs.data.capacity })}</span>}
      </div>

      {logs.isError ? (
        <QueryError error={logs.error} onRetry={() => void logs.refetch()} compact />
      ) : !logs.data ? (
        <Pending compact />
      ) : entries.length === 0 ? (
        <p className="text-sm text-muted">{t("obs.logs.empty")}</p>
      ) : (
        <ul className="flex max-h-[28rem] flex-col divide-y divide-line overflow-y-auto rounded-field border border-line bg-canvas font-mono text-[12px] leading-snug" dir="ltr">
          {entries.map((e) => (
            <LogRow key={String(e.seq)} e={e} time={fmt.clock(Math.floor(Number(e.timeUnixMs) / 1000))} />
          ))}
        </ul>
      )}
      {!live && cursor > 0n && (
        <div>
          <Button variant="secondary" size="sm" disabled={loadingMore} onClick={() => void loadMore()}>
            {t("obs.logs.more")}
          </Button>
        </div>
      )}
    </section>
  );
}

function LogRow({ e, time }: { e: PanelLogEntry; time: string }) {
  const t = useT();
  const attrs = Object.entries(e.attrs).filter(([k]) => k !== "code" && k !== "request_id");
  return (
    <li className="flex flex-col gap-0.5 px-3 py-2">
      <div className="flex flex-wrap items-baseline gap-x-2">
        <span className="text-muted">{time}</span>
        <span className={cx("font-bold uppercase", levelTone[e.level])}>{e.level}</span>
        {e.component && <span className="text-muted">{e.component}</span>}
        <span className="min-w-0 break-words text-fg">{e.message}</span>
      </div>
      {(e.code || e.requestId || attrs.length > 0) && (
        <div className="flex flex-wrap gap-x-3 text-muted">
          {e.code && (
            <span>
              {t("obs.logs.code.label")}=<span className="text-fg">{e.code}</span>
            </span>
          )}
          {e.requestId && (
            <span>
              {t("obs.logs.req.label")}=<span className="text-fg">{e.requestId}</span>
            </span>
          )}
          {attrs.map(([k, v]) => (
            <span key={k}>
              {k}=<span className="break-all text-fg">{v}</span>
            </span>
          ))}
        </div>
      )}
    </li>
  );
}

function RetentionCard() {
  const t = useT();
  const actions = useOpsActions();
  const q = useQuery(retentionQuery);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const items = q.data?.items ?? [];
  const changes = changedRetention(items, draft);
  const invalid = items.some((i) => retentionProblem(i, draft[i.key] ?? ""));
  const save = () => actions.saveRetention.mutate(changes, { onSuccess: () => setDraft({}) });

  return (
    <section className={card}>
      <SectionLabel as="h2" icon="clock" tone="sand">
        {t("obs.ret.title")}
      </SectionLabel>
      <p className={intro}>{t("obs.ret.intro")}</p>
      {q.isError ? (
        <QueryError error={q.error} onRetry={() => void q.refetch()} compact />
      ) : !q.data ? (
        <Pending compact />
      ) : (
        <>
          <div className="grid gap-3 sm:grid-cols-2">
            {items.map((i) => (
              <RetentionField key={i.key} item={i} value={draft[i.key] ?? String(i.value)} onChange={(v) => setDraft((d) => ({ ...d, [i.key]: v }))} />
            ))}
          </div>
          <p className="text-xs leading-snug text-muted">{t("obs.ret.warn")}</p>
          <div className="flex flex-wrap gap-2">
            <Button variant="primary" size="md" disabled={actions.saveRetention.isPending || invalid || Object.keys(changes).length === 0} onClick={save}>
              {t("obs.ret.save")}
            </Button>
            <Button variant="ghost" size="md" disabled={actions.saveRetention.isPending} onClick={() => setDraft(Object.fromEntries(items.map((i) => [i.key, String(i.defaultValue)])))}>
              {t("obs.ret.reset")}
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

function RetentionField({ item, value, onChange }: { item: RetentionItem; value: string; onChange: (v: string) => void }) {
  const t = useT();
  const unit = t(item.unit === "hours" ? "obs.ret.hours" : "obs.ret.days");
  return (
    <TextField
      label={t(`obs.ret.f.${item.key}` as MessageKey)}
      hint={t("obs.ret.range", { min: item.min, max: item.max, unit, default: item.defaultValue })}
      error={retentionProblem(item, value) ? t("obs.ret.range", { min: item.min, max: item.max, unit, default: item.defaultValue }) : undefined}
      inputMode="numeric"
      mono
      value={value}
      onChange={(e) => onChange(e.target.value.replace(/[^0-9]/g, ""))}
    />
  );
}

function ExportCard() {
  const t = useT();
  const actions = useOpsActions();
  const [hours, setHours] = useState("24");
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="list" tone="sky">
        {t("obs.exp.title")}
      </SectionLabel>
      <p className={intro}>{t("obs.exp.intro")}</p>
      <div className="flex flex-wrap items-end gap-3">
        <label className="flex w-52 flex-col gap-1.5 text-xs font-semibold">
          {t("obs.exp.window")}
          <Select
            aria-label={t("obs.exp.window")}
            value={hours}
            onValueChange={setHours}
            options={[
              { value: "24", label: t("obs.exp.w24") },
              { value: "168", label: t("obs.exp.w168") },
              { value: "744", label: t("obs.exp.w744") },
            ]}
          />
        </label>
        <Button variant="primary" size="md" disabled={actions.exportNow.isPending} onClick={() => actions.exportNow.mutate(Number(hours))}>
          {t("obs.exp.download")}
        </Button>
      </div>
    </section>
  );
}

function MetricsCard() {
  const t = useT();
  const actions = useOpsActions();
  const q = useQuery(metricsQuery);
  const [token, setToken] = useState("");
  const apply = (req: { enabled?: boolean; rotateToken?: boolean }) =>
    actions.setMetrics.mutate(req, { onSuccess: (r) => setToken(r.token) });
  return (
    <section className={card}>
      <SectionLabel as="h2" icon="gauge" tone="mint">
        {t("obs.met.title")}
      </SectionLabel>
      <p className={intro}>{t("obs.met.intro")}</p>
      {q.isError ? (
        <QueryError error={q.error} onRetry={() => void q.refetch()} compact />
      ) : !q.data ? (
        <Pending compact />
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-4">
            <label className="flex items-center gap-2 text-sm font-semibold">
              <Switch checked={q.data.enabled} disabled={actions.setMetrics.isPending} onCheckedChange={(enabled) => apply({ enabled })} aria-label={t("obs.met.enable")} />
              {t("obs.met.enable")}
            </label>
            {q.data.hasToken && (
              <Button variant="secondary" size="sm" disabled={actions.setMetrics.isPending} onClick={() => apply({ rotateToken: true })}>
                {t("obs.met.rotate")}
              </Button>
            )}
          </div>
          {token ? (
            <Notice title={t("obs.met.tokenOnce")}>
              <code className="break-all font-mono text-xs" dir="ltr">
                {token}
              </code>
              <span className="ms-2 inline-block">
                <CopyButton value={token} />
              </span>
            </Notice>
          ) : (
            q.data.hasToken && <p className="text-xs leading-snug text-muted">{t("obs.met.hasToken")}</p>
          )}
        </>
      )}
    </section>
  );
}

export type { LogLevel };
