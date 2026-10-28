import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { ActionBar, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { GetNodeResponse } from "@/gen/mistgate/admin/v1/node_pb";
import { useT } from "@/i18n";
import type { Plain } from "@/lib/plain";
import { meQuery } from "@/lib/session";
import { SwitchRow } from "@/screens/users/ui";
import { nodeDnsMode, nodeDnsResolvers, type NodeDnsMode } from "./dns";
import { DnsOptionsCard } from "./dns-options";
import { useNodeForm, useSaveNode } from "./node-form";
import { WarpCard } from "./warp";

const resolverPattern = /^[0-9a-fA-F.:]{2,45}$/;
const parseResolvers = (s: string) => s.split(/[\s,]+/).filter(Boolean);

/**
 * Exit: where people's traffic leaves the node. WARP first (every "Open WARP" of the panel lands on its `#warp`), then
 * the node's own traffic settings with one Save, then the DNS people are offered on their page, next to the node's DNS.
 * Not shown for a retired node.
 */
export function ExitTab({ data }: { data: Plain<GetNodeResponse> }) {
  const node = data.node!;
  return (
    <div className="grid items-start gap-3.5 xl:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
      <div className="flex min-w-0 flex-col gap-3.5">
        <WarpCard nodeId={node.id} nodeName={node.name} retired={false} />
        {/* keyed by the node: switching nodes starts from that node's values */}
        <TrafficForm key={node.id} data={data} />
      </div>
      <DnsOptionsCard nodeId={node.id} />
    </div>
  );
}

/** People's traffic: the IPv6 exit, BitTorrent and the node's DNS resolvers. */
function TrafficForm({ data }: { data: Plain<GetNodeResponse> }) {
  const t = useT();
  const node = data.node!;
  const owner = useQuery(meQuery).data?.admin?.role === Role.OWNER;
  const { f, set, changed, dirty } = useNodeForm(() => ({
    dns: data.dnsResolvers.join(", "),
    torrentBlockerEnabled: node.torrentBlockerEnabled,
    clientIpv6: node.clientIpv6,
  }));
  const [dnsMode, setDnsMode] = useState<NodeDnsMode>(() => nodeDnsMode(data.dnsResolvers));
  const resolvers = parseResolvers(f.dns);
  const dnsError = dnsMode !== "custom" || (resolvers.length > 0 && resolvers.every((r) => resolverPattern.test(r))) ? undefined : t("node.settings.dnsError");
  // owner only (the API refuses a helper); switching it off needs an agent that follows it, switching it on never does
  const ipv6Locked = !owner || (!node.clientIpv6Supported && f.clientIpv6);

  const save = useSaveNode(node.id, () => ({
    dnsResolvers: changed("dns") ? { values: resolvers } : undefined,
    torrentBlockerEnabled: changed("torrentBlockerEnabled") ? f.torrentBlockerEnabled : undefined,
    clientIpv6: changed("clientIpv6") ? f.clientIpv6 : undefined,
  }));
  function submit(e: FormEvent) {
    e.preventDefault();
    if (dirty && !dnsError) save.mutate();
  }

  const dnsModes: { value: NodeDnsMode; label: string; hint: string }[] = [
    { value: "system", label: t("node.settings.dnsMode.system"), hint: t("node.settings.dnsMode.systemHint") },
    { value: "yandex", label: t("node.settings.dnsMode.yandex"), hint: "77.88.8.8 · 77.88.8.1" },
    { value: "cloudflareGoogle", label: t("node.settings.dnsMode.cloudflareGoogle"), hint: "1.1.1.1 · 8.8.8.8" },
    { value: "custom", label: t("node.settings.dnsMode.custom"), hint: t("node.settings.dnsMode.customHint") },
  ];
  function chooseDnsMode(value: string) {
    const mode = value as NodeDnsMode;
    setDnsMode(mode);
    if (mode !== "custom") set("dns", nodeDnsResolvers(mode).join(", "));
  }
  const note = (text: string) => <span className="mt-1 block">{text}</span>;

  return (
    <form onSubmit={submit} className="flex min-w-0 flex-col rounded-card-lg border border-line bg-surface">
      <SectionLabel as="h2" icon="traffic" tone="mint" className="px-4 pt-4 pb-1 md:px-5">
        {t("node.exit.traffic")}
      </SectionLabel>
      <div className="flex flex-col px-4 md:px-5">
        <SwitchRow
          label={t("node.settings.clientIpv6")}
          hint={
            <>
              {t("node.settings.clientIpv6Hint")}
              {!owner && note(t("node.settings.clientIpv6Owner"))}
              {!node.clientIpv6Supported && note(t(node.clientIpv6 ? "node.settings.clientIpv6Unsupported" : "node.settings.clientIpv6Stale"))}
            </>
          }
          checked={f.clientIpv6}
          onCheckedChange={(on) => set("clientIpv6", on)}
          disabled={ipv6Locked}
          className="py-3"
        />
        <SwitchRow
          label={t("node.settings.torrentBlocker")}
          hint={
            <>
              {t("node.settings.torrentBlockerHint")}
              {!node.torrentBlockerSupported && note(t("node.settings.torrentBlockerUnsupported"))}
            </>
          }
          checked={f.torrentBlockerEnabled}
          onCheckedChange={(on) => set("torrentBlockerEnabled", on)}
          disabled={!node.torrentBlockerSupported}
          className="py-3"
        />
        <div className="flex flex-col gap-2 border-t border-line py-3">
          <span className="text-[13px] font-bold">{t("node.settings.dns")}</span>
          <Select aria-label={t("node.settings.dns")} value={dnsMode} onValueChange={chooseDnsMode} options={dnsModes} />
          <span className="text-[11px] leading-snug text-pretty text-muted">{t("node.settings.dnsHint")}</span>
          {dnsMode === "custom" && (
            <TextField
              tone="sky"
              label={t("node.settings.dnsCustom")}
              value={f.dns}
              onChange={(e) => set("dns", e.target.value)}
              placeholder={t("node.settings.dnsPh")}
              mono
              error={dnsError}
              autoCapitalize="off"
              spellCheck={false}
            />
          )}
        </div>
      </div>
      <ActionBar>
        <Button type="submit" variant="primary" size="md" className="ml-auto" disabled={!dirty || !!dnsError || save.isPending}>
          {t("common.save")}
        </Button>
      </ActionBar>
    </form>
  );
}
