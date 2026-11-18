import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Notice } from "@/components/ui/notice";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { AccessImpact } from "@/gen/mistgate/admin/v1/user_pb";
import { users as usersApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { ImpactText } from "./groups";
import type { UserN } from "./model";
import { useTx } from "./t";
import { ConfirmModal } from "./ui";

// A change of one person's groups, asked and done the same way on the person's page and in the People tab of a group: the server's
// dry run says what the person gets and loses (profiles and nodes), the window shows it, the button does it. A person has one MAIN group
// (it decides the DNS, the Mihomo template and the colour) and any number of EXTRA ones (they only add servers):
//   add     the group becomes an extra one: nothing is lost
//   remove  the extra group goes: what only it gave is lost
//   main    the group becomes the main one; when it was an extra one the old main group stays as an extra one (a swap, nothing is lost),
//           when it was not, the old main group is left

export type MemberKind = "main" | "add" | "remove";
type Who = Pick<UserN, "id" | "name" | "groupId" | "extraGroupIds">;

/** The UpdateUser fields of a change. */
export function changeRequest(u: Pick<Who, "groupId" | "extraGroupIds">, kind: MemberKind, groupId: string): { groupId?: string; addGroupIds?: string[]; removeGroupIds?: string[] } {
  if (kind === "add") return { addGroupIds: [groupId] };
  if (kind === "remove") return { removeGroupIds: [groupId] };
  return u.extraGroupIds.includes(groupId) ? { groupId, addGroupIds: [u.groupId] } : { groupId };
}

/** The ids a person is in (the main group first), for lists that must not offer a group twice. */
export const groupIdsOf = (u: Pick<Who, "groupId" | "extraGroupIds">) => [u.groupId, ...u.extraGroupIds];

type Asked = { who: Who; kind: MemberKind; group: Pick<Group, "id" | "name">; impact: AccessImpact | null };

/** What the dev kit's scenes open the window with (there is no panel to ask for the dry run): the kind, the group and the impact. */
export type ChangeDemo = Omit<Asked, "who">;
/** The scene of the person's page names its window here before the page draws (the page reads it once). */
export const changeDemo: { current: ChangeDemo | null } = { current: null };

/**
 * `ask(person, kind, group)` asks the server what the change does and opens the window; `modal` goes into the page. `after` runs once the
 * change is made (refresh what the page shows). `nodeName` words the node ids of the impact; without it the server's ids are shown.
 */
export function useGroupChange(after: () => Promise<unknown> | void, nodeName?: (id: string) => string): { ask: (who: Who, kind: MemberKind, group: Pick<Group, "id" | "name">) => Promise<void>; show: (a: Asked) => void; modal: ReactNode } {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const nodes = useQuery({ ...nodesQuery, enabled: !nodeName }).data?.nodes;
  const names = (id: string) => nodeName?.(id) ?? nodes?.find((n) => n.id === id)?.name ?? id;
  // kept after the window closes so its text does not go blank while it fades out
  const [asked, setAsked] = useState<Asked | null>(null);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function ask(who: Who, kind: MemberKind, group: Pick<Group, "id" | "name">) {
    try {
      const r = await usersApi.updateUser({ userId: who.id, ...changeRequest(who, kind, group.id), dryRun: true });
      setError(null);
      setAsked({ who, kind, group, impact: r.impact ?? null });
      setOpen(true);
    } catch (e) {
      toast.error(errorText(e, t));
    }
  }
  async function change() {
    if (!asked) return;
    setError(null);
    try {
      await usersApi.updateUser({ userId: asked.who.id, ...changeRequest(asked.who, asked.kind, asked.group.id) });
    } catch (e) {
      setError(errorText(e, t));
      throw e;
    }
    await Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    await after();
    const vars = { name: asked.who.name, group: asked.group.name };
    toast(t(asked.kind === "add" ? "users.groupAdded" : asked.kind === "remove" ? "users.groupRemoved" : swap ? "users.groupMain" : "users.groupChanged", vars));
  }

  const kind = asked?.kind ?? "main";
  // a group that is an extra one becomes the main one and the old main group stays; any other replaces it, as it always did
  const swap = kind === "main" && !!asked?.who.extraGroupIds.includes(asked.group.id);
  const imp = asked?.impact;
  const hurts = (imp?.lost.length ?? 0) + (imp?.lostNodeIds.length ?? 0) > 0;
  const vars = { name: asked?.who.name ?? "", group: asked?.group.name ?? "" };
  const modal = (
    <ConfirmModal
      open={open}
      onOpenChange={setOpen}
      title={t(kind === "add" ? "users.groupAddT" : kind === "remove" ? "users.groupRemoveT" : swap ? "users.groupMainT" : "users.groupChangeT", vars)}
      confirmLabel={t(kind === "add" ? "users.groupAddDo" : kind === "remove" ? "users.groupRemoveDo" : swap ? "users.groupMainDo" : "users.groupChangeDo")}
      danger={hurts}
      onConfirm={change}
    >
      {imp ? (
        <Notice tone={hurts ? "warn" : undefined} className="items-start">
          <ImpactText impact={imp} who={vars.name} nodeName={names} />
        </Notice>
      ) : (
        <p className="text-[13px] leading-normal text-muted">{kind === "main" ? t("users.impactSame") : t("users.impactNone", { name: vars.name })}</p>
      )}
      {error && <Notice tone="danger">{error}</Notice>}
    </ConfirmModal>
  );
  // the dev kit's scenes: the window as the dry run would have filled it
  const show = (a: Asked) => {
    setAsked(a);
    setOpen(true);
  };
  return { ask, show, modal };
}
