import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useState, type ReactNode } from "react";
import { useListEntrance } from "@/components/fx/page";
import { PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { FilterChips } from "@/components/ui/chips";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Segmented } from "@/components/ui/segmented";
import { useToast } from "@/components/ui/toast";
import { UserFilter, UserSort } from "@/gen/mistgate/admin/v1/user_pb";
import { plainText } from "@/i18n";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { clampPage, useMove, usePaging } from "@/lib/paging";
import { userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { CreateUserModal } from "./create-user";
import { barButton, UsersBulk, UsersPick } from "./bulk";
import { maxBulkUsers } from "./bulk-actions";
import { canManage, GroupsTab } from "./groups";
import { LinkModal, linkTargetOf, type LinkTarget } from "./link-modal";
import { usersPageN, type UserN as User } from "./model";
import { useGo } from "./nav";
import { groupsQuery } from "./rpc";
import { clickSort, sortOf, type SortId, type SortSearch } from "./sort";
import { useTx, type Tx } from "./t";
import { PeopleTable } from "./users-table";
import { UsersFirst } from "./users-first";
import { useDebounced } from "./ui";
import "../nodes.css";

const PAGE = 50;
/** What the dev kit's done screen shows: the answer of a made-up create call. */
const demoCreated = { user: { id: "usr_carol", name: "carol", groupId: "grp_friends", groupName: "friends", groupColor: "sky", status: 1, apps: { happ: true, amnezia: false }, quotaBytes: 50e9, expiresUnix: Math.floor(Date.now() / 1000) + 30 * 86400, deviceLimit: 3, accessHapp: true, accessAmnezia: false } as unknown as User, url: "https://sub.example.com/s/carol-7f3k9q", password: "" };
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

type Search = { create?: boolean; group?: string; tab?: "groups" };

/** What the dev kit starts the screen with (a filter, a search, selecting, an open row, a window); the app starts with none. */
export type UsersDemo = {
  tab?: "groups";
  filter?: FilterId;
  find?: string;
  selecting?: boolean;
  ticked?: readonly string[];
  /** The bar's button that asks "Sure?" at the start. */
  ask?: "disable" | "reset";
  open?: string;
  creating?: boolean;
  /** The name typed into the new-person window, and whether it starts on the done screen. */
  name?: string;
  done?: boolean;
  newGroup?: boolean;
  groupFind?: string;
};

/** /users: two tabs, the people and the groups they belong to. The header's main button is the one of the open tab. */
export function UsersScreen({ demo }: { demo?: UsersDemo } = {}) {
  const t = useTx();
  const navigate = useNavigate();
  const { create: wanted, group = "", tab } = useSearch({ strict: false }) as Search;
  // the dev kit has no query string of its own: its scenes keep the tab here
  const [demoTab, setDemoTab] = useState(demo?.tab);
  const onGroups = demo ? demoTab === "groups" : tab === "groups";
  const [clicked, setClicked] = useState(!!demo?.creating);
  const creating = clicked || wanted === true;
  const setCreating = (open: boolean) => {
    setClicked(open);
    if (!open && wanted) void navigate({ to: "/users", replace: true } as never);
  };
  const [newGroup, setNewGroup] = useState(!!demo?.newGroup);
  const [groupFind, setGroupFind] = useState(demo?.groupFind ?? "");
  const groups = useQuery(groupsQuery);
  const counts = useQuery(userCountQuery).data?.counts;
  const manage = canManage(useQuery(meQuery).data?.admin?.role);

  const go = (next: "people" | "groups") => (demo ? setDemoTab(next === "groups" ? "groups" : undefined) : void navigate({ to: "/users", search: next === "groups" ? { tab: "groups" } : {}, replace: true } as never));
  const sub = onGroups ? (groups.data ? t.n("users.groupsCount", groups.data.length) : "") : counts && counts.all > 0 ? summary(counts, t) : counts ? t("ul.sub.none") : "";

  return (
    <div className="flex flex-col gap-4">
      <div className="mb-1 flex items-center gap-3 max-md:flex-wrap">
        <PageTitle>{t("users.title")}</PageTitle>
        <span className="me-auto inline-flex min-w-0 items-center gap-2 text-[13px] text-muted max-md:flex-none max-md:text-[0px]">
          <i aria-hidden className="fz-live-dot size-2 flex-none rounded-full bg-accent-mark" />
          {sub}
        </span>
        {manage &&
          (onGroups ? (
            <Button variant="primary" size="lg" className="max-md:flex-none" onClick={() => setNewGroup(true)}>
              <Icon name="plus" size={16} />
              {t("ul.group")}
            </Button>
          ) : (
            <Button variant="primary" size="lg" className="max-md:flex-none" onClick={() => setCreating(true)}>
              <Icon name="plus" size={16} />
              {t("ul.person")}
            </Button>
          ))}
      </div>
      {onGroups ? (
        <>
          <Bar view="groups" onView={go}>
            <Search value={groupFind} onChange={setGroupFind} placeholder={t("ul.groupSearch")} label={t("ul.groupFind")} />
          </Bar>
          <GroupsTab creating={newGroup} onCreatingChange={setNewGroup} focus={group || undefined} query={groupFind} onFocusDone={() => {}} onResetQuery={() => setGroupFind("")} />
        </>
      ) : (
        <PeopleTab groupId={group} demo={demo} onView={go} creating={creating} setCreating={setCreating} />
      )}
    </div>
  );
}

/** The line under the title: "17 people · 31 devices · 6 online". */
function summary(c: { all: number; online: number; devicesUsed: number }, t: Tx) {
  return `${t.n("users.sum.people", c.all)} · ${t.n("users.sum.devices", c.devicesUsed)} · ${t("ul.sub.online", { n: c.online })}`;
}

/** The bar over the list: the section switch first, then what the tab brings (filters, search, Select). */
function Bar({ view, onView, children }: { view: "people" | "groups"; onView: (v: "people" | "groups") => void; children?: ReactNode }) {
  const t = useTx();
  return (
    <div className="fz-bar us-bar flex flex-wrap items-center gap-3">
      <Segmented variant="flat" className="fz-seg" aria-label={t("ul.section")} value={view} onValueChange={onView} options={[{ value: "people", label: t("ul.tab.people") }, { value: "groups", label: t("ul.tab.groups") }]} />
      {children}
    </div>
  );
}

function Search({ value, onChange, placeholder, label }: { value: string; onChange: (v: string) => void; placeholder: string; label: string }) {
  return (
    <label data-has={value ? "" : undefined} className="fz-q flex h-9 items-center gap-2 rounded-field border border-line bg-surface px-3 text-muted focus-within:border-accent-line">
      <Icon name="search" size={16} />
      <input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} aria-label={label} autoComplete="off" className="min-w-0 flex-1 border-0 bg-transparent text-fg outline-none placeholder:text-muted" />
    </label>
  );
}

