import { Link } from "@tanstack/react-router";
import { type CSSProperties } from "react";
import { Fresh } from "@/components/fx/fresh";
import { Scramble } from "@/components/fx/scramble-react";
import { Avatar, Check } from "@/components/ui/bits";
import { Icon, type IconName, type Tone } from "@/components/ui/icons";
import { Pagination } from "@/components/ui/pagination";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { cx } from "@/lib/cx";
import { useFmt, type Fmt } from "@/lib/format";
import type { useListEntrance } from "@/components/fx/page";
import { avatarIndex, devicesView, groupTone, noAccess, nowSec, seenText, shownStatus, termView, trafficTone, usage, viaOf, type CellTone } from "./format";
import type { UserN as User } from "./model";
import { AppLink } from "./nav";
import { UserExpand } from "./users-expand";
import { firstDesc, type SortId, type SortState } from "./sort";
import { useTx, type Tx } from "./t";

type Entrance = ReturnType<typeof useListEntrance>;

// The people table as the redesign board draws it (PnUsers, the us-r rows): one card, a head of sortable columns and a row per
// person; a click opens the row (the link, the page password, the devices, the quick actions) and, while "Select" is on, ticks
// it instead. The columns follow the card's own width: under 1040 the last-online column and the dots go, under 760 the group
// and the devices, under 520 the term and the head, and a line of sort chips takes the head's place.

// The columns: the check (0 or 20px, --cb), the person, the group, the devices, the traffic, the term, last online, the arrow.
const grid =
  "grid-cols-[var(--cb)_minmax(200px,1.5fr)_110px_112px_minmax(170px,1fr)_104px_minmax(130px,.8fr)_16px] " +
  "@max-[1040px]/ust:grid-cols-[var(--cb)_minmax(170px,1.5fr)_96px_64px_minmax(140px,1fr)_96px_16px] " +
  "@max-[760px]/ust:grid-cols-[var(--cb)_minmax(0,1.5fr)_minmax(110px,1fr)_84px_16px] " +
  "@max-[520px]/ust:grid-cols-[var(--cb)_minmax(0,1fr)_108px_16px]";
const rowBase =
  "grid items-center gap-4 px-6 [transition:background-color_.15s_var(--e),grid-template-columns_.25s_var(--e)] @max-[760px]/ust:gap-3 @max-[520px]/ust:gap-2.5 @max-[520px]/ust:px-4 " + grid;
/** The columns that give way as the card narrows (the row's cells and the head's, by the same cut). */
const lastCol = "@max-[1040px]/ust:hidden";
const groupCol = "@max-[760px]/ust:hidden";
const devCol = "@max-[760px]/ust:hidden";
const termCol = "@max-[520px]/ust:hidden";

const toneText: Record<CellTone, string> = { "": "", warn: "text-warn-text", crit: "text-danger-text", off: "text-muted" };
const stop = (e: { stopPropagation: () => void }) => e.stopPropagation();

type ColumnDef = { id: string; label: Parameters<Tx>[0]; sort?: SortId; cls?: string };
const columns: readonly ColumnDef[] = [
  { id: "name", label: "ul.col.person", sort: "name" },
  { id: "group", label: "users.col.group", sort: "group", cls: groupCol },
  { id: "devices", label: "ul.col.devices", sort: "devices", cls: devCol },
  { id: "traffic", label: "ul.col.traffic", sort: "traffic" },
  { id: "term", label: "users.col.term", sort: "expiry", cls: termCol },
  { id: "seen", label: "ul.col.seen", sort: "seen", cls: lastCol },
];

export type PeopleSelect = { on: boolean; picked: Readonly<Record<string, User>>; toggle: (u: User) => void };

