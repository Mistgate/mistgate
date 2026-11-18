import { keepPreviousData, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Avatar } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { IconButton } from "@/components/ui/icon-button";
import { Menu, MenuItem, MenuLink } from "@/components/ui/menu";
import { Pagination } from "@/components/ui/pagination";
import { Segmented } from "@/components/ui/segmented";
import { Tip } from "@/components/ui/tooltip";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { UserSort, UserStatus, type UserCounts } from "@/gen/mistgate/admin/v1/user_pb";
import { users as usersApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEffectsEnabled } from "@/lib/effects";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { clampPage, usePaging } from "@/lib/paging";
import { profileListQuery } from "./rpc";
import { avatarIndex, daysLeft, groupTone, nowSec, seenText, shownKey, shownStatus, usage } from "./format";
import { Tick, type GroupDemo } from "./group-bits";
import { addUsers, lostProfileIds, moveUsers } from "./group-move";
import { useGroupChange } from "./group-membership";
import { usersPageN, type UserN } from "./model";
import { useTx, type Tx } from "./t";
import { ConfirmModal, useDebounced } from "./ui";
import "./group.css";

// The People tab of the group page (PnGroup: ug-): four figures of the group, the panel "Who to add" (or move in), the people with their
// devices, traffic, term and last visit, and the menu of a row. The people come from ListUsers with the group's id (the ones that have it
// as the main group and the ones that have it as an extra group, the second with a pill), paged on the server.

const PAGE = 50;
const sizes = [25, PAGE, 100] as const;
/** The first people a "Who to move in" search shows: a longer list is a reason to search, not to scroll. */
const PICK_LIMIT = 100;

/** The numbers of the group: the counts ListUsers brings back for the group alone (page of one, nothing else is asked). */
export const groupCountsQuery = (groupId: string) => ({
  queryKey: ["users", "group-counts", groupId] as const,
  queryFn: ({ signal }: { signal: AbortSignal }) => usersApi.listUsers({ groupId, pageSize: 1 }, { signal }).then((r) => r.counts),
  refetchInterval: 10_000,
});

/** The key of the people list of one group (the dev kit fills it). */
export const groupPeopleKey = (groupId: string, query: string, page: number, size: number) => ["users", "group-people", groupId, query, page, size] as const;

// ---- the four figures ----

function Stats({ counts, group }: { counts: UserCounts | undefined; group: Group }) {
  const t = useTx();
  const all = counts?.all ?? group.userCount + group.extraUserCount;
  const cells = [
    { label: t("grp.st.people"), v: String(all), u: group.extraUserCount > 0 ? t("grp.countExtra", { n: group.extraUserCount }) : "", s: counts && counts.expiring > 0 ? t("grp.st.expiring", { n: counts.expiring }) : t("grp.st.noExpiring") },
    { label: t("grp.st.online"), v: String(counts?.online ?? 0), u: t("grp.st.of", { n: all }), s: t("grp.st.devOnline", { n: counts?.devicesOnline ?? 0 }) },
    { label: t("grp.st.devices"), v: String(counts?.devicesUsed ?? 0), u: "", s: counts && counts.overDeviceLimit > 0 ? t("grp.st.overDev", { n: counts.overDeviceLimit }) : t("grp.st.noOverDev") },
    { label: t("grp.st.quota"), v: String(counts?.overQuota ?? 0), u: "", s: counts && counts.overQuota > 0 ? t("grp.st.overQuota") : t("grp.st.noOverQuota") },
  ];
  return (
    <div className="grid grid-cols-4 rounded-card border border-line bg-surface max-md:grid-cols-2">
      {cells.map((c, i) => (
        <div key={c.label} className={cx("flex min-w-0 flex-col gap-0.5 px-6 py-4 max-md:px-4 max-md:py-3", i > 0 && "border-s border-line", i === 2 && "max-md:border-s-0", i >= 2 && "max-md:border-t")}>
          <span className="text-[13px] text-sec">{c.label}</span>
          <b className="flex items-baseline gap-2 text-[28px] leading-9 font-medium tracking-[-0.02em] max-md:text-2xl max-md:leading-8">
            <Scramble once value={c.v} className="font-mono" />
            {c.u && <small className="text-[13px] font-normal tracking-normal text-muted">{c.u}</small>}
          </b>
          <em className="truncate text-xs text-muted not-italic">{c.s}</em>
        </div>
      ))}
    </div>
  );
}

