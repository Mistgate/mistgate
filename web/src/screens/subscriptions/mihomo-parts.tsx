import type { UseQueryResult } from "@tanstack/react-query";
import type { Ace } from "ace-builds";
import { useId, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { QueryError } from "@/components/ui/query-error";
import { downloadText } from "@/lib/awg";
import { cx } from "@/lib/cx";
import type { Tx } from "@/screens/users/t";
import { hxDemo } from "./demo";
import { Chips, HxChip, Msg, Opt, People, RowHead } from "./hx-parts";
import { condition, editSource, groupLimit, inactiveRefs, sourceSelection, variants, writeTransfer, type Group, type Selection, type Source, type SourceDraft, type SourceIssue } from "./mihomo-source";
import { MihomoSourceEditor } from "./mihomo-source-editor";
import { MihomoYamlPreview } from "./mihomo-yaml-preview";
import { templateBytes, templateFileMax, templateLimit, type HeaderProblem, type MihomoTemplate } from "./model";

export type Row = MihomoTemplate & { k: number; source?: SourceDraft }; // k: the row's own key, so that a delete keeps what is typed where it was
export type Refusal = { text: string; line?: number };
export type Warning = { code: string; arg: string };
export type Check = UseQueryResult<{ warnings: Warning[]; groupName: string; notServed: string; yaml: string }>;
export type Status = { kind: "bad" | "part" | "ok" | "wait"; label: string };

/** Bytes as KiB with one decimal, in the language's own way of writing a number ("1,2" in Russian). */
export const kb = (bytes: number, t: Tx): string => (bytes / 1024).toLocaleString(t.locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 });

