import { Code } from "@connectrpc/connect";
import { useState } from "react";
import { Footer, TargetList, type PanelHead, type RowLink } from "@/components/bulk";
import { FilterChips } from "@/components/ui/chips";
import { Modal } from "@/components/ui/modal";
import { Callout } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { TextField } from "@/components/ui/text-field";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { MessageKey } from "@/i18n/en";
import { users } from "@/lib/api";
import type { Batch, Outcome, Row, Skipped, Spec, Target } from "@/lib/batch";
import { errorText } from "@/lib/errors";
import type { ActionMeta, Avail } from "@/components/bulk";
import { big, userN, type UserN as User } from "./model";
import { isCode } from "./rpc";
import { useTx, type Tx } from "./t";
import { TypeConfirmModal } from "./ui";

// The actions of the Users page's bulk bar (screens/users/bulk.tsx): which selected people each one takes and why the
// others sit out (worked out here, in the browser, from what the page already holds), the calls and how their answers
// read, and the confirms. The server takes many ids in one call for the term in days, disable, enable, the traffic reset
// and the delete: those go in slices of 200, one slice after another (lib/batch.ts); a fixed end date or "no expiry" is
// one UpdateUser per person, four at a time. A subscription link, a page password, a Telegram or AmneziaVPN key is never
// fetched, shown or changed here: they stay in the person's card.

/** At most this many people are selected at once. */
export const maxBulkUsers = 5000;
/** The server's own limit per call is 5000; slices of 200 keep each call short (the edge answers within one request's budget). */
const slice = 200;

/** What the bar and its menus offer. */
export type ActionId = "extend" | "disable" | "enable" | "reset" | "delete";
/** What can run: the term has three ways, each with its own words. */
export type RunId = "extend" | "until" | "never" | "disable" | "enable" | "reset" | "delete";

type Meta = ActionMeta & { title: MessageKey; done: MessageKey };

/** One tone per meaning: the term and traffic sand, a switch off or a removal rose, a switch on mint. */
export const actionMeta: Record<RunId, Meta> = {
  extend: { icon: "calendar", tone: "sand", label: "bulk.people.extend", title: "bulk.people.title.extend", done: "bulk.people.done.extend" },
  until: { icon: "calendar", tone: "sand", label: "bulk.people.extend", title: "bulk.people.title.until", done: "bulk.people.done.until" },
  never: { icon: "calendar", tone: "sand", label: "bulk.people.extend", title: "bulk.people.title.never", done: "bulk.people.done.never" },
  disable: { icon: "shieldOff", tone: "rose", label: "bulk.people.disable", title: "bulk.people.title.disable", done: "bulk.people.done.disable" },
  enable: { icon: "check", tone: "mint", label: "bulk.people.enable", title: "bulk.people.title.enable", done: "bulk.people.done.enable" },
  reset: { icon: "traffic", tone: "sand", label: "bulk.people.reset", title: "bulk.people.title.reset", done: "bulk.people.done.reset" },
  delete: { icon: "trash", tone: "rose", label: "bulk.people.delete", title: "bulk.people.title.delete", done: "bulk.people.done.delete", danger: true },
};

/** What a run needs besides the people: the days to add, or the end date (Unix seconds). */
export type Extra = { days?: number; until?: number };

/** Why a selected person sits a run out, or null when they take part. */
export function skipOf(run: RunId, u: User, extra: Extra = {}): MessageKey | null {
  switch (run) {
    case "extend":
      // "never expires" people have no date to move: adding days would put a term on them
      return u.expiresUnix > 0 ? null : "bulk.people.skip.noTerm";
    case "until":
      return extra.until && u.expiresUnix === extra.until ? "bulk.people.skip.same" : null;
    case "never":
      return u.expiresUnix === 0 ? "bulk.people.skip.never" : null;
    case "disable":
      return u.status === UserStatus.DISABLED ? "bulk.people.skip.disabled" : null;
    case "enable":
      return u.status === UserStatus.DISABLED ? null : "bulk.people.skip.enabled";
    case "reset":
      return u.usedBytes > 0 ? null : "bulk.people.skip.noTraffic";
    case "delete":
      return null;
  }
}