// ---- the box that finds a person ----

function SearchBox({ value, onChange, label, placeholder, className }: { value: string; onChange: (v: string) => void; label: string; placeholder: string; className?: string }) {
  return (
    <label className={cx("flex h-9 items-center gap-2 rounded-ctl border border-line px-2.5 transition-[border-color,box-shadow] duration-150 ease-out focus-within:border-accent-line focus-within:shadow-[0_0_0_3px_var(--focus-ring)]", className)}>
      <Icon name="search" size={16} className="block flex-none text-muted" />
      <input
        type="text"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        aria-label={label}
        autoComplete="off"
        className="min-w-0 flex-1 bg-transparent text-[13px] text-fg outline-none placeholder:text-muted"
      />
    </label>
  );
}

// ---- who to move in ----

/**
 * "Who to add to the group": everybody of the other groups, ticked one by one; the footer says what happens and does it. [Add | Move]:
 * "Add" (the one it opens with) makes the group an extra group of each, nothing is lost and their main group stays; "Move" makes it their
 * main group, and the footer says what they lose before it does it.
 */
function PickPanel({ group, groups, onClose, onMoved, demo }: { group: Group; groups: readonly Group[]; onClose: () => void; onMoved: (ids: string[]) => void; demo?: GroupDemo }) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const [mode, setMode] = useState<"add" | "move">(demo?.mode ?? "add");
  const [find, setFind] = useState("");
  const query = useDebounced(find.trim(), 250);
  const [ticked, setTicked] = useState<Record<string, UserN>>({});
  const [busy, setBusy] = useState(0);
  const [sure, setSure] = useState(false);
  const list = useQuery<ReturnType<typeof usersPageN>>({
    queryKey: ["users", "group-pick", query],
    queryFn: ({ signal }) => usersApi.listUsers({ query, pageSize: PICK_LIMIT, sort: UserSort.NAME }, { signal }).then(usersPageN),
    placeholderData: keepPreviousData,
  });
  // who can be added: nobody that is in the group already, as the main group or as an extra one; who can be moved: everybody whose main group it is not
  const candidates = (list.data?.users ?? []).filter((u) => u.groupId !== group.id && (mode === "move" || !u.extraGroupIds.includes(group.id)));
  // the demo ticks the first two
  const [seeded, setSeeded] = useState(false);
  if (demo?.tick && !seeded && candidates.length > 0) {
    setSeeded(true);
    setTicked(Object.fromEntries(candidates.slice(0, demo.tick).map((u) => [u.id, u])));
  }
  const chosen = Object.values(ticked);
  const adding = mode === "add";
  // a move costs each person what only their old main group gave; their extra groups stay
  const byId = (id: string) => groups.find((g) => g.id === id);
  const lost = adding
    ? []
    : [...new Set(chosen.flatMap((u) => lostProfileIds([byId(u.groupId)].filter((g) => g !== undefined), group, u.extraGroupIds.map(byId).filter((g) => g !== undefined))))];
  const profiles = useQuery(profileListQuery).data ?? [];
  const lostNames = lost.map((id) => profiles.find((p) => p.id === id)?.name ?? id);
  const go = async () => {
    if (chosen.length === 0 || busy > 0) return;
    if (lost.length > 0 && !sure) {
      setSure(true);
      return;
    }
    setBusy(1);
    const ids = chosen.map((u) => u.id);
    const r = await (adding ? addUsers : moveUsers)(ids, group.id, (n) => setBusy(n + 1));
    await Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    setBusy(0);
    if (r.error) toast.error(t("grp.moveFail", { done: r.done, total: chosen.length, error: errorText(r.error, t) }));
    else toast(t.n(adding ? "grp.added" : "grp.moved", r.done, { name: group.name }));
    if (r.done > 0) onMoved(ids);
    onClose();
  };
  const now = nowSec();
  return (
    <section className="animate-[mg-up_0.25s_var(--e)_both] overflow-hidden rounded-card border border-line bg-surface">
      <div className="flex items-center gap-3 px-6 pt-4 pb-3 max-md:flex-wrap max-md:px-4">
        <h3 className="me-auto text-base leading-6 font-medium max-md:order-1">{t(adding ? "grp.pick.titleAdd" : "grp.pick.title", { name: group.name })}</h3>
        {/* on a phone: the title and the close button, then [Add | Move], then the search */}
        <div className="flex-none max-md:order-3 max-md:basis-full">
          <Segmented
            aria-label={t("grp.pick.mode")}
            value={mode}
            onValueChange={(m) => {
              setMode(m);
              setTicked({});
              setSure(false);
            }}
            options={[
              { value: "add", label: t("grp.mode.add") },
              { value: "move", label: t("grp.mode.move") },
            ]}
          />
        </div>
        <SearchBox className="flex-[0_1_240px] max-md:order-4 max-md:flex-[1_1_100%]" value={find} onChange={(v) => setFind(v)} label={t("grp.find")} placeholder={t("grp.findPh")} />
        <IconButton variant="compact" aria-label={t("common.close")} onClick={onClose} className="max-md:order-2">
          <Icon name="x" size={16} />
        </IconButton>
      </div>
      {list.isPending ? (
        <Pending compact className="border-t border-line px-6 pt-4 pb-5 max-md:px-4" />
      ) : list.isError && !list.data ? (
        <QueryError error={list.error} compact onRetry={() => void list.refetch()} />
      ) : candidates.length === 0 ? (
        <p className="m-0 border-t border-line px-6 pt-4 pb-5 text-[13px] leading-[18px] text-muted max-md:px-4">{query ? t("grp.noFind") : t(adding ? "grp.pick.noneAdd" : "grp.pick.none")}</p>
      ) : (
        <div role="group" aria-label={t(adding ? "grp.pick.titleAdd" : "grp.pick.title", { name: group.name })}>
          {candidates.map((u) => {
            const on = !!ticked[u.id];
            const g = groups.find((x) => x.id === u.groupId);
            const extra = u.extraGroupIds.includes(group.id);
            return (
              <button
                key={u.id}
                type="button"
                role="checkbox"
                aria-checked={on}
                onClick={() => {
                  setSure(false);
                  setTicked((p) => {
                    const next = { ...p };
                    if (next[u.id]) delete next[u.id];
                    else next[u.id] = u;
                    return next;
                  });
                }}
                className="grid min-h-12 w-full grid-cols-[20px_28px_auto_auto_minmax(0,1fr)] items-center gap-3 border-t border-line px-6 text-start text-[13px] transition-colors duration-150 ease-(--e) hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)] max-md:grid-cols-[20px_28px_minmax(0,1fr)_auto] max-md:px-4"
              >
                <Tick on={on} />
                <Avatar name={u.name} index={avatarIndex(u.name)} size={28} />
                <b className="truncate font-mono text-sm font-medium">{u.name}</b>
                <span className="flex min-w-0 items-center gap-1.5">
                  <span data-tone={groupTone(u.groupId, g?.color ?? u.groupColor)} className="tone-chip inline-flex h-[22px] max-w-40 items-center rounded-chip px-2 text-[11px]">
                    <span className="truncate">{u.groupName}</span>
                  </span>
                  {extra && <ExtraPill t={t} />}
                </span>
                <em className="truncate text-end text-muted not-italic max-md:hidden">{seenText(u, t, now)}</em>
              </button>
            );
          })}
        </div>
      )}
      {list.data?.nextPageToken && <p className="m-0 border-t border-line px-6 py-2.5 text-xs text-muted max-md:px-4">{t("grp.pick.more", { n: fmt.num(PICK_LIMIT) })}</p>}
      <div className="flex items-center gap-2 rounded-b-card border-t border-line bg-[color-mix(in_srgb,var(--text)_2%,transparent)] px-6 py-3 max-md:flex-wrap max-md:px-4">
        <span className="me-auto text-xs leading-4 text-muted max-md:basis-full">
          {sure && lost.length > 0 ? (
            <span className="text-warn-text">
              <b className="font-medium">{t.n("users.impactGroup", chosen.length)}</b> {lostNames.map((n) => t("common.quoted", { name: n })).join(", ")}. {t("grp.pick.keys")}
            </span>
          ) : adding ? (
            t("grp.pick.noteAdd", { name: group.name })
          ) : (
            t("grp.pick.note")
          )}
        </span>
        <Button size="md" onClick={onClose}>
          {t("users.cancel")}
        </Button>
        <Button size="md" variant={sure && lost.length > 0 ? "danger" : "primary"} aria-disabled={chosen.length === 0 || undefined} disabled={busy > 0} className={cx(chosen.length === 0 && "opacity-45")} onClick={() => void go()}>
          {busy > 0 && <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" />}
          {sure && lost.length > 0 ? t("grp.pick.goAnyway", { n: chosen.length }) : chosen.length > 0 ? t(adding ? "grp.pick.goAddN" : "grp.pick.goN", { n: chosen.length }) : t(adding ? "grp.pick.goAdd" : "grp.pick.go")}
        </Button>
      </div>
    </section>
  );
}

