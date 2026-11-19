import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Fresh } from "@/components/fx/fresh";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { useToast } from "@/components/ui/toast";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { countryLook } from "@/lib/health";
import { inboundErrorKind, inboundErrorText, nameFixes } from "@/lib/inbound-error";
import { agentLinked } from "@/lib/node-status";
import { nodesQuery } from "@/lib/queries";
import { relayState, relaysQuery, statePill } from "@/lib/relay";
import { useT } from "@/i18n";
import { useRunChecks } from "@/screens/health/history";
import { useInboundRefresh } from "@/screens/node/inbound-check";
import { usePortHolders } from "@/screens/node/profiles";
import { useTx, type Tx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { networkOf } from "@/lib/ways-graph";
import { WarpMissing, warpGaps } from "./egress-note";
import { PfMenu, PfMenuItem, PfMenuLink } from "./pf-frame";
import { D, Gl } from "./pf-bits";
import type { TabCtx } from "./page-types";
import { useServers } from "./profiles-data";
import type { Gk, ServerRow } from "./profiles-model";
import { ServerEdit } from "./server-edit";
import { useTakeOff } from "./take-off";
import "./profiles.css";
import "./profile-page.css";
import "./profile-tabs.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;

const pillOf: Record<ServerRow["kind"], string> = { ok: "ok", part: "part", bad: "bad", busy: "busy", off: "", gone: "" };
const rank: Record<ServerRow["kind"], number> = { bad: 0, gone: 1, busy: 2, part: 3, ok: 4, off: 5 };
const stateWord = (k: ServerRow["kind"], t: Tx) => ({ bad: t("pf.st.fail"), busy: t("pf.st.start"), off: t("pf.st.off"), gone: t("pf.st.gone"), part: t("pf.st.partial"), ok: t("pf.st.works") })[k];

/** The tokens of a scene's variant that fix a state of this tab (the dev kit's; a real page has none). */
const has = (variant: string, token: string) => variant.split(".").includes(token);

/**
 * The checking run the button starts (the board's ppChk): the rows that work are walked through one after another, the one being asked
 * is "checking", the rest "queued", and when the last one is done they flash once. The panel's own answer lands on the rows as the
 * checks refresh; a scene fixes the walk at its first step (`frozen`).
 */
function useCheckWalk(okIds: readonly string[], frozen: "" | "walk" | "flash") {
  const [step, setStep] = useState(frozen === "walk" ? 1 : 0);
  const [flash, setFlash] = useState(frozen === "flash");
  const timer = useRef<ReturnType<typeof setInterval>>(undefined);
  const wait = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(
    () => () => {
      clearInterval(timer.current);
      clearTimeout(wait.current);
    },
    [],
  );
  const start = () => {
    if (step > 0 || frozen) return;
    clearTimeout(wait.current);
    setFlash(true);
    setStep(1);
    let k = 1;
    timer.current = setInterval(() => {
      k += 1;
      if (k > okIds.length) {
        clearInterval(timer.current);
        setStep(0);
        wait.current = setTimeout(() => setFlash(false), 1600);
      } else setStep(k);
    }, 650);
  };
  const phase = (id: string): "" | "run" | "wait" | "done" => {
    const at = okIds.indexOf(id);
    if (at < 0 || step === 0) return "";
    return at === step - 1 ? "run" : at > step - 1 ? "wait" : "done";
  };
  return { step, flash: flash && step === 0, flashing: flash, start, phase, busy: step > 0 };
}

/**
 * The course of an apply (the board's .pp-apply): while some server of the profile is starting after the profile was saved, a card says
 * "Applying on N nodes · k of N" with a line that fills and a row for each node; when the last one is up it says so for six seconds.
 */
function useApply(rows: readonly ServerRow[], frozen: "" | "run" | "done") {
  const busyIds = rows.filter((r) => r.kind === "busy").map((r) => r.inbound.id);
  // a scene fixes the course: the first three servers that are on, with the ones that are up already ticked off
  const [ids, setIds] = useState<string[]>(() => (frozen ? rows.filter((r) => r.kind !== "off").slice(0, 3).map((r) => r.inbound.id) : []));
  const [done, setDone] = useState(frozen === "done");
  const seen = useRef(frozen === "done");
  useEffect(() => {
    if (busyIds.length > 0) {
      seen.current = true;
      setDone(false);
      setIds((was) => [...new Set([...was, ...busyIds])]);
    } else if (seen.current) {
      seen.current = false;
      setDone(true);
      const id = setTimeout(() => (setDone(false), setIds([])), 6000);
      return () => clearTimeout(id);
    }
    return undefined;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the ids that are starting now, as a key
  }, [busyIds.join(",")]);
  const list = rows.filter((r) => ids.includes(r.inbound.id));
  const upN = list.filter((r) => r.kind !== "busy").length;
  return { on: frozen === "run" || busyIds.length > 0 || done, done: busyIds.length === 0 && done, list, upN };
}

export function ServersTab(ctx: TabCtx) {
  const { data, owner, canGroups, servers: all, edit, onEdit, openDlg, variant } = ctx;
  const t = useTx();
  const tt = useT();
  const fmt = useFmt();
  const toast = useToast();
  const profile = data.profile;
  const srv = useServers([profile]);
  const nodeList = useQuery(nodesQuery);
  const relays = useQuery({ ...relaysQuery(""), enabled: owner });
  const can = useRunChecks("");
  const canRestart = canGroups; // RestartInbounds is the helper's too (policy.go); the rest of the menu is the owner's
  const rows = [...all].sort((a, b) => rank[a.kind] - rank[b.kind] || a.nodeName.localeCompare(b.nodeName));
  const nodeOf = new Map((nodeList.data?.nodes ?? []).map((n) => [n.id, n]));
  const bad = rows.filter((r) => r.kind === "bad");
  const okIds = rows.filter((r) => r.kind === "ok").map((r) => r.inbound.id);
  const walk = useCheckWalk(okIds, has(variant, "chk") ? "walk" : has(variant, "chkd") ? "flash" : "");
  const apply = useApply(rows, has(variant, "apply") ? "run" : has(variant, "applied") ? "done" : "");
  const [editing, setEditing] = useState(has(variant, "edit") && rows[0] ? { id: rows[0].inbound.id, focus: (has(variant, "port") ? "port" : has(variant, "sni") ? "sni" : undefined) as "port" | "sni" | undefined } : { id: "", focus: undefined as "port" | "sni" | undefined });
  const [ask, setAsk] = useState(has(variant, "ask") && rows[0] ? rows[0].inbound.id : "");
  // the head or the verdict asks this tab to open a server's port and domain (once)
  useEffect(() => {
    if (!edit) return;
    const r = rows.find((x) => x.inbound.id === edit);
    if (r) setEditing({ id: r.inbound.id, focus: r.inbound.lastError && nameFixes(inboundErrorKind(r.inbound.lastError)) ? "sni" : "port" });
    onEdit("");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only when the request arrives
  }, [edit]);

  const takeOff = useTakeOff(profile.id);
  const gone = useMutation({
    mutationFn: (r: ServerRow) => takeOff([r.nodeId]),
    onSuccess: (res, r) => {
      setAsk("");
      toast(t.n("where.offDone", res.removed) + ` · ${r.nodeName}`);
    },
    onError: (e) => toast.error(errorText(e, tt)),
  });

  const total = rows.length;
  const rest = (nodeList.data?.nodes ?? []).filter((n) => !rows.some((r) => r.nodeId === n.id) && n.status !== NodeStatus.RETIRED);
  const restNames = rest.map((n) => n.name);
  const canPut = owner && restNames.length > 0;
  const ckState = srv.checks.isError ? "err" : srv.checks.isPending ? "load" : "";
  const sum: { t: string; c: string; g?: Gk }[] = [{ t: t.n("pf.row.nodesCount", total), c: "" }];
  if (bad.length > 0) sum.push({ t: t.n("ps.sv.sumBad", bad.length), c: "bad", g: "bad" });
  sum.push({ t: bad.length > 0 ? t("ps.sv.sumExcept", { nodes: bad.map((b) => b.nodeName).join(", ") }) : t("ps.sv.sumAll"), c: "" });
  const warpGap = ctx.egress === "warp" ? warpGaps(nodeList.data?.nodes ?? [], rows.map((r) => r.nodeId)).missingNodes : [];

  const rl = (relays.data ?? []).filter((r) => rows.some((x) => x.inbound.id === r.inboundId));
  if (total === 0) {
    return (
      <div className="col pp-col">
        <section className="card us-none pr-none">
          <span className="g" aria-hidden>
            <G d={D.server} size={20} stroke={1.5} />
          </span>
          <b>{t("ps.sv.none")}</b>
          <span>{t("ps.sv.noneBody")}</span>
          {owner && (
            <Button variant="primary" size="lg" onClick={() => openDlg("put")}>
              {t("pf.pg.put")}
            </Button>
          )}
        </section>
      </div>
    );
  }

  const countryG = (nodeId: string, down: boolean): Gk => {
    const c = srv.country?.byNode.get(nodeId);
    if (down || !c) return "none";
    const k = countryLook(tt, fmt, c).kind;
    return k === "ok" ? "ok" : k === "partial" || k === "warn" ? "part" : k === "bad" || k === "listed" ? "bad" : "none";
  };

  return (
    <div className="col pp-col">
      {apply.on && (
        <section className="card pp-apply" aria-live="polite">
          <div className="pp-aph">
            <b>{apply.done ? t("ps.ap.done") : t.n("ps.ap.run", apply.list.length, { k: apply.upN, m: apply.list.length })}</b>
            <i className="rw-pg" style={{ ["--w" as string]: `${apply.list.length ? Math.round((apply.upN / apply.list.length) * 100) : 100}%` }} aria-hidden />
          </div>
          {apply.list.map((r) => (
            <div key={r.inbound.id} className={r.kind !== "busy" ? "done" : ""}>
              <Gl k={r.kind === "busy" ? "busy" : r.kind === "bad" ? "bad" : "ok"} />
              <span className="mono">{r.nodeName}</span>
              <span>{r.kind === "busy" ? t("ps.ap.busy") : r.kind === "bad" ? t("pf.st.fail") : t("ps.ap.did")}</span>
            </div>
          ))}
        </section>
      )}
      <section className="card pp-sec pp-svc">
        <div className="pp-sh">
          <span className="pp-ic sky" aria-hidden>
            <G d={D.server} size={20} stroke={1.5} />
          </span>
          <h3>{t("pf.pg.tab.servers")}</h3>
        </div>
        <div className="pp-svt">
          <p className="pv-sum">
            {sum.map((x, i) => (
              <span key={i} className={x.c}>
                {x.g && <Gl k={x.g} />}
                {x.t}
              </span>
            ))}
          </p>
          {can.can && (
            <button type="button" className={cx("btn sm", walk.busy && "busy")} aria-disabled={walk.busy} onClick={() => (can.run(), walk.start())}>
              <Btn walk={walk.busy}>{walk.busy ? t("ps.sv.checking") : t("ps.sv.checkNow")}</Btn>
            </button>
          )}
          {canPut && (
            <button type="button" className="btn sm" onClick={() => openDlg("put")}>
              {t("pf.pg.put")}
            </button>
          )}
          {owner && (
            <button type="button" className="btn sm" onClick={() => openDlg("take")}>
              {t("ps.sv.take")}
            </button>
          )}
        </div>
        <div className="pp-s1 pp-svh" aria-hidden>
          <span>{t("ps.sv.col.node")}</span>
          <span>{t("ps.sv.col.port")}</span>
          <span>{t("ps.sv.col.state")}</span>
          <span>{t("ps.sv.col.check")}</span>
          <span>{t("ps.sv.col.conns")}</span>
          <span>{t("ps.sv.col.ways")}</span>
          <span />
        </div>
        {ckState === "err" && (
          <p className="pp-rest pp-cke">
            {t("pf.mx.err")} ·{" "}
            <button type="button" className="an-lk" onClick={() => void srv.checks.refetch()}>
              {t("pf.err.retry")}
            </button>
          </p>
        )}
        {ckState === "load" ? (
          <div aria-busy="true" aria-label={t("ps.sv.loading")}>
            {[168, 140, 156].map((w) => (
              <div key={w} className="pp-sksv">
                <i className="pf-sk" style={{ width: 44 }} />
                <i className="pf-sk" style={{ width: 72 }} />
                <i className="pf-sk p" />
                <i className="pf-sk" style={{ width: w }} />
                <i className="pf-sk" style={{ width: 40 }} />
              </div>
            ))}
          </div>
        ) : (
          rows.map((r) => {
            const i = r.inbound;
            const node = nodeOf.get(r.nodeId);
            const ph = walk.phase(i.id);
            const ck = ckOf(r, ph, t, fmt);
            const conn = srv.connOf.get(i.id);
            const cn = r.kind === "ok" && conn ? t("pf.cell.conn", { n: conn.connects }) : "—";
            const hidden = !i.directInSubscriptions;
            const g = hidden ? "none" : countryG(r.nodeId, r.kind === "gone");
            const via = srv.via.get(i.id) ?? [];
            const open = editing.id === i.id;
            const net = networkOf(i.protocol) ?? "udp";
            const certAt = Number(i.certNotAfterUnix);
            const certLeft = certAt ? Math.ceil((certAt - Date.now() / 1000) / 86400) : 0;
            const notes: ReactNode[] = [];
            if (r.kind === "ok" && i.awg) notes.push(t.n("ps.sv.awg", i.awg.peers, { online: i.awg.peersOnline, ago: Number(i.awg.newestHandshakeUnix) ? fmt.ago(Number(i.awg.newestHandshakeUnix)) : "—", backend: i.awg.backend }));
            if (r.kind === "ok" && i.protocol !== "awg" && certAt) notes.push(certLeft < 14 ? t("node.profiles.certLeft", { date: fmt.date(certAt), n: Math.max(0, certLeft) }) : t("ps.sv.cert", { date: fmt.date(certAt) }));
            if (warpGap.some((n) => n.id === r.nodeId)) notes.push(<WarpMissing key="warp" nodes={warpGap.filter((n) => n.id === r.nodeId)} />);
            return (
              <Fresh key={i.id} kind="inbound" id={i.id} className={cx("pp-sv", r.kind === "bad" && "bad", r.kind === "off" && "off", ph === "run" ? "cking" : ph === "wait" ? "ckw" : r.kind === "ok" && walk.flash && "ckn")}>
                <div className="pp-s1">
                  <span className="pp-sn">
                    <Flag code={r.countryCode} size={14} />
                    <Lnk className="mono" to="/nodes/$id" params={{ id: r.nodeId }} search={{ tab: "profiles" }}>
                      {r.nodeName}
                    </Lnk>
                    {node?.location && <em>{node.location}</em>}
                  </span>
                  <span className="mono pp-sp">
                    {net}/{i.port}
                    {i.portOverride > 0 ? ` ${t("ps.sv.own")}` : ""}
                  </span>
                  <span className={cx("pv-pill", pillOf[r.kind])}>
                    <Gl k={r.gl} shut={r.kind === "off" || r.kind === "gone"} />
                    {stateWord(r.kind, t)}
                  </span>
                  <span className="pp-sk" data-l={t("ps.sv.col.check")}>
                    {ck.g && <Gl k={ck.g} />}
                    <Scramble value={ck.text} />
                  </span>
                  <span className="pp-sk" data-l={t("ps.sv.col.conns")}>
                    <Scramble value={cn} />
                  </span>
                  <span className="pp-ways">
                    <span className={cx("rw-chip", g)}>
                      <Gl k={g} />
                      {hidden ? t("ps.sv.directHidden") : t("ps.sv.direct")}
                    </span>
                    {via.map((v) => (
                      <span key={v} className="rw-chip ok">
                        <Gl k="ok" />
                        {v}
                      </span>
                    ))}
                  </span>
                  {(owner || canRestart) && (
                    <div className="up-mw pp-mw">
                      <RowMenu row={r} owner={owner} canRestart={canRestart} node={node} ask={ask} setAsk={setAsk} open={has(variant, "menu") && r === rows[0]} onEdit={() => setEditing({ id: i.id, focus: undefined })} onRemove={() => gone.mutate(r)} />
                    </div>
                  )}
                </div>
                {r.kind === "bad" && <WhyRow row={r} owner={owner} onFix={(f) => setEditing({ id: i.id, focus: f })} />}
                {notes.length > 0 && <em className="pp-s2">{notes.map((n, k) => (k > 0 ? <span key={k}> · {n}</span> : <span key={k}>{n}</span>))}</em>}
                {open && owner && <ServerEdit key={i.id} row={r} nodeAddress={node?.address ?? ""} focus={editing.focus} onClose={() => setEditing({ id: "", focus: undefined })} />}
              </Fresh>
            );
          })
        )}
        {restNames.length > 0 && (
          <p className="pp-rest">
            {t("ps.sv.rest")} <span className="mono">{restNames.join(", ")}</span>
            {owner && (
              <>
                {" "}
                ·{" "}
                <button type="button" className="an-lk" onClick={() => openDlg("put")}>
                  {t("ps.sv.restPut")}
                </button>
              </>
            )}
          </p>
        )}
      </section>
      {owner && (
        <section className="card pp-sec">
          <div className="pp-sh">
            <span className="pp-ic sky" aria-hidden>
              <G d="M4 8h13l-3-3M20 16H7l3 3" size={20} stroke={1.5} />
            </span>
            <h3>{t("ps.rl.title")}</h3>
          </div>
          {data.profile.protocol === "awg" && <p className="pp-quiet">{t("ps.rl.awg")}</p>}
          {rl.map((r) => {
            const row = rows.find((x) => x.inbound.id === r.inboundId)!;
            const st = statePill[relayState(r)];
            return (
              <Lnk key={r.id} className="pp-rl" to="/nodes/$id" params={{ id: r.exitNodeId }} search={{ tab: "ways" }}>
                <span>
                  <Flag code={row.countryCode} size={14} />
                  <b className="mono">{r.exitNodeName}</b> {t("ps.rl.via")} <Flag code={nodeOf.get(r.viaNodeId)?.countryCode ?? ""} size={14} />
                  <b className="mono">{r.viaNodeName}</b>
                </span>
                <span className="mono">{t("ps.rl.port", { net: r.network, port: r.port, via: r.viaNodeName })}</span>
                <span className={cx("pv-pill", st.kind === "ok" ? "ok" : st.kind === "bad" ? "bad" : st.kind === "busy" ? "busy" : "")}>
                  <Gl k={st.kind === "ok" ? "ok" : st.kind === "bad" ? "bad" : st.kind === "busy" ? "busy" : "none"} />
                  {tt(st.word)}
                </span>
                <em>{r.monitor?.ok && r.monitor.responseMs > 0 ? t("ps.rl.eyes", { ms: r.monitor.responseMs }) : ""}</em>
                <G d={D.chev} size={16} stroke={1.5} />
              </Lnk>
            );
          })}
          {rl.length === 0 && <p className="pp-quiet">{t("ps.rl.none")}</p>}
          <div className="pp-secf">
            <Lnk className={"btn sm"} to="/nodes">
              {t("ps.sv.via")}
            </Lnk>
          </div>
        </section>
      )}
    </div>
  );
}

const Btn = ({ walk, children }: { walk: boolean; children: ReactNode }) => (
  <>
    {walk && <span className="an-sp" aria-hidden />}
    {children}
  </>
);

/** What the panel's check column says of a server: its median and when, the failed rounds, a word while the walk asks it. */
function ckOf(r: ServerRow, ph: "" | "run" | "wait" | "done", t: Tx, fmt: ReturnType<typeof useFmt>): { text: string; g: Gk | "" } {
  if (ph === "run") return { text: t("ps.sv.ckRun"), g: "busy" };
  if (ph === "wait") return { text: t("ps.sv.ckWait"), g: "none" };
  const c = r.cell;
  if (r.kind === "ok") return c?.checked ? { text: `${t("pf.cell.ms", { ms: c.ms })} · ${fmt.ago(c.atUnix)}`, g: "ok" } : { text: t("pf.cell.noCheck"), g: "none" };
  if (r.kind === "part" && c) return { text: t.n("pf.cell.rounds", Math.max(1, c.streak)) + (c.ms ? ` · ${t("pf.cell.ms", { ms: c.ms })}` : ""), g: "part" };
  return { text: "—", g: "" };
}

/** The reason a server did not start, in words, with the node's own text under it and the one button that mends it (owner). */
function WhyRow({ row, owner, onFix }: { row: ServerRow; owner: boolean; onFix: (focus: "port" | "sni") => void }) {
  const t = useTx();
  const tt = useT();
  const i = row.inbound;
  const kind = inboundErrorKind(i.lastError);
  const holders = usePortHolders(row.nodeId, kind === "port");
  const text = inboundErrorText(tt, i.lastError, { port: i.port, protocol: i.protocol, process: holders[i.id] });
  return (
    <div className="rw-why pp-why">
      <div>
        <span>{text}</span>
        <code className="mono">{i.lastError}</code>
      </div>
      {owner &&
        (kind === "warp" ? (
          <Lnk className="btn sm" to="/nodes/$id" params={{ id: row.nodeId }} search={{ tab: "exit" }} hash="warp">
            {tt("warp.open")}
          </Lnk>
        ) : kind === "share" ? (
          <Lnk className="btn sm" to="/updates">
            {tt("share.openUpdates")}
          </Lnk>
        ) : (
          <Button size="md" onClick={() => onFix(nameFixes(kind) && i.protocol !== "awg" ? "sni" : "port")}>
            {kind === "port" ? t("pf.pg.vd.port", { node: row.nodeName }) : nameFixes(kind) && i.protocol !== "awg" ? tt("node.profiles.setDomain") : t("pf.pg.vd.fix")}
          </Button>
        ))}
    </div>
  );
}

/** The row's ⋯: change the port or the domain, restart, switch off, leave the node's address out of the subscriptions, relay it, open the node, take it off. */
function RowMenu({ row, owner, canRestart, node, ask, setAsk, open, onEdit, onRemove }: { row: ServerRow; owner: boolean; canRestart: boolean; node?: { status: number }; ask: string; setAsk: (id: string) => void; open: boolean; onEdit: () => void; onRemove: () => void }) {
  const t = useTx();
  const tt = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const refresh = useInboundRefresh(row.nodeId);
  const i = row.inbound;
  const off = i.state === InboundState.DISABLED;
  const hidden = !i.directInSubscriptions;
  const update = useMutation({
    mutationFn: (p: { enabled?: boolean; directInSubscriptions?: boolean }) => profilesApi.updateInbound({ inboundId: i.id, ...p }),
    onSuccess: () => {
      toast(t("common.saved"));
      refresh();
    },
    onError: (e) => toast.error(errorText(e, tt)),
  });
  const restart = useMutation({
    mutationFn: () => nodesApi.restartInbounds({ nodeId: row.nodeId, inboundId: i.id }),
    onSuccess: () => {
      toast(tt("node.profiles.restarted", { profile: i.profileName }));
      refresh();
      void qc.invalidateQueries({ queryKey: ["profiles"] });
    },
    onError: (e) => toast.error(errorText(e, tt)),
  });
  const linked = node ? agentLinked(node.status as never) : true;
  return (
    <PfMenu
      label={t("ps.sv.actions")}
      trigger={
        <button type="button" className="ic up-dots" aria-label={t("ps.sv.actions")}>
          <G d={D.dots} size={18} stroke={2.6} />
        </button>
      }
      defaultOpen={open}
    >
      {owner && <PfMenuItem onClick={onEdit}>{t("ps.sv.editPort")}</PfMenuItem>}
      {canRestart && linked && !off && <PfMenuItem onClick={() => restart.mutate()}>{t("ps.sv.restart")}</PfMenuItem>}
      {owner && <PfMenuItem onClick={() => update.mutate({ enabled: off })}>{off ? t("ps.sv.on") : t("ps.sv.off")}</PfMenuItem>}
      {owner && <PfMenuItem onClick={() => update.mutate({ directInSubscriptions: hidden })}>{hidden ? t("ps.sv.unhide") : t("ps.sv.hide")}</PfMenuItem>}
      {owner && (
        <PfMenuLink render={<Lnk to="/nodes/$id" params={{ id: row.nodeId }} search={{ tab: "ways" }} />}>{t("ps.sv.via")}</PfMenuLink>
      )}
      <PfMenuLink render={<Lnk to="/nodes/$id" params={{ id: row.nodeId }} search={{ tab: "profiles" }} />}>{t("ps.sv.openNode")}</PfMenuLink>
      {owner && (
        <PfMenuItem
          tone={ask === i.id ? "ask" : "risk"}
          closeOnClick={ask === i.id}
          onClick={() => {
            if (ask === i.id) onRemove();
            else setAsk(i.id);
          }}
        >
          {ask === i.id ? t("ps.sv.rmAsk", { node: row.nodeName }) : t("ps.sv.rm")}
        </PfMenuItem>
      )}
    </PfMenu>
  );
}
