import { useQuery } from "@tanstack/react-query";
import { useMemo, useRef, useState } from "react";
import { ActionMenu, BarAction, BatchPanel, BulkFloat, ClearButton, LeaveGuard, PickMenu, useEscClear, useNarrow, type Avail, type MenuEntry } from "@/components/bulk";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { UserSort, UserStatus, type UserFilter } from "@/gen/mistgate/admin/v1/user_pb";
import { users } from "@/lib/api";
import { useBatch, type Batch } from "@/lib/batch";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { noAccess, shownStatus } from "./format";
import { actionMeta, availability, DeleteDialog, DisableDialog, EnableDialog, ExtendDialog, maxBulkUsers, panelFor, ResetDialog, useUsersBulkRun, type ActionId, type Merge, type RunId } from "./bulk-actions";
import { userN, type UserN as User } from "./model";
import { useTx } from "./t";

// The bulk bar of the People tab (owner and helper): it floats at the foot of the content while people are ticked, in the
// look of the phone's dock (on a phone it takes the dock's place) and the Nodes page's bar, and grows upwards into the rows
// of a running batch. Every action asks first (a window naming who takes part and who sits out). The rules and the calls
// are in bulk-actions.tsx, running them in lib/batch.ts, the parts shared with the Nodes bar in components/bulk.tsx.

/** Away from the People list (another page, or the Groups tab, which takes the list off the screen): paging and sorting stay. */
export const leavesPeople = (next: { fullPath: string; search: Record<string, unknown> }) => next.fullPath.replace(/\/$/, "") !== "/users" || next.search.tab === "groups";

/** The window a bar action opens. */
type Dialog = ActionId;

export function UsersBulk({ picked, shown, onClear, onMerge }: { picked: readonly User[]; shown: ReadonlySet<string>; onClear: () => void; onMerge: Merge }) {
  const t = useTx();
  const batch = useBatch();
  const run = useUsersBulkRun(batch, onMerge, t);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const job = batch.job;
  const offPage = useMemo(() => picked.filter((u) => !shown.has(u.id)).length, [picked, shown]);
  // availability reads every selected person once per action: not again for a re-render with the same selection
  const avail = useMemo(() => Object.fromEntries((Object.keys(menuMeta) as ActionId[]).map((a) => [a, availability(a, picked, t)])) as Record<ActionId, Avail>, [picked, t]);
  const disabledCount = picked.filter((u) => u.status === UserStatus.DISABLED).length;
  const close = () => setDialog(null);
  const props = { people: picked, run, onClose: close };

  useEscClear(picked.length > 0, onClear);

  const panel = job ? panelFor(t, job.spec.action as RunId) : null;
  // after a Disable the summary offers to take it back for the people it did switch off
  const switched = job && !job.running && job.spec.action === "disable" ? job.rows.filter((r) => r.state === "ok" && !r.label) : [];
  const undo = switched.length > 0 && (
    <Button variant="secondary" size="sm" disabled={batch.running} onClick={() => run.enableAgain(switched)}>
      <Icon name="check" size={12} />
      {t("bulk.people.undo")}
    </Button>
  );

  return (
    <>
      <BulkFloat show={picked.length > 0 || !!job} wide={!!job} region={t("bulk.people.region")}>
        {job && panel && <BatchPanel job={job} rows={job.rows} head={panel.head} open={panel.open} fold={30} extra={undo} onStop={batch.stop} onRetry={batch.retry} onClose={batch.close} />}
        {picked.length > 0 && (
          <UsersBulkBar count={picked.length} offPage={offPage} avail={avail} toggle={disabledCount * 2 > picked.length ? "enable" : "disable"} busy={batch.running} onAction={setDialog} onClear={onClear} />
        )}
      </BulkFloat>
      {dialog === "extend" && <ExtendDialog {...props} />}
      {dialog === "disable" && <DisableDialog {...props} />}
      {dialog === "enable" && <EnableDialog {...props} />}
      {dialog === "reset" && <ResetDialog {...props} />}
      {dialog === "delete" && <DeleteDialog {...props} />}
      <LeaveGuard active={batch.running} leaves={leavesPeople} title={t("bulk.people.leave.title")} body={t("bulk.people.leave.body")} onLeave={batch.stop} />
    </>
  );
}

