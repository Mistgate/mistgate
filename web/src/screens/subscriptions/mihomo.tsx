import { Code, ConnectError } from "@connectrpc/connect";
import { keepPreviousData, useQueries, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { Card, Chip, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Callout } from "@/components/ui/notice";
import { QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useIsOwner } from "@/lib/updates";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { ConfirmModal, useDebounced } from "@/screens/users/ui";
import { linkAutoOn } from "./link-auto";
import { kib, starterTemplate, templateBytes, templateFileMax, templateLimit, templateProblems, templateRefusal, type HeaderProblem, type MihomoTemplate, type Settings } from "./model";
import { mihomoPreviewQuery, pickerUsersQuery, useSaveSettings } from "./queries";
import { inputCls, Lead, SaveBar, ToggleChip } from "./ui";

type Row = MihomoTemplate & { k: number }; // k: the row's own key, so that a delete keeps what is typed where it was
type Group = { id: string; name: string };
type Refusal = { text: string; line?: number };
type Check = UseQueryResult<{ warnings: { code: string; arg: string }[]; groupName: string; notServed: string; yaml: string }>;

const eyebrow = "text-[11px] font-bold tracking-[0.1em] text-muted uppercase";
const norm = (r: MihomoTemplate): MihomoTemplate => ({ name: r.name, yaml: r.yaml, groupIds: r.groupIds });

/** What a failed call says: the panel's own words (English, the line of the text in them) for a refusal, else the usual sentence. */
function refusalOf(e: unknown, t: Tx): Refusal & { index?: number } {
  const c = ConnectError.from(e);
  return c.code === Code.InvalidArgument ? templateRefusal(c.rawMessage) : { text: errorText(e, t) };
}

/** A name not used yet: `base`, else `base 2`, `base 3` ... (kept within the panel's length). */
function freeName(base: string, rows: readonly Row[]): string {
  const used = new Set(rows.map((r) => r.name.trim().toLowerCase()));
  const cut = [...base].slice(0, templateLimit.name - 4).join("");
  for (let n = 1; ; n++) {
    const name = n === 1 ? base : `${cut} ${n}`;
    if (!used.has(name.toLowerCase())) return [...name].slice(0, templateLimit.name).join("");
  }
}

/**
 * "Mihomo templates": the owner's own Mihomo YAML profiles, each for the groups it names. A list on the left, the chosen template's
 * editor and the preview of what one person gets from the text as it stands. The panel checks the text on save (and in the
 * preview, before it); the form says the early part of that and shows what the panel says, with its line.
 */
export function MihomoTab({ data, initial }: { data: { settings: Settings }; initial?: number | null }) {
  // the chosen row survives a save (the form starts again from what is saved, which keeps the rows in the order they were sent)
  const [keep, setKeep] = useState<number | null | undefined>(initial);
  const saved = data.settings.mihomoTemplates ?? [];
  const start = keep === undefined ? (saved.length > 0 ? 0 : null) : keep === null || saved.length === 0 ? null : Math.min(keep, saved.length - 1);
  return <MihomoForm key={JSON.stringify(saved)} settings={data.settings} start={start} onKeep={setKeep} />;
}

function MihomoForm({ settings, start, onKeep }: { settings: Settings; start: number | null; onKeep: (i: number | null) => void }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const owner = useIsOwner();
  const groups = useQuery(groupsQuery);
  const users = useQuery(pickerUsersQuery);
  const groupList: Group[] = (groups.data ?? []).map((g) => ({ id: g.id, name: g.name }));
  const known = groups.data ? groupList.map((g) => g.id) : null;
  const auto = linkAutoOn(settings);
  const saved = settings.mihomoTemplates ?? [];
  const keys = useRef(saved.length);
  const [rows, setRows] = useState<Row[]>(() => saved.map((x, k) => ({ ...x, k })));
  const [sel, setSel] = useState<number | null>(start);
  const [refusal, setRefusal] = useState<(Refusal & { k?: number }) | null>(null);
  const [asking, setAsking] = useState(false);
  const [picked, setPicked] = useState("");

  const i = rows.findIndex((r) => r.k === sel);
  const cur = rows[i];
  const problems = rows.map((_, j) => templateProblems(rows, j));
  const total = rows.reduce((n, r) => n + templateBytes(r.yaml), 0);
  const tooBig = total > templateLimit.total;
  const dirty = JSON.stringify(rows.map(norm)) !== JSON.stringify(saved.map(norm));

  // what the panel says of each text (every template, for the list; the chosen one, for the editor and the preview), as typed 0.4 s ago
  const list = users.data ?? [];
  const listUser = list[0]?.id ?? "";
  const userId = list.some((u) => u.id === picked) ? picked : (list.find((u) => cur?.groupIds.includes(u.groupId))?.id ?? listUser);
  const drafts = useDebounced(rows, 400);
  const checks: Check[] = useQueries({ queries: drafts.map((r) => ({ ...mihomoPreviewQuery(listUser, r.yaml, r.groupIds), enabled: listUser !== "" && r.yaml.trim() !== "" })) }) as Check[];
  const dcur = drafts.find((r) => r.k === sel);
  const q = useQuery({ ...mihomoPreviewQuery(userId, dcur?.yaml ?? "", dcur?.groupIds ?? []), enabled: !!dcur && userId !== "" && dcur.yaml.trim() !== "", placeholderData: keepPreviousData });
  const invalid = (c?: Check) => !!c?.isError && ConnectError.from(c.error).code === Code.InvalidArgument;
  const anyInvalid = checks.some(invalid) || invalid(q as Check);
  const valid = problems.every((p) => !p.name && !p.text) && !tooBig && !anyInvalid;

  const patch = (k: number, p: Partial<MihomoTemplate>) => {
    setRows((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
    setRefusal((r) => (r?.k === k ? null : r));
  };
  const add = (from?: Row) => {
    const k = keys.current++;
    const name = from ? freeName(t("subs.mt.copyOf", { name: from.name.trim() }), rows) : freeName(t("subs.mt.defaultName", { n: rows.length + 1 }), rows);
    setRows((rs) => [...rs, { k, name, yaml: from?.yaml ?? starterTemplate(auto), groupIds: [] }]);
    setSel(k);
  };
  const remove = () => {
    const next = rows.filter((r) => r.k !== sel);
    setRows(next);
    setSel(next.length === 0 ? null : next[Math.min(i, next.length - 1)]!.k);
  };

  function onSave() {
    onKeep(i < 0 ? null : i);
    const out = rows.map((r) => ({ name: r.name.trim(), yaml: r.yaml, groupIds: known ? r.groupIds.filter((g) => known.includes(g)) : [...r.groupIds] }));
    save
      .mutateAsync((c) => ({ ...c, mihomoTemplates: out }))
      .then(() => {
        toast(t("subs.saved"));
        halo("save");
      })
      .catch((e: unknown) => {
        const f = refusalOf(e, t);
        const row = f.index !== undefined ? rows[f.index] : undefined;
        setRefusal({ text: f.text, ...(f.line && { line: f.line }), ...(row && { k: row.k }) });
        if (row) setSel(row.k);
        toast.error(t("subs.mt.refusedAll"));
      });
  }

  // leaving the tab (another tab, another page, closing it) while the form holds changes asks first. A ref: the router asks outside a render.
  const held = useRef(false);
  const leaving = useRef(false);
  useEffect(() => {
    held.current = dirty && owner;
  }, [dirty, owner]);
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) => held.current && !leaving.current && (next.pathname !== current.pathname || (next.search as { tab?: string }).tab !== "mihomo"),
    enableBeforeUnload: () => held.current,
    withResolver: true,
  });

  const statusOf = (r: Row): { kind: StatusKind; label: string } | null => {
    const p = problems[rows.indexOf(r)]!;
    if (p.name || p.text) return { kind: "bad", label: t("subs.mt.bad") };
    const c = r.k === sel ? (q as Check) : checks[drafts.findIndex((d) => d.k === r.k)];
    if (invalid(c)) return { kind: "bad", label: t("subs.mt.bad") };
    const w = c?.data?.warnings;
    if (w) return w.length > 0 ? { kind: "warn", label: t.n("subs.mt.warn", w.length) } : { kind: "ok", label: t("subs.mt.ok") };
    return c?.isFetching ? { kind: "off", label: t("subs.mt.wait") } : null;
  };

  const draftRefusal = q.isError && ConnectError.from(q.error).code === Code.InvalidArgument ? templateRefusal(ConnectError.from(q.error).rawMessage) : null;
  const shown: Refusal | null = cur && refusal?.k === cur.k ? refusal : draftRefusal;
  const wholeRefusal = refusal && refusal.k === undefined ? refusal : null;

  return (
    <div className="flex flex-col gap-4">
      <Lead>{t("subs.mt.lead")}</Lead>
      {!owner && <p className="max-w-[680px] text-xs leading-snug text-muted">{t("subs.mt.ownerOnly")}</p>}

      {rows.length === 0 ? (
        <Card lg className="flex flex-col gap-3 p-4">
          <ListHead t={t} />
          <div className="flex flex-col items-center gap-2 rounded-card bg-surface-2 px-5 py-7 text-center">
            <span aria-hidden className="grid size-11 place-items-center rounded-xl bg-surface text-muted">
              <Icon name="code" size={20} />
            </span>
            <b className="text-sm">{t("subs.mt.noneT")}</b>
            <p className="max-w-[460px] text-xs leading-normal text-muted">{t("subs.mt.noneBody")}</p>
            {owner && (
              <Button variant="primary" size="lg" className="mt-1" onClick={() => add()}>
                <Icon name="plus" size={14} />
                {t("subs.mt.addFirst")}
              </Button>
            )}
          </div>
          {wholeRefusal && <Refused t={t} r={wholeRefusal} all />}
        </Card>
      ) : (
        <div className="grid items-start gap-4 lg:grid-cols-[300px_minmax(0,1fr)]">
          <Card lg className="flex min-w-0 flex-col gap-3 p-4 lg:sticky lg:top-[72px]">
            <ListHead t={t} />
            {owner && (
              <Button variant="secondary" onClick={() => add()} disabled={rows.length >= templateLimit.count} className="self-start">
                <Icon name="plus" size={14} />
                {t("subs.mt.add")}
              </Button>
            )}
            <div className="flex flex-col gap-2">
              {rows.map((r) => (
                <TemplateRow key={r.k} row={r} on={r.k === sel} groups={groupList} status={statusOf(r)} t={t} onPick={() => setSel(r.k)} />
              ))}
            </div>
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted">
              <span className="tabular-nums">{t("subs.mt.count", { n: rows.length, max: templateLimit.count })}</span>
              <span className={cx("tabular-nums", tooBig && "font-bold text-danger-text")}>{t("subs.mt.total", { n: kib(total), max: templateLimit.total >> 10 })}</span>
            </div>
            {tooBig && (
              <span role="alert" className="text-[11px] leading-snug text-danger-text">
                {t("subs.mt.err.total", { n: kib(total), max: templateLimit.total >> 10 })}
              </span>
            )}
            {wholeRefusal && <Refused t={t} r={wholeRefusal} all />}
          </Card>

          {cur && (
            <div className="flex min-w-0 flex-col gap-4">
              <Editor
                t={t}
                row={cur}
                owner={owner}
                groups={groupList}
                groupsLoading={groups.isPending}
                others={rows.filter((r) => r.k !== cur.k)}
                problem={problems[i]!}
                refusal={shown}
                warnings={q.data?.warnings ?? []}
                onChange={(p) => patch(cur.k, p)}
                onDuplicate={() => add(cur)}
                onDelete={() => setAsking(true)}
              />
              <Preview t={t} row={cur} rows={rows} groups={groupList} users={users} userId={userId} onPick={setPicked} q={q as Check} valid={!invalid(q as Check) && cur.yaml.trim() !== ""} />
            </div>
          )}
        </div>
      )}

      {dirty && owner && <SaveBar busy={save.isPending} canSave={valid && known !== null} onSave={onSave} onDiscard={() => { setRows(saved.map((x, k) => ({ ...x, k }))); setSel(start); setRefusal(null); }} />}

      <ConfirmModal
        open={asking}
        onOpenChange={setAsking}
        title={t("subs.mt.delT", { name: cur?.name.trim() || t("subs.mt.unnamed") })}
        description={cur && cur.groupIds.length > 0 ? t.n("subs.mt.delBody", cur.groupIds.length) : t("subs.mt.delNone")}
        confirmLabel={t("subs.mt.delDo")}
        danger
        onConfirm={async () => remove()}
      />
      <ConfirmModal
        open={blocker.status === "blocked"}
        onOpenChange={(o) => {
          if (!o) blocker.reset?.();
        }}
        title={t("subs.mt.leaveT")}
        description={t("subs.mt.leaveBody")}
        confirmLabel={t("subs.mt.leave")}
        danger
        onConfirm={async () => {
          leaving.current = true;
          blocker.proceed?.();
        }}
      />
    </div>
  );
}

