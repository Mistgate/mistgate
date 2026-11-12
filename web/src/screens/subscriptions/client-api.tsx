import { useQuery } from "@tanstack/react-query";
import { Card } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { clientApiQuery, useClientApiUpdate } from "@/lib/clientapi";
import { useIsOwner } from "@/lib/updates";
import { MistgateTile } from "@/screens/users/mistgate-app";
import { useTx } from "@/screens/users/t";

/**
 * "The Mistgate app" for the whole panel: whether the Mistgate desktop app may find the panel and connect to it directly, each device
 * with a key of its own. The owner flips it (a step-up, then a toast); for anyone else the card reads the state and says whose it is.
 * Off closes discovery and new connections only: devices already connected keep working. Under the text a short summary: how many
 * people have the app on (their own switch is on their card) and how many devices are connected with a key.
 */
export function ClientApiCard() {
  const t = useTx();
  const owner = useIsOwner();
  const q = useQuery(clientApiQuery);
  const save = useClientApiUpdate();
  const on = q.data?.enabled ?? false;
  const stats = q.data?.stats;

  return (
    <section id="client-api">
      <Card lg className="flex min-w-0 flex-col gap-3 p-4">
        <div className="flex min-w-0 items-start gap-3">
          <MistgateTile dim={q.data !== undefined && !on} />
          <div className="flex min-w-0 flex-1 flex-col gap-1">
            <h2 className="text-[13px] font-bold">{t("subs.clientApi.title")}</h2>
            {q.isPending && <Pending compact />}
            {q.data && <p className="text-xs leading-snug text-pretty text-muted">{t(on ? "subs.clientApi.on" : "subs.clientApi.off")}</p>}
            {q.data && <p className="text-xs leading-snug text-pretty text-muted">{t("subs.clientApi.perPerson")}</p>}
            {q.data && !owner && <p className="text-xs leading-snug text-pretty text-muted">{t("subs.clientApi.ownerOnly")}</p>}
            {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
          </div>
          {q.data && <Switch checked={on} disabled={!owner || save.isPending} onCheckedChange={(v) => save.mutate(v)} aria-label={t("subs.clientApi.title")} className="mt-0.5" />}
        </div>
        {stats && (
          <dl data-stats className="ms-12 grid grid-cols-2 gap-x-4 gap-y-1 border-t border-line pt-3">
            <div className="flex min-w-0 flex-col gap-0.5">
              <dt className="text-[11px] leading-snug text-muted">{t("subs.clientApi.statPeople")}</dt>
              <dd className="text-[15px] font-bold tabular-nums">{t("subs.clientApi.statPeopleValue", { on: stats.peopleOn, all: stats.people })}</dd>
            </div>
            <div className="flex min-w-0 flex-col gap-0.5">
              <dt className="text-[11px] leading-snug text-muted">{t("subs.clientApi.statDevices")}</dt>
              <dd className="text-[15px] font-bold tabular-nums">{stats.devices}</dd>
            </div>
          </dl>
        )}
      </Card>
    </section>
  );
}