/** The people: the bar (section, filters, search, Select), the table, the bulk bar and the windows (create, link). */
function PeopleTab({ groupId, demo, onView, creating, setCreating }: { groupId: string; demo?: UsersDemo; onView: (v: "people" | "groups") => void; creating: boolean; setCreating: (o: boolean) => void }) {
  const t = useTx();
  const go = useGo();
  const toast = useToast();
  // the owner and the helper tick people for the bulk bar; a read-only admin sees the list alone
  const manage = canManage(useQuery(meQuery).data?.admin?.role);
  const [filter, setFilter] = useState<FilterId>(demo?.filter ?? "all");
  const [search, setSearch] = useState(demo?.find ?? "");
  const query = useDebounced(search.trim(), demo?.find ? 0 : 250);
  const [selecting, setSelecting] = useState(!!demo?.selecting);
  const [open, setOpen] = useState(demo?.open ?? "");
  // id -> user of everything ticked, across pages and filters, so a bulk action knows names and expiry dates
  const [picked, setPicked] = useState<Record<string, User>>({});
  // "/users?group=<id>" (the profile page's "Where it runs", the Groups tab) keeps the list to one group
  const groupList = useQuery(groupsQuery);
  const groupName = groupList.data?.find((g) => g.id === groupId)?.name ?? "…";
  const [link, setLink] = useState<LinkTarget | null>(null);

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

  const entrance = useListEntrance(list.data !== undefined);
  const rows = useMemo(() => list.data?.users ?? [], [list.data]);
  const counts = list.data?.counts;
  const total = counts ? { all: counts.all, online: counts.online, expiring: counts.expiring, over: counts.overQuota, devices: counts.overDeviceLimit }[filter] : 0;
  const page = clampPage(paging.page, total, paging.size);
  // rows went away (a bulk disable, an expiry) and the page is past the end now: step back to the last one
  useEffect(() => {
    if (counts && page !== paging.page) paging.setPage(page, true);
  }, [counts, page, paging]);
  const changeFilter = (next: FilterId) => {
    setFilter(next);
    setOpen("");
    paging.setPage(1, true);
  };
  const pager = { total, page, size: paging.size, sizes: pageSizes, onPage: paging.setPage, onSize: paging.setSize, busy: list.isPlaceholderData };
  const pickedList = useMemo(() => Object.values(picked), [picked]);
  const shownIds = useMemo(() => new Set(rows.map((u) => u.id)), [rows]);
  const allOnPage = rows.length > 0 && rows.every((u) => picked[u.id]);

  const toggle = useCallback(
    (u: User) =>
      setPicked((p) => {
        const next = { ...p };
        if (next[u.id]) delete next[u.id];
        else if (Object.keys(next).length < maxBulkUsers) next[u.id] = u;
        return next;
      }),
    [],
  );
  const toggleAll = () =>
    setPicked((p) => {
      const next = { ...p };
      for (const u of rows) {
        if (allOnPage) delete next[u.id];
        else if (next[u.id] || Object.keys(next).length < maxBulkUsers) next[u.id] = u;
      }
      return next;
    });
  // a refetch brings fresh rows: the ticked ones on screen take them (what a bulk call brought back is merged the same way)
  const [seenRows, setSeenRows] = useState(rows);
  if (seenRows !== rows) {
    setSeenRows(rows);
    setPicked((p) => {
      let next: Record<string, User> | null = null;
      for (const u of rows) if (p[u.id] && p[u.id] !== u) (next ??= { ...p })[u.id] = u;
      return next ?? p;
    });
  }
  // the demo ticks some of the people it shows, once they are there
  const [demoTicked, setDemoTicked] = useState(false);
  if (!demoTicked && demo?.ticked && rows.length > 0) {
    setDemoTicked(true);
    setPicked(Object.fromEntries(rows.filter((u) => demo.ticked!.includes(u.id)).map((u) => [u.id, u])));
  }
  const clear = useCallback(() => setPicked({}), []);
  const finishSelecting = () => {
    setSelecting(false);
    clear();
  };
  // what a bulk call brought back replaces the ticked person; the ones that are gone leave the selection
  const merge = useCallback((fresh: User[], gone: readonly string[] = []) => {
    setPicked((p) => {
      const next = { ...p };
      for (const u of fresh) if (next[u.id]) next[u.id] = u;
      for (const id of gone) delete next[id];
      return next;
    });
  }, []);

  const label = (text: string, n: number | undefined, tone: "" | "warn" | "crit" = "") => (
    <>
      {text}
      {n !== undefined && <span className={cx("font-mono text-[11px] font-normal", tone === "warn" ? "text-warn-text" : tone === "crit" ? "text-danger-text" : "text-muted")}>{n}</span>}
    </>
  );
  // "Expiring" and "Over quota" show while somebody is, "Over device limit" too; the one in use stays (so it can be left)
  const chips: { id: FilterId; text: string; n: number | undefined; tone: "" | "warn" | "crit" }[] = [
    { id: "all", text: t("users.filter.all"), n: counts?.all, tone: "" },
    { id: "online", text: t("users.filter.online"), n: counts?.online, tone: "" },
    { id: "expiring", text: t("users.filter.expiring"), n: counts?.expiring, tone: "warn" },
    { id: "over", text: t("users.filter.over"), n: counts?.overQuota, tone: "crit" },
    { id: "devices", text: t("users.filter.overDevices"), n: counts?.overDeviceLimit, tone: "warn" },
  ];
  const shownChips = chips.filter((c) => c.id === "all" || c.id === "online" || c.id === filter || (c.n ?? 0) > 0);
  const hasNone = !list.isPending && counts?.all === 0 && filter === "all" && query === "" && groupId === "";
  const narrowed = filter !== "all" || query !== "" || groupId !== "";
  const reset = () => {
    setFilter("all");
    setSearch("");
    setOpen("");
    if (groupId) go("/users", { replace: true });
  };
  const select = manage ? { on: selecting, picked, toggle } : undefined;

  const afterCreate = (u: User, url: string, password: string) => {
    const target = linkTargetOf(u, groupList.data?.find((g) => g.id === u.groupId));
    // what the owner sends: the link, and the page password when the page asks for one; a link that gives nothing yet is not
    // copied: the window says why first
    if (!target.access.happ && !target.access.amnezia) return void toast(t("users.createdPlain", { name: u.name }));
    navigator.clipboard.writeText(password ? plainText(t("users.shareText", { url, password })) : url).then(
      () => toast(t("users.created", { name: u.name })),
      () => toast(t("users.createdPlain", { name: u.name })),
    );
  };

  return (
    <>
      <Bar view="people" onView={onView}>
        {!hasNone && (
          <>
            <FilterChips
              className="fz-ch flex-nowrap!"
              aria-label={t("users.filters")}
              value={filter}
              onValueChange={changeFilter}
              options={shownChips.map((c) => ({ value: c.id, label: label(c.text, c.n, c.tone) }))}
            />
            {groupId && (
              <button type="button" aria-label={t("users.groupFilterClear")} onClick={() => go("/users", { replace: true })} className="flex h-8 flex-none items-center gap-1.5 rounded-2xl border border-accent-line bg-accent-soft px-3 text-[13px] font-medium">
                {t("users.groupFilter", { name: groupName })}
                <Icon name="x" size={12} />
              </button>
            )}
            <Search
              value={search}
              onChange={(v) => {
                setSearch(v);
                setOpen("");
                paging.setPage(1, true);
              }}
              placeholder={t("users.search")}
              label={t("ul.find")}
            />
            {manage && (
              <div className="fz-sbw flex flex-none items-center gap-3">
                {selecting && <UsersPick filter={filterValue[filter]} query={query} groupId={groupId} page={rows} any={pickedList.length > 0} onPick={(people) => setPicked(Object.fromEntries(people.map((u) => [u.id, u])))} />}
                <button type="button" aria-pressed={selecting} onClick={() => (selecting ? finishSelecting() : setSelecting(true))} className={barButton}>
                  {selecting ? t("ul.done") : t("ul.select")}
                </button>
              </div>
            )}
          </>
        )}
      </Bar>

      {hasNone ? (
        <UsersFirst onAdd={manage ? () => setCreating(true) : undefined} />
      ) : list.isPending ? (
        <Pending />
      ) : list.isError && rows.length === 0 ? (
        <QueryError error={list.error} onRetry={() => void list.refetch()} />
      ) : (
        <>
          {list.isError && <Notice tone="danger">{errorText(list.error, t)}</Notice>}
          <PeopleTable
            rows={rows}
            groups={groupList.data ?? []}
            order={order}
            onSort={(id) => setSort(clickSort(order, id))}
            select={select}
            open={open}
            onOpen={(id) => setOpen(open === id ? "" : id)}
            manage={manage}
            entrance={entrance}
            busy={list.isPlaceholderData}
            pager={pager}
            showPager={total > Math.min(...pageSizes)}
            emptyNote={rows.length === 0 && narrowed}
            onReset={reset}
            onAdd={manage ? () => setCreating(true) : undefined}
            onLink={(u) => setLink(linkTargetOf(u, groupList.data?.find((g) => g.id === u.groupId)))}
          />
        </>
      )}

      <CreateUserModal open={creating} onOpenChange={setCreating} onCreated={afterCreate} startName={demo?.name} startDone={demo?.done ? demoCreated : undefined} />
      <LinkModal target={link} onClose={() => setLink(null)} />
      {manage && <UsersBulk initialAsk={demo?.ask} picked={pickedList} shown={shownIds} allShown={allOnPage} total={rows.length} onAll={toggleAll} onClear={clear} onMerge={merge} />}
    </>
  );
}

