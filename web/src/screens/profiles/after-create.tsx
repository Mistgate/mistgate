import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Flag } from "@/components/ui/flag";
import { Switch } from "@/components/ui/switch";
import { nodesQuery } from "@/lib/queries";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { PillToggle, wayOf } from "@/screens/users/groups";
import { groupsQuery, profileListQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { isIp } from "./deploy";
import { getAt, type Settings } from "./schema";

// "Right after it is created" on the new-profile form: the nodes it goes on and the groups it joins, chosen before the
// click, so a new profile works in one pass. A group gets it by default only when it has no profile of this protocol
// yet (without one its people get nothing in that app); a second profile of a protocol is usually a test, so groups that
// already have one are left alone. The hint says which case each group is.

/** "Hysteria2 · 443": the name a new profile starts with, following the port until the name is typed. */
export function autoName(info: Pick<ProtocolInfo, "displayName">, settings: Settings): string {
  const port = getAt(settings, ["port"]);
  return typeof port === "number" && port > 0 ? `${info.displayName} · ${port}` : info.displayName;
}

export type AfterPlan = { nodesOn: boolean; nodeIds: string[]; groupsOn: boolean; groupIds: string[]; everyone: boolean };

/** The data and the choice of the block (asked for only while `enabled`); the choice starts from the defaults and keeps what the owner changed. */
export function useAfterCreate(info: ProtocolInfo, fromNode: string, enabled = true) {
  const nodes = useQuery({ ...nodesQuery, enabled });
  const groups = useQuery({ ...groupsQuery, enabled });
  const profiles = useQuery({ ...profileListQuery, enabled });
  const [edit, setEdit] = useState<Partial<AfterPlan>>({});
  const live = (nodes.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED);
  const all = groups.data ?? [];
  const has = (g: Group) => g.profileIds.some((id) => profiles.data?.find((p) => p.id === id)?.protocol === info.id);
  const lacking = all.filter((g) => !has(g));
  const having = all.filter(has);
  const defaults: AfterPlan = {
    nodesOn: live.length > 0,
    nodeIds: fromNode && live.some((n) => n.id === fromNode) ? [fromNode] : live.map((n) => n.id),
    groupsOn: all.length === 0 || lacking.length > 0,
    groupIds: lacking.map((g) => g.id),
    everyone: true,
  };
  return {
    plan: { ...defaults, ...edit },
    set: (p: Partial<AfterPlan>) => setEdit((e) => ({ ...e, ...p })),
    groupsTouched: "groupIds" in edit,
    nodes: live,
    groups: all,
    lacking,
    having,
  };
}

export type AfterState = ReturnType<typeof useAfterCreate>;

const quoted = (gs: Group[]) => gs.map((g) => `«${g.name}»`).join(", ");

/** The app a protocol reaches, in words for the hint: the subscription apps by name, or AmneziaVPN. */
const appWords = (t: Tx, info: ProtocolInfo, linkApps: string) =>
  wayOf(info.id, [{ id: info.id, apps: info.apps }]) === "awg" ? t("profiles.appAwg") : t("profiles.appSub", { apps: linkApps });

export function AfterCreate({ a, info, settings }: { a: AfterState; info: ProtocolInfo; settings: Settings }) {
  const t = useTx();
  const linkApps = useLinkAppNames();
  const p = a.plan;
  const allNodes = a.nodes.length > 0 && a.nodes.every((n) => p.nodeIds.includes(n.id));
  const flipNode = (id: string) => a.set({ nodeIds: p.nodeIds.includes(id) ? p.nodeIds.filter((x) => x !== id) : [...p.nodeIds, id] });
  const flipGroup = (id: string) => a.set({ groupIds: p.groupIds.includes(id) ? p.groupIds.filter((x) => x !== id) : [...p.groupIds, id], groupsOn: true });
  // Let's Encrypt on a node whose address is an IP cannot work: said before the click, with the two ways out
  const tls = getAt(settings, ["tls_mode"]);
  const sni = getAt(settings, ["sni"]);
  const ipNodes = tls === "acme_domain" && !sni ? a.nodes.filter((n) => p.nodeIds.includes(n.id) && isIp(n.address)) : [];
  const app = appWords(t, info, linkApps);
  const proto = info.displayName;

  return (
    <Card lg className="flex flex-col gap-1 px-4 pt-3 pb-3.5">
      <SectionLabel icon="sparkle" tone="mint" className="pb-1">
        {t("profiles.after")}
      </SectionLabel>

      <label className="flex min-h-11 cursor-pointer items-center gap-3 border-t border-line">
        <span className="flex-1 text-[13px] font-bold">{t("profiles.afterNodes")}</span>
        <Switch checked={p.nodesOn} disabled={a.nodes.length === 0} onCheckedChange={(on) => a.set({ nodesOn: on })} />
      </label>
      {a.nodes.length === 0 ? (
        <p className="pb-2 text-xs leading-snug text-muted">{t("profiles.afterNoNodes")}</p>
      ) : (
        p.nodesOn && (
          <div className="flex flex-col gap-2 pb-2">
            <div className="flex flex-wrap gap-1.5">
              {a.nodes.length > 1 && (
                <PillToggle on={allNodes} onClick={() => a.set({ nodeIds: allNodes ? [] : a.nodes.map((n) => n.id) })}>
                  {t("profiles.afterAllNodes")}
                  <span className="font-mono text-[11px] font-semibold text-muted">{a.nodes.length}</span>
                </PillToggle>
              )}
              {a.nodes.map((n) => (
                <PillToggle key={n.id} mono on={p.nodeIds.includes(n.id)} onClick={() => flipNode(n.id)}>
                  {n.countryCode && <Flag code={n.countryCode} size={11} />}
                  {n.name}
                </PillToggle>
              ))}
            </div>
            {ipNodes.length > 0 && <p className="text-xs leading-snug text-warn-text">{t("profiles.afterIp", { nodes: ipNodes.map((n) => n.name).join(", ") })}</p>}
          </div>
        )
      )}

      <label className="flex min-h-11 cursor-pointer items-center gap-3 border-t border-line">
        <span className="flex-1 text-[13px] font-bold">{t("profiles.afterGroups")}</span>
        <Switch checked={p.groupsOn} onCheckedChange={(on) => a.set({ groupsOn: on })} />
      </label>
      {p.groupsOn &&
        (a.groups.length === 0 ? (
          <div className="flex flex-col gap-2">
            <p className="text-xs leading-snug text-muted">{t("profiles.afterNoGroups")}</p>
            <div>
              <PillToggle on={p.everyone} onClick={() => a.set({ everyone: !p.everyone })}>
                {t("profiles.afterEveryone", { name: t("users.everyone") })}
              </PillToggle>
            </div>
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            <div className="flex flex-wrap gap-1.5">
              {a.groups.map((g) => (
                <PillToggle key={g.id} on={p.groupIds.includes(g.id)} onClick={() => flipGroup(g.id)}>
                  {g.name}
                </PillToggle>
              ))}
            </div>
            {!a.groupsTouched && (
              <div className="flex flex-col gap-1 text-xs leading-snug text-muted">
                {a.lacking.length > 0 && <p>{t.n("profiles.afterFirst", a.lacking.length, { groups: quoted(a.lacking), proto, app })}</p>}
                {a.having.length > 0 && <p>{t.n("profiles.afterSecond", a.having.length, { groups: quoted(a.having), proto })}</p>}
              </div>
            )}
          </div>
        ))}
    </Card>
  );
}
