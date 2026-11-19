import { Link } from "@tanstack/react-router";
import { useEffect, useRef } from "react";
import { playWave } from "@/components/fx/wave";
import { useToast } from "@/components/ui/toast";
import { Button, buttonClass } from "@/components/ui/button";
import { BundleStatus, NodeUpdateState, RolloutStatus } from "@/gen/mistgate/admin/v1/update_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { themeNow } from "@/lib/theme";
import {
  bundleErrorText,
  heroOf,
  isActive,
  isFailurePause,

  pausedStep,
  pauseText,
  progressOf,
  type Hero,
  type NodeUpdate,
  type UpdateActions,
  type Updates,
} from "@/lib/updates";
import { G } from "@/screens/users/user-bits";
import { Bar, D, Pill, type Gk } from "./bits";
import { fleetCards, rolloutNow } from "./fleet";
import { Fleet } from "./fleet-grid";
import { NodesCard } from "./nodes";
import { BundleCard, RolloutHistory } from "./rollout";

// The "Node agents" tab (the board's second): where the fleet stands against the release ("2 of 4 nodes on v0.1.24"), the one
// button that moves it, a card per node, the rollout that is going on (and what to do when it stops), then the nodes in a table
// and, at the foot, the signed bundle and the last rollout.

type Look = { title: string; text: string; pill: { k: Gk; label: string } | null };

/** What the header of the card says for each situation: the board's "N of M nodes on vX" when there is a release to measure by. */
function look(t: T, d: Updates, hero: Hero, edge: boolean): Look {
  const version = d.bundle?.version ?? "";
  const trusted = d.bundle?.status === BundleStatus.TRUSTED;
  const count = trusted ? t.n("up.hero.available.title", d.nodes.length, { done: d.nodes.filter((n) => n.state === NodeUpdateState.UP_TO_DATE).length, version }) : "";
  switch (hero.id) {
    case "available":
      return { title: count, text: t("up.hero.available.text", { version }), pill: null };
    case "running":
      return count
        ? { title: count, text: t("up.hero.available.text", { version }), pill: { k: "busy", label: t("up.layer.pill.running") } }
        : { title: t("up.hero.running.title", { version: hero.rollout.toVersion }), text: t("up.hero.running.text"), pill: { k: "busy", label: t("up.layer.pill.running") } };
    case "paused":
      return { title: count || t("up.hero.paused.title"), text: count ? t("up.hero.available.text", { version }) : "", pill: { k: "part", label: t("up.layer.pill.paused") } };
    case "current":
      return count
        ? { title: count, text: t("uq.ag.allSub", { version }), pill: { k: "ok", label: t("uq.ag.all") } }
        : { title: t("up.hero.current.title"), text: t("up.hero.current.text"), pill: { k: "ok", label: t("uq.ag.all") } };
    case "attention":
      return { title: count || t("up.hero.attention.title"), text: t.n("up.hero.attention.text", hero.retry), pill: { k: "part", label: t("up.layer.pill.attention") } };
    case "manual":
      return {
        title: t.n("up.hero.manual.title", hero.nodes.length, { names: hero.nodes.map((n) => n.name).join(", ") }),
        text: t.n(edge ? "up.hero.manual.textEdge" : "up.hero.manual.text", hero.nodes.length),
        pill: { k: "part", label: t("up.state.unsupported") },
      };
    case "noBundle": {
      const text = edge ? bundleErrorText(t, d.bundle?.errorKey ?? "", d.bundle?.params) : hero.older > 0 ? t.n("up.hero.noBundle.older", hero.older) : t("up.hero.noBundle.text");
      return { title: t("up.hero.noBundle.title"), text, pill: { k: "", label: t("up.bundle.status.missing") } };
    }
    case "untrusted":
      return { title: t("up.hero.untrusted.title"), text: bundleErrorText(t, d.bundle?.errorKey ?? "", d.bundle?.params), pill: { k: "bad", label: t("up.bundle.status.untrusted") } };
    case "noKey":
      return { title: t("up.hero.noKey.title"), text: t(edge ? "up.hero.noKey.edge" : "up.hero.noKey.text"), pill: { k: "part", label: t("up.bundle.status.noKey") } };
    default:
      return { title: t("up.hero.none.title"), text: t("up.hero.none.text"), pill: null };
  }
}