function ListHead({ t }: { t: Tx }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <SectionLabel icon="code" tone="sky">
        {t("subs.mt.title")}
      </SectionLabel>
      <span className="text-xs leading-snug text-muted">{t("subs.mt.hint")}</span>
    </div>
  );
}

/** One template in the list: its name and state, the groups it is for, its size. A button: it opens the template in the editor. */
function TemplateRow({ row, on, groups, status, t, onPick }: { row: Row; on: boolean; groups: Group[]; status: { kind: StatusKind; label: string } | null; t: Tx; onPick: () => void }) {
  const names = row.groupIds.map((id) => groups.find((g) => g.id === id)?.name).filter((n): n is string => !!n);
  return (
    <button
      type="button"
      aria-pressed={on}
      aria-label={t("subs.mt.pick", { name: row.name.trim() || t("subs.mt.unnamed") })}
      onClick={onPick}
      className={cx("flex w-full min-w-0 flex-col gap-2 rounded-xl border p-3 text-start transition-colors duration-200", on ? "border-accent-line bg-accent-soft" : "border-line bg-surface hover:bg-surface-2")}
    >
      <span className="flex min-w-0 items-center gap-2">
        <b className="min-w-0 flex-1 truncate text-[13px]">{row.name.trim() || t("subs.mt.unnamed")}</b>
        {status && <StatusPill sm kind={status.kind} label={status.label} />}
      </span>
      <span className="flex min-w-0 flex-wrap items-center gap-1.5">
        {names.length === 0 ? (
          <span className="text-[11px] text-muted">{t("subs.mt.unassigned")}</span>
        ) : (
          <>
            {names.slice(0, 4).map((n) => (
              <Chip key={n} className="max-w-full truncate">
                {n}
              </Chip>
            ))}
            {names.length > 4 && <Chip>+{names.length - 4}</Chip>}
          </>
        )}
        <span className="ms-auto text-[11px] text-muted tabular-nums">{t("subs.mt.size", { n: kib(templateBytes(row.yaml)) })}</span>
      </span>
    </button>
  );
}

