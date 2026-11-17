import { useEffect, useState, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { playWave } from "@/components/fx/wave";
import { Scramble } from "@/components/fx/scramble-react";
import { EditionNotice } from "@/components/edition-notice";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { asChannel, type Channel } from "@/lib/channels";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { themeNow } from "@/lib/theme";
import type { UpdateActions, Updates } from "@/lib/updates";
import { G } from "@/screens/users/user-bits";
import { Bar, D, Gl, Pill, gk } from "./bits";
import { ChannelChip, ChannelDialog, channelName } from "./channel";
import { panelUpdate, ChannelPolicy, EdgeDeploy, PanelHow, shortHash } from "./cards";
import { ReleaseCard, type ReleaseNote } from "./notes";
import { HeroTrack, trackOf } from "./track";
import { Takeover, type TakeMode } from "./takeover";
import { PanelUpdateWindow } from "./update-dialog";

// "This panel" (the board's first tab): the version it runs and the one that is out, the four steps of an update, the buttons, what
// the new release brings and the facts behind the signature. While the panel installs itself the whole window is the takeover
// screen (takeover.tsx); the panel comes back by itself and says so.

/** What the page shows of the update that is going on, or has just been: nothing, the run, the new version answering, the look after. */
export type PanelPhase =
  | { kind: "idle" }
  | { kind: "run"; from: string; to: string; step?: number; slow: boolean }
  | { kind: "out"; from: string; to: string; /** The dev kit keeps the screen from fading out, so a screenshot finds it. */ hold?: boolean }
  | { kind: "done"; from: string; to: string };

/** A scene of the dev kit puts a phase and the card's own folds in place of what the page would find out. */
export type PanelScene = { phase?: PanelPhase; notes?: readonly ReleaseNote[]; checking?: boolean; details?: boolean; more?: boolean; win?: "channel" | "update"; pick?: Channel };

const SLOW_MS = 120_000;
/** After the click, how long the panel may say "not installing" before the run counts as over (it takes a moment to mark itself). */
const GRACE_MS = 15_000;
/** The panel has not answered for this long: it is down for its restart. */
const SILENT_MS = 8_000;
const POLL_MS = 3_000;

type Pending = { from: string; at: number; notes: readonly ReleaseNote[] };

/**
 * Follows an update from the click to the new version answering. The panel reports only that it is installing, never a step, so
 * this knows what is true of every moment: it was asked (or says `installing`), it does not answer (it is restarting), the version
 * changed (done: the screen says so for a moment, then the card looks like "just updated"), or it answers on the old version and is
 * not installing (it went back: the card says why). After two minutes without a new version the screen offers a reload.
 */
export function usePanelPhase(data: Updates, refetch: () => unknown, answeredAt: number, scene?: PanelScene): { phase: PanelPhase; begin: () => void; notes: readonly ReleaseNote[] | undefined } {
  const t = useT();
  const toast = useToast();
  const p = data.panel;
  const update = p?.update;
  const version = p?.version ?? "";
  const installing = !!update?.installing;
  const [pending, setPending] = useState<Pending | null>(null);
  const [settled, setSettled] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const finished = !!pending && !!version && version !== pending.from;
  const running = !!pending && !finished && (installing || now - answeredAt > SILENT_MS || now - pending.at < GRACE_MS);

  // while the update is on its way the page asks again every few seconds (a failed ask while the panel restarts is expected)
  useEffect(() => {
    if (!pending || finished || scene) return;
    const id = setInterval(() => {
      setNow(Date.now());
      void refetch();
    }, POLL_MS);
    return () => clearInterval(id);
  }, [pending, finished, scene, refetch]);

  // the new version answered: the wave goes over the window at once, a moment for the screen to say so, then the card is the page again
  useEffect(() => {
    if (!finished || scene) return;
    playWave({ x: Math.round(window.innerWidth / 2), y: Math.round(window.innerHeight * 0.42) }, { light: themeNow() === "light", theme: true });
    const id = window.setTimeout(() => {
      setSettled(true);
      toast(t("uq.toast.done", { version }));
      halo("save");
    }, 1500);
    return () => window.clearTimeout(id);
  }, [finished, scene, version, toast, t]);

  if (scene?.phase) return { phase: scene.phase, begin: () => {}, notes: scene.notes ?? data.panelNotes };
  const phase: PanelPhase =
    running && pending
      ? { kind: "run", from: pending.from, to: update?.version ?? "", slow: now - pending.at > SLOW_MS }
      : finished && pending
        ? { kind: settled ? "done" : "out", from: pending.from, to: version }
        : installing
          ? { kind: "run", from: version, to: update?.version ?? "", slow: false }
          : { kind: "idle" };
  return {
    phase,
    begin: () => {
      setNow(Date.now());
      setSettled(false);
      setPending({ from: version, at: Date.now(), notes: data.panelNotes });
    },
    notes: finished && pending ? pending.notes : data.panelNotes,
  };
}

/** The Takeover for a phase (nothing outside a run and the moment after it). */
export function PanelTakeover({ phase, onReload }: { phase: PanelPhase; onReload: () => void }) {
  if (phase.kind !== "run" && phase.kind !== "out") return null;
  const mode: TakeMode = phase.kind === "out" ? "out" : phase.slow ? "slow" : "run";
  return <Takeover mode={mode} step={phase.kind === "run" ? phase.step : undefined} from={phase.from} to={phase.to} onReload={onReload} hold={phase.kind === "out" && phase.hold} />;
}

/** The fold with the facts behind a release: the signature, the checksum and what happens on failure. */
function Details({ data, open, onToggle }: { data: Updates; open: boolean; onToggle: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const edition = useEdition();
  const off = edition.off("panel_update");
  const p = data.panel;
  const update = p?.update;
  return (
    <section className="uq-card uq-det">
      <button type="button" className="sb-mvh" aria-expanded={open} aria-controls="uq-det" onClick={onToggle}>
        <G d={D.chev} size={16} stroke={1.75} />
        <span>
          <b>{t("up.panel.details")}</b>
          <em>{t("uq.det.sub")}</em>
        </span>
      </button>
      {open && (
        <div id="uq-det">
          <dl className="uq-dl">
            {update?.version && (
              <div>
                <dt>{t("up.panel.latest")}</dt>
                <dd>
                  {update.url ? (
                    <a href={update.url} target="_blank" rel="noreferrer" className="an-lk">
                      <b className="mono">{update.version}</b>
                    </a>
                  ) : (
                    <b className="mono">{update.version}</b>
                  )}
                  {update.checkedUnix > 0 && <span>{t("up.panel.checkedAt", { ago: fmt.ago(update.checkedUnix) })}</span>}
                </dd>
              </div>
            )}
            {update?.built ? (
              <div>
                <dt>{t("up.panel.sha")}</dt>
                <dd>
                  <b className="mono" title={update.sha256}>
                    {shortHash(update.sha256)}
                  </b>
                  <span>{fmt.dateTime(update.built)}</span>
                </dd>
              </div>
            ) : null}
            <div>
              <dt>{t("up.panel.key")}</dt>
              <dd>{p?.hasReleaseKey ? <b className="mono uq-key">{p.releaseKeyFingerprint}</b> : <span className="text-warn-text">{t("up.panel.noKey")}</span>}</dd>
            </div>
          </dl>
          {!off && <p className="uq-dp">{t("up.panel.updateBody")}</p>}
          {!update?.supported && !off && (
            <div className="uq-dx">
              <PanelHow />
            </div>
          )}
        </div>
      )}
    </section>
  );
}

/** The first tab. `owner`: the person may update (the owner, in an edition that has the update). */
export function PanelTab({ data, owner, actions, refetch, answeredAt, scene }: { data: Updates; owner: boolean; actions: UpdateActions; refetch: () => unknown; /** When the panel last answered (ms): a long silence during an update is the restart. */ answeredAt: number; scene?: PanelScene }) {
  const t = useT();
  const fmt = useFmt();
  const edition = useEdition();
  const off = edition.off("panel_update");
  const { phase, begin, notes } = usePanelPhase(data, refetch, answeredAt, scene);
  const look = panelUpdate(t, data, edition.edge);
  const { p, update } = look;
  const key = update?.errorKey ?? "";
  const channel = asChannel(data.updateChannel);
  const running = { version: p?.version ?? "", channel: asChannel(data.runningVersionChannel) };
  const [win, setWin] = useState<"" | "channel" | "update">(scene?.win ?? "");
  const [detTouched, setDetTouched] = useState<boolean | null>(null);
  const detOpen = scene?.details ?? detTouched ?? (!update?.supported && !off);

  const run = phase.kind === "run";
  const done = phase.kind === "out" || phase.kind === "done";
  const sig = key === "unsigned" || key === "expired";
  const gh = key === "check_failed";
  const back = key === "install_failed" && !run;
  const available = !!update?.available;
  const hasNew = (available || sig) && !done;
  const checking = scene?.checking ?? actions.checking;
  const cur = done ? phase.to : (p?.version ?? "");
  const nextVersion = update?.version ?? "";
  const canUpdate = owner && !off && available && !!update?.installable && !update.installing && !run && !sig;
  const security = (notes ?? []).some((n) => n.security);
  const step = phase.kind === "run" ? phase.step : undefined;
  const trk = trackOf(run ? { run: true, step: step ?? 0 } : { back, sig });
  const heroClass = run ? "run" : done ? "done" : back || sig ? "back" : checking ? "cking" : "";
  const checked = update?.checkedUnix ? t("uq.noNew", { ago: fmt.ago(update.checkedUnix) }) : t("up.panel.checking");

  const stepText = step !== undefined ? t(`uq.step.${Math.min(step, 3) + 1}` as "uq.step.1") + "…" : t("uq.run.generic", { version: phase.kind === "run" ? phase.to : nextVersion });

  const hints: ReactNode[] = [];
  // a release that is out but cannot be installed from here says why (the channel, no key, no systemd), in the state's own words
  if (hasNew && !sig && !gh && !back && !run && !(available && update?.installable && !key)) {
    hints.push(
      <p key="state" className="rc-nt part">
        <Gl k="part" />
        <span>{look.label}</span>
      </p>,
    );
  }
  if (!edition.edge && !off && data.runningNewerThanChannelOffers) {
    hints.push(
      <p key="ahead">{t("up.ch.ahead", { version: running.version, channel: channelName(t, channel) })}</p>,
    );
  }
  if (edition.edge) hints.push(<EdgeDeploy key="edge" version={update?.version} />);
  else if (off) hints.push(<EditionNotice key="ed" feature="panel_update" />);
  if (!off && owner && (key === "channel_not_allowed" || key === "channel_config_invalid")) hints.push(<ChannelPolicy key="policy" data={data} owner={owner} />);

  return (
    <>
      <section className={cx("uq-card uq-hero", heroClass, !hasNew && "upto")} aria-label={t("up.panel.title")}>
        <i className="uq-halo" aria-hidden />
        <div className="uq-vs">
          <div className="uq-v">
            <span className="uq-vl">{t("up.panel.title")}</span>
            <b className="mono">
              <Scramble value={cur || "—"} />
            </b>
            <span className="uq-vm">
              {!edition.edge && <ChannelChip channel={channel} onClick={owner && !run ? () => setWin("channel") : undefined} />}
              <span>{phase.kind === "out" || phase.kind === "done" ? t("uq.builtDone", { version: phase.from }) : p?.built ? t("up.built", { date: fmt.date(p.built) }) : ""}</span>
            </span>
          </div>
          {hasNew && (
            <>
              {(run ? step !== undefined : true) && <HeroTrack steps={trk.steps} width={trk.width} />}
              <div className="uq-v new">
                <span className="uq-vl">{run ? t("uq.installing") : t("uq.avail")}</span>
                <b className="mono">{nextVersion}</b>
                <span className="uq-vm">
                  {update && update.publishedUnix > 0 && <span>{fmt.date(update.publishedUnix)}</span>}
                  {security && <Pill k="part">{t("up.notes.security")}</Pill>}
                </span>
              </div>
            </>
          )}
          {!hasNew && (
            <div className="uq-ok">
              {look.kind === "ok" || done ? <Pill k="ok">{t("uq.upto")}</Pill> : <Pill k={gk(look.kind)}>{look.label}</Pill>}
              <span>{done ? t("uq.noNewNow") : checked}</span>
            </div>
          )}
        </div>
        {run && (
          <div className="uq-now" role="status">
            <span className="an-sp" aria-hidden />
            <div>
              <b>{stepText}</b>
              <span>{t("uq.now.sub")}</span>
            </div>
            {step !== undefined && <em className="mono">{t("uq.of", { n: step + 1, total: 4 })}</em>}
          </div>
        )}
        {back && (
          <Bar
            bad
            action={
              canUpdate ? (
                <Button variant="secondary" size="md" onClick={() => setWin("update")}>
                  {t("uq.retryAgain")}
                </Button>
              ) : undefined
            }
          >
            <b>{t("uq.back.title", { version: p?.version ?? "" })}</b>
            <span>
              {t("uq.back.text")}. {t("uq.back.why")}
            </span>
          </Bar>
        )}
        {sig && (
          <Bar bad>
            <b>{t(key === "expired" ? "uq.sig.expired.title" : "uq.sig.title")}</b>
            <span>{t(key === "expired" ? "uq.sig.expired.text" : "uq.sig.text", { new: nextVersion, cur: p?.version ?? "" })}</span>
          </Bar>
        )}
        {gh && !run && (
          <Bar
            action={
              <Button variant="secondary" size="md" disabled={actions.busy} onClick={() => void actions.checkPanel()}>
                {t("uq.gh.again")}
              </Button>
            }
          >
            <b>{t("uq.gh.title")}</b>
            <span>{update?.checkedUnix ? t("uq.gh.textAt", { ago: fmt.ago(update.checkedUnix) }) : t("uq.gh.text")}</span>
          </Bar>
        )}
        {hints.length > 0 && !run && <div className="uq-hint">{hints}</div>}
        {!run && (
          <div className="uq-ha">
            {!owner && hasNew && <span className="uq-ro">{t("uq.ro.panel")}</span>}
            <Button variant="secondary" size="lg" disabled={actions.busy} onClick={() => void actions.checkPanel()}>
              {checking && <span className="an-sp" aria-hidden />}
              {checking ? t("uq.gh.busy") : t("up.panel.check")}
            </Button>
            {canUpdate && (
              <Button variant="primary" size="lg" disabled={actions.busy} onClick={() => setWin("update")}>
                {t("up.panel.install")}
              </Button>
            )}
          </div>
        )}
      </section>
      {(hasNew || done) && <ReleaseCard notes={notes} done={done} defaultAll={scene?.more} />}
      <Details data={data} open={detOpen} onToggle={() => setDetTouched(!detOpen)} />
      {owner && !edition.edge && (
        <ChannelDialog open={win === "channel"} onOpenChange={(o) => setWin(o ? "channel" : "")} current={channel} running={running} onPick={actions.setChannel} pick={scene?.pick} />
      )}
      {owner && !off && (
        <PanelUpdateWindow
          open={win === "update"}
          onOpenChange={(o) => setWin(o ? "update" : "")}
          data={data}
          actions={actions}
          onStarted={begin}
        />
      )}
      <PanelTakeover phase={phase} onReload={() => location.reload()} />
    </>
  );
}

