import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { HappPing, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { clientApiQuery } from "@/lib/clientapi";
import { meQuery } from "@/lib/session";
import { ClientApiCard } from "@/screens/subscriptions/client-api";
import { FormatsTab } from "@/screens/subscriptions/formats";
import { LinkAutoCard } from "@/screens/subscriptions/link-auto";
import { LinkAwgCard, LinkAwgOffDialog } from "@/screens/subscriptions/link-awg";
import type { Settings } from "@/screens/subscriptions/model";
import { clientsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { TextsTab } from "@/screens/subscriptions/texts";
import { protocolsQuery } from "@/screens/users/rpc";

// Development only (the dev kit): Subscriptions on made-up settings. /dev-kit?only=subs: "Names & texts" with how Happ
// pings the servers; &state=awg-on|awg-off|awg-ask: the switch "AmneziaWG in apps on the link" on the tab "Who gets
// what", the Mihomo card of "Apps & formats" while it is off, and its confirmation. The blocks carry data-shot for the
// screenshot script. &state=client-api: the card "Client API for the Mistgate app" on the tab "Who gets what", on, off and
// as a helper sees it (read only).

const settings = {
  title: "",
  announcement: "",
  supportUrl: "",
  updateIntervalHours: 12,
  serverNameTemplate: "",
  rules: [
    { uaContains: "mihomo", format: SubFormat.MIHOMO_YAML },
    { uaContains: "clash", format: SubFormat.MIHOMO_YAML },
    { uaContains: "sing-box", format: SubFormat.SINGBOX_JSON },
  ],
  apps: [],
  defaultDnsPresetId: "",
  userPage: { showAnnouncement: true, showSupport: true, showQr: true },
  happPing: HappPing.TCP,
  happPingOnOpen: true,
} as unknown as Settings;

const clients = [
  { id: "happ", name: "Happ", protocols: ["hysteria2", "vless"], formats: [SubFormat.BASE64_URIS, SubFormat.XRAY_JSON] },
  { id: "mihomo", name: "Mihomo (kl!ck, Clash Verge, FlClash)", protocols: ["hysteria2", "vless", "awg"], formats: [SubFormat.MIHOMO_YAML] },
  { id: "amnezia", name: "AmneziaVPN", protocols: ["awg"], formats: [] },
];
const protocols = [
  { id: "hysteria2", displayName: "Hysteria2" },
  { id: "vless", displayName: "VLESS" },
  { id: "awg", displayName: "AmneziaWG" },
];

/** The client API card on its own cache: `enabled` is what the panel says, `owner` is who looks at it. */
function ClientApiShot({ enabled, owner, label }: { enabled: boolean; owner: boolean; label: string }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: owner ? Role.OWNER : Role.HELPER, displayName: owner ? "Owner" : "Helper" }, version: "dev" } as never);
    c.setQueryData(clientApiQuery.queryKey, { enabled });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <div data-shot={`subs-client-api-${owner ? (enabled ? "on" : "off") : "readonly"}`} className="flex flex-col gap-3">
        <SectionLabel>{label}</SectionLabel>
        <ClientApiCard />
      </div>
    </QueryClientProvider>
  );
}

export function SubsKit() {
  const state = new URLSearchParams(location.search).get("state") ?? "";
  const awg = state.startsWith("awg-");
  const shown = { ...settings, linkAwg: state === "awg-off" ? false : undefined } as Settings;
  // &state=many: a group with as many servers as a real fleet gives, so the preview scrolls
  const samples =
    state === "many"
      ? ["nl", "ru", "de", "tr", "fi", "be"].flatMap((cc, i) => ["HY2", "VLESS · XHTTP", "VLESS · TCP"].map((profile) => ({ node: `${cc}1`, countryCode: cc === "be" ? "BY" : cc.toUpperCase(), profile, loadPercent: i + 1 })))
      : [];
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(settingsQuery.queryKey, { settings: shown, effectiveTitle: "Mistgate", samples, sampleGroup: state === "many" ? "Default" : "", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 14 });
    c.setQueryData(clientsQuery.queryKey, clients as never);
    c.setQueryData(protocolsQuery.queryKey, protocols as never);
    return c;
  });
  const [ask, setAsk] = useState(true);
  return (
    <QueryClientProvider client={qc}>
      <section className="relative left-1/2 flex w-[min(calc(100vw-2rem),1080px)] -translate-x-1/2 flex-col gap-6">
        {(state === "client-api" || state === "") && (
          <div className="flex flex-col gap-6">
            <ClientApiShot enabled owner label="Subscriptions → Who gets what: Client API for the Mistgate app, on" />
            <ClientApiShot enabled={false} owner label="Subscriptions → Who gets what: Client API for the Mistgate app, off" />
            <ClientApiShot enabled owner={false} label="Subscriptions → Who gets what: Client API for the Mistgate app, read only (helper)" />
          </div>
        )}
        {!awg && state !== "client-api" && (
          <div data-shot="subs-texts" className="flex flex-col gap-3">
            <SectionLabel>Subscriptions → Names &amp; texts: ping in Happ</SectionLabel>
            <TextsTab data={{ settings, effectiveTitle: "Mistgate", samples, sampleGroup: state === "many" ? "Default" : "" } as never} />
          </div>
        )}
        {awg && (
          <div data-shot="subs-link-awg" className="flex flex-col gap-3">
            <SectionLabel>Subscriptions → Who gets what: automatic server choice, AmneziaWG in apps on the link</SectionLabel>
            <LinkAutoCard settings={shown} />
            <LinkAutoCard settings={{ ...shown, linkAutoSelect: false } as Settings} />
            <LinkAwgCard settings={shown} />
          </div>
        )}
        {state === "awg-off" && (
          <div data-shot="subs-formats-awg-off" className="flex flex-col gap-3">
            <SectionLabel>Subscriptions → Apps &amp; formats, AmneziaWG off for the apps on the link</SectionLabel>
            <FormatsTab go={() => {}} />
          </div>
        )}
        {state === "awg-ask" && <LinkAwgOffDialog open={ask} onOpenChange={setAsk} keys={14} onConfirm={async () => {}} />}
      </section>
    </QueryClientProvider>
  );
}