/** The selected people a run takes, in their order, and the ones it skips with their reason. */
export function split(run: RunId, people: readonly User[], extra: Extra, t: Tx): { targets: Target[]; skipped: Skipped[]; taking: User[] } {
  const targets: Target[] = [];
  const skipped: Skipped[] = [];
  const taking: User[] = [];
  for (const u of people) {
    const why = skipOf(run, u, extra);
    if (why) skipped.push({ id: u.id, name: u.name, host: "", note: t(why) });
    else {
      targets.push({ id: u.id, name: u.name, host: "" });
      taking.push(u);
    }
  }
  return { targets, skipped, taking };
}

/** How many people a bar action takes and, when none, why (the most common reason). Opening the term's window takes everybody. */
export function availability(action: ActionId, people: readonly User[], t: Tx): Avail {
  if (action === "extend") return { count: people.length };
  const reasons = people.map((u) => skipOf(action, u));
  const count = reasons.filter((r) => r === null).length;
  if (count > 0) return { count };
  const tally = new Map<MessageKey, number>();
  for (const r of reasons) if (r) tally.set(r, (tally.get(r) ?? 0) + 1);
  const top = [...tally].sort((a, b) => b[1] - a[1])[0]?.[0];
  return { count, why: top ? t(top) : undefined };
}

/** How a batch of `run` heads its panel, and that a failed row offers the person's own page. */
export function panelFor(t: Tx, run: RunId): { head: PanelHead; open: (row: Row) => RowLink | null } {
  const m = actionMeta[run];
  return {
    head: { icon: m.icon, tone: m.tone, title: t(m.title), done: (n, ok) => t.n(m.done, n, { ok }) },
    open: (row) => (row.state === "bad" ? { to: "/users/$id", params: { id: row.id } } : null),
  };
}

// ---------------------------------------------------------------------------------------------------
// Running

/** What came back: the people as they are now (merged into the selection), and the ids that are gone. */
export type Merge = (people: User[], gone?: readonly string[]) => void;

/** The calls of every run, and `run`, which starts one over the selected people. */
export function useUsersBulkRun(batch: Batch, merge: Merge, t: Tx) {
  const gone = (): Outcome => ({ kind: "off", label: t("bulk.people.gone") });
  const fail = (e: unknown): string | Outcome => (isCode(e, Code.NotFound) ? gone() : errorText(e, t));
  /** The ids a many-call did not answer for were gone by then. */
  const goneOf = (ids: readonly string[], answered: readonly User[]): Record<string, Outcome> => {
    const seen = new Set(answered.map((u) => u.id));
    return Object.fromEntries(ids.filter((id) => !seen.has(id)).map((id) => [id, gone()]));
  };
  /** One server call for a slice of ids that answers with the people as they are now. */
  const sliceCall = (call: (ids: string[]) => Promise<{ users: Parameters<typeof userN>[0][] }>): NonNullable<Spec["many"]> => async (ids) => {
    const answered = (await call(ids)).users.map(userN);
    const missing = goneOf(ids, answered);
    merge(answered, Object.keys(missing));
    return missing;
  };
  const common = { error: fail, invalidate: [["users"]] };
  const setUntil = (expiresUnix: bigint): NonNullable<Spec["call"]> => async (id) => {
    const r = await users.updateUser({ userId: id, expiresUnix });
    if (r.user) merge([userN(r.user)]);
  };

  function spec(run: RunId, extra: Extra): Spec {
    switch (run) {
      case "extend":
        return { ...common, action: run, limit: 1, chunk: slice, many: sliceCall((userIds) => users.extendUsers({ userIds, days: extra.days ?? 0 })) };
      case "until":
        return { ...common, action: run, limit: 4, call: setUntil(big(extra.until ?? 0)) };
      case "never":
        return { ...common, action: run, limit: 4, call: setUntil(0n) };
      case "disable":
      case "enable":
        return { ...common, action: run, limit: 1, chunk: slice, many: sliceCall((userIds) => users.setUsersEnabled({ userIds, enabled: run === "enable" })) };
      case "reset":
        return { ...common, action: run, limit: 1, chunk: slice, many: sliceCall((userIds) => users.resetUserTraffic({ userIds })) };
      case "delete":
        return {
          ...common,
          action: run,
          limit: 1,
          chunk: slice,
          invalidate: [["users"], ["groups"]],
          many: async (userIds) => {
            await users.deleteUsers({ userIds });
            merge([], userIds);
          },
        };
    }
  }

  return {
    run(run: RunId, people: readonly User[], extra: Extra = {}) {
      const { targets, skipped } = split(run, people, extra, t);
      batch.start(spec(run, extra), targets, skipped);
    },
    /** "Enable again" after a Disable: the rows that went well, started as an Enable of their own. */
    enableAgain(rows: readonly Row[]) {
      batch.start(
        spec("enable", {}),
        rows.map((r) => ({ id: r.id, name: r.name, host: "" })),
      );
    },
  };
}

