import { useQuery } from "@tanstack/react-query";
import { Pending, QueryError } from "@/components/ui/query-error";
import { clientApiQuery, useClientApiUpdate } from "@/lib/clientapi";
import { useIsOwner } from "@/lib/updates";
import { DefaultMark } from "@/components/brand";
import { useTx } from "@/screens/users/t";
import { Section, Toggle } from "./parts";

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
      <Section tone="mint" icon={<DefaultMark size={18} />} title={t("subs.clientApi.title")} shot="subs-client-api">
        {q.isPending && <Pending compact />}
        {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
        {q.data && (
          <div className="sb-tgs">
            <Toggle title={t("sx.mg.on")} label={t("subs.clientApi.title")} hint={t(on ? "subs.clientApi.on" : "subs.clientApi.off")} checked={on} disabled={!owner || save.isPending} onChange={(v) => save.mutate(v)} />
          </div>
        )}
        {stats && (
          <div className="sb-st2" data-stats>
            <div>
              <span>{t("subs.clientApi.statPeople")}</span>
              <b className="mono">{t("subs.clientApi.statPeopleValue", { on: stats.peopleOn, all: stats.people })}</b>
            </div>
            <div>
              <span>{t("subs.clientApi.statDevices")}</span>
              <b className="mono">{stats.devices}</b>
            </div>
          </div>
        )}
        {q.data && <p className="pp-q1 sb-pad">{t("subs.clientApi.perPerson")}</p>}
        {q.data && !owner && <p className="pp-q1 sb-pad">{t("subs.clientApi.ownerOnly")}</p>}
      </Section>
    </section>
  );
}
