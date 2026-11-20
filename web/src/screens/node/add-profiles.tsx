import { useQueries } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { Fragment, useState, type ReactNode } from "react";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { profiles as profilesApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { agentLinked } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { portNotes, withNotes } from "@/lib/port-check";
import { useTx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { refusalOf } from "@/screens/profiles/deploy";
import { DlgFrame } from "@/screens/profiles/pf-frame";
import { Mark, PK, kindOf } from "@/screens/profiles/pf-bits";
import { PtHead, PtWhy } from "@/screens/profiles/pt-bits";
import { askNode, rowOf, shapeOf, type Asked, type NodeIn, type NodeRow, type PtGroup, type PtShape } from "@/screens/profiles/put-check";
import { useInboundRefresh } from "./inbound-check";
import { profileLine } from "./add-inbound";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;
const TICK = "M5 12.5l4.5 4.5L19 7.5";
const sections: PtGroup[] = ["can", "act", "no"];

/** What the form still has to do for a row: a field to fill (a domain, a port), a choice (the AmneziaWG backend), a note about a shared port. */
const needsForm = (r: NodeRow) => r.need || r.backend || !!r.shared;

/** The row's section and line: a row the list cannot settle by a tick is "needs an action", and the action opens the form for that profile. */
function shapeOfProfile(r: NodeRow, n: NodeIn): PtShape {
  const s = shapeOf(r, n);
  return !r.need && needsForm(r) ? { group: "act", kind: "act", why: r.text } : s;
}

/**
 * "Put a profile on a node" in the board's list (PnNode, dlg=add): the profiles the node does not have, each already checked by the
 * panel (CreateInbound with validate_only), in the sections "can be put / needs an action / cannot". A profile that works as it is (or on a
 * free port the check found) is ticked and put with the others in one click; one that needs a field or a choice opens the form of that
 * profile ("Set up…"), which holds the rest of the way (the domain, the port, the AmneziaWG backend, the certificate).
 */
export function AddProfilesList({ data, available, onClose, onForm }: { data: Plain<GetNodeResponse>; available: ProfileSummary[]; onClose: () => void; onForm: (profileId: string) => void }) {
  const t = useTx();
  const toast = useToast();
  const node = data.node!;
  const refresh = useInboundRefresh(node.id);
  const [picked, setPicked] = useState<Record<string, boolean>>({});
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState<Record<string, string>>({});

  const asks = useQueries({
    queries: available.map((p) => ({
      queryKey: ["node-add-check", p.id, node.id],
      queryFn: ({ signal }: { signal: AbortSignal }) => askNode({ profileId: p.id, nodeId: node.id, port: "", sni: "", anyway: false }, signal),
      retry: false,
      staleTime: 5000,
    })),
  });
  const nodeIn = (p: ProfileSummary): NodeIn => ({ id: node.id, name: node.name, address: node.address, linked: agentLinked(node.status), entry: false, firstAwg: p.protocol === "awg" && !(node.protocols ?? []).includes("awg"), awgMode: node.awgBackend });
  const view = available.map((p, i) => {
    const asked: Asked = asks[i]?.data ?? { state: "pending" };
    const ni = nodeIn(p);
    const r = rowOf(asked, ni, { t, protocol: p.protocol, fix: {} });
    const pending = asked.state === "pending";
    const shape = shapeOfProfile(r, ni);
    const can = !pending && !r.need && !needsForm(r);
    // the port it will take: the check's own, or the free one it found when the profile's is taken
    const port = asked.state === "ok" ? String(asked.inbound?.port ?? "") : (r.port?.free ?? "");
    const net = PK[kindOf(p.protocol)].net;
    return { p, asked, r, pending, shape, group: pending ? ("can" as PtGroup) : shape.group, can, on: can && !!picked[p.id], port: port && port !== "0" ? `${net}/${port}` : "" };
  });
  const flat = view.length > 0 && view.every((v) => v.pending);
  const counts: Record<PtGroup, number> = { can: 0, act: 0, no: 0 };
  for (const v of view) counts[v.group]++;
  const listed = view.slice().sort((a, b) => sections.indexOf(a.group) - sections.indexOf(b.group));
  const chosen = view.filter((v) => v.on);
  const sectionTitle = { can: t("ps.pt.gCan"), act: t("ps.pt.gAct"), no: t("ps.pt.gNo") } as const;

  async function put() {
    setBusy(true);
    setFailed({});
    const notes: string[] = [];
    const bad: Record<string, string> = {};
    for (const v of chosen) {
      try {
        const added = await profilesApi.createInbound({ profileId: v.p.id, nodeId: node.id, portOverride: v.r.port?.free ? Number(v.r.port.free) : 0, tlsServerNameOverride: "" });
        if (added.inbound) markFresh("inbound", added.inbound.id);
        notes.push(...portNotes(t, added.warnings));
      } catch (e) {
        bad[v.p.id] = refusalOf(e, t, node, v.p.protocol).text;
      }
    }
    setBusy(false);
    refresh();
    const done = chosen.length - Object.keys(bad).length;
    if (done > 0) toast(withNotes(t("node.profiles.added", { name: node.name }), notes), notes.length > 0 ? { ms: 8000 } : undefined);
    if (Object.keys(bad).length === 0) return onClose();
    setFailed(bad);
    setPicked((x) => Object.fromEntries(Object.entries(x).filter(([id]) => bad[id])));
  }

  return (
    <DlgFrame
      open
      onClose={onClose}
      busy={busy}
      className="hl-dlg nd-pa"
      title={t("node.profiles.addTitle", { name: node.name })}
      footer={
        <>
          <Lnk className="an-lk nd-panew" to="/profiles/new" search={{ node: node.id }} onClick={onClose}>
            {t("profiles.new")}…
          </Lnk>
          <Button size="lg" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="primary" size="lg" aria-disabled={chosen.length === 0 || busy} onClick={() => chosen.length > 0 && !busy && void put()}>
            {chosen.length > 0 ? t("node.profiles.putN", { n: chosen.length }) : t("node.profiles.put")}
          </Button>
        </>
      }
    >
      <div className="us-db">
        <p>{t("node.profiles.addLead", { name: node.name })}</p>
        <div className="pt-l">
          {listed.map((v, i) => {
            const { p, r } = v;
            const why = failed[p.id] ?? (v.pending ? "" : v.shape.why);
            const kind = failed[p.id] ? "no" : v.shape.kind;
            const head = !flat && v.group !== listed[i - 1]?.group ? v.group : null;
            return (
              <Fragment key={p.id}>
                {head && <PtHead title={sectionTitle[head]} count={counts[head]} hint={head === "act" ? t("node.profiles.actHint") : undefined} />}
                <div className={cx("pt-r pt-pf", !flat && !v.pending && kind)}>
                  <div className="pt-hw">
                    <button type="button" className="pt-h" role="checkbox" aria-checked={v.on} aria-disabled={!v.can} onClick={() => v.can && setPicked((x) => ({ ...x, [p.id]: !x[p.id] }))}>
                      <span className={cx("fz-cb us-cb", v.on && "on", !v.can && "dis")} aria-hidden>
                        <i>
                          <G d={TICK} size={14} stroke={2.5} />
                        </i>
                      </span>
                      <Mark k={kindOf(p.protocol)} />
                      <b title={profileLine(t, p)}>{p.name}</b>
                      <span className="pt-ct" />
                      {v.pending ? <em>{t("ps.pt.check")}</em> : <span className="mono nd-pap">{v.port}</span>}
                    </button>
                    {/* a profile that works as it is is put with a tick; its own port or domain are asked for in its form, here */}
                    {v.can && (
                      <button type="button" className="an-lk pt-set" onClick={() => onForm(p.id)}>
                        {t("node.profiles.setUp")}
                      </button>
                    )}
                  </div>
                  {why && (
                    <PtWhy kind={kind} clock={v.shape.clock && !failed[p.id]}>
                      {why}
                    </PtWhy>
                  )}
                  {!v.pending && (r.upd || needsForm(r)) && (
                    <div className="pt-act">
                      {r.upd ? (
                        <Lnk className="btn sm" to="/updates" onClick={onClose}>
                          {t("ps.pt.openUpdates")}
                        </Lnk>
                      ) : (
                        <Button size="sm" onClick={() => onForm(p.id)}>
                          {t("node.profiles.setUp")}
                        </Button>
                      )}
                    </div>
                  )}
                </div>
              </Fragment>
            );
          })}
        </div>
      </div>
    </DlgFrame>
  );
}
