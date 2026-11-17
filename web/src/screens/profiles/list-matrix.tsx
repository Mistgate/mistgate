import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { useT } from "@/i18n";
import { inboundConnParts } from "@/lib/connections";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryLook } from "@/lib/health";
import { relayState, statePill } from "@/lib/relay";
import { G } from "@/screens/users/user-bits";
import { useTx, type Tx } from "@/screens/users/t";
import { D, Gl, kindOf, Mark } from "./pf-bits";
import type { Servers } from "./profiles-data";
import type { Gk, PCell } from "./profiles-model";
import "./profiles.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;

/** The state's words, as the drawer's pill says them. */
const stateWord = (c: PCell, t: Tx) =>
  c.kind === "bad" ? t("pf.st.fail") : c.kind === "busy" ? t("pf.st.start") : c.kind === "off" ? t("pf.st.off") : c.kind === "gone" ? t("pf.st.gone") : c.kind === "part" ? t("pf.st.partial") : t("pf.st.works");
const pillOf: Record<PCell["kind"], string> = { ok: "ok", part: "part", bad: "bad", busy: "busy", off: "", gone: "" };

/** What a cell of the matrix says: its number or its word, the small line under it and the relay that also reaches it. */
function cellText(c: PCell, t: Tx, conns: number | undefined, relay: string) {
  const conn = conns === undefined ? "" : t("pf.cell.conn", { n: conns });
  switch (c.kind) {
    case "gone":
      return { t: t("pf.cell.gone"), s: "", cls: "off", rl: "" };
    case "bad":
      return { t: t("pf.cell.fail"), s: "", cls: "bad", rl: "" };
    case "busy":
      return { t: t("pf.cell.start"), s: "", cls: "busy", rl: "" };
    case "off":
      return { t: t("pf.cell.off"), s: "", cls: "off", rl: "" };
    case "part":
      return { t: t.n("pf.cell.rounds", Math.max(1, c.streak)), s: conn, cls: "part", rl: "" };
    default:
      return { t: c.checked ? t("pf.cell.ms", { ms: c.ms }) : t("pf.cell.works"), s: c.checked ? conn : t("pf.cell.noCheck"), cls: "", rl: relay ? `+${relay}` : "" };
  }
}

/**
 * The board's matrix "Where it passes": a row per profile, a column per node (headed by the node and what the country check says of
 * it), a cell where the profile is on that node (its number or its trouble) and a quiet "+" where it is not (for the owner). A click on
 * a cell opens the drawer: that server's check, its connections, the relays, and the way to the profile and the node. On a narrow
 * panel each row stacks into its own list of nodes.
 */