export function PeopleTable({
  rows,
  groups,
  order,
  onSort,
  select,
  open,
  onOpen,
  manage,
  entrance,
  busy,
  pager,
  showPager,
  emptyNote,
  onReset,
  onAdd,
  onLink,
}: {
  rows: readonly User[];
  groups: readonly Group[];
  order: SortState;
  onSort: (id: SortId) => void;
  select: PeopleSelect | undefined;
  open: string;
  onOpen: (id: string) => void;
  manage: boolean;
  entrance: Entrance;
  /** The next page is on its way: the rows stay, a little paler. */
  busy: boolean;
  pager: Parameters<typeof Pagination>[0];
  showPager: boolean;
  /** Nobody to show although people exist (a filter or a search hides them all). */
  emptyNote: boolean;
  onReset: () => void;
  onAdd: () => void;
  onLink: (u: User) => void;
}) {
  const t = useTx();
  const fmt = useFmt();
  const selecting = !!select?.on;
  const now = nowSec();
  return (
    <section data-select={selecting} className="@container/ust relative rounded-card border border-line bg-surface [--cb:0px] data-[select=true]:[--cb:20px]">
      {rows.length > 0 && (
        <div role="row" className={cx("min-h-10 text-[13px] text-muted @max-[520px]/ust:hidden", rowBase)}>
          <span aria-hidden />
          {columns.map((c) => (
            <SortHead key={c.id} def={c} order={order} onSort={onSort} t={t} />
          ))}
          <span aria-hidden />
        </div>
      )}
      <SortChips order={order} onSort={onSort} t={t} />
      {emptyNote && (
        <div className="flex flex-col items-center gap-1 border-t border-line px-6 py-10 text-center text-sec">
          <b className="font-medium text-fg">{t("ul.none.title")}</b>
          <span className="text-sm">{t("ul.none.hint")}</span>
          <button type="button" onClick={onReset} className="mt-3 inline-flex h-8 items-center rounded-ctl border border-line bg-step px-3 text-[13px] font-medium text-fg transition-colors hover:bg-(--step-hover)">
            {t("ul.none.reset")}
          </button>
        </div>
      )}
      <div className={cx("transition-opacity duration-200", busy && "opacity-60")} aria-busy={busy || undefined}>
        {rows.map((u, i) => (
          <PersonRow
            key={u.id}
            u={u}
            group={groups.find((g) => g.id === u.groupId)}
            t={t}
            fmt={fmt}
            now={now}
            style={entrance(i)}
            select={select}
            on={!!select?.picked[u.id]}
            expanded={!selecting && open === u.id}
            onOpen={() => onOpen(u.id)}
            manage={manage}
            onLink={() => onLink(u)}
          />
        ))}
      </div>
      {showPager && <Pagination className="border-t border-line px-4" {...pager} />}
      <button
        type="button"
        onClick={onAdd}
        className={cx(
          "flex h-12 w-full items-center justify-center gap-2 rounded-b-card border-t border-dashed border-line text-sm font-medium text-sec transition-colors duration-150 ease-out hover:text-fg",
          rows.length === 0 && !emptyNote && "border-t-0",
        )}
      >
        <Icon name="plus" size={16} />
        {t("ul.add")}
      </button>
    </section>
  );
}

function SortHead({ def, order, onSort, t }: { def: ColumnDef; order: SortState; onSort: (id: SortId) => void; t: Tx }) {
  const sort = def.sort;
  const on = sort !== undefined && order.sort === sort;
  return (
    <div role="columnheader" aria-sort={on ? (order.desc ? "descending" : "ascending") : sort ? "none" : undefined} className={cx("flex min-w-0 text-[13px]", def.cls)}>
      {sort ? (
        <button
          type="button"
          onClick={() => onSort(sort)}
          className={cx("group/sort inline-flex h-7 min-w-0 items-center gap-1 text-start whitespace-nowrap transition-colors duration-150 ease-out hover:text-fg", on ? "text-fg" : "text-muted")}
        >
          <span className="truncate">{t(def.label)}</span>
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className={cx("flex-none transition-opacity duration-150", on ? "opacity-100" : "opacity-0")}>
            <path d={(on ? order.desc : firstDesc[sort]) ? "M7 10l5 5 5-5" : "M7 14l5-5 5 5"} />
          </svg>
        </button>
      ) : (
        <span className="inline-flex h-7 items-center whitespace-nowrap">{t(def.label)}</span>
      )}
    </div>
  );
}

/** Where the head is gone (a card under 520 wide) the same choice is a line of chips under it. */
const chipSorts: readonly SortId[] = ["name", "traffic", "devices", "expiry", "group", "seen", "status"];
const chipLabel: Record<SortId, Parameters<Tx>[0]> = {
  name: "ul.sort.name",
  traffic: "ul.col.traffic",
  devices: "ul.sort.devices",
  expiry: "users.col.term",
  group: "users.col.group",
  seen: "ul.col.seen",
  status: "users.col.status",
};
function SortChips({ order, onSort, t }: { order: SortState; onSort: (id: SortId) => void; t: Tx }) {
  return (
    <div role="group" aria-label={t("users.sort")} className="hidden items-center gap-1.5 overflow-x-auto px-4 py-2.5 text-xs text-muted [scrollbar-width:none] @max-[520px]/ust:flex">
      <span className="flex-none">{t("users.sort")}</span>
      {chipSorts.map((id) => {
        const on = order.sort === id;
        return (
          <button
            key={id}
            type="button"
            aria-pressed={on}
            onClick={() => onSort(id)}
            className={cx("inline-flex h-7 flex-none items-center gap-0.5 rounded-2xl border px-2.5 text-xs transition-colors", on ? "border-accent-line pe-1.5 text-fg" : "border-line text-sec")}
          >
            {t(chipLabel[id])}
            {on && (
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
                <path d={order.desc ? "M7 10l5 5 5-5" : "M7 14l5-5 5 5"} />
              </svg>
            )}
          </button>
        );
      })}
    </div>
  );
}

