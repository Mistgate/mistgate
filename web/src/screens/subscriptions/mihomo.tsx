import { Code, ConnectError } from "@connectrpc/connect";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { errorText } from "@/lib/errors";
import { useIsOwner } from "@/lib/updates";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { hxDemo } from "./demo";
import { useLeaveGuard } from "./guard";
import { CardHead, Foot, Msg, Nothing } from "./hx-parts";
import { linkAutoOn } from "./link-auto";
import { starterTemplate, templateBytes, templateLimit, templateNameKey, templateProblems, templateRefusal, trimTemplateName, type MihomoTemplate, type Settings } from "./model";
import { fileTooBig, kb, MtPreview, panelWarnings, problemText, sourceIssueText, TemplateRow, textWarnings, type Check, type Refusal, type Row, type Status } from "./mihomo-parts";
import { canonicalSource, displaySource, mapTransfer, parseSource, readTransfer, variants, type Transfer } from "./mihomo-source";
import { mihomoPreviewQuery, pickerUsersQuery, useSaveSettings } from "./queries";

const norm = (r: MihomoTemplate): MihomoTemplate => ({ name: r.name, yaml: r.yaml, groupIds: r.groupIds });

/** What a failed call says: the panel's own words (English, the line of the text in them) for a refusal, else the usual sentence. */
function refusalOf(e: unknown, t: Tx): Refusal & { index?: number } {
  const c = ConnectError.from(e);
  return c.code === Code.InvalidArgument ? templateRefusal(c.rawMessage) : { text: errorText(e, t) };
}

/** A name not used yet: `base`, else `base 2`, `base 3` ... (kept within the panel's length). */
function freeName(base: string, rows: readonly Row[]): string {
  const used = new Set(rows.map((r) => templateNameKey(r.name)));
  const cut = [...base].slice(0, templateLimit.name - 4).join("");
  for (let n = 1; ; n++) {
    const name = n === 1 ? base : `${cut} ${n}`;
    const candidate = [...name].slice(0, templateLimit.name).join("");
    if (!used.has(templateNameKey(candidate))) return candidate;
  }
}

/**
 * "Mihomo templates": the owner's own Mihomo YAML profiles, each for the groups it names, in rows that unfold into their editor, and
 * the preview of what one person gets from the text as it stands. The panel checks the text on save (and in the preview, before it);
 * the form says the early part of that and shows what the panel says, with its line.
 */
export function MihomoTab({ data, initial }: { data: { settings: Settings }; initial?: number | null }) {
  // the open row survives a save (the form starts again from what is saved, which keeps the rows in the order they were sent), and so does the word "Saved", once
  const [keep, setKeep] = useState<number | null | undefined>(initial);
  const [done, setDone] = useState(false);
  const shown = useCallback(() => setDone(false), []);
  const saved = data.settings.mihomoTemplates ?? [];
  const start = keep === undefined ? (saved.length > 0 ? 0 : null) : keep === null || saved.length === 0 ? null : Math.min(keep, saved.length - 1);
  return (
    <MihomoForm
      key={JSON.stringify(saved)}
      settings={data.settings}
      start={start}
      done={done}
      onKeep={(i) => {
        setKeep(i);
        setDone(true);
      }}
      onShown={shown}
    />
  );
}

type FormProps = { settings: Settings; start: number | null; done: boolean; onKeep: (i: number | null) => void; onShown: () => void };

