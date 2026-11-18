import { Code, ConnectError } from "@connectrpc/connect";
import { useQueries, useQuery, type UseQueryResult } from "@tanstack/react-query";
import { useBlocker } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { useToast } from "@/components/ui/toast";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { useDebounced } from "@/screens/users/ui";
import { hxDemo } from "./demo";
import { useLeaveGuard } from "./guard";
import { CardHead, Chips, Foot, HxChip, Msg, Nothing, Opt, RowHead } from "./hx-parts";
import { geoHosts, linkMode, routingLimit, routingRefusal, type RoutingInfo, type RoutingProfile, type Settings } from "./model";
import { routingPreviewQuery, useSaveSettings } from "./queries";

type Row = { k: number; text: string; onlyAdd: boolean; onlyGroups: boolean; groupIds: string[]; text0: string; info0?: RoutingInfo | undefined };
type Group = { id: string; name: string };
type Check = UseQueryResult<RoutingProfile>;

const fromSaved = (p: RoutingProfile, k: number): Row => ({ k, text: p.profile, text0: p.profile, onlyAdd: p.onlyAdd, onlyGroups: p.onlyGroups, groupIds: [...p.groupIds], info0: p.info });
const toSave = (r: Row, known: readonly string[] | null): Pick<RoutingProfile, "profile" | "onlyAdd" | "onlyGroups" | "groupIds"> => ({
  profile: r.text.trim(),
  onlyAdd: r.onlyAdd,
  onlyGroups: r.onlyGroups,
  groupIds: !r.onlyGroups ? [] : known ? r.groupIds.filter((g) => known.includes(g)) : [...r.groupIds],
});
const same = (a: ReturnType<typeof toSave>, b: ReturnType<typeof toSave>) => JSON.stringify(a) === JSON.stringify(b);
const refused = (c: Check | undefined) => !!c?.isError && ConnectError.from(c.error).code === Code.InvalidArgument;
const stuck = (c: Check | undefined) => !!c?.isError && !refused(c);
const why = (c: Check) => routingRefusal(ConnectError.from(c.error).rawMessage).text;

/**
 * "Routing": the owner's own routing profiles for Happ and INCY, each for the groups it names, in rows that unfold into their editor,
 * and what the open profile holds in words. The owner pastes what 3x-ui or the app gave (a link, base64 or JSON); the panel reads it
 * (the same reading as the save) and the form shows what it found, or why not. The panel puts the DNS of each person's preset into
 * the profile; the rules stay as the owner wrote them.
 */
export function RoutingTab({ data }: { data: { settings: Settings } }) {
  // keyed by what is saved: after a save (or a change from elsewhere) the form starts again from the server; the open row and the word "Saved" go along
  const [carry, setCarry] = useState<{ open: number | null; saved: boolean } | null>(null);
  const saved = (data.settings.routingProfiles ?? []).map((p) => ({ ...p, info: undefined }));
  return <RoutingForm key={JSON.stringify(saved)} settings={data.settings} carry={carry} onCarry={setCarry} />;
}

