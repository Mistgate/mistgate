import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Avatar, Card, PageTitle, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { EmptyState, Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { StatusPill } from "@/components/ui/status";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { users } from "@/lib/api";
import { ProfileCard } from "@/screens/profiles/list";
import { AppLink, useGo } from "@/screens/users/nav";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { useTx } from "@/screens/users/t";

// The Telegram tab: the MTProto profiles of the panel and the people whose
// subscriptions carry tg://proxy links. Telegram does not fetch subscriptions
// by itself, so a row can reveal the actual links and copy them.
export function TelegramScreen({ initialOpen = false }: { initialOpen?: boolean } = {}) {
  const t = useT();
  const tx = useTx();
  const go = useGo();
  const toast = useToast();
  const [busy, setBusy] = useState<string | null>(null);
  const profiles = useQuery(profileListQuery);
  const protocols = useQuery(protocolsQuery);
  const groups = useQuery(groupsQuery);
  const people = useQuery({
    queryKey: ["telegram", "users"],
    queryFn: async ({ signal }) => (await users.listUsers({ pageSize: 200 }, { signal })).users,
    refetchInterval: 10_000,
  });
  const mtproto = (profiles.data ?? []).filter((p) => p.protocol === "mtproto");
  const withTelegram = (people.data ?? []).filter((u) => u.apps?.telegram);

  async function copySubscription(userID: string) {
    setBusy(userID);
    try {
      const link = await users.getSubscriptionLink({ userId: userID });
      await navigator.clipboard.writeText(link.url);
      toast(t("tgproxy.copied"));
    } catch (e) {
      toast.error(String(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex flex-col gap-3.5">
      <div className="flex items-center gap-3">
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <PageTitle>{t("tgproxy.title")}</PageTitle>
          <span className="text-xs text-muted">{t("tgproxy.sub")}</span>
        </div>
        <Button variant="primary" onClick={() => go("/profiles/new")}>
          <Icon name="plus" size={14} />
          {t("tgproxy.create")}
        </Button>
      </div>

      <section className="flex flex-col gap-2.5">
        <div className="flex items-center justify-between gap-3">
          <SectionLabel icon="layers" tone="sky">{t("tgproxy.profiles")}</SectionLabel>
          {mtproto.length > 0 && <span className="text-xs text-muted">{mtproto.length}</span>}
        </div>
        {profiles.isPending ? (
          <Pending />
        ) : profiles.isError ? (
          <QueryError error={profiles.error} onRetry={() => void profiles.refetch()} />
        ) : mtproto.length === 0 ? (
          <Card lg className="border-dashed">
            <EmptyState
              title={t("tgproxy.profiles")}
              action={
                <Button variant="primary" size="lg" onClick={() => go("/profiles/new")}>
                  {t("tgproxy.create")}
                </Button>
              }
            >
              {t("tgproxy.none")}
            </EmptyState>
          </Card>
        ) : (
          <div className="grid gap-2.5 md:grid-cols-2">
            {mtproto.map((p) => (
              <ProfileCard
                key={p.id}
                p={p}
                t={tx}
                protocol={protocols.data?.find((x) => x.id === p.protocol)}
                inGroup={groups.data ? groups.data.some((g) => g.profileIds.includes(p.id)) : undefined}
              />
            ))}
          </div>
        )}
      </section>

      <Card lg className="flex flex-col p-4">
        <div className="flex items-center justify-between gap-3 pb-2">
          <SectionLabel icon="people" tone="sand">{t("tgproxy.users")}</SectionLabel>
          {withTelegram.length > 0 && <span className="text-xs text-muted">{withTelegram.length}</span>}
        </div>
        {people.isPending ? (
          <Pending />
        ) : people.isError ? (
          <QueryError error={people.error} onRetry={() => void people.refetch()} />
        ) : withTelegram.length === 0 ? (
          <Notice tone="quiet">{t("tgproxy.enable")}</Notice>
        ) : (
          withTelegram.map((u, i) => (
            <TelegramPerson
              key={u.id}
              userID={u.id}
              name={u.name}
              enabled={!!u.apps?.telegram}
              usable={u.accessTelegram}
              avatar={i}
              busy={busy === u.id}
              initialOpen={initialOpen}
              onCopy={() => void copySubscription(u.id)}
            />
          ))
        )}
        {withTelegram.length > 0 && <p className="mt-2 border-t border-line pt-2 text-xs leading-normal text-muted">{t("tgproxy.note")}</p>}
      </Card>
    </div>
  );
}

function TelegramPerson({
  userID, name, enabled, usable, avatar, busy, initialOpen, onCopy,
}: {
  userID: string; name: string; enabled: boolean; usable: boolean; avatar: number; busy: boolean; initialOpen: boolean; onCopy: () => void;
}) {
  const t = useT();
  const toast = useToast();
  const [open, setOpen] = useState(initialOpen);
  const links = useQuery({
    queryKey: ["telegram", "links", userID],
    queryFn: async ({ signal }) => (await users.getTelegramLinks({ userId: userID }, { signal })).links,
    enabled: open,
    staleTime: 0,
  });
  async function copyLink(link: string) {
    try {
      await navigator.clipboard.writeText(link);
      toast(t("tgproxy.copied"));
    } catch (e) {
      toast.error(String(e));
    }
  }
  return (
    <div className="flex flex-col gap-2 border-t border-line py-3 first:border-t-0">
      <div className="flex flex-wrap items-center gap-3">
        <Avatar name={name} index={avatar} />
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <AppLink to="/users/$id" params={{ id: userID }} className="truncate text-sm font-bold hover:underline">
            {name}
          </AppLink>
          <div className="flex flex-wrap items-center gap-2 text-xs text-muted">
            <StatusPill sm kind={!enabled ? "off" : usable ? "ok" : "warn"} label={!enabled ? t("tgproxy.off") : usable ? t("tgproxy.toggle") : t("tgproxy.waiting")} />
            <span>{usable ? t("tgproxy.sub") : t("tgproxy.enable")}</span>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button variant="secondary" size="sm" onClick={() => setOpen((v) => !v)}>
            <Icon name="eye" size={12} />
            {open ? t("tgproxy.hide") : t("tgproxy.show")}
          </Button>
          <Button variant="secondary" size="sm" disabled={busy} onClick={onCopy}>
            <Icon name="copy" size={12} />
            {t("tgproxy.copy")}
          </Button>
        </div>
      </div>
      {open && (
        <div className="ml-10 flex flex-col gap-1 rounded-ctl bg-surface-2 p-2">
          {links.isPending ? (
            <span className="text-xs text-muted">{t("common.loading")}</span>
          ) : links.isError ? (
            <span className="text-xs text-danger">{String(links.error)}</span>
          ) : links.data.length === 0 ? (
            <span className="text-xs text-muted">{t("tgproxy.noLinks")}</span>
          ) : (
            links.data.map((l) => (
              <div key={l.url} className="flex flex-wrap items-center justify-between gap-2 rounded-[8px] px-1.5 py-1 hover:bg-surface">
                <span className="flex min-w-0 items-center gap-2 text-xs">
                  <Icon name="server" size={12} className="flex-none text-muted" />
                  <b className="truncate text-fg">{l.nodeName}</b>
                  <span className="truncate text-muted">{l.profileName}</span>
                </span>
                <Button variant="ghost" size="xs" onClick={() => void copyLink(l.url)}>
                  {t("tgproxy.copyLink")}
                </Button>
              </div>
            ))
          )}
        </div>
      )}
    </div>
  );
}