/** "extra": the group is an extra one for this person (it only adds servers); a 20 px tag. */
function ExtraPill({ t }: { t: Tx }) {
  return (
    <Tip label={t("grp.extraPillTip")}>
      <span tabIndex={0} className="inline-flex h-5 flex-none items-center rounded-chip bg-step px-1.5 text-[11px] font-medium text-sec">
        {t("grp.extraPill")}
      </span>
    </Tip>
  );
}

// ---- the people ----

const cols =
  "grid grid-cols-[minmax(200px,1.6fr)_112px_minmax(160px,1fr)_96px_minmax(120px,0.8fr)_32px] items-center gap-4 px-6 max-[1040px]:grid-cols-[minmax(0,1.6fr)_64px_minmax(130px,1fr)_88px_32px] max-md:grid-cols-[minmax(0,1fr)_104px_32px] max-md:gap-2.5 max-md:px-4";

function Member({ u, group, groups, manage, fresh, open0, onMove, onChange, t, fmt, now }: { u: UserN; open0?: boolean; group: Group; groups: readonly Group[]; manage: boolean; fresh: boolean; onMove: (u: UserN, to: Group) => void; onChange: (u: UserN, kind: "main" | "remove") => void; t: Tx; fmt: ReturnType<typeof useFmt>; now: number }) {
  const extra = u.groupId !== group.id; // the group is an extra one for this person
  const status = shownStatus(u);
  const expired = u.status === UserStatus.EXPIRED || (u.expiresUnix > 0 && u.expiresUnix <= now);
  const left = daysLeft(u.expiresUnix, now);
  const full = u.deviceLimit > 0 && u.devicesUsed >= u.deviceLimit;
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const tone = u.quotaBytes <= 0 ? "inf" : use.tone === "over" ? "crit" : use.tone === "high" ? "warn" : "";
  const live = u.online && !expired;
  const others = groups.filter((g) => g.id !== group.id);
  return (
    <div className={cx(cols, "min-h-[60px] border-t border-line transition-colors duration-150 ease-(--e) hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]", fresh && "grp-fresh")}>
      <Link to="/users/$id" params={{ id: u.id }} className="group/n flex min-w-0 items-center gap-3 text-inherit no-underline">
        <Avatar name={u.name} index={avatarIndex(u.name)} size={32} />
        <span className="flex min-w-0 flex-col">
          <span className="flex min-w-0 items-center gap-2">
            <b className="truncate font-mono font-medium group-hover/n:underline group-hover/n:underline-offset-[3px]">{u.name}</b>
            {extra && <ExtraPill t={t} />}
          </span>
          <span className="truncate text-xs leading-4 text-muted">{t(shownKey[status])}</span>
        </span>
      </Link>
      <span className="flex items-center gap-2.5 max-md:hidden">
        <b className={cx("min-w-9 font-mono text-[13px] font-medium", full && "text-warn-text")}>{u.deviceLimit > 0 ? `${u.devicesUsed}/${u.deviceLimit}` : u.devicesUsed}</b>
        {u.deviceLimit > 0 && (
          <span aria-hidden className="flex gap-[3px] max-[1040px]:hidden">
            {Array.from({ length: Math.min(u.deviceLimit, 8) }, (_, i) => (
              <i key={i} className={cx("size-1.5 rounded-full", i < u.devicesUsed ? (full ? "bg-warn" : "bg-accent-mark") : "bg-step")} />
            ))}
          </span>
        )}
      </span>
      <span className="flex min-w-0 flex-col gap-1.5">
        <span className="flex items-baseline whitespace-nowrap">
          <b className={cx("font-mono text-[13px] font-medium", tone === "crit" && "text-danger-text")}>{use.used}</b>
          {use.of && <em className="ms-1 font-mono text-xs text-muted not-italic">/ {use.of}</em>}
        </span>
        <i
          aria-hidden
          style={{ "--w": `${Math.min(100, Math.round(use.pct * 100))}%` } as CSSProperties}
          className={cx(
            "block h-1 rounded-[2px] rtl:-scale-x-100",
            tone === "inf" && "bg-[repeating-linear-gradient(90deg,var(--step)_0_4px,transparent_4px_8px)]",
            tone === "crit" && "bg-danger",
            tone === "warn" && "bg-[linear-gradient(90deg,var(--warning)_var(--w),var(--step)_0)]",
            tone === "" && "bg-[linear-gradient(90deg,var(--accent-mark)_var(--w),var(--step)_0)]",
          )}
        />
      </span>
      <span className={cx("text-[13px] whitespace-nowrap max-md:hidden", expired ? "text-danger-text" : left !== null && left <= 7 && "text-warn-text")}>
        {left === null ? t("grp.term.none") : expired ? t("grp.term.expired") : t("users.days", { n: left })}
      </span>
      <span className={cx("flex min-w-0 items-center gap-2 text-[13px] whitespace-nowrap max-[1040px]:hidden", live ? "text-fg" : "text-muted")}>
        <i aria-hidden className={cx("size-2 flex-none rounded-full", live ? "bg-accent-mark" : "bg-[color-mix(in_srgb,var(--text)_22%,transparent)]")} />
        <span className="truncate">{live ? (u.currentNodeName ? `${t("users.now")} · ${u.currentNodeName}` : t("users.now")) : u.lastSeenUnix === 0 ? t("grp.never") : seenText(u, t, now)}</span>
      </span>
      <Menu
        side="bottom"
        align="end"
        pop
        className="w-60"
        defaultOpen={open0}
        aria-label={t("grp.actions")}
        trigger={
          <IconButton variant="compact" aria-label={`${t("grp.actions")}: ${u.name}`} className="data-popup-open:bg-step data-popup-open:text-fg">
            <Icon name="ellipsis" size={18} />
          </IconButton>
        }
      >
        <MenuLink render={<Link to="/users/$id" params={{ id: u.id }} />}>
          <Icon name="externalLink" size={16} />
          {t("grp.openPage")}
        </MenuLink>
        {manage && extra && (
          <>
            <MenuItem onClick={() => onChange(u, "main")}>
              <Icon name="check" size={16} />
              {t("grp.makeMain", { name: group.name })}
            </MenuItem>
            <MenuItem danger onClick={() => onChange(u, "remove")}>
              <Icon name="x" size={16} />
              {t("grp.leave")}
            </MenuItem>
          </>
        )}
        {manage &&
          !extra &&
          others.map((g) => (
            <MenuItem key={g.id} onClick={() => onMove(u, g)}>
              <i aria-hidden data-tone={groupTone(g.id, g.color)} className="mx-1 size-2 flex-none rounded-full bg-(--t)" />
              {t("grp.moveTo", { name: g.name })}
            </MenuItem>
          ))}
      </Menu>
    </div>
  );
}

