import type { UseQueryResult } from "@tanstack/react-query";
import { useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { QueryError } from "@/components/ui/query-error";
import { cx } from "@/lib/cx";
import type { Tx } from "@/screens/users/t";
import { hxDemo } from "./demo";
import { Chips, HxChip, Msg, Opt, People, RowHead } from "./hx-parts";
import { templateBytes, templateFileMax, templateLimit, type HeaderProblem, type MihomoTemplate } from "./model";

export type Row = MihomoTemplate & { k: number }; // k: the row's own key, so that a delete keeps what is typed where it was
export type Group = { id: string; name: string };
export type Refusal = { text: string; line?: number };
export type Warning = { code: string; arg: string };
export type Check = UseQueryResult<{ warnings: Warning[]; groupName: string; notServed: string; yaml: string }>;
export type Status = { kind: "bad" | "part" | "ok" | "wait"; label: string };

/** Bytes as KiB with one decimal, in the language's own way of writing a number ("1,2" in Russian). */
export const kb = (bytes: number, t: Tx): string => (bytes / 1024).toLocaleString(t.locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 });

/** What the form itself can see in a text that is worth a look (the panel's own notes come from the panel): no dns of its own, other hosts it names, nobody it is for. */
export function textWarnings(r: Pick<MihomoTemplate, "yaml" | "groupIds">, t: Tx): string[] {
  const out: string[] = [];
  if (r.yaml.trim() !== "" && !/^dns\s*:/m.test(r.yaml)) out.push(t("sx.hx.m.wNoDns"));
  const hosts = [...new Set([...r.yaml.matchAll(/https?:\/\/([^\s/'"]+)/g)].map((m) => m[1]!))].slice(0, 20);
  if (hosts.length > 0) out.push(t("sx.hx.m.wHosts", { hosts: hosts.join(", ") }));
  if (r.groupIds.length === 0) out.push(t("sx.hx.m.wFree"));
  return out;
}

/** What the panel's note codes say; a code of a newer panel gets the general sentence. `unused` is the form's own "nobody it is for". */
export function panelWarnings(w: readonly Warning[], t: Tx): string[] {
  return w.filter((x) => x.code !== "unused").map((x) => t.opt(`subs.mt.warn.${x.code}`, { arg: x.arg }) ?? t("subs.mt.warn.other", { code: x.code }));
}

/** The problems of the form in the board's words where it has them (the name and the text), the product's own for the rest. */
export function problemText(p: HeaderProblem, t: Tx): string {
  switch (p.key) {
    case "subs.mt.err.noName":
      return t("sx.hx.m.eName");
    case "subs.mt.err.empty":
      return t("sx.hx.m.eEmpty");
    case "subs.mt.err.big":
      return t("sx.hx.m.eBig", { max: templateLimit.bytes >> 10 });
    default:
      return t(p.key, p.vars);
  }
}

type RowProps = {
  t: Tx;
  row: Row;
  n: number;
  open: boolean;
  owner: boolean;
  groups: Group[];
  groupsLoading: boolean;
  others: Row[];
  status: Status | null;
  errors: { text: string; line?: number }[];
  warnings: string[];
  asking: boolean;
  atLimit: boolean;
  onToggle: () => void;
  onChange: (p: Partial<MihomoTemplate>) => void;
  onDuplicate: () => void;
  onAsk: (on: boolean) => void;
  onDelete: () => void;
  onFile: (f: File | undefined) => void;
};

/** One template of the list: its head (name, groups and size, state) and, while it is open, its editor. */
export function TemplateRow({ t, row, n, open, owner, groups, groupsLoading, others, status, errors, warnings, asking, atLimit, onToggle, onChange, onDuplicate, onAsk, onDelete, onFile }: RowProps) {
  const area = useRef<HTMLTextAreaElement>(null);
  const file = useRef<HTMLInputElement>(null);
  const [drag, setDrag] = useState(() => hxDemo.drag ?? false);
  const bytes = templateBytes(row.yaml);
  const names = groups.filter((g) => row.groupIds.includes(g.id)).map((g) => g.name);
  const takenBy = (id: string) => others.find((o) => o.groupIds.includes(id));
  const busy = groups.filter((g) => takenBy(g.id));
  const toggle = (id: string) => onChange({ groupIds: row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id] });
  const unnamed = row.name.trim() || t("sx.hx.m.unnamed");
  const goTo = (line: number) => {
    const el = area.current;
    if (!el) return;
    const at = row.yaml.split("\n").slice(0, line - 1).reduce((s, l) => s + l.length + 1, 0);
    el.focus();
    el.setSelectionRange(at, at + (row.yaml.split("\n")[line - 1]?.length ?? 0));
    el.scrollTop = Math.max(0, (line - 3) * 20);
  };
  return (
    <div className={cx("hx-r", status?.kind === "bad" && "bad", open && "on")}>
      <RowHead n={n} open={open} onToggle={onToggle} meta={status?.label ?? ""} metaClass={cx("mt-st", status?.kind === "bad" && "bad", status?.kind === "part" && "part")}>
        <b>{unnamed}</b>
        <span>{t("sx.hx.m.sub", { groups: names.length > 0 ? names.join(", ") : t("sx.hx.m.noGroups"), kib: kb(bytes, t) })}</span>
      </RowHead>
      {open && (
        <div className="hx-ed">
          <label className="hx-f">
            <span>{t("sx.hx.m.name")}</span>
            <input id="mt-name" className="an-i mt-n" type="text" autoComplete="off" spellCheck={false} value={row.name} readOnly={!owner} onChange={(e) => onChange({ name: e.target.value })} />
          </label>
          <Opt label={t("sx.hx.m.groups")}>
            {groups.length === 0 ? (
              <span className="hx-ol">{groupsLoading ? t("common.loading") : t("sx.hx.m.noGroupsYet")}</span>
            ) : (
              <Chips label={t("sx.hx.m.groups")}>
                {groups.map((g) => {
                  const other = takenBy(g.id);
                  return (
                    <HxChip key={g.id} on={row.groupIds.includes(g.id)} off={!owner || !!other} title={other ? t("sx.hx.m.taken", { tpl: other.name.trim() || t("sx.hx.m.unnamed") }) : undefined} onClick={() => toggle(g.id)}>
                      {g.name}
                    </HxChip>
                  );
                })}
              </Chips>
            )}
          </Opt>
          {busy.length > 0 && (
            <p className="hx-note">
              {busy.map((g) => t("sx.hx.m.busy", { group: g.name, tpl: takenBy(g.id)!.name.trim() || t("sx.hx.m.unnamed") })).join(" · ")}. {t("sx.hx.m.busyEnd")}
            </p>
          )}
          <div className="hx-f mt-yf">
            <div className="mt-yh">
              <span>{t("sx.hx.m.text")}</span>
              <span className={cx("mono", bytes > templateLimit.bytes && "bad")}>{t("sx.hx.m.textSz", { kib: kb(bytes, t), max: templateLimit.bytes >> 10 })}</span>
            </div>
            <textarea
              ref={area}
              className={cx("an-i mono mt-y", drag && "drag")}
              rows={16}
              spellCheck={false}
              wrap="off"
              aria-label={t("sx.hx.m.text")}
              data-noi18n
              value={row.yaml}
              readOnly={!owner}
              onChange={(e) => onChange({ yaml: e.target.value })}
              onDragOver={(e) => {
                if (owner && e.dataTransfer.types.includes("Files")) {
                  e.preventDefault();
                  setDrag(true);
                }
              }}
              onDragLeave={() => setDrag(false)}
              onDrop={(e) => {
                setDrag(false);
                const f = e.dataTransfer.files[0];
                if (owner && f) {
                  e.preventDefault();
                  onFile(f);
                }
              }}
            />
            {owner && (
              <div className="mt-up">
                <input ref={file} type="file" hidden accept=".yaml,.yml,.txt,text/yaml,text/plain" aria-label={t("sx.hx.m.load")} onChange={(e) => (onFile(e.target.files?.[0]), (e.target.value = ""))} />
                <Button className="sb-btn" onClick={() => file.current?.click()}>
                  {t("sx.hx.m.load")}
                </Button>
                <span>{t("sx.hx.m.drop")}</span>
              </div>
            )}
          </div>
          {errors.map((m) => (
            <Msg key={m.text} kind="bad">
              {m.text}
              {m.line && (
                <button type="button" className="mono mt-ln" aria-label={t("sx.hx.m.lineGo", { n: m.line })} onClick={() => goTo(m.line!)}>
                  {t("sx.hx.m.line", { n: m.line })}
                </button>
              )}
            </Msg>
          ))}
          {warnings.map((w) => (
            <Msg key={w} kind="part">
              {w}
            </Msg>
          ))}
          {owner &&
            (asking ? (
              <div className="mt-cf" role="alert">
                <span>{names.length === 0 ? t("sx.hx.m.askFree", { name: unnamed }) : t("sx.hx.m.ask", { name: unnamed })}</span>
                <Button className="sb-btn" onClick={() => onAsk(false)}>
                  {t("sx.hx.m.cancel")}
                </Button>
                <Button className="sb-btn hx-del" onClick={onDelete}>
                  {t("sx.hx.m.del")}
                </Button>
              </div>
            ) : (
              <div className="hx-ea mt-ea">
                <Button className="sb-btn" disabled={atLimit} onClick={onDuplicate}>
                  {t("sx.hx.m.dup")}
                </Button>
                <span />
                <span />
                <Button className="sb-btn hx-del" onClick={() => onAsk(true)}>
                  {t("sx.hx.m.del")}
                </Button>
              </div>
            ))}
        </div>
      )}
    </div>
  );
}

export const fileTooBig = (f: File) => f.size > templateFileMax;

type PreviewProps = {
  t: Tx;
  row: Row | undefined;
  rows: Row[];
  groups: Group[];
  users: UseQueryResult<{ id: string; name: string; groupId: string }[]>;
  userId: string;
  onPick: (id: string) => void;
  q: Check;
  /** Nothing is wrong with the open text, so that the panel was asked about it. */
  valid: boolean;
};

/** What one person gets: the person, the verdict and why, and (for the template that is theirs) the profile, read only. */
export function MtPreview({ t, row, rows, groups, users, userId, onPick, q, valid }: PreviewProps) {
  const [anyway, setAnyway] = useState(false);
  const list = users.data ?? [];
  const person = list.find((u) => u.id === userId);
  const r = q.data;
  const group = groups.find((g) => g.id === person?.groupId)?.name ?? r?.groupName ?? "";
  const tOf = person ? rows.findIndex((x) => x.groupIds.includes(person.groupId)) : -1;
  const mine = !!row && tOf >= 0 && rows[tOf]!.k === row.k;
  const tpl = tOf >= 0 ? rows[tOf]!.name.trim() || t("sx.hx.m.unnamed") : "";

  let head: [string, string, string] = ["", "", ""];
  if (rows.length === 0) head = [t("sx.hx.m.rBuilt"), t("sx.hx.m.rNoTpl"), ""];
  else if (row && valid && r?.notServed === "no_access") head = [t("sx.hx.m.rOff"), t("sx.hx.m.rOffS"), "part"];
  else if (row && valid && r?.notServed === "no_servers") head = [t("sx.hx.m.rNoSrv"), t("sx.hx.m.rNoSrvS"), "part"];
  else if (tOf < 0) head = [t("sx.hx.m.rBuilt"), t("sx.hx.m.rNoGrp", { group }), ""];
  else if (!mine) head = [t("sx.hx.m.rOther"), t("sx.hx.m.rOtherS", { group, tpl }), ""];
  else if (!valid) head = [t("sx.hx.m.rBad"), t("sx.hx.m.rBadS"), "bad"];
  else if (!r) head = [t("sx.hx.m.rThis"), t("sx.hx.m.rWait"), ""];
  else head = [t("sx.hx.m.rThis"), t("sx.hx.m.rThisS", { group }), "ok"];

  const ready = !!row && valid && !!r && !r.notServed;
  const profile = ready && (mine || anyway);
  let yaml: ReactNode = null;
  if (profile && r) yaml = <pre className="pp-pre mt-pre" tabIndex={0} aria-label={t("sx.hx.m.pvYaml")} data-noi18n>{r.yaml}</pre>;
  else if (row && valid && !r && q.isError) yaml = <QueryError error={q.error} onRetry={() => void q.refetch()} />;
  else if (row && valid && !r && userId !== "") yaml = <div className="hx-sk" aria-busy="true" />;

  return (
    <section className="card hx-card hx-pv">
      <header className="hx-h">
        <h3>{t("sx.hx.m.pvTitle")}</h3>
      </header>
      <div className="hx-pb">
        {users.isPending ? (
          <div className="hx-sk" />
        ) : users.isError ? (
          <QueryError error={users.error} onRetry={() => void users.refetch()} />
        ) : list.length === 0 ? (
          <p className="hx-note">{t("sx.hx.m.pvNoUsers")}</p>
        ) : (
          <People label={t("sx.hx.m.pvWho")} users={list} value={userId} onPick={onPick} />
        )}
        {(list.length > 0 || rows.length === 0) && (
          <div className={cx("mt-res", head[2])} aria-live="polite">
            <b>{head[0]}</b>
            <span>{head[1]}</span>
          </div>
        )}
        {ready && !mine && !anyway && (
          <button type="button" className="hx-more mt-any" onClick={() => setAnyway(true)}>
            {t("sx.hx.m.pvAnyway")}
          </button>
        )}
        {yaml}
        {profile && <p className="hx-note">{t("sx.hx.m.pvKeys")}</p>}
        <p className="hx-note">{t("sx.hx.m.pvNote")}</p>
      </div>
    </section>
  );
}
