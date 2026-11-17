import { Link } from "@tanstack/react-router";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { buttonClass } from "@/components/ui/button";
import { useLang, useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { updateTimezoneName, useIsOwner, useUpdateActions, useUpdates, type Updates } from "@/lib/updates";
import { PanelResourcesCard } from "./panel-resources";
import { OpsLogs, OpsSide, type OpsDemo } from "./system-ops";
import { Foot, ICONS, Otg, Row, Rows, Sec, SxGate, SxRoot } from "./sx-bits";
import "./settings-sys.css";

/**
 * Settings -> System (the board's sxIs_sys): what the panel costs this server across the page, then two columns: the logs and the
 * node updates' settings on the left; this panel, retention, export, metrics and "About" on the right. The updates' settings and
 * the version come from GetUpdates (the tab's gate); the diagnostics are the owner's and load their own.
 */
export function SystemPage({ demo }: { demo?: OpsDemo } = {}) {
  const q = useUpdates();
  const owner = useIsOwner();
  const actions = useUpdateActions();
  const data = q.data;
  return (
    <SxGate pending={q.isPending} error={q.isError && !data} onRetry={() => void q.refetch()}>
      {data && (
        <>
          <PanelResourcesCard />
          <div className="pf-g pp-grid sb-grid sx-grid">
            <div className="col">
              <OpsLogs />
              <NodeUpdatesCard data={data} owner={owner} busy={actions.busy} actions={actions} />
            </div>
            <aside className="col pp-as">
              <ThisPanelCard panel={data.panel} />
              <OpsSide demo={demo} />
              <AboutCard />
            </aside>
          </div>
        </>
      )}
    </SxGate>
  );
}

const updateTimezoneOffsets = [-720, -660, -600, -570, -540, -480, -420, -360, -300, -270, -240, -210, -180, -120, -60, 0, 60, 120, 180, 210, 240, 270, 300, 330, 345, 360, 390, 420, 480, 525, 540, 570, 600, 630, 660, 690, 720, 765, 780, 840];

type Change = { setUpdateTimezone: (offset: number) => Promise<boolean>; setDownloadTimeout: (seconds: number) => Promise<boolean>; setDownloadSources: (sources: string[]) => Promise<boolean> };

/** The card of the node updates' settings: the time zone of new schedules, how long a download may take, where it may come from. */
function NodeUpdatesCard({ data, owner, busy, actions }: { data: Updates; owner: boolean; busy: boolean; actions: Change }) {
  const t = useT();
  return (
    <Sec tone="mint" d={ICONS.refresh} title={t("sx.sys.upd")}>
      <Rows className={cx("sx-all", !owner && "ro")}>
        <TimezoneRow data={data} owner={owner} busy={busy} onChange={actions.setUpdateTimezone} />
        <TimeoutRow data={data} owner={owner} busy={busy} onChange={actions.setDownloadTimeout} />
      </Rows>
      <SourcesBlock data={data} owner={owner} busy={busy} onChange={actions.setDownloadSources} />
    </Sec>
  );
}

function TimezoneRow({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (offset: number) => Promise<boolean> }) {
  const t = useT();
  const current = data.scheduleTimezoneOffsetMinutes;
  // a stored offset the list does not know (a panel from elsewhere) still shows as it is
  const offsets = updateTimezoneOffsets.includes(current) ? updateTimezoneOffsets : [...updateTimezoneOffsets, current].sort((a, b) => a - b);
  return (
    <Row label={t("up.timezone.label")} hint={t("sx.sys.tz.hint")}>
      <select className="an-i mono sx-sel" aria-label={t("up.timezone.label")} value={current} disabled={busy || !owner} onChange={(e) => void onChange(Number(e.target.value))}>
        {offsets.map((o) => (
          <option key={o} value={o}>
            {updateTimezoneName(o)}
          </option>
        ))}
      </select>
    </Row>
  );
}

const timeouts = [300, 600, 1800, 3600, 7200];

function TimeoutRow({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (seconds: number) => Promise<boolean> }) {
  const t = useT();
  const current = data.updateDownloadTimeoutSeconds || 1800;
  // a timeout the panel holds that is not one of the five still shows, in its place
  const options = (timeouts.includes(current) ? timeouts : [...timeouts, current].sort((a, b) => a - b)).map((s) => ({
    value: String(s),
    label: t(s < 3600 ? "up.downloadTimeout.minutes" : "up.downloadTimeout.hours", { n: s < 3600 ? s / 60 : s / 3600 }),
  }));
  return (
    <Row label={t("up.downloadTimeout.label")} hint={t("sx.sys.tmo.hint")}>
      <div inert={!owner || busy}>
        <Segmented aria-label={t("up.downloadTimeout.label")} value={String(current)} onValueChange={(v) => void onChange(Number(v))} options={options} className="w-full [&>*]:flex-1" />
      </div>
    </Row>
  );
}

/**
 * Where a node may fetch the update file besides the panel. The panel is always first and has no switch; GitHub is the one optional
 * fallback. The Cloudflare edition has no such setting (the call answers UNIMPLEMENTED), so the block is not drawn there.
 */
function SourcesBlock({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (sources: string[]) => Promise<boolean> }) {
  const t = useT();
  const edge = useEdition().edge;
  if (edge) return null;
  const github = data.updateDownloadSources.includes("github");
  return (
    <div className="sx-srcs">
      <span className="rw-sl">{t("up.downloadSources")}</span>
      <p>{t("sx.sys.srcs.lead")}</p>
      <div className="sx-so">
        <b className="mono">1</b>
        <span>
          <b>{t("up.downloadSources.panel")}</b>
          <em>{t("sx.sys.src.panel.hint")}</em>
        </span>
        <Otg>{t("sx.sys.src.always")}</Otg>
      </div>
      <div className={cx("sx-so", !github && "off")}>
        <b className="mono">2</b>
        <span>
          <b>{t("up.downloadSources.github")}</b>
          <em>{t("sx.sys.src.github.hint")}</em>
        </span>
        <Switch className="fz-sw" checked={github} disabled={busy || !owner} aria-label={t("sx.sys.src.github.aria")} onCheckedChange={(on) => void onChange(on ? ["github"] : [])} />
      </div>
    </div>
  );
}

/** The two settings cards of the update downloads, each alone on a page of its own (the scenes of the board's pieces). */
export function UpdateDownloadTimeoutCard({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (seconds: number) => Promise<boolean> }) {
  const t = useT();
  return (
    <SxRoot>
      <Sec tone="mint" d={ICONS.refresh} title={t("sx.sys.upd")}>
        <Rows>
          <TimeoutRow data={data} owner={owner} busy={busy} onChange={onChange} />
        </Rows>
      </Sec>
    </SxRoot>
  );
}

export function UpdateDownloadSourcesCard({ data, owner, busy, onChange }: { data: Updates; owner: boolean; busy: boolean; onChange: (sources: string[]) => Promise<boolean> }) {
  const t = useT();
  if (useEdition().edge) return null;
  return (
    <SxRoot>
      <Sec tone="mint" d={ICONS.refresh} title={t("up.downloadSources")}>
        <SourcesBlock data={data} owner={owner} busy={busy} onChange={onChange} />
      </Sec>
    </SxRoot>
  );
}

/** "This panel": the build it runs and the latest release the panel has seen; the update itself is on the Updates page. */
function ThisPanelCard({ panel }: { panel: Updates["panel"] }) {
  const t = useT();
  const fmt = useFmt();
  const latest = panel?.update?.version;
  return (
    <Sec tone="lav" d={ICONS.info} title={t("sx.sys.this")}>
      <dl className="uq-dl ai-dl sx-dl">
        <div>
          <dt>{t("sx.sys.this.ver")}</dt>
          <dd>
            <b className="mono">{panel?.version || "—"}</b>
            {panel?.built ? <span>{t("sx.sys.this.built", { at: fmt.dateTime(panel.built) })}</span> : null}
          </dd>
        </div>
        <div>
          <dt>{t("sx.sys.this.latest")}</dt>
          <dd>
            <b className="mono">{latest || "—"}</b>
          </dd>
        </div>
      </dl>
      <Foot>
        <Link to="/updates" className={buttonClass("secondary", "md")}>
          {t("sx.sys.this.open")}
        </Link>
      </Foot>
    </Sec>
  );
}

function AboutCard() {
  const t = useT();
  const lang = useLang();
  const docs = lang === "ru" || lang === "zh" ? `https://mistgate.app/${lang}/` : "https://mistgate.app/";
  return (
    <Sec tone="none" d={ICONS.info} title={t("sx.sys.about")} lead={t("sx.sys.about.lead")}>
      <div className="sx-links">
        <a className="an-lk" href={docs}>
          {t("sx.sys.about.docs")}
        </a>
        <a className="an-lk" href="https://github.com/Mistgate/mistgate" target="_blank" rel="noreferrer">
          {t("sx.sys.about.src")}
        </a>
        <a className="sx-lic" href="https://github.com/Mistgate/mistgate/blob/main/LICENSE" target="_blank" rel="noreferrer">
          {t("sx.sys.about.lic")}
        </a>
      </div>
    </Sec>
  );
}

