import { Link } from "@tanstack/react-router";
import { Button, buttonClass } from "@/components/ui/button";
import { StatusDot } from "@/components/ui/status";
import type { StatusKind } from "@/components/ui/status";
import { StepState } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { useFmt, type Fmt } from "@/lib/format";
import { cx } from "@/lib/cx";
import {
  fleetCounts,
  heroOf,
  isFailurePause,
  nodeStateKind,
  pausedStep,
  pauseText,
  progressOf,
  stepInfo,
  bundleErrorText,
  type Hero,
  type NodeUpdate,
  type Rollout,
  type Updates,
  type UpdateActions,
} from "@/lib/updates";
import { BundleStrip } from "./cards";

type Look = { kind: StatusKind; accent?: boolean; tint?: boolean; eyebrow: string; title: string; text: string };

/** What the hero says for each situation: the page leads with the one thing the owner can do now. */
function look(t: T, fmt: Fmt, d: Updates, hero: Hero): Look {
  const version = d.bundle?.version ?? "";
  switch (hero.id) {
    case "running":
      return { kind: "busy", accent: true, eyebrow: t("up.hero.running"), title: t("up.hero.running.title", { version: hero.rollout.toVersion }), text: t("up.hero.running.text") };
    case "paused":
      return { kind: "warn", tint: true, eyebrow: t("up.hero.paused"), title: t("up.hero.paused.title"), text: pauseText(t, hero.rollout.pauseKey, hero.rollout.pauseParams) };
    case "manual":
      return {
        kind: "warn",
        tint: true,
        eyebrow: t("up.hero.manual"),
        title: t.n("up.hero.manual.title", hero.nodes.length, { names: hero.nodes.map((n) => n.name).join(", ") }),
        text: t.n("up.hero.manual.text", hero.nodes.length),
      };
    case "available": {
      // "2 of 4 nodes on v0.1.24": how far the fleet is, and the one thing left to do
      const done = d.nodes.length - hero.outdated;
      return {
        kind: "ok",
        accent: true,
        eyebrow: d.bundle?.built ? t("up.hero.available", { date: fmt.date(d.bundle.built) }) : t("up.hero.availablePlain"),
        title: t.n("up.hero.available.title", d.nodes.length, { done, version }),
        text: t("up.hero.available.text", { version }),
      };
    }
    case "noBundle":
      return { kind: "off", eyebrow: t("up.hero.noBundle"), title: t("up.hero.noBundle.title"), text: hero.older > 0 ? t.n("up.hero.noBundle.older", hero.older) : t("up.hero.noBundle.text") };
    case "untrusted":
      return { kind: "bad", eyebrow: t("up.hero.untrusted"), title: t("up.hero.untrusted.title"), text: bundleErrorText(t, d.bundle?.errorKey ?? "", d.bundle?.params) };
    case "noKey":
      return { kind: "warn", eyebrow: t("up.hero.noKey"), title: t("up.hero.noKey.title"), text: t("up.hero.noKey.text") };
    case "attention":
      return { kind: "warn", eyebrow: t("up.hero.attention"), title: t("up.hero.attention.title"), text: t.n("up.hero.attention.text", hero.retry) };
    case "none":
      return { kind: "off", eyebrow: t("up.hero.none"), title: t("up.hero.none.title"), text: t("up.hero.none.text") };
    default:
      return { kind: "ok", eyebrow: t("up.hero.current"), title: t("up.hero.current.title"), text: t("up.hero.current.text") };
  }
}

const segment: Record<StatusKind, string> = {
  ok: "bg-ok",
  warn: "bg-warn",
  bad: "bg-danger",
  busy: "bg-busy animate-pulse",
  off: "bg-faint",
  blip: "bg-faint",
};

/**
 * One slim segment per node, coloured by where it stands against the release: the whole fleet in one look, with the
 * count of each group under it. The name and state of a node are on its segment's hover; the table has the rest.
 */
function FleetBar({ nodes }: { nodes: readonly NodeUpdate[] }) {
  const t = useT();
  const c = fleetCounts(nodes);
  const all: { n: number; kind: StatusKind; text: string }[] = [
    { n: c.ok, kind: "ok", text: t.n("up.fleet.ok", c.ok) },
    { n: c.outdated, kind: "warn", text: t.n("up.fleet.outdated", c.outdated) },
    { n: c.updating, kind: "busy", text: t.n("up.fleet.updating", c.updating) },
    { n: c.attention, kind: "bad", text: t.n("up.fleet.attention", c.attention) },
    { n: c.manual, kind: "warn", text: t.n("up.fleet.manual", c.manual) },
    { n: c.offline, kind: "off", text: t.n("up.fleet.offline", c.offline) },
  ];
  const legend = all.filter((l) => l.n > 0);
  return (
    <div className="flex flex-col gap-2">
      <div role="img" aria-label={`${t("up.fleet.aria")}: ${legend.map((l) => l.text).join(", ")}`} className="flex h-2 gap-[3px]">
        {nodes.map((n) => (
          <span key={n.nodeId} title={n.name} className={cx("h-full min-w-1.5 flex-1 rounded-full", segment[nodeStateKind(n.state)])} />
        ))}
      </div>
      <ul className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted">
        {legend.map((l) => (
          <li key={l.kind + l.text} className="flex items-center gap-1.5">
            <StatusDot kind={l.kind} />
            {l.text}
          </li>
        ))}
      </ul>
    </div>
  );
}