export type UsersRun = ReturnType<typeof useUsersBulkRun>;

// ---------------------------------------------------------------------------------------------------
// Confirms

type DialogProps = { people: readonly User[]; run: UsersRun; onClose: () => void };

/** Who takes part and who sits out: the first names and "and 12 more", the skipped by their reason. */
function Who({ targets, skipped }: { targets: readonly Target[]; skipped: readonly Skipped[] }) {
  const t = useTx();
  return (
    <>
      <TargetList title={t("bulk.people.list.targets", { n: targets.length })} targets={targets} skipped={skipped} max={10} />
      {targets.length > slice && <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.people.pace", { n: slice })}</p>}
    </>
  );
}

function TextDialog({ id, people, run, onClose, danger, extra }: DialogProps & { id: "disable" | "enable" | "reset"; danger?: boolean; extra?: (taking: User[]) => string | null }) {
  const t = useTx();
  const { targets, skipped, taking } = split(id, people, {}, t);
  const warn = extra?.(taking);
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n(`bulk.people.${id}.title`, targets.length)}
      description={t(`bulk.people.${id}.body`)}
      footer={
        <Footer
          danger={danger}
          label={t(`bulk.people.${id}.go`)}
          disabled={targets.length === 0}
          onClose={onClose}
          go={() => {
            onClose();
            run.run(id, people);
          }}
        />
      }
    >
      {warn && <Callout kind="warn">{warn}</Callout>}
      <Who targets={targets} skipped={skipped} />
    </Modal>
  );
}

/** Disable: the people lose access at once; their links and keys stay. */
export const DisableDialog = (p: DialogProps) => <TextDialog {...p} id="disable" danger />;
/** Enable: a person whose term has run out is switched on but gets nothing until they are extended. */
export function EnableDialog(p: DialogProps) {
  const t = useTx();
  const [now] = useState(() => Math.floor(Date.now() / 1000));
  return <TextDialog {...p} id="enable" extra={(taking) => ((n) => (n > 0 ? t.n("bulk.people.enable.expired", n) : null))(taking.filter((u) => u.expiresUnix > 0 && u.expiresUnix <= now).length)} />;
}
export const ResetDialog = (p: DialogProps) => <TextDialog {...p} id="reset" />;

/** Delete: the number of people is typed to confirm; the links and keys die with them. */
export function DeleteDialog({ people, run, onClose }: DialogProps) {
  const t = useTx();
  const { targets, skipped } = split("delete", people, {}, t);
  return (
    <TypeConfirmModal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("bulk.people.delete.title", targets.length)}
      description={t("bulk.people.delete.body")}
      confirmLabel={t("bulk.people.delete.go")}
      match={String(targets.length)}
      onConfirm={async () => {
        run.run("delete", people);
        onClose();
      }}
    >
      <Who targets={targets} skipped={skipped} />
      <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.people.delete.stepUp")}</p>
    </TypeConfirmModal>
  );
}

