import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { Button } from "@/components/ui/button";
import { QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx, type Key, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { hxDemo } from "./demo";
import { CardHead, Chips, Foot, HxChip, Msg, Nothing, Opt, People, RowHead } from "./hx-parts";
import { formatKey, headerFormats, headerLimit, headerNameProblem, headerShadowed, headerSize, headerToSave, headerValueProblem, move, reservedEntry, ruleFor, type HeaderProblem, type ResponseHeader, type Settings } from "./model";
import { headerPreviewQuery, pickerUsersQuery, useSaveSettings } from "./queries";
import { samples } from "./rules";

type Data = { settings: Settings; reservedHeaderNames: string[] };
type Row = ResponseHeader & { k: number }; // k: the row's own key, so that a move keeps what is typed where it was
type Group = { id: string; name: string };
/** A message of a row: what the editor says, what the folded row says in its place (`head`, the same if left out), and whether it stops a save. */
type Message = { text: string; head?: string; bad: boolean };
type Carry = { open: number | null; saved: boolean };

const strip = ({ k: _k, ...h }: Row): ResponseHeader => h;
const blank = (k: number): Row => ({ k, name: "", value: "", base64: false, formats: [], onlyGroups: false, groupIds: [] });
const utf8 = new TextEncoder();

// the board writes the formats short on the chips and in full in the summaries
const shortKey: Partial<Record<SubFormat, Key>> = { [SubFormat.BASE64_URIS]: "sx.hx.h.fs.base64", [SubFormat.MIHOMO_YAML]: "sx.hx.h.fs.mihomo", [SubFormat.XRAY_JSON]: "sx.hx.h.fs.xray", [SubFormat.SINGBOX_JSON]: "sx.hx.h.fs.singbox" };
const fullKey: Partial<Record<SubFormat, Key>> = { [SubFormat.BASE64_URIS]: "sx.hx.h.fm.base64", [SubFormat.MIHOMO_YAML]: "sx.hx.h.fm.mihomo", [SubFormat.XRAY_JSON]: "sx.hx.h.fm.xray", [SubFormat.SINGBOX_JSON]: "sx.hx.h.fm.singbox" };
/** The apps the preview offers, by the User-Agent they send (the first is the one the board starts with: the app that reads the flclashx-* headers). */
const apps: readonly (readonly [string, string])[] = [["Happ", samples[0][1]], ["FlClashX", "FlClashX/0.4.2 Clash.Meta"], ["sing-box", samples[3][1]], ["v2rayNG", samples[4][1]], ["Safari", samples[5][1]]];
/** Names of the panel's list shown at once (the families the board names); the rest unfold. */
const firstNames = ["X-Hwid-*", "Profile-*", "Subscription-*", "Content-*"];

/** What a problem of the form says, in the board's words. */
function say(p: HeaderProblem, r: { name: string }, reserved: readonly string[], t: Tx): string {
  switch (p.key) {
    case "subs.hdr.err.noName":
      return t("sx.hx.h.eNoName");
    case "subs.hdr.err.name":
      return r.name.trim().length > headerLimit.name ? t("sx.hx.h.eNameLong", { max: headerLimit.name }) : t("sx.hx.h.eName");
    case "subs.hdr.err.reserved": {
      const e = reservedEntry(r.name.trim(), reserved) ?? r.name.trim();
      return t(e.endsWith("*") ? "sx.hx.h.eFamily" : "sx.hx.h.eReserved", { name: e });
    }
    case "subs.hdr.err.noValue":
      return t("sx.hx.h.eNoValue");
    case "subs.hdr.err.long":
      return t("sx.hx.h.eValLong", { max: headerLimit.value });
    case "subs.hdr.err.ctl":
      return t("sx.hx.h.eCtl");
    case "subs.hdr.err.ascii":
      return t("sx.hx.h.eAscii");
    default:
      return t(p.key, p.vars);
  }
}

/**
 * "Response headers": the owner's own headers of a successful subscription answer (the flclashx-* ones FlClashX reads, say), each
 * limited to the formats and the groups it goes to, in rows that unfold into their editor, and a preview of what one person gets with
 * one User-Agent. The panel checks every name and value on save; the form says the same early.
 */
export function HeadersTab({ data }: { data: Data }) {
  // keyed by what is saved: after a save (or a change from elsewhere) the form starts again from the server; the open row and the word "Saved" go along
  const [carry, setCarry] = useState<Carry | null>(null);
  return <HeadersForm key={JSON.stringify(data.settings.responseHeaders ?? [])} settings={data.settings} reserved={data.reservedHeaderNames} carry={carry} onCarry={setCarry} />;
}

function HeadersForm({ settings, reserved, carry, onCarry }: { settings: Settings; reserved: string[]; carry: Carry | null; onCarry: (c: Carry | null) => void }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const groups = useQuery(groupsQuery);
  const groupList: Group[] = (groups.data ?? []).map((g) => ({ id: g.id, name: g.name }));
  const saved = settings.responseHeaders ?? [];
  const start = hxDemo.headers ?? saved;
  const keys = useRef(start.length);
  const [rows, setRows] = useState<Row[]>(() => start.map((h, k) => ({ ...h, k })));
  const [open, setOpen] = useState<number | null>(() => (hxDemo.open !== undefined ? hxDemo.open : carry ? carry.open : null));
  const [done, setDone] = useState(() => hxDemo.saved ?? carry?.saved ?? false);
  const [fail, setFail] = useState<string | null>(() => hxDemo.fail ?? null);
  const [names, setNames] = useState(() => hxDemo.names ?? false);
  const known = groups.data ? groupList.map((g) => g.id) : null;
  // what came over from before the save is used once
  useEffect(() => onCarry(null), [onCarry]);

  const messages = rows.map((r, i) => {
    const out: Message[] = [];
    const name = headerNameProblem(r.name, reserved);
    const value = headerValueProblem(r.value, r.base64);
    if (name) out.push({ text: say(name, r, reserved, t), bad: true });
    if (value) out.push({ text: say(value, r, reserved, t), bad: true });
    if (r.onlyGroups && known !== null && !r.groupIds.some((id) => known.includes(id))) out.push({ text: t(r.groupIds.length > 0 ? "sx.hx.h.eGone" : "sx.hx.h.eNoGroup"), bad: true });
    if (out.length === 0 && headerShadowed(rows.map(strip), i, known ?? [])) out.push({ head: t("sx.hx.h.shadow"), text: t("sx.hx.h.shadowNote"), bad: false });
    return out;
  });
  const total = rows.reduce((n, r) => n + headerSize(r), 0);
  const over = total > headerLimit.total;
  const invalid = messages.some((m) => m.some((x) => x.bad));
  const bad = invalid || over;
  const dirty = JSON.stringify(rows.map(strip)) !== JSON.stringify(saved);
  const busy = save.isPending || !!hxDemo.busy;

  const edit = (f: (rs: Row[]) => Row[]) => {
    setRows(f);
    setDone(false);
    setFail(null);
  };
  const patch = (k: number, p: Partial<ResponseHeader>) => edit((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
  const add = () => {
    const k = keys.current++;
    edit((rs) => [...rs, blank(k)]);
    setOpen(k);
  };

  function discard() {
    setRows(saved.map((h, k) => ({ ...h, k })));
    setDone(false);
    setFail(null);
    setOpen(null);
  }

  function onSave() {
    const at = rows.findIndex((r) => r.k === open);
    save
      .mutateAsync((cur) => ({ ...cur, responseHeaders: rows.map((r) => headerToSave(strip(r), known)) }))
      .then(() => {
        onCarry({ open: at < 0 ? null : at, saved: true });
        setDone(true);
        toast(t("subs.saved"));
        halo("save");
      })
      .catch((e: unknown) => {
        setFail(errorText(e, t));
        toast.error(errorText(e, t));
      });
  }

  const hint = over ? t("sx.hx.h.ftOver", { max: headerLimit.total }) : bad ? t("sx.hx.h.ftBad") : fail ? fail : done ? t("subs.saved") : rows.length >= headerLimit.count ? t("sx.hx.h.ftMax", { max: headerLimit.count }) : t("sx.hx.h.ftHint");
  const shownNames = firstNames.filter((n) => reserved.includes(n));
  const head = (shownNames.length > 0 ? shownNames : reserved.slice(0, 4)).slice(0, 4);

  return (
    <div className="hx-tab">
      <p className="sb-intro">{t("sx.hx.h.intro")}</p>
      <div className="pf-g pp-grid sb-grid">
        <div className="col">
          <section className="card hx-card">
            <CardHead title={t("sx.hx.h.title")} count={t("sx.hx.h.sz", { n: rows.length, max: headerLimit.count, bytes: total, total: headerLimit.total })} bad={over} />
            {rows.length === 0 && <Nothing title={t("sx.hx.h.noneT")} text={t("sx.hx.h.noneS")} />}
            <div className="hx-l">
              {rows.map((r, i) => (
                <HeaderRow
                  key={r.k}
                  row={r}
                  n={i + 1}
                  last={i === rows.length - 1}
                  open={open === r.k}
                  messages={messages[i]!}
                  groups={groupList}
                  known={known}
                  t={t}
                  onToggle={() => setOpen(open === r.k ? null : r.k)}
                  onChange={(p) => patch(r.k, p)}
                  onMove={(to) => edit((rs) => move(rs, i, to))}
                  onRemove={() => {
                    edit((rs) => rs.filter((x) => x.k !== r.k));
                    setOpen(null);
                  }}
                />
              ))}
            </div>
            <Foot
              addLabel={t("sx.hx.h.add")}
              onAdd={add}
              addOff={rows.length >= headerLimit.count}
              hint={hint}
              bad={bad || !!fail}
              saveLabel={busy ? t("sx.hx.h.saving") : t("subs.save")}
              saveOff={bad || busy || known === null}
              onSave={onSave}
              {...(dirty && !busy && { onDiscard: discard, discardLabel: t("subs.discard") })}
            />
            <p className="hx-rs">
              {t("sx.hx.h.res")}{" "}
              {head.map((n) => (
                <span key={n} className="mono">
                  {n}
                </span>
              ))}{" "}
              <button type="button" className="hx-more" aria-expanded={names} onClick={() => setNames(!names)}>
                {names ? t("sx.hx.h.resLess") : t("sx.hx.h.resMore")}
              </button>
              {names && (
                <span className="hx-all">
                  {reserved.map((n) => (
                    <span key={n} className="mono">
                      {n}
                    </span>
                  ))}
                </span>
              )}
            </p>
          </section>
        </div>
        <div className="col">
          <Preview t={t} rows={rows} valid={!bad} known={known} groups={groupList} settings={settings} />
        </div>
      </div>
    </div>
  );
}

type RowProps = {
  row: Row;
  n: number;
  last: boolean;
  open: boolean;
  messages: Message[];
  groups: Group[];
  known: string[] | null;
  t: Tx;
  onToggle: () => void;
  onChange: (p: Partial<ResponseHeader>) => void;
  onMove: (to: number) => void;
  onRemove: () => void;
};

function HeaderRow({ row, n, last, open, messages, groups, known, t, onToggle, onChange, onMove, onRemove }: RowProps) {
  const failed = messages.find((m) => m.bad);
  const formats = row.formats.length === 0 ? t("sx.hx.h.allFormats") : headerFormats.filter((f) => row.formats.includes(f)).map((f) => t(fullKey[f]!)).join(", ");
  const names = groups.filter((g) => row.groupIds.includes(g.id)).map((g) => g.name);
  const where = !row.onlyGroups ? t("sx.hx.h.allGroups") : names.length > 0 ? names.join(", ") : t("sx.hx.h.noGroups");
  const meta = `${formats} · ${where}${row.base64 ? ` · ${t("sx.hx.h.b64")}` : ""}`;
  const toggleFormat = (f: SubFormat) => {
    const next = row.formats.includes(f) ? row.formats.filter((x) => x !== f) : [...row.formats, f];
    onChange({ formats: next.length === headerFormats.length ? [] : next });
  };
  const toggleGroup = (id: string) => {
    const next = row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id];
    onChange({ onlyGroups: true, groupIds: known ? next.filter((g) => known.includes(g)) : next });
  };
  const sent = 7 + 4 * Math.ceil(utf8.encode(row.value.trim()).length / 3);
  return (
    <div className={cx("hx-r", failed && "bad", open && "on")}>
      <RowHead n={n} open={open} onToggle={onToggle} meta={meta} label={`${n}. ${row.name.trim() || t("sx.hx.h.noName")}`}>
        <b className="mono">{row.name.trim() || t("sx.hx.h.noName")}</b>
        <span className="mono">{row.value || t("sx.hx.h.noValue")}</span>
        {!open && messages[0] && <em className={messages[0].bad ? "bad" : undefined}>{messages[0].head ?? messages[0].text}</em>}
      </RowHead>
      {open && (
        <div className="hx-ed">
          <div className="hx-nv">
            <label className="hx-f">
              <span>{t("sx.hx.h.name")}</span>
              <input className="an-i mono hx-n" type="text" placeholder={t("sx.hx.h.namePh")} autoComplete="off" autoCapitalize="off" spellCheck={false} value={row.name} onChange={(e) => onChange({ name: e.target.value })} />
            </label>
            <label className="hx-f">
              <span>{t("sx.hx.h.value")}</span>
              <input className="an-i mono hx-v" type="text" autoComplete="off" spellCheck={false} value={row.value} onChange={(e) => onChange({ value: e.target.value })} />
            </label>
          </div>
          <Opt label={t("sx.hx.h.formats")}>
            <Chips label={t("sx.hx.h.formats")}>
              <HxChip on={row.formats.length === 0} onClick={() => onChange({ formats: [] })}>
                {t("sx.hx.h.fAll")}
              </HxChip>
              {headerFormats.map((f) => (
                <HxChip key={f} on={row.formats.includes(f)} onClick={() => toggleFormat(f)}>
                  {t(shortKey[f]!)}
                </HxChip>
              ))}
            </Chips>
          </Opt>
          <Opt label={t("sx.hx.h.groups")}>
            <Chips label={t("sx.hx.h.groups")}>
              <HxChip on={!row.onlyGroups} onClick={() => onChange({ onlyGroups: false, groupIds: [] })}>
                {t("sx.hx.h.gAll")}
              </HxChip>
              {groups.map((g) => (
                <HxChip key={g.id} on={row.onlyGroups && row.groupIds.includes(g.id)} onClick={() => toggleGroup(g.id)}>
                  {g.name}
                </HxChip>
              ))}
            </Chips>
          </Opt>
          <Opt label={t("sx.hx.h.b64")}>
            <div className="hx-b64">
              <Switch className="sb-sw" aria-label={t("sx.hx.h.b64Aria")} checked={row.base64} onCheckedChange={(on) => onChange({ base64: on })} />
              <span>{row.base64 ? t("sx.hx.h.b64On", { n: sent }) : t("sx.hx.h.b64Off")}</span>
            </div>
          </Opt>
          {messages.map((m) => (
            <Msg key={m.text} kind={m.bad ? "bad" : "part"}>
              {m.text}
            </Msg>
          ))}
          <div className="hx-ea">
            <Button className="sb-btn" disabled={n === 1} onClick={() => onMove(n - 2)}>
              {t("sx.hx.h.up")}
            </Button>
            <Button className="sb-btn" disabled={last} onClick={() => onMove(n)}>
              {t("sx.hx.h.down")}
            </Button>
            <span />
            <Button className="sb-btn hx-del" onClick={onRemove}>
              {t("sx.hx.h.del")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/** What one person's answer to one User-Agent carries of the list as it stands in the form, asked of the panel. */
function Preview({ t, rows, valid, known, groups, settings }: { t: Tx; rows: Row[]; valid: boolean; known: string[] | null; groups: Group[]; settings: Settings }) {
  const users = useQuery(pickerUsersQuery);
  const list = users.data ?? [];
  const [picked, setPicked] = useState(() => hxDemo.who ?? "");
  const [ua, setUa] = useState<string>(() => hxDemo.ua ?? apps[1]![1]);
  const userId = list.some((u) => u.id === picked) ? picked : (list[0]?.id ?? "");
  const probe = useDebounced(ua.trim(), 300);
  const draft = useDebounced(JSON.stringify(rows.map((r) => headerToSave(strip(r), known))), 300);
  const q = useQuery({ ...headerPreviewQuery(userId, probe, JSON.parse(draft) as ResponseHeader[]), enabled: valid && userId !== "", placeholderData: keepPreviousData });
  const r = q.data;

  // what the panel says, else what the rules and the groups in the form say of it (so the line stands while the panel answers, and while the form has a mistake)
  const rule = ruleFor(settings.rules ?? [], ua);
  const format = r?.format ?? (rule >= 0 ? settings.rules![rule]!.format : SubFormat.BASE64_URIS);
  const person = list.find((u) => u.id === userId);
  const group = r?.groupName || groups.find((g) => g.id === person?.groupId)?.name || "";
  const formatName = t(fullKey[format] ?? formatKey[format] ?? "subs.fmt.base64");
  const why = r?.notServed === "page" ? "subs.hdr.pv.page" : r?.notServed === "decoy" ? "subs.hdr.pv.decoy" : r?.notServed === "no_access" ? "subs.hdr.pv.noAccess" : null;
  const count = !valid || !r ? null : why ? null : r.headers.length;
  const sum = `${t("sx.hx.h.pvSum", { format: formatName, group })}${count === null ? "" : ` · ${count > 0 ? t.n("sx.hx.h.pvN", count) : t("sx.hx.h.pvNone")}`}`;

  return (
    <section className="card hx-card hx-pv">
      <CardHead title={t("sx.hx.h.pvTitle")} />
      <div className="hx-pb">
        {users.isPending ? (
          <div className="hx-sk" />
        ) : users.isError ? (
          <QueryError error={users.error} onRetry={() => void users.refetch()} />
        ) : list.length === 0 ? (
          <p className="hx-note">{t("subs.hdr.pv.noUsers")}</p>
        ) : (
          <People label={t("sx.hx.h.pvWho")} users={list} value={userId} onPick={setPicked} />
        )}
        <label className="hx-f">
          <span>{t("sx.hx.h.pvUa")}</span>
          <input className="an-i mono hx-ua" type="text" autoComplete="off" spellCheck={false} value={ua} onChange={(e) => setUa(e.target.value)} />
        </label>
        <Chips label={t("sx.hx.h.pvUas")}>
          {apps.map(([name, value]) => (
            <HxChip key={name} on={ua === value} onClick={() => setUa(value)}>
              {name}
            </HxChip>
          ))}
        </Chips>
        {userId !== "" && <p className="hx-sum">{sum}</p>}
        {!valid && <Msg kind="part">{t("sx.hx.h.pvFix")}</Msg>}
        {valid && userId !== "" && !r && (q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : <div className="hx-sk" aria-busy="true" />)}
        {valid && r && why && <p className="hx-note">{t(why)}</p>}
        {valid && r && !why && r.headers.length > 0 && (
          <div className="hx-out" aria-live="polite">
            {r.headers.map((h, i) => (
              <div key={`${h.name}:${i}`}>
                <span className="mono">{h.name}</span>
                <b className="mono">{h.value}</b>
              </div>
            ))}
          </div>
        )}
        {valid && q.isError && r && <p className="hx-note">{errorText(q.error, t)}</p>}
        <p className="hx-note">{t("sx.hx.h.pvNote")}</p>
        <p className="hx-note">{t("subs.hdr.pv.note")}</p>
      </div>
    </section>
  );
}
