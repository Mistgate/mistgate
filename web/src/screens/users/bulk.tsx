import { Menu } from "@base-ui/react/menu";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { createPortal } from "react-dom";
import { ActionMenu, BatchRow, LeaveGuard, useEscClear, type ActionMeta, type Avail, type MenuEntry, type PanelHead, type RowLink } from "@/components/bulk";
import { Avatar } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { selectItemClass, selectPopupClass } from "@/components/ui/select";
import { Tip } from "@/components/ui/tooltip";
import { UserSort, UserStatus, type UserFilter } from "@/gen/mistgate/admin/v1/user_pb";
import { users } from "@/lib/api";
import { useBatch, type Batch, type Job, type Row } from "@/lib/batch";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { avatarIndex, noAccess, shownStatus } from "./format";
import { actionMeta, availability, DeleteDialog, DisableDialog, EnableDialog, ExtendDialog, maxBulkUsers, panelFor, ResetDialog, useUsersBulkRun, type ActionId, type Merge, type RunId } from "./bulk-actions";
import { userN, type UserN as User } from "./model";
import { useTx } from "./t";

// The bulk bar of the People tab (owner and helper), as the redesign board draws it (fz-bulk): over the foot of the page while
// people are ticked, with the first avatars, how many, "select all" under it, the three things done most as buttons with the
// number of people each takes (extend by 30 days, reset the traffic, switch off), a menu of the rest (the term in its three ways,
// switch on, delete) and a cross. The ones that cannot be taken back ask once, in the button itself. A batch takes the
// actions' place: a spinner, its title, a dot a share, "2 of 4"; when it ends, the summary and "Got it". The rules and the calls
// are in bulk-actions.tsx, running them in lib/batch.ts, the parts shared with the Nodes bar in components/bulk.tsx; the look
// (nodes.css: fz-bulk) is the Nodes page's.

/** Away from the People list (another page, or the Groups tab, which takes the list off the screen): paging and sorting stay. */
export const leavesPeople = (next: { fullPath: string; search: Record<string, unknown> }) => next.fullPath.replace(/\/$/, "") !== "/users" || next.search.tab === "groups";

/** The window a bar action opens. */
type Dialog = ActionId;
/** The buttons of the bar: the term (30 days at once), the traffic and the switch (both ask once). */
export type BarId = "extend" | "reset" | "disable" | "enable";
/** What the menus show of an action. */
const menuMeta: Record<ActionId, ActionMeta> = { extend: actionMeta.extend, disable: actionMeta.disable, enable: actionMeta.enable, reset: actionMeta.reset, delete: actionMeta.delete };

// the glyphs of the board's buttons (24 grid, stroke 1.5)
const glyph: Record<BarId, string> = {
  extend: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM12 7v5l3 2",
  reset: "M20 12a8 8 0 1 1-2.3-5.6M20 4v4h-4",
  disable: "M5 5l14 14M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z",
  enable: "M5 12.5l4.5 4.5L19 7.5",
};

