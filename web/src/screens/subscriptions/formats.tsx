import { useQuery } from "@tanstack/react-query";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Avatar, Card, Chip, SectionLabel } from "@/components/ui/bits";
import { protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Tx } from "@/screens/users/t";
import { Pending, QueryError } from "@/components/ui/query-error";
import { formatDescKey, formatKey, ruleFormats, type Rule } from "./model";
import { clientsQuery, settingsQuery } from "./queries";
import { Lead } from "./ui";

/**
 * "Who gets it" of a format the rules hand out (Mihomo YAML, the fake 404): the rules that pick it, by their text, or
 * nobody while no rule does. The link list and the page have their own sentences (every other app, every browser).
 */
export function whoByRules(t: Tx, f: SubFormat, rules: readonly Rule[]) {
  const texts = rules.filter((r) => r.format === f).map((r) => (t.lang === "ru" ? `«${r.uaContains}»` : `“${r.uaContains}”`));
  if (texts.length === 0) return t("subs.fmt.who.decoy");
  const list = new Intl.ListFormat(t.lang, { type: "conjunction" }).format(texts);
  return t.n(f === SubFormat.MIHOMO_YAML ? "subs.fmt.who.mihomo" : "subs.fmt.who.rules", texts.length, { rules: list });
}

/** "Apps & formats": which apps we work with and what they read, then what the link can hand out. Read-only; the choice lives in "Who gets what". */
export function FormatsTab({ go }: { go: (tab: string) => void }) {
  const t = useTx();
  const clients = useQuery(clientsQuery);
  const protocols = useQuery(protocolsQuery);
  const rules = useQuery(settingsQuery).data?.settings.rules ?? [];
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  if (clients.isPending) return <Pending />;
  if (clients.isError) return <QueryError error={clients.error} onRetry={() => void clients.refetch()} />;

  const fmtName = (f: SubFormat) => t(formatKey[f] ?? "subs.fmt.base64");
  return (
    <div className="flex flex-col gap-5">
      <Lead>{t("subs.formats.lead")}</Lead>

      <section className="flex flex-col gap-3">
        <div className="flex flex-col gap-0.5">
          <SectionLabel icon="phone" tone="mint">
            {t("subs.clients")}
          </SectionLabel>
          <span className="text-xs text-muted">{t("subs.clientsHint")}</span>
        </div>
        {clients.data.length === 0 ? (
          <p className="text-[13px] text-muted">{t("subs.clientsNone")}</p>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {clients.data.map((c, i) => (
              <Card key={c.id} lg className="flex min-w-0 flex-col gap-3 p-4">
                <div className="flex items-center gap-2.5">
                  <Avatar name={c.name} index={i} size={34} />
                  <b className="min-w-0 flex-1 truncate text-[15px] tracking-[-0.01em]">{c.name}</b>
                </div>
                <span className="flex flex-wrap gap-1.5">
                  {c.protocols.map((p) => (
                    <Chip key={p}>{protoName(p)}</Chip>
                  ))}
                </span>
                <div className="flex flex-col gap-1.5 border-t border-line pt-3">
                  <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.clientFormats")}</span>
                  {c.formats.length === 0 ? (
                    <span className="text-xs leading-snug text-muted">{t("subs.clientNoSub")}</span>
                  ) : (
                    <span className="flex flex-wrap gap-1.5">
                      {c.formats.map((f) => (
                        <span key={f} className="inline-flex h-[22px] items-center rounded-ctl bg-accent-soft px-2 text-[11px] font-bold text-accent-text">
                          {fmtName(f)}
                        </span>
                      ))}
                    </span>
                  )}
                </div>
              </Card>
            ))}
          </div>
        )}
      </section>

      <section className="flex flex-col gap-3">
        <div className="flex items-end gap-3">
          <div className="flex flex-1 flex-col gap-0.5">
            <SectionLabel icon="layers" tone="sky">
              {t("subs.formats")}
            </SectionLabel>
            <span className="text-xs text-muted">{t("subs.formatsHint")}</span>
          </div>
          <button type="button" onClick={() => go("rules")} className="text-xs font-bold text-accent-text">
            {t("subs.fmt.rulesLink")} →
          </button>
        </div>
        <div className="grid gap-3 sm:grid-cols-2">
          {ruleFormats.map((f) => {
            const names = clients.data.filter((c) => c.formats.includes(f)).map((c) => c.name);
            const who =
              f === SubFormat.BASE64_URIS
                ? names.length > 0
                  ? t("subs.fmt.who.base64", { names: names.join(", ") })
                  : t("subs.fmt.who.base64.any")
                : f === SubFormat.USER_PAGE
                  ? t("subs.fmt.who.page")
                  : whoByRules(t, f, rules);
            return (
              <Card key={f} lg className="flex min-w-0 flex-col gap-2 p-4">
                <div className="flex items-center gap-2">
                  <b className="min-w-0 flex-1 truncate text-[15px] tracking-[-0.01em]">{fmtName(f)}</b>
                </div>
                <p className="text-xs leading-normal text-muted">{t(formatDescKey[f]!)}</p>
                <div className="mt-auto flex flex-col gap-1 border-t border-line pt-3">
                  <span className="text-[11px] font-bold tracking-[0.1em] text-muted uppercase">{t("subs.fmt.who")}</span>
                  <span className="text-[13px] leading-snug">{who}</span>
                </div>
              </Card>
            );
          })}
        </div>
      </section>
    </div>
  );
}
