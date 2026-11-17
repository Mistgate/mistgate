import { Code, ConnectError } from "@connectrpc/connect";
import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useEffect, useRef, useState, type ReactNode } from "react";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { PrepareAwgOutcome } from "@/gen/mistgate/admin/v1/node_pb";
import { nodes as nodesApi, profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { countryLook } from "@/lib/health";
import { inboundErrorKind, inboundErrorText } from "@/lib/inbound-error";
import { agentLinked } from "@/lib/node-status";
import { portNotes } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { useT } from "@/i18n";
import { asMode, AwgBackendFields, canPrepare } from "@/screens/node/awg-backend";
import { useTx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { refusalOf, isIp } from "./deploy";
import { DlgFrame } from "./pf-frame";
import { Gl } from "./pf-bits";
import type { TabCtx } from "./page-types";
import { useServers } from "./profiles-data";
import { askNode, rowOf, type Asked, type Fix, type NodeRow, type Verdict } from "./put-check";
import "./profile-tabs.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;
const TICK = "M5 12.5l4.5 4.5L19 7.5";

type Phase = "wait" | "put" | "apply" | "ok" | "bad" | "off";
type Run = { phase: Phase; text: string; inboundId?: string; mend: "" | "port" | "domain"; free?: string; lossy?: string; notes?: string[]; since: number };
const gOf: Record<Phase, "ok" | "bad" | "busy" | "none"> = { wait: "none", put: "busy", apply: "busy", ok: "ok", bad: "bad", off: "none" };

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const POLL_MS = 1500;
/** A node that has not answered in this long keeps its row as "still applying"; the Servers tab goes on following it. */
const PATIENCE_MS = 120_000;

/**
 * "Put on nodes" (the board's pp-dlg, put): the nodes that do not carry the profile, each checked before the click (CreateInbound with
 * validate_only) and ticked where it will work as it is; what a node lacks (a domain, a port, the AmneziaWG backend) is asked right
 * under it. The click puts the profile on the ticked nodes one after another (CreateInbound, the node's backend first for its first
 * AmneziaWG profile) and follows each until the node says it runs. A node that refuses does not undo the others. The window may be closed
 * while it runs: the Servers tab shows the rest.
 */
export function PutDialog({ ctx, onClose, preselect, autorun }: { ctx: TabCtx; onClose: () => void; preselect?: string[]; autorun?: boolean }) {
  const t = useTx();
  const tt = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const { profile } = ctx.data;
  const list = useQuery(nodesQuery);
  const srv = useServers([profile]);
  // the nodes that carried the profile when the window opened; one put on here stays listed with its result
  const [here] = useState(() => new Set(ctx.servers.map((s) => s.nodeId)));
  const cands = (list.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED && !here.has(n.id));
  const [picked, setPicked] = useState<Record<string, boolean>>(() => Object.fromEntries((preselect ?? []).map((id) => [id, true])));
  const [fix, setFix] = useState<Record<string, Fix>>({});
  const [typed, setTyped] = useState<Record<string, { sni: string; port: string }>>({});
  const [backendOpen, setBackendOpen] = useState<Record<string, boolean>>({});
  const [phase, setPhase] = useState<"pick" | "run" | "done">("pick");
  const [runIds, setRunIds] = useState<string[]>([]);
  const [rows, setRows] = useState<Record<string, Run>>({});
  const live = useRef(rows);
  const patch = (id: string, p: Partial<Run>) => {
    live.current = { ...live.current, [id]: { ...(live.current[id] ?? { phase: "wait", text: "", mend: "", since: 0 }), ...p } };
    setRows(live.current);
  };
  const awg = profile.protocol === "awg";
  const picking = phase === "pick";

  const asks = useQueries({
    queries: cands.map((n) => {
      const f = fix[n.id] ?? {};
      return { queryKey: ["put-check", profile.id, n.id, f.sni ?? "", f.port ?? "", !!f.anyway], queryFn: ({ signal }: { signal: AbortSignal }) => askNode({ profileId: profile.id, nodeId: n.id, port: f.port ?? "", sni: f.sni ?? "", anyway: !!f.anyway }, signal), enabled: phase === "pick", retry: false, staleTime: 5000 };
    }),
  });
  const askedOf = (i: number): Asked => asks[i]?.data ?? { state: "pending" };
  const checking = phase === "pick" && (list.isPending || asks.some((q) => q.isPending));

  const nodeIn = (n: (typeof cands)[number]) => ({ id: n.id, name: n.name, address: n.address, linked: agentLinked(n.status), entry: srv.entries.has(n.id), firstAwg: awg && !n.protocols.includes("awg"), awgMode: n.awgBackend });
  const view = cands.map((n, i) => {
    const ni = nodeIn(n);
    const r: NodeRow = rowOf(askedOf(i), ni, { t, protocol: profile.protocol, fix: fix[n.id] ?? {} });
    const on = !r.need && (picked[n.id] ?? r.on);
    const c = srv.country?.byNode.get(n.id);
    const k = c ? countryLook(tt, fmt, c).kind : "";
    const g: Verdict = !c ? "none" : k === "ok" ? "ok" : k === "partial" || k === "warn" ? "part" : k === "bad" || k === "listed" ? "bad" : "none";
    const chip = ni.entry ? (g === "ok" ? t("pf.ru.seen") : t("ps.pt.entryChip")) : g === "none" ? t("pf.ru.none") : t("ps.sv.direct");
    return { n, ni, r, on, g, chip };
  });
  const chosen = view.filter((v) => v.on && phase === "pick");
  const shown = phase === "pick" ? view : view.filter((v) => runIds.includes(v.n.id));

  async function put(id: string, f: Fix) {
    const n = cands.find((x) => x.id === id);
    if (!n) return;
    patch(id, { phase: "put", text: "", since: Date.now(), mend: "" });
    try {
      const mode = f.backend ?? asMode(n.awgBackend);
      if (awg && !n.protocols.includes("awg") && mode !== asMode(n.awgBackend)) {
        if (mode === "kernel" && canPrepare(n)) {
          const plan = await nodesApi.prepareAwgKernel({ nodeId: id, confirm: false });
          if (plan.outcome !== PrepareAwgOutcome.READY) throw new ConnectError("kernel_not_ready", Code.FailedPrecondition);
        }
        await nodesApi.updateNode({ nodeId: id, awgBackend: mode });
      }
      const added = await profilesApi.createInbound({ profileId: profile.id, nodeId: id, portOverride: f.port ? Number(f.port) : 0, tlsServerNameOverride: awg ? "" : (f.sni ?? "").trim(), ...(f.anyway && { allowLossyPort: true }) });
      if (added.inbound) markFresh("inbound", added.inbound.id);
      const linked = agentLinked(n.status);
      patch(id, { phase: linked ? "apply" : "off", inboundId: added.inbound?.id, text: linked ? "" : t("ps.pt.offline"), notes: portNotes(tt, added.warnings), mend: "" });
    } catch (e) {
      const ref = refusalOf(e, t, n, profile.protocol);
      if (ref.kind === "already") return patch(id, { phase: "ok", text: t("pf.st.works") });
      patch(id, { phase: "bad", text: ref.text, mend: ref.kind === "port" ? "port" : ref.kind === "domain" ? "domain" : "", free: ref.kind === "port" ? ref.free : undefined, lossy: ref.kind === "port" ? ref.lossy : undefined });
      if (ref.kind === "port" && ref.free) setFix((x) => ({ ...x, [id]: { ...f, port: ref.free } }));
    }
  }

  /** Reads the profile's inbounds once: a node that runs is "ok", one that failed says why. Returns whether any row still waits. */
  async function follow(): Promise<boolean> {
    const got = await profilesApi.getProfile({ profileId: profile.id });
    for (const [id, r] of Object.entries(live.current)) {
      if (r.phase !== "apply") continue;
      const i = got.inbounds.find((x) => x.nodeId === id);
      if (i?.state === InboundState.ACTIVE) patch(id, { phase: "ok", text: t("pf.st.works") });
      else if (i?.state === InboundState.FAILED) {
        const kind = i.lastError ? inboundErrorKind(i.lastError) : "other";
        patch(id, { phase: "bad", inboundId: i.id, text: t("ps.pt.failed", { why: inboundErrorText(tt, i.lastError, { port: i.port, protocol: profile.protocol }) }), mend: kind === "port" ? "port" : kind === "cert" ? "domain" : "" });
      } else if (Date.now() - r.since > PATIENCE_MS) patch(id, { phase: "off", text: t("ps.pt.slow") });
    }
    return Object.values(live.current).some((r) => r.phase === "apply");
  }
  const refresh = () => Promise.all(["profiles", "nodes", "node", "groups", "users", "subs"].map((k) => qc.invalidateQueries({ queryKey: [k] })));

  async function start(ids: string[]) {
    setRunIds((was) => [...new Set([...was, ...ids])]);
    setPhase("run");
    for (const id of ids) patch(id, { phase: "wait", text: "", since: Date.now(), mend: "" });
    for (const id of ids) await put(id, fix[id] ?? {});
    await refresh();
    try {
      while (await follow()) {
        await sleep(POLL_MS);
        await refresh();
      }
    } catch {
      /* the Servers tab goes on following it */
    }
    await refresh();
    setPhase("done");
  }
  const started = useRef(false);
  useEffect(() => {
    if (!autorun || started.current || list.isPending || !preselect?.length) return;
    started.current = true;
    void start(preselect);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- once, when the nodes are here
  }, [autorun, list.isPending]);

  const failed = runIds.filter((id) => rows[id]?.phase === "bad");
  const finished = runIds.filter((id) => ["ok", "bad", "off"].includes(rows[id]?.phase ?? ""));
  const okN = runIds.filter((id) => rows[id]?.phase === "ok").length;
  const offN = runIds.filter((id) => rows[id]?.phase === "off");
  const units = runIds.reduce((a, id) => a + ({ wait: 0, put: 0.5, apply: 1, ok: 2, bad: 2, off: 2 } as const)[rows[id]?.phase ?? "wait"], 0);
  const w = runIds.length ? Math.min(100, Math.round((units / (runIds.length * 2)) * 100)) : 0;
  const name = (id: string) => cands.find((n) => n.id === id)?.name ?? id;
  const title = phase === "done" ? (failed.length > 0 ? t("ps.pt.doneFail", { ok: okN, n: runIds.length, bad: failed.map(name).join(", ") }) : offN.length > 0 ? t("ps.pt.doneOff", { ok: okN, n: runIds.length, off: offN.map(name).join(", ") }) : t("ps.pt.doneAll", { ok: okN, n: runIds.length })) : t("ps.pt.run", { k: finished.length, n: runIds.length });

  const retry = () => void start(failed);
  const rmFailed = async () => {
    const ids = failed.filter((id) => rows[id]?.inboundId);
    if (ids.length > 0) await profilesApi.removeProfileFromNodes({ profileId: profile.id, nodeIds: ids, deleteProfile: false, confirmName: "" });
    await refresh();
    onClose();
  };
  const done = () => {
    onClose();
    ctx.goTab("servers");
  };
  const footText = checking ? t("ps.pt.checkingNodes") : phase === "pick" ? t.n("ps.pt.picked", chosen.length) : phase === "done" ? t("ps.pt.canClose") : t("ps.pt.closeOk");
  const btnT = phase === "done" ? t("common.done") : phase === "run" ? t("ps.pt.putting") : t.n("ps.pt.putOn", chosen.length);
  const dis = phase === "run" || (phase === "pick" && (checking || chosen.length === 0));
  const go = () => (phase === "done" ? done() : phase === "pick" && !dis ? void start(chosen.map((v) => v.n.id)) : undefined);

  const commit = (id: string, p: Partial<Fix>) => setFix((x) => ({ ...x, [id]: { ...x[id], ...p } }));
  const typedOf = (id: string) => typed[id] ?? { sni: "", port: "" };

  return (
    <DlgFrame
      open
      onClose={onClose}
      title={t("ps.pt.title", { name: profile.name })}
      shot="put-dialog"
      footer={
        <>
          <span>{footText}</span>
          {phase === "done" && failed.length > 0 && (
            <>
              {failed.some((id) => rows[id]?.inboundId) && (
                <button type="button" className="an-lk" onClick={() => void rmFailed()}>
                  {t("ps.pt.rmFailed")}
                </button>
              )}
              <Button size="lg" onClick={retry}>
                {t("ps.pt.retry", { nodes: failed.map(name).join(", ") })}
              </Button>
            </>
          )}
          <Button variant="primary" size="lg" aria-disabled={dis} onClick={go}>
            {btnT}
          </Button>
        </>
      }
    >
      <div className="us-db pp-db">
        {phase === "pick" && <p className="rw-lead">{t("ps.pt.lead")}</p>}
        {phase !== "pick" && (
          <>
            <div className="rw-rh">
              <b>{title}</b>
            </div>
            <i className={cx("rw-pg", failed.length > 0 && phase === "done" && "bad")} style={{ ["--w" as string]: `${w}%` }} aria-hidden />
          </>
        )}
        {cands.length === 0 && !list.isPending && <p>{t("where.noNodeLeft")}</p>}
        {shown.map(({ n, ni, r, on, g, chip }) => {
          const run = rows[n.id];
          const f = fix[n.id] ?? {};
          const ph = run?.phase;
          const mend = ph === "bad" && !run?.inboundId ? run?.mend : "";
          const showDom = phase === "pick" ? r.dom : mend === "domain";
          const showPort = phase === "pick" ? !!r.port && r.need : mend === "port";
          const nd = typedOf(n.id);
          const lossy = phase === "pick" ? r.port?.lossy : run?.lossy;
          return (
            <div key={n.id} className={cx("pp-pn", r.need && phase === "pick" && "need", phase !== "pick" && ph && (ph === "bad" ? "bad" : ph === "ok" ? "ok" : ph === "off" || ph === "wait" ? "none" : "busy"))}>
              <button
                type="button"
                className="pp-pnh"
                role="checkbox"
                aria-checked={on || !picking}
                aria-disabled={r.need || phase !== "pick"}
                onClick={() => phase === "pick" && !r.need && setPicked((x) => ({ ...x, [n.id]: !on }))}
              >
                {phase === "pick" ? (
                  <span className={cx("fz-cb us-cb", on && "on")} aria-hidden>
                    <i>
                      <G d={TICK} size={14} stroke={2.5} />
                    </i>
                  </span>
                ) : (
                  <Gl k={gOf[ph ?? "wait"]} />
                )}
                <Flag code={n.countryCode} size={14} />
                <b className="mono">{n.name}</b>
                {ni.entry && <i className="rw-role">{t("ps.pt.entryRole")}</i>}
                <span className={cx("rw-chip", g)}>
                  <Gl k={g} />
                  {chip}
                </span>
                <em className={phase === "pick" ? (checking ? "" : r.verdict === "none" ? "" : r.verdict) : ph === "ok" ? "ok" : ph === "bad" ? "bad" : ""}>{phase === "pick" ? (checking ? t("ps.pt.check") : r.text) : (run?.text ?? "")}</em>
              </button>
              {phase !== "pick" && run?.notes?.map((note) => (
                <div key={note} className="pp-pnf">
                  <small>{note}</small>
                </div>
              ))}
              {phase === "pick" && !checking && r.upd && (
                <div className="pp-pnf">
                  <Lnk className="btn sm" to="/updates" onClick={onClose}>
                    {t("ps.pt.openUpdates")}
                  </Lnk>
                </div>
              )}
              {phase === "pick" && !checking && r.backend && on && (
                <div className="pp-pnf" style={{ flexDirection: "column" }}>
                  <span>
                    {t("where.awgFirst", { node: n.name, mode: tt(`awg.backend.${f.backend ?? asMode(n.awgBackend)}`) })}{" "}
                    {!backendOpen[n.id] && (
                      <button type="button" className="an-lk" onClick={() => setBackendOpen((x) => ({ ...x, [n.id]: true }))}>
                        {t("where.awgChange")}
                      </button>
                    )}
                  </span>
                  {backendOpen[n.id] && <AwgBackendFields mode={f.backend ?? asMode(n.awgBackend)} onChange={(m) => commit(n.id, { backend: m })} label={tt("awg.backend.title")} auto={canPrepare(n)} />}
                </div>
              )}
              {showDom && (
                <div className="pp-pnf">
                  <input className="an-i mono" type="text" placeholder="vpn.example.com" aria-label={t("where.domainLabel", { node: n.name })} value={nd.sni} autoCapitalize="off" spellCheck={false} onChange={(e) => setTyped((x) => ({ ...x, [n.id]: { ...nd, sni: e.target.value.trim() } }))} />
                  <Button size="md" aria-disabled={!/\./.test(nd.sni) || isIp(nd.sni)} onClick={() => /\./.test(nd.sni) && !isIp(nd.sni) && commit(n.id, { sni: nd.sni })}>
                    {t("ps.pt.setDomain")}
                  </Button>
                </div>
              )}
              {showPort && (
                <div className="pp-pnf">
                  <input className="an-i mono" type="text" inputMode="numeric" aria-label={t("where.portLabel", { node: n.name })} placeholder={t("where.portLabel", { node: n.name })} value={nd.port} onChange={(e) => setTyped((x) => ({ ...x, [n.id]: { ...nd, port: e.target.value.replace(/\D/g, "").slice(0, 5) } }))} />
                  <Button size="md" aria-disabled={!nd.port} onClick={() => nd.port && commit(n.id, { port: nd.port, anyway: false })}>
                    {t("ps.pt.tryPort")}
                  </Button>
                  {lossy && !(f.anyway && f.port === lossy) && (
                    <Button size="md" onClick={() => commit(n.id, { port: lossy, anyway: true })}>
                      {tt("ports.anyway.put")} · {lossy}
                    </Button>
                  )}
                </div>
              )}
              {phase === "done" && ph === "bad" && run?.inboundId && (
                <div className="pp-pnf">
                  <Button
                    size="md"
                    onClick={() => {
                      onClose();
                      ctx.onEdit(run.inboundId!);
                      ctx.goTab("servers");
                    }}
                  >
                    {t("pf.pg.vd.port", { node: n.name })}
                  </Button>
                </div>
              )}
            </div>
          );
        })}
        {checking && cands.length === 0 && <p className="rw-lead">{t("ps.pt.checkingNodes")}</p>}
      </div>
    </DlgFrame>
  );
}