/** What the rollout is doing this moment, on one line, with the buttons that act on it. */
function Now({ d, owner, actions, onCancel }: { d: Updates; owner: boolean; actions: UpdateActions; onCancel: () => void }) {
  const t = useT();
  const r = d.rollout!;
  const now = rolloutNow(r);
  const { done, total } = progressOf(r);
  const text =
    now.kind === "gate" ? t("uq.roll.gate", { node: now.node }) : now.kind === "canary" ? t("uq.roll.canary", { node: now.node, version: r.toVersion }) : now.kind === "batch" ? t("uq.roll.batch", { nodes: now.names, version: r.toVersion }) : t("uq.roll.wait");
  return (
    <div className="uq-now" role="status">
      <span className="an-sp" aria-hidden />
      <div>
        <b>{text}</b>
        <span>{t("uq.roll.sub")}</span>
      </div>
      <em className="mono">{t.n("up.progress", total, { done })}</em>
      {owner && (
        <>
          <Button variant="secondary" size="md" disabled={actions.busy} onClick={() => void actions.pause(r.id)}>
            {t("up.pause")}
          </Button>
          <Button variant="secondary" size="md" disabled={actions.busy} onClick={onCancel}>
            {t("uq.roll.stop")}
          </Button>
        </>
      )}
    </div>
  );
}

/** A paused rollout: the reason (and the node it names, when a node caused it), and the way on. */
function Paused({ d, owner, actions, onCancel }: { d: Updates; owner: boolean; actions: UpdateActions; onCancel: () => void }) {
  const t = useT();
  const r = d.rollout!;
  const failed = isFailurePause(r.pauseKey);
  const culprit = failed ? pausedStep(r) : undefined;
  return (
    <Bar
      bad={failed}
      action={
        <>
          {culprit && (
            <Link to="/nodes/$id" params={{ id: culprit.nodeId }} search={{ tab: "history" }} className={buttonClass("secondary", "md")}>
              {t("up.openNode", { name: culprit.nodeName })}
            </Link>
          )}
          {owner && (
            <Button variant="secondary" size="md" disabled={actions.busy} onClick={() => void actions.resume(r.id)}>
              {t("up.resume")}
            </Button>
          )}
          {owner && (
            <Button variant="secondary" size="md" disabled={actions.busy} onClick={onCancel}>
              {t("up.cancel")}
            </Button>
          )}
        </>
      }
    >
      <b>{t("up.hero.paused.title")}</b>
      <span>{pauseText(t, r.pauseKey, r.pauseParams)}</span>
    </Bar>
  );
}

/** Nodes that are not reachable and have an update waiting: "x is offline", and the schedule that will catch up with them. */
function Offline({ nodes }: { nodes: readonly NodeUpdate[] }) {
  const t = useT();
  if (nodes.length === 0) return null;
  const first = nodes[0]!;
  return (
    <Bar
      action={
        <Link to="/nodes/$id" params={{ id: first.nodeId }} className={buttonClass("secondary", "md")}>
          {t("up.openNode", { name: first.name })}
        </Link>
      }
    >
      <b>{t.n("uq.off.title", nodes.length, { names: nodes.map((n) => n.name).join(", ") })}</b>
      <span>{t.n("uq.off.text", nodes.length)}</span>
    </Bar>
  );
}

/** What a scene of the dev kit puts in place of what the cards would find out by themselves. */
export type AgentsScene = { last?: boolean; reading?: boolean; matrixOpen?: boolean };

