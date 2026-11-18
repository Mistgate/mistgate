import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { AccessImpact } from "@/gen/mistgate/admin/v1/user_pb";
import { useTx } from "./t";

// The Groups tab of the users screen and the pieces every screen shows about a group: what it gives (the two ways,
// "Подписка" and "Ключ AmneziaVPN"), its profiles as links. The windows to make and edit one are users-group-new.tsx.

// ---- the two ways ----

/** How a person gets a profile: by the subscription link (in any app that takes it) or by an AmneziaVPN key per device. */
export type Way = "sub" | "awg";

type ProtocolApps = { id: string; apps: readonly App[] };

/** The way of a protocol: the one whose app takes it (a protocol only AmneziaVPN takes is a key; anything else rides the link). */
export function wayOf(protocol: string, protocols: readonly ProtocolApps[] | undefined): Way {
  const apps = protocols?.find((p) => p.id === protocol)?.apps;
  if (apps) return apps.includes(App.AMNEZIA) && !apps.includes(App.HAPP) ? "awg" : "sub";
  return protocol === "awg" ? "awg" : "sub";
}

/** Lost and gained profiles of a group change, in words: "alice loses: “AWG 3.1” (2 AmneziaVPN keys stop working)". */
export function ImpactText({ impact, who }: { impact: AccessImpact; who?: string }) {
  const t = useTx();
  const name = (p: AccessImpact["lost"][number]) => `${t("common.quoted", { name: p.name })}${p.awgDevices > 0 ? ` (${t.n("users.impactKeys", p.awgDevices)})` : ""}`;
  return (
    <span className="flex flex-col gap-1">
      {impact.lost.length > 0 && (
        <span>
          <b>{who ? t("users.impactUser", { name: who }) : t.n("users.impactGroup", impact.users)}</b> {impact.lost.map(name).join(", ")}
        </span>
      )}
      {impact.gained.length > 0 && (
        <span>
          <b>{who ? t("users.impactGains") : t("users.impactGainsGroup")}</b> {impact.gained.map((p) => t("common.quoted", { name: p.name })).join(", ")}
        </span>
      )}
    </span>
  );
}

// ---- the tab ----

// The Groups tab (cards, the tile, the search), "New group" and who may manage are the redesign's: users-groups.tsx, users-group-new.tsx.
export { canManage, GroupsTab } from "./users-groups";
export { NewGroupModal } from "./users-group-new";

// "Edit group" is the same window as "New group", with the group in it.
export { GroupEditModal, type GroupEditDemo } from "./users-group-new";

/** The default group: the one setup makes, in either language. */
export const isEveryone = (name: string) => ["все", "everyone"].includes(name.trim().toLowerCase());

/** The group a new user lands in: "Everyone" when there is one, else the first. */
export const defaultGroup = (groups: readonly Group[]) => groups.find((g) => isEveryone(g.name)) ?? groups[0];
