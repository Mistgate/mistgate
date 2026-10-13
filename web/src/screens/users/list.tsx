import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useEffect, useState } from "react";
import { UserFilter, UserSort, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { Avatar, Card, PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, type IconName, type Tone } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pagination } from "@/components/ui/pagination";
import { Select } from "@/components/ui/select";
import { StatusDot } from "@/components/ui/status";
import { Tabs } from "@/components/ui/tabs";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { clampPage, useMove, usePaging } from "@/lib/paging";
import { userCountQuery } from "@/lib/queries";
import { CreateUserModal } from "./create-user";
import { avatarIndex, barTone, nowSec, seenText, shownKey, shownKind, shownStatus, shownText, termText, usage, viaOf } from "./format";
import { GroupChip, GroupsTab } from "./groups";
import { LinkModal, linkTargetOf, type LinkTarget } from "./link-modal";
import { usersPageN, type UserN as User } from "./model";
import { AppLink, useGo } from "./nav";
import { clickSort, firstDesc, sortIds, sortLabel, sortOf, type SortId, type SortSearch, type SortState } from "./sort";
import { useTx, type Tx } from "./t";
import { groupsQuery } from "./rpc";
import { Check, useDebounced } from "./ui";
import { Pending, QueryError } from "@/components/ui/query-error";

const PAGE = 50;
const pageSizes = [25, PAGE, 100] as const;
type FilterId = "all" | "online" | "expiring" | "over";
const filterValue: Record<FilterId, UserFilter> = {
  all: UserFilter.UNSPECIFIED,
  online: UserFilter.ONLINE,
  expiring: UserFilter.EXPIRING,
  over: UserFilter.OVER_QUOTA,
};

const sortValue: Record<SortId, UserSort> = {
  name: UserSort.NAME,
  status: UserSort.STATUS,
  group: UserSort.GROUP,
  devices: UserSort.DEVICES,
  traffic: UserSort.TRAFFIC,
  expiry: UserSort.EXPIRY,
  seen: UserSort.LAST_SEEN,
};

// One grid for the header and every row keeps the columns aligned: check, name, status, app, group, devices,
// traffic, term, last seen, node.
const cols = "grid-cols-[18px_minmax(96px,1.3fr)_124px_128px_minmax(64px,0.7fr)_58px_minmax(104px,1.2fr)_56px_106px_minmax(40px,0.5fr)]";