function MihomoForm({ settings, start, done: carried, onKeep, onShown }: FormProps) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const owner = useIsOwner();
  const queryClient = useQueryClient();
  const groups = useQuery(groupsQuery);
  const users = useQuery(pickerUsersQuery);
  const groupList = groups.data;
  const known = groupList ? new Set(groupList.map((g) => g.id)) : null;
  const auto = linkAutoOn(settings);
  const saved = settings.mihomoTemplates ?? [];
  const first = hxDemo.templates ?? saved;
  const keys = useRef(first.length);
  const [rows, setRows] = useState<Row[]>(() => first.map((x, k) => ({ ...x, k })));
  const [sel, setSel] = useState<number | null>(() => (hxDemo.open !== undefined ? hxDemo.open : start));
  const [refusal, setRefusal] = useState<(Refusal & { k?: number }) | null>(() => hxDemo.refusal ?? null);
  const [asking, setAsking] = useState<number | null>(() => hxDemo.ask ?? null);
  const [fileErr, setFileErr] = useState<{ k: number; text: string } | null>(() => hxDemo.fileErr && (hxDemo.open ?? start) != null ? { k: (hxDemo.open ?? start)!, text: t("sx.hx.m.eFile") } : null);
  const [transfer, setTransfer] = useState<{ k: number; file: Transfer; mapping: Map<string, string> } | null>(null);
  const fileRequest = useRef(0);
  const [picked, setPicked] = useState(() => hxDemo.who ?? "");
  const [done, setDone] = useState(() => hxDemo.saved ?? carried);
  useEffect(() => onShown(), [onShown]);

  const i = rows.findIndex((r) => r.k === sel);
  const cur = rows[i];
  const sources = useMemo(() => rows.map((r) => {
    const assigned = groupList ? r.groupIds.filter((id) => groupList.some((g) => g.id === id)) : r.groupIds;
    const mapped = r.source ? canonicalSource(r.source) : { value: r.yaml, issue: undefined };
    const yaml = mapped.value ?? r.source!.text;
    const parsed = parseSource(yaml, true);
    const issue = mapped.issue ?? parsed.issue;
    const source = !issue ? parsed.value : undefined;
    const common = source ? variants(yaml, source, []).next().value ?? yaml : yaml;
    return { yaml, issue, source, common, groupIds: assigned, k: r.k, name: r.name };
  }), [rows, groupList]);
  const problems = sources.map((r, j) => {
    const p = templateProblems(sources, j);
    return !p.text && r.common.trim() === "" ? { ...p, text: { key: "subs.mt.err.empty" as const } } : p;
  });
  const total = sources.reduce((n, r) => n + templateBytes(r.yaml), 0);
  const tooBig = total > templateLimit.total;
  const dirty = rows.some((r, j) => r.source && sources[j]!.issue) ||
    JSON.stringify(sources.map((r, j) => norm({ ...r, groupIds: rows[j]!.groupIds }))) !== JSON.stringify(saved.map(norm));
  const busy = save.isPending || !!hxDemo.busy;

  // what the panel says of each text (every template, for the list; the open one, for the editor and the preview), as typed 0.4 s ago
  const list = users.data ?? [];
  const listPerson = list[0];
  const person = list.find((u) => u.id === picked) ?? list.find((u) => cur?.groupIds.includes(u.groupId)) ?? listPerson;
  const userId = person?.id ?? "";
  const drafts = useDebounced(sources, 400);
  const checks: Check[] = useQueries({ queries: drafts.map((r) => ({ ...mihomoPreviewQuery(listPerson?.id ?? "", listPerson?.groupId ?? "", r.yaml, r.groupIds), enabled: !!listPerson && !r.issue && r.common.trim() !== "" })) }) as Check[];
  const draftIsCurrent = (draft: (typeof drafts)[number] | undefined) => {
    if (!draft) return false;
    const current = sources.find((r) => r.k === draft.k);
    return !!current && !current.issue && current.yaml === draft.yaml && current.groupIds.length === draft.groupIds.length &&
      current.groupIds.every((id, j) => id === draft.groupIds[j]);
  };
  const dcur = drafts.find((r) => r.k === sel);
  const q = useQuery({ ...mihomoPreviewQuery(userId, person?.groupId ?? "", dcur?.yaml ?? "", dcur?.groupIds ?? []), enabled: !!dcur && !dcur.issue && userId !== "" && dcur.common.trim() !== "" });
  const invalid = (c?: Check) => !!c?.isError && ConnectError.from(c.error).code === Code.InvalidArgument;
  const fresh = draftIsCurrent(dcur);
  const anyInvalid = checks.some(invalid) || invalid(q as Check);
  const valid = problems.every((p) => !p.name && !p.text) && !sources.some((r) => r.issue) && !tooBig && !anyInvalid;
  const bad = !valid;

  const edit = () => {
    setDone(false);
  };
  const patch = (k: number, p: Partial<Omit<Row, "k">>) => {
    fileRequest.current++;
    setRows((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
    setRefusal((r) => (r?.k === k ? null : r));
    if ("yaml" in p || "source" in p) setFileErr((e) => (e?.k === k ? null : e));
    edit();
  };
  const add = (from?: Row) => {
    fileRequest.current++;
    const k = keys.current++;
    const name = from ? freeName(t("sx.hx.m.copy", { name: from.name.trim() || t("sx.hx.m.unnamed") }), rows) : freeName(t("subs.mt.defaultName", { n: rows.length + 1 }), rows);
    const row = { k, name, yaml: from?.yaml ?? starterTemplate(auto), source: from?.source, groupIds: [] };
    setRows((rs) => {
      const at = from ? rs.findIndex((r) => r.k === from.k) + 1 : rs.length;
      return [...rs.slice(0, at), row, ...rs.slice(at)];
    });
    setSel(k);
    setAsking(null);
    edit();
  };
  const remove = (k: number) => {
    fileRequest.current++;
    const at = rows.findIndex((r) => r.k === k);
    const next = rows.filter((r) => r.k !== k);
    setRows(next);
    setSel(next.length === 0 ? null : next[Math.min(at, next.length - 1)]!.k);
    setAsking(null);
    edit();
  };
  const open = (k: number) => {
    fileRequest.current++;
    setSel(sel === k ? null : k);
    setAsking(null);
  };
  const replace = (k: number, replacement: Pick<Row, "yaml"> & Partial<Pick<Row, "name" | "groupIds">>) => {
    patch(k, { ...replacement, source: undefined, editorVersion: fileRequest.current + 1 });
  };
  async function take(k: number, f: File | undefined) {
    if (!f || !owner) return;
    const request = ++fileRequest.current;
    const rejected = (text: string, line?: number) => setFileErr({ k, text: line ? `${t("sx.hx.m.line", { n: line })}: ${text}` : text });
    if (fileTooBig(f)) return rejected(t("sx.hx.m.eFile"));
    let yaml: string;
    try {
      yaml = await f.text();
    } catch {
      if (request === fileRequest.current) rejected(t("sx.hx.m.fileRead"));
      return;
    }
    if (request !== fileRequest.current) return;
    const file = readTransfer(yaml);
    if (file.issue) return rejected(sourceIssueText(file.issue, t), file.issue.line);
    const ordinary = file.value ? undefined : parseSource(yaml);
    if (ordinary?.issue) return rejected(sourceIssueText(ordinary.issue, t), ordinary.issue.line);
    if (ordinary?.value?.blocks.length === 0) {
      replace(k, { yaml });
      return;
    }
    let localGroups = queryClient.getQueryData(groupsQuery.queryKey);
    if ((file.value?.groups.length ?? 1) > 0 &&
        (!localGroups || queryClient.getQueryState(groupsQuery.queryKey)?.fetchStatus === "fetching")) {
      try {
        localGroups = await queryClient.fetchQuery({ ...groupsQuery, staleTime: 0 });
      } catch (e) {
        if (request === fileRequest.current) rejected(errorText(e, t));
        return;
      }
    }
    if (request !== fileRequest.current) return;
    const local = localGroups ?? [];
    if (!file.value) {
      const assignedIds = rows.find((r) => r.k === k)?.groupIds ?? [];
      const assigned = local.filter((g) => assignedIds.includes(g.id));
      const mapping = new Map(assigned.filter((g) => !assigned.some((other) => other !== g && other.name === g.name)).map((g) => [g.name, g.id]));
      const mapped = mapTransfer(yaml, mapping);
      if (mapped.issue) return rejected(sourceIssueText(mapped.issue, t), mapped.issue.line);
      replace(k, { yaml: mapped.value });
      return;
    }
    const available = local.filter((g) => !rows.some((r) => r.k !== k && r.groupIds.includes(g.id)));
    const mapping = new Map<string, string>();
    for (const g of file.value.groups) {
      const same = local.filter((group) => group.name === g);
      if (same.length === 1 && available.includes(same[0]!)) mapping.set(g, same[0]!.id);
    }
    setFileErr(null);
    setTransfer({ k, file: file.value, mapping });
  }
  function discard() {
    fileRequest.current++;
    setTransfer(null);
    const restored = saved.map((x) => ({ ...x, k: keys.current++ }));
    setRows(restored);
    setSel(start === null ? null : restored[start]?.k ?? null);
    setRefusal(null);
    setAsking(null);
    setFileErr(null);
    edit();
  }

  function onSave() {
    if (!valid || !known) return;
    const out = sources.map((r) => ({ name: trimTemplateName(r.name), yaml: r.yaml, groupIds: r.groupIds }));
    onKeep(i < 0 ? null : i); // before the save: the form starts again when the new settings arrive, which is before the save answers
    save
      .mutateAsync((c) => ({ ...c, mihomoTemplates: out }))
      .then(() => {
        setDone(true);
        toast(t("subs.saved"));
      })
      .catch((e: unknown) => {
        onShown();
        const f = refusalOf(e, t);
        const row = f.index !== undefined ? rows[f.index] : undefined;
        setRefusal({ text: f.text, ...(f.line && { line: f.line }), ...(row && { k: row.k }) });
        if (row) setSel(row.k);
        toast.error(t("sx.hx.m.refusedAll"));
      });
  }

  // leaving the tab (the strip, a link in the tab) while the form holds changes asks first: the screen does that, told here. Leaving the page, or closing it, the router asks.
  useLeaveGuard(dirty && owner);
  const held = useRef(false);
  const leaving = useRef(false);
  useEffect(() => {
    held.current = dirty && owner;
  }, [dirty, owner]);
  const blocker = useBlocker({
    shouldBlockFn: ({ current, next }) => held.current && !leaving.current && next.pathname !== current.pathname,
    enableBeforeUnload: () => held.current,
    withResolver: true,
  });
  // development only: a scene asks to leave the tab, to show the question
  useEffect(() => {
    if (!hxDemo.leave) return;
    const id = setTimeout(() => document.querySelector<HTMLElement>('.sb-tabs [role="tab"]:not([aria-selected="true"])')?.click(), 150);
    return () => clearTimeout(id);
  }, []);

  const checkOf = (r: Row) => {
    const at = drafts.findIndex((d) => d.k === r.k);
    return draftIsCurrent(drafts[at]) ? (r.k === sel ? (q as Check) : checks[at]) : undefined;
  };
  const shownRefusal = (r: Row): (Refusal & { k?: number }) | null => (refusal?.k === r.k ? refusal : r.k === sel && fresh && invalid(q as Check) ? templateRefusal(ConnectError.from(q.error).rawMessage) : null);
  const warningsOf = (r: Row): string[] => {
    const s = sources[rows.indexOf(r)]!;
    return [...new Set([...panelWarnings(checkOf(r)?.data?.warnings ?? [], t), ...textWarnings(s, t)])];
  };
  const statusOf = (r: Row): Status | null => {
    const p = problems[rows.indexOf(r)]!;
    const c = checkOf(r);
    if (p.name || p.text || sources[rows.indexOf(r)]!.issue || shownRefusal(r) || invalid(c)) return { kind: "bad", label: t("sx.hx.m.stBad") };
    if (r.groupIds.length === 0) return { kind: "part", label: t("sx.hx.m.stFree") };
    if (!c?.data && c?.isFetching) return { kind: "wait", label: t("sx.hx.m.stWait") };
    const w = warningsOf(r).length;
    return w > 0 ? { kind: "part", label: t("sx.hx.m.stWarn", { n: w }) } : { kind: "ok", label: t("sx.hx.m.stOk") };
  };
  const errorsOf = (r: Row) => {
    const j = rows.indexOf(r);
    const p = problems[j]!;
    const out: { text: string; line?: number }[] = [];
    if (p.name) out.push({ text: problemText(p.name, t) });
    if (p.text) out.push({ text: problemText(p.text, t) });
    const issue = sources[j]!.issue;
    if (issue) out.push({ text: sourceIssueText(issue, t), line: issue.line });
    const f = shownRefusal(r);
    if (f) out.push({ text: `${t("sx.hx.m.refused")}: ${f.text}`, ...(f.line && { line: f.line }) });
    if (fileErr?.k === r.k) out.push({ text: fileErr.text });
    return out;
  };

  const wholeRefusal = refusal && refusal.k === undefined ? refusal : null;
  const hint = tooBig ? t("sx.hx.m.ftOver", { max: templateLimit.total >> 10 }) : bad ? t("sx.hx.m.ftBad") : done ? t("subs.saved") : dirty ? t("sx.hx.m.ftDirty") : rows.length >= templateLimit.count ? t("sx.hx.m.ftMax", { max: templateLimit.count }) : t("sx.hx.m.ftHint");
  const previewValid = !!cur && !(fresh && invalid(q as Check)) && !problems[i]!.text && !sources[i]!.issue && !shownRefusal(cur);
  const available = transfer ? (groupList ?? []).filter((g) => !rows.some((r) => r.k !== transfer.k && r.groupIds.includes(g.id))) : [];
  const transferIds = transfer?.file.groups.map((name) => transfer.mapping.get(name) ?? "") ?? [];
  const complete = !!transfer && !!known && transferIds.every((id) => available.some((g) => g.id === id)) && new Set(transferIds).size === transferIds.length;
  const target = transfer ? rows.findIndex((r) => r.k === transfer.k) : -1;
  const imported = complete ? mapTransfer(transfer.file.text, transfer.mapping).value : undefined;
  const importedRow = target < 0 || imported === undefined ? undefined : { name: transfer!.file.name, yaml: imported, groupIds: transferIds };
  const importProblems = importedRow ? templateProblems(sources.map((r, j) => j === target ? importedRow : r), target) : null;
  const importSource = imported === undefined ? undefined : parseSource(imported, true).value;
  const importEmpty = importSource && !(variants(imported!, importSource, []).next().value ?? "").trim();
  const importIssue = importProblems?.name ?? importProblems?.text ?? (importEmpty ? { key: "subs.mt.err.empty" as const } : null);
  const importTooBig = imported !== undefined && target >= 0 && total - templateBytes(sources[target]!.yaml) + templateBytes(imported) > templateLimit.total;
  const importBadName = transfer && /[\p{Cc}\p{Cs}]/u.test(trimTemplateName(transfer.file.name));

  return (
    <div className="hx-tab mt-tab">
      <p className="sb-intro">{t("sx.hx.m.intro")}</p>
      <div className="pf-g pp-grid sb-grid">
        <div className="col">
          <section className="card hx-card">
            <CardHead title={t("sx.hx.m.title")} count={t("sx.hx.m.sz", { n: rows.length, max: templateLimit.count, kib: kb(total, t), total: templateLimit.total >> 10 })} bad={tooBig} />
            {rows.length === 0 && <Nothing title={t("sx.hx.m.noneT")} text={t("sx.hx.m.noneS")} />}
            <div className="hx-l">
              {rows.map((r, n) => (
                <TemplateRow
                  key={`${r.k}:${r.editorVersion ?? 0}`}
                  t={t}
                  row={r}
                  n={n + 1}
                  open={r.k === sel}
                  owner={owner}
                  groups={groupList ?? []}
                  groupsLoading={groups.isPending}
                  draft={r.source ?? (r.k === sel ? displaySource(r.yaml, groupList ?? []) : undefined)}
                  source={sources[n]!}
                  others={rows.filter((x) => x.k !== r.k)}
                  status={statusOf(r)}
                  errors={errorsOf(r)}
                  warnings={warningsOf(r)}
                  asking={asking === r.k}
                  atLimit={rows.length >= templateLimit.count}
                  onToggle={() => open(r.k)}
                  onChange={(p) => patch(r.k, p)}
                  onDuplicate={() => add(r)}
                  onAsk={(on) => setAsking(on ? r.k : null)}
                  onDelete={() => remove(r.k)}
                  onFile={(f) => void take(r.k, f)}
                />
              ))}
            </div>
            {wholeRefusal && (
              <div className="hx-whole">
                <Msg kind="bad">{`${t("sx.hx.m.refusedAll")}: ${wholeRefusal.text}`}</Msg>
              </div>
            )}
            {owner && (
              <Foot
                addLabel={t("sx.hx.m.add")}
                onAdd={() => add()}
                addOff={rows.length >= templateLimit.count}
                hint={hint}
                bad={bad}
                saveLabel={busy ? t("sx.hx.h.saving") : t("subs.save")}
                saveOff={bad || busy || !dirty || known === null}
                onSave={onSave}
                {...(dirty && !busy && { onDiscard: discard, discardLabel: t("subs.discard") })}
              />
            )}
            {!owner && <p className="hx-rs">{t("sx.hx.m.ro")}</p>}
            <p className="hx-rs">{t("sx.hx.m.rewrite")}</p>
          </section>
        </div>
        <div className="col">
          <MtPreview t={t} row={cur} rows={rows} groups={groupList ?? []} users={users} userId={userId} onPick={setPicked} q={q as Check} valid={previewValid} fresh={fresh} />
        </div>
      </div>

      {transfer && (
        <Modal
          open
          onOpenChange={(o) => { if (!o) setTransfer(null); }}
          title={t("sx.hx.m.importTitle", { name: transfer.file.name })}
          description={t("sx.hx.m.importHint")}
          closeLabel={t("common.close")}
          footer={
            <>
              <Button onClick={() => setTransfer(null)}>{t("sx.hx.m.cancel")}</Button>
              <Button variant="primary" disabled={!importedRow || !!importIssue || importTooBig || !!importBadName} onClick={() => {
                if (!importedRow) return;
                replace(transfer.k, importedRow);
                setTransfer(null);
              }}>{t("sx.hx.m.importApply")}</Button>
            </>
          }
        >
          {transfer.file.groups.map((name) => (
            <label className="flex flex-col gap-1" key={name}>
              <span>{t("sx.hx.m.importGroup", { name })}</span>
              <select className="an-i" value={transfer.mapping.get(name) ?? ""} onChange={(e) => {
                setTransfer({ ...transfer, mapping: new Map(transfer.mapping).set(name, e.target.value) });
              }}>
                <option value="">{t("sx.hx.m.importChoose")}</option>
                {(groupList ?? []).map((g) => (
                  <option key={g.id} value={g.id} disabled={!available.includes(g) || transfer.file.groups.some((other) => other !== name && transfer.mapping.get(other) === g.id)}>{g.name}</option>
                ))}
              </select>
            </label>
          ))}
          {importIssue && <Msg kind="bad">{problemText(importIssue, t)}</Msg>}
          {importBadName && <Msg kind="bad">{t("sx.hx.m.importBadName")}</Msg>}
          {importTooBig && <Msg kind="bad">{t("sx.hx.m.ftOver", { max: templateLimit.total >> 10 })}</Msg>}
        </Modal>
      )}

      <Modal
        open={blocker.status === "blocked"}
        onOpenChange={(o) => {
          if (!o) blocker.reset?.();
        }}
        title={t("sx.leave.title")}
        description={t("sx.leave.text")}
        closeLabel={t("common.close")}
        footer={
          <>
            <Button
              size="lg"
              onClick={() => {
                leaving.current = true;
                blocker.proceed?.();
              }}
            >
              {t("sx.leave.go")}
            </Button>
            <Button size="lg" variant="primary" onClick={() => blocker.reset?.()}>
              {t("sx.leave.stay")}
            </Button>
          </>
        }
      />
    </div>
  );
}