function Empty({ group, manage, onPick }: { group: Group; manage: boolean; onPick: () => void }) {
  const t = useTx();
  return (
    <div className="flex flex-col items-center gap-1.5 border-t border-line px-6 pt-10 pb-12 text-center max-md:px-4">
      <span aria-hidden className="mb-2 grid size-12 place-items-center rounded-full bg-step text-sec">
        <Icon name="person" size={22} />
      </span>
      <b className="text-base font-medium">{t("grp.empty.title")}</b>
      <span className="max-w-[380px] text-[13px] leading-[18px] text-muted">{t("grp.empty.body")}</span>
      {manage && (
        <div className="mt-3 flex flex-wrap justify-center gap-2">
          <Link to="/users" search={{ create: true, group: group.id }} className={buttonClass("primary", "lg")}>
            {t("grp.newPerson")}
          </Link>
          <Button size="lg" onClick={onPick}>
            {t("grp.pickOpen")}
          </Button>
        </div>
      )}
    </div>
  );
}

export function PeopleTab({ group, groups, manage, pick, setPick, demo }: { group: Group; groups: readonly Group[]; manage: boolean; pick: boolean; setPick: (o: boolean) => void; demo?: GroupDemo }) {
  const t = useTx();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const fx = useEffectsEnabled();
  const [find, setFind] = useState(demo?.find ?? "");
  const query = useDebounced(find.trim(), 250);
  const paging = usePaging({ sizes, defaultSize: PAGE });
  const [fresh, setFresh] = useState<ReadonlySet<string>>(new Set());
  const [asking, setAsking] = useState<{ u: UserN; to: Group } | null>(null);
  const counts = useQuery(groupCountsQuery(group.id)).data;
  const list = useQuery<ReturnType<typeof usersPageN>>({
    queryKey: groupPeopleKey(group.id, query, paging.page, paging.size),
    queryFn: ({ signal }) => usersApi.listUsers({ groupId: group.id, query, pageSize: paging.size, offset: (paging.page - 1) * paging.size, sort: UserSort.NAME }, { signal }).then(usersPageN),
    refetchInterval: 10_000,
    placeholderData: keepPreviousData,
  });
  const rows = useMemo(() => list.data?.users ?? [], [list.data]);
  const total = list.data?.counts?.all ?? 0;
  const page = clampPage(paging.page, total, paging.size);
  useEffect(() => {
    if (list.data && page !== paging.page) paging.setPage(page, true);
  }, [list.data, page, paging]);
  // the rows that moved in wash once, then the mark goes
  useEffect(() => {
    if (fresh.size === 0) return;
    const id = setTimeout(() => setFresh(new Set()), 2600);
    return () => clearTimeout(id);
  }, [fresh]);
  const profiles = useQuery(profileListQuery).data ?? [];

  const refresh = () => Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
  const moveOne = async (u: UserN, to: Group) => {
    const r = await moveUsers([u.id], to.id);
    await refresh();
    if (r.error) {
      toast.error(errorText(r.error, t));
      throw r.error;
    }
    toast(t.n("grp.moved", 1, { name: to.name }));
  };
  // the person's extra groups stay when the main one changes, so what they give is not lost
  const lostOf = (u: UserN, to: Group) =>
    lostProfileIds([group], to, u.extraGroupIds.map((id) => groups.find((g) => g.id === id)).filter((g) => g !== undefined));
  const askMove = (u: UserN, to: Group) => {
    if (lostOf(u, to).length > 0) setAsking({ u, to });
    else void moveOne(u, to).catch(() => {});
  };
  // an extra member: make the group their main one, or take them out of it (the server says what that costs them)
  const { ask: askChange, show: showChange, modal: changeModal } = useGroupChange(() => undefined);
  const demoWho = rows.find((u) => u.groupId !== group.id);
  const demoChange = demo?.change;
  useEffect(() => {
    if (demoChange && demoWho) showChange({ who: demoWho, ...demoChange });
    // the scene opens its window once the people are there
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [demoChange, demoWho?.id]);
  const lostNames = asking ? lostOf(asking.u, asking.to).map((id) => profiles.find((p) => p.id === id)?.name ?? id) : [];
  const now = nowSec();
  const head = cols + " min-h-9 text-xs text-muted max-md:hidden";

  let body: ReactNode;
  if (list.isPending) body = <Pending compact className="border-t border-line px-6 pt-4 pb-5 max-md:px-4" />;
  else if (list.isError && !list.data) body = <QueryError error={list.error} onRetry={() => void list.refetch()} />;
  else if (rows.length === 0 && query === "" && total === 0) body = <Empty group={group} manage={manage} onPick={() => setPick(true)} />;
  else if (rows.length === 0) body = <p className="m-0 border-t border-line px-6 pt-4 pb-5 text-[13px] leading-[18px] text-muted max-md:px-4">{t("grp.noFind")}</p>;
  else
    body = (
      <>
        <div aria-hidden className={cx(head, "border-t border-line")}>
          <span>{t("grp.col.person")}</span>
          <span>{t("grp.col.devices")}</span>
          <span>{t("grp.col.traffic")}</span>
          <span>{t("grp.col.term")}</span>
          <span className="max-[1040px]:hidden">{t("grp.col.seen")}</span>
          <span />
        </div>
        {rows.map((u, i) => (
          <Member key={u.id} u={u} group={group} groups={groups} manage={manage} fresh={fx && fresh.has(u.id)} open0={(demo?.menu === true && i === 1) || (demo?.menu === "extra" && u.groupId !== group.id)} onMove={askMove} onChange={(p, kind) => void askChange(p, kind, group)} t={t} fmt={fmt} now={now} />
        ))}
      </>
    );

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <Stats counts={counts} group={group} />
      {manage && pick && (
        <PickPanel
          group={group}
          groups={groups}
          demo={demo}
          onClose={() => setPick(false)}
          onMoved={(ids) => {
            setFresh(new Set(ids));
            setFind("");
          }}
        />
      )}
      <section className="relative overflow-hidden rounded-card border border-line bg-surface">
        <div className="flex items-center gap-4 px-6 pt-4 pb-3 max-md:flex-wrap max-md:px-4">
          <h3 className="me-auto text-base leading-6 font-medium">{t("grp.members")}</h3>
          {(total > 0 || query !== "") && <SearchBox className="ms-auto flex-[0_1_280px] max-md:flex-[1_1_100%]" value={find} onChange={setFind} label={t("grp.find")} placeholder={t("grp.findPh")} />}
        </div>
        {body}
        <ConfirmModal
          open={asking !== null}
          onOpenChange={(o) => !o && setAsking(null)}
          title={asking ? t("grp.moveTo", { name: asking.to.name }) : ""}
          confirmLabel={t("grp.pick.go")}
          danger
          onConfirm={() => (asking ? moveOne(asking.u, asking.to) : Promise.resolve())}
        >
          <Notice tone="warn" className="items-start">
            <span>
              <b className="font-medium">{t("users.impactUser", { name: asking?.u.name ?? "" })}</b> {lostNames.map((n) => t("common.quoted", { name: n })).join(", ")}. {t("grp.pick.keys")}
            </span>
          </Notice>
        </ConfirmModal>
        {changeModal}
      </section>
      <Pagination total={total} page={page} size={paging.size} sizes={sizes} onPage={paging.setPage} onSize={paging.setSize} busy={list.isPlaceholderData} />
    </div>
  );
}

