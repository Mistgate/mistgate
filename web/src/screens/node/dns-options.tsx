import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type ReactNode } from "react";
import { IconButton } from "@/components/ui/icon-button";
import { Icon } from "@/components/ui/icons";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useLang, useT } from "@/i18n";
import { dns } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { catalogOf, move, presetName, presetSubtitle, transportKey, type DnsData } from "@/screens/subscriptions/model";
import { dnsPresetsQuery } from "@/screens/subscriptions/queries";
import { SvSave } from "./server-parts";
import "./exit.css";

// the server refuses more (SetNodeDnsOptions, SubscriptionSettings.link_dns)
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
 * applies when they pick nothing. Not the node's own resolver (the card beside WARP). Everyone sees it, only the owner
 * changes it (as the API). The board's .nx-pre: a head with the count, a line per preset with its default and its switch.
 */
export function DnsOptionsCard({ nodeId }: { nodeId: string }) {
  const t = useT();
  const presets = useQuery(dnsPresetsQuery);
  const options = useQuery(nodeDnsOptionsQuery);
  const failed = presets.isError ? presets : options.isError ? options : null;
  const saved = options.data?.find((o) => o.nodeId === nodeId);
  return (
    <section className="nx-pre relative overflow-hidden rounded-card border border-line bg-surface" aria-label={t("subs.dnsOpt.title")}>
      {failed ? (
        <>
          <Head title={t("subs.dnsOpt.title")} sub={t("subs.dnsOpt.note")} />
          <div className="px-6 pb-5 max-[720px]:px-4">
            <QueryError compact error={failed.error} onRetry={() => void failed.refetch()} />
          </div>
        </>
      ) : !presets.data || !options.data ? (
        <>
          <Head title={t("subs.dnsOpt.title")} sub={t("subs.dnsOpt.note")} />
          <div className="px-6 pb-5 max-[720px]:px-4">
            <Pending compact />
          </div>
        </>
      ) : (
        // keyed by what is saved: a save (or a change from elsewhere) restarts the form from the server
        <DnsOptionsForm key={`${nodeId}:${JSON.stringify(saved ?? null)}`} nodeId={nodeId} saved={saved} data={presets.data} />
      )}
    </section>
  );
}

function Head({ title, sub, count }: { title: string; sub: ReactNode; count?: string }) {
  return (
    <div className="nx-ch">
      <div className="nd-wt">
        <h3>{title}</h3>
        <span>{sub}</span>
      </div>
      {count && <span className="nd-lc">{count}</span>}
    </div>
  );
}

/** An offer as saved, without the presets deleted since (they are not sent back) and with a default that is still offered. */
export function offerOf(saved: Saved | undefined, data: DnsData): Saved {
  const ids = (saved?.presetIds ?? []).filter((id) => data.presets.some((p) => p.id === id));
  return { presetIds: ids, defaultPresetId: ids.includes(saved?.defaultPresetId ?? "") ? saved!.defaultPresetId : "" };
}

