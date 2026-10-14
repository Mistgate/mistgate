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
import { kindTextClass, StatusDot, StatusGlyph, type StatusKind } from "@/components/ui/status";
import { Tip } from "@/components/ui/tooltip";
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
import { avatarIndex, barTone, nowSec, seenText, shownKey, shownKind, shownStatus, termText, usage, viaOf } from "./format";
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
type FilterId = "all" | "online" | "expiring" | "over" | "devices";
const filterValue: Record<FilterId, UserFilter> = {
  all: UserFilter.UNSPECIFIED,
  online: UserFilter.ONLINE,
  expiring: UserFilter.EXPIRING,
  over: UserFilter.OVER_QUOTA,
  devices: UserFilter.OVER_DEVICE_LIMIT,
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

// The table's columns. A real table with fixed layout: each header cell sets its width (padding included), the name
// takes the rest, so nothing overlaps. The status is a mark before the name (its header an icon), like the nodes list's
// dot. As the list narrows the traffic column slims down and the columns go in this order: App (under 900px), Devices
// (under 840), Group (under 700), Term (under 640); under 600 the list is cards. The sortable ones name the order they
// set (the term column sorts by the expiry time). The classes are whole strings, for Tailwind to find.
type ColId = "name" | "status" | "via" | "group" | "devices" | "traffic" | "term" | "seen";
const columns: readonly { id: ColId; sort?: SortId; cls?: string; end?: boolean; icon?: IconName }[] = [
  { id: "status", sort: "status", cls: "w-11 text-center", icon: "pulse" },
  { id: "name", sort: "name" },
  { id: "via", cls: "hidden w-16 @min-[900px]:table-cell" },
  { id: "group", sort: "group", cls: "hidden w-28 @min-[700px]:table-cell" },
  { id: "devices", sort: "devices", cls: "hidden w-[72px] @min-[840px]:table-cell", end: true },
  { id: "traffic", sort: "traffic", cls: "w-[172px] pl-6 @min-[900px]:w-[196px]", end: true },
  { id: "term", sort: "expiry", cls: "hidden w-[68px] @min-[640px]:table-cell", end: true },
  { id: "seen", sort: "seen", cls: "w-32 pr-4", end: true },
];
const col = Object.fromEntries(columns.map((c) => [c.id, cx("px-2", c.cls, c.end && "text-right")])) as Record<ColId, string>;

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
  const total = counts ? { all: counts.all, online: counts.online, expiring: counts.expiring, over: counts.overQuota, devices: counts.overDeviceLimit }[filter] : 0;
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
    devices: [t("users.filter.overDevices"), counts?.overDeviceLimit],
  };
  // "Over device limit" shows only while somebody is (or while it is the filter in use, so it can be left)
  const filterIds = (Object.keys(filterLabels) as FilterId[]).filter((id) => id !== "devices" || filter === "devices" || (counts?.overDeviceLimit ?? 0) > 0);
  const hasNone = !list.isPending && counts?.all === 0 && filter === "all" && query === "" && groupId === "";

  // a container: the table's columns, and the switch to cards, follow the width the list really has (not the window's)
  return (
    <div className="@container flex flex-col gap-3.5">
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
              options={filterIds.map((id) => ({
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
                  <Card lg className="hidden overflow-hidden @min-[600px]:block">
                    <table className="w-full table-fixed text-left">
                      <thead>
                        <tr className="h-10 border-b border-line text-[11px] font-bold tracking-[0.06em] text-muted uppercase">
                          <th scope="col" className="w-[42px] pr-2 pl-4">
                            <Check checked={allOnPage} onCheckedChange={toggleAll} label={t("users.selectAll")} />
                          </th>
                          {columns.map((c) =>
                            c.sort ? (
                              <SortTh key={c.id} id={c.sort} order={order} onSort={(id) => setSort(clickSort(order, id))} label={t(`users.col.${c.id}`)} end={c.end} icon={c.icon} className={col[c.id]} />
                            ) : (
                              <th key={c.id} scope="col" className={cx("truncate font-bold", col[c.id])}>
                                {t(`users.col.${c.id}`)}
                              </th>
                            ),
                          )}
                        </tr>
                      </thead>
                      <tbody className={cx("transition-opacity duration-200", list.isPlaceholderData && "opacity-60")} aria-busy={list.isPlaceholderData || undefined}>
                        {rows.map((u) => (
                          <Row key={u.id} u={u} group={groupOf(u)} t={t} fmt={fmt} now={now} on={!!picked[u.id]} onToggle={() => toggle(u)} onOpen={() => go("/users/$id", { params: { id: u.id } })} />
                        ))}
                      </tbody>
                    </table>
                    <Pagination className="px-4" {...pager} />
                  </Card>
                  <div className="flex flex-col gap-2 @min-[600px]:hidden">
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
 * A right-aligned column (`end`) keeps its label flush right and puts the arrow before it. A narrow one shows an `icon`
 * instead of its label: the label is then its accessible name and its tooltip.
 */
function SortTh({ id, order, onSort, label, end, icon, className }: { id: SortId; order: SortState; onSort: (id: SortId) => void; label: string; end?: boolean; icon?: IconName; className?: string }) {
  const active = order.sort === id;
  const desc = active ? order.desc : firstDesc[id];
  const button = (
    <button
      type="button"
      onClick={() => onSort(id)}
      className={cx(
        "group/sort inline-flex max-w-full cursor-pointer items-center gap-1 rounded-sm align-middle text-[length:inherit] font-[inherit] tracking-[inherit] uppercase transition-colors duration-200 hover:text-fg",
        end && "flex-row-reverse",
        icon && "relative",
        active && "text-fg",
      )}
    >
      {icon ? (
        <>
          <Icon name={icon} size={13} />
          <span className="sr-only">{label}</span>
        </>
      ) : (
        <span className="truncate">{label}</span>
      )}
      <Icon
        name={desc ? "arrowDown" : "arrowUp"}
        size={11}
        className={cx(
          "transition-opacity duration-200",
          // beside an icon the arrow hangs off to the right, so the icon stays on the axis of the marks under it
          icon && "absolute top-1/2 left-full ml-0.5 -translate-y-1/2",
          active ? "opacity-100" : "opacity-0 group-hover/sort:opacity-40 group-focus-visible/sort:opacity-40",
        )}
      />
    </button>
  );
  return (
    <th scope="col" aria-sort={active ? (order.desc ? "descending" : "ascending") : undefined} className={cx("font-bold", className)}>
      {icon ? <Tip label={label}>{button}</Tip> : button}
    </th>
  );
}

/** Where the list is cards (a phone, a narrow window) there are no headers: the same choice as a small select and a direction button. */
function SortPick({ order, onSort, t }: { order: SortState; onSort: (next: SortSearch) => void; t: Tx }) {
  return (
    <div className="flex items-center gap-1.5 @min-[600px]:hidden">
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

/** A slim usage bar: quiet while there is room, amber near the quota, red once it is used up. */
function UsageBar({ pct, tone, className }: { pct: number; tone: keyof typeof barTone; className?: string }) {
  return (
    <span aria-hidden className={cx("block h-1 overflow-hidden rounded-full bg-surface-2", className)}>
      <span className={cx("block h-full rounded-full", barTone[tone])} style={{ width: `${Math.min(100, pct * 100).toFixed(1)}%` }} />
    </span>
  );
}

/** "182 / 500 GB": what was used in the text colour (red once the quota is used up), the quota muted; "330 GB / ∞" without one. */
function UsageText({ use, t }: { use: ReturnType<typeof usage>; t: Tx }) {
  return (
    <span className="font-mono text-xs whitespace-nowrap">
      <span className={use.tone === "over" ? "text-danger-text" : "text-fg"}>{use.used}</span>
      <span className="text-muted">
        {" / "}
        {use.of || <Inf title={t("users.unlimited")} />}
      </span>
    </span>
  );
}

/**
 * The person's state in the list: a status of the API, "devices full", or "no access" (active, but their page would give
 * nothing; `why` says why: the group gives nothing, or nothing to the person's apps). `word` is the state's name.
 */
function stateOf(u: User, group: Group | undefined, t: Tx): { active: boolean; kind: StatusKind; word: string; why?: string } {
  const s = shownStatus(u);
  if (s === "active" && !u.accessHapp && !u.accessAmnezia) {
    const groupGivesNothing = !group || (group.happNodes === 0 && group.amneziaNodes === 0);
    return { active: false, kind: "warn", word: t("users.noAccess"), why: t(groupGivesNothing ? "users.noAccessGroup" : "users.noAccessApps") };
  }
  return { active: s === "active", kind: shownKind[s], word: t(shownKey[s]) };
}

/**
 * The table's status: a mark without words, its name in a tooltip (hover and keyboard focus) and in its aria-label. A
 * healthy person is a small quiet dot; a problem is the round glyph on its colour, "!" amber, "×" red, "–" grey, so a
 * state reads by its shape too, not by the hue alone.
 */
function StatusCell({ u, group, t }: { u: User; group?: Group; t: Tx }) {
  const s = stateOf(u, group, t);
  return (
    <Tip label={s.why ? <><b className={kindTextClass[s.kind]}>{s.word}</b>{` · ${s.why}`}</> : s.word}>
      <span role="img" tabIndex={0} aria-label={s.why ? `${s.word}. ${s.why}` : s.word} onClick={stop} className="inline-grid size-3.5 cursor-default place-items-center rounded-full align-middle">
        {s.active ? <StatusDot kind="ok" /> : <StatusGlyph kind={s.kind} />}
      </span>
    </Tip>
  );
}

/**
 * The card's status, in words: nothing for a healthy person, a problem as the dot and its word in its colour. "No
 * access" says why on hover.
 */
function StatusMark({ u, group, t }: { u: User; group?: Group; t: Tx }) {
  const s = stateOf(u, group, t);
  if (s.active) return null;
  return (
    <span title={s.why ?? s.word} className={cx("flex min-w-0 items-center gap-1.5 text-xs font-semibold", kindTextClass[s.kind])}>
      <StatusDot kind={s.kind} />
      <span className="truncate">{s.word}</span>
    </span>
  );
}

/**
 * The app column: what the person really used lately, as small icon chips, the link (sky) and the keys (mint), tinted
 * like the subscription page's. Each chip names itself on hover; a screen reader gets the whole wording once.
 */
function AppIcons({ u, t }: { u: User; t: Tx }) {
  const { link, keys } = viaOf(u);
  if (!link && !keys) return null;
  const full = t(link && keys ? "users.appsBoth" : link ? "users.appsHapp" : "users.appsAwg");
  return (
    <span role="img" aria-label={full} className="flex flex-none items-center gap-1">
      {link && <ViaChip tone="sky" icon="link" label={t("users.appsHapp")} />}
      {keys && <ViaChip tone="mint" icon="key" label={t("users.appsAwg")} />}
    </span>
  );
}

function ViaChip({ tone, icon, label }: { tone: Tone; icon: IconName; label: string }) {
  return (
    <span data-tone={tone} title={label} className="tone-chip grid size-[22px] place-items-center">
      <Icon name={icon} size={13} />
    </span>
  );
}

/** Last seen: "now" in green with a dot, and the node they are on (not on the narrow cards); anything else is how long ago, quiet grey. */
function SeenCell({ u, t, now, node = true }: { u: User; t: Tx; now: number; node?: boolean }) {
  if (!u.online) return <span className={cx("block truncate text-xs", u.lastSeenUnix ? "text-muted" : "text-faint")}>{seenText(u, t, now)}</span>;
  return (
    <span className="flex min-w-0 items-center justify-end gap-1.5 text-xs">
      <span className="tone-ok tone-text flex flex-none items-center gap-1.5 font-semibold">
        <StatusDot kind="ok" />
        <span>{t("users.now")}</span>
      </span>
      {node && u.currentNodeId && (
        <>
          <span aria-hidden className="text-faint">
            ·
          </span>
          <Link to="/nodes/$id" params={{ id: u.currentNodeId }} title={t("users.col.node")} onClick={stop} className="truncate font-mono text-muted hover:text-fg hover:underline">
            {u.currentNodeName}
          </Link>
        </>
      )}
    </span>
  );
}

const toneClass = { muted: "text-muted", fg: "font-semibold text-fg", bad: "font-semibold text-danger-text" } as const;
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();
/** "2/5"; no limit is "2/∞". */
const devicesOf = (u: User) => `${u.devicesUsed}/${u.deviceLimit > 0 ? u.deviceLimit : "∞"}`;
/** The devices count like the traffic bar: quiet with room, amber when the limit is full, red when the person is over it. */
const devicesTone = (u: User) => (u.deviceLimit <= 0 ? "" : u.devicesUsed > u.deviceLimit ? "font-semibold text-danger-text" : u.devicesUsed === u.deviceLimit ? "font-semibold text-warn-text" : "");
const devicesTitle = (u: User, t: Tx) => (u.deviceLimit > 0 && u.devicesUsed > u.deviceLimit ? t("users.filter.overDevices") : undefined);
/** ∞ a size up: at 12px the glyph is a speck next to the digits. */
const Inf = ({ title }: { title: string }) => (
  <span title={title} className="text-[15px] leading-none">
    ∞
  </span>
);
/** The term: days left, or ∞ for no expiry. */
const TermText = ({ u, text, t }: { u: User; text: string; t: Tx }) => (u.expiresUnix === 0 ? <Inf title={t("users.never")} /> : text);

function Row({ u, group, t, fmt, now, on, onToggle, onOpen }: RowProps) {
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const term = termText(u, t, now);
  return (
    <tr
      onClick={onOpen}
      className={cx(
        "h-14 cursor-pointer border-b border-line text-[13px] transition-colors duration-200 last:border-b-0 hover:bg-surface-2",
        on && "bg-accent-soft hover:bg-accent-soft",
        u.status === UserStatus.DISABLED && "opacity-60",
      )}
    >
      <td className="pr-2 pl-4">
        <Check checked={on} onCheckedChange={onToggle} label={t("users.selectOne", { name: u.name })} />
      </td>
      <td className={col.status}>
        <StatusCell u={u} group={group} t={t} />
      </td>
      <td className={col.name}>
        <span className="flex min-w-0 items-center gap-2.5">
          <Avatar name={u.name} index={avatarIndex(u.id)} />
          <AppLink to="/users/$id" params={{ id: u.id }} title={u.name} className="truncate font-bold" onClick={stop}>
            {u.name}
          </AppLink>
        </span>
      </td>
      <td className={col.via}>
        <AppIcons u={u} t={t} />
      </td>
      <td className={col.group} onClick={stop}>
        <span className="flex min-w-0">
          <GroupChip id={u.groupId} name={u.groupName} color={u.groupColor} />
        </span>
      </td>
      <td title={devicesTitle(u, t)} className={cx(col.devices, "font-mono text-xs", devicesTone(u) || "text-muted")}>
        {devicesOf(u)}
      </td>
      <td className={col.traffic}>
        <span className="flex items-center gap-3">
          <UsageBar pct={use.pct} tone={use.tone} className={cx("w-10 min-w-4 @min-[900px]:w-16", u.quotaBytes <= 0 && "invisible")} />
          <span className="ml-auto flex-none">
            <UsageText use={use} t={t} />
          </span>
        </span>
      </td>
      <td className={cx(col.term, "text-xs whitespace-nowrap", toneClass[term.tone])}>
        <TermText u={u} text={term.text} t={t} />
      </td>
      <td className={col.seen}>
        <SeenCell u={u} t={t} now={now} />
      </td>
    </tr>
  );
}

/**
 * The card of the narrow list: the person on the first line (a problem right after the name, when they were last seen on
 * the right), the group, the apps, the devices and the term under it, the traffic and its bar at the bottom.
 */
function UserCard({ u, group, t, fmt, now, on, onToggle, onOpen }: RowProps) {
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const term = termText(u, t, now);
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
        <span className="flex min-w-0 flex-1 items-center gap-2">
          <AppLink to="/users/$id" params={{ id: u.id }} className="min-w-0 truncate text-[15px] font-bold" onClick={stop}>
            {u.name}
          </AppLink>
          <span className="flex flex-none">
            <StatusMark u={u} group={group} t={t} />
          </span>
        </span>
        <span className="flex flex-none justify-end">
          <SeenCell u={u} t={t} now={now} node={false} />
        </span>
      </div>
      <div className="flex items-center gap-2 text-xs text-muted">
        <span onClick={stop} className="flex min-w-0">
          <GroupChip id={u.groupId} name={u.groupName} color={u.groupColor} />
        </span>
        <AppIcons u={u} t={t} />
        <span className="flex-1" />
        <span title={devicesTitle(u, t) ?? t("users.sortBy.devices")} className={cx("flex items-center gap-1 font-mono whitespace-nowrap", devicesTone(u))}>
          <Icon name="phone" size={12} />
          <span className="sr-only">{t("users.sortBy.devices")}</span>
          {devicesOf(u)}
        </span>
        <span title={t("users.col.term")} className={cx("flex items-center gap-1 whitespace-nowrap", toneClass[term.tone])}>
          <Icon name="calendar" size={12} className="text-muted" />
          <span className="sr-only">{t("users.col.term")}</span>
          <TermText u={u} text={term.text} t={t} />
        </span>
      </div>
      <div className="flex items-center gap-3">
        <UsageBar pct={use.pct} tone={use.tone} className={cx("flex-1", u.quotaBytes <= 0 && "invisible")} />
        <UsageText use={use} t={t} />
      </div>
    </div>
  );
}
