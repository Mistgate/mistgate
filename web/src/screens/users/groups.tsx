import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { AccessImpact } from "@/gen/mistgate/admin/v1/user_pb";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon, IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { meQuery } from "@/lib/session";
import { presetName } from "@/screens/subscriptions/model";
import { dnsPresetsQuery, useLinkAppNames } from "@/screens/subscriptions/queries";
import { DnsSelect, useInheritedDns } from "./dns-select";
import { groupsQuery, profileListQuery, protocolsQuery } from "./rpc";
import { useTx } from "./t";
import { ConfirmModal } from "./ui";
import { Pending, QueryError } from "@/components/ui/query-error";

// The Groups tab of the users screen and the pieces every screen shows about a group: what it gives (the two ways,
// "Подписка" and "Ключ AmneziaVPN"), its profiles as links, the forms to make, edit and delete one.

/** A 30px pill that is on or off (node chips, profile chips of a group). */
export function PillToggle({ on, onClick, children, mono }: { on: boolean; onClick: () => void; children: ReactNode; mono?: boolean }) {
  return (
    <button
      type="button"
      aria-pressed={on}
      onClick={onClick}
      className={cx(
        "flex h-[30px] items-center gap-1.5 rounded-[15px] border px-2.5 text-xs font-bold transition-colors duration-200",
        mono && "font-mono",
        on ? "border-accent-line bg-accent-soft text-fg" : "border-line bg-transparent text-muted",
      )}
    >
      {children}
    </button>
  );
}

// ---- the two ways ----

/** How a person gets a profile: by the subscription link (in any app that takes it) or by an AmneziaVPN key per device. */
export type Way = "sub" | "awg";
export const wayMark: Record<Way, { icon: IconName; tone: Tone }> = { sub: { icon: "link", tone: "mint" }, awg: { icon: "key", tone: "sage" } };

type ProtocolApps = { id: string; apps: readonly App[] };

/** The way of a protocol: the one whose app takes it (a protocol only AmneziaVPN takes is a key; anything else rides the link). */
export function wayOf(protocol: string, protocols: readonly ProtocolApps[] | undefined): Way {
  const apps = protocols?.find((p) => p.id === protocol)?.apps;
  if (apps) return apps.includes(App.AMNEZIA) && !apps.includes(App.HAPP) ? "awg" : "sub";
  return protocol === "awg" ? "awg" : "sub";
}

/** A profile, named, as a link to its page. */
export function ProfileLink({ id, name, className }: { id: string; name: string; className?: string }) {
  return (
    <Link
      to="/profiles/$id"
      params={{ id }}
      className={cx(
        "inline-flex h-[26px] max-w-full items-center gap-1 rounded-ctl border border-line bg-surface-2 px-2 font-mono text-[11px] font-semibold text-fg transition-colors hover:border-accent-line hover:bg-accent-soft focus-visible:border-accent-line",
        className,
      )}
    >
      <span className="truncate">{name}</span>
    </Link>
  );
}

/** A group, named, as a link: its row on the Groups tab, with its editor open. */
export function GroupLink({ id, name, className, children }: { id: string; name: string; className?: string; children?: ReactNode }) {
  const t = useTx();
  return (
    <Link to="/users" search={{ tab: "groups", group: id }} aria-label={t("users.groupOpen", { name })} className={cx("font-bold text-accent-text hover:underline", className)}>
      {children ?? name}
    </Link>
  );
}

/**
 * The profiles of a group sorted into the two ways, each a row with its tinted mark, its label (and, with `reach`, on how
 * many nodes it works now) and the profiles as links. A way without a profile says so, quietly.
 */