const viaChips: { key: "link" | "keys" | "trustTunnel"; tone: Tone; icon: IconName; label: Parameters<Tx>[0] | string }[] = [
  { key: "link", tone: "sky", icon: "link", label: "users.appsHapp" },
  { key: "keys", tone: "mint", icon: "key", label: "users.appsAwg" },
  { key: "trustTunnel", tone: "sage", icon: "tunnel", label: "TrustTunnel" },
];

/** What the person really connected with lately, as small chips (the link in sky, the keys in mint, TrustTunnel in sage). */
function AppIcons({ u, t }: { u: User; t: Tx }) {
  const via = viaOf(u);
  const on = viaChips.filter((c) => via[c.key]);
  if (on.length === 0) return null;
  const names = on.map((c) => (c.label === "TrustTunnel" ? c.label : t(c.label as Parameters<Tx>[0])));
  const base = via.link || via.keys ? t(via.link && via.keys ? "users.appsBoth" : via.link ? "users.appsHapp" : "users.appsAwg") : "";
  const full = [base, via.trustTunnel && "TrustTunnel"].filter(Boolean).join(" + ");
  return (
    <span role="img" aria-label={full} className="flex flex-none items-center gap-1">
      {on.map((c, i) => (
        <span key={c.key} data-tone={c.tone} title={names[i]} className="tone-chip grid size-[22px] place-items-center">
          <Icon name={c.icon} size={13} />
        </span>
      ))}
    </span>
  );
}

