import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, type Inbound } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { meQuery } from "@/lib/session";
import { InboundErrorNote, states } from "@/screens/node/profiles";
import { GroupEditModal, GroupLink, NewGroupModal } from "@/screens/users/groups";
import { groupsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";
import { Panel } from "@/screens/users/ui";
import { DeployDialog } from "./deploy";
import { TwinPanel, type Egress } from "./twin";

type Verdict = "ok" | "none" | "noNode" | "noGroup" | "notRunning";

/** What the profile is doing, from the facts the page already has: where it is deployed and which groups carry it. */
export function whereOf(inbounds: Inbound[], groups: Group[], profile: Pick<ProfileSummary, "id" | "userCount">) {
  const inGroups = groups.filter((g) => g.profileIds.includes(profile.id));
  const nodes = new Set(inbounds.map((i) => i.nodeId)).size;
  const active = inbounds.filter((i) => i.state === InboundState.ACTIVE).length;
  const failing = inbounds.filter((i) => i.state === InboundState.FAILED).length;
  const verdict: Verdict =
    nodes === 0 && inGroups.length === 0 ? "none" : nodes === 0 ? "noNode" : inGroups.length === 0 ? "noGroup" : active === 0 ? "notRunning" : "ok";
  return { inGroups, nodes, failing, users: profile.userCount, verdict };
}

/**
 * "Where it runs" on the profile page: a one-line verdict with the action that fixes what is missing, then nodes, groups
 * and users side by side (stacked on the phone), each with its count and one quiet "+". Names are links. The WARP twin is
 * the last line of the block. `cert` lets "Put on nodes" ask for a domain before the click where Let's Encrypt cannot work.
 */
export function WherePanel({
  profile,
  inbounds,
  cert,
  twin,
}: {
  profile: ProfileSummary;
  inbounds: Inbound[];
  cert?: { tlsMode?: string; sni?: string };
  twin?: { egress: Egress; dirty: boolean };
}) {
  const t = useTx();
  const groupList = useQuery(groupsQuery);
  const role = useQuery(meQuery).data?.admin?.role;
  const canGroups = role === Role.OWNER || role === Role.HELPER;
  const canNodes = role === Role.OWNER; // policy.go: an inbound is infrastructure, a group is day-to-day
  const [pickNode, setPickNode] = useState(false);
  const [pickGroup, setPickGroup] = useState(false);
  const [editId, setEditId] = useState("");
  const [editOpen, setEditOpen] = useState(false);

  const groups = groupList.data ?? [];
  const w = whereOf(inbounds, groups, profile);
  const awg = profile.protocol === "awg";
  const sum = w.inGroups.reduce((n, g) => n + g.userCount, 0);

  const addNode = canNodes && (
    <Button variant="primary" size="sm" onClick={() => setPickNode(true)}>
      {t("where.addNode")}
    </Button>
  );
  const addGroup = canGroups && (
    <Button variant={w.verdict === "none" && canNodes ? "secondary" : "primary"} size="sm" onClick={() => setPickGroup(true)}>
      {t("where.addGroup")}
    </Button>
  );

  const parts = [t.n("where.nodesN", w.nodes), t.n("where.groupsN", w.inGroups.length), t.n("where.usersN", w.users)].join(", ");
  const line: { kind: StatusKind; title: string; body?: string; actions?: ReactNode } = {
    ok: { kind: w.failing > 0 ? ("warn" as const) : ("ok" as const), title: t("where.ok", { parts }), body: w.failing > 0 ? t("where.failBody", { n: w.failing, m: w.nodes }) : undefined },
    none: { kind: "warn" as const, title: t("where.none"), body: `${t("where.noNodeBody")} ${t("where.noGroupBody")}`, actions: (addNode || addGroup) && <>{addNode}{addGroup}</> },
    noNode: { kind: "warn" as const, title: t("where.noNode"), body: t("where.noNodeBody"), actions: addNode },
    noGroup: { kind: "warn" as const, title: t("where.noGroup"), body: t("where.noGroupBody"), actions: addGroup },
    notRunning: { kind: w.failing > 0 ? ("bad" as const) : ("warn" as const), title: t("where.failedAll"), body: w.failing > 0 ? t("where.failBody", { n: w.failing, m: w.nodes }) : undefined },
  }[w.verdict];

  return (
    <Panel title={t("where.title")} icon="network" tone="sage">
      {groupList.isPending ? (
        <p className="text-sm text-muted">{t("common.loading")}</p>
      ) : groupList.isError ? (
        <Notice tone="danger">{errorText(groupList.error, t)}</Notice>
      ) : (
        <>
          <StatusLine kind={line.kind} title={line.title} actions={line.actions || undefined}>
            {line.body}
          </StatusLine>

          <div className="grid gap-2.5 md:grid-cols-3">
            <Column icon="server" tone="sky" title={t("where.nodes")} count={w.nodes} add={canNodes ? { label: t("where.addNode"), onClick: () => setPickNode(true) } : undefined}>
              {inbounds.length === 0 ? (
                <Empty>{t("where.dash")}</Empty>
              ) : (
                inbounds.map((i) => {
                  const s = states[i.state] ?? states[InboundState.UNSPECIFIED];
                  return (
                    <li key={i.id} className="flex flex-col gap-1.5 py-1.5">
                      <div className="flex min-w-0 items-center gap-2">
                        <Link to="/nodes/$id" params={{ id: i.nodeId }} search={{ tab: "profiles" }} className="min-w-0 truncate text-[13px] font-bold text-accent-text hover:underline">
                          {i.nodeName}
                        </Link>
                        {i.port > 0 && <span className="font-mono text-[11px] text-muted">:{i.port}</span>}
                        <span className="ml-auto flex-none">
                          <StatusPill kind={s.kind} label={t(s.word)} sm />
                        </span>
                      </div>
                      {i.lastError && <InboundErrorNote inbound={i} />}
                    </li>
                  );
                })
              )}
            </Column>

            <Column icon="family" tone="lavender" title={t("where.groups")} count={w.inGroups.length} add={canGroups ? { label: t("where.addGroup"), onClick: () => setPickGroup(true) } : undefined}>
              {w.inGroups.length === 0 ? (
                <Empty>{t("where.dash")}</Empty>
              ) : (
                w.inGroups.map((g) => (
                  <li key={g.id} className="flex min-h-[34px] items-center gap-2 py-1">
                    <GroupLink id={g.id} name={g.name} className="min-w-0 truncate text-[13px]" />
                    {canGroups && (
                      <button
                        type="button"
                        onClick={() => {
                          setEditId(g.id);
                          setEditOpen(true);
                        }}
                        className="ml-auto flex-none text-xs font-bold text-muted transition-colors hover:text-fg"
                      >
                        {t("common.edit")}
                      </button>
                    )}
                  </li>
                ))
              )}
            </Column>

            <Column icon="person" tone="mint" title={t("where.users")} count={w.users}>
              {w.inGroups.length === 0 ? (
                <Empty>{t("where.usersNone")}</Empty>
              ) : (
                w.inGroups.map((g) => (
                  <li key={g.id} className="py-0.5">
                    <Link
                      to="/users"
                      search={{ group: g.id }}
                      aria-label={t("where.usersOfGroup", { name: g.name })}
                      className="-mx-1.5 flex min-h-[30px] items-center gap-1.5 rounded-lg px-1.5 text-[13px] transition-colors hover:bg-surface-2"
                    >
                      <span className="min-w-0 truncate font-semibold">{g.name}</span>
                      <span className="flex-none text-muted">· {t.n("where.usersShort", g.userCount)}</span>
                      <Icon name="chevronRight" size={12} className="ml-auto flex-none text-muted" />
                    </Link>
                  </li>
                ))
              )}
              {w.inGroups.length > 0 && w.users < sum && <Note>{t("where.usersDiff")}</Note>}
              {awg && <Note>{t("where.awg")}</Note>}
            </Column>
          </div>

          {twin && <TwinPanel profile={profile} egress={twin.egress} dirty={twin.dirty} />}
        </>
      )}

      <DeployDialog open={pickNode} onOpenChange={setPickNode} profile={{ id: profile.id, name: profile.name, protocol: profile.protocol, tlsMode: cert?.tlsMode, sni: cert?.sni }} inbounds={inbounds} />
      <GroupPicker open={pickGroup} onOpenChange={setPickGroup} profile={profile} groups={groups} />
      <GroupEditModal group={groups.find((g) => g.id === editId)} open={editOpen} onOpenChange={setEditOpen} />
    </Panel>
  );
}

/** The verdict: a glyph in its status colour, a thin accent on the left, the words, and the fix right there. */
function StatusLine({ kind, title, children, actions }: { kind: StatusKind; title: string; children?: ReactNode; actions?: ReactNode }) {
  return (
    <section className={cx(`tone-${kind} screen-enter flex flex-wrap items-center gap-x-3 gap-y-2 rounded-field border border-line border-l-[3px] border-l-(color:--c) bg-surface-2 py-2.5 pr-2.5 pl-3`)}>
      <StatusPill kind={kind} glyphOnly />
      <div className="flex min-w-[200px] flex-1 flex-col gap-0.5">
        <h3 className="text-[13px] leading-snug font-extrabold">{title}</h3>
        {children && <p className="text-xs leading-snug text-pretty text-muted">{children}</p>}
      </div>
      {actions && <div className="flex flex-wrap gap-1.5">{actions}</div>}
    </section>
  );
}

/** One of the three: a small card with its mark, title, count and one quiet "+" in the header. */
function Column({ icon, tone, title, count, add, children }: { icon: IconName; tone: Tone; title: string; count: number; add?: { label: string; onClick: () => void }; children: ReactNode }) {
  return (
    <section className="flex min-w-0 flex-col rounded-field border border-line px-3 pt-2.5 pb-2">
      <div className="flex h-7 items-center gap-2">
        <IconChip icon={icon} tone={tone} size={22} />
        <h3 className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{title}</h3>
        <span className="font-mono text-xs font-bold text-fg">{count}</span>
        {add && (
          <IconButton variant="flat" aria-label={add.label} title={add.label} className="ml-auto" onClick={add.onClick}>
            <Icon name="plus" size={15} />
          </IconButton>
        )}
      </div>
      <ul className="mt-1 flex flex-col divide-y divide-line">{children}</ul>
    </section>
  );
}

const Empty = ({ children }: { children: ReactNode }) => <li className="py-1.5 text-xs text-muted">{children}</li>;
const Note = ({ children }: { children: ReactNode }) => <li className="py-1.5 text-[11px] leading-snug text-muted">{children}</li>;

/**
 * Which group: one click adds this profile to the group's set (UpdateGroup, the same call the group editor makes). When
 * there is nowhere to add it, the window offers a new group with this profile already in it.
 */
function GroupPicker({ open, onOpenChange, profile, groups }: { open: boolean; onOpenChange: (o: boolean) => void; profile: ProfileSummary; groups: Group[] }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const [making, setMaking] = useState(false);
  const free = groups.filter((g) => !g.profileIds.includes(profile.id));
  const add = useMutation({
    // The set is read from the cached list, so a concurrent edit of the same group in another tab is overwritten
    mutationFn: (g: Group) => groupsApi.updateGroup({ groupId: g.id, profileIds: { values: [...g.profileIds, profile.id] } }),
    onSuccess: async (_, g) => {
      // a group change reaches its users' cards, devices and subscription previews; "profiles" holds this page's counts
      await Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("where.addedToGroup", { group: g.name }));
      onOpenChange(false);
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (
    <>
      <Modal open={open} onOpenChange={onOpenChange} title={t("where.groupTitle", { name: profile.name })} description={t("where.groupBody")} closeLabel={t("common.close")}>
        {free.length === 0 ? (
          <div className="flex flex-col gap-3">
            <Notice>{t("where.noGroupLeft")}</Notice>
            <div className="flex justify-end">
              <Button
                variant="primary"
                size="md"
                onClick={() => {
                  onOpenChange(false);
                  setMaking(true);
                }}
              >
                <Icon name="plus" size={14} />
                {t("where.newGroupWith")}
              </Button>
            </div>
          </div>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {free.map((g) => (
              <li key={g.id} className="flex min-h-11 items-center gap-2.5 rounded-field border border-line py-1.5 pr-1.5 pl-3">
                <b className="min-w-0 flex-1 truncate text-[13px]">{g.name}</b>
                <span className="font-mono text-[11px] text-muted">{t.n("where.usersN", g.userCount)}</span>
                <Button variant="primary" size="sm" disabled={add.isPending} aria-label={`${t("where.add")}: ${g.name}`} onClick={() => add.mutate(g)}>
                  {t("where.add")}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </Modal>
      <NewGroupModal open={making} onOpenChange={setMaking} initialProfiles={[profile.id]} />
    </>
  );
}
