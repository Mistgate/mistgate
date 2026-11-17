import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { isIPAddress, useAddNode } from "@/components/add-node";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { Node } from "@/gen/mistgate/admin/v1/node_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { useT, type T } from "@/i18n";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { hasConnected } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { nodesQuery, userCountQuery } from "@/lib/queries";
import { readPref, writePref } from "@/lib/storage";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { OvDot } from "./overview-parts";
import "./overview.css";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";

// "First run": from an empty panel to a friend who connects, as a checklist on the Overview. Every item is computed from
// what the panel already has (nodes, profiles, groups, users), so it never asks the owner to tick anything; it stays
// until everything is done, then folds into one line he can hide.

export type StepId = "node" | "profile" | "group" | "user";

/** What a step's button does. */
export type StepAction =
  | { kind: "addNode" }
  | { kind: "newCommand"; id: string; name: string }
  | { kind: "profiles"; id: string }
  | { kind: "groups" }
  | { kind: "createUser" };

export type Step = {
  id: StepId;
  done: boolean;
  title: string;
  text: string;
  action?: { label: string; run: StepAction; disabled?: string };
};

type Input = {
  nodes: readonly Pick<Plain<Node>, "id" | "name" | "address" | "status" | "reason">[];
  profiles: readonly Pick<ProfileSummary, "id" | "name" | "nodeCount">[];
  groups: readonly Pick<Group, "name" | "profileIds">[];
  users: number;
  /** The subscription apps by name (useLinkAppNames); the generic words when absent. */
  linkApps?: string;
};

/** The four steps from the panel's data; `clock` writes the time an install command ends ("19:27"). Pure: the rules are tested. */
export function firstRunSteps(t: T, clock: (unix: number) => string, { nodes, profiles, groups, users, linkApps }: Input): Step[] {
  const apps = linkApps || t("subs.kind.happ");
  const connected = nodes.filter(hasConnected);
  const pending = nodes.filter((n) => n.status === NodeStatus.PENDING);
  const deployed = profiles.filter((p) => p.nodeCount > 0);
  const deployedIds = new Set(deployed.map((p) => p.id));
  const group = groups.find((g) => g.profileIds.some((id) => deployedIds.has(id)));
  // the node that needs a profile: one the server says has none, else the first that ever connected
  const target = connected.find((n) => n.reason?.code === "no_profiles") ?? connected[0];

  let node: Step;
  if (connected.length > 0) {
    node = {
      id: "node",
      done: true,
      title: t("fr.node.title"),
      text: connected.length === 1 ? t("fr.node.done", { name: connected[0]!.name }) : t.n("fr.node.doneMany", connected.length),
    };
  } else if (pending[0]) {
    const p = pending[0];
    const until = Number(p.reason?.params.expires_unix ?? 0);
    node = {
      id: "node",
      done: false,
      title: t("fr.node.title"),
      text: p.reason?.code === "enrollment_pending" && until > 0 ? t("fr.node.pending", { name: p.name, time: clock(until) }) : t("fr.node.expired", { name: p.name }),
      action: { label: t("node.banner.newCommand"), run: { kind: "newCommand", id: p.id, name: p.name } },
    };
  } else {
    node = { id: "node", done: false, title: t("fr.node.title"), text: t("fr.node.none"), action: { label: t("node.add.button"), run: { kind: "addNode" } } };
  }

  const certText = target && isIPAddress(target.address) ? t("fr.profile.ip", { name: target.name }) : t("fr.profile.cert");
  const profile: Step = deployed.length
    ? {
        id: "profile",
        done: true,
        title: t("fr.profile.title"),
        text: deployed.length === 1 ? t.n("fr.profile.done", deployed[0]!.nodeCount, { profile: deployed[0]!.name }) : t.n("fr.profile.doneMany", deployed.length),
      }
    : {
        id: "profile",
        done: false,
        title: t("fr.profile.title"),
        text: `${t("fr.profile.text", { apps })} ${certText}`,
        action: target
          ? { label: t("fr.profile.add", { name: target.name }), run: { kind: "profiles", id: target.id } }
          : { label: t("node.profiles.add"), run: { kind: "addNode" }, disabled: t("fr.profile.needNode") },
      };

  const groupStep: Step = group
    ? {
        id: "group",
        done: true,
        title: t("fr.group.title"),
        text: t.n("fr.group.done", group.profileIds.filter((id) => deployedIds.has(id)).length, { group: group.name }),
      }
    : { id: "group", done: false, title: t("fr.group.title"), text: t("fr.group.text"), action: { label: t("fr.group.open"), run: { kind: "groups" } } };

  const user: Step =
    users > 0
      ? { id: "user", done: true, title: t("fr.user.title"), text: t.n("fr.user.done", users) }
      : { id: "user", done: false, title: t("fr.user.title"), text: t("fr.user.text", { apps }), action: { label: t("fr.user.create"), run: { kind: "createUser" } } };

  return [node, profile, groupStep, user];
}