function Problem({ p, t }: { p: HeaderProblem | null; t: Tx }) {
  return p ? (
    <span role="alert" className="text-[11px] leading-snug text-danger-text">
      {t(p.key, p.vars)}
    </span>
  ) : null;
}

/** What the panel refused, in its own words under our headline, the line of the text apart. */
function Refused({ t, r, all }: { t: Tx; r: Refusal; all?: boolean }) {
  return (
    <Callout kind="bad" alert>
      <div className="flex min-w-0 flex-col gap-1.5">
        <span className="flex flex-wrap items-center gap-2">
          <b>{t(all ? "subs.mt.refusedAll" : "subs.mt.refused")}</b>
          {r.line && <Chip className="bg-danger-soft text-danger-text">{t("subs.mt.line", { n: r.line })}</Chip>}
        </span>
        <code dir="ltr" className="font-mono text-[11px] leading-snug wrap-anywhere whitespace-pre-wrap">
          {r.text}
        </code>
      </div>
    </Callout>
  );
}

type EditorProps = {
  t: Tx;
  row: Row;
  owner: boolean;
  groups: Group[];
  groupsLoading: boolean;
  others: Row[];
  problem: { name: HeaderProblem | null; text: HeaderProblem | null };
  refusal: Refusal | null;
  warnings: { code: string; arg: string }[];
  onChange: (p: Partial<MihomoTemplate>) => void;
  onDuplicate: () => void;
  onDelete: () => void;
};