export function UsersBulk({ initialAsk, picked, shown, allShown, total, onAll, onClear, onMerge }: { initialAsk?: BarId; picked: readonly User[]; shown: ReadonlySet<string>; allShown: boolean; total: number; onAll: () => void; onClear: () => void; onMerge: Merge }) {
  const t = useTx();
  const batch = useBatch();
  const run = useUsersBulkRun(batch, onMerge, t);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [ask, setAsk] = useState<BarId | null>(initialAsk ?? null);
  const job = batch.job;
  const offPage = useMemo(() => picked.filter((u) => !shown.has(u.id)).length, [picked, shown]);
  // availability reads every selected person once per action: not again for a re-render with the same selection
  const avail = useMemo(() => Object.fromEntries((Object.keys(menuMeta) as ActionId[]).map((a) => [a, availability(a, picked, t)])) as Record<ActionId, Avail>, [picked, t]);
  const disabledCount = picked.filter((u) => u.status === UserStatus.DISABLED).length;
  const close = () => setDialog(null);
  const props = { people: picked, run, onClose: close };

  useEscClear(picked.length > 0, onClear);
  // a question that is not answered goes by itself, and so does one about people who are no longer picked
  useEffect(() => {
    if (!ask) return;
    const id = setTimeout(() => setAsk(null), 5000);
    return () => clearTimeout(id);
  }, [ask]);
  const [seen, setSeen] = useState(picked.length);
  if (seen !== picked.length) {
    setSeen(picked.length);
    if (ask) setAsk(null);
  }

  const panel = job ? panelFor(t, job.spec.action as RunId) : null;
  // after a Disable the summary offers to take it back for the people it did switch off
  const switched = job && !job.running && job.spec.action === "disable" ? job.rows.filter((r) => r.state === "ok" && !r.label) : [];
  const undo = switched.length > 0 && (
    <Button variant="secondary" size="md" disabled={batch.running} onClick={() => run.enableAgain(switched)}>
      <Icon name="check" size={12} />
      {t("bulk.people.undo")}
    </Button>
  );
  const act = (a: BarId) => {
    // switching on says who it takes and who sits out (an expired person is switched on but still gets nothing), in its window
    if (a === "enable") return setDialog("enable");
    if ((a === "disable" || a === "reset") && ask !== a) return setAsk(a);
    setAsk(null);
    if (a === "extend") run.run("extend", picked, { days: 30 });
    else run.run(a, picked);
  };
  const show = picked.length > 0 || !!job;

  return (
    <>
      {show && <div aria-hidden className="h-24 flex-none" />}
      {show &&
        createPortal(
          <UsersBulkBar
            people={picked}
            offPage={offPage}
            allShown={allShown}
            total={total}
            avail={avail}
            toggle={disabledCount * 2 > picked.length ? "enable" : "disable"}
            ask={ask}
            busy={batch.running}
            job={job}
            panel={panel}
            extra={undo}
            onAction={act}
            onMore={setDialog}
            onClear={onClear}
            onAll={onAll}
            onStop={batch.stop}
            onRetry={batch.retry}
            onCloseJob={batch.close}
          />,
          document.body,
        )}
      {dialog === "extend" && <ExtendDialog {...props} />}
      {dialog === "disable" && <DisableDialog {...props} />}
      {dialog === "enable" && <EnableDialog {...props} />}
      {dialog === "reset" && <ResetDialog {...props} />}
      {dialog === "delete" && <DeleteDialog {...props} />}
      <LeaveGuard active={batch.running} leaves={leavesPeople} title={t("bulk.people.leave.title")} body={t("bulk.people.leave.body")} onLeave={batch.stop} />
    </>
  );
}

/** One round button of the bar: 40px, a glyph in the quiet colour. */
const roundButton = "grid size-10 flex-none place-items-center rounded-field text-sec transition-colors duration-150 ease-out hover:bg-step hover:text-fg";
/** Over this many rows the list of a batch keeps only what needs a look (the ones that failed, sat out or are at work). */
const foldRows = 30;