/** What the form itself can see in a text that is worth a look (the panel's own notes come from the panel): no dns of its own, other hosts it names, nobody it is for. */
export function textWarnings(r: Pick<MihomoTemplate, "yaml" | "groupIds"> & { source?: Source }, t: Tx): string[] {
  const out: string[] = [];
  if (r.source) {
    for (const yaml of variants(r.yaml, r.source, r.groupIds)) {
      if (yaml.trim() !== "" && !/^dns\s*:/m.test(yaml)) {
        out.push(t("sx.hx.m.wNoDns"));
        break;
      }
    }
  }
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

export function sourceIssueText(issue: SourceIssue, t: Tx): string {
  return t(`sx.hx.m.source.${issue.kind}`, { group: issue.group ?? "" });
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
  draft: SourceDraft | undefined;
  source: { yaml: string; source?: Source; issue?: SourceIssue };
  status: Status | null;
  errors: { text: string; line?: number }[];
  warnings: string[];
  asking: boolean;
  atLimit: boolean;
  onToggle: () => void;
  onChange: (p: Partial<Omit<Row, "k">>) => void;
  onDuplicate: () => void;
  onAsk: (on: boolean) => void;
  onDelete: () => void;
  onFile: (f: File | undefined) => void;
};

/** One template of the list: its head (name, groups and size, state) and, while it is open, its editor. */
export function TemplateRow({ t, row, n, open, owner, groups, groupsLoading, draft, source, others, status, errors, warnings, asking, atLimit, onToggle, onChange, onDuplicate, onAsk, onDelete, onFile }: RowProps) {
  const editor = useRef<Ace.Editor>(null);
  const helpId = useId();
  const file = useRef<HTMLInputElement>(null);
  const [drag, setDrag] = useState(() => hxDemo.drag ?? false);
  const [selectedVariant, setSelectedVariant] = useState("all");
  const [operation, setOperation] = useState<SourceIssue | null>(null);
  const [download, setDownload] = useState<string | null>(null);
  const [chooser, setChooser] = useState<{ draft: SourceDraft; selection: Selection; ids: string[]; from: number; to: number } | null>(null);
  const bytes = templateBytes(source.yaml);
  const assigned = groups.filter((g) => row.groupIds.includes(g.id));
  const names = assigned.map((g) => g.name);
  const takenBy = (id: string) => others.find((o) => o.groupIds.includes(id));
  const busy = groups.filter((g) => takenBy(g.id));
  const toggle = (id: string) => onChange({ groupIds: row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id] });
  const selected = chooser?.ids.filter((id) => assigned.some((g) => g.id === id)) ?? [];
  const inactive = source.source && !groupsLoading ? inactiveRefs(source.source, groups, row.groupIds) : [];
  const blocks = source.source?.blocks ?? [];
  const variant = selectedVariant === "" || assigned.some((g) => g.id === selectedVariant) ? selectedVariant : "all";
  const input = open ? (draft?.text ?? row.yaml).replace(/\r\n?/g, "\n") : "";
  const unnamed = row.name.trim() || t("sx.hx.m.unnamed");
  const goTo = (line: number, cursor = false) => {
    const instance = editor.current;
    if (!instance) return;
    const rowIndex = Math.max(0, Math.min(line - 1, instance.session.getLength() - 1));
    instance.session.unfold(rowIndex);
    instance.gotoLine(rowIndex + 1, 0);
    if (!cursor) {
      instance.selection.setSelectionRange({ start: { row: rowIndex, column: 0 }, end: { row: rowIndex, column: instance.session.getLine(rowIndex).length } });
    }
    instance.focus();
  };
  const choose = (line?: number) => {
    if (line !== undefined) goTo(line, true);
    const instance = editor.current;
    if (!instance || !draft) return;
    const { start, end } = instance.getSelectionRange();
    const doc = instance.session.getDocument();
    const result = sourceSelection(draft.text, doc.positionToIndex(start), doc.positionToIndex(end));
    setOperation(result.issue ?? null);
    if (!result.value) return;
    const selection = result.value;
    const ids = selection.block?.refs.flatMap((name) => {
      const id = draft.names.get(name);
      return id ? [id] : [];
    }) ?? [];
    const from = selection.block ? selection.block.open + 2 : selection.lines.findIndex((line) => line.start === selection.start) + 1;
    const to = selection.block ? Math.max(from, selection.block.close) : selection.lines.findIndex((line) => line.end === selection.end) + 1;
    setChooser({ draft, selection, ids, from, to });
  };
  const applyCondition = (ids: string[]) => {
    if (!chooser) return;
    onChange({ source: condition(chooser.draft, chooser.selection, ids, groups) });
    setChooser(null);
  };
  const exportSource = () => {
    if (source.issue) return setOperation(source.issue);
    const output = writeTransfer(row.name, source.yaml, row.groupIds, groups, draft);
    setOperation(output.issue ?? null);
    if (output.value !== undefined) setDownload(output.value);
  };
  return (
    <div className={cx("hx-r", status?.kind === "bad" && "bad", open && "on")}>
      <RowHead n={n} open={open} onToggle={() => { setOperation(null); onToggle(); }} meta={status?.label ?? ""} metaClass={cx("mt-st", status?.kind === "bad" && "bad", status?.kind === "part" && "part")}>
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
            {owner && (
              <>
                <p className="hx-note" id={helpId}>{t(assigned.length ? "sx.hx.m.conditionHelp" : "sx.hx.m.conditionEmpty")}</p>
                <div className="mt-up">
                  <Button className="sb-btn" disabled={groupsLoading || assigned.length === 0} onClick={() => choose()}>{t("sx.hx.m.onlyGroups")}</Button>
                  <Button className="sb-btn" disabled={groupsLoading} onClick={exportSource}>{t("sx.hx.m.export")}</Button>
                </div>
                {blocks.length > 0 && (
                  <ul className="mt-blocks" aria-label={t("sx.hx.m.conditionTitle")}>
                    {blocks.map((block) => (
                      <li key={block.open}>
                        <span id={`${helpId}-${block.open}`}>{t("sx.hx.m.conditionEntry", { from: block.open + 2, to: Math.max(block.open + 2, block.close), groups: block.refs.map((id) => groups.find((g) => g.id === id)?.name ?? id).join(", ") })}</span>
                        <Button className="sb-btn" aria-describedby={`${helpId}-${block.open}`} disabled={groupsLoading} onClick={() => choose(block.open + 1)}>{t("sx.hx.m.conditionEdit")}</Button>
                      </li>
                    ))}
                  </ul>
                )}
              </>
            )}
            {blocks.length > 0 && (
              <label className="mt-variant">
                <span>{t("sx.hx.m.variant")}</span>
                <select className="an-i" aria-label={t("sx.hx.m.variant")} aria-describedby={`${helpId}-variant`} value={variant} onChange={(e) => setSelectedVariant(e.target.value)}>
                  <option value="all">{t("sx.hx.m.variantAll")}</option>
                  <option value="">{t("sx.hx.m.variantCommon")}</option>
                  {assigned.map((group) => <option key={group.id} value={group.id}>{group.name}</option>)}
                </select>
                <small id={`${helpId}-variant`}>{t("sx.hx.m.variantHint")}</small>
              </label>
            )}
            <div
              className={cx("mt-editor", drag && "drag")}
              onDragOverCapture={(e) => {
                if (!owner || !e.dataTransfer.types.includes("Files")) return;
                e.preventDefault();
                e.stopPropagation();
                setDrag(true);
              }}
              onDragLeave={(e) => {
                if (!(e.relatedTarget instanceof Node) || !e.currentTarget.contains(e.relatedTarget)) setDrag(false);
              }}
              onDropCapture={(e) => {
                if (!owner || !e.dataTransfer.files.length) return;
                e.preventDefault();
                e.stopPropagation();
                setDrag(false);
                onFile(e.dataTransfer.files[0]);
              }}
            >
              <MihomoSourceEditor
                editorRef={editor}
                label={t("sx.hx.m.text")}
                description={owner ? helpId : undefined}
                text={input}
                readOnly={!owner}
                blocks={blocks}
                variant={variant}
                onChange={(value) => {
                  if (!draft) return;
                  setOperation(null);
                  onChange({ source: editSource(draft, value, groups) });
                }}
              />
            </div>
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
          {[...errors, ...(operation && operation !== source.issue ? [{ text: sourceIssueText(operation, t), line: operation.kind === "header" ? undefined : operation.line }] : [])].map((m, i) => (
            <Msg key={`${m.text}:${i}`} kind="bad">
              {m.text}
              {m.line && (
                <button type="button" className="mono mt-ln" aria-label={t("sx.hx.m.lineGo", { n: m.line })} onClick={() => goTo(m.line!, operation?.kind === "inactive" && operation.line === m.line)}>
                  {t("sx.hx.m.line", { n: m.line })}
                </button>
              )}
            </Msg>
          ))}
          {inactive.map((m, i) => (
            <Msg key={`${m.line}:${i}`} kind="part">
              {sourceIssueText(m, t)}
              <button type="button" className="mono mt-ln" aria-label={t("sx.hx.m.lineGo", { n: m.line })} onClick={() => goTo(m.line, true)}>{t("sx.hx.m.line", { n: m.line })}</button>
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
          {chooser && (
            <Modal
              open
              onOpenChange={(o) => { if (!o) setChooser(null); }}
              title={t("sx.hx.m.conditionTitle")}
              description={assigned.length ? t("sx.hx.m.conditionHint", { from: chooser.from, to: chooser.to }) : t("sx.hx.m.conditionEmpty")}
              closeLabel={t("common.close")}
              footer={
                <>
                  {chooser.selection.block && <Button onClick={() => applyCondition([])}>{t("sx.hx.m.conditionRemove")}</Button>}
                  <Button onClick={() => setChooser(null)}>{t("sx.hx.m.cancel")}</Button>
                  <Button variant="primary" disabled={selected.length === 0 || selected.length > groupLimit} onClick={() => applyCondition(selected)}>{t("sx.hx.m.conditionApply")}</Button>
                </>
              }
            >
              {assigned.length > 0 && (
                <div className="flex flex-col gap-2">
                  {assigned.map((g) => (
                    <label className="flex items-center gap-2" key={g.id}>
                      <input type="checkbox" checked={selected.includes(g.id)} onChange={(e) => setChooser({ ...chooser, ids: e.target.checked ? [...selected, g.id] : selected.filter((id) => id !== g.id) })} />
                      {g.name}
                    </label>
                  ))}
                </div>
              )}
              {chooser.selection.block && <p>{t("sx.hx.m.conditionCommon")}</p>}
            </Modal>
          )}
          {download !== null && (
            <Modal
              open
              onOpenChange={(o) => { if (!o) setDownload(null); }}
              title={t("sx.hx.m.export")}
              description={t("sx.hx.m.exportHint")}
              closeLabel={t("common.close")}
              footer={
                <>
                  <Button onClick={() => setDownload(null)}>{t("sx.hx.m.cancel")}</Button>
                  <Button variant="primary" onClick={() => {
                    downloadText("mihomo-template.yaml", download);
                    setDownload(null);
                  }}>{t("sx.hx.m.exportConfirm")}</Button>
                </>
              }
            />
          )}
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
  if (profile && r) yaml = <pre className="pp-pre mt-pre" tabIndex={0} aria-label={t("sx.hx.m.pvYaml")} data-noi18n><MihomoYamlPreview text={r.yaml} /></pre>;
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
