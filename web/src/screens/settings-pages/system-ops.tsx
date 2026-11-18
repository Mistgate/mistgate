import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
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
import { G } from "@/screens/users/user-bits";
import { BusyButton, CmdRow, Foot, ICONS, Mono, Pb, Sec, slots, SxRoot, Swr } from "./sx-bits";
import "./settings-sys.css";

// Settings -> System, the panel's own diagnostics: logs and their level (the left column), retention, export, metrics (the right
// one). Owner only (the procedures are the owner's); on the Cloudflare edition the first read answers UNIMPLEMENTED and the page says
// so once, where the logs would be.

/** What the diagnostics' first read says: nothing to show (not the owner), a line (the edition has none, a failure) or the cards. */
function useOps() {
  const owner = useIsOwner();
  const settings = useQuery({ ...logSettingsQuery, enabled: owner });
  return { owner, settings };
}

/** The logs card, or the one line that says why there are none. */
export function OpsLogs() {
  const t = useT();
  const { owner, settings } = useOps();
  if (!owner) return null;
  if (settings.isError) {
    return isUnavailable(settings.error) ? <Notice tone="quiet">{t("obs.unavailable")}</Notice> : <QueryError error={settings.error} onRetry={() => void settings.refetch()} />;
  }
  if (!settings.data) return <Pending />;
  return <LogsCard level={settings.data.level} defaultLevel={settings.data.defaultLevel} />;
}

/** What a scene opens the metrics window with: the question before a new token, or the token made. */
export type OpsDemo = { token?: "form" | "made" };

/** The cards of the right column that are the owner's: retention, export and the metrics. */
export function OpsSide({ demo }: { demo?: OpsDemo }) {
  const { owner, settings } = useOps();
  if (!owner || !settings.data) return null;
  return (
    <>
      <RetentionCard />
      <ExportCard />
      <MetricsCard demo={demo} />
    </>
  );
}

/** Both, one after the other (for a page that does not split them in two columns). */
export function OpsCards() {
  return (
    <>
      <OpsLogs />
      <OpsSide />
    </>
  );
}

const levelLabel = (t: T, l: string) => t(`obs.level.${isLogLevel(l) ? l : "info"}` as MessageKey);

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
  const [more, setMore] = useState(false);
  // older pages belong to the filter they were loaded with: another filter (or live refresh) starts again from the newest
  const [loaded, setLoaded] = useState<{ key: string; entries: PanelLogEntry[]; cursor: bigint } | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const logs = useQuery(logsQuery(filter, live));
  const key = `${live}:${JSON.stringify(filter)}`;
  const older = loaded?.key === key ? loaded : null;

  const patch = (p: Partial<LogFilter>) => setFilter((f) => ({ ...f, ...p }));
  const entries = [...(logs.data?.entries ?? []), ...(older?.entries ?? [])];
  const cursor = older ? older.cursor : (logs.data?.nextCursor ?? 0n);
  const loadMore = async () => {
    setLoadingMore(true);
    try {
      const r = await olderLogs(filter, cursor);
      setLoaded({ key, entries: [...(older?.entries ?? []), ...r.entries], cursor: r.nextCursor });
    } finally {
      setLoadingMore(false);
    }
  };
  const levels = logLevels.map((l) => ({ value: l, label: levelLabel(t, l) }));

  return (
    <Sec tone="sand" d={ICONS.lines} title={t("obs.logs.title")} lead={t("sx.sys.logs.lead")}>
      <div className="sx-lc">
        <div className="sx-lf">
          <Segmented aria-label={t("obs.logs.show")} value={filter.minLevel} onValueChange={(v) => patch({ minLevel: v })} options={levels} className="w-full [&>*]:flex-1" />
          <input className="an-i sx-q" type="search" placeholder={t("obs.logs.search")} aria-label={t("obs.logs.search")} autoComplete="off" maxLength={100} value={filter.query} onChange={(e) => patch({ query: e.target.value })} />
          <button type="button" className="an-lk sx-more" aria-expanded={more} onClick={() => setMore((m) => !m)}>
            {t("sx.sys.logs.more")}
          </button>
        </div>
        <div className={cx("sx-live", live && "live")}>
          <span>
            <i aria-hidden />
            {t("obs.logs.live")}
          </span>
          <Switch className="fz-sw" checked={live} onCheckedChange={setLive} aria-label={t("obs.logs.live")} />
        </div>
      </div>
      {more && (
        <div className="sx-xf">
          <label className="an-f">
            <span>{t("obs.logs.level")}</span>
            <Select className="h-9!" aria-label={t("obs.logs.level")} value={isLogLevel(level) ? level : "info"} onValueChange={(v) => isLogLevel(v) && v !== level && actions.setLevel.mutate(v)} options={levels} />
          </label>
          <label className="an-f">
            <span>{t("obs.logs.window")}</span>
            <Select className="h-9!" aria-label={t("obs.logs.window")} value={String(filter.minutes)} onValueChange={(v) => patch({ minutes: Number(v) })} options={windows.map((w) => ({ value: w.value, label: t(w.key) }))} />
          </label>
          <label className="an-f">
            <span>{t("obs.logs.component")}</span>
            <input className="an-i mono" value={filter.component} maxLength={24} onChange={(e) => patch({ component: e.target.value })} />
          </label>
          <label className="an-f">
            <span>{t("obs.logs.code")}</span>
            <input className="an-i mono" value={filter.code} maxLength={64} onChange={(e) => patch({ code: e.target.value })} />
          </label>
          <label className="an-f">
            <span>{t("obs.logs.request")}</span>
            <input className="an-i mono" value={filter.requestId} maxLength={8} onChange={(e) => patch({ requestId: e.target.value })} />
          </label>
        </div>
      )}
      {logs.isError ? (
        <div className="sx-pad">
          <QueryError error={logs.error} onRetry={() => void logs.refetch()} compact />
        </div>
      ) : !logs.data ? (
        <div className="sx-pad">
          <Pending compact />
        </div>
      ) : entries.length === 0 ? (
        <p className="sb-emp ai-emp">{t("sx.sys.logs.empty")}</p>
      ) : (
        <div className="sx-logs" dir="ltr">
          {entries.map((e) => (
            <LogRow key={String(e.seq)} e={e} time={fmt.clock(Math.floor(Number(e.timeUnixMs) / 1000))} />
          ))}
        </div>
      )}
      {!live && cursor > 0n && (
        <Foot>
          <Button size="md" disabled={loadingMore} onClick={() => void loadMore()}>
            {t("obs.logs.more")}
          </Button>
        </Foot>
      )}
      <p className="sb-vfoot">
        {t("sx.sys.logs.foot", { n: logs.data?.buffered ?? 0, cap: logs.data?.capacity ?? 0, level: levelLabel(t, level), default: levelLabel(t, defaultLevel) })}
      </p>
    </Sec>
  );
}

