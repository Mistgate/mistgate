import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { BundleStatus, RolloutStatus, StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { triedText } from "@/lib/update-sources";
import { heroOf, isActive, progressOf, rolloutInfo, skippedProfilesNote, stagesOf, stepErrorText, stepInfo, vantageNote, type Rollout, type Step, type UpdateActions, type Updates } from "@/lib/updates";
import { BundleDetails, bundleStates } from "./cards";
import { CardHead, D, NodeName, Pill, gk, useFlags, w } from "./bits";

// The two cards at the foot of the agents tab: the signed release bundle (the panel installs only what is signed) and the last
// rollout (the stages it went through, one row a node). A rollout that is going on opens by itself.

/** The signed bundle in one line (does the panel trust it, which version, built and valid until), "Read the folder again" and the details folded away. */
export function BundleCard({ data, owner, actions, reading: readingNow }: { data: Updates; owner: boolean; actions: UpdateActions; /** The dev kit shows the folder being read. */ reading?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const b = data.bundle;
  const status = b?.status ?? BundleStatus.MISSING;
  const s = bundleStates[status];
  const missing = status === BundleStatus.MISSING || status === BundleStatus.UNSPECIFIED;
  const [touched, setTouched] = useState<boolean | null>(null);
  const [readingOwn, setReading] = useState(false);
  const reading = readingOwn || !!readingNow;
  // a bundle that is missing or not trusted opens by itself: that is where its reason and the steps to fix it are
  const open = touched ?? status !== BundleStatus.TRUSTED;
  const expired = !!b && b.expiresUnix > 0 && b.expiresUnix <= data.nowUnix;
  const pill = status === BundleStatus.UNTRUSTED && b?.errorKey.endsWith(".bad_signature") ? t("up.bundle.status.badSignature") : t(s.key);
  const facts = b && !missing ? [b.version, b.built ? t("up.built", { date: fmt.date(b.built) }) : "", b.expiresUnix ? (expired ? t("up.bundle.expired", { date: fmt.date(b.expiresUnix) }) : t("up.bundle.validUntil", { date: fmt.date(b.expiresUnix) })) : ""].filter(Boolean) : [];
  return (
    <section className="uq-card pp-sec" aria-label={t("up.bundle.title")}>
      <CardHead icon={D.key} tone="mint" title={t("up.bundle.title")} />
      <div className="uq-pk">
        <Pill k={gk(s.kind)}>{pill}</Pill>
        {facts.length > 0 && <span className={cx("mono", expired && "uq-lb")}>{facts.join(" · ")}</span>}
        {facts.length === 0 && <span />}
        {owner && (
          <Button
            variant="secondary"
            size="md"
            disabled={actions.busy || reading}
            onClick={() => {
              setReading(true);
              void actions.rescan().finally(() => setReading(false));
            }}
          >
            {reading && <span className="an-sp" aria-hidden />}
            {reading ? t("uq.pk.busy") : t("up.bundle.rescan")}
          </Button>
        )}
        <button type="button" className="an-lk nd-lk" aria-expanded={open} aria-controls="up-bundle-details" onClick={() => setTouched(!open)}>
          {t("up.bundle.details")}
        </button>
      </div>
      {open && (
        <div id="up-bundle-details" className="uq-bf">
          <BundleDetails data={data} hideError={heroOf(data).id === "untrusted"} />
        </div>
      )}
      <p className="sb-vfoot">{t("uq.pk.foot")}</p>
    </section>
  );
}

const downloadPhaseKeys: Record<string, Parameters<T>[0]> = {
  accepted: "up.download.phase.accepted",
  downloading: "up.download.phase.downloading",
  verifying: "up.download.phase.verifying",
  installing: "up.download.phase.installing",
  restarting: "up.download.phase.restarting",
  completed: "up.download.phase.completed",
  failed: "up.download.phase.failed",
};

function size(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(0)} KiB`;
  return `${(n / 1024 ** 2).toFixed(1)} MiB`;
}

/** A node whose update is under way and reports how far it got: a line with the phase, the share and the speed. */
const showsProgress = (step: Step) => !!step.downloadPhase && step.state === StepState.SENT;

function DownloadProgress({ step }: { step: Step }) {
  const t = useT();
  if (!showsProgress(step)) return null;
  const total = step.totalBytes;
  const done = Math.min(step.downloadedBytes, total || step.downloadedBytes);
  const percent = total > 0 ? Math.min(100, Math.round((done / total) * 100)) : 0;
  const downloading = step.downloadPhase === "downloading";
  return (
    <span role="progressbar" aria-label={t("up.download.aria", { percent })} aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} className="flex flex-col gap-0.5">
      <small>
        {t(downloadPhaseKeys[step.downloadPhase] ?? "up.download.phase.downloading")} ·{" "}
        {total ? `${percent}%${downloading ? ` · ${size(step.bytesPerSecond)}/s` : ""}` : t("up.download.phase.waiting")}
        {/* the panel is the usual source and says nothing; only the fallback is worth a word */}
        {total > 0 && downloading && step.downloadSource === "github" && ` · ${t("up.downloadSource.fromGithub")}`}
        {total > 0 && ` · ${size(done)} / ${size(total)}`}
      </small>
      <i className="rw-pg" style={w(percent)} aria-hidden />
    </span>
  );
}

function stageName(t: T, stage: number, kind: "canary" | "batch" | "skipped") {
  return kind === "skipped" ? t("up.ro.skipped") : kind === "canary" ? t("up.ro.canary") : t("up.ro.batch", { n: stage });
}

/** The stages of a rollout, a row a node: the node, its stage, what happened to it, and why when it did not go well. */
function Stages({ rollout }: { rollout: Rollout }) {
  const t = useT();
  const flags = useFlags();
  const stages = stagesOf(rollout.steps);
  const noProbe = useEdition().off("health_checks"); // the gate there has no client-eye check yet
  return (
    <>
      <div className="uq-ls">
        {stages.flatMap((st) =>
          st.steps.map((s) => {
            const info = stepInfo(s.state);
            const label = s.state === StepState.PASSED ? t("up.step.to", { version: rollout.toVersion }) : t(info.key);
            const note = vantageNote(t, s.nodeName, s.params, s.state === StepState.PASSED);
            const skipped = skippedProfilesNote(t, s.params);
            const sentence = s.errorKey ? stepErrorText(t, s.errorKey, s.params) : "";
            const tried = s.errorKey.endsWith(".download_failed") ? triedText(t, s.params.sources_failed) : "";
            return (
              <div key={s.nodeId}>
                <NodeName name={s.nodeName} countryCode={flags.get(s.nodeId)} to={<Link to="/nodes/$id" params={{ id: s.nodeId }} className="mono hover:underline"><b>{s.nodeName}</b></Link>} />
                <em>{stageName(t, st.stage, st.kind)}</em>
                <span className="uq-lq">
                  <span className={cx(info.kind === "bad" && "uq-lb", info.kind === "warn" && "uq-lx")}>{label}</span>
                  <DownloadProgress step={s} />
                  {sentence && <small className={info.kind === "bad" ? "bad" : undefined}>{sentence}</small>}
                  {tried && <small>{tried}</small>}
                  {note && <small>{note}</small>}
                  {skipped && <small>{skipped}</small>}
                </span>
              </div>
            );
          }),
        )}
      </div>
      <p className="uq-lsn">
        {t("up.ro.safety")}
        {noProbe && ` ${t("up.gate.noProbe")}`}
      </p>
    </>
  );
}

/**
 * The rollout in progress, or the last one: its release, when it began, how many nodes are done and the state; the stages are one
 * click away (a running rollout, and one that ended badly, show them at once).
 */
export function RolloutHistory({ rollout, defaultOpen }: { rollout: Rollout; /** The dev kit shows the stages of a finished rollout open. */ defaultOpen?: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const info = rolloutInfo(rollout.status);
  const active = isActive(rollout);
  const { done, total } = progressOf(rollout);
  const [touched, setTouched] = useState<boolean | null>(null);
  const open = touched ?? (defaultOpen || active || rollout.status === RolloutStatus.FAILED);
  return (
    <section className="uq-card pp-sec" aria-label={active ? t("up.ro.title") : t("up.ro.titleLast")}>
      <CardHead icon={D.history} tone="sand" title={active ? t("up.ro.title") : t("up.ro.titleLast")} />
      <div className="uq-pk">
        <Pill k={gk(info.kind)}>{t(info.key)}</Pill>
        <span>
          {t("up.ro.sub", { version: rollout.toVersion, ago: fmt.ago(rollout.createdUnix) })}
          {total > 0 && ` · ${t.n("up.progress", total, { done })}`}
        </span>
        {!active && (
          <Button variant="secondary" size="md" aria-expanded={open} onClick={() => setTouched(!open)}>
            {open ? t("up.ro.hide") : t("up.ro.show")}
          </Button>
        )}
      </div>
      {open && <Stages rollout={rollout} />}
    </section>
  );
}

