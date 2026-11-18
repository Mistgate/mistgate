import { Radio } from "@base-ui/react/radio";
import { RadioGroup } from "@base-ui/react/radio-group";
import { useQuery } from "@tanstack/react-query";
import { Check } from "@/components/ui/bits";
import { Icon, type IconName } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { defaultShare, shareValid, sharing, shareModes, type ShareChoice, type ShareMode } from "@/lib/node-share";
import { nodesQuery } from "@/lib/queries";
import { groupTone } from "@/screens/users/format";
import { groupsQuery } from "@/screens/users/rpc";

// "Who gets this node" in the New node window: three ways (everyone, some groups, nobody yet) in the window's own switch, the groups to
// tick under the second. The first node of the panel goes to everyone, so the window only says so; from the second node on the choice
// starts at the last one the person made (lib/node-share.ts), else "nobody yet". The panel decides when the window sends nothing.

const icons: Record<ShareMode, IconName> = { all: "globe", listed: "people", none: "eyeOff" };

/** What the window knows about the share: whether to ask (a node exists already), the choice it holds, and what goes in the request. */
export function useNodeShare(chosen: ShareChoice | undefined) {
  const nodes = useQuery(nodesQuery);
  const groups = useQuery(groupsQuery);
  const known = nodes.data !== undefined;
  const live = nodes.data?.nodes.filter((n) => n.status !== NodeStatus.RETIRED).length ?? 0;
  const choice = chosen ?? defaultShare(groups.data);
  // the first node is the panel's own choice (everyone); until the list is there the window sends nothing, which is the same
  const ask = known && live > 0;
  return {
    /** The window asks (it is not the first node). */
    ask,
    /** The first node of the panel: the window says it goes to everyone. */
    first: known && live === 0,
    choice,
    groups,
    /** The choice can be sent: "some groups" needs a group. */
    valid: !ask || shareValid(choice),
    /** The `sharing` of the request, or nothing for the panel to decide. */
    request: ask ? { sharing: sharing(choice) } : {},
  };
}

export type NodeShare = ReturnType<typeof useNodeShare>;

export function SharePick({ share, onChange }: { share: NodeShare; onChange: (c: ShareChoice) => void }) {
  const t = useT();
  const { choice, groups } = share;
  if (share.first)
    return (
      <p className="flex items-start gap-2 text-xs leading-4 text-muted">
        <Icon name="globe" size={14} className="mt-px flex-none" />
        {t("ns.pick.first")}
      </p>
    );
  if (!share.ask) return null;
  const list = groups.data ?? [];
  const set = (mode: ShareMode) => onChange(mode === "listed" ? { mode, groupIds: choice.mode === "listed" ? choice.groupIds : [] } : { mode, groupIds: [] });
  const toggle = (id: string, on: boolean) => onChange({ mode: "listed", groupIds: on ? [...choice.groupIds, id] : choice.groupIds.filter((x) => x !== id) });
  return (
    <fieldset className="m-0 flex min-w-0 flex-col gap-2.5 border-0 p-0" data-testid="node-share">
      <div className="an-lb">{t("ns.pick.title")}</div>
      <RadioGroup aria-label={t("ns.pick.title")} value={choice.mode} onValueChange={(v) => set(v as ShareMode)} className="an-ws">
        {shareModes.map((m) => (
          <Radio.Root key={m} value={m}>
            <Icon name={icons[m]} size={16} />
            {t(`ns.mode.${m}`)}
          </Radio.Root>
        ))}
      </RadioGroup>
      <p className="text-xs leading-4 text-muted">{t(`ns.pick.${choice.mode}`)}</p>
      {choice.mode === "listed" &&
        (groups.isPending ? (
          <Pending compact />
        ) : groups.isError && !groups.data ? (
          <QueryError error={groups.error} compact onRetry={() => void groups.refetch()} />
        ) : list.length === 0 ? (
          <p className="text-[13px] text-muted">{t("ns.pick.noGroups")}</p>
        ) : (
          <div role="group" aria-label={t("ns.pick.groups")} className="wn-scroll max-h-[196px] overflow-y-auto rounded-field border border-line">
            {list.map((g) => (
              <label key={g.id} className="flex min-h-10 cursor-pointer items-center gap-2.5 border-t border-line px-3 text-sm first:border-t-0 hover:bg-step">
                <Check checked={choice.groupIds.includes(g.id)} onCheckedChange={(on) => toggle(g.id, on)} aria-label={g.name} />
                <i aria-hidden data-tone={groupTone(g.id, g.color)} className="size-2 flex-none rounded-full bg-(--t)" />
                <span className="min-w-0 flex-1 truncate">{g.name}</span>
                <span className="flex-none text-xs text-muted">{t.n("users.groupPeople", g.userCount + g.extraUserCount)}</span>
              </label>
            ))}
          </div>
        ))}
      {!share.valid && <p className="text-xs leading-4 text-warn-text">{t("ns.pick.pickOne")}</p>}
    </fieldset>
  );
}