export function UsersBulkBar({
  people,
  offPage,
  allShown,
  total,
  avail,
  toggle,
  ask,
  busy,
  job = null,
  panel = null,
  extra,
  float = true,
  onAction,
  onMore,
  onClear,
  onAll,
  onStop,
  onRetry,
  onCloseJob,
}: {
  people: readonly Pick<User, "id" | "name">[];
  /** Selected on other pages or under another filter. */
  offPage: number;
  allShown: boolean;
  /** How many people the page shows (the ones that can be ticked). */
  total: number;
  avail: Record<ActionId, Avail>;
  /** Which of Disable and Enable the bar itself offers. */
  toggle: "disable" | "enable";
  /** The button that asks "Sure?" now. */
  ask?: BarId | null;
  /** A batch runs: a second one waits for it. */
  busy: boolean;
  job?: Job | null;
  panel?: { head: PanelHead; open: (row: Row) => RowLink | null } | null;
  /** A button of the summary (Enable again). */
  extra?: React.ReactNode;
  /** Floating over the page; false draws it in place (the dev kit). */
  float?: boolean;
  onAction: (a: BarId) => void;
  onMore?: (a: ActionId) => void;
  onClear: () => void;
  onAll: () => void;
  onStop?: () => void;
  onRetry?: () => void;
  onCloseJob?: () => void;
}) {
  const t = useTx();
  const count = people.length;
  const rows = job?.rows ?? [];
  const finished = !!job && !job.running;
  const done = rows.filter((r) => r.state === "ok" || r.state === "bad" || r.state === "notStarted").length;
  const share = rows.filter((r) => r.state !== "skipped").length;
  const trouble = finished && rows.some((r) => r.state === "bad" || r.state === "skipped" || r.state === "notStarted");
  // the people's list opens with the batch when something needs a look; the arrow folds it, for this batch
  const [opened, setOpened] = useState<{ spec: Job["spec"]; open: boolean } | null>(null);
  const list = job ? (opened?.spec === job.spec ? opened.open : trouble || (!finished && rows.length <= 12)) : false;
  const folded = rows.length > foldRows;
  const listed = folded ? rows.filter((r) => r.state === "bad" || r.state === "running" || r.state === "skipped" || r.state === "notStarted") : rows;
  const other = toggle === "disable" ? "enable" : "disable";
  const item = (a: ActionId): MenuEntry<ActionId> => ({ id: a, meta: menuMeta[a], avail: avail[a] });
  // the term in its three ways (a window), the other switch, and the removal
  const more: MenuEntry<ActionId>[] = [item("extend"), item(other), null, item("delete")];
  const barActions: BarId[] = ["extend", "reset", toggle];

  const bar = (
    <div
      role="toolbar"
      aria-label={t("bulk.people.region")}
      className={cx(
        "fz-bulk pointer-events-auto relative isolate flex max-w-full flex-col rounded-[18px] border bg-[color-mix(in_srgb,var(--text)_9%,var(--surface))] shadow-[0_16px_48px_rgb(0_0_0/0.55),0_0_0_1px_rgb(0_0_0/0.3)] max-md:overflow-x-auto max-md:rounded-2xl max-md:[scrollbar-width:none]",
        finished && !trouble ? "border-accent-line" : "border-[color-mix(in_srgb,var(--text)_26%,transparent)]",
      )}
    >
      {job && panel && list && listed.length > 0 && (
        <ul data-batch className="@container grid max-h-[min(40dvh,360px)] min-h-0 grid-cols-[minmax(0,1fr)_auto] content-start gap-x-3 overflow-y-auto overscroll-contain border-b border-line px-4 pt-1 pb-1 @lg:grid-cols-[minmax(0,13rem)_auto_minmax(0,1fr)]">
          {listed.map((r) => (
            <BatchRow key={r.id} row={r} link={panel.open(r)} />
          ))}
          {folded && <li className="col-span-full py-2.5 text-xs text-muted">{t("ul.bulk.folded", { n: rows.length - listed.length })}</li>}
        </ul>
      )}
      <div className="flex min-h-[60px] items-center gap-3 py-2 ps-3 pe-2 max-md:min-h-0 max-md:gap-2 max-md:p-1.5">
        {count > 0 && (
          <div className="flex flex-none items-center gap-3 border-e border-[color-mix(in_srgb,var(--text)_18%,transparent)] pe-3 max-md:pe-2">
            <span aria-hidden className="flex ps-2 max-md:hidden">
              {people.slice(0, 4).map((u) => (
                <span key={u.id} className="fz-bulk-pop -ms-2 flex rounded-full shadow-[0_0_0_2px_var(--surface-2)]">
                  <Avatar name={u.name} index={avatarIndex(u.id)} size={28} />
                </span>
              ))}
            </span>
            <div className="flex flex-col">
              <b className="leading-[18px] font-semibold whitespace-nowrap">{t.n("ul.bulk.selected", count)}</b>
              <span className="flex items-baseline gap-1.5 text-xs leading-4 whitespace-nowrap">
                <button type="button" onClick={onAll} className="text-start text-sec underline-offset-[3px] hover:text-fg hover:underline">
                  {allShown ? t("ul.bulk.none") : t("ul.bulk.all", { n: total })}
                </button>
                {offPage > 0 && <span className="text-muted max-md:hidden">· {t("bulk.people.offPage", { n: offPage })}</span>}
                {count >= maxBulkUsers && <span className="text-muted max-md:hidden">· {t("bulk.people.limit", { n: maxBulkUsers })}</span>}
              </span>
            </div>
          </div>
        )}
        {!job && (
          <div className="flex gap-1">
            {barActions.map((a) => (
              <Act key={a} id={a} avail={avail[a]} asking={ask === a} busy={busy} count={count} onClick={() => onAction(a)} />
            ))}
            <ActionMenu label={t("bulk.more")} items={more} busy={busy} onAction={(a) => onMore?.(a)} iconOnly align="end" />
          </div>
        )}
        {job && !finished && (
          <div className="fz-bulk-in flex h-11 min-w-[360px] items-center gap-3 ps-1 pe-2 max-[1200px]:min-w-0 max-md:min-w-[260px]">
            <span aria-hidden className="size-3.5 flex-none animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-tone-sky border-t-transparent" />
            <b className="font-medium whitespace-nowrap max-md:text-[13px]">{panel?.head.title}</b>
            <Dots done={done} share={share} />
            <span className="ms-auto font-mono text-xs font-normal text-muted tabular-nums">{t("bulk.progress", { done: Math.min(done + 1, share), n: share })}</span>
            {rows.length > 0 && <DetailsToggle open={list} onClick={() => setOpened({ spec: job.spec, open: !list })} />}
            <Button variant="ghost" size="sm" disabled={job.stopping} onClick={onStop}>
              {job.stopping ? t("bulk.stopping") : t("bulk.stop")}
            </Button>
          </div>
        )}
        {job && finished && panel && <Summary job={job} rows={rows} head={panel.head} list={list} extra={extra} onList={() => setOpened({ spec: job.spec, open: !list })} onRetry={onRetry} onClose={onCloseJob} />}
        <button type="button" aria-label={t("bulk.clear")} title={`${t("bulk.clear")} (Esc)`} onClick={onClear} className={roundButton}>
          <Icon name="x" size={16} />
        </button>
      </div>
    </div>
  );
  if (!float) return bar;
  return (
    <div data-nodes-bar className="pointer-events-none fixed inset-x-3 bottom-[104px] z-20 flex justify-center md:inset-s-(--menu-w) md:inset-e-0 md:bottom-6 md:px-5 rail:px-8">
      {bar}
    </div>
  );
}