export function WaysOfGroup({ profiles, reach, compact }: { profiles: { id: string; name: string; protocol: string }[]; reach?: { sub: number; awg: number }; compact?: boolean }) {
  const t = useTx();
  const protocols = useQuery(protocolsQuery);
  const apps = useLinkAppNames();
  const of = (w: Way) => profiles.filter((p) => wayOf(p.protocol, protocols.data) === w);
  return (
    <div className={cx("flex flex-col", compact ? "gap-2.5" : "gap-3")}>
      {(["sub", "awg"] as const).map((w) => {
        const list = of(w);
        const n = reach?.[w];
        return (
          <div key={w} className="flex items-start gap-2.5">
            <IconChip icon={wayMark[w].icon} tone={wayMark[w].tone} size={22} className="mt-px" />
            <div className="flex min-w-0 flex-1 flex-col gap-1.5">
              <span className="text-xs leading-[22px] font-bold">
                {w === "sub" ? t("users.reachSub", { apps }) : t("users.reachAwg")}
                {n !== undefined && list.length > 0 && (
                  <span className={cx("font-semibold", n > 0 ? "text-muted" : "text-warn-text")}> · {n > 0 ? t.n("users.reachNodes", n) : t(w === "sub" ? "users.reachNoSub" : "users.reachNoAwg")}</span>
                )}
                {list.length === 0 && <span className="font-semibold text-faint"> · {t("where.dash")}</span>}
              </span>
              {list.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {list.map((p) => (
                    <ProfileLink key={p.id} id={p.id} name={p.name} />
                  ))}
                </div>
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

/**
 * "Gets": what a person of this group receives, by way, before anything is created. Nothing at all is a red line: a
 * friend must never open an empty page.
 */
export function ReachLine({ group, action }: { group: Group; action?: ReactNode }) {
  const t = useTx();
  const apps = useLinkAppNames();
  const nothing = group.happNodes === 0 && group.amneziaNodes === 0;
  const item = (w: Way, n: number) => (
    <li key={w} className="flex items-start gap-2 text-xs leading-snug">
      <span aria-hidden className={cx("mt-px grid size-4 flex-none place-items-center rounded-full text-[10px] font-extrabold", n > 0 ? "bg-accent-soft text-accent-text" : "bg-surface-2 text-muted")}>
        {n > 0 ? "✓" : "–"}
      </span>
      <span className={n > 0 ? "text-fg" : "text-muted"}>
        <b>{w === "sub" ? t("users.reachSub", { apps }) : t("users.reachAwg")}</b> — {n > 0 ? t.n("users.reachNodes", n) : `${t("users.reachNo")}: ${t(w === "sub" ? "users.reachNoSub" : "users.reachNoAwg")}`}
      </span>
    </li>
  );
  return (
    <div className="flex flex-col gap-2 py-2.5">
      <div className="flex items-center gap-2">
        <SectionLabel as="span" className="flex-1">
          {t("users.reach")}
        </SectionLabel>
        {action}
      </div>
      <ul className="flex flex-col gap-1.5">{[item("sub", group.happNodes), item("awg", group.amneziaNodes)]}</ul>
      {nothing && <Notice tone="danger">{t("users.reachNothing", { group: group.name })}</Notice>}
    </div>
  );
}

/** Lost and gained profiles of a group change, in words: "Marina loses: «AWG 3.1» (2 AmneziaVPN keys stop working)". */
export function ImpactText({ impact, who }: { impact: AccessImpact; who?: string }) {
  const t = useTx();
  const name = (p: AccessImpact["lost"][number]) => `«${p.name}»${p.awgDevices > 0 ? ` (${t.n("users.impactKeys", p.awgDevices)})` : ""}`;
  return (
    <span className="flex flex-col gap-1">
      {impact.lost.length > 0 && (
        <span>
          <b>{who ? t("users.impactUser", { name: who }) : t.n("users.impactGroup", impact.users)}</b> {impact.lost.map(name).join(", ")}
        </span>
      )}
      {impact.gained.length > 0 && (
        <span>
          <b>{who ? t("users.impactGains") : t("users.impactGainsGroup")}</b> {impact.gained.map((p) => `«${p.name}»`).join(", ")}
        </span>
      )}
    </span>
  );
}

// ---- the tab ----

const canManage = (role: Role | undefined) => role === Role.OWNER || role === Role.HELPER;

/** /users?tab=groups: every group with what it gives and to how many people, and the ways to make, change and delete one. */
export function GroupsTab({ creating, onCreatingChange, focus, onFocusDone }: { creating: boolean; onCreatingChange: (o: boolean) => void; focus?: string; onFocusDone?: () => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const list = useQuery(groupsQuery);
  const profiles = useQuery(profileListQuery);
  const presets = useQuery(dnsPresetsQuery);
  const manage = canManage(useQuery(meQuery).data?.admin?.role);
  const [editId, setEditId] = useState("");
  const [deleting, setDeleting] = useState<Group | null>(null);
  // "?group=<id>" opens that group's editor once
  const [seenFocus, setSeenFocus] = useState("");
  if (focus && focus !== seenFocus && list.data?.some((g) => g.id === focus)) {
    setSeenFocus(focus);
    setEditId(focus);
  }
  const everyone = useMutation({
    mutationFn: () => groupsApi.createGroup({ name: t("users.everyone"), profileIds: (profiles.data ?? []).map((p) => p.id) }),
    onSuccess: async (r) => {
      await Promise.all(["groups", "users", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("users.everyoneCreated", { name: r.group?.name ?? t("users.everyone") }));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  if (list.isPending) return <Pending />;
  if (list.isError) return <QueryError error={list.error} onRetry={() => void list.refetch()} />;
  const profileOf = (id: string) => profiles.data?.find((p) => p.id === id);
  const dnsName = (id: string) => {
    const p = presets.data?.presets.find((x) => x.id === id);
    return p ? presetName(p, t.lang) : "";
  };

  return (
    <div className="flex flex-col gap-2.5">
      {list.data.length === 0 ? (
        <Card lg className="border-dashed">
          <EmptyState
            title={t("users.groupsNone")}
            action={
              manage && (
                <div className="flex flex-wrap justify-center gap-2">
                  <Button variant="primary" size="lg" disabled={everyone.isPending} onClick={() => everyone.mutate()}>
                    {t("users.everyoneCreate")}
                  </Button>
                  <Button variant="secondary" size="lg" onClick={() => onCreatingChange(true)}>
                    {t("users.newGroup")}
                  </Button>
                </div>
              )
            }
          >
            {t("users.groupsNoneBody")}
          </EmptyState>
        </Card>
      ) : (
        list.data.map((g) => {
          const ps = g.profileIds.map(profileOf).filter((p) => p !== undefined);
          return (
            <Card key={g.id} lg className={cx("flex flex-col gap-3 p-4 transition-colors", focus === g.id && "border-accent-line")}>
              <div className="flex flex-wrap items-center gap-x-3 gap-y-2.5">
                <div className="flex min-w-0 flex-[1_1_12rem] items-center gap-3">
                  <IconChip icon="family" tone="lavender" size={28} />
                  <b className="min-w-0 text-[15px] tracking-[-0.01em] break-words">{g.name}</b>
                </div>
                <div className="flex items-center gap-1.5">
                  <Link
                    to="/users"
                    search={{ group: g.id }}
                    aria-label={t("where.usersOfGroup", { name: g.name })}
                    className="inline-flex h-7 items-center gap-1 rounded-[14px] border border-line px-2.5 text-xs font-bold whitespace-nowrap transition-colors hover:border-accent-line hover:bg-accent-soft"
                  >
                    {t.n("users.groupPeople", g.userCount)}
                    <Icon name="chevronRight" size={12} className="text-muted" />
                  </Link>
                  {manage && (
                    <>
                      <Button size="sm" onClick={() => setEditId(g.id)}>
                        {t("common.edit")}
                      </Button>
                      <Button variant="ghostDanger" size="sm" onClick={() => setDeleting(g)}>
                        {t("users.delete")}
                      </Button>
                    </>
                  )}
                </div>
              </div>
              {ps.length === 0 ? (
                <p className="text-xs text-warn-text">{t("users.groupNoProfilesRow")}</p>
              ) : (
                <WaysOfGroup profiles={ps} reach={{ sub: g.happNodes, awg: g.amneziaNodes }} />
              )}
              {g.dnsPresetId && dnsName(g.dnsPresetId) && <span className="text-[11px] text-muted">{t("users.groupDns", { name: dnsName(g.dnsPresetId) })}</span>}
            </Card>
          );
        })
      )}
      <NewGroupModal open={creating} onOpenChange={onCreatingChange} />
      <GroupEditModal
        group={list.data.find((g) => g.id === editId)}
        open={editId !== ""}
        onOpenChange={(o) => {
          if (o) return;
          setEditId("");
          onFocusDone?.();
        }}
      />
      <DeleteGroupModal group={deleting} groups={list.data} onClose={() => setDeleting(null)} />
    </div>
  );
}

/** "New group": a name, the profiles (all of them to begin with), the DNS preset under "More". */
export function NewGroupModal({ open, onOpenChange, initialProfiles, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; initialProfiles?: string[]; onCreated?: (g: Group) => void }) {
  const t = useTx();
  const toast = useToast();
  const profiles = useQuery(profileListQuery);
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={t("users.newGroup")} closeLabel={t("common.close")}>
      <GroupForm
        first={false}
        initialProfiles={initialProfiles}
        profiles={(profiles.data ?? []).map((p) => ({ id: p.id, name: p.name }))}
        onCancel={() => onOpenChange(false)}
        onCreated={(g) => {
          onOpenChange(false);
          toast(t("users.groupCreated", { name: g.name }));
          onCreated?.(g);
        }}
      />
    </Modal>
  );
}

/** The edit-group modal (Groups tab, user card, profile page): the same form, prefilled, saved with UpdateGroup. Mounted only while open. */
export function GroupEditModal({ group, open, onOpenChange }: { group: Group | undefined; open: boolean; onOpenChange: (o: boolean) => void }) {
  const t = useTx();
  const toast = useToast();
  const profileList = useQuery(profileListQuery);
  return (
    <Modal open={open && !!group} onOpenChange={onOpenChange} title={t("users.groupEdit")} closeLabel={t("common.close")}>
      {group && (
        <div className="flex flex-col gap-1">
          <p className="text-xs leading-normal text-muted">{t.n("users.groupEditUsers", group.userCount)}</p>
          <GroupForm
            group={group}
            first={false}
            profiles={(profileList.data ?? []).map((p) => ({ id: p.id, name: p.name }))}
            onCancel={() => onOpenChange(false)}
            onCreated={(g) => {
              onOpenChange(false);
              toast(t("users.groupSaved", { name: g.name }));
            }}
          />
        </div>
      )}
    </Modal>
  );
}

/**
 * A group's name and profiles (all of them for a new one, unless `initialProfiles`); `dns` adds the DNS preset under "More".
 * With `group` it edits that group. Taking a profile away from a group with people asks first: the server's dry run names
 * what they lose (and the AmneziaVPN keys that stop working), and the red button does it.
 */
export function GroupForm({
  group,
  first,
  profiles,
  initialProfiles,
  dns = true,
  quiet,
  onCancel,
  onCreated,
}: {
  group?: Group;
  first: boolean;
  profiles: { id: string; name: string }[];
  initialProfiles?: string[];
  dns?: boolean;
  /** The inline form inside "New user": its button is not the form's main action. */
  quiet?: boolean;
  onCancel: () => void;
  onCreated: (g: Group) => void;
}) {
  const t = useTx();
  const qc = useQueryClient();
  const [name, setName] = useState(group?.name ?? "");
  const [chosen, setChosen] = useState<string[] | null>(group ? [...group.profileIds] : initialProfiles ? [...initialProfiles] : null); // null = every profile
  const [dnsPreset, setDnsPreset] = useState(group?.dnsPresetId ?? ""); // "" = the instance default
  const inheritedDns = useInheritedDns();
  const ids = chosen ?? profiles.map((p) => p.id);
  const [error, setError] = useState<string | null>(null);
  const [impact, setImpact] = useState<AccessImpact | null>(null);
  const lost = group ? group.profileIds.filter((id) => !ids.includes(id)) : [];
  const save = useMutation({
    mutationFn: async (confirmed: boolean) => {
      if (!group) return (await groupsApi.createGroup({ name: name.trim(), profileIds: ids, dnsPresetId: dnsPreset })).group;
      const req = { groupId: group.id, name: name.trim(), profileIds: { values: ids }, dnsPresetId: dnsPreset };
      if (!confirmed && group.userCount > 0 && lost.length > 0) {
        const dry = await groupsApi.updateGroup({ ...req, dryRun: true });
        if (dry.impact && dry.impact.lost.length > 0) {
          setImpact(dry.impact);
          return null;
        }
      }
      return (await groupsApi.updateGroup(req)).group;
    },
    onSuccess: async (res) => {
      if (!res) return;
      // a group change reaches its users' cards, devices and subscription previews, and the profiles' "where it runs"
      await Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      onCreated(res);
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const submit = (confirmed = false) => {
    if (!name.trim() || save.isPending) return;
    setError(null);
    save.mutate(confirmed);
  };
  return (
    <div className={cx("flex flex-col gap-3 py-3", quiet && "border-t border-line")}>
      {first && <p className="text-xs leading-normal text-muted">{t("users.groupFirst")}</p>}
      <TextField
        icon="tag"
        tone="lavender"
        label={t("users.groupName")}
        placeholder={t("users.groupNamePh")}
        value={name}
        onChange={(e) => setName(e.target.value)}
        // Enter here saves the group; it must not submit a user form around this one
        onKeyDown={(e) => {
          if (e.key !== "Enter") return;
          e.preventDefault();
          submit();
        }}
        autoComplete="off"
        maxLength={64}
      />
      <div className="flex flex-col gap-1.5">
        <SectionLabel icon="sliders" tone="sage">
          {t("users.groupProfiles")}
        </SectionLabel>
        {profiles.length === 0 ? (
          <p className="text-xs leading-normal text-muted">{t("users.groupNoProfiles")}</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {profiles.map((p) => (
              <PillToggle
                key={p.id}
                on={ids.includes(p.id)}
                onClick={() => {
                  setImpact(null);
                  setChosen(ids.includes(p.id) ? ids.filter((x) => x !== p.id) : [...ids, p.id]);
                }}
              >
                {p.name}
              </PillToggle>
            ))}
          </div>
        )}
      </div>
      {dns && (
        <details className="group" open={!!group?.dnsPresetId}>
          <summary className="flex w-fit cursor-pointer items-center gap-1.5 text-xs font-bold text-muted select-none hover:text-fg [&::-webkit-details-marker]:hidden">
            <Icon name="chevronRight" size={12} className="transition-transform duration-200 group-open:rotate-90" />
            {t("users.groupMore")}
          </summary>
          <div className="flex flex-col gap-1.5 pt-2.5">
            <SectionLabel icon="dns" tone="sky">
              {t("subs.dns.field")}
            </SectionLabel>
            <DnsSelect value={dnsPreset} inherited={inheritedDns} onChange={setDnsPreset} scope="group" />
            <span className="text-[11px] leading-snug text-muted">{t("subs.dns.inheritGroupHint")}</span>
          </div>
        </details>
      )}
      {impact && (
        <Notice className="items-start">
          <ImpactText impact={impact} />
        </Notice>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
      <div className="flex justify-end gap-2">
        {!first && (
          <Button variant="ghost" onClick={onCancel}>
            {t("users.cancel")}
          </Button>
        )}
        {impact ? (
          <Button variant="danger" disabled={save.isPending} onClick={() => submit(true)}>
            {t.n("users.groupLoseDo", impact.users)}
          </Button>
        ) : (
          <Button variant={quiet ? "secondary" : "primary"} disabled={!name.trim() || save.isPending} onClick={() => submit()}>
            {group ? t("common.save") : quiet ? t("users.groupAdd") : t("users.groupCreate")}
          </Button>
        )}
      </div>
    </div>
  );
}

/**
 * "Delete group". An empty one asks and goes. One with people asks where they go (another group, "Everyone" first) and
 * moves them in the same call; without another group there is nowhere to go, so it says that instead.
 */
function DeleteGroupModal({ group, groups, onClose }: { group: Group | null; groups: Group[]; onClose: () => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const [last, setLast] = useState(group);
  if (group && group !== last) setLast(group);
  const g = group ?? last;
  const others = groups.filter((x) => x.id !== g?.id);
  const preferred = others.find((x) => isEveryone(x.name)) ?? others[0];
  const [target, setTarget] = useState("");
  const to = others.find((x) => x.id === target) ?? preferred;
  const people = g?.userCount ?? 0;
  const [error, setError] = useState<string | null>(null);

  async function remove() {
    if (!g) return;
    setError(null);
    try {
      await groupsApi.deleteGroup({ groupId: g.id, moveUsersTo: people > 0 ? (to?.id ?? "") : "" });
    } catch (e) {
      setError(errorText(e, t));
      throw e;
    }
    await Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    toast(people > 0 && to ? t("users.groupMovedDeleted", { name: g.name, to: to.name }) : t("users.groupDeleted", { name: g.name }));
  }

  return (
    <ConfirmModal
      open={group !== null}
      onOpenChange={(o) => {
        if (o) return;
        setError(null);
        setTarget("");
        onClose();
      }}
      title={t("users.groupDeleteT", { name: g?.name ?? "" })}
      description={people === 0 ? t("users.groupDeleteBody") : undefined}
      confirmLabel={people > 0 ? t.n("users.groupDeleteMoveDo", people) : t("users.groupDelete")}
      confirmDisabled={people > 0 && !to}
      danger
      onConfirm={remove}
    >
      {people > 0 &&
        (to ? (
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] font-bold">{t.n("users.groupDeleteMove", people)}</span>
            <Select aria-label={t("users.group")} value={to.id} onValueChange={setTarget} options={others.map((x) => ({ value: x.id, label: x.name }))} />
          </div>
        ) : (
          <Notice>{t("users.groupDeleteNoTarget")}</Notice>
        ))}
      {error && <Notice tone="danger">{error}</Notice>}
    </ConfirmModal>
  );
}

/** The default group: the one setup makes, in either language. */
export const isEveryone = (name: string) => ["все", "everyone"].includes(name.trim().toLowerCase());

/** The group a new user lands in: "Everyone" when there is one, else the first. */
export const defaultGroup = (groups: readonly Group[]) => groups.find((g) => isEveryone(g.name)) ?? groups[0];
