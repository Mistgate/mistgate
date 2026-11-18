import { Code, ConnectError } from "@connectrpc/connect";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { useT, type T } from "@/i18n";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { modeOf, receivingIds, shareOfIds, sharing, type ShareChoice, type ShareMode } from "@/lib/node-share";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { CardHead } from "@/screens/overview-parts";
import { groupTone } from "@/screens/users/format";
import { groupsQuery } from "@/screens/users/rpc";

// "Who gets this node" on the node's Overview (under what waits for you, in the first column): the groups with a switch each, "Give to everyone" and "Take off everyone". A switch
// writes at once (SetNodeSharing, undone by the same switch); the two buttons for everyone ask first: the panel counts who is touched
// (dry_run, nothing written) and the card says it before the one yes. Taking the node off everyone is the one action the owner
// came here for, so it is a button, not a switch per group.

type Node = Plain<NodeMsg>;

/** What the dev kit starts the card with: the question open for one of the two buttons, and what the count of the panel is (waiting, refused, or these numbers). */
export type ShareDemo = { ask?: "all" | "none"; count?: "load" | "error" | { gain: number; lose: number } };

/** What a change touches, as the panel counted it (a dry run of SetNodeSharing). */
type Counted = { peopleGain: number; peopleLose: number };

// every list that shows who gets what: the people's subscriptions, the groups' pages, the node's own page
const everyKey = ["groups", "users", "subs", "profiles", "nodes", "node"];

/** The pill of the card: everyone is the healthy tone, some groups the same with its count, nobody grey (amber once it has lasted over a day: a hint, never an alert). */
function pillOf(mode: ShareMode, receiving: number, total: number, idle: boolean): { kind: StatusKind; key: "all" | "some" | "none" } {
  if (mode === "none") return { kind: idle ? "warn" : "off", key: "none" };
  return { kind: "ok", key: mode === "all" || (total > 0 && receiving === total) ? "all" : "some" };
}

/** The dev kit's start of the card (the page it draws has no props to carry it down): the question open, and the panel's count. */
export const shareDemo: { current?: ShareDemo } = {};