/** What the menus show of an action. */
const menuMeta: Record<ActionId, (typeof actionMeta)[RunId]> = { extend: actionMeta.extend, disable: actionMeta.disable, enable: actionMeta.enable, reset: actionMeta.reset, delete: actionMeta.delete };

/**
 * The bar's row: clear, the count, Extend…, the switch (Disable, or Enable while most of the selection is off) and More ▾
 * (Reset traffic, the other switch, Delete…). Narrow, the switch goes into More as well. Steps 3 and 4 add Group… beside
 * Extend… and Limits…, Servers…, DNS… and Telegram… at the head of More.
 */
export function UsersBulkBar({
  count,
  offPage,
  avail,
  toggle,
  busy,
  onAction,
  onClear,
}: {
  count: number;
  /** Selected on other pages or under another filter. */
  offPage: number;
  avail: Record<ActionId, Avail>;
  /** Which of Disable and Enable the bar itself offers. */
  toggle: "disable" | "enable";
  /** A batch runs: a second one waits for it. */
  busy: boolean;
  onAction: (a: ActionId) => void;
  onClear: () => void;
}) {
  const t = useTx();
  const ref = useRef<HTMLDivElement>(null);
  const narrow = useNarrow(ref);
  const item = (a: ActionId): MenuEntry<ActionId> => ({ id: a, meta: menuMeta[a], avail: avail[a] });
  const other = toggle === "disable" ? "enable" : "disable";
  const more = narrow ? [item(toggle), item(other), item("reset"), null, item("delete")] : [item("reset"), item(other), null, item("delete")];
  const note = narrow && offPage > 0 ? t("bulk.people.offPage", { n: offPage }) : undefined;
  return (
    <div ref={ref} className="flex h-[60px] flex-none items-center gap-1 p-2.5">
      <ClearButton onClear={onClear} />
      {/* wide, the words keep their room (so the row's measure is honest); narrow, the count takes what is left */}
      <div className={cx("flex items-center gap-2", narrow ? "min-w-0 flex-1" : "flex-none pe-1")}>
        <span className="inline-flex h-6 min-w-6 flex-none items-center justify-center rounded-full bg-accent-soft px-2 font-mono text-xs font-bold text-accent-text tabular-nums">{count}</span>
        <span className="truncate text-[13px] font-semibold whitespace-nowrap">{t.n(narrow ? "bulk.people.selectedShort" : "bulk.people.selected", count)}</span>
        {!narrow && offPage > 0 && <span className="text-xs whitespace-nowrap text-muted">· {t("bulk.people.offPage", { n: offPage })}</span>}
        {!narrow && count >= maxBulkUsers && <span className="text-xs whitespace-nowrap text-muted">· {t("bulk.people.limit", { n: maxBulkUsers })}</span>}
      </div>
      <span aria-hidden className={cx("h-6 w-px flex-none bg-line", narrow ? "mx-0.5" : "mx-1")} />
      <BarAction meta={menuMeta.extend} avail={avail.extend} busy={busy} onClick={() => onAction("extend")} />
      {!narrow && <BarAction meta={menuMeta[toggle]} avail={avail[toggle]} busy={busy} onClick={() => onAction(toggle)} count={avail[toggle].count < count} />}
      <ActionMenu label={t("bulk.more")} items={more} note={note} busy={busy} onAction={onAction} iconOnly={narrow} align="end" />
    </div>
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

/**
 * "Select ▾" in the filter row: every pick replaces the selection. The list is collected when the menu opens (all the pages
 * of the current filter, search and group), so "All found" and the state picks (expired, off, no access, never connected)
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
  return (
    <PickMenu
      picks={picks}
      any={any}
      head={head}
      onOpenChange={setOpen}
      onPick={(ids) => {
        const byId = new Map([...(found ?? []), ...page].map((u) => [u.id, u]));
        onPick(ids.flatMap((id) => byId.get(id) ?? []).slice(0, maxBulkUsers));
      }}
    />
  );
}

export type { Batch };
