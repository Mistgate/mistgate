import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { isIPAddress, useAddNode } from "@/components/add-node";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon, IconChip } from "@/components/ui/icons";
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

/** The list itself (the dev kit shows it with made-up steps): open while a step is left, one line once all are done. */
export function FirstRunView({ steps, onHide }: { steps: readonly Step[]; onHide: () => void }) {
  const t = useT();
  const done = steps.filter((s) => s.done).length;
  const current = steps.findIndex((s) => !s.done);

  if (current < 0)
    return (
      <section className="screen-enter flex items-center gap-3 rounded-card-lg border border-line bg-surface px-4 py-3">
        <Circle state="done" />
        <p className="min-w-0 flex-1 text-[13px] text-pretty">
          <b className="font-bold">{t("fr.title")}</b> <span className="text-muted">· {t("fr.allDone")}</span>
        </p>
        <Button variant="ghost" size="sm" onClick={onHide}>
          {t("fr.hide")}
        </Button>
      </section>
    );

  return (
    <section aria-labelledby="first-run-title" className="screen-enter flex flex-col gap-4 rounded-card-lg border border-line bg-surface p-4 md:p-5">
      <div className="flex items-center gap-2.5">
        <IconChip icon="sparkle" tone="lavender" />
        <h2 id="first-run-title" className="flex-1 text-[11px] font-bold tracking-[0.1em] text-muted uppercase">
          {t("fr.title")}
        </h2>
        <span className="font-mono text-xs text-muted">{t("fr.progress", { done, total: steps.length })}</span>
      </div>
      <div aria-hidden className="h-1 overflow-hidden rounded-[2px] bg-surface-2">
        <div className="h-full rounded-[2px] bg-accent transition-[width] duration-500" style={{ width: `${(done / steps.length) * 100}%` }} />
      </div>
      <ol className="flex flex-col">
        {steps.map((s, i) => (
          <li key={s.id} className="flex gap-3.5">
            <div className="flex w-7 flex-none flex-col items-center">
              <Circle state={s.done ? "done" : i === current ? "current" : "later"} n={i + 1} />
              {i < steps.length - 1 && <span aria-hidden className="my-1 w-px flex-1 bg-line" />}
            </div>
            <div className={cx("flex min-w-0 flex-1 flex-wrap items-start gap-x-4 gap-y-2.5 pt-1", i < steps.length - 1 && "pb-5")}>
              <div className="flex min-w-[min(100%,260px)] flex-1 flex-col gap-1">
                <span className={cx("text-[15px] font-bold", s.done && "text-muted")}>{s.title}</span>
                <span className="text-[13px] leading-snug text-pretty text-muted">{s.text}</span>
              </div>
              {s.action && <StepButton action={s.action} primary={i === current} />}
            </div>
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
        "box-border grid size-7 flex-none place-items-center rounded-full border font-mono text-[13px] font-bold",
        state === "done" && "tone-ok border-transparent bg-(--c) text-on-accent",
        state === "current" && "border-accent bg-accent text-on-accent",
        state === "later" && "border-line text-muted",
      )}
    >
      {state === "done" ? <Icon name="check" size={14} /> : n}
    </span>
  );
}

function StepButton({ action, primary }: { action: NonNullable<Step["action"]>; primary: boolean }) {
  const addNode = useAddNode();
  const variant = primary ? "primary" : "secondary";
  if (action.disabled)
    return (
      <span className="flex flex-col items-start gap-1">
        <Button variant="secondary" size="md" disabled>
          {action.label}
        </Button>
        <span className="text-xs text-muted">{action.disabled}</span>
      </span>
    );
  const r = action.run;
  const cls = buttonClass(variant, "md");
  switch (r.kind) {
    case "addNode":
    case "newCommand":
      return (
        <Button variant={variant} size="md" onClick={() => addNode(r.kind === "newCommand" ? { id: r.id, name: r.name } : undefined)}>
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