const hiddenKey = "firstRunHidden";

/**
 * The checklist's steps from the panel's data; null while it loads or after the owner hid the finished list. `open` is
 * the action of the first step left (the Overview strip leaves that button to the list).
 */
export function useFirstRun(): { steps: Step[]; open: StepAction | null; hide: () => void } | null {
  const t = useT();
  const fmt = useFmt();
  const nodes = useQuery(nodesQuery);
  const profiles = useQuery(profileListQuery);
  const groups = useQuery(groupsQuery);
  const users = useQuery(userCountQuery);
  const linkApps = useLinkAppNames();
  const [hidden, setHidden] = useState(() => readPref(hiddenKey) === "1");
  if (hidden || !nodes.data || !profiles.data || !groups.data || !users.data) return null;
  const steps = firstRunSteps(t, fmt.clock, {
    nodes: nodes.data.nodes,
    profiles: profiles.data,
    groups: groups.data,
    users: users.data.counts?.all ?? 0,
    linkApps,
  });
  return {
    steps,
    open: steps.find((s) => !s.done)?.action?.run ?? null,
    hide: () => {
      writePref(hiddenKey, "1");
      setHidden(true);
    },
  };
}

/**
 * The card itself (the dev kit shows it with made-up steps): while a step is left, the board's two columns, the step to take on
 * the left with its button and the whole list on the right; one line once all are done. `empty` is a panel with no node at all:
 * the card then says the panel is ready instead of counting steps.
 */
export function FirstRunView({ steps, onHide, empty = false }: { steps: readonly Step[]; onHide: () => void; empty?: boolean }) {
  const t = useT();
  const done = steps.filter((s) => s.done).length;
  const current = steps.findIndex((s) => !s.done);

  if (current < 0)
    return (
      <section className="screen-enter flex items-center gap-3 rounded-card border border-line bg-surface px-6 py-3 max-md:px-4">
        <Circle state="done" />
        <p className="min-w-0 flex-1 text-[13px] text-pretty">
          <b className="font-medium">{t("fr.title")}</b> <span className="text-muted">· {t("fr.allDone")}</span>
        </p>
        <Button variant="ghost" size="sm" onClick={onHide}>
          {t("fr.hide")}
        </Button>
      </section>
    );

  const step = steps[current]!;
  const id = step.id;
  return (
    <section aria-labelledby="first-run-title" className="ov-first screen-enter relative grid overflow-hidden rounded-card border border-line bg-surface">
      <div className="relative flex min-w-0 flex-col items-start px-8 pt-7 pb-8 max-md:p-5">
        <span className="flex flex-wrap items-center gap-2 text-[13px] text-sec">
          <OvDot kind="ok" />
          <b className="font-medium text-fg">{t(empty ? "ov.first.ready" : "ov.first.setup")}</b>
          {` · ${empty ? t("ov.first.noNodes") : t("fr.progress", { done, total: steps.length })}`}
        </span>
        <h2 id="first-run-title" className="mt-3 text-[32px] leading-9 font-medium tracking-[-0.02em] max-md:text-2xl max-md:leading-7">
          {t(`ov.first.${id}.head`)} <span className="text-sec">{t(`ov.first.${id}.tail`)}</span>
        </h2>
        <p className="mt-3 max-w-[520px] text-sec">{step.text}</p>
        {step.action && (
          <div className="mt-8 flex flex-wrap gap-2 max-md:w-full max-md:[&>*]:flex-[1_1_100%]">
            <StepButton action={step.action} variant="primary" size="lg" />
          </div>
        )}
      </div>
      <ol aria-label={t("fr.title")} className="ov-steps relative m-0 flex list-none flex-col justify-center border-s border-line bg-step py-2">
        {steps.map((s, i) => (
          <li key={s.id} aria-current={i === current ? "step" : undefined} className="relative grid grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 px-6 py-4 max-md:px-4 max-md:py-3">
            <Circle state={s.done ? "done" : i === current ? "current" : "later"} n={i + 1} />
            <div className="min-w-0">
              <b className={cx("block font-medium", i === current ? "text-fg" : "text-sec")}>{s.title}</b>
              <span className="block text-[13px] text-muted">{s.done ? s.text : t(`ov.first.${s.id}.hint`)}</span>
            </div>
            {/* a step that is not the next one still has its button, quiet, when it can be taken */}
            {!s.done && i !== current && s.action && !s.action.disabled && <StepButton action={s.action} variant="ghost" size="sm" />}
          </li>
        ))}
      </ol>
    </section>
  );
}