export function ProfilesMatrix({ profiles, servers, owner, open, onOpen }: { profiles: readonly ProfileSummary[]; servers: Servers; owner: boolean; open: string; onOpen: (id: string) => void }) {
  const t = useTx();
  const tt = useT();
  const fmt = useFmt();
  const [pid, nid] = open.split("|");
  const cols = servers.nodes;
  if (servers.checks.isPending) return <MatrixLoading t={t} />;
  if (servers.checks.isError) {
    return (
      <section className="card pf-none pv-qe" role="alert">
        <span className="g bad" aria-hidden>
          <G d={D.warn} size={20} stroke={1.5} />
        </span>
        <b>{t("pf.mx.err")}</b>
        <Button size="lg" onClick={() => void servers.checks.refetch()}>
          {t("pf.err.retry")}
        </Button>
      </section>
    );
  }
  const headOf = (n: (typeof cols)[number]): { gl: Gk; word: string; off: boolean; ent: boolean; tip: string } => {
    const ent = servers.entries.has(n.nodeId);
    const c = servers.country?.byNode.get(n.nodeId);
    if (n.nodeStatus === NodeStatus.DOWN) return { gl: "bad", word: t("pf.ru.gone"), off: true, ent, tip: "" };
    if (!c) return { gl: "none", word: "", off: false, ent, tip: "" };
    const k = countryLook(tt, fmt, c).kind;
    const gl: Gk = k === "ok" ? "ok" : k === "partial" || k === "warn" ? "part" : k === "bad" || k === "listed" ? "bad" : "none";
    const word = k === "ok" ? (ent ? t("pf.ru.seen") : t("pf.ru.open")) : k === "partial" || k === "warn" ? t("pf.ru.part") : k === "bad" || k === "listed" ? t("pf.ru.closed") : t("pf.ru.none");
    return { gl, word, off: false, ent, tip: word };
  };
  const heads = cols.map((n) => ({ n, ...headOf(n) }));
  const cellOf = (p: ProfileSummary, nodeId: string) => servers.cells(p.id).find((c) => c.nodeId === nodeId);
  const sel = pid && nid ? profiles.find((p) => p.id === pid) : undefined;
  const selCell = sel ? cellOf(sel, nid!) : undefined;
  const drawer = sel && selCell ? <Drawer p={sel} c={selCell} servers={servers} owner={owner} onClose={() => onOpen("")} /> : null;
  return (
    <div className={cx("pv-wrap", drawer && "open")}>
      <section className="card pv-mx" style={{ ["--nc" as string]: String(cols.length) }}>
        <div className="pv-mr pv-mh">
          <span>{t("pf.col.profile")}</span>
          {heads.map(({ n, gl, word, off, ent, tip }) => (
            <Lnk
              key={n.nodeId}
              className={cx("pv-nh", ent && "ent", off && "off")}
              to="/nodes/$id"
              params={{ id: n.nodeId }}
              title={t("pf.nh.tt", { node: n.nodeName, where: ent ? `${n.countryCode} · ${t("pf.nh.entry")}` : n.countryCode, verdict: tip || "—" })}
            >
              <b>
                <Flag code={n.countryCode} size={11} />
                <span className="mono">{n.nodeName}</span>
              </b>
              {word && (
                <span>
                  <Gl k={gl} />
                  <span>{word}</span>
                </span>
              )}
            </Lnk>
          ))}
        </div>
        {profiles.map((p) => {
          const kind = kindOf(p.protocol);
          return (
            <div className="pv-mr" key={p.id}>
              <Lnk className="pv-n" to="/profiles/$id" params={{ id: p.id }}>
                <Mark k={kind} />
                <span>
                  <b>{p.name}</b>
                  <span>{p.protocol}</span>
                </span>
              </Lnk>
              {cols.map((n) => {
                const c = cellOf(p, n.nodeId);
                if (!c)
                  return (
                    <span className="pv-c none" data-l={n.nodeName} key={n.nodeId}>
                      {owner && (
                        <Lnk to="/profiles/$id" params={{ id: p.id }} search={{ tab: "servers" }} title={t("pf.cell.put", { node: n.nodeName })} aria-label={t("pf.cell.put", { node: n.nodeName })}>
                          <G d={D.plus} size={14} stroke={1.5} />
                        </Lnk>
                      )}
                    </span>
                  );
                const x = cellText(c, t, servers.connOf.get(c.inboundId)?.connects, (servers.via.get(c.inboundId) ?? [])[0] ?? "");
                const on = open === `${p.id}|${n.nodeId}`;
                return (
                  <button
                    type="button"
                    key={n.nodeId}
                    className={cx("pv-c", x.cls, on && "on")}
                    data-l={n.nodeName}
                    aria-pressed={on}
                    aria-label={`${p.name}, ${n.nodeName}: ${stateWord(c, t)}`}
                    onClick={() => onOpen(on ? "" : `${p.id}|${n.nodeId}`)}
                  >
                    <span>
                      <Gl k={c.gl} />
                      <b className={c.kind === "ok" && c.checked ? "mono" : ""}>{x.t}</b>
                    </span>
                    <em>{x.s}</em>
                    {x.rl && <u title={t("pf.cell.relay")}>{x.rl}</u>}
                  </button>
                );
              })}
            </div>
          );
        })}
        <p className="rw-foot pv-foot">{t("pf.mx.foot")}</p>
      </section>
      {drawer}
    </div>
  );
}

/** The matrix while the checks load: the rows' bars in the matrix's own shape. */
function MatrixLoading({ t }: { t: Tx }) {
  return (
    <section className="card pv-tb pv-skc" aria-busy="true" aria-label={t("pf.loading")}>
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <div key={i} className="pv-r pv-sk mx">
          <span className="pv-n">
            <i className="pf-sk sq" />
            <span>
              <i className="pf-sk" style={{ width: 130 }} />
              <i className="pf-sk s" style={{ width: 210 }} />
            </span>
          </span>
          <span className="pv-wh">
            <i className="pf-sk p" />
            <i className="pf-sk p" />
            <i className="pf-sk p" />
          </span>
          <span />
          <span />
          <span />
        </div>
      ))}
    </section>
  );
}