// the header's cells: the sortable ones name the order they set (the term column sorts by the expiry time)
const columns: readonly { id: "name" | "status" | "via" | "group" | "devices" | "traffic" | "term" | "seen" | "node"; sort?: SortId }[] = [
  { id: "name", sort: "name" },
  { id: "status", sort: "status" },
  { id: "via" },
  { id: "group", sort: "group" },
  { id: "devices", sort: "devices" },
  { id: "traffic", sort: "traffic" },
  { id: "term", sort: "expiry" },
  { id: "seen", sort: "seen" },
  { id: "node" },
];

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
            : counts && <Summary counts={counts} t={t} />}
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

  // numbered pages: the server skips `offset` rows; the size of the filter in use is among the counts of the answer
  const paging = usePaging({ sizes: pageSizes, defaultSize: PAGE });
  // the order is in the URL too (?sort=traffic&desc=1); another order starts from page 1, the filter, search and group stay
  const order = sortOf(useSearch({ strict: false }) as Record<string, unknown>);
  const move = useMove();
  const setSort = (next: SortSearch) => move({ sort: undefined, desc: undefined, ...next, page: undefined, before: undefined });
  const list = useQuery<ReturnType<typeof usersPageN>>({
    queryKey: ["users", "list", filter, query, groupId, paging.page, paging.size, order.sort, order.desc],
    // every order pages by offset (never a page_token: the server takes it for name order only, and the numbered pager does not need it)
    queryFn: ({ signal }) =>
      users
        .listUsers({ filter: filterValue[filter], query, groupId, pageSize: paging.size, offset: (paging.page - 1) * paging.size, sort: sortValue[order.sort], descending: order.desc }, { signal })
        .then(usersPageN),
    refetchInterval: 10_000,
    // the old rows stay while the next page loads: the card keeps its height, nothing jumps
    placeholderData: keepPreviousData,
  });

  const rows = list.data?.users ?? [];
  const counts = list.data?.counts;
  const total = counts ? { all: counts.all, online: counts.online, expiring: counts.expiring, over: counts.overQuota }[filter] : 0;
  const page = clampPage(paging.page, total, paging.size);
  // rows went away (a bulk disable, an expiry) and the page is past the end now: step back to the last one
  useEffect(() => {
    if (counts && page !== paging.page) paging.setPage(page, true);
  }, [counts, page, paging]);
  const changeFilter = (next: FilterId) => {
    setFilter(next);
    paging.setPage(1, true);
  };
  const pager = { total, page, size: paging.size, sizes: pageSizes, onPage: paging.setPage, onSize: paging.setSize, busy: list.isPlaceholderData };
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
              onChange={(e) => {
                setSearch(e.target.value);
                paging.setPage(1, true);
              }}
              className="min-w-[200px] flex-1 md:max-w-[300px] md:flex-none"
            />
            <FilterChips
              aria-label={t("users.filters")}
              value={filter}
              onValueChange={changeFilter}
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
            <SortPick order={order} onSort={setSort} t={t} />
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
                    <table className="block w-full text-left">
                      <thead className="block">
                        <tr className={cx("grid h-[38px] items-center gap-2.5 border-b border-line px-4 text-[11px] font-bold tracking-[0.06em] text-muted uppercase", cols)}>
                          <th scope="col" className="p-0">
                            <Check checked={allOnPage} onCheckedChange={toggleAll} label={t("users.selectAll")} />
                          </th>
                          {columns.map((c) =>
                            c.sort ? (
                              <SortTh key={c.id} id={c.sort} order={order} onSort={(id) => setSort(clickSort(order, id))} label={t(`users.col.${c.id}`)} />
                            ) : (
                              <th key={c.id} scope="col" className="min-w-0 truncate p-0">
                                {t(`users.col.${c.id}`)}
                              </th>
                            ),
                          )}
                        </tr>
                      </thead>
                      <tbody className={cx("block transition-opacity duration-200", list.isPlaceholderData && "opacity-60")} aria-busy={list.isPlaceholderData || undefined}>
                        {rows.map((u) => (
                          <Row key={u.id} u={u} group={groupOf(u)} t={t} fmt={fmt} now={now} on={!!picked[u.id]} onToggle={() => toggle(u)} onOpen={() => go("/users/$id", { params: { id: u.id } })} />
                        ))}
                      </tbody>
                    </table>
                    <Pagination className="px-4" {...pager} />
                  </Card>
                  <div className="flex flex-col gap-2 md:hidden">
                    <div className={cx("flex flex-col gap-2 transition-opacity duration-200", list.isPlaceholderData && "opacity-60")} aria-busy={list.isPlaceholderData || undefined}>
                      {rows.map((u) => (
                        <UserCard key={u.id} u={u} group={groupOf(u)} t={t} fmt={fmt} now={now} on={!!picked[u.id]} onToggle={() => toggle(u)} onOpen={() => go("/users/$id", { params: { id: u.id } })} />
                      ))}
                    </div>
                    {total > Math.min(...pageSizes) && (
                      <Card lg className="px-4">
                        <Pagination flush {...pager} />
                      </Card>
                    )}
                  </div>
                </>
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

/** The digits of a counted message in the text colour, the words around them muted (the parent sets `b`). */
function Num({ text }: { text: string }) {
  return text.split(/(\d+)/).map((part, i) => (i % 2 ? <b key={i}>{part}</b> : part));
}

/**
 * Under the title: "17 people · 31 devices · 6 online now (8 devices)". Numbers carry the weight, the online part is
 * green (like every online state), and each part stays whole when the line wraps on a phone.
 */