const barLabel: Record<BarId, Parameters<ReturnType<typeof useTx>>[0]> = { extend: "ul.bulk.extend", reset: "ul.bulk.reset", disable: "ul.bulk.off", enable: "ul.bulk.on" };
const askLabel: Partial<Record<BarId, Parameters<ReturnType<typeof useTx>>[0]>> = { disable: "ul.bulk.askOff", reset: "ul.bulk.askReset" };

/** One of the bar's buttons: its glyph, its name (a tooltip under 1200px, where only the glyph stays) and how many people it takes. */
function Act({ id, avail, asking, busy, count, onClick }: { id: BarId; avail: Avail; asking: boolean; busy: boolean; count: number; onClick: () => void }) {
  const t = useTx();
  const off = busy || avail.count === 0;
  const label = t(asking && askLabel[id] ? askLabel[id]! : barLabel[id]);
  const button = (
    <button
      type="button"
      disabled={off}
      aria-label={t(barLabel[id])}
      onClick={onClick}
      className={cx(
        "group/act inline-flex h-11 flex-none items-center gap-2 rounded-[12px] px-3 font-medium whitespace-nowrap text-fg transition-[background-color,transform] duration-150 ease-out hover:bg-[color-mix(in_srgb,var(--text)_8%,transparent)] active:scale-[0.97] disabled:pointer-events-none disabled:opacity-40 max-md:px-2.5",
        asking && "animate-[fz-pop_0.25s_var(--ease-spring)_both] bg-warn-soft text-warn hover:bg-warn-soft",
      )}
    >
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden className={cx("block flex-none opacity-80 transition-[transform,color,opacity] duration-[250ms] ease-spring group-hover/act:scale-[1.12] group-hover/act:opacity-100", asking ? "text-warn opacity-100" : "group-hover/act:text-accent-mark")}>
        <path d={glyph[id]} />
      </svg>
      <span className={cx(!asking && "max-[1200px]:hidden")}>{label}</span>
      {!asking && avail.count > 0 && <em className="grid h-5 min-w-5 place-items-center rounded-[10px] bg-[color-mix(in_srgb,var(--text)_14%,transparent)] px-1.5 font-mono text-[11px] font-normal text-fg not-italic">{id === "extend" ? count : avail.count}</em>}
    </button>
  );
  // a button that cannot be pressed says why: the people are not fit for it
  if (!busy && avail.count === 0 && avail.why)
    return (
      <Tip label={avail.why}>
        <span tabIndex={0} className="flex rounded-[12px] outline-none focus-visible:ring-2 focus-visible:ring-accent">
          {button}
        </span>
      </Tip>
    );
  return button;
}