function Editor({ t, row, owner, groups, groupsLoading, others, problem, refusal, warnings, onChange, onDuplicate, onDelete }: EditorProps) {
  const toast = useToast();
  const file = useRef<HTMLInputElement>(null);
  const bytes = templateBytes(row.yaml);
  const takenBy = (id: string) => others.find((o) => o.groupIds.includes(id));
  const toggle = (id: string) => onChange({ groupIds: row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id] });
  async function take(f: File | undefined) {
    if (!f || !owner) return;
    if (f.size > templateFileMax) return void toast.error(t("subs.mt.loadBig"));
    onChange({ yaml: await f.text() });
  }
  return (
    <Card lg className="flex min-w-0 flex-col gap-3.5 p-4">
      <div className="flex flex-wrap items-center gap-2">
        <div className="min-w-0 flex-1">
          <SectionLabel icon="tag" tone="lavender">
            {t("subs.mt.edit")}
          </SectionLabel>
        </div>
        {owner && (
          <>
            <Button size="sm" variant="secondary" onClick={onDuplicate} disabled={others.length + 1 >= templateLimit.count}>
              <Icon name="copy" size={13} />
              {t("subs.mt.dup")}
            </Button>
            <Button size="sm" variant="ghostDanger" onClick={onDelete}>
              <Icon name="trash" size={13} />
              {t("subs.mt.del")}
            </Button>
          </>
        )}
      </div>

      <div className="flex flex-col gap-1.5">
        <label htmlFor="mt-name" className={eyebrow}>
          {t("subs.mt.name")}
        </label>
        <input id="mt-name" aria-invalid={!!problem.name || undefined} className={inputCls} value={row.name} readOnly={!owner} placeholder={t("subs.mt.namePh")} onChange={(e) => onChange({ name: e.target.value })} autoComplete="off" spellCheck={false} />
        <Problem p={problem.name} t={t} />
      </div>

      <div className="flex flex-col gap-1.5">
        <span className={eyebrow}>{t("subs.mt.groups")}</span>
        {groups.length === 0 ? (
          <p className="text-xs text-muted">{groupsLoading ? t("common.loading") : t("subs.mt.groupsNone")}</p>
        ) : (
          <div role="group" aria-label={t("subs.mt.groups")} className="flex flex-wrap gap-1.5">
            {groups.map((g) => {
              const other = takenBy(g.id);
              return (
                <ToggleChip key={g.id} on={row.groupIds.includes(g.id)} disabled={!owner || !!other} title={other ? t("subs.mt.takenTitle", { group: g.name, name: other.name.trim() }) : undefined} onClick={() => toggle(g.id)}>
                  <span className="truncate">{g.name}</span>
                  {other && <span className="truncate font-semibold text-faint">{t("subs.mt.takenBy", { name: other.name.trim() })}</span>}
                </ToggleChip>
              );
            })}
          </div>
        )}
        <span className="text-[11px] leading-snug text-muted">{t("subs.mt.groupsHint")}</span>
      </div>

      <div className="flex flex-col gap-1.5">
        <div className="flex min-h-7 flex-wrap items-center justify-between gap-x-3 gap-y-1">
          <span className={eyebrow}>{t("subs.mt.yaml")}</span>
          <span className="flex items-center gap-3">
            <span className={cx("text-[11px] tabular-nums", bytes > templateLimit.bytes ? "font-bold text-danger-text" : "text-muted")}>{t("subs.mt.sizeOf", { n: kib(bytes), max: templateLimit.bytes >> 10 })}</span>
            {owner && (
              <>
                <input ref={file} type="file" hidden accept=".yaml,.yml,.txt,text/yaml,text/plain" aria-label={t("subs.mt.load")} onChange={(e) => { void take(e.target.files?.[0]); e.target.value = ""; }} />
                <Button size="sm" variant="secondary" onClick={() => file.current?.click()}>
                  <Icon name="upload" size={13} />
                  {t("subs.mt.load")}
                </Button>
              </>
            )}
          </span>
        </div>
        <CodeBox label={t("subs.mt.yamlAria")} value={row.yaml} onChange={owner ? (yaml) => onChange({ yaml }) : undefined} onFile={owner ? (f) => void take(f) : undefined} errorLine={refusal?.line} invalid={!!refusal || !!problem.text} />
        <Problem p={problem.text} t={t} />
        {owner && <span className="text-[11px] leading-snug text-muted">{t("subs.mt.dropHint")}</span>}
      </div>

      {refusal && <Refused t={t} r={refusal} />}
      {warnings.length > 0 && (
        <Callout kind="warn">
          <div className="flex min-w-0 flex-col gap-1.5">
            <span className="flex flex-wrap items-baseline gap-x-2">
              <b>{t("subs.mt.warnT")}</b>
              <span className="text-muted">{t("subs.mt.warnNote")}</span>
            </span>
            <ul className="flex flex-col gap-1 ps-4">
              {warnings.map((w, n) => (
                <li key={`${w.code}:${w.arg}:${n}`} className="list-disc leading-snug">
                  {t.opt(`subs.mt.warn.${w.code}`, { arg: w.arg }) ?? t("subs.mt.warn.other", { code: w.code })}
                </li>
              ))}
            </ul>
          </div>
        </Callout>
      )}
    </Card>
  );
}