type Mode = "days" | "until" | "never";
const presets = ["7", "30", "90", "365"] as const;

/** The date input's value ("2026-11-05") for a Date, in the local time zone. */
const dayOf = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
/** The last second of a local day, in Unix seconds: "until the 5th" means the whole of the 5th. */
export function endOfDay(value: string): number {
  const [y, m, d] = value.split("-").map(Number);
  return y && m && d ? Math.floor(new Date(y, m - 1, d, 23, 59, 59).getTime() / 1000) : 0;
}

/** The term: add days (from the later of today and the current end), set an end date, or take the end away. */
export function ExtendDialog({ people, run, onClose }: DialogProps) {
  const t = useTx();
  const [mode, setMode] = useState<Mode>("days");
  const [days, setDays] = useState("30");
  const [date, setDate] = useState("");
  const [tomorrow] = useState(() => dayOf(new Date(Date.now() + 86400_000)));
  const n = Number(days);
  const daysOk = Number.isInteger(n) && n >= 1 && n <= 36500;
  const until = date >= tomorrow ? endOfDay(date) : 0;
  const id: RunId = mode === "days" ? "extend" : mode === "until" ? "until" : "never";
  const { targets, skipped, taking } = split(id, people, { days: n, until }, t);
  const shorter = mode === "until" && until > 0 ? taking.filter((u) => u.expiresUnix > until).length : 0;
  const ready = targets.length > 0 && (mode === "never" || (mode === "days" ? daysOk : until > 0));
  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t.n("bulk.people.extend.title", people.length)}
      footer={
        <Footer
          label={mode === "days" ? t.n("bulk.people.extend.go.days", daysOk ? n : 0) : t(mode === "until" ? "bulk.people.extend.go.until" : "bulk.people.extend.go.never")}
          disabled={!ready}
          onClose={onClose}
          go={() => {
            onClose();
            run.run(id, people, { days: n, until });
          }}
        />
      }
    >
      <Segmented
        variant="thumb"
        aria-label={t("bulk.people.extend.mode")}
        value={mode}
        onValueChange={setMode}
        options={[
          { value: "days", label: t("bulk.people.extend.byDays") },
          { value: "until", label: t("bulk.people.extend.toDate") },
          { value: "never", label: t("bulk.people.extend.never") },
        ]}
      />
      {mode === "days" && (
        <div className="flex flex-col gap-3">
          <FilterChips aria-label={t("bulk.people.extend.days")} value={days} onValueChange={setDays} options={presets.map((p) => ({ value: p, label: <span className="font-mono tabular-nums">+{p}</span> }))} />
          <div className="flex flex-col gap-1.5">
            <TextField
              label={t("bulk.people.extend.days")}
              type="number"
              inputMode="numeric"
              min={1}
              max={36500}
              value={days}
              onChange={(e) => setDays(e.target.value)}
              error={days !== "" && !daysOk ? "1–36500" : undefined}
              mono
              className="max-w-48"
            />
            <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.people.extend.daysHint")}</p>
          </div>
        </div>
      )}
      {mode === "until" && (
        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <TextField label={t("bulk.people.extend.date")} type="date" min={tomorrow} value={date} onChange={(e) => setDate(e.target.value)} className="max-w-60" />
            <p className="text-xs leading-snug text-pretty text-muted">{t("bulk.people.extend.dateHint")}</p>
          </div>
          {shorter > 0 && <Callout kind="warn">{t.n("bulk.people.extend.shorten", shorter)}</Callout>}
        </div>
      )}
      {mode === "never" && <p className="text-[13px] leading-normal text-muted">{t("bulk.people.extend.neverHint")}</p>}
      <Who targets={targets} skipped={skipped} />
    </Modal>
  );
}