export function ShareCard({ node }: { node: Node }) {
  const demo = shareDemo.current;
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const groupsQ = useQuery(groupsQuery);
  // the owner and the helper write (canManage of the Users screen); an observer sees who gets the node
  const role = useQuery(meQuery).data?.admin?.role;
  const manage = role === Role.OWNER || role === Role.HELPER;
  const [ask, setAsk] = useState<"all" | "none" | null>(demo?.ask ?? null);

  const list = groupsQ.data ?? [];
  const ids = receivingIds(list, node.id);
  const summary = node.sharing;
  const mode: ShareMode = summary ? modeOf(summary.mode) : ids.length === 0 ? "none" : ids.length === list.length ? "all" : "listed";

  const apply = useMutation({
    mutationFn: (c: ShareChoice) => groupsApi.setNodeSharing({ nodeId: node.id, ...sharing(c), dryRun: false }),
    onSuccess: async (_r, c) => {
      await Promise.all(everyKey.map((k) => qc.invalidateQueries({ queryKey: [k] })));
      setAsk(null);
      toast(c.mode === "all" ? t("ns.done.all", { name: node.name }) : c.mode === "none" ? t("ns.done.none", { name: node.name }) : t("ns.done.listed", { name: node.name, groups: t.n("ns.groupsN", c.groupIds.length) }));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const count = useMutation({
    mutationFn: async (kind: "all" | "none"): Promise<Counted> => {
      const r = await groupsApi.setNodeSharing({ nodeId: node.id, ...sharing({ mode: kind, groupIds: [] }), dryRun: true });
      return { peopleGain: r.peopleGain, peopleLose: r.peopleLose };
    },
  });
  const counted = demo?.count !== undefined ? demoCount(demo.count) : { status: count.status, data: count.data, error: count.error };

  // while a write is on its way the switches already show the answer
  const sent = apply.isPending ? apply.variables : undefined;
  const shownIds = sent ? (sent.mode === "all" ? list.map((g) => g.id) : sent.mode === "none" ? [] : [...sent.groupIds]) : ids;
  const shownMode: ShareMode = sent ? (sent.mode === "listed" && shownIds.length === list.length ? "all" : sent.mode) : mode;
  const pill = pillOf(shownMode, shownIds.length, list.length, !!summary?.idle && !sent);

  const open = (kind: "all" | "none") => {
    setAsk(kind);
    count.mutate(kind);
  };
  const close = () => {
    setAsk(null);
    count.reset();
  };
  const toggle = (id: string, on: boolean) => {
    if (apply.isPending) return;
    apply.mutate(shareOfIds(on ? [...shownIds, id] : shownIds.filter((x) => x !== id)));
  };
  const hint = shownMode === "all" ? t("ns.hint.all") : shownMode === "listed" ? t("ns.hint.listed") : summary?.idle && !sent ? t("ns.hint.noneIdle") : t("ns.hint.none");
  const pad = "px-6 max-md:px-4";

  return (
    <section id="share" aria-label={t("ns.card.title")} className="scroll-mt-4 rounded-card border border-line bg-surface">
      <CardHead title={t("ns.card.title")}>
        <StatusPill sm kind={pill.kind} label={pill.key === "some" ? t("ns.pill.some", { n: shownIds.length, total: list.length }) : t(pill.key === "all" ? "ns.pill.all" : "ns.pill.none")} />
      </CardHead>
      <p className={cx("pb-3 text-[13px] leading-5 text-sec", pad)}>{hint}</p>

      {groupsQ.isPending ? (
        <Pending compact className={cx("border-t border-line py-3", pad)} />
      ) : groupsQ.isError && !groupsQ.data ? (
        <QueryError error={groupsQ.error} compact onRetry={() => void groupsQ.refetch()} className={cx("border-t border-line py-3", pad)} />
      ) : list.length === 0 ? (
        <p className={cx("border-t border-line py-3 text-[13px] text-muted", pad)}>{t("ns.noGroups")}</p>
      ) : (
        <ul aria-label={t("ns.pick.groups")} aria-busy={apply.isPending || undefined}>
          {list.map((g) => {
            const on = shownIds.includes(g.id);
            return (
              <li key={g.id} className={cx("grid min-h-12 grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-t border-line py-2", pad)}>
                <span className="flex min-w-0 items-center gap-2.5">
                  <i aria-hidden data-tone={groupTone(g.id, g.color)} className="size-2 flex-none rounded-full bg-(--t)" />
                  <b className={cx("truncate font-medium transition-opacity duration-200 ease-out", !on && "opacity-60")}>{g.name}</b>
                  <span className="flex-none text-xs text-muted">{t.n("users.groupPeople", g.userCount + g.extraUserCount)}</span>
                </span>
                <Switch checked={on} disabled={!manage} aria-label={t("ns.switch", { name: g.name })} onCheckedChange={(v) => toggle(g.id, v)} />
              </li>
            );
          })}
        </ul>
      )}

      {manage && !ask && (
        <div className={cx("flex flex-wrap items-center gap-2 border-t border-line py-3", pad)}>
          <Button disabled={!groupsQ.data || apply.isPending || shownMode === "all"} onClick={() => open("all")}>
            {t("ns.giveAll")}
          </Button>
          <Button variant="ghostDanger" disabled={!groupsQ.data || apply.isPending || shownMode === "none"} onClick={() => open("none")}>
            {t("ns.takeAll")}
          </Button>
          {apply.isPending && <span className="ms-auto text-xs text-muted">{t("ns.saving")}</span>}
        </div>
      )}
      {!manage && <p className={cx("border-t border-line py-3 text-xs text-muted", pad)}>{t("ns.readOnly")}</p>}

      {manage && ask && (
        <div role="group" aria-label={t(ask === "none" ? "ns.ask.none.title" : "ns.ask.all.title", { name: node.name })} className={cx("flex flex-col gap-3 rounded-b-card border-t border-line bg-[color-mix(in_srgb,var(--text)_2%,var(--surface))] py-4", pad)}>
          <b className="font-medium">{t(ask === "none" ? "ns.ask.none.title" : "ns.ask.all.title", { name: node.name })}</b>
          {counted.status === "pending" && <p className="text-[13px] text-muted">{t("ns.ask.count")}</p>}
          {counted.status === "error" && <QueryError error={counted.error} compact onRetry={() => count.mutate(ask)} />}
          {counted.status === "success" && counted.data && <p className="text-[13px] leading-5 text-sec">{askText(t, ask, counted.data, ids.length)}</p>}
          <div className="flex flex-wrap gap-2">
            <Button
              variant={ask === "none" ? "danger" : "primary"}
              disabled={counted.status !== "success" || apply.isPending}
              onClick={() => apply.mutate({ mode: ask, groupIds: [] })}
            >
              {apply.isPending ? t("ns.saving") : t(ask === "none" ? "ns.ask.none.yes" : "ns.ask.all.yes")}
            </Button>
            <Button disabled={apply.isPending} onClick={close}>
              {t("users.cancel")}
            </Button>
          </div>
        </div>
      )}
    </section>
  );
}

/** The sentence under the question: who stops (or starts) getting the node, as the panel counted. */
function askText(t: T, ask: "all" | "none", c: Counted, groups: number): string {
  if (ask === "all") return c.peopleGain > 0 ? t("ns.ask.all.body", { people: t.n("ns.peopleN", c.peopleGain) }) : t("ns.ask.all.nobody");
  return groups === 0 && c.peopleLose === 0 ? t("ns.ask.none.nobody") : t("ns.ask.none.body", { groups: t.n("ns.groupsN", groups), people: t.n("ns.peopleN", c.peopleLose) });
}

/** The dev kit's count: waiting, refused (a node that was retired meanwhile), or the numbers it states. */
function demoCount(c: NonNullable<ShareDemo["count"]>): { status: "pending" | "error" | "success"; data?: Counted; error?: unknown } {
  if (c === "load") return { status: "pending" };
  if (c === "error") return { status: "error", error: new ConnectError("node_retired", Code.FailedPrecondition) };
  return { status: "success", data: { peopleGain: c.gain, peopleLose: c.lose } };
}