function Summary({ counts, t }: { counts: { all: number; online: number; devicesUsed: number; devicesOnline: number }; t: Tx }) {
  const live = counts.online > 0;
  return (
    <p className="flex flex-wrap items-center gap-x-2 gap-y-0.5 text-xs text-muted max-sm:gap-x-3 [&_b]:font-bold [&_b]:text-fg">
      <span className="whitespace-nowrap">
        <Num text={t.n("users.sum.people", counts.all)} />
      </span>
      <span aria-hidden className="text-faint max-sm:hidden">
        ·
      </span>
      <span className="whitespace-nowrap">
        <Num text={t.n("users.sum.devices", counts.devicesUsed)} />
      </span>
      <span aria-hidden className="text-faint max-sm:hidden">
        ·
      </span>
      <span className={cx("flex items-center gap-1.5 whitespace-nowrap", live && "tone-ok")}>
        {live && <StatusDot kind="ok" />}
        <span className={cx(live && "tone-text font-semibold [&_b]:text-[inherit]")}>
          <Num text={t.n("users.sum.online", counts.online)} />
        </span>
        {live && counts.devicesOnline > 0 && (
          <span>
            <Num text={t.n("users.sum.onDevices", counts.devicesOnline)} />
          </span>
        )}
      </span>
    </p>
  );
}

/**
 * A column header that sorts: a real button in the <th>, the order on the <th> for a screen reader. The arrow is the
 * current direction; on the others it is a faint hint of the first one, shown on hover and focus only (its room stays).
 */
function SortTh({ id, order, onSort, label }: { id: SortId; order: SortState; onSort: (id: SortId) => void; label: string }) {
  const active = order.sort === id;
  const desc = active ? order.desc : firstDesc[id];
  return (
    <th scope="col" aria-sort={active ? (order.desc ? "descending" : "ascending") : undefined} className="min-w-0 p-0">
      <button
        type="button"
        onClick={() => onSort(id)}
        className={cx(
          "group/sort flex max-w-full cursor-pointer items-center gap-1 rounded-sm text-[length:inherit] font-[inherit] tracking-[inherit] uppercase transition-colors duration-200 hover:text-fg",
          active && "text-fg",
        )}
      >
        <span className="truncate">{label}</span>
        <Icon
          name={desc ? "arrowDown" : "arrowUp"}
          size={11}
          className={cx("transition-opacity duration-200", active ? "opacity-100" : "opacity-0 group-hover/sort:opacity-40 group-focus-visible/sort:opacity-40")}
        />
      </button>
    </th>
  );
}

