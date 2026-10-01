import { Link } from "@tanstack/react-router";
import { Button, buttonClass } from "@/components/ui/button";
import { StatusDot } from "@/components/ui/status";
import type { StatusKind } from "@/components/ui/status";
import { useT, type T } from "@/i18n";
import { useFmt, type Fmt } from "@/lib/format";
import { cx } from "@/lib/cx";
import { heroOf, isFailurePause, pausedStep, pauseText, progressOf, bundleErrorText, type Hero, type Updates, type UpdateActions } from "@/lib/updates";

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
    case "available":
      return {
        kind: "ok",
        accent: true,
        eyebrow: d.bundle?.built ? t("up.hero.available", { date: fmt.date(d.bundle.built) }) : t("up.hero.availablePlain"),
        title: t("up.hero.available.title", { version }),
        text: t.n("up.hero.available.text", hero.outdated),
      };
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

/** The top card: what is going on with updates, the progress of a running rollout, and the buttons that move it (owner only). */
export function HeroCard({ data, owner, canStart, actions, onStart, onCancel }: { data: Updates; owner: boolean; canStart: boolean; actions: UpdateActions; onStart: () => void; onCancel: () => void }) {
  const t = useT();
  const fmt = useFmt();
  const hero = heroOf(data);
  const l = look(t, fmt, data, hero);
  const active = hero.id === "running" || hero.id === "paused" ? hero.rollout : null;
  const progress = active ? progressOf(active) : null;
  // a node failed and stopped the rollout: look at it first; going on is not the obvious next step then
  const failed = hero.id === "paused" && isFailurePause(hero.rollout.pauseKey);
  const culprit = failed ? pausedStep(hero.rollout) : undefined;
  const hasButtons = (owner && (canStart || !!active)) || !!culprit;

  return (
    <section
      className={cx(
        "flex flex-col gap-3.5 rounded-card-lg border p-4 md:p-5",
        l.accent ? "border-accent-line bg-accent-soft" : l.tint ? "border-warn-line bg-warn-soft" : "border-line bg-surface",
      )}
    >
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
            {culprit && (
              <Link to="/nodes/$id" params={{ id: culprit.nodeId }} search={{ tab: "events" }} className={buttonClass("secondary", "lg")}>
                {t("up.openNode", { name: culprit.nodeName })}
              </Link>
            )}
            {owner && canStart && (
              <Button variant="primary" size="lg" disabled={actions.busy} onClick={onStart}>
                {t("up.startAll")}
              </Button>
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
      {progress && progress.total > 0 && (
        <div className="flex flex-col gap-1.5">
          <div
            role="progressbar"
            aria-valuemin={0}
            aria-valuemax={progress.total}
            aria-valuenow={progress.done}
            aria-label={t.n("up.progress", progress.total, { done: progress.done })}
            className="h-1.5 overflow-hidden rounded-[3px] bg-canvas"
          >
            <div className="h-full rounded-[3px] bg-accent transition-[width] duration-300" style={{ width: `${Math.round((progress.done / progress.total) * 100)}%` }} />
          </div>
          <span className="font-mono text-[11px] text-muted">{t.n("up.progress", progress.total, { done: progress.done })}</span>
        </div>
      )}
      {!owner && (hero.id === "available" || active) && <p className="text-xs text-muted">{t("up.ownerOnly")}</p>}
    </section>
  );
}
