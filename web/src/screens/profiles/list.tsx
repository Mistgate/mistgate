import { useQuery } from "@tanstack/react-query";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Card, PageTitle } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { EmptyState } from "@/components/ui/notice";
import { cx } from "@/lib/cx";
import { AppLink, useGo } from "@/screens/users/nav";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { Pending, QueryError } from "@/components/ui/query-error";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import type { StatusReason } from "@/gen/mistgate/admin/v1/common_pb";

const warnText = (w: StatusReason, t: Tx) => t.opt(`profiles.warn.${w.code}`, w.params) ?? w.code.replaceAll("_", " ");

export function ProfilesScreen() {
  const t = useTx();
  const go = useGo();
  const list = useQuery(profileListQuery);
  const protocols = useQuery(protocolsQuery);
  const groups = useQuery(groupsQuery);

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <PageTitle>{t("profiles.title")}</PageTitle>
          <span className="text-xs text-muted">{t("profiles.sub")}</span>
        </div>
        <Button variant="primary" onClick={() => go("/profiles/new")}>
          <Icon name="plus" size={14} />
          {t("profiles.create")}
        </Button>
      </div>

      {list.isPending ? (
        <Pending />
      ) : list.isError ? (
        <QueryError error={list.error} onRetry={() => void list.refetch()} />
      ) : list.data.length === 0 ? (
        <Card lg className="border-dashed">
          <EmptyState
            title={t("profiles.none.title")}
            action={
              <Button variant="primary" size="lg" onClick={() => go("/profiles/new")}>
                {t("profiles.new")}
              </Button>
            }
          >
            {t("profiles.none.body")}
          </EmptyState>
        </Card>
      ) : (
        <div className="grid gap-2.5 md:grid-cols-2">
          {list.data.map((p) => (
            <ProfileCard
              key={p.id}
              p={p}
              t={t}
              protocol={protocols.data?.find((x) => x.id === p.protocol)}
              inGroup={groups.data ? groups.data.some((g) => g.profileIds.includes(p.id)) : undefined}
            />
          ))}
        </div>
      )}
    </div>
  );
}

/** A yellow mark of something that keeps the profile from anyone: on no node, or in no group. */
function Label({ children }: { children: string }) {
  return <span className="inline-flex h-[22px] items-center rounded-[7px] border border-warn-line bg-warn-soft px-1.5 text-[11px] font-bold text-warn-text">{children}</span>;
}

function ProfileCard({ p, t, protocol, inGroup }: { p: ProfileSummary; t: Tx; protocol?: { displayName: string; apps: App[] }; inGroup?: boolean }) {
  // accent for the protocol Happ consumes (the primary one), grey for the rest (HY2 / AWG)
  const accent = protocol?.apps.includes(App.HAPP) ?? false;
  const labels = [p.nodeCount === 0 && t("profiles.label.nowhere"), inGroup === false && t("profiles.label.noGroup")].filter((x): x is string => !!x);
  return (
    <AppLink to="/profiles/$id" params={{ id: p.id }} className="block rounded-card-lg outline-offset-2">
      <Card lg lift className="flex h-full flex-col gap-3 p-4">
        <div className="flex items-center gap-2">
          <b className="min-w-0 flex-1 truncate text-[15px] tracking-[-0.01em]">{p.name}</b>
          <span className={cx("flex h-[22px] items-center rounded-[7px] px-2 font-mono text-[10px] font-bold uppercase", accent ? "bg-accent-soft text-accent-text" : "bg-surface-2 text-fg")}>
            {protocol?.displayName ?? p.protocol}
          </span>
        </div>
        <span className="font-mono text-xs leading-normal break-words text-muted">{p.summary || "—"}</span>
        {labels.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {labels.map((l) => (
              <Label key={l}>{l}</Label>
            ))}
          </div>
        )}
        <div className="mt-auto flex flex-wrap gap-x-4 gap-y-1 border-t border-line pt-2.5 text-xs text-muted">
          <span>
            <b className="font-mono text-fg">{p.nodeCount}</b> {t.n("profiles.nodes", p.nodeCount)}
          </span>
          <span>
            <b className="font-mono text-fg">{p.userCount}</b> {t.n("profiles.users", p.userCount)}
          </span>
          {p.warnings.map((w, i) => (
            <span key={i} className="flex items-center gap-1.5 text-fg">
              <span aria-hidden className="size-[7px] rounded-full bg-warn" />
              {warnText(w, t)}
            </span>
          ))}
        </div>
      </Card>
    </AppLink>
  );
}
