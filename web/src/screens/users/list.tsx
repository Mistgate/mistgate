import { keepPreviousData, useInfiniteQuery, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { UserFilter, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { Avatar, Card, PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Icon } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { StatusDot } from "@/components/ui/status";
import { Tabs } from "@/components/ui/tabs";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { userCountQuery } from "@/lib/queries";
import { CreateUserModal } from "./create-user";
import { avatarIndex, barTone, nowSec, seenText, shownKey, shownKind, shownStatus, shownText, termText, usage, viaText } from "./format";
import { GroupLink, GroupsTab } from "./groups";
import { LinkModal, linkTargetOf, type LinkTarget } from "./link-modal";
import { usersPageN, type UserN as User } from "./model";
import { AppLink, useGo } from "./nav";
import { useTx, type Tx } from "./t";
import { groupsQuery } from "./rpc";
import { Check, useDebounced } from "./ui";
import { Pending, QueryError } from "@/components/ui/query-error";

const PAGE = 50;
type FilterId = "all" | "online" | "expiring" | "over";
const filterValue: Record<FilterId, UserFilter> = {
  all: UserFilter.UNSPECIFIED,
  online: UserFilter.ONLINE,
  expiring: UserFilter.EXPIRING,
  over: UserFilter.OVER_QUOTA,
};

// One grid for the header and every row keeps the columns aligned: check, name, status, app, group, devices,
// traffic, term, last seen, node.
const cols = "grid-cols-[18px_minmax(120px,1.3fr)_132px_108px_minmax(70px,0.7fr)_40px_minmax(120px,1.2fr)_52px_86px_minmax(40px,0.5fr)]";

type Search = { create?: boolean; group?: string; tab?: "groups" };

/** /users: two tabs, the people and the groups they belong to. The header's main button is the one of the open tab. */
export function UsersScreen() {
  const t = useTx();
  const navigate = useNavigate();
  const { create: wanted, group = "", tab } = useSearch({ strict: false }) as Search;
  const onGroups = tab === "groups";
  const [clicked, setClicked] = useState(false);
  const creating = clicked || wanted === true;
  const setCreating = (open: boolean) => {
    setClicked(open);
    if (!open && wanted) void navigate({ to: "/users", replace: true } as never);
  };
  const [newGroup, setNewGroup] = useState(false);
  const groups = useQuery(groupsQuery);
  const counts = useQuery(userCountQuery).data?.counts;

  const go = (next: string) => void navigate({ to: "/users", search: next === "groups" ? { tab: "groups" } : {}, replace: true } as never);

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <PageTitle>{t("users.title")}</PageTitle>
          {onGroups
            ? groups.data && <span className="text-xs text-muted">{t.n("users.groupsCount", groups.data.length)}</span>
            : counts && <span className="text-xs text-muted">{t.n("users.summary", counts.all, { o: counts.online })}</span>}
        </div>
        {onGroups ? (
          <Button variant="primary" onClick={() => setNewGroup(true)}>
            <Icon name="plus" size={14} />
            {t("users.newGroup")}
          </Button>
        ) : (
          <Button variant="primary" onClick={() => setCreating(true)}>
            <Icon name="plus" size={14} />
            <span className="hidden md:inline">{t("users.create")}</span>
            <span className="md:hidden">{t("users.createShort")}</span>
          </Button>
        )}
      </div>
      <Tabs
        aria-label={t("users.tabs")}
        value={onGroups ? "groups" : "people"}
        onValueChange={go}
        items={[
          { value: "people", label: t("users.tab.people"), content: <PeopleTab groupId={onGroups ? "" : group} onCreate={() => setCreating(true)} creating={creating} setCreating={setCreating} /> },
          {
            value: "groups",
            label: t("users.tab.groups"),
            content: <GroupsTab creating={newGroup} onCreatingChange={setNewGroup} focus={onGroups ? group : undefined} onFocusDone={() => group && go("groups")} />,
          },
        ]}
      />
    </div>
  );
}

/** The people: search, filters, the table (cards on the phone), the bulk bar and the create-user dialog. */
function PeopleTab({ groupId, onCreate, creating, setCreating }: { groupId: string; onCreate: () => void; creating: boolean; setCreating: (o: boolean) => void }) {
  const t = useTx();
  const fmt = useFmt();
  const go = useGo();
  const qc = useQueryClient();
  const toast = useToast();
  const [filter, setFilter] = useState<FilterId>("all");
  const [search, setSearch] = useState("");
  const query = useDebounced(search.trim(), 250);
  // id -> user of everything ticked, across pages and filters, so a bulk action knows names and expiry dates
  const [picked, setPicked] = useState<Record<string, User>>({});
  // "/users?group=<id>" (the profile page's "Where it runs", the Groups tab) keeps the list to one group
  const groupList = useQuery(groupsQuery);
  const groupName = groupList.data?.find((g) => g.id === groupId)?.name ?? "…";
  const [link, setLink] = useState<{ target: LinkTarget; url?: string; password?: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const list = useInfiniteQuery({
    queryKey: ["users", "list", filter, query, groupId],
    queryFn: ({ pageParam, signal }) =>
      users.listUsers({ filter: filterValue[filter], query, groupId, pageSize: PAGE, pageToken: pageParam }, { signal }).then(usersPageN),
    initialPageParam: "",
    getNextPageParam: (last) => last.nextPageToken || undefined,
    refetchInterval: 10_000,
    placeholderData: keepPreviousData,
  });

  const rows = list.data?.pages.flatMap((p) => p.users) ?? [];
  const counts = list.data?.pages[0]?.counts;
  const pickedList = Object.values(picked);
  const allOnPage = rows.length > 0 && rows.every((u) => picked[u.id]);
  const now = nowSec();
  const groupOf = (u: User) => groupList.data?.find((g) => g.id === u.groupId);

  const toggle = (u: User) =>
    setPicked((p) => {
      const next = { ...p };
      if (next[u.id]) delete next[u.id];
      else next[u.id] = u;
      return next;
    });
  const toggleAll = () =>
    setPicked((p) => {
      const next = { ...p };
      for (const u of rows) {
        if (allOnPage) delete next[u.id];
        else next[u.id] = u;
      }
      return next;
    });

  async function act(run: () => Promise<void>) {
    setBusy(true);
    try {
      await run();
      setPicked({});
      await qc.invalidateQueries({ queryKey: ["users"] });
    } catch (e) {
      toast.error(errorText(e, t));
    } finally {
      setBusy(false);
    }
  }
  const names = (us: User[]) => us.map((u) => u.name).join(", ");

  const extend = () =>
    act(async () => {
      // "never expires" users have no date to move: extending would put a term on them, so they are left out
      const us = pickedList.filter((u) => u.expiresUnix > 0);
      if (us.length === 0) return void toast(t("users.extendedNone"));
      await users.extendUsers({ userIds: us.map((u) => u.id), days: 30 });
      toast(t("users.extended", { names: names(us) }));
    });
  const disable = () =>
    act(async () => {
      const us = pickedList.filter((u) => u.status !== UserStatus.DISABLED);
      const ids = us.map((u) => u.id);
      await users.setUsersEnabled({ userIds: ids, enabled: false });
      toast(t("users.disabledToast", { names: names(us) }), {
        undo: () => void users.setUsersEnabled({ userIds: ids, enabled: true }).then(() => qc.invalidateQueries({ queryKey: ["users"] })),
      });
    });

  const filterLabels: Record<FilterId, [string, number | undefined]> = {
    all: [t("users.filter.all"), counts?.all],
    online: [t("users.filter.online"), counts?.online],
    expiring: [t("users.filter.expiring"), counts?.expiring],
    over: [t("users.filter.over"), counts?.overQuota],
  };
  const hasNone = !list.isPending && counts?.all === 0 && filter === "all" && query === "" && groupId === "";

  return (
    <div className="flex flex-col gap-3.5">
      {hasNone ? (
        <Card lg className="border-dashed">
          <EmptyState
            title={t("users.none.title")}
            action={
              <Button variant="primary" size="lg" onClick={onCreate}>
                {t("users.create")}
              </Button>
            }
          >
            {t("users.none.body")}
          </EmptyState>
        </Card>
      ) : (
        <>
          <div className="flex flex-wrap items-center gap-2">
            {groupId && (
              <button
                type="button"
                aria-label={t("users.groupFilterClear")}
                onClick={() => go("/users", { replace: true })}
                className="flex h-[30px] items-center gap-1.5 rounded-[15px] border border-accent-line bg-accent-soft px-2.5 text-xs font-bold"
              >
                {t("users.groupFilter", { name: groupName })}
                <Icon name="x" size={12} />
              </button>
            )}
            <TextField
              search
              aria-label={t("users.search")}
              placeholder={t("users.search")}
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="min-w-[200px] flex-1 md:max-w-[300px] md:flex-none"
            />
            <FilterChips
              aria-label={t("users.filters")}
              value={filter}
              onValueChange={setFilter}
              options={(Object.keys(filterLabels) as FilterId[]).map((id) => ({
                value: id,
                label: (
                  <>
                    {filterLabels[id][0]}
                    {filterLabels[id][1] !== undefined && <span className="font-mono text-[11px] opacity-70">{filterLabels[id][1]}</span>}
                  </>
                ),
              }))}
            />
          </div>

          {pickedList.length > 0 && (
            <div className="screen-enter flex flex-wrap items-center gap-2 rounded-field border border-accent-line bg-accent-soft py-2 pr-2 pl-3.5">
              <b className="min-w-[120px] flex-1 text-[13px]">{t("users.selected", { n: pickedList.length })}</b>
              <Button disabled={busy} onClick={extend}>
                {t("users.extend30")}
              </Button>
              <Button variant="danger" disabled={busy} onClick={disable}>
                {t("users.disable")}
              </Button>
              <Button variant="ghost" onClick={() => setPicked({})}>
                {t("users.cancel")}
              </Button>
            </div>
          )}

          {list.isPending ? (
            <Pending />
          ) : list.isError && rows.length === 0 ? (
            <QueryError error={list.error} onRetry={() => void list.refetch()} />
          ) : (
            <>
              {list.isError && <Notice tone="danger">{errorText(list.error, t)}</Notice>}
              {rows.length === 0 ? (
                <p className="p-8 text-center text-[13px] text-muted">{t("users.empty")}</p>
              ) : (
                <>
                  <Card lg className="hidden overflow-hidden md:block">
                    <div className={cx("grid h-[38px] items-center gap-2.5 border-b border-line px-4 text-[11px] font-bold tracking-[0.06em] text-muted uppercase", cols)}>
                      <Check checked={allOnPage} onCheckedChange={toggleAll} label={t("users.selectAll")} />
                      {(["name", "status", "via", "group", "devices", "traffic", "term", "seen", "node"] as const).map((c) => (
                        <span key={c} className="truncate">
                          {t(`users.col.${c}`)}
                        </span>
                      ))}
                    </div>
                    {rows.map((u) => (
                      <Row key={u.id} u={u} group={groupOf(u)} t={t} fmt={fmt} now={now} on={!!picked[u.id]} onToggle={() => toggle(u)} onOpen={() => go("/users/$id", { params: { id: u.id } })} />
                    ))}
                  </Card>
                  <div className="flex flex-col gap-2 md:hidden">
                    {rows.map((u) => (
                      <UserCard key={u.id} u={u} group={groupOf(u)} t={t} fmt={fmt} now={now} on={!!picked[u.id]} onToggle={() => toggle(u)} onOpen={() => go("/users/$id", { params: { id: u.id } })} />
                    ))}
                  </div>
                </>
              )}
              {list.hasNextPage && (
                <div className="flex justify-center">
                  <Button variant="secondary" size="lg" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                    {t("users.loadMore")}
                  </Button>
                </div>
              )}
            </>
          )}
        </>
      )}

      <CreateUserModal
        open={creating}
        onOpenChange={setCreating}
        onCreated={(u, url, password) => {
          setCreating(false);
          const target = linkTargetOf(u, groupList.data?.find((g) => g.id === u.groupId));
          setLink({ target, url, password });
          // what the owner sends: the link, and the page password when the page asks for one; a link that gives
          // nothing yet is not copied: the window says why first
          if (!target.access.happ && !target.access.amnezia) return void toast(t("users.createdPlain", { name: u.name }));
          navigator.clipboard.writeText(password ? t("users.shareText", { url, password }) : url).then(
            () => toast(t("users.created", { name: u.name })),
            () => toast(t("users.createdPlain", { name: u.name })),
          );
        }}
      />
      <LinkModal target={link?.target ?? null} initialUrl={link?.url} initialPassword={link?.password} onClose={() => setLink(null)} />
    </div>
  );
}

type RowProps = { u: User; group?: Group; t: Tx; fmt: Fmt; now: number; on: boolean; onToggle: () => void; onOpen: () => void };

function UsageBar({ pct, tone }: { pct: number; tone: keyof typeof barTone }) {
  return (
    <div className="h-[3px] rounded-sm bg-surface-2">
      <div className={cx("h-full rounded-sm", barTone[tone])} style={{ width: `${Math.min(100, pct * 100).toFixed(1)}%` }} />
    </div>
  );
}

function StatusCell({ u, t }: { u: User; t: Tx }) {
  const s = shownStatus(u);
  return (
    <span className={cx("flex min-w-0 items-center gap-[7px] text-xs font-semibold", shownText[s])}>
      <StatusDot kind={shownKind[s]} />
      <span className="truncate">{t(shownKey[s])}</span>
    </span>
  );
}

/**
 * The app column: what the person really used lately ("Subscription link", "Both", "—"). An active person whose page would give
 * nothing gets a yellow "No access" instead, with the reason on hover (the group gives nothing, or not for their apps).
 */
function AppCell({ u, group, t }: { u: User; group?: Group; t: Tx }) {
  if (u.status === UserStatus.ACTIVE && !u.accessHapp && !u.accessAmnezia) {
    const groupGivesNothing = !group || (group.happNodes === 0 && group.amneziaNodes === 0);
    return (
      <span title={t(groupGivesNothing ? "users.noAccessGroup" : "users.noAccessApps")} className="inline-flex h-[22px] max-w-full items-center gap-1 rounded-[7px] border border-warn-line bg-warn-soft px-1.5 text-[11px] font-bold whitespace-nowrap text-warn-text">
        <span aria-hidden>!</span>
        {t("users.noAccess")}
      </span>
    );
  }
  return <span className="truncate text-xs text-muted">{viaText(u, t)}</span>;
}

const toneClass = { muted: "text-muted", fg: "text-fg", bad: "text-danger-text" } as const;
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();

function Row({ u, group, t, fmt, now, on, onToggle, onOpen }: RowProps) {
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const term = termText(u, t, now);
  const full = shownStatus(u) === "devices";
  return (
    <div
      onClick={onOpen}
      className={cx(
        "grid min-h-[52px] cursor-pointer items-center gap-2.5 border-b border-line px-4 py-1.5 text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2",
        cols,
        on && "bg-accent-soft hover:bg-accent-soft",
        u.status === UserStatus.DISABLED && "opacity-60",
      )}
    >
      <Check checked={on} onCheckedChange={onToggle} label={t("users.selectOne", { name: u.name })} />
      <div className="flex min-w-0 items-center gap-2.5">
        <Avatar name={u.name} index={avatarIndex(u.id)} />
        <AppLink to="/users/$id" params={{ id: u.id }} className="truncate font-bold" onClick={stop}>
          {u.name}
        </AppLink>
      </div>
      <StatusCell u={u} t={t} />
      <AppCell u={u} group={group} t={t} />
      <span className="min-w-0 truncate" onClick={stop}>
        <GroupLink id={u.groupId} name={u.groupName} className="font-semibold text-muted hover:text-fg" />
      </span>
      <span className={cx("font-mono text-xs", full ? "text-warn" : "text-muted")}>
        {u.devicesUsed}/{u.deviceLimit}
      </span>
      <div className="flex min-w-0 flex-col gap-1">
        <span className="font-mono text-[11px] whitespace-nowrap text-muted">{use.text}</span>
        <UsageBar pct={use.pct} tone={use.tone} />
      </div>
      <span className={cx("font-mono text-xs", toneClass[term.tone])}>{term.text}</span>
      <span className={cx("truncate text-xs", u.online ? "text-fg" : "text-muted")}>{seenText(u, t, now)}</span>
      <span className="truncate font-mono text-xs text-muted">
        {u.currentNodeId ? (
          <Link to="/nodes/$id" params={{ id: u.currentNodeId }} onClick={stop} className="hover:text-fg hover:underline">
            {u.currentNodeName}
          </Link>
        ) : (
          "—"
        )}
      </span>
    </div>
  );
}

function UserCard({ u, group, t, fmt, now, on, onToggle, onOpen }: RowProps) {
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const term = termText(u, t, now);
  const full = shownStatus(u) === "devices";
  return (
    <div
      onClick={onOpen}
      className={cx(
        "flex cursor-pointer flex-col gap-2.5 rounded-card border px-3.5 py-3",
        on ? "border-accent-line bg-accent-soft" : "border-line bg-surface",
        u.status === UserStatus.DISABLED && "opacity-60",
      )}
    >
      <div className="flex items-center gap-2.5">
        <Check large checked={on} onCheckedChange={onToggle} label={t("users.selectOne", { name: u.name })} />
        <Avatar name={u.name} index={avatarIndex(u.id)} />
        <AppLink to="/users/$id" params={{ id: u.id }} className="min-w-0 flex-1 truncate text-[15px] font-bold" onClick={stop}>
          {u.name}
        </AppLink>
        <StatusCell u={u} t={t} />
      </div>
      <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
        <AppCell u={u} group={group} t={t} />·
        <span onClick={stop}>
          <GroupLink id={u.groupId} name={u.groupName} className="font-semibold text-muted" />
        </span>
        ·
        <span className={cx("font-mono", full && "text-warn")}>
          {u.devicesUsed}/{u.deviceLimit}
        </span>
        ·<span className={cx("font-mono", toneClass[term.tone])}>{term.text}</span>
        <span className="flex-1" />
        <span className={u.online ? "text-fg" : undefined}>{seenText(u, t, now)}</span>
      </div>
      <div className="flex items-center gap-2.5">
        <div className="flex-1">
          <UsageBar pct={use.pct} tone={use.tone} />
        </div>
        <span className="font-mono text-[11px] text-muted">{use.text}</span>
      </div>
    </div>
  );
}
