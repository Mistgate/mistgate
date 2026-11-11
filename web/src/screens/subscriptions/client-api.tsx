import { useQuery } from "@tanstack/react-query";
import { Card, SectionLabel } from "@/components/ui/bits";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { clientApiQuery, useClientApiUpdate } from "@/lib/clientapi";
import { useIsOwner } from "@/lib/updates";
import { useTx } from "@/screens/users/t";

/**
 * "Client API for the Mistgate app": whether the Mistgate desktop app may find the panel and connect to it directly, each device
 * with a key of its own. The owner flips it (a step-up, then a toast); for anyone else the card reads the state and says whose
 * it is. Off closes discovery and new connections only: devices already connected keep working.
 */
export function ClientApiCard() {
  const t = useTx();
  const owner = useIsOwner();
  const q = useQuery(clientApiQuery);
  const save = useClientApiUpdate();
  const on = q.data?.enabled ?? false;

  return (
    <section id="client-api">
      <Card lg className="flex min-w-0 items-start gap-4 p-4">
        <div className="flex min-w-0 flex-1 flex-col gap-1">
          <SectionLabel icon="laptop" tone="lavender" as="h2">
            {t("subs.clientApi.title")}
          </SectionLabel>
          {q.isPending && <Pending compact />}
          {q.data && <p className="text-xs leading-snug text-pretty text-muted">{t(on ? "subs.clientApi.on" : "subs.clientApi.off")}</p>}
          {q.data && !owner && <p className="text-xs leading-snug text-pretty text-muted">{t("subs.clientApi.ownerOnly")}</p>}
          {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
        </div>
        {q.data && <Switch checked={on} disabled={!owner || save.isPending} onCheckedChange={(v) => save.mutate(v)} aria-label={t("subs.clientApi.title")} className="mt-0.5" />}
      </Card>
    </section>
  );
}