/** The running rollout in one bar: a segment per node it goes through, in the colour of its step. */
function RolloutBar({ rollout }: { rollout: Rollout }) {
  const t = useT();
  const { done, total } = progressOf(rollout);
  const steps = rollout.steps.filter((s) => s.state !== StepState.SKIPPED);
  if (total === 0) return null;
  const label = t.n("up.progress", total, { done });
  return (
    <div className="flex flex-col gap-2">
      <div role="progressbar" aria-valuemin={0} aria-valuemax={total} aria-valuenow={done} aria-label={label} className="flex h-2 gap-[3px]">
        {steps.map((s) => (
          <span key={s.nodeId} title={s.nodeName} className={cx("h-full min-w-1.5 flex-1 rounded-full transition-colors duration-300", segment[stepInfo(s.state).kind])} />
        ))}
      </div>
      <span className="font-mono text-[11px] text-muted">{label}</span>
    </div>
  );
}

/**
 * The top card: where the fleet stands against the release ("2 of 4 nodes on v0.1.24"), the one button that moves it (update
 * the rest, canary first; or pause / resume / cancel while a rollout runs) and, at its foot, the signed release bundle the
 * update comes from, its details folded away.
 */
export function HeroCard({
  data,
  owner,
  actions,
  onCancel,
  onRollout,
}: {
  data: Updates;
  owner: boolean;
  actions: UpdateActions;
  onCancel: () => void;
  onRollout: () => void;
}) {
  const t = useT();
  const fmt = useFmt();
  const hero = heroOf(data);
  const l = look(t, fmt, data, hero);
  const active = hero.id === "running" || hero.id === "paused" ? hero.rollout : null;
  // a node failed and stopped the rollout: look at it first; going on is not the obvious next step then
  const failed = hero.id === "paused" && isFailurePause(hero.rollout.pauseKey);
  const culprit = failed ? pausedStep(hero.rollout) : undefined;
  const canStart = owner && hero.id === "available" && !!data.bundle;
  const hasButtons = (owner && !!active) || !!culprit || canStart;

  return (
    <section
      className={cx(
        "flex flex-col overflow-hidden rounded-card-lg border",
        l.accent ? "border-accent-line bg-accent-soft" : l.tint ? "border-warn-line bg-warn-soft" : "border-line bg-surface",
      )}
    >
      <div className="flex flex-col gap-4 p-4 md:p-5">
        <div className="flex flex-wrap items-start gap-4">
          <div className="flex min-w-[240px] flex-1 flex-col gap-1.5">
            <span className="flex items-center gap-2 text-xs font-bold tracking-[0.04em] text-muted">
              <StatusDot kind={l.kind} />
              {l.eyebrow}
            </span>
            <h2 className="text-[22px] leading-tight font-extrabold tracking-[-0.03em] text-pretty">{l.title}</h2>
            {l.text && <p className="max-w-[640px] text-[13px] leading-normal text-pretty text-muted">{l.text}</p>}
          </div>
          {hasButtons && (
            <div className="flex flex-wrap gap-2">
              {canStart && hero.id === "available" && (
                <Button variant="primary" size="lg" disabled={actions.busy} onClick={onRollout}>
                  {t.n("up.rollout.start", hero.outdated)}
                </Button>
              )}
              {culprit && (
                <Link to="/nodes/$id" params={{ id: culprit.nodeId }} search={{ tab: "events" }} className={buttonClass("secondary", "lg")}>
                  {t("up.openNode", { name: culprit.nodeName })}
                </Link>
              )}
              {owner && active && hero.id === "running" && (
                <Button variant="secondary" size="lg" disabled={actions.busy} onClick={() => void actions.pause(active.id)}>
                  {t("up.pause")}
                </Button>
              )}
              {owner && active && hero.id === "paused" && (
                <Button variant={failed ? "secondary" : "primary"} size="lg" disabled={actions.busy} onClick={() => void actions.resume(active.id)}>
                  {t("up.resume")}
                </Button>
              )}
              {owner && active && (
                <Button variant="ghost" size="lg" disabled={actions.busy} onClick={onCancel}>
                  {t("up.cancel")}
                </Button>
              )}
            </div>
          )}
        </div>
        {active ? <RolloutBar rollout={active} /> : data.nodes.length > 0 && <FleetBar nodes={data.nodes} />}
        {!owner && (hero.id === "available" || active) && <p className="text-xs text-muted">{t("up.ownerOnly")}</p>}
      </div>
      <BundleStrip data={data} owner={owner} actions={actions} />
    </section>
  );
}