/** What one person gets: the person, what they get and why, and the profile (read only). */
function Preview({ t, row, rows, groups, users, userId, onPick, q, valid }: { t: Tx; row: Row; rows: Row[]; groups: Group[]; users: UseQueryResult<{ id: string; name: string; groupId: string }[]>; userId: string; onPick: (id: string) => void; q: Check; valid: boolean }) {
  const list = users.data ?? [];
  const person = list.find((u) => u.id === userId);
  const r = q.data;
  const gid = groups.find((g) => g.name === r?.groupName)?.id;
  const mine = gid ? row.groupIds.includes(gid) : false;
  const other = gid && !mine ? rows.find((x) => x.k !== row.k && x.groupIds.includes(gid)) : undefined;
  const name = person?.name ?? "";

  let verdict: ReactNode = null;
  if (r && valid) {
    const body = r.notServed === "no_access" ? t("subs.mt.pv.noAccess", { name }) : r.notServed === "no_servers" ? t("subs.mt.pv.noServers", { name }) : mine ? t("subs.mt.pv.isThis", { name, group: r.groupName }) : other ? t("subs.mt.pv.other", { name, group: r.groupName, tpl: other.name.trim() }) : t("subs.mt.pv.none", { name, group: r.groupName || "—" });
    const own = !!r.notServed || !mine;
    verdict = (
      <div className="flex flex-col gap-2" aria-live="polite">
        <div className="flex flex-wrap items-center gap-1.5">
          <StatusPill kind={own ? "warn" : "ok"} label={own ? (r.notServed ? t("subs.mt.pv.own") : (other?.name.trim() ?? t("subs.mt.pv.own"))) : t("subs.mt.pv.this")} />
          {r.groupName && <Chip>{t("subs.mt.pv.group", { group: r.groupName })}</Chip>}
        </div>
        <p className="text-xs leading-snug">{body}</p>
      </div>
    );
  }

  let yaml: ReactNode;
  if (!valid) yaml = <p className="rounded-card bg-surface-2 px-4 py-6 text-center text-xs leading-snug text-muted">{t("subs.mt.pv.fix")}</p>;
  else if (userId === "") yaml = <p className="rounded-card bg-surface-2 px-4 py-6 text-center text-xs leading-snug text-muted">{users.isPending ? t("common.loading") : t("subs.mt.pv.noUsers")}</p>;
  else if (q.isError && !r) yaml = <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  else if (!r) yaml = <div className="h-60 rounded-card bg-surface-2" aria-label={t("subs.mt.pv.loading")} />;
  else yaml = <CodeBox label={t("subs.mt.pv.yaml")} value={r.yaml} tall />;

  return (
    <Card lg className="flex min-w-0 flex-col gap-3.5 p-4">
      <div className="flex min-w-0 flex-col gap-0.5">
        <SectionLabel icon="eye" tone="mint">
          {t("subs.mt.pv.title")}
        </SectionLabel>
        <span className="text-xs leading-snug text-muted">{t("subs.mt.pv.hint")}</span>
      </div>
      <div className="grid items-start gap-4 lg:grid-cols-[260px_minmax(0,1fr)]">
        <div className="flex min-w-0 flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <span className={eyebrow}>{t("subs.mt.pv.as")}</span>
            {users.isPending ? (
              <div className="h-11 rounded-field bg-surface-2" />
            ) : users.isError ? (
              <QueryError error={users.error} onRetry={() => void users.refetch()} />
            ) : list.length === 0 ? null : (
              <Select aria-label={t("subs.mt.pv.as")} value={userId} onValueChange={onPick} options={list.map((u) => ({ value: u.id, label: u.name }))} />
            )}
          </div>
          {verdict}
          <p className="text-[11px] leading-snug text-muted">{t("subs.mt.pv.note")}</p>
        </div>
        <div className="min-w-0">{yaml}</div>
      </div>
    </Card>
  );
}

