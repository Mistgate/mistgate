import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { Card, Chip, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { QueryError } from "@/components/ui/query-error";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { formatKey, headerFormats, headerLimit, headerNameProblem, headerSize, headerToSave, headerValueProblem, move, type HeaderProblem, type ResponseHeader, type Settings } from "./model";
import { headerPreviewQuery, pickerUsersQuery, useSaveSettings } from "./queries";
import { samples } from "./rules";
import { inputCls, Lead, SaveBar } from "./ui";

type Data = { settings: Settings; reservedHeaderNames: string[] };
type Row = ResponseHeader & { k: number }; // k: the row's own key, so that a move keeps what is typed where it was
type Group = { id: string; name: string };

const strip = ({ k: _k, ...h }: Row): ResponseHeader => h;
const blank = (k: number): Row => ({ k, name: "", value: "", base64: false, formats: [], onlyGroups: false, groupIds: [] });
const eyebrow = "text-[11px] font-bold tracking-[0.1em] text-muted uppercase";

/**
 * "Response headers": the owner's own headers of a successful subscription answer (the flclashx-* ones FlClashX reads, say),
 * each limited to the formats and the groups it goes to, with a preview of what one person gets with one User-Agent. The
 * panel checks every name and value on save; the form says the same early.
 */
export function HeadersTab({ data }: { data: Data }) {
  // keyed by what is saved: after a save (or a change from elsewhere) the form starts again from the server
  return <HeadersForm key={JSON.stringify(data.settings.responseHeaders ?? [])} settings={data.settings} reserved={data.reservedHeaderNames} />;
}

function HeadersForm({ settings, reserved }: { settings: Settings; reserved: string[] }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const groups = useQuery(groupsQuery);
  const groupList: Group[] = (groups.data ?? []).map((g) => ({ id: g.id, name: g.name }));
  const saved = settings.responseHeaders ?? [];
  const keys = useRef(saved.length);
  const [rows, setRows] = useState<Row[]>(() => saved.map((h, k) => ({ ...h, k })));
  const [showNames, setShowNames] = useState(false);
  const known = groups.data ? groupList.map((g) => g.id) : null;

  const problems = rows.map((r) => ({
    name: headerNameProblem(r.name, reserved),
    value: headerValueProblem(r.value, r.base64),
    groups: r.onlyGroups && known !== null && !r.groupIds.some((id) => known.includes(id)) ? ({ key: "subs.hdr.groupsPick" } as HeaderProblem) : null,
  }));
  const total = rows.reduce((n, r) => n + headerSize(r), 0);
  const tooBig = total > headerLimit.total;
  const valid = !tooBig && problems.every((p) => !p.name && !p.value && !p.groups);
  const dirty = JSON.stringify(rows.map(strip)) !== JSON.stringify(saved);

  const patch = (k: number, p: Partial<ResponseHeader>) => setRows((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
  const add = () => setRows((rs) => [...rs, blank(keys.current++)]);

  function onSave() {
    save
      .mutateAsync((cur) => ({ ...cur, responseHeaders: rows.map((r) => headerToSave(strip(r), known)) }))
      .then(() => {
        toast(t("subs.saved"));
        halo("save");
      })
      .catch((e: unknown) => toast.error(errorText(e, t)));
  }

  return (
    <div className="flex flex-col gap-4">
      <Lead>{t("subs.hdr.lead")}</Lead>
      <div className="grid items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Card lg className="flex min-w-0 flex-col gap-3 p-4">
          <div className="flex flex-wrap items-center gap-3">
            <div className="flex min-w-[220px] flex-1 flex-col gap-0.5">
              <SectionLabel icon="list" tone="sky">
                {t("subs.hdr.title")}
              </SectionLabel>
              <span className="text-xs leading-snug text-muted">{t("subs.hdr.hint")}</span>
            </div>
            {rows.length > 0 && (
              <Button variant="secondary" onClick={add} disabled={rows.length >= headerLimit.count}>
                <Icon name="plus" size={14} />
                {t("subs.hdr.add")}
              </Button>
            )}
          </div>

          {rows.length === 0 && (
            <div className="flex flex-col items-center gap-2 rounded-card bg-surface-2 px-5 py-7 text-center">
              <span aria-hidden className="grid size-11 place-items-center rounded-xl bg-surface text-muted">
                <Icon name="list" size={20} />
              </span>
              <b className="text-sm">{t("subs.hdr.noneT")}</b>
              <p className="max-w-[420px] text-xs leading-normal text-muted">{t("subs.hdr.noneBody")}</p>
              <Button variant="primary" size="lg" className="mt-1" onClick={add}>
                <Icon name="plus" size={14} />
                {t("subs.hdr.addFirst")}
              </Button>
            </div>
          )}

          {/* a container: name and value stand side by side where the list is wide */}
          <div className="@container flex flex-col gap-2.5">
            {rows.map((r, i) => (
              <HeaderRow
                key={r.k}
                row={r}
                n={i + 1}
                total={rows.length}
                problem={problems[i]!}
                groups={groupList}
                groupsLoading={groups.isPending}
                t={t}
                onChange={(p) => patch(r.k, p)}
                onMove={(to) => setRows((rs) => move(rs, i, to))}
                onRemove={() => setRows((rs) => rs.filter((x) => x.k !== r.k))}
              />
            ))}
          </div>

          {rows.length > 0 && (
            <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted">
              <span className="tabular-nums">{t("subs.hdr.count", { n: rows.length, max: headerLimit.count })}</span>
              <span className={cx("tabular-nums", tooBig && "font-bold text-danger-text")}>{t("subs.hdr.size", { n: total, max: headerLimit.total })}</span>
            </div>
          )}
          {tooBig && (
            <span role="alert" className="text-[11px] leading-snug text-danger-text">
              {t("subs.hdr.err.total", { max: headerLimit.total, n: total })}
            </span>
          )}

          <div className="flex flex-col gap-2 border-t border-line pt-3">
            <button type="button" aria-expanded={showNames} onClick={() => setShowNames(!showNames)} className="flex w-fit items-center gap-1.5 text-xs font-bold text-muted transition-colors hover:text-fg">
              <Icon name="chevronRight" size={12} className={cx("transition-transform duration-300 ease-spring", showNames && "rotate-90")} />
              {t("subs.hdr.reserved")} · {showNames ? t("subs.hdr.reservedHide") : t("subs.hdr.reservedShow")}
            </button>
            {showNames && (
              <>
                <p className="text-[11px] leading-snug text-muted">{t("subs.hdr.reservedBody")}</p>
                <div className="flex flex-wrap gap-1.5">
                  {reserved.map((name) => (
                    <Chip key={name} mono>
                      {name}
                    </Chip>
                  ))}
                </div>
              </>
            )}
          </div>
        </Card>

        <HeadersPreview t={t} rows={rows} valid={valid} known={known} />
      </div>

      {dirty && <SaveBar busy={save.isPending} canSave={valid && known !== null} onSave={onSave} onDiscard={() => setRows(saved.map((h, k) => ({ ...h, k })))} />}
    </div>
  );
}

/** A pill the size of the filter chips that is pressed or not. */
function ToggleChip({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={cx("flex h-[30px] items-center gap-1.5 rounded-[15px] border px-3 text-xs font-bold transition-colors duration-200", on ? "border-accent-line bg-accent-soft text-fg" : "border-line bg-surface text-muted")}
    >
      {on && <Icon name="check" size={12} />}
      {children}
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

type RowProps = {
  row: Row;
  n: number;
  total: number;
  problem: { name: HeaderProblem | null; value: HeaderProblem | null; groups: HeaderProblem | null };
  groups: Group[];
  groupsLoading: boolean;
  t: Tx;
  onChange: (p: Partial<ResponseHeader>) => void;
  onMove: (to: number) => void;
  onRemove: () => void;
};

function HeaderRow({ row, n, total, problem, groups, groupsLoading, t, onChange, onMove, onRemove }: RowProps) {
  const label = (what: string) => `${t("subs.hdr.n", { n })}: ${what}`;
  const toggleFormat = (f: SubFormat) => {
    const next = row.formats.includes(f) ? row.formats.filter((x) => x !== f) : [...row.formats, f];
    onChange({ formats: next.length === headerFormats.length ? [] : next });
  };
  const toggleGroup = (id: string) => onChange({ groupIds: row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id] });
  return (
    <div className="flex flex-col gap-2.5 rounded-xl border border-line bg-surface p-3">
      {/* name | value | order and delete where the list is wide; name and the buttons, then the value, where it is narrow */}
      <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-2 gap-y-2 @min-[560px]:grid-cols-[minmax(0,2fr)_minmax(0,3fr)_auto]">
        <div className="flex min-w-0 flex-col gap-1.5">
          <span className={cx(eyebrow, "flex min-h-6 items-center")}>{t("subs.hdr.name")}</span>
          <input
            aria-label={label(t("subs.hdr.name"))}
            aria-invalid={(row.name !== "" && !!problem.name) || undefined}
            className={`${inputCls} font-mono`}
            value={row.name}
            placeholder={t("subs.hdr.namePh")}
            onChange={(e) => onChange({ name: e.target.value })}
            autoComplete="off"
            autoCapitalize="off"
            spellCheck={false}
          />
          {row.name !== "" && <Problem p={problem.name} t={t} />}
        </div>
        <div className="col-span-2 row-start-2 flex min-w-0 flex-col gap-1.5 @min-[560px]:col-span-1 @min-[560px]:col-start-2 @min-[560px]:row-start-1">
          <div className="flex min-h-6 items-center justify-between gap-3">
            <span className={eyebrow}>{t("subs.hdr.value")}</span>
            <label title={t("subs.hdr.base64Hint")} className="flex cursor-pointer items-center gap-2 text-xs font-bold text-muted">
              {t("subs.hdr.base64")}
              <Switch checked={row.base64} onCheckedChange={(on) => onChange({ base64: on })} />
            </label>
          </div>
          <input
            aria-label={label(t("subs.hdr.value"))}
            aria-invalid={(row.value !== "" && !!problem.value) || undefined}
            className={`${inputCls} font-mono`}
            value={row.value}
            placeholder={t("subs.hdr.valuePh")}
            onChange={(e) => onChange({ value: e.target.value })}
            autoComplete="off"
            spellCheck={false}
          />
          {row.value !== "" && <Problem p={problem.value} t={t} />}
        </div>
        <span className="col-start-2 row-start-1 mt-[30px] flex h-10 flex-none items-center gap-0.5 @min-[560px]:col-start-3">
          <IconButton aria-label={t("subs.hdr.up", { n })} variant="flat" disabled={n === 1} onClick={() => onMove(n - 2)}>
            <Icon name="arrowUp" size={14} />
          </IconButton>
          <IconButton aria-label={t("subs.hdr.down", { n })} variant="flat" disabled={n === total} onClick={() => onMove(n)}>
            <Icon name="arrowDown" size={14} />
          </IconButton>
          <IconButton aria-label={t("subs.hdr.remove", { n })} variant="flat" onClick={onRemove}>
            <Icon name="trash" size={14} />
          </IconButton>
        </span>
      </div>

      <div className="grid gap-2.5 @min-[560px]:grid-cols-[minmax(0,1fr)_minmax(0,15rem)]">
        <div className="flex min-w-0 flex-col gap-1.5">
          <span className={eyebrow}>{t("subs.hdr.formats")}</span>
          <div role="group" aria-label={label(t("subs.hdr.formats"))} className="flex flex-wrap gap-1.5">
            <ToggleChip on={row.formats.length === 0} onClick={() => onChange({ formats: [] })}>
              {t("subs.hdr.formatsAll")}
            </ToggleChip>
            {headerFormats.map((f) => (
              <ToggleChip key={f} on={row.formats.includes(f)} onClick={() => toggleFormat(f)}>
                {t(formatKey[f]!)}
              </ToggleChip>
            ))}
          </div>
        </div>

        <div className="flex min-w-0 flex-col gap-1.5">
          <span className={eyebrow}>{t("subs.hdr.groups")}</span>
          <Segmented
            variant="thumb"
            wrap
            aria-label={label(t("subs.hdr.groups"))}
            value={row.onlyGroups ? "some" : "all"}
            onValueChange={(v) => onChange({ onlyGroups: v === "some" })}
            options={[
              { value: "all", label: t("subs.hdr.groupsAll") },
              { value: "some", label: t("subs.hdr.groupsSome") },
            ]}
            className="max-w-[360px]"
          />
          {row.onlyGroups &&
            (groups.length === 0 ? (
              <p className="text-xs text-muted">{groupsLoading ? t("common.loading") : t("subs.hdr.groupsNone")}</p>
            ) : (
              <div role="group" aria-label={label(t("subs.hdr.groups"))} className="flex flex-wrap gap-1.5">
                {groups.map((g) => (
                  <ToggleChip key={g.id} on={row.groupIds.includes(g.id)} onClick={() => toggleGroup(g.id)}>
                    {g.name}
                  </ToggleChip>
                ))}
              </div>
            ))}
          <Problem p={problem.groups} t={t} />
        </div>
      </div>
    </div>
  );
}

/** What one person's answer to one User-Agent carries of the list as it stands in the form, asked of the panel. */
function HeadersPreview({ t, rows, valid, known }: { t: Tx; rows: Row[]; valid: boolean; known: string[] | null }) {
  const users = useQuery(pickerUsersQuery);
  const list = users.data ?? [];
  const [picked, setPicked] = useState("");
  const [ua, setUa] = useState<string>(samples[2][1]);
  const userId = list.some((u) => u.id === picked) ? picked : (list[0]?.id ?? "");
  const probe = useDebounced(ua.trim(), 300);
  const draft = useDebounced(JSON.stringify(rows.map((r) => headerToSave(strip(r), known))), 300);
  const q = useQuery({ ...headerPreviewQuery(userId, probe, JSON.parse(draft) as ResponseHeader[]), enabled: valid && userId !== "", placeholderData: keepPreviousData });
  const r = q.data;

  let body: ReactNode;
  if (!valid) body = <p className="text-xs leading-snug text-muted">{t("subs.hdr.pv.fix")}</p>;
  else if (userId === "") body = <p className="text-xs leading-snug text-muted">{users.isPending ? t("common.loading") : t("subs.hdr.pv.noUsers")}</p>;
  else if (q.isError && !r) body = <p className="text-xs leading-snug text-danger-text">{errorText(q.error, t)}</p>;
  else if (!r) body = <div className="h-16 rounded-xl bg-surface-2" />;
  else {
    const why = r.notServed === "page" ? "subs.hdr.pv.page" : r.notServed === "decoy" ? "subs.hdr.pv.decoy" : r.notServed === "no_access" ? "subs.hdr.pv.noAccess" : "";
    body = (
      <div className="flex flex-col gap-2.5" aria-live="polite">
        <div className="flex flex-wrap items-center gap-1.5">
          <Chip>{t("subs.hdr.pv.format", { format: t(formatKey[r.format] ?? "subs.fmt.base64") })}</Chip>
          {r.groupName && <Chip>{t("subs.hdr.pv.group", { group: r.groupName })}</Chip>}
        </div>
        {why ? (
          <p className="text-xs leading-snug text-muted">{t(why as "subs.hdr.pv.page")}</p>
        ) : r.headers.length === 0 ? (
          <p className="text-xs leading-snug text-muted">{t("subs.hdr.pv.none")}</p>
        ) : (
          <div className="flex flex-col gap-1.5">
            {r.headers.map((h, i) => (
              <div key={`${h.name}:${i}`} className="flex flex-col gap-1 rounded-xl border border-line bg-canvas px-3 py-2">
                <b className="font-mono text-xs leading-snug wrap-anywhere">{h.name}</b>
                <span className="font-mono text-xs leading-snug wrap-anywhere">{h.value}</span>
                {h.base64 && <Chip className="self-start">base64</Chip>}
              </div>
            ))}
          </div>
        )}
      </div>
    );
  }

  return (
    <Card lg className="flex min-w-0 flex-col gap-3 p-4 lg:sticky lg:top-[72px]">
      <div className="flex flex-col gap-0.5">
        <SectionLabel icon="code" tone="mint">
          {t("subs.hdr.pv.title")}
        </SectionLabel>
        <span className="text-xs leading-snug text-muted">{t("subs.hdr.pv.hint")}</span>
      </div>
      <div className="flex flex-col gap-1.5">
        <span className={eyebrow}>{t("subs.hdr.pv.as")}</span>
        {users.isPending ? (
          <div className="h-11 rounded-field bg-surface-2" />
        ) : users.isError ? (
          <QueryError error={users.error} onRetry={() => void users.refetch()} />
        ) : list.length === 0 ? null : (
          <Select aria-label={t("subs.hdr.pv.as")} value={userId} onValueChange={setPicked} options={list.map((u) => ({ value: u.id, label: u.name }))} />
        )}
      </div>
      <div className="flex flex-col gap-1.5">
        <span className={eyebrow}>User-Agent</span>
        <input aria-label="User-Agent" placeholder="User-Agent" value={ua} onChange={(e) => setUa(e.target.value)} autoComplete="off" spellCheck={false} className={`${inputCls} font-mono text-xs`} />
        <div className="flex flex-wrap gap-1.5">
          {samples.slice(0, 5).map(([label, value]) => (
            <button key={label} type="button" onClick={() => setUa(value)} className="h-[26px] rounded-[13px] bg-surface-2 px-2.5 text-[11px] font-semibold text-muted transition-colors hover:bg-accent-soft hover:text-fg">
              {label}
            </button>
          ))}
        </div>
      </div>
      {body}
      <p className="text-[11px] leading-snug text-muted">{t("subs.hdr.pv.note")}</p>
    </Card>
  );
}