export type AgentsProps = {
  data: Updates;
  owner: boolean;
  actions: UpdateActions;
  onRollout: () => void;
  onCancel: () => void;
  onSchedule: (n: NodeUpdate) => void;
  onRollback: (n: NodeUpdate) => void;
  scene?: AgentsScene;
};

export function AgentsTab({ data: d, owner, actions, onRollout, onCancel, onSchedule, onRollback, scene }: AgentsProps) {
  const t = useT();
  const edition = useEdition();
  const hero = heroOf(d);
  const l = look(t, d, hero, edition.edge);
  const active = isActive(d.rollout) ? d.rollout : null;
  const cards = fleetCards(d);
  const trusted = d.bundle?.status === BundleStatus.TRUSTED;
  const offline = trusted ? d.nodes.filter((n) => n.state === NodeUpdateState.OFFLINE && n.supportsUpdate && n.built < d.bundle!.built) : [];
  const canStart = owner && hero.id === "available" && !!d.bundle;
  const readOnly = !owner && (hero.id === "available" || !!active);
  // a rollout that ends while the page is open: the wave from the card and a word
  const toast = useToast();
  const card = useRef<HTMLElement>(null);
  const status = d.rollout?.status;
  const was = useRef(status);
  const toVersion = d.rollout?.toVersion ?? "";
  useEffect(() => {
    if (was.current === RolloutStatus.RUNNING && status === RolloutStatus.DONE) {
      toast(t("uq.toast.rolled", { version: toVersion }));
      const r = card.current?.getBoundingClientRect();
      if (r) playWave({ x: r.left + 120, y: r.top + 60 }, { light: themeNow() === "light" });
    }
    was.current = status;
  }, [status, toVersion, toast, t]);

  if (d.nodes.length === 0) {
    return (
      <>
        <section className="uq-card us-none hl-calm rc-emp">
          <span className="g" aria-hidden>
            <G d={D.server} size={20} stroke={1.75} />
          </span>
          <b>{t("up.nodes.empty")}</b>
          <span>{t("uq.none.text")}</span>
          <Link to="/nodes" className={buttonClass("secondary", "lg")}>
            {t("uq.none.open")}
          </Link>
        </section>
      </>
    );
  }
  return (
    <>
      <section ref={card} className={cx("uq-card uq-ag", active?.status === RolloutStatus.RUNNING && "run")} aria-label={t("up.tab.agents")}>
        <div className="uq-agh">
          <div>
            <b>{l.title}</b>
            {l.text && <span>{l.text}</span>}
          </div>
          {canStart && hero.id === "available" && (
            <Button variant="primary" size="lg" disabled={actions.busy} onClick={onRollout}>
              {t.n("up.rollout.start", hero.outdated)}
            </Button>
          )}
          {l.pill && !(canStart && hero.id === "available") && <Pill k={l.pill.k}>{l.pill.label}</Pill>}
        </div>
        <Fleet cards={cards} version={trusted ? d.bundle!.version : ""} unfolded={scene?.matrixOpen} />
        {readOnly && <p className="uq-ro">{t("uq.ro.agents")}</p>}
        {active?.status === RolloutStatus.RUNNING && <Now d={d} owner={owner} actions={actions} onCancel={onCancel} />}
        {active?.status === RolloutStatus.PAUSED && <Paused d={d} owner={owner} actions={actions} onCancel={onCancel} />}
        <Offline nodes={offline} />
      </section>
      <NodesCard nodes={d.nodes} data={d} owner={owner} rollout={active} onSchedule={onSchedule} onRollback={onRollback} />
      <div className="uq-two">
        {!edition.edge && <BundleCard data={d} owner={owner} actions={actions} reading={scene?.reading} />}
        {d.rollout && <RolloutHistory rollout={d.rollout} defaultOpen={scene?.last} />}
      </div>
    </>
  );
}

