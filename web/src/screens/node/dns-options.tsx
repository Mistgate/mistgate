import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useLang, useT } from "@/i18n";
import { dns } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { catalogOf, move, presetName, presetSubtitle, transportKey, type DnsData } from "@/screens/subscriptions/model";
import { dnsPresetsQuery } from "@/screens/subscriptions/queries";

// the server refuses more (SetNodeDnsOptions)
const maxOffered = 20;

/** What every node offers on the user page, in the owner's order. A node that is not in the list offers nothing. */
export const nodeDnsOptionsQuery = queryOptions({
  queryKey: ["dns", "node-options"],
  queryFn: async ({ signal }) => plain((await dns.listNodeDnsOptions({}, { signal })).nodes),
  staleTime: 15_000,
});

type Saved = { presetIds: string[]; defaultPresetId: string };

/**
 * "DNS choice on the user page": which DNS presets this node offers a person on their page, in what order, and which one
 * applies when they pick nothing. Not the node's own resolver (Settings: "DNS resolvers for this node"). Everyone sees
 * it, only the owner changes it (as the API).
 */
export function DnsOptionsCard({ nodeId }: { nodeId: string }) {
  const t = useT();
  const presets = useQuery(dnsPresetsQuery);
  const options = useQuery(nodeDnsOptionsQuery);
  const failed = presets.isError ? presets : options.isError ? options : null;
  const saved = options.data?.find((o) => o.nodeId === nodeId);
  return (
    <section className="flex flex-col gap-3 rounded-card-lg border border-line bg-panel p-4">
      <SectionLabel as="h2" icon="dns" tone="lavender">
        {t("subs.dnsOpt.title")}
      </SectionLabel>
      <p className="text-[13px] leading-snug text-muted">{t("subs.dnsOpt.note")}</p>
      <Notice>{t("subs.dnsOpt.warn")}</Notice>
      {failed ? (
        <QueryError compact error={failed.error} onRetry={() => void failed.refetch()} />
      ) : !presets.data || !options.data ? (
        <Pending compact />
      ) : (
        // keyed by what is saved: a save (or a change from elsewhere) restarts the form from the server
        <DnsOptionsForm key={`${nodeId}:${JSON.stringify(saved ?? null)}`} nodeId={nodeId} saved={saved} data={presets.data} />
      )}
    </section>
  );
}

function DnsOptionsForm({ nodeId, saved, data }: { nodeId: string; saved: Saved | undefined; data: DnsData }) {
  const t = useT();
  const lang = useLang();
  const toast = useToast();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const catalog = useMemo(() => catalogOf(data.providers), [data.providers]);
  const byId = new Map(data.presets.map((p) => [p.id, p]));

  // an offered preset that was deleted since is gone from the list: it is not sent back
  const startIds = (saved?.presetIds ?? []).filter((id) => byId.has(id));
  const startDef = startIds.includes(saved?.defaultPresetId ?? "") ? saved!.defaultPresetId : "";
  const [ids, setIds] = useState(startIds);
  const [def, setDef] = useState(startDef);
  const dirty = JSON.stringify([ids, def]) !== JSON.stringify([startIds, startDef]);
  const full = ids.length >= maxOffered;

  const save = useMutation({
    mutationFn: () => dns.setNodeDnsOptions({ nodeId, presetIds: ids, defaultPresetId: def }),
    onSuccess: () => {
      toast(t("common.saved"));
      void qc.invalidateQueries({ queryKey: ["dns"] });
      void qc.invalidateQueries({ queryKey: ["users"] }); // a person's pick is "offered" or not by this
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  const toggle = (id: string, on: boolean) => {
    setIds((x) => (on ? [...x, id] : x.filter((y) => y !== id)));
    if (!on && def === id) setDef("");
  };
  // the offered ones first, in the owner's order, then the rest as the presets are listed
  const rows = [...ids.flatMap((id) => byId.get(id) ?? []), ...data.presets.filter((p) => !ids.includes(p.id))];
  const group = `dns-default-${nodeId}`;

  if (data.presets.length === 0) return <p className="text-[13px] text-muted">{t("subs.dnsOpt.empty")}</p>;

  return (
    <>
      <ul className="flex flex-col">
        {rows.map((p) => {
          const at = ids.indexOf(p.id);
          const on = at >= 0;
          const name = presetName(p, lang);
          const sub = presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));
          return (
            <li key={p.id} className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-line py-2 first:border-t-0">
              <label className="flex min-w-0 flex-[1_1_200px] items-start gap-2.5">
                <input
                  type="checkbox"
                  className="mt-0.5 size-4 flex-none accent-accent"
                  aria-label={t("subs.dnsOpt.offer", { name })}
                  checked={on}
                  disabled={!owner || (!on && full)}
                  onChange={(e) => toggle(p.id, e.target.checked)}
                />
                <span className="flex min-w-0 flex-col">
                  <span className="text-[13px] font-bold break-words">{name}</span>
                  {sub && <span className="text-[11px] leading-snug break-words text-muted">{sub}</span>}
                </span>
              </label>
              {on && (
                <>
                  <label className="flex items-center gap-1.5 text-xs text-muted">
                    <input type="radio" name={group} className="size-4 accent-accent" aria-label={t("subs.dnsOpt.def", { name })} checked={def === p.id} disabled={!owner} onChange={() => setDef(p.id)} />
                    {t("subs.dnsOpt.defShort")}
                  </label>
                  {owner && (
                    <span className="flex gap-0.5">
                      <IconButton variant="flat" aria-label={t("subs.dnsOpt.up", { name })} disabled={at === 0} onClick={() => setIds((x) => move(x, at, at - 1))}>
                        <Icon name="arrowUp" size={14} />
                      </IconButton>
                      <IconButton variant="flat" aria-label={t("subs.dnsOpt.down", { name })} disabled={at === ids.length - 1} onClick={() => setIds((x) => move(x, at, at + 1))}>
                        <Icon name="arrowDown" size={14} />
                      </IconButton>
                    </span>
                  )}
                </>
              )}
            </li>
          );
        })}
      </ul>
      {ids.length > 0 && (
        <label className="flex items-start gap-1.5 border-t border-line pt-3 text-xs text-muted">
          <input type="radio" name={group} className="mt-px size-4 flex-none accent-accent" checked={def === ""} disabled={!owner} onChange={() => setDef("")} />
          <span className="flex flex-col">
            <span className="font-bold text-fg">{t("subs.dnsOpt.defNone")}</span>
            <span className="leading-snug">{t("subs.dnsOpt.defNoneHint")}</span>
          </span>
        </label>
      )}
      <p className="text-xs leading-snug text-muted">{ids.length === 0 ? t("subs.dnsOpt.nothing") : t("subs.dnsOpt.count", { n: ids.length, max: maxOffered })}</p>
      {owner ? (
        <div className="flex justify-end">
          <Button variant="primary" size="md" disabled={!dirty || save.isPending} onClick={() => save.mutate()}>
            {t("common.save")}
          </Button>
        </div>
      ) : (
        <p className="text-xs leading-snug text-muted">{t("subs.dnsOpt.ownerOnly")}</p>
      )}
    </>
  );
}