function LogRow({ e, time }: { e: PanelLogEntry; time: string }) {
  const t = useT();
  const kv: [string, string][] = [
    ...(e.code ? [[t("obs.logs.code.label"), e.code] as [string, string]] : []),
    ...(e.requestId ? [[t("obs.logs.req.label"), e.requestId] as [string, string]] : []),
    ...Object.entries(e.attrs).filter(([k]) => k !== "code" && k !== "request_id"),
  ];
  return (
    <div className={cx("sx-log", e.level)}>
      <em className="mono">{time}</em>
      <i className="mono">{e.level}</i>
      <span className="mono sx-part">{e.component}</span>
      <span className="sx-msg">{e.message}</span>
      {kv.length > 0 && (
        <span className="sx-kv">
          {kv.map(([k, v]) => (
            <i key={k} className="mono">
              <em>{k}=</em>
              {v}
            </i>
          ))}
        </span>
      )}
    </div>
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
  // a shorter time than the stored one deletes rows at the next sweep, for good
  const shorter = items.some((i) => i.key in changes && changes[i.key]! < i.value);
  const dirty = Object.keys(changes).length > 0;
  const save = () => actions.saveRetention.mutate(changes, { onSuccess: () => setDraft({}) });

  return (
    <Sec tone="sand" d={ICONS.clock} title={t("obs.ret.title")} lead={t("sx.sys.ret.lead")}>
      {q.isError ? (
        <div className="sx-pad">
          <QueryError error={q.error} onRetry={() => void q.refetch()} compact />
        </div>
      ) : !q.data ? (
        <div className="sx-pad">
          <Pending compact />
        </div>
      ) : (
        <>
          <div className="sx-ret">
            {items.map((i) => (
              <RetentionField key={i.key} item={i} value={draft[i.key] ?? String(i.value)} onChange={(v) => setDraft((d) => ({ ...d, [i.key]: v }))} />
            ))}
          </div>
          {shorter && !invalid && (
            <div className="sx-pbr">
              <Pb tone="part">{t("sx.sys.ret.warn")}</Pb>
            </div>
          )}
          <Foot>
            <BusyButton variant="primary" size="md" busy={actions.saveRetention.isPending} disabled={invalid || !dirty} onClick={save}>
              {t("obs.ret.save")}
            </BusyButton>
            <Button size="md" disabled={actions.saveRetention.isPending} onClick={() => setDraft(Object.fromEntries(items.map((i) => [i.key, String(i.defaultValue)])))}>
              {t("obs.ret.reset")}
            </Button>
          </Foot>
        </>
      )}
    </Sec>
  );
}

function RetentionField({ item, value, onChange }: { item: RetentionItem; value: string; onChange: (v: string) => void }) {
  const t = useT();
  const unit = t(item.unit === "hours" ? "obs.ret.hours" : "obs.ret.days");
  const range = t("obs.ret.range", { min: item.min, max: item.max, unit, default: item.defaultValue });
  const bad = retentionProblem(item, value);
  return (
    <label className={cx("an-f sx-rf", bad && "err")}>
      <span>{t(`obs.ret.f.${item.key}` as MessageKey)}</span>
      <span className="sx-num">
        <input className="an-i mono" type="text" inputMode="numeric" value={value} aria-invalid={bad} onChange={(e) => onChange(e.target.value.replace(/[^0-9]/g, ""))} />
        <em>{unit}</em>
      </span>
      <small>{range}</small>
    </label>
  );
}

function ExportCard() {
  const t = useT();
  const actions = useOpsActions();
  const [hours, setHours] = useState("24");
  const busy = actions.exportNow.isPending;
  return (
    <Sec tone="sky" d={ICONS.download} title={t("obs.exp.title")} lead={t("sx.sys.exp.lead")}>
      <div className="sx-pad">
        <Segmented
          aria-label={t("obs.exp.window")}
          value={hours}
          onValueChange={setHours}
          className="w-full [&>*]:flex-1"
          options={[
            { value: "24", label: t("sx.sys.exp.24") },
            { value: "168", label: t("sx.sys.exp.7") },
            { value: "720", label: t("sx.sys.exp.30") },
          ]}
        />
      </div>
      <Foot>
        <BusyButton size="md" busy={busy} onClick={() => actions.exportNow.mutate(Number(hours))}>
          {busy ? t("sx.sys.exp.busy") : t("obs.exp.download")}
        </BusyButton>
      </Foot>
    </Sec>
  );
}

type TokenWindow = { step: "form" } | { step: "made"; token: string };

function MetricsCard({ demo }: { demo?: OpsDemo }) {
  const t = useT();
  const actions = useOpsActions();
  const q = useQuery(metricsQuery);
  const [win, setWin] = useState<TokenWindow | null>(demo?.token === "made" ? { step: "made", token: "mgm_example_3f9a1c7e5b2d4086a1f3c5e7" } : demo?.token === "form" ? { step: "form" } : null);
  const apply = (req: { enabled?: boolean; rotateToken?: boolean }) =>
    actions.setMetrics.mutate(req, { onSuccess: (r) => r.token && setWin({ step: "made", token: r.token }) });
  return (
    <Sec
      tone="mint"
      d={ICONS.bars}
      title={t("obs.met.title")}
      lead={slots(t("sx.sys.met.lead"), { path: <Mono>/metrics</Mono>, auth: <Mono>Authorization: Bearer</Mono> })}
    >
      {q.isError ? (
        <div className="sx-pad">
          <QueryError error={q.error} onRetry={() => void q.refetch()} compact />
        </div>
      ) : !q.data ? (
        <div className="sx-pad">
          <Pending compact />
        </div>
      ) : (
        <>
          <Swr label={t("obs.met.enable")}>
            <Switch className="fz-sw" checked={q.data.enabled} disabled={actions.setMetrics.isPending} onCheckedChange={(enabled) => apply({ enabled })} aria-label={t("obs.met.enable")} />
          </Swr>
          {q.data.enabled && q.data.hasToken && (
            <Foot className="sx-tk">
              <em>{t("sx.sys.met.tok")}</em>
              <Button size="md" onClick={() => setWin({ step: "form" })}>
                {t("obs.met.rotate")}
              </Button>
            </Foot>
          )}
        </>
      )}
      <TokenModal win={win} setWin={setWin} busy={actions.setMetrics.isPending} onMake={() => apply({ rotateToken: true })} />
    </Sec>
  );
}

/** The window of a new metrics token: what changes, then the token, which is shown once. Esc and the backdrop do not close it over a token. */
function TokenModal({ win, setWin, busy, onMake }: { win: TokenWindow | null; setWin: (w: TokenWindow | null) => void; busy: boolean; onMake: () => void }) {
  const t = useT();
  const made = win?.step === "made";
  return (
    <Modal
      open={!!win}
      onOpenChange={(o) => !o && !made && setWin(null)}
      title={t("sx.sys.met.dlg")}
      closeLabel={made ? undefined : t("sx.sys.met.close")}
      shot="metrics-token"
      footer={
        made ? (
          <Button variant="primary" size="lg" onClick={() => setWin(null)}>
            {t("sx.sys.met.done")}
          </Button>
        ) : (
          <>
            <Button size="lg" onClick={() => setWin(null)}>
              {t("sx.sys.met.cancel")}
            </Button>
            <BusyButton variant="primary" size="lg" busy={busy} onClick={onMake}>
              {t("sx.sys.met.make")}
            </BusyButton>
          </>
        )
      }
    >
      <SxRoot className="sx-dlg">
        {win?.step === "made" ? (
          <>
            <div className="ai-made">
              <span className="g" aria-hidden>
                <G d={ICONS.tick} size={18} stroke={1.8} />
              </span>
              <b>{t("sx.sys.met.made")}</b>
              <span>{t("sx.sys.met.made.body")}</span>
            </div>
            <CmdRow value={win.token} wrap />
          </>
        ) : (
          <p className="sx-dp">{t("sx.sys.met.dlg.body")}</p>
        )}
      </SxRoot>
    </Modal>
  );
}

export type { LogLevel };