function RoutingForm({ settings, carry, onCarry }: { settings: Settings; carry: { open: number | null; saved: boolean } | null; onCarry: (c: { open: number | null; saved: boolean } | null) => void }) {
  const t = useTx();
  const toast = useToast();
  const save = useSaveSettings();
  const groups = useQuery(groupsQuery);
  const groupList: Group[] = (groups.data ?? []).map((g) => ({ id: g.id, name: g.name }));
  const known = groups.data ? groupList.map((g) => g.id) : null;
  const saved = settings.routingProfiles ?? [];
  const start = hxDemo.routing ?? saved.map(fromSaved);
  const keys = useRef(start.length);
  const [rows, setRows] = useState<Row[]>(() => start.map((r, k) => ({ ...r, k })));
  const [open, setOpen] = useState<number | null>(() => (hxDemo.open !== undefined ? hxDemo.open : carry ? carry.open : saved.length > 0 ? 0 : null));
  const [done, setDone] = useState(() => hxDemo.saved ?? carry?.saved ?? false);
  const [fail, setFail] = useState<string | null>(() => hxDemo.fail ?? null);
  const [refusal, setRefusal] = useState<{ text: string; k?: number } | null>(() => (hxDemo.refusal ? { text: hxDemo.refusal.text, ...(hxDemo.refusal.k !== undefined && { k: hxDemo.refusal.k }) } : null));
  useEffect(() => onCarry(null), [onCarry]);

  // what the panel says of each pasted text, as typed 0.4 s ago; a saved row that was not touched needs no question
  const drafts = useDebounced(rows, 400);
  const checks: Check[] = useQueries({ queries: drafts.map((r) => ({ ...routingPreviewQuery(r.text.trim()), enabled: r.text.trim() !== "" && r.text.trim() !== r.text0 })) }) as Check[];
  const checkOf = (r: Row): Check | undefined => checks[drafts.findIndex((d) => d.k === r.k)];
  const infoOf = (r: Row): RoutingInfo | undefined => (r.text.trim() === r.text0 ? r.info0 : drafts.find((d) => d.k === r.k)?.text.trim() === r.text.trim() ? checkOf(r)?.data?.info : undefined);
  const dropped = (r: Row) => infoOf(r)?.droppedFields ?? [];

  const names = rows.map((r) => infoOf(r)?.name ?? "");
  const messages = rows.map((r, i) => {
    const out: { text: string; bad: boolean }[] = [];
    const c = checkOf(r);
    const empty = r.text.trim() === "";
    if (empty) out.push({ text: t("sx.rt.empty"), bad: true });
    else if (r.text.length > routingLimit.input) out.push({ text: t("sx.rt.eBig", { max: routingLimit.input >> 10 }), bad: true });
    else if (refused(c) && r.text.trim() !== r.text0) out.push({ text: t("sx.rt.refused", { text: why(c!) }), bad: true });
    else if (stuck(c) && r.text.trim() !== r.text0) out.push({ text: t("sx.rt.noAnswer", { text: errorText(c!.error, t) }), bad: true });
    const mine = names[i]!.toLowerCase();
    if (mine !== "" && names.some((n, j) => j !== i && n.toLowerCase() === mine)) out.push({ text: t("sx.rt.eName", { name: names[i]! }), bad: true });
    if (r.onlyGroups) {
      if (known !== null && !r.groupIds.some((id) => known.includes(id))) out.push({ text: t(r.groupIds.length > 0 ? "sx.rt.eGone" : "sx.rt.eNoGroup"), bad: true });
      const other = groupList.find((g) => r.groupIds.includes(g.id) && rows.some((o, j) => j !== i && o.onlyGroups && o.groupIds.includes(g.id)));
      if (other) out.push({ text: t("sx.rt.eGroup", { group: other.name }), bad: true });
    } else if (rows.some((o, j) => j !== i && !o.onlyGroups)) out.push({ text: t("sx.rt.eAll"), bad: true });
    return out;
  });
  const waiting = rows.some((r) => r.text.trim() !== "" && r.text.trim() !== r.text0 && !infoOf(r) && !refused(checkOf(r)) && !stuck(checkOf(r)));
  const bad = messages.some((m) => m.some((x) => x.bad));
  const current = rows.map((r) => toSave(r, known));
  const dirty = !same(current as never, saved.map((p) => ({ profile: p.profile, onlyAdd: p.onlyAdd, onlyGroups: p.onlyGroups, groupIds: p.groupIds })) as never);
  const busy = save.isPending || !!hxDemo.busy;

  const edit = (f: (rs: Row[]) => Row[]) => {
    setRows(f);
    setDone(false);
    setFail(null);
    setRefusal(null);
  };
  const patch = (k: number, p: Partial<Row>) => edit((rs) => rs.map((r) => (r.k === k ? { ...r, ...p } : r)));
  const paste = (k: number, text: string) => {
    const mode = linkMode(text); // a link says whether the app only adds the profile or turns it on too
    patch(k, { text, ...(mode && { onlyAdd: mode === "add" }) });
  };
  const add = () => {
    const k = keys.current++;
    edit((rs) => [...rs, { k, text: "", text0: "", onlyAdd: false, onlyGroups: false, groupIds: [] }]);
    setOpen(k);
  };
  const discard = () => {
    setRows(saved.map(fromSaved).map((r, k) => ({ ...r, k })));
    setDone(false);
    setFail(null);
    setRefusal(null);
    setOpen(null);
  };
  function onSave() {
    const at = rows.findIndex((r) => r.k === open);
    onCarry({ open: at < 0 ? null : at, saved: true }); // before the save: the form starts again when the new settings arrive, which is before the save answers
    save
      .mutateAsync((cur) => ({ ...cur, routingProfiles: current.map((p) => ({ ...p, info: undefined })) as never }))
      .then(() => {
        setDone(true);
        toast(t("subs.saved"));
      })
      .catch((e: unknown) => {
        onCarry(null);
        const c = ConnectError.from(e);
        if (c.code === Code.InvalidArgument) {
          const f = routingRefusal(c.rawMessage);
          const row = f.index !== undefined ? rows[f.index] : undefined;
          setRefusal({ text: f.text, ...(row && { k: row.k }) });
          if (row) setOpen(row.k);
        } else setFail(errorText(e, t));
        toast.error(errorText(e, t));
      });
  }

  // leaving the tab (the strip, a link in the tab) while the form holds changes asks first: the screen does that, told here. Leaving the page, or closing it, the router asks.
  useLeaveGuard(dirty);
  const held = useRef(false);
  const leaving = useRef(false);
  useEffect(() => {
    held.current = dirty;
  }, [dirty]);
  const blocker = useBlocker({
    shouldBlockFn: ({ current: here, next }) => held.current && !leaving.current && next.pathname !== here.pathname,
    enableBeforeUnload: () => held.current,
    withResolver: true,
  });
  // development only: a scene asks to leave the tab, to show the question
  useEffect(() => {
    if (!hxDemo.leave) return;
    const id = setTimeout(() => document.querySelector<HTMLElement>('.sb-tabs [role="tab"]:not([aria-selected="true"])')?.click(), 150);
    return () => clearTimeout(id);
  }, []);

  const hint = bad ? t("sx.rt.ftBad") : fail ? fail : done ? t("subs.saved") : rows.length >= routingLimit.count ? t("sx.rt.ftMax", { max: routingLimit.count }) : t("sx.rt.ftHint");
  const cur = rows.find((r) => r.k === open);

  return (
    <div className="hx-tab rt-tab">
      <p className="sb-intro">{t("sx.rt.intro")}</p>
      <div className="pf-g pp-grid sb-grid">
        <div className="col">
          <section className="card hx-card">
            <CardHead title={t("sx.rt.title")} count={t("sx.rt.sz", { n: rows.length, max: routingLimit.count })} />
            {rows.length === 0 && <Nothing title={t("sx.rt.noneT")} text={t("sx.rt.noneS")} />}
            <div className="hx-l">
              {rows.map((r, i) => (
                <RoutingRow
                  key={r.k}
                  t={t}
                  row={r}
                  n={i + 1}
                  open={open === r.k}
                  name={names[i]!}
                  info={infoOf(r)}
                  check={checkOf(r)}
                  messages={[...messages[i]!, ...(refusal?.k === r.k ? [{ text: `${t("sx.rt.refusedSave")}: ${refusal.text}`, bad: true }] : [])]}
                  groups={groupList}
                  known={known}
                  onToggle={() => setOpen(open === r.k ? null : r.k)}
                  onPaste={(text) => paste(r.k, text)}
                  onChange={(p) => patch(r.k, p)}
                  onRemove={() => {
                    edit((rs) => rs.filter((x) => x.k !== r.k));
                    setOpen(null);
                  }}
                />
              ))}
            </div>
            {refusal && refusal.k === undefined && (
              <div className="hx-whole">
                <Msg kind="bad">{`${t("sx.rt.refusedSave")}: ${refusal.text}`}</Msg>
              </div>
            )}
            <Foot
              addLabel={t("sx.rt.add")}
              onAdd={add}
              addOff={rows.length >= routingLimit.count}
              hint={hint}
              bad={bad || !!fail}
              saveLabel={busy ? t("sx.hx.h.saving") : t("subs.save")}
              saveOff={bad || busy || waiting || !dirty || known === null}
              onSave={onSave}
              {...(dirty && !busy && { onDiscard: discard, discardLabel: t("subs.discard") })}
            />
            <p className="hx-rs">{t("sx.rt.rewrite")}</p>
          </section>
        </div>
        <div className="col">
          <Summary t={t} row={cur} name={cur ? names[rows.indexOf(cur)]! : ""} info={cur ? infoOf(cur) : undefined} dropped={cur ? dropped(cur) : []} reading={!!cur && cur.text.trim() !== cur.text0 && !infoOf(cur) && !refused(checkOf(cur)) && !stuck(checkOf(cur))} />
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

type RowProps = {
  t: Tx;
  row: Row;
  n: number;
  open: boolean;
  name: string;
  info: RoutingInfo | undefined;
  check: Check | undefined;
  messages: { text: string; bad: boolean }[];
  groups: Group[];
  known: string[] | null;
  onToggle: () => void;
  onPaste: (text: string) => void;
  onChange: (p: Partial<Row>) => void;
  onRemove: () => void;
};

function RoutingRow({ t, row, n, open, name, info, check, messages, groups, known, onToggle, onPaste, onChange, onRemove }: RowProps) {
  const failed = messages.find((m) => m.bad);
  const where = !row.onlyGroups ? t("sx.rt.allGroups") : groups.filter((g) => row.groupIds.includes(g.id)).map((g) => g.name).join(", ") || t("sx.rt.noGroups");
  const meta = `${where} · ${t(row.onlyAdd ? "sx.rt.modeAdd" : "sx.rt.modeOn")}`;
  const toggleGroup = (id: string) => {
    const next = row.groupIds.includes(id) ? row.groupIds.filter((x) => x !== id) : [...row.groupIds, id];
    onChange({ onlyGroups: true, groupIds: known ? next.filter((g) => known.includes(g)) : next });
  };
  const reading = row.text.trim() !== "" && row.text.trim() !== row.text0 && !info && !refused(check) && !stuck(check);
  return (
    <div className={cx("hx-r", failed && "bad", open && "on")}>
      <RowHead n={n} open={open} onToggle={onToggle} meta={meta}>
        <b className="mono">{name || t("sx.rt.unnamed")}</b>
        <span className="mono">{info ? t("sx.rt.counts", { direct: info.directSites + info.directIp, proxy: info.proxySites + info.proxyIp, block: info.blockSites + info.blockIp }) : reading ? t("sx.rt.reading") : t("sx.rt.noValue")}</span>
        {!open && failed && <em className="bad">{failed.text}</em>}
      </RowHead>
      {open && (
        <div className="hx-ed">
          <label className="hx-f">
            <span>{t("sx.rt.paste")}</span>
            <textarea className="an-i mono rt-y" placeholder={t("sx.rt.pastePh")} autoComplete="off" autoCapitalize="off" spellCheck={false} value={row.text} onChange={(e) => onPaste(e.target.value)} />
          </label>
          <Opt label={t("sx.rt.mode")}>
            <Chips label={t("sx.rt.mode")}>
              <HxChip on={!row.onlyAdd} onClick={() => onChange({ onlyAdd: false })}>
                {t("sx.rt.mOn")}
              </HxChip>
              <HxChip on={row.onlyAdd} onClick={() => onChange({ onlyAdd: true })}>
                {t("sx.rt.mAdd")}
              </HxChip>
            </Chips>
          </Opt>
          <Opt label={t("sx.rt.groups")}>
            <Chips label={t("sx.rt.groups")}>
              <HxChip on={!row.onlyGroups} onClick={() => onChange({ onlyGroups: false, groupIds: [] })}>
                {t("sx.rt.gAll")}
              </HxChip>
              {groups.map((g) => (
                <HxChip key={g.id} on={row.onlyGroups && row.groupIds.includes(g.id)} onClick={() => toggleGroup(g.id)}>
                  {g.name}
                </HxChip>
              ))}
            </Chips>
          </Opt>
          {reading && <p className="hx-note">{t("sx.rt.reading")}</p>}
          {messages.map((m) => (
            <Msg key={m.text} kind={m.bad ? "bad" : "part"}>
              {m.text}
            </Msg>
          ))}
          <div className="hx-ea">
            <span />
            <Button className="sb-btn hx-del" onClick={onRemove}>
              {t("sx.rt.del")}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

/** What the open profile holds, in words: the rules by their counts, the DNS, the geo files, how it reaches the app and what the panel left out. */
function Summary({ t, row, name, info, dropped, reading }: { t: Tx; row: Row | undefined; name: string; info: RoutingInfo | undefined; dropped: string[]; reading: boolean }) {
  const n = (sites: number, ip: number) => (sites + ip === 0 ? t("sx.rt.none") : t("sx.rt.cnt", { sites, ip }));
  const dns = (kind: string, addr: string) => (addr ? `${kind ? `${kind} ` : ""}${addr}` : t("sx.rt.dnsApp"));
  const inBody = !!info && info.headerBytes > routingLimit.header;
  const hosts = info ? geoHosts(info) : [];
  const rest = info?.globalProxy === "true" ? t("sx.rt.restVpn") : info?.globalProxy === "false" ? t("sx.rt.restDirect") : t("sx.rt.restApp");
  return (
    <section className="card hx-card hx-pv">
      <CardHead title={t("sx.rt.sTitle")} />
      <div className="hx-pb">
        {!row || !info ? (
          <p className="hx-note">{!row ? t("sx.rt.sPick") : reading && row.text.trim() !== "" ? t("sx.rt.sWait") : t("sx.rt.sPaste")}</p>
        ) : (
          <div className="hx-out" aria-live="polite">
            <div>
              <span>{t("sx.rt.sName")}</span>
              <b>{name}</b>
            </div>
            <div>
              <span>{t("sx.rt.sDirect")}</span>
              <b>{n(info.directSites, info.directIp)}</b>
            </div>
            <div>
              <span>{t("sx.rt.sProxy")}</span>
              <b>{n(info.proxySites, info.proxyIp)}</b>
            </div>
            <div>
              <span>{t("sx.rt.sBlock")}</span>
              <b>{n(info.blockSites, info.blockIp)}</b>
            </div>
            <div>
              <span>{t("sx.rt.sRest")}</span>
              <b>{rest}</b>
            </div>
            <div>
              <span>{t("sx.rt.sDns")}</span>
              <b>{t("sx.rt.dnsPreset")}</b>
              {(info.remoteDns || info.domesticDns) && <span>{t("sx.rt.dnsOwn", { remote: dns(info.remoteDnsType, info.remoteDns), domestic: dns(info.domesticDnsType, info.domesticDns) })}</span>}
            </div>
            <div>
              <span>{t("sx.rt.sGeo")}</span>
              <b className={hosts.length > 0 ? "mono" : undefined}>{hosts.length > 0 ? hosts.join(", ") : t("sx.rt.geoApp")}</b>
            </div>
            <div>
              <span>{t("sx.rt.sSend")}</span>
              <b>{inBody ? t("sx.rt.inBody", { n: info.headerBytes, max: routingLimit.header }) : t("sx.rt.inHeader", { n: info.headerBytes })}</b>
            </div>
            {dropped.length > 0 && (
              <div>
                <span>{t("sx.rt.sDropped")}</span>
                <b className="mono">{dropped.join(", ")}</b>
                <span>{t("sx.rt.droppedNote")}</span>
              </div>
            )}
          </div>
        )}
        <p className="hx-note">{t("sx.rt.note")}</p>
      </div>
    </section>
  );
}
