import { useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { fieldClass } from "@/components/ui/text-field";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { agentLinked } from "@/lib/node-status";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { Glyph } from "@/screens/overview-parts";
import { nodeDnsMode, nodeDnsPresets, nodeDnsResolvers } from "./dns";
import { DnsOptionsCard } from "./dns-options";
import { useNodeForm, useSaveSection } from "./node-form";
import { SvSave } from "./server-parts";
import { WarpCard } from "./warp";
import "./exit.css";

const resolverPattern = /^[0-9a-fA-F.:]{2,45}$/;
const parseResolvers = (s: string) => s.split(/[\s,]+/).filter(Boolean);
const iconIpv6 = "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18";
const iconTorrent = "M5 5l14 14M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z";

/**
 * Exit: where people's traffic leaves the node. WARP first (every "Open WARP" of the panel lands on its `#warp`), the node's own
 * traffic settings and DNS beside it, then the DNS the node offers on people's pages. Not shown for a retired node.
 */
export function ExitTab({ data }: { data: Plain<GetNodeResponse> }) {
  const node = data.node!;
  return (
    <div className="nx">
      <WarpCard nodeId={node.id} nodeName={node.name} retired={false} down={!agentLinked(node.status)} profiles={data.inbounds.length} />
      {/* keyed by the node: switching nodes starts from that node's values */}
      <TrafficCards key={node.id} data={data} />
      <DnsOptionsCard nodeId={node.id} />
    </div>
  );
}

/** A card of the tab: a hairline frame with its head. */
function NxCard({ title, aside, children, className, ...rest }: { title: ReactNode; aside?: ReactNode; children: ReactNode; className?: string } & Omit<React.HTMLAttributes<HTMLElement>, "title" | "className">) {
  return (
    <section {...rest} className={cx("relative overflow-hidden rounded-card border border-line bg-surface", className)}>
      <div className="nx-ch">
        <h3>{title}</h3>
        {aside}
      </div>
      {children}
    </section>
  );
}

/** People's traffic (the IPv6 exit, BitTorrent) and the node's own DNS: two cards over one form, a bar in each. */
function TrafficCards({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const node = data.node!;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const form = useNodeForm(() => ({
    dns: data.dnsResolvers.join(", "),
    torrentBlockerEnabled: node.torrentBlockerEnabled,
    clientIpv6: node.clientIpv6,
  }));
  const { f, set, changed } = form;
  // the node's DNS is the server's own resolver, or the owner's: the presets of the board's two tabs are a quick way to fill the second
  const [own, setOwn] = useState(() => nodeDnsMode(data.dnsResolvers) !== "system");
  const resolvers = parseResolvers(f.dns);
  const dnsError = !own || (resolvers.length > 0 && resolvers.every((r) => resolverPattern.test(r))) ? undefined : t("node.settings.dnsError");
  // owner only (the API refuses a helper); switching it off needs an agent that follows it, switching it on never does
  const ipv6Locked = !owner || (!node.clientIpv6Supported && f.clientIpv6);

  const traffic = useSaveSection(node.id, form, ["torrentBlockerEnabled", "clientIpv6"], () => ({
    torrentBlockerEnabled: changed("torrentBlockerEnabled") ? f.torrentBlockerEnabled : undefined,
    clientIpv6: changed("clientIpv6") ? f.clientIpv6 : undefined,
  }));
  const dns = useSaveSection(node.id, form, ["dns"], () => ({ dnsResolvers: changed("dns") ? { values: resolvers } : undefined }));

  function chooseMode(value: "system" | "own") {
    setOwn(value === "own");
    if (value === "system") set("dns", "");
  }
  const current = nodeDnsMode(resolvers);
  const note = (text: string, warn?: boolean) => <span className={warn ? "w" : undefined}>{text}</span>;

  return (
    <div className="nx-col">
      <NxCard title={t("node.exit.traffic")} aria-label={t("node.exit.traffic")}>
        <label className="nx-opt">
          <span className="g" aria-hidden>
            <Glyph d={iconIpv6} size={16} stroke={1.5} />
          </span>
          <div>
            <b>{t("node.settings.clientIpv6")}</b>
            <span>{t("node.settings.clientIpv6Hint")}</span>
            {!owner && note(t("node.settings.clientIpv6Owner"))}
            {!node.clientIpv6Supported && note(t(node.clientIpv6 ? "node.settings.clientIpv6Unsupported" : "node.settings.clientIpv6Stale"), true)}
          </div>
          <Switch checked={f.clientIpv6} onCheckedChange={(on) => set("clientIpv6", on)} disabled={ipv6Locked} />
        </label>
        <label className="nx-opt">
          <span className="g" aria-hidden>
            <Glyph d={iconTorrent} size={16} stroke={1.5} />
          </span>
          <div>
            <b>{t("node.settings.torrentBlocker")}</b>
            <span>{t("node.settings.torrentBlockerHint")}</span>
            {!node.torrentBlockerSupported && note(t("node.settings.torrentBlockerUnsupported"), true)}
          </div>
          <Switch checked={f.torrentBlockerEnabled} onCheckedChange={(on) => set("torrentBlockerEnabled", on)} disabled={!node.torrentBlockerSupported} />
        </label>
        <SvSave dirty={traffic.dirty} saved={traffic.saved} busy={traffic.busy} onCancel={traffic.cancel} onSave={traffic.save} />
      </NxCard>

      <NxCard
        title={t("np.ex.dns.title")}
        aria-label={t("np.ex.dns.title")}
        aside={
          <Segmented
            aria-label={t("node.settings.dns")}
            value={own ? "own" : "system"}
            onValueChange={chooseMode}
            options={[
              { value: "system", label: t("np.ex.dns.system") },
              { value: "own", label: t("np.ex.dns.own") },
            ]}
            className="ms-auto flex-none"
          />
        }
      >
        {!own ? (
          <p className="nx-p">{t("np.ex.dns.systemHint")}</p>
        ) : (
          <div className="nx-dns">
            <label htmlFor={`nx-dns-${node.id}`}>{t("node.settings.dnsCustom")}</label>
            <input
              id={`nx-dns-${node.id}`}
              className={cx(fieldClass, "h-10 px-3 font-mono text-[13px]")}
              dir="ltr"
              value={f.dns}
              onChange={(e) => set("dns", e.target.value)}
              placeholder={t("node.settings.dnsPh")}
              autoCapitalize="off"
              spellCheck={false}
              data-invalid={dnsError ? "" : undefined}
            />
            <div className="chips" role="group" aria-label={t("np.ex.dns.presets")}>
              {(["yandex", "cloudflareGoogle"] as const).map((m) => (
                <button key={m} type="button" aria-pressed={current === m} title={nodeDnsPresets[m].join(" · ")} onClick={() => set("dns", nodeDnsResolvers(m).join(", "))}>
                  {t(`node.settings.dnsMode.${m}`)}
                </button>
              ))}
            </div>
            {dnsError ? <span className="!text-danger-text">{dnsError}</span> : <span>{t("np.ex.dns.hint")}</span>}
          </div>
        )}
        <SvSave dirty={dns.dirty} saved={dns.saved} busy={dns.busy} invalid={!!dnsError} onCancel={() => (dns.cancel(), setOwn(nodeDnsMode(data.dnsResolvers) !== "system"))} onSave={dns.save} />
      </NxCard>
    </div>
  );
}
