import { Code, ConnectError } from "@connectrpc/connect";
import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
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
import { starterTemplate, templateBytes, templateLimit, templateProblems, templateRefusal, type MihomoTemplate, type Settings } from "./model";
import { fileTooBig, kb, MtPreview, panelWarnings, problemText, TemplateRow, textWarnings, type Check, type Group, type Refusal, type Row, type Status } from "./mihomo-parts";
import { mihomoPreviewQuery, pickerUsersQuery, useSaveSettings } from "./queries";

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
  const groups = useQuery(groupsQuery);
  const users = useQuery(pickerUsersQuery);
  const groupList: Group[] = (groups.data ?? []).map((g) => ({ id: g.id, name: g.name }));
  const known = groups.data ? groupList.map((g) => g.id) : null;
  const auto = linkAutoOn(settings);
  const saved = settings.mihomoTemplates ?? [];
  const first = hxDemo.templates ?? saved;
  const keys = useRef(first.length);
  const [rows, setRows] = useState<Row[]>(() => first.map((x, k) => ({ ...x, k })));
  const [sel, setSel] = useState<number | null>(() => (hxDemo.open !== undefined ? hxDemo.open : start));
  const [refusal, setRefusal] = useState<(Refusal & { k?: number }) | null>(() => hxDemo.refusal ?? null);
  const [asking, setAsking] = useState<number | null>(() => hxDemo.ask ?? null);
  const [fileErr, setFileErr] = useState<number | null>(() => (hxDemo.fileErr ? (hxDemo.open ?? start) : null));
  const [picked, setPicked] = useState(() => hxDemo.who ?? "");
  const [done, setDone] = useState(() => hxDemo.saved ?? carried);
  useEffect(() => onShown(), [onShown]);

  const i = rows.findIndex((r) => r.k === sel);
  const cur = rows[i];
  const problems = rows.map((_, j) => templateProblems(rows, j));
  const total = rows.reduce((n, r) => n + templateBytes(r.yaml), 0);
  const tooBig = total > templateLimit.total;
  const dirty = JSON.stringify(rows.map(norm)) !== JSON.stringify(saved.map(norm));
  const busy = save.isPending || !!hxDemo.busy;

  // what the panel says of each text (every template, for the list; the open one, for the editor and the preview), as typed 0.4 s ago
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
  const bad = !valid;

  const edit = () => {
    setDone(false);
  };
  const patch = (k: number, p: Partial<MihomoTemplate>) => {
    setRows((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
    setRefusal((r) => (r?.k === k ? null : r));
    if (p.yaml !== undefined) setFileErr((e) => (e === k ? null : e));
    edit();
  };
  const add = (from?: Row) => {
    const k = keys.current++;
    const name = from ? freeName(t("sx.hx.m.copy", { name: from.name.trim() || t("sx.hx.m.unnamed") }), rows) : freeName(t("subs.mt.defaultName", { n: rows.length + 1 }), rows);
    const row = { k, name, yaml: from?.yaml ?? starterTemplate(auto), groupIds: [] };
    setRows((rs) => {
      const at = from ? rs.findIndex((r) => r.k === from.k) + 1 : rs.length;
      return [...rs.slice(0, at), row, ...rs.slice(at)];
    });
    setSel(k);
    setAsking(null);
    edit();
  };
  const remove = (k: number) => {
    const at = rows.findIndex((r) => r.k === k);
    const next = rows.filter((r) => r.k !== k);
    setRows(next);
    setSel(next.length === 0 ? null : next[Math.min(at, next.length - 1)]!.k);
    setAsking(null);
    edit();
  };
  const open = (k: number) => {
    setSel(sel === k ? null : k);
    setAsking(null);
  };
  async function take(k: number, f: File | undefined) {
    if (!f || !owner) return;
    if (fileTooBig(f)) return void setFileErr(k);
    setFileErr(null);
    patch(k, { yaml: await f.text() });
  }
  function discard() {
    setRows(saved.map((x, k) => ({ ...x, k })));
    setSel(start);
    setRefusal(null);
    setAsking(null);
    setFileErr(null);
    edit();
  }

  function onSave() {
    const out = rows.map((r) => ({ name: r.name.trim(), yaml: r.yaml, groupIds: known ? r.groupIds.filter((g) => known.includes(g)) : [...r.groupIds] }));
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

  const checkOf = (r: Row) => (r.k === sel ? (q as Check) : checks[drafts.findIndex((d) => d.k === r.k)]);
  const shownRefusal = (r: Row): (Refusal & { k?: number }) | null => (refusal?.k === r.k ? refusal : r.k === sel && q.isError && ConnectError.from(q.error).code === Code.InvalidArgument ? templateRefusal(ConnectError.from(q.error).rawMessage) : null);
  const warningsOf = (r: Row): string[] => [...panelWarnings(checkOf(r)?.data?.warnings ?? [], t), ...textWarnings(r, t)];
  const statusOf = (r: Row): Status | null => {
    const p = problems[rows.indexOf(r)]!;
    const c = checkOf(r);
    if (p.name || p.text || shownRefusal(r) || invalid(c)) return { kind: "bad", label: t("sx.hx.m.stBad") };
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
    const f = shownRefusal(r);
    if (f) out.push({ text: `${t("sx.hx.m.refused")}: ${f.text}`, ...(f.line && { line: f.line }) });
    if (fileErr === r.k) out.push({ text: t("sx.hx.m.eFile") });
    return out;
  };

  const wholeRefusal = refusal && refusal.k === undefined ? refusal : null;
  const hint = tooBig ? t("sx.hx.m.ftOver", { max: templateLimit.total >> 10 }) : bad ? t("sx.hx.m.ftBad") : done ? t("subs.saved") : dirty ? t("sx.hx.m.ftDirty") : rows.length >= templateLimit.count ? t("sx.hx.m.ftMax", { max: templateLimit.count }) : t("sx.hx.m.ftHint");
  const previewValid = !!cur && !invalid(q as Check) && !problems[i]!.text && !shownRefusal(cur);

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
                  key={r.k}
                  t={t}
                  row={r}
                  n={n + 1}
                  open={r.k === sel}
                  owner={owner}
                  groups={groupList}
                  groupsLoading={groups.isPending}
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
          <MtPreview t={t} row={cur} rows={rows} groups={groupList} users={users} userId={userId} onPick={setPicked} q={q as Check} valid={previewValid} />
        </div>
      </div>

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