/**
 * Text with line numbers: a monospace box that scrolls inside itself (both ways, no wrapping, so a line stays a line), the numbers
 * on its start edge following it. `errorLine` marks a number red; with no `onChange` the text is read only (a selection and a copy
 * still work); `onFile` takes a file dropped on it. Left to right whatever the language: it is code.
 */
function CodeBox({ label, value, onChange, onFile, errorLine, invalid, tall }: { label: string; value: string; onChange?: (v: string) => void; onFile?: (f: File) => void; errorLine?: number; invalid?: boolean; tall?: boolean }) {
  const gutter = useRef<HTMLDivElement>(null);
  const lines = value === "" ? 1 : value.split("\n").length;
  const nums = useMemo(() => Array.from({ length: lines }, (_, n) => n + 1), [lines]);
  return (
    <div dir="ltr" className={cx("flex overflow-hidden rounded-field border bg-canvas transition-colors duration-200 focus-within:border-accent", tall ? "h-[26rem]" : "h-[22rem]", invalid ? "border-danger" : "border-line")}>
      {/* the extra bottom padding is the horizontal scrollbar of the text: both reach their end together */}
      <div ref={gutter} aria-hidden className="flex-none select-none overflow-hidden border-e border-line bg-surface-2 pt-2.5 pb-[30px] text-end font-mono text-xs leading-[18px] text-faint" style={{ width: `${String(lines).length + 3}ch`, minWidth: "4ch" }}>
        {nums.map((n) => (
          <div key={n} className={cx("px-2", n === errorLine && "bg-danger-soft font-bold text-danger-text")}>
            {n}
          </div>
        ))}
      </div>
      <textarea
        aria-label={label}
        value={value}
        readOnly={!onChange}
        wrap="off"
        spellCheck={false}
        autoCapitalize="off"
        autoComplete="off"
        autoCorrect="off"
        onChange={(e) => onChange?.(e.target.value)}
        onScroll={(e) => {
          if (gutter.current) gutter.current.scrollTop = e.currentTarget.scrollTop;
        }}
        onDragOver={(e) => {
          if (onFile && e.dataTransfer.types.includes("Files")) e.preventDefault();
        }}
        onDrop={(e) => {
          const f = e.dataTransfer.files[0];
          if (onFile && f) {
            e.preventDefault();
            onFile(f);
          }
        }}
        className="min-w-0 flex-1 resize-none overflow-auto bg-transparent px-3 py-2.5 font-mono text-xs leading-[18px] whitespace-pre text-fg outline-none"
      />
    </div>
  );
}
