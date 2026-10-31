import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DnsCategory, DnsClient, DnsTransport } from "@/gen/mistgate/admin/v1/dns_pb";
import { Platform } from "@/gen/mistgate/admin/v1/subscription_pb";
import { meQuery } from "@/lib/session";
import { DnsOptionsCard, nodeDnsOptionsQuery } from "@/screens/node/dns-options";
import { DnsTab } from "@/screens/subscriptions/dns";
import type { DnsData, Preset, Settings } from "@/screens/subscriptions/model";
import { PageTab } from "@/screens/subscriptions/page";
import { dnsPresetsQuery, pickerUsersQuery, settingsQuery } from "@/screens/subscriptions/queries";
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

// ?only=ads: Subscriptions → DNS with the built-in "No ads on the device" and its badge (a card opens the editor with the
// switch), and a node's "DNS choice on the user page" with the AmneziaWG line.
const variant = (id: string, nameEn: string, ipv4: string[], noteEn: string, category = DnsCategory.REGULAR) => ({
  id, nameRu: nameEn, nameEn, category, country: "", ipv4, ipv6: [], plain: true, dotHost: "dns.example.com", dotPort: 853, dohUrl: "https://dns.example.com/dns-query", noteRu: noteEn, noteEn,
});
const cf = { providerVariant: "cloudflare/standard", kind: 1, address: "1.1.1.1" };
const google = { providerVariant: "google/standard", kind: 1, address: "8.8.8.8" };
const full = (p: Partial<Preset> & { id: string; name: string }): Preset =>
  ({ description: "", builtin: true, servers: [cf, google], split: [], ipv4Only: false, userCount: 0, isDefault: false, splitDirect: false, preferredTransport: DnsTransport.PLAIN, category: DnsCategory.REGULAR, country: "", blockAds: false, ...p }) as Preset;
const ruSplit = [{ suffixes: [".ru", ".su", ".xn--p1ai"], geo: [], servers: [{ providerVariant: "yandex/basic", kind: 1, address: "77.88.8.8" }] }];
const adsData = {
  presets: [
    full({ id: "dns_builtin_ru_split", name: "Россия: .ru напрямую", description: "Российские сайты напрямую, остальное через VPN\nRussian sites direct, everything else through the VPN", split: ruSplit, splitDirect: true, isDefault: true, userCount: 12, blockAds: true, category: DnsCategory.REGIONAL, country: "RU" }),
    full({ id: "dns_builtin_standard", name: "Cloudflare + Google", description: "Cloudflare и Google для всего\nCloudflare and Google for everything", userCount: 3 }),
    full({ id: "dns_builtin_adblock", name: "AdGuard: без рекламы", description: "AdGuard DNS: без рекламы и трекеров\nAdGuard DNS: no ads, no trackers", servers: [{ providerVariant: "adguard/default", kind: 1, address: "94.140.14.14" }], category: DnsCategory.NO_ADS }),
    full({ id: "dns_builtin_ads_device", name: "Без рекламы на устройстве", description: "Приложение само режет рекламу и трекеры по списку category-ads-all; DNS — Cloudflare и Google\nThe app drops ads and trackers by the category-ads-all list; Cloudflare and Google for DNS", blockAds: true, userCount: 1, category: DnsCategory.NO_ADS }),
    full({ id: "dns_office", name: "Office: .ru direct, no ads", description: "For the office in Moscow", builtin: false, split: ruSplit, splitDirect: true, blockAds: true, userCount: 4, category: DnsCategory.REGIONAL, country: "RU" }),
  ],
  providers: [
    { id: "cloudflare", name: "Cloudflare", variants: [variant("cloudflare/standard", "Standard", ["1.1.1.1", "1.0.0.1"], "Fast, no filtering")] },
    { id: "google", name: "Google", variants: [variant("google/standard", "Standard", ["8.8.8.8", "8.8.4.4"], "No filtering")] },
    { id: "adguard", name: "AdGuard", variants: [variant("adguard/default", "Default", ["94.140.14.14", "94.140.15.15"], "Blocks ads and trackers", DnsCategory.NO_ADS)] },
    { id: "yandex", name: "Yandex", variants: [variant("yandex/basic", "Basic", ["77.88.8.8", "77.88.8.1"], "No filtering")] },
  ],
  clientSupport: [
    { client: DnsClient.HAPP, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
    { client: DnsClient.MIHOMO, transports: [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN], ipv6: true },
    { client: DnsClient.XRAY, transports: [DnsTransport.DOH, DnsTransport.PLAIN], ipv6: false },
    { client: DnsClient.SINGBOX, transports: [DnsTransport.DOH, DnsTransport.DOT, DnsTransport.PLAIN], ipv6: false },
    { client: DnsClient.AMNEZIAWG, transports: [DnsTransport.PLAIN], ipv6: false },
  ],
} as unknown as DnsData;

export function DnsAdsKit() {
  const seed: Seed = [owner, [dnsPresetsQuery.queryKey, adsData], [settingsQuery.queryKey, { settings, effectiveTitle: "" }]];
  return (
    <div className="flex flex-col gap-8">
      <Shot id="ads-list" title="Subscriptions → DNS: the presets, the built-in No ads on the device with its badge" seed={seed}>
        <DnsTab />
      </Shot>
      <Shot id="ads-node" title="Node → DNS choice on the user page: AmneziaWG keys do not block ads" seed={[...seed, [nodeDnsOptionsQuery.queryKey, [{ nodeId: "nod_de1", presetIds: ["dns_builtin_standard", "dns_builtin_ads_device"], defaultPresetId: "dns_builtin_standard" }]]]}>
        <div className="max-w-2xl">
          <DnsOptionsCard nodeId="nod_de1" />
        </div>
      </Shot>
    </div>
  );
}
