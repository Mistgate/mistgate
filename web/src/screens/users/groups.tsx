import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { AccessImpact } from "@/gen/mistgate/admin/v1/user_pb";
import type { ProfileFormat } from "@/gen/mistgate/admin/v1/profile_pb";
import { SectionLabel } from "@/components/ui/bits";
import { IconChip, type IconName, type Tone } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { cx } from "@/lib/cx";
import { FormatDrops } from "@/screens/profiles/formats";
import { useLinkAppList } from "@/screens/subscriptions/queries";
import { telegramOnly, trustTunnelOnly } from "@/screens/profiles/protocol-meta";
import { protocolsQuery } from "./rpc";
import { useTx } from "./t";

// The Groups tab of the users screen and the pieces every screen shows about a group: what it gives (the two ways,
// "Подписка" and "Ключ AmneziaVPN"), its profiles as links. The windows to make and edit one are users-group-new.tsx.

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

/** "Subscription (Happ, kl!ck)": the way by the link with the apps on it; just "Subscription" when the page lists none. */
function useSubLabel(): string {
  const t = useTx();
  const names = useLinkAppList();
  return names.length > 0 ? t("users.reachSub", { apps: names.join(", ") }) : t("users.reachSubPlain");
}

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

/**
 * The profiles of a group sorted into the two ways, each a row with its tinted mark, its label (and, with `reach`, on how
 * many nodes it works now) and the profiles as links. A way without a profile says so, quietly. With `drops` (the user
 * card) a link profile that some subscription format leaves out carries the yellow mark of it ("not in sing-box: XHTTP").
 */
export function WaysOfGroup({
  profiles,
  reach,
  compact,
  drops,
}: {
  profiles: { id: string; name: string; protocol: string; formats?: ProfileFormat[] }[];
  reach?: { sub: number; awg: number };
  compact?: boolean;
  drops?: boolean;
}) {
  const t = useTx();
  const protocols = useQuery(protocolsQuery);
  const sub = useSubLabel();
  // MTProto reaches people by its own tg:// links, never through the subscription: a row of its own, when the group has one
  const tgOnly = (p: { protocol: string }) => {
    const apps = protocols.data?.find((x) => x.id === p.protocol)?.apps;
    return apps ? telegramOnly(apps) : p.protocol === "mtproto";
  };
  // TrustTunnel too: its tt:// keys are the person's own, on their page, and not in the subscription
  const ttOnly = (p: { protocol: string }) => {
    const apps = protocols.data?.find((x) => x.id === p.protocol)?.apps;
    return apps ? trustTunnelOnly(apps) : p.protocol === "trusttunnel";
  };
  const of = (w: Way) => profiles.filter((p) => !tgOnly(p) && !ttOnly(p) && wayOf(p.protocol, protocols.data) === w);
  const tg = profiles.filter(tgOnly);
  const tt = profiles.filter(ttOnly);
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
                {w === "sub" ? sub : t("users.reachAwg")}
                {n !== undefined && list.length > 0 && (
                  <span className={cx("font-semibold", n > 0 ? "text-muted" : "text-warn-text")}> · {n > 0 ? t.n("users.reachNodes", n) : t(w === "sub" ? "users.reachNoSub" : "users.reachNoAwg")}</span>
                )}
                {list.length === 0 && <span className="font-semibold text-faint"> · {t("where.dash")}</span>}
              </span>
              {list.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {list.map((p) =>
                    drops && w === "sub" && p.formats?.some((f) => !f.ok) ? (
                      <span key={p.id} className="inline-flex max-w-full flex-wrap items-center gap-1">
                        <ProfileLink id={p.id} name={p.name} />
                        <FormatDrops formats={p.formats} t={t} />
                      </span>
                    ) : (
                      <ProfileLink key={p.id} id={p.id} name={p.name} />
                    ),
                  )}
                </div>
              )}
            </div>
          </div>
        );
      })}
      {tg.length > 0 && (
        <div className="flex items-start gap-2.5">
          <IconChip icon="plane" tone="rose" size={22} className="mt-px" />
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <span className="text-xs leading-[22px] font-bold">{t("tgproxy.toggle")}</span>
            <div className="flex flex-wrap gap-1.5">
              {tg.map((p) => (
                <ProfileLink key={p.id} id={p.id} name={p.name} />
              ))}
            </div>
          </div>
        </div>
      )}
      {tt.length > 0 && (
        <div className="flex items-start gap-2.5">
          <IconChip icon="tunnel" tone="sage" size={22} className="mt-px" />
          <div className="flex min-w-0 flex-1 flex-col gap-1.5">
            <span className="text-xs leading-[22px] font-bold">{t("tt.toggle")}</span>
            <div className="flex flex-wrap gap-1.5">
              {tt.map((p) => (
                <ProfileLink key={p.id} id={p.id} name={p.name} />
              ))}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

/**
 * "Gets": what a person of this group receives, by way, before anything is created. Nothing at all is a red line: a
 * friend must never open an empty page.
 */
export function ReachLine({ group, action }: { group: Group; action?: ReactNode }) {
  const t = useTx();
  const sub = useSubLabel();
  const nothing = group.happNodes === 0 && group.amneziaNodes === 0;
  const item = (w: Way, n: number) => (
    <li key={w} className="flex items-start gap-2 text-xs leading-snug">
      <span aria-hidden className={cx("mt-px grid size-4 flex-none place-items-center rounded-full text-[10px] font-extrabold", n > 0 ? "bg-accent-soft text-accent-text" : "bg-surface-2 text-muted")}>
        {n > 0 ? "✓" : "–"}
      </span>
      <span className={n > 0 ? "text-fg" : "text-muted"}>
        <b>{w === "sub" ? sub : t("users.reachAwg")}</b> — {n > 0 ? t.n("users.reachNodes", n) : t("common.labeled", { label: t("users.reachNo"), value: t(w === "sub" ? "users.reachNoSub" : "users.reachNoAwg") })}
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
