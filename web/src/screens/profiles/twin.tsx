import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { ProfileSummary, TwinProfileResponse } from "@/gen/mistgate/admin/v1/profile_pb";
import { StatusPill } from "@/components/ui/status";
import { profiles } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { lossPct, reasonShort } from "@/lib/port-check";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { Check, ConfirmModal } from "@/screens/users/ui";
import { WarpMissing, warpGaps } from "./egress-note";

export type Egress = "direct" | "warp";

/** The plan of a twin (a dry run of TwinProfile): the same key the dev kit fills, so the dialog can be looked at without a server. */
export const twinPlanKey = (profileId: string, egress: Egress, version: number) => ["profiles", "twin", profileId, egress, version] as const;

/**
 * "The same server with and without WARP", folded into "Where it runs" on the profile page: one line with the action. One
 * click makes a twin of the profile with the exit flipped, on the same nodes and in the same groups, on a port of its own.
 * The dialog lists exactly what will be made (the server's dry run); a node without a working WARP is named in it and in
 * the result, because the WARP twin carries nothing there. Owner only, like the three calls behind it (the server decides too).
 */
export function TwinPanel({ profile, egress, dirty }: { profile: ProfileSummary; egress: Egress; dirty: boolean }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const to: Egress = egress === "warp" ? "direct" : "warp";
  const [open, setOpen] = useState(false);
  const [done, setDone] = useState<TwinProfileResponse | null>(null);
  // the owner's ticks in the WARP twin's node list; an untouched node keeps its default (ticked when it has WARP)
  const [ticked, setTicked] = useState<Record<string, boolean>>({});
  const shown = open || done !== null;
  const nodes = useQuery({ ...nodesQuery, enabled: shown });
  const groups = useQuery({ ...groupsQuery, enabled: shown });
  const plan = useQuery({
    queryKey: twinPlanKey(profile.id, to, profile.version),
    queryFn: ({ signal }) => profiles.twinProfile({ profileId: profile.id, egress: to, dryRun: true }, { signal }),
    enabled: open,
  });
  if (!owner) return null;

  const awg = profile.protocol === "awg";
  const nodeName = (id: string) => nodes.data?.nodes.find((n) => n.id === id)?.name ?? id;
  const groupName = (id: string) => groups.data?.find((g) => g.id === id)?.name ?? id;
  const gaps = (r: TwinProfileResponse) => (r.egress === "warp" ? warpGaps(nodes.data?.nodes ?? [], r.nodeIds).missingNodes : []);
  const planGaps = plan.data ? gaps(plan.data) : [];
  // a WARP copy carries nothing on a node without WARP: such a node is left out unless the owner ticks it
  const isTicked = (id: string) => ticked[id] ?? !planGaps.some((n) => n.id === id);

  async function create() {
    try {
      const skipNodeIds = plan.data?.egress === "warp" ? plan.data.nodeIds.filter((id) => !isTicked(id)) : [];
      const res = await profiles.twinProfile({ profileId: profile.id, egress: to, port: plan.data?.port ?? 0, skipNodeIds });
      // a new profile, inbounds on the nodes, a changed group (its users' cards, devices and subscription previews)
      // (not the plan of this very dialog, which is still on screen while it closes)
      await Promise.all(["profiles", "groups", "users", "subs", "nodes", "node"].map((k) => qc.invalidateQueries({ queryKey: [k], predicate: (q) => q.queryKey[1] !== "twin" })));
      setDone(res);
    } catch (e) {
      toast.error(errorText(e, t));
      throw e;
    }
  }

  /**
   * The UDP delivery check behind the port: a badge per node for the port that was chosen (checked / unchecked and why), and
   * the candidates that were skipped because they lost packets.
   */
  const udp = (r: TwinProfileResponse): ReactNode => {
    const checks = r.portChecks ?? [];
    const chosen = checks.filter((c) => c.port === r.port);
    const skipped = checks.filter((c) => c.port !== r.port && (c.verdict === "lossy" || c.verdict === "broken"));
    if (chosen.length === 0 && skipped.length === 0) return null;
    return (
      <>
        <dt className="text-muted">{t("twin.udp")}</dt>
        <dd className="flex flex-col gap-1.5">
          <ul className="flex flex-wrap gap-1.5">
            {chosen.map((c) => {
              const node = nodeName(c.nodeId);
              const lost = lossPct(c.sent, c.got);
              return (
                <li key={c.nodeId}>
                  {c.verdict === "ok" ? (
                    <StatusPill kind="ok" label={t("twin.udp.ok", { node })} sm />
                  ) : c.verdict === "lossy" || c.verdict === "broken" ? (
                    <StatusPill kind="warn" label={t("twin.udp.lossy", { node, lost })} sm />
                  ) : (
                    <StatusPill kind="off" label={t("twin.udp.unchecked", { node, why: reasonShort(t, c.reason) })} sm />
                  )}
                </li>
              );
            })}
          </ul>
          {skipped.map((c) => (
            <span key={`${c.nodeId}/${c.port}`} className="text-xs text-muted">
              {t("twin.udp.skipped", { port: c.port, lost: lossPct(c.sent, c.got), node: nodeName(c.nodeId) })}
            </span>
          ))}
        </dd>
      </>
    );
  };

  /** What the owner must know about a plan or a result: where the twin goes, the WARP gaps, the AmneziaWG keys. */
  const details = (r: TwinProfileResponse, result: boolean): ReactNode => {
    // the plan of a WARP copy is a list to tick; the result names only the nodes it was made on
    const pick = !result && r.egress === "warp" && r.nodeIds.length > 0;
    const missing = gaps(r).filter((n) => result || isTicked(n.id));
    return (
      <div className="flex flex-col gap-3">
        <dl className="grid grid-cols-[84px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[13px]">
          <dt className="text-muted">{t("twin.name")}</dt>
          <dd className="font-bold break-words">{r.name}</dd>
          <dt className="text-muted">{t("twin.port")}</dt>
          <dd className="font-mono">udp/{r.port}</dd>
          <dt className="text-muted">{t("twin.nodes")}</dt>
          <dd>
            {pick ? (
              <ul className="flex flex-col gap-1.5">
                {r.nodeIds.map((id) => {
                  const noWarp = planGaps.some((n) => n.id === id);
                  return (
                    <li key={id}>
                      <label className="flex w-fit cursor-pointer items-center gap-2">
                        <Check checked={isTicked(id)} onCheckedChange={(on) => setTicked((x) => ({ ...x, [id]: on }))} label={nodeName(id)} />
                        <span>
                          {nodeName(id)}
                          {noWarp && <span className="text-muted">{t(isTicked(id) ? "twin.noWarp" : "twin.noWarpSkip")}</span>}
                        </span>
                      </label>
                    </li>
                  );
                })}
              </ul>
            ) : (
              r.nodeIds.map(nodeName).join(", ") || t("twin.noNodes")
            )}
          </dd>
          {udp(r)}
          <dt className="text-muted">{t("twin.groups")}</dt>
          <dd>{r.groupIds.map(groupName).join(", ") || t("twin.noGroups")}</dd>
        </dl>
        {missing.length > 0 && (
          <Notice>
            <WarpMissing nodes={missing} textKey="twin.warpMissing" />
          </Notice>
        )}
        {r.hopDropped && <p className="text-xs leading-normal text-muted">{t("twin.hop")}</p>}
        {awg && <p className="text-xs leading-normal text-muted">{t("twin.awg")}</p>}
        {r.groupIds.length > 0 && r.nodeIds.length > 0 && <p className="text-xs leading-normal text-muted">{t(result ? "twin.usersDone" : "twin.users")}</p>}
      </div>
    );
  };

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-line pt-3">
      <Button
        variant="secondary"
        size="sm"
        disabled={dirty}
        onClick={() => {
          setTicked({});
          setOpen(true);
        }}
      >
        <Icon name="copy" size={13} />
        {t(to === "warp" ? "twin.btn.warp" : "twin.btn.direct")}
      </Button>
      <span className="min-w-0 flex-1 text-[11px] leading-snug text-muted">{t(dirty ? "twin.dirty" : to === "warp" ? "twin.hint.warp" : "twin.hint.direct")}</span>

      <ConfirmModal
        open={open}
        onOpenChange={setOpen}
        title={t(to === "warp" ? "twin.confirm.warp" : "twin.confirm.direct", { name: profile.name })}
        description={`${t(to === "warp" ? "twin.body.warp" : "twin.body.direct")} ${t("twin.confirmBody")}`}
        confirmLabel={t("twin.do")}
        confirmDisabled={!plan.data || !nodes.data}
        onConfirm={create}
      >
        {plan.isError ? <Notice tone="danger">{errorText(plan.error, t)}</Notice> : plan.data ? details(plan.data, false) : <p className="text-sm text-muted">{plan.isPending && profile.nodeCount > 0 ? t.n("twin.checking", profile.nodeCount) : t("common.loading")}</p>}
      </ConfirmModal>

      <Modal
        open={done !== null}
        onOpenChange={(o) => !o && setDone(null)}
        title={t("twin.doneTitle", { name: done?.name ?? "" })}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDone(null)}>
              {t("common.close")}
            </Button>
            {done?.profile && (
              <Link to="/profiles/$id" params={{ id: done.profile.id }} className={buttonClass("primary", "md")} onClick={() => setDone(null)}>
                {t("twin.open")}
              </Link>
            )}
          </>
        }
      >
        {done && details(done, true)}
      </Modal>
    </div>
  );
}