function DnsOptionsForm({ nodeId, saved, data }: { nodeId: string; saved: Saved | undefined; data: DnsData }) {
  const t = useT();
  const lang = useLang();
  const toast = useToast();
  const qc = useQueryClient();
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const start = offerOf(saved, data);
  const [offer, setOffer] = useState(start);
  const [flash, setFlash] = useState(false);
  const dirty = JSON.stringify(offer) !== JSON.stringify(start);

  const save = useMutation({
    mutationFn: () => dns.setNodeDnsOptions({ nodeId, ...offer }),
    onSuccess: () => {
      toast(t("common.saved"));
      setFlash(true);
      void qc.invalidateQueries({ queryKey: ["dns"] });
      void qc.invalidateQueries({ queryKey: ["users"] }); // a person's pick is "offered" or not by this
    },
    onError: (e) => toast.error(errorText(e, t)),
  });

  const catalog = useMemo(() => catalogOf(data.providers), [data.providers]);
  const byId = new Map(data.presets.map((p) => [p.id, p]));
  const ids = offer.presetIds;
  const def = offer.defaultPresetId;
  const full = ids.length >= maxOffered;
  const setIds = (next: string[]) => setOffer({ presetIds: next, defaultPresetId: next.includes(def) ? def : "" });
  const toggle = (id: string, on: boolean) => setIds(on ? [...ids, id] : ids.filter((y) => y !== id));
  // the offered ones first, in the owner's order, then the rest as the presets are listed
  const rows = [...ids.flatMap((id) => byId.get(id) ?? []), ...data.presets.filter((p) => !ids.includes(p.id))];

  return (
    <>
      <Head
        title={t("subs.dnsOpt.title")}
        sub={
          <>
            {t("subs.dnsOpt.warn")}
            {data.presets.some((p) => p.blockAds || p.blockMax) && <span className="mt-1 block">{t("subs.dns.awgNoBlock")}</span>}
          </>
        }
        count={ids.length > 0 ? t("subs.dnsOpt.count", { n: ids.length, max: maxOffered }) : undefined}
      />
      <p className="nx-p !pb-3">{t("subs.dnsOpt.note")}</p>
      {data.presets.length === 0 ? (
        <p className="nx-p !pb-5">{t("subs.dnsOpt.empty")}</p>
      ) : (
        <>
          <div className="nx-ph" aria-hidden>
            <span>{t("np.ex.pre.col.preset")}</span>
            <span>{t("subs.dnsOpt.defShort")}</span>
            <span>{t("np.ex.pre.col.offer")}</span>
          </div>
          <div role="radiogroup" aria-label={t("subs.dnsOpt.defShort")}>
            {rows.map((p) => {
              const at = ids.indexOf(p.id);
              const on = at >= 0;
              const name = presetName(p, lang);
              const sub = presetSubtitle(p, (x) => t("subs.dns.sub.direct", { s: x }), catalog, (tr) => t(transportKey[tr]));
              return (
                <div key={p.id} className={cx("nx-pr", !on && "off")}>
                  <div className="nm flex min-w-0 items-center gap-2">
                    <div className="min-w-0 flex-1">
                      <b className="break-words">{name}</b>
                      {sub && <span className="break-words">{sub}</span>}
                    </div>
                    {on && owner && (
                      <span className="mv">
                        <IconButton variant="flat" aria-label={t("subs.dnsOpt.up", { name })} disabled={at === 0} onClick={() => setIds(move(ids, at, at - 1))}>
                          <Icon name="arrowUp" size={14} />
                        </IconButton>
                        <IconButton variant="flat" aria-label={t("subs.dnsOpt.down", { name })} disabled={at === ids.length - 1} onClick={() => setIds(move(ids, at, at + 1))}>
                          <Icon name="arrowDown" size={14} />
                        </IconButton>
                      </span>
                    )}
                  </div>
                  {/* the default is one of the offered; a preset that is not offered has none to be */}
                  <button type="button" role="radio" aria-checked={on && def === p.id} aria-label={t("subs.dnsOpt.def", { name })} className="nx-rad" disabled={!owner || !on} onClick={() => setOffer({ presetIds: ids, defaultPresetId: p.id })}>
                    <i />
                  </button>
                  <Switch aria-label={t("subs.dnsOpt.offer", { name })} checked={on} disabled={!owner || (!on && full)} onCheckedChange={(v) => toggle(p.id, v)} />
                </div>
              );
            })}
          </div>
          <div className="nx-cfoot">
            <span>{ids.length === 0 ? t("subs.dnsOpt.nothing") : def ? t("np.ex.pre.defOn") : t("subs.dnsOpt.defNoneHint")}</span>
            {ids.length > 0 && def !== "" && owner && (
              <button type="button" className="nx-lk" onClick={() => setOffer({ presetIds: ids, defaultPresetId: "" })}>
                {t("subs.dnsOpt.defNone")}
              </button>
            )}
          </div>
          {owner ? (
            <SvSave dirty={dirty} saved={flash && !dirty} busy={save.isPending} onCancel={() => setOffer(start)} onSave={() => save.mutate()} />
          ) : (
            <p className="nx-p !pt-3 !pb-4">{t("subs.dnsOpt.ownerOnly")}</p>
          )}
        </>
      )}
    </>
  );
}