function Circle({ state, n }: { state: "done" | "current" | "later"; n?: number }) {
  return (
    <span
      className={cx(
        "box-border grid size-8 flex-none place-items-center rounded-full border font-mono text-[13px] font-medium",
        state === "done" && "border-transparent bg-tone-mint-bg text-tone-mint",
        state === "current" && "border-accent-mark text-fg",
        state === "later" && "border-line text-muted",
      )}
    >
      {state === "done" ? <Icon name="check" size={16} strokeWidth={2} /> : n}
    </span>
  );
}

function StepButton({ action, variant, size }: { action: NonNullable<Step["action"]>; variant: "primary" | "ghost"; size: "lg" | "sm" }) {
  const addNode = useAddNode();
  if (action.disabled)
    return (
      <span className="flex flex-col items-start gap-1">
        <Button variant="secondary" size={size} disabled>
          {action.label}
        </Button>
        <span className="text-xs text-muted">{action.disabled}</span>
      </span>
    );
  const r = action.run;
  const cls = buttonClass(variant, size);
  switch (r.kind) {
    case "addNode":
    case "newCommand":
      return (
        <Button variant={variant} size={size} onClick={() => addNode(r.kind === "newCommand" ? { id: r.id, name: r.name } : undefined)}>
          {action.label}
        </Button>
      );
    case "profiles":
      return (
        <Link to="/nodes/$id" params={{ id: r.id }} search={{ tab: "profiles" }} className={cls}>
          {action.label}
        </Link>
      );
    case "groups":
      return (
        <Link to="/users" search={{ tab: "groups" }} className={cls}>
          {action.label}
        </Link>
      );
    case "createUser":
      return (
        <Link to="/users" search={{ create: true }} className={cls}>
          {action.label}
        </Link>
      );
  }
}

/** Ghost previews of what an empty panel will show (the board's three tiles): the nodes with their checks, the chart, the feed. */
export function SoonCards() {
  const t = useT();
  const cell = "relative flex min-w-0 flex-col gap-1 overflow-hidden rounded-card border border-line bg-[color-mix(in_srgb,var(--surface)_50%,transparent)] px-5 pt-4 pb-5";
  const title = "mt-4 font-medium text-fg";
  const text = "text-[13px] text-muted";
  return (
    <div className="grid grid-cols-3 gap-4 max-md:grid-cols-1">
      <div className={cell}>
        <div aria-hidden className="ov-so-v">
          {[false, true, false].map((short, r) => (
            <div key={r} className="ov-so-r">
              <i className="a" />
              <i className={cx("b", short && "s")} />
              <span className="ov-so-tk">
                {Array.from({ length: 14 }, (_, i) => (
                  <i key={i} />
                ))}
              </span>
            </div>
          ))}
        </div>
        <b className={title}>{t("ov.soon.nodes.title")}</b>
        <span className={text}>{t("ov.soon.nodes.text")}</span>
      </div>
      <div className={cell}>
        <div aria-hidden className="ov-so-v">
          <svg viewBox="0 0 320 88" preserveAspectRatio="none">
            <path className="f" d="M0 70C40 66 56 40 96 44s52 26 92 14 56-40 92-36 28 14 40 12V88H0z" />
            <path className="l" d="M0 70C40 66 56 40 96 44s52 26 92 14 56-40 92-36 28 14 40 12" />
            <path className="g" d="M0 22h320M0 44h320M0 66h320" />
          </svg>
        </div>
        <b className={title}>{t("ov.soon.traffic.title")}</b>
        <span className={text}>{t("ov.soon.traffic.text")}</span>
      </div>
      <div className={cell}>
        <div aria-hidden className="ov-so-v">
          {["", "s", "m"].map((w) => (
            <div key={w} className="ov-so-e">
              <i className="d" />
              <i className={cx("b", w)} />
              <i className="t" />
            </div>
          ))}
        </div>
        <b className={title}>{t("ov.soon.events.title")}</b>
        <span className={text}>{t("ov.soon.events.text")}</span>
      </div>
    </div>
  );
}

/** "/?add=1" (the end of the setup wizard) opens the add-node window once, then drops the parameter. */
export function useAddNodeFromUrl() {
  const search = useSearch({ strict: false }) as { add?: 1 };
  const navigate = useNavigate();
  const addNode = useAddNode();
  useEffect(() => {
    if (!search.add) return;
    addNode();
    void navigate({ to: "/", search: {}, replace: true });
  }, [search.add, addNode, navigate]);
}
