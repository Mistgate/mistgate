import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { Fresh } from "@/components/fx/fresh";
import { markFresh } from "@/components/fx/registry";
import { Avatar } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { UserSort } from "@/gen/mistgate/admin/v1/user_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { groups as groupsApi, users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { useToast } from "@/components/ui/toast";
import { presetName } from "@/screens/subscriptions/model";
import { dnsPresetsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import { avatarIndex, groupTone } from "./format";
import { AppLink } from "./nav";
import { groupsQuery, profileListQuery } from "./rpc";
import { useTx, type Tx } from "./t";
import { NewGroupModal, useWayOf } from "./users-group-new";

// The Groups tab as the redesign board draws it (us-gr): a card per group, what it gives (the subscription, the keys, the Telegram
// proxy, TrustTunnel, each with its protocols), four facts, "Open" (to the group's page) and "Delete" that asks once, and a dashed
// tile for a new group. A search by name filters the cards; one that finds none says so and offers to make it.

/** Who may change people and groups: the owner and the helper (a read-only admin sees the lists). */
export const canManage = (role: Role | undefined) => role === Role.OWNER || role === Role.HELPER;

/**
 * /users?tab=groups. `focus` is an old link (?group=<id>, from before the group had a page): it goes to the page of that group.
 * `query` is the bar's search (by name).
 */
export function GroupsTab({
  creating,
  onCreatingChange,
  focus,
  onFocusDone,
  query = "",
  onResetQuery,
}: {
  creating: boolean;
  onCreatingChange: (o: boolean) => void;
  focus?: string;
  onFocusDone?: () => void;
  query?: string;
  onResetQuery?: () => void;
}) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const list = useQuery(groupsQuery);
  const profiles = useQuery(profileListQuery);
  const nodes = useQuery(nodesQuery);
  const presets = useQuery(dnsPresetsQuery);
  const wayOf = useWayOf();
  const linkApps = useLinkAppNames();
  const manage = canManage(useQuery(meQuery).data?.admin?.role);
  const [ask, setAsk] = useState("");
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  // an old link to a group opens its page, once the list says there is such a group
  const target = focus && list.data?.some((g) => g.id === focus) ? focus : "";
  const done = useRef(onFocusDone);
  useEffect(() => {
    done.current = onFocusDone;
  });
  useEffect(() => {
    if (!target) return;
    void navigate({ to: "/users/groups/$id", params: { id: target }, replace: true } as never);
    done.current?.();
  }, [target, navigate]);
  const everyone = useMutation({
    mutationFn: () => groupsApi.createGroup({ name: t("users.everyone"), profileIds: (profiles.data ?? []).map((p) => p.id) }),
    onSuccess: async (r) => {
      if (r.group) markFresh("group", r.group.id);
      await Promise.all(["groups", "users", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("users.everyoneCreated", { name: r.group?.name ?? t("users.everyone") }));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  const remove = useMutation({
    mutationFn: (g: Group) => groupsApi.deleteGroup({ groupId: g.id }),
    onSuccess: async (_, g) => {
      await Promise.all(["groups", "users", "subs", "profiles", "nodes"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("users.groupDeleted", { name: g.name }));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  if (list.isPending) return <Pending />;
  if (list.isError) return <QueryError error={list.error} onRetry={() => void list.refetch()} />;
  const q = query.trim().toLowerCase();
  const shown = list.data.filter((g) => !q || g.name.toLowerCase().includes(q));
  const dnsName = (id: string) => {
    const p = presets.data?.presets.find((x) => x.id === id);
    return p ? presetName(p, t.lang) : "";
  };
  const liveNodes = (nodes.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED);
  const ask1 = (g: Group) => {
    // once: "Sure?", or, for a group with people, who must be moved first; a second press deletes an empty one
    if (ask === g.id && g.userCount === 0) {
      setAsk("");
      remove.mutate(g);
      return;
    }
    setAsk(g.id);
    clearTimeout(timer.current);
    timer.current = setTimeout(() => setAsk(""), 3000);
  };

  return (
    <>
      {list.data.length === 0 && !q ? (
        manage && (
          <section className="flex flex-col items-center gap-1 rounded-card border border-dashed border-line px-6 py-10 text-center">
            <b className="font-medium">{t("users.groupsNone")}</b>
            <span className="max-w-[420px] text-sm text-sec">{t("users.groupsNoneBody")}</span>
            <div className="mt-3 flex flex-wrap justify-center gap-2">
              <Button variant="primary" size="lg" disabled={everyone.isPending} onClick={() => everyone.mutate()}>
                {t("users.everyoneCreate")}
              </Button>
              <Button variant="secondary" size="lg" onClick={() => onCreatingChange(true)}>
                {t("users.newGroup")}
              </Button>
            </div>
          </section>
        )
      ) : (
        shown.length === 0 && (
          <section className="flex flex-col items-center gap-1 rounded-card border border-line bg-surface px-6 py-10 text-center">
            <b className="font-medium">{t("ul.g.none")}</b>
            <span className="text-sm text-sec">{t("ul.g.noneHint")}</span>
            {manage && (
              <Button size="lg" className="mt-3" onClick={() => onCreatingChange(true)}>
                {t("users.groupCreate")}
              </Button>
            )}
          </section>
        )
      )}
      <div className="grid grid-cols-[repeat(auto-fill,minmax(380px,1fr))] gap-4 max-md:grid-cols-1">
        {shown.map((g) => {
          const ps = g.profileIds.map((id) => profiles.data?.find((p) => p.id === id)).filter((p) => p !== undefined);
          const waysOf = (w: "sub" | "awg" | "tg" | "tt") => ps.filter((p) => wayOf(p) === w);
          const gives = [
            { id: "sub", title: t("ul.get.sub"), sub: g.happNodes > 0 ? `${linkApps} · ${t.n("users.reachNodes", g.happNodes)}` : `${linkApps} · ${t("users.reachNoSub")}`, list: waysOf("sub"), warn: g.happNodes === 0 },
            { id: "awg", title: t("ul.get.awg"), sub: g.amneziaNodes > 0 ? `${t("ul.get.awgHint")} · ${t.n("users.reachNodes", g.amneziaNodes)}` : t("users.reachNoAwg"), list: waysOf("awg"), warn: g.amneziaNodes === 0 },
            { id: "tg", title: t("tgproxy.toggle"), sub: t("ul.get.tg"), list: waysOf("tg"), warn: false },
            { id: "tt", title: t("tt.toggle"), sub: t("ul.get.ttHint"), list: waysOf("tt"), warn: false },
          ].filter((x) => x.list.length > 0);
          const reach = liveNodes.length - liveNodes.filter((n) => g.disabledNodeIds.includes(n.id)).length;
          const facts = [
            [t("ul.fact.people"), String(g.userCount)],
            [t("ul.fact.profiles"), String(g.profileIds.length)],
            [t("ul.fact.nodes"), nodes.data ? `${reach}/${liveNodes.length}` : "—"],
            ["DNS", (g.dnsPresetId && dnsName(g.dnsPresetId)) || t("ul.fact.dnsDefault")],
          ];
          const asking = ask === g.id;
          return (
            <Fresh key={g.id} kind="group" id={g.id} className="relative flex flex-col overflow-hidden rounded-card border border-line bg-surface">
              <AppLink to="/users/groups/$id" params={{ id: g.id }} className="flex items-center justify-between gap-3 rounded-t-card px-6 pt-5 pb-3 transition-colors hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]">
                <span className="flex min-w-0 flex-col">
                  <h3 className="flex min-w-0 items-center text-lg font-medium">
                    <i aria-hidden data-tone={groupTone(g.id, g.color)} className="me-2 size-2 flex-none rounded-full bg-(--t)" />
                    <span className="truncate">{g.name}</span>
                  </h3>
                  <span className="text-[13px] text-muted">{t.n("users.groupPeople", g.userCount)}</span>
                </span>
                <Stack groupId={g.id} />
              </AppLink>
              <div className="flex flex-1 flex-col">
                {gives.length === 0 ? (
                  <p className="border-t border-line px-6 py-3 text-[13px] text-warn-text">{t("users.groupNoProfilesRow")}</p>
                ) : (
                  gives.map((x) => (
                    <div key={x.id} className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-0.5 border-t border-line px-6 py-3 max-md:grid-cols-1">
                      <b className="font-medium">{x.title}</b>
                      <span className="flex flex-wrap justify-end gap-1 max-md:row-auto max-md:mt-1.5 max-md:justify-start md:col-start-2 md:row-span-2 md:row-start-1">
                        {[...new Set(x.list.map((p) => p.protocol))].map((p) => (
                          <ProtocolTag key={p} protocol={p} className="font-medium" />
                        ))}
                      </span>
                      <span className={cx("text-[13px]", x.warn ? "text-warn-text" : "text-muted")}>{x.sub}</span>
                    </div>
                  ))
                )}
              </div>
              <dl className="m-0 grid grid-cols-[repeat(4,auto)] justify-between gap-x-6 gap-y-3 border-t border-line px-6 py-4 max-md:grid-cols-[repeat(2,minmax(0,1fr))] max-md:justify-stretch">
                {facts.map(([l, v]) => (
                  <div key={l} className="min-w-0">
                    <dt className="text-xs text-muted">{l}</dt>
                    <dd className="m-0 mt-0.5 truncate text-sm">{v}</dd>
                  </div>
                ))}
              </dl>
              <div className="flex items-center gap-4 border-t border-line px-6 py-3 text-[13px] [&>a]:text-sec">
                <AppLink to="/users/groups/$id" params={{ id: g.id }} className={buttonClass("secondary", "md")}>
                  {t("ul.g.open")}
                </AppLink>
                {manage && (
                  <button type="button" onClick={() => ask1(g)} className={cx("transition-colors hover:text-danger-text", asking ? "text-danger-text" : "text-muted")}>
                    {asking ? (g.userCount > 0 ? t("ul.g.movePeople") : t("ul.g.sure")) : t("users.delete")}
                  </button>
                )}
              </div>
            </Fresh>
          );
        })}
        {manage && !q && (
          <button
            type="button"
            onClick={() => onCreatingChange(true)}
            className="flex min-h-[200px] flex-col items-start gap-1 rounded-card border border-dashed border-[color-mix(in_srgb,var(--text)_18%,transparent)] p-6 text-start transition-[border-color,background-color] duration-150 hover:border-accent-mark hover:bg-[color-mix(in_srgb,var(--accent-mark)_4%,transparent)]"
          >
            <span aria-hidden className="mb-2 grid size-9 place-items-center rounded-field bg-tone-mint-bg text-accent-text">
              <Icon name="plus" size={16} />
            </span>
            <b className="font-medium">{t("users.newGroup")}</b>
            <span className="max-w-[280px] text-[13px] leading-[18px] text-muted">{t("ul.g.tile")}</span>
          </button>
        )}
      </div>
      <NewGroupModal open={creating} onOpenChange={onCreatingChange} initialName={shown.length === 0 ? query.trim() : undefined} onCreated={() => onResetQuery?.()} />
    </>
  );
}

/** The first five people of a group, as overlapping avatars (their own small query: the groups' list has no names). */
function Stack({ groupId }: { groupId: string }) {
  const q = useQuery({
    queryKey: ["users", "stack", groupId],
    queryFn: ({ signal }) => users.listUsers({ groupId, pageSize: 5, sort: UserSort.NAME }, { signal }).then((r) => r.users.map((u) => ({ id: u.id, name: u.name }))),
    staleTime: 30_000,
  });
  if (!q.data?.length) return null;
  return (
    <span aria-hidden className="flex flex-none ps-2">
      {q.data.map((u) => (
        <span key={u.id} className="-ms-2 flex animate-[fz-pop_0.3s_var(--ease-spring)_both] rounded-full shadow-[0_0_0_2px_var(--surface)]">
          <Avatar name={u.name} index={avatarIndex(u.name)} size={28} fontSize={12} />
        </span>
      ))}
    </span>
  );
}

export type { Tx };
