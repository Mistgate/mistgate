import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { meQuery } from "@/lib/session";
import type { Settings } from "@/screens/subscriptions/model";
import { PageTab } from "@/screens/subscriptions/page";
import { dnsPresetsQuery, pickerUsersQuery } from "@/screens/subscriptions/queries";
import { DnsChoicesPanel } from "@/screens/users/user-detail";

// Development only (the /dev-kit page): the DNS of the apps on the link, as the owner offers it (Subscriptions → User page) and as
// the user card shows a person's picks. Made-up data: alice, nodes de1 and nl1, the built-in presets.

type Seed = [readonly unknown[], unknown][];

function Shot({ id, title, seed, children }: { id: string; title: string; seed: Seed; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false } } });
    for (const [key, data] of seed) c.setQueryData(key, data);
    return c;
  });
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </section>
  );
}

const preset = (id: string, name: string) => ({ id, name, description: "", builtin: true, servers: [], split: [], splitDirect: false, ipv4Only: false, users: 0, isDefault: false });
const presets = {
  presets: [
    preset("dns_builtin_ru_split", "Россия: .ru напрямую"),
    preset("dns_builtin_standard", "Cloudflare + Google"),
    preset("dns_builtin_adblock", "AdGuard: без рекламы"),
    preset("dns_builtin_family", "AdGuard Family: без рекламы и 18+"),
    preset("dns_builtin_quad9", "Quad9: защита от вредоносных"),
  ],
  providers: [],
  clientSupport: [],
};
const owner: Seed[number] = [meQuery.queryKey, { admin: { id: "adm_1", displayName: "alice", role: Role.OWNER }, version: "0.1.0-dev", stepUpUntilUnix: 0n }];
const app = (platform: Platform, kind: App, name: string) => ({ platform, kind, name, downloadUrl: "https://example.com/" + name, addLinkTemplate: "", description: "", recommended: false });

const settings = {
  title: "",
  announcement: "",
  supportUrl: "",
  updateIntervalHours: 12,
  serverNameTemplate: "{flag} {country} · {profile}",
  rules: [],
  defaultDnsPresetId: "",
  apps: [app(Platform.IOS, App.HAPP, "Happ"), app(Platform.IOS, App.AMNEZIA, "AmneziaVPN")],
  userPage: { showAnnouncement: true, showSupport: true, showQr: true, allowDeviceSelfService: true, requirePagePassword: true, allowDnsChoice: true },
  linkDns: { presetIds: ["dns_builtin_standard", "dns_builtin_adblock", "dns_builtin_family", "dns_builtin_ru_split"], defaultPresetId: "dns_builtin_standard" },
} as unknown as Settings;

const pick = (nodeId: string, nodeName: string, presetId: string, presetName: string, over: object = {}) => ({
  nodeId, nodeName, presetId, presetName, offered: true, effectivePresetId: presetId, effectivePresetName: presetName, updatedUnix: 0n, ...over,
});
const choices = {
  choices: [
    pick("nod_de1", "de1", "dns_builtin_family", "AdGuard Family: без рекламы и 18+"),
    pick("nod_nl1", "nl1", "dns_builtin_quad9", "Quad9: защита от вредоносных", { offered: false, effectivePresetId: "dns_builtin_standard", effectivePresetName: "Cloudflare + Google" }),
  ],
  link: pick("", "", "dns_builtin_adblock", "AdGuard: без рекламы"),
};

export function DnsLinkKit() {
  return (
    <div className="flex flex-col gap-8">
      <Shot id="dns-link-offer" title="Subscriptions → User page: the DNS offered for the apps on the link, under the switch" seed={[owner, [dnsPresetsQuery.queryKey, presets], [pickerUsersQuery.queryKey, []]]}>
        <PageTab settings={settings} />
      </Shot>
      <Shot id="dns-link-user-card" title="User card: the DNS alice picked on her page, for the apps on the link and per server" seed={[owner, [dnsPresetsQuery.queryKey, presets], [["users", "dns-choices", "usr_alice"], choices]]}>
        <div className="max-w-md">
          <DnsChoicesPanel user={{ id: "usr_alice", name: "alice" }} actions={{ refresh: async () => {} }} />
        </div>
      </Shot>
    </div>
  );
}
