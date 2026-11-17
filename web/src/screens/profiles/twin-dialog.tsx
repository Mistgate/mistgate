import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import type { TwinProfileResponse } from "@/gen/mistgate/admin/v1/profile_pb";
import { profiles } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { lossPct, reasonShort } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { useT } from "@/i18n";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { WarpMissing, warpGaps } from "./egress-note";
import { DlgFrame } from "./pf-frame";
import { Gl } from "./pf-bits";
import type { TabCtx } from "./page-types";
import { twinPlanKey, type Egress } from "./twin";
import "./profile-tabs.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;

/**
 * "A copy through WARP / without it" (the board's pp-dlg, twin): the server's dry run says what will be made (name, port, nodes, groups, the
 * other exit), the WARP copy lists the nodes to tick (a node without a working WARP is left out unless ticked), and the click makes it. When it is
 * made the same window says so and offers the copy. Owner only, like the three calls behind it.
 */
export function TwinDialog({ ctx, onClose }: { ctx: TabCtx; onClose: () => void }) {
  const t = useTx();
  const tt = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const { profile } = ctx.data;
  const to: Egress = ctx.egress === "warp" ? "direct" : "warp";
  const [done, setDone] = useState<TwinProfileResponse | null>(null);
  const [busy, setBusy] = useState(false);
  // the owner's ticks in the WARP copy's node list; an untouched node keeps its default (ticked when it has WARP)
  const [ticked, setTicked] = useState<Record<string, boolean>>({});
  const nodes = useQuery(nodesQuery);
  const groups = useQuery(groupsQuery);
  const plan = useQuery({ queryKey: twinPlanKey(profile.id, to, profile.version), queryFn: ({ signal }) => profiles.twinProfile({ profileId: profile.id, egress: to, dryRun: true }, { signal }) });
  const awg = profile.protocol === "awg";
  const nodeName = (id: string) => nodes.data?.nodes.find((n) => n.id === id)?.name ?? id;
  const groupName = (id: string) => groups.data?.find((g) => g.id === id)?.name ?? id;
  const gaps = (r: TwinProfileResponse) => (r.egress === "warp" ? warpGaps(nodes.data?.nodes ?? [], r.nodeIds).missingNodes : []);
  const planGaps = plan.data ? gaps(plan.data) : [];
  const isTicked = (id: string) => ticked[id] ?? !planGaps.some((n) => n.id === id);

  async function create() {
    setBusy(true);
    try {
      const skipNodeIds = plan.data?.egress === "warp" ? plan.data.nodeIds.filter((id) => !isTicked(id)) : [];
      const res = await profiles.twinProfile({ profileId: profile.id, egress: to, port: plan.data?.port ?? 0, skipNodeIds });
      // a new profile, inbounds on the nodes, a changed group (not the plan of this very window, which is still on screen)
      await Promise.all(["profiles", "groups", "users", "subs", "nodes", "node"].map((k) => qc.invalidateQueries({ queryKey: [k], predicate: (q) => q.queryKey[1] !== "twin" })));
      setDone(res);
    } catch (e) {
      toast.error(errorText(e, tt));
    } finally {
      setBusy(false);
    }
  }

  /** The UDP delivery check behind the port: a pill per node for the port that was chosen, and the candidates skipped for losing packets. */
  const udp = (r: TwinProfileResponse): ReactNode => {
    const checks = r.portChecks ?? [];
    const chosen = checks.filter((c) => c.port === r.port);
    const skipped = checks.filter((c) => c.port !== r.port && (c.verdict === "lossy" || c.verdict === "broken"));
    if (chosen.length === 0 && skipped.length === 0) return null;
    return (
      <div className="pp-chg">
        <b>{tt("twin.udp")}</b>
        <span>
          <span className="pp-ways">
            {chosen.map((c) => {
              const node = nodeName(c.nodeId);
              const bad = c.verdict === "lossy" || c.verdict === "broken";
              return (
                <span key={c.nodeId} className={`rw-chip ${c.verdict === "ok" ? "ok" : bad ? "part" : ""}`}>
                  <Gl k={c.verdict === "ok" ? "ok" : bad ? "part" : "none"} />
                  {c.verdict === "ok" ? tt("twin.udp.ok", { node }) : bad ? tt("twin.udp.lossy", { node, lost: lossPct(c.sent, c.got) }) : tt("twin.udp.unchecked", { node, why: reasonShort(tt, c.reason) })}
                </span>
              );
            })}
          </span>
          {skipped.map((c) => (
            <small key={`${c.nodeId}/${c.port}`} style={{ display: "block" }}>
              {tt("twin.udp.skipped", { port: c.port, lost: lossPct(c.sent, c.got), node: nodeName(c.nodeId) })}
            </small>
          ))}
        </span>
      </div>
    );
  };

  /** What the owner must know about a plan or a result: where the copy goes, the WARP gaps, the AmneziaWG keys. */
  const details = (r: TwinProfileResponse, result: boolean): ReactNode => {
    const pick = !result && r.egress === "warp" && r.nodeIds.length > 0;
    const missing = gaps(r).filter((n) => result || isTicked(n.id));
    return (
      <>
        <div className="pp-chg">
          <b>{t("ps.tw.name")}</b>
          <span>{r.name}</span>
        </div>
        <div className="pp-chg">
          <b>{t("ps.tw.port")}</b>
          <span className="mono">udp/{r.port}</span>
        </div>
        <div className="pp-chg">
          <b>{t("ps.tw.nodes")}</b>
          <span className="mono">
            {pick
              ? r.nodeIds.map((id) => {
                  const noWarp = planGaps.some((n) => n.id === id);
                  return (
                    <label key={id} style={{ display: "flex", alignItems: "center", gap: 8, cursor: "pointer", width: "fit-content" }}>
                      <input type="checkbox" checked={isTicked(id)} onChange={(e) => setTicked((x) => ({ ...x, [id]: e.target.checked }))} />
                      <span>
                        {nodeName(id)}
                        {noWarp && <small>{tt(isTicked(id) ? "twin.noWarp" : "twin.noWarpSkip")}</small>}
                      </span>
                    </label>
                  );
                })
              : r.nodeIds.map(nodeName).join(", ") || tt("twin.noNodes")}
          </span>
        </div>
        {udp(r)}
        <div className="pp-chg">
          <b>{t("ps.tw.groups")}</b>
          <span>{r.groupIds.map(groupName).join(", ") || tt("twin.noGroups")}</span>
        </div>
        <div className="pp-chg">
          <b>{t("ps.tw.exit")}</b>
          <span>{r.egress === "warp" ? t("ps.tw.viaWarp") : t("ps.tw.fromNode")}</span>
        </div>
        {missing.length > 0 && (
          <p>
            <WarpMissing nodes={missing} textKey="twin.warpMissing" />
          </p>
        )}
        {r.hopDropped && <p>{tt("twin.hop")}</p>}
        {awg && <p>{tt("twin.awg")}</p>}
        {r.groupIds.length > 0 && r.nodeIds.length > 0 && <p>{tt(result ? "twin.usersDone" : "twin.users")}</p>}
      </>
    );
  };

  const title = done ? tt("twin.doneTitle", { name: done.name }) : to === "warp" ? t("pf.pg.twin.warp") : t("pf.pg.twin.direct");
  return (
    <DlgFrame
      open
      onClose={onClose}
      title={title}
      className="pp-del"
      busy={busy}
      shot="twin-dialog"
      footer={
        done ? (
          <>
            <span />
            <Button size="lg" onClick={onClose}>
              {t("common.close")}
            </Button>
            {done.profile && (
              <Lnk to="/profiles/$id" params={{ id: done.profile.id }} className={buttonClass("primary", "lg")} onClick={onClose}>
                {tt("twin.open")}
              </Lnk>
            )}
          </>
        ) : (
          <>
            <span />
            <Button size="lg" disabled={busy} onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button variant="primary" size="lg" aria-disabled={!plan.data || !nodes.data || busy || ctx.edits > 0} onClick={() => plan.data && nodes.data && ctx.edits === 0 && void create()}>
              {t("ps.tw.make")}
            </Button>
          </>
        )
      }
    >
      <div className="us-db pp-db">
        {plan.isError && !done ? <p>{errorText(plan.error, tt)}</p> : !done && <p>{t("ps.tw.lead")}</p>}
        {ctx.edits > 0 && !done && <p>{t("pf.pg.twin.dirty")}</p>}
        {done ? details(done, true) : plan.data ? details(plan.data, false) : !plan.isError && <p>{plan.isPending && profile.nodeCount > 0 ? tt.n("twin.checking", profile.nodeCount) : t("ps.sv.loading")}</p>}
      </div>
    </DlgFrame>
  );
}