/** One server's drawer: its state, why it failed, the panel's check of the last day, the connections, the country and the relays. */
function Drawer({ p, c, servers, owner, onClose }: { p: ProfileSummary; c: PCell; servers: Servers; owner: boolean; onClose: () => void }) {
  const t = useTx();
  const tt = useT();
  const fmt = useFmt();
  const conn = servers.connOf.get(c.inboundId);
  const parts = conn ? inboundConnParts(tt, fmt, conn) : [];
  const country = servers.country?.byNode.get(c.nodeId);
  const look = country ? countryLook(tt, fmt, country) : undefined;
  const relays = owner ? servers.relaysOf(c.inboundId) : [];
  const check =
    c.kind === "gone" || !c.atUnix
      ? c.kind === "bad"
        ? t("pf.dr.failedSince")
        : t("pf.dr.checkNone")
      : c.kind === "part"
        ? t.n(c.ms ? "pf.dr.checkStreakMs" : "pf.dr.checkStreak", Math.max(1, c.streak), { ms: c.ms })
        : c.checked
          ? t("pf.dr.checkMs", { ms: c.ms, ago: fmt.ago(c.atUnix) })
          : t("pf.dr.checkNone");
  return (
    <aside
      className="card pv-dr"
      aria-label={t("pf.dr.label")}
      onKeyDown={(e) => {
        if (e.key === "Escape") onClose();
      }}
    >
      <header>
        <div>
          <b>
            <Flag code={c.countryCode} size={14} />
            <span>{t("pf.dr.title", { profile: p.name, node: c.nodeName })}</span>
          </b>
        </div>
        <span className={cx("pv-pill", pillOf[c.kind])}>
          <Gl k={c.gl} shut={c.kind === "off" || c.kind === "gone"} />
          {stateWord(c, t)}
        </span>
        <button type="button" className="pf-ic" aria-label={t("pf.dr.close")} onClick={onClose}>
          <G d={D.close} size={16} stroke={1.5} />
        </button>
      </header>
      {c.kind === "bad" && (
        <div className="pv-ds">
          <div className="pv-why">
            <span>{t("profiles.warn.inbound_failed", { node: c.nodeName })}</span>
            <Lnk className={buttonClass("secondary", "md")} to="/nodes/$id" params={{ id: c.nodeId }} search={{ tab: "profiles" }}>
              {t("pf.dr.openNode")}
            </Lnk>
          </div>
        </div>
      )}
      <div className="pv-ds">
        <span className="rw-sl">{t("pf.dr.check")}</span>
        <div className="pv-hist" aria-hidden>
          {c.hist.map((h, i) => (
            <i key={i} className={h === "ok" ? "" : h} />
          ))}
        </div>
        <p>{check}</p>
        <p className="mut">{t("pf.dr.histNote")}</p>
      </div>
      <div className="pv-ds">
        <span className="rw-sl">{t("pf.dr.conns")}</span>
        {parts.length === 0 ? (
          <div className="pv-ck">
            <Gl k="none" />
            <span>{t("pf.dr.connsNone")}</span>
          </div>
        ) : (
          parts.map((x, i) => (
            <div className="pv-ck" key={i} title={x.hint}>
              <Gl k={x.warn ? "part" : "ok"} />
              <span>{x.text}</span>
            </div>
          ))
        )}
      </div>
      {look && (
        <div className="pv-ds">
          <span className="rw-sl">{t("pf.dr.country")}</span>
          <div className="pv-ck">
            <Gl k={look.kind === "ok" ? "ok" : look.kind === "unknown" ? "none" : look.kind === "bad" || look.kind === "listed" ? "bad" : "part"} />
            <span>{look.head}</span>
            {look.note && <em>{look.note}</em>}
          </div>
        </div>
      )}
      {relays.length > 0 && (
        <div className="pv-ds">
          <span className="rw-sl">{t("pf.dr.relay")}</span>
          {relays.map((r) => {
            const s = relayState(r);
            return (
              <div className="pv-ck" key={r.id}>
                <Gl k={statePill[s].kind === "ok" ? "ok" : statePill[s].kind === "bad" ? "bad" : statePill[s].kind === "busy" ? "busy" : "none"} />
                <span>{t("pf.dr.relayLine", { exit: r.exitNodeName, via: r.viaNodeName, net: r.network, port: r.port, state: tt(statePill[s].word) })}</span>
              </div>
            );
          })}
        </div>
      )}
      <div className="pv-act">
        <Lnk className="nd-lk" to="/profiles/$id" params={{ id: p.id }}>
          {t("pf.dr.openProfile")}
        </Lnk>
        <Lnk className="nd-lk" to="/nodes/$id" params={{ id: c.nodeId }}>
          {t("pf.dr.openNode2")}
        </Lnk>
      </div>
    </aside>
  );
}