/** On the phone the list is cards without headers: the same choice as a small select and a direction button. */
function SortPick({ order, onSort, t }: { order: SortState; onSort: (next: SortSearch) => void; t: Tx }) {
  return (
    <div className="flex items-center gap-1.5 md:hidden">
      <span aria-hidden className="text-xs font-semibold text-muted">
        {t("users.sort")}
      </span>
      <div className="w-[132px]">
        <Select
          compact
          aria-label={t("users.sort")}
          value={order.sort}
          onValueChange={(id) => onSort(clickSort(order, id as SortId))}
          options={sortIds.map((id) => ({ value: id, label: id === "devices" ? t("users.sortBy.devices") : t(sortLabel[id]) }))}
        />
      </div>
      <IconButton variant="field" aria-label={t(order.desc ? "users.sort.toAsc" : "users.sort.toDesc")} onClick={() => onSort({ ...(order.sort !== "name" ? { sort: order.sort } : {}), ...(order.desc ? {} : { desc: 1 as const }) })}>
        <Icon name={order.desc ? "arrowDown" : "arrowUp"} size={14} />
      </IconButton>
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
 * The app column: what the person really used lately, as a "Link" chip, a "Keys" chip, both, or "—". An active person whose page would give
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
  const { link, keys } = viaOf(u);
  if (!link && !keys) return <span className="text-xs text-muted">—</span>;
  // the chips say it short; the whole wording is what a screen reader and the hover get
  const full = t(link && keys ? "users.appsBoth" : link ? "users.appsHapp" : "users.appsAwg");
  return (
    <span role="img" aria-label={full} title={full} className="flex flex-none items-center gap-1">
      {link && <ViaChip tone="sky" icon="link" label={t("users.via.link")} />}
      {keys && <ViaChip tone="mint" icon="key" label={t("users.via.keys")} />}
    </span>
  );
}

/** "Link" (sky) and "Keys" (mint): the two ways, tinted like the subscription page's chips. */
function ViaChip({ tone, icon, label }: { tone: Tone; icon: IconName; label: string }) {
  return (
    <span data-tone={tone} className="tone-chip inline-flex h-[22px] items-center gap-1 px-1.5 text-[11px] font-bold whitespace-nowrap">
      <Icon name={icon} size={12} />
      {label}
    </span>
  );
}

/** Last seen: "now" is green (the dot and the word, like every online state); anything else is how long ago, quiet grey. */
function SeenCell({ u, t, now }: { u: User; t: Tx; now: number }) {
  if (!u.online) return <span className="truncate text-xs text-muted">{seenText(u, t, now)}</span>;
  return (
    <span className="tone-ok tone-text flex min-w-0 items-center gap-1.5 text-xs font-semibold">
      <StatusDot kind="ok" />
      <span className="truncate">{t("users.now")}</span>
    </span>
  );
}

const toneClass = { muted: "text-muted", fg: "text-fg", bad: "text-danger-text" } as const;
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();

function Row({ u, group, t, fmt, now, on, onToggle, onOpen }: RowProps) {
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const term = termText(u, t, now);
  const full = shownStatus(u) === "devices";
  return (
    <tr
      onClick={onOpen}
      className={cx(
        "grid min-h-[52px] cursor-pointer items-center gap-2.5 border-b border-line px-4 py-1.5 text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2",
        cols,
        on && "bg-accent-soft hover:bg-accent-soft",
        u.status === UserStatus.DISABLED && "opacity-60",
      )}
    >
      <td>
        <Check checked={on} onCheckedChange={onToggle} label={t("users.selectOne", { name: u.name })} />
      </td>
      <td className="flex min-w-0 items-center gap-2.5">
        <Avatar name={u.name} index={avatarIndex(u.id)} />
        <AppLink to="/users/$id" params={{ id: u.id }} className="truncate font-bold" onClick={stop}>
          {u.name}
        </AppLink>
      </td>
      <td className="flex min-w-0">
        <StatusCell u={u} t={t} />
      </td>
      <td className="flex min-w-0">
        <AppCell u={u} group={group} t={t} />
      </td>
      <td className="flex min-w-0" onClick={stop}>
        <GroupChip id={u.groupId} name={u.groupName} color={u.groupColor} />
      </td>
      <td className={cx("font-mono text-xs", full ? "text-warn" : "text-muted")}>
        {u.devicesUsed}/{u.deviceLimit}
      </td>
      <td className="flex min-w-0 flex-col gap-1">
        <span className="font-mono text-[11px] whitespace-nowrap text-muted">{use.text}</span>
        <UsageBar pct={use.pct} tone={use.tone} />
      </td>
      <td className={cx("font-mono text-xs", toneClass[term.tone])}>{term.text}</td>
      <td className="flex min-w-0">
        <SeenCell u={u} t={t} now={now} />
      </td>
      <td className="truncate font-mono text-xs text-muted">
        {u.currentNodeId ? (
          <Link to="/nodes/$id" params={{ id: u.currentNodeId }} onClick={stop} className="hover:text-fg hover:underline">
            {u.currentNodeName}
          </Link>
        ) : (
          "—"
        )}
      </td>
    </tr>
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
          <GroupChip id={u.groupId} name={u.groupName} color={u.groupColor} />
        </span>
        ·
        <span className={cx("font-mono", full && "text-warn")}>
          {u.devicesUsed}/{u.deviceLimit}
        </span>
        ·<span className={cx("font-mono", toneClass[term.tone])}>{term.text}</span>
        <span className="flex-1" />
        <SeenCell u={u} t={t} now={now} />
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