/** A dot for each share of a batch (twelve at most): done, the one at work, waiting. */
function Dots({ done, share }: { done: number; share: number }) {
  const n = Math.min(12, share);
  if (n === 0) return null;
  const at = Math.floor((done / Math.max(1, share)) * n);
  return (
    <span aria-hidden className="flex gap-1">
      {Array.from({ length: n }, (_, i) => (
        <i key={i} className={cx("size-2 rounded-full transition-[background-color,transform] duration-300 ease-out", i < at ? "bg-accent-mark" : i === at ? "scale-[1.4] bg-tone-sky" : "bg-step")} />
      ))}
    </span>
  );
}

/** The end of a batch: a mint tick (an amber mark when something failed), what came of it, retry, the list and "Got it". */
function Summary({ job, rows, head, list, extra, onList, onRetry, onClose }: { job: Job; rows: readonly Row[]; head: PanelHead; list: boolean; extra?: React.ReactNode; onList: () => void; onRetry?: () => void; onClose?: () => void }) {
  const t = useTx();
  const count = (s: Row["state"]) => rows.filter((r) => r.state === s).length;
  const [ok, bad, skipped, notStarted] = [count("ok"), count("bad"), count("skipped"), count("notStarted")];
  const again = bad + notStarted;
  const parts = [bad && t.n("bulk.sum.bad", bad), skipped && t.n("bulk.sum.skipped", skipped), notStarted && t.n("bulk.sum.notStarted", notStarted)].filter(Boolean);
  const kind = bad > 0 ? "warn" : "ok";
  return (
    <div className="fz-bulk-in flex min-h-11 min-w-[360px] items-center gap-3 ps-1 pe-2 max-[1200px]:min-w-0 max-md:min-w-[260px]">
      <span aria-hidden className={cx(`tone-${kind} fz-bulk-pop grid size-6 flex-none place-items-center rounded-full bg-(--c) text-(--c-ink)`)}>
        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round">
          <path d={bad > 0 ? "M12 7v6M12 17h.01" : "M5 12.5l4.5 4.5L19 7.5"} />
        </svg>
      </span>
      <div className="flex min-w-0 flex-col">
        <b aria-live="polite" className="font-medium text-pretty max-md:text-[13px]">
          {head.done(rows.length, ok)}
          {parts.map((p) => (
            <span key={String(p)} className="font-normal text-muted">
              {" · "}
              {p}
            </span>
          ))}
        </b>
        {bad > 0 && head.hint && <span className="text-xs leading-4 text-pretty text-muted max-md:hidden">{head.hint}</span>}
      </div>
      <span className="ms-auto flex flex-none items-center gap-1">
        {again > 0 && (job.spec.call || job.spec.many) && (
          <Button variant="secondary" size="md" onClick={onRetry}>
            <Icon name="refresh" size={12} />
            {t("bulk.retry", { n: again })}
          </Button>
        )}
        {extra}
        {rows.length > 0 && <DetailsToggle open={list} onClick={onList} />}
        <Button variant="secondary" size="md" onClick={onClose}>
          {t("ul.bulk.got")}
        </Button>
      </span>
    </div>
  );
}

/** The arrow that folds the list of the people of a batch in or out. */
function DetailsToggle({ open, onClick }: { open: boolean; onClick: () => void }) {
  const t = useTx();
  return (
    <button type="button" aria-expanded={open} aria-label={t("ul.bulk.details")} title={t("ul.bulk.details")} onClick={onClick} className={cx(roundButton, "size-8")}>
      <Icon name="chevronDown" size={14} className={cx("block flex-none transition-transform duration-200", open && "rotate-180")} />
    </button>
  );
}

// ---------------------------------------------------------------------------------------------------
// Select ▾

/** Everybody the filter, the search and the group list, name by name in pages of 200, up to the limit; `more` says there are more. */
async function collect(filter: UserFilter, query: string, groupId: string, signal: AbortSignal): Promise<{ users: User[]; more: boolean }> {
  const found: User[] = [];
  let token = "";
  do {
    const r = await users.listUsers({ filter, query, groupId, pageSize: 200, pageToken: token, sort: UserSort.NAME }, { signal });
    found.push(...r.users.map(userN));
    token = r.nextPageToken;
  } while (token && found.length < maxBulkUsers);
  return { users: found.slice(0, maxBulkUsers), more: token !== "" || found.length > maxBulkUsers };
}