function PersonRow({
  u,
  group,
  t,
  fmt,
  now,
  style,
  select,
  on,
  expanded,
  onOpen,
  manage,
  onLink,
}: {
  u: User;
  group: Group | undefined;
  t: Tx;
  fmt: Fmt;
  now: number;
  style?: CSSProperties;
  select: PeopleSelect | undefined;
  on: boolean;
  expanded: boolean;
  onOpen: () => void;
  manage: boolean;
  onLink: () => void;
}) {
  const selecting = !!select?.on;
  const use = usage(u.usedBytes, u.quotaBytes, fmt);
  const dev = devicesView(u);
  const term = termView(u, t, now);
  const tone = trafficTone(u);
  const off = u.status === UserStatus.DISABLED;
  // "no access": active, but the person's page would give nothing (said in the line under the name, with its reason on hover)
  const noAcc = shownStatus(u) === "active" && noAccess(u);
  const accessWhy = noAcc ? t(!group || (group.happNodes === 0 && group.amneziaNodes === 0) ? "users.noAccessGroup" : "users.noAccessApps") : undefined;
  const never = u.lastSeenUnix === 0 && !u.online;
  return (
    <Fresh
      kind="user"
      id={u.id}
      style={style}
      className={cx(
        "us-e group/e border-t border-line transition-colors first:border-t-0 [[role=row]+&]:border-t-0",
        on ? "bg-[color-mix(in_srgb,var(--accent-mark)_6%,transparent)]" : "hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]",
        expanded && "bg-[color-mix(in_srgb,var(--text)_3%,transparent)]",
      )}
    >
      <div className={cx("relative min-h-16", rowBase, expanded && "bg-transparent")}>
        <span aria-hidden={!selecting} inert={!selecting} className="relative z-10 grid place-items-center overflow-hidden">
          {select && <Check aria-label={t("users.selectOne", { name: u.name })} checked={on} onCheckedChange={() => select.toggle(u)} />}
        </span>
        <span className={cx("flex min-w-0 items-center gap-3", off && "opacity-50")}>
          <Avatar name={u.name} index={avatarIndex(u.id)} size={32} />
          <span className="flex min-w-0 flex-col">
            <span className="flex min-w-0 items-center gap-2">
              <b className="min-w-0 truncate font-medium">{u.name}</b>
              <span className="relative z-10 @max-[760px]/ust:hidden">
                <AppIcons u={u} t={t} />
              </span>
            </span>
            <span title={accessWhy} className={cx("truncate text-xs leading-4", noAcc ? "text-warn-text" : "text-muted")}>
              {noAcc ? `${t("users.noAccess")} · ${accessWhy}` : never ? t("ul.neverSeen") : u.devicesUsed > 0 ? t.n("ul.devicesN", u.devicesUsed) : t("ul.noDevices")}
            </span>
          </span>
        </span>
        <span className={cx("relative z-10 flex min-w-0", groupCol, off && "opacity-50")}>
          <AppLink to="/users/groups/$id" params={{ id: u.groupId }} aria-label={t("users.groupOpen", { name: u.groupName })} className="tone-chip inline-flex h-6 max-w-full min-w-0 items-center px-2.5 text-xs font-medium hover:underline" data-tone={groupTone(u.groupId, u.groupColor)}>
            <span className="truncate">{u.groupName}</span>
          </AppLink>
        </span>
        <span title={dev.full && u.devicesUsed > u.deviceLimit ? t("users.filter.overDevices") : undefined} className={cx("flex items-center gap-2.5", devCol, off && "opacity-50")}>
          <b className={cx("min-w-9 font-mono text-[13px] font-medium", dev.full && "text-warn-text")}>
            <Scramble once value={dev.text} />
          </b>
          {dev.dots > 0 && (
            <span aria-hidden className="flex gap-[3px] @max-[1040px]/ust:hidden">
              {Array.from({ length: dev.dots }, (_, i) => (
                <i key={i} className={cx("size-1.5 rounded-full", i < u.devicesUsed ? (dev.full ? "bg-warn" : "bg-accent-mark") : "bg-step")} />
              ))}
            </span>
          )}
          {u.deviceLimit > 6 && (
            <i aria-hidden className="block h-1 w-12 rounded-[2px] @max-[1040px]/ust:hidden" style={{ background: `linear-gradient(90deg, ${dev.full ? "var(--warning)" : "var(--accent-mark)"} ${dev.pct}%, var(--step) 0)` }} />
          )}
        </span>
        <span className={cx("flex min-w-0 flex-col gap-1.5", off && "opacity-50")}>
          <span className="flex items-baseline whitespace-nowrap">
            <b className={cx("font-mono text-[13px] font-medium", tone === "crit" && "text-danger-text")}>
              <Scramble once value={use.used} />
            </b>
            <em className="font-mono text-xs text-muted not-italic">{use.of ? ` / ${use.of}` : ` · ${t("ul.noLimit")}`}</em>
          </span>
          <i
            aria-hidden
            className="block h-1 rounded-[2px]"
            style={{
              background:
                tone === "crit"
                  ? "var(--danger)"
                  : tone === "inf"
                    ? "repeating-linear-gradient(90deg, var(--step) 0 4px, transparent 4px 8px)"
                    : `linear-gradient(90deg, ${tone === "warn" ? "var(--warning)" : "var(--accent-mark)"} ${Math.min(100, Math.max(u.usedBytes > 0 ? 2 : 0, use.pct * 100)).toFixed(0)}%, var(--step) 0)`,
            }}
          />
        </span>
        <span className={cx("text-[13px] whitespace-nowrap", toneText[term.tone], termCol, off && "opacity-50")}>{term.text}</span>
        <span className={cx("flex min-w-0 items-center gap-2 text-[13px] whitespace-nowrap", u.online ? "text-fg" : "text-muted", lastCol)}>
          <i aria-hidden className={cx("size-2 flex-none rounded-full", u.online ? "bg-accent-mark" : "bg-[color-mix(in_srgb,var(--text)_22%,transparent)]")} />
          {u.online ? (
            <span className="flex min-w-0 items-baseline gap-1.5 truncate">
              <span>{t("users.now")}</span>
              {u.currentNodeId && (
                <>
                  <span aria-hidden className="text-muted">
                    ·
                  </span>
                  <Link to="/nodes/$id" params={{ id: u.currentNodeId }} title={t("users.col.node")} onClick={stop} className="relative z-10 truncate font-mono text-muted hover:text-fg hover:underline">
                    {u.currentNodeName}
                  </Link>
                </>
              )}
            </span>
          ) : (
            <span className="truncate">{never ? t("ul.neverOnline") : seenText(u, t, now)}</span>
          )}
        </span>
        <Icon name="chevronDown" size={16} className={cx("block flex-none text-muted transition-[transform,color,opacity] duration-250 ease-out", expanded && "rotate-180 text-fg", selecting && "opacity-0")} />
        {/* last, so it lies over what is not a control of its own: the row is one big target (it opens, or ticks while selecting) */}
        <button type="button" aria-expanded={selecting ? undefined : expanded} aria-pressed={selecting ? on : undefined} aria-label={u.name} onClick={selecting ? () => select?.toggle(u) : onOpen} className="absolute inset-0 rounded-[inherit] focus-visible:outline-offset-[-2px]" />
      </div>
      {expanded && <UserExpand u={u} group={group} manage={manage} onLink={onLink} />}
    </Fresh>
  );
}
