import { useQuery } from "@tanstack/react-query";
import { SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { Button } from "@/components/ui/button";
import { Pending, QueryError } from "@/components/ui/query-error";
import { protocolsQuery } from "@/screens/users/rpc";
import { useTx, type Key, type Tx } from "@/screens/users/t";
import { formatDescKey, formatKey, ruleFor, ruleFormats, singboxAgents, type Rule } from "./model";
import { linkAwgOn } from "./link-awg";
import { Glyph, path, Section, Svg } from "./parts";
import { clientsQuery, settingsQuery } from "./queries";

/**
 * "Who gets it" of a format the rules hand out (Mihomo YAML, Xray JSON, sing-box JSON, the fake 404): the rules that pick
 * it, by their text, or nobody while no rule does. The official sing-box apps are named only while the rules really hand
 * every one of them sing-box JSON (an earlier rule or a narrower text would take some away). The link list and the page
 * have their own sentences (every other app, every browser).
 */
export function whoByRules(t: Tx, f: SubFormat, rules: readonly Rule[]) {
  const texts = rules.filter((r) => r.format === f).map((r) => t("common.quoted", { name: r.uaContains }));
  if (texts.length === 0) return t("subs.fmt.who.decoy");
  const list = new Intl.ListFormat(t.locale, { type: "conjunction" }).format(texts);
  const official = f === SubFormat.SINGBOX_JSON && singboxAgents.every((ua) => rules[ruleFor(rules, ua)]?.format === f);
  return t.n(f === SubFormat.MIHOMO_YAML ? "subs.fmt.who.mihomo" : official ? "subs.fmt.who.singbox" : "subs.fmt.who.rules", texts.length, { rules: list });
}

// What a JSON format leaves out of a person's servers (the panel renders nothing for them there), one line per card.
const leftOut: Partial<Record<SubFormat, Key>> = { [SubFormat.XRAY_JSON]: "subs.fmt.xray.out", [SubFormat.SINGBOX_JSON]: "subs.fmt.singbox.out" };
// What an app carries, where the protocol marks alone would promise too much.
const clientNote: Record<string, Key> = { singbox: "subs.clientSingbox" };
// The mark of a format card and its tone, as the board draws them.
const look: Record<number, { tone: "lav" | "mint" | "sky" | "sand" | "rose" | "none"; icon: string }> = {
  [SubFormat.BASE64_URIS]: { tone: "lav", icon: path.file },
  [SubFormat.MIHOMO_YAML]: { tone: "mint", icon: path.file },
  [SubFormat.XRAY_JSON]: { tone: "sky", icon: path.file },
  [SubFormat.SINGBOX_JSON]: { tone: "sand", icon: path.file },
  [SubFormat.USER_PAGE]: { tone: "rose", icon: path.eye },
  [SubFormat.DECOY]: { tone: "none", icon: path.noFile },
};
// The protocols the table has a column for, whatever the apps carry; another protocol an app carries gets a column of its own after them.
const baseProtocols = ["vless", "hysteria2", "awg"];

/** "Apps & formats": which apps we work with and what they read, then what the link can hand out. Read-only; the choice lives in "Who gets what". */
export function FormatsTab({ go }: { go: (tab: string) => void }) {
  const t = useTx();
  const clients = useQuery(clientsQuery);
  const protocols = useQuery(protocolsQuery);
  const settings = useQuery(settingsQuery).data?.settings;
  const rules = settings?.rules ?? [];
  // the owner took AmneziaWG out of the apps on the link: the Mihomo row says so, and where to turn it back on
  const awgOff = settings !== undefined && !linkAwgOn(settings);
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  if (clients.isPending) return <Pending />;
  if (clients.isError) return <QueryError error={clients.error} onRetry={() => void clients.refetch()} />;

  const fmtName = (f: SubFormat) => t(formatKey[f] ?? "subs.fmt.base64");
  const cols = [...baseProtocols, ...[...new Set(clients.data.flatMap((c) => c.protocols))].filter((p) => !baseProtocols.includes(p))];
  const grid = `minmax(160px,1fr) ${cols.map((_, i) => (i === 2 ? "96px" : "88px")).join(" ")} minmax(0,2fr)`;
  const cntOf = (f: SubFormat) => rules.filter((r) => r.format === f).length;

  return (
    <>
      <p className="sb-intro">{t("subs.formats.lead")}</p>
      <Section tone="lav" icon={path.phone} title={t("subs.clients")} shot="subs-apps-table">
        <p className="sb-lead">{t("sx.apps.lead")}</p>
        {clients.data.length === 0 ? (
          <p className="sb-emp">{t("subs.clientsNone")}</p>
        ) : (
          <div className="sb-ax" role="table" aria-label={t("subs.clients")}>
            <div className="sb-ar sb-ah" role="row" style={{ "--sb-cols": grid } as React.CSSProperties}>
              <span role="columnheader">{t("sx.apps.app")}</span>
              {cols.map((p) => (
                <span key={p} role="columnheader">
                  {protoName(p)}
                </span>
              ))}
              <span role="columnheader">{t("subs.clientFormats")}</span>
            </div>
            {clients.data.map((c) => {
              const awgNote = awgOff && c.id === "mihomo" && c.protocols.includes("awg");
              const note = clientNote[c.id] ? t(clientNote[c.id]!) : c.formats.length === 0 ? t("subs.clientNoSub") : "";
              return (
                <div className="sb-ar" role="row" key={c.id} style={{ "--sb-cols": grid } as React.CSSProperties}>
                  <span className="sb-an" role="cell">
                    <i className="sb-mono" aria-hidden="true">
                      {c.name.charAt(0).toUpperCase()}
                    </i>
                    <b>{c.name}</b>
                  </span>
                  {cols.map((p) => {
                    const on = c.protocols.includes(p) && !(awgNote && p === "awg");
                    return (
                      <span key={p} className={on ? "sb-y y" : "sb-y"} data-p={protoName(p)} role="cell" aria-label={`${protoName(p)}: ${t(on ? "sx.apps.yes" : "sx.apps.no")}`}>
                        <Svg d={path.check} />
                      </span>
                    );
                  })}
                  <span className="sb-rd" role="cell">
                    {c.formats.map((f) => (
                      <i key={f}>{fmtName(f)}</i>
                    ))}
                    {note && <em>{note}</em>}
                    {awgNote && (
                      <em>
                        <button type="button" className="sb-go" onClick={() => go("rules")}>
                          {t("subs.linkAwg.chip")} <span aria-hidden="true" className="flip">→</span>
                        </button>
                      </em>
                    )}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </Section>

      <div className="sb-fh">
        <h2>{t("subs.formats")}</h2>
        <span>{t("sx.fmt.sub")}</span>
        <Button className="sb-btn" onClick={() => go("rules")}>
          {t("subs.fmt.rulesLink")}
          <Svg d={path.arrowR} className="flip" />
        </Button>
      </div>
      <div className="sb-fg">
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
          const l = look[f]!;
          return (
            <section className="card sb-fc" key={f}>
              <div className="sb-fct">
                <span className={`pp-ic ${l.tone}`} aria-hidden="true">
                  <Svg d={l.icon} />
                </span>
                <b>{fmtName(f)}</b>
                {cntOf(f) > 0 && <em className="mono">{t.n("sx.fmt.rules", cntOf(f))}</em>}
              </div>
              <p>{t(formatDescKey[f]!)}</p>
              {leftOut[f] && (
                <p className="sb-lim">
                  <Glyph kind="part" />
                  {t(leftOut[f])}
                </p>
              )}
              <div className="sb-fw">
                <span>{t("subs.fmt.who")}</span>
                {who}
              </div>
            </section>
          );
        })}
      </div>
    </>
  );
}