/** The 36px button of the bar that holds a menu (the Nodes bar's barButton). */
export const barButton =
  "inline-flex h-9 flex-none items-center justify-center gap-2 rounded-field border border-line bg-step px-4 text-sm font-medium whitespace-nowrap text-fg transition-[background-color,border-color,transform] duration-150 ease-out hover:bg-(--step-hover) active:scale-[0.98] aria-pressed:border-accent-line aria-pressed:bg-accent-soft data-popup-open:border-accent-line";

/**
 * "Select ▾" in the bar while selecting: every pick replaces the selection. The list is collected when the menu opens (all the
 * pages of the current filter, search and group), so "All found" and the state picks (expired, off, no access, never connected)
 * reach past the page on the screen; until it is there they read "…".
 */
export function UsersPick({ filter, query, groupId, page, any, onPick }: { filter: UserFilter; query: string; groupId: string; page: readonly User[]; any: boolean; onPick: (people: User[]) => void }) {
  const t = useTx();
  const [open, setOpen] = useState(false);
  const scope = useQuery({ queryKey: ["users", "scope", filter, query, groupId], queryFn: ({ signal }) => collect(filter, query, groupId, signal), enabled: open, staleTime: 10_000 });
  const found = scope.data?.users;
  const of = (keep: (u: User) => boolean) => found?.filter(keep).map((u) => u.id);
  const picks = [
    { label: t("bulk.people.pick.page"), ids: page.map((u) => u.id) },
    { label: t("bulk.people.pick.all"), ids: found?.map((u) => u.id) },
    null,
    { label: t("bulk.people.pick.expired"), ids: of((u) => u.status === UserStatus.EXPIRED) },
    { label: t("bulk.people.pick.disabled"), ids: of((u) => u.status === UserStatus.DISABLED) },
    { label: t("bulk.people.pick.noAccess"), ids: of((u) => shownStatus(u) === "active" && noAccess(u)) },
    { label: t("bulk.people.pick.neverSeen"), ids: of((u) => u.lastSeenUnix === 0) },
  ];
  const head = scope.isError ? `${t("bulk.people.pick.failed")}: ${errorText(scope.error, t)}` : scope.data?.more ? t("bulk.people.cap", { n: maxBulkUsers }) : undefined;
  const choose = (ids: readonly string[]) => {
    const byId = new Map([...(found ?? []), ...page].map((u) => [u.id, u]));
    onPick(ids.flatMap((id) => byId.get(id) ?? []).slice(0, maxBulkUsers));
  };
  return (
    <Menu.Root onOpenChange={setOpen}>
      <Menu.Trigger className={cx(barButton, "gap-1.5")}>
        {t("bulk.pick")}
        <Icon name="chevronDown" size={12} className="block flex-none text-muted" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Positioner sideOffset={6} align="end" className="z-50 outline-none">
          <Menu.Popup aria-busy={picks.some((p) => p && !p.ids) || undefined} className={cx(selectPopupClass, "min-w-52")}>
            {head && <p className="px-2.5 py-1.5 text-[11px] font-medium text-muted">{head}</p>}
            {picks.map((p, i) =>
              p ? (
                <Menu.Item key={p.label} disabled={!p.ids || p.ids.length === 0} onClick={() => p.ids && choose(p.ids)} className={cx(selectItemClass, "justify-between gap-4 data-disabled:cursor-default data-disabled:text-muted")}>
                  {p.label}
                  <span className="font-mono text-xs font-medium text-muted tabular-nums">{p.ids ? p.ids.length : "…"}</span>
                </Menu.Item>
              ) : (
                <Menu.Separator key={`sep${i}`} className="mx-2.5 my-1 h-px bg-line" />
              ),
            )}
            <Menu.Separator className="mx-2.5 my-1 h-px bg-line" />
            <Menu.Item disabled={!any} onClick={() => choose([])} className={cx(selectItemClass, "data-disabled:cursor-default data-disabled:text-muted")}>
              {t("bulk.clear")}
            </Menu.Item>
          </Menu.Popup>
        </Menu.Positioner>
      </Menu.Portal>
    </Menu.Root>
  );
}

export type { Batch };
