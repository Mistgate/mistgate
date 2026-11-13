import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { HappPing, SubFormat } from "@/gen/mistgate/admin/v1/subscription_pb";
import { clientApiQuery } from "@/lib/clientapi";
import { ClientCompatActual, ClientCompatState, clientCompatQuery, outdatedDevicesQuery, type CompatDevice, type CompatRule } from "@/lib/clientcompat";
import { meQuery } from "@/lib/session";
import { ClientApiCard } from "@/screens/subscriptions/client-api";
import { ClientsTab } from "@/screens/subscriptions/clients";
import { FormatsTab } from "@/screens/subscriptions/formats";
import { DeviceIdleCard } from "@/screens/subscriptions/device-idle";
import { LinkAutoCard } from "@/screens/subscriptions/link-auto";
import { LinkAwgCard, LinkAwgOffDialog } from "@/screens/subscriptions/link-awg";
import type { Settings } from "@/screens/subscriptions/model";
import { clientsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { TextsTab } from "@/screens/subscriptions/texts";
import { protocolsQuery } from "@/screens/users/rpc";

// Development only (the dev kit): Subscriptions on made-up settings. /dev-kit?only=subs: "Names & texts" with how Happ
// pings the servers; &state=awg-on|awg-off|awg-ask: the switch "AmneziaWG in apps on the link" on the tab "Who gets
// what", the Mihomo card of "Apps & formats" while it is off, and its confirmation. The blocks carry data-shot for the
// screenshot script. &state=client-api: the card "Mistgate app" on the tab "Who gets what" (with the summary of people and devices), on, off, nobody connected yet and
// as a helper sees it (read only). &state=clients|clients-empty|clients-readonly|clients-rules: the tab "App versions" with
// devices of every state and of every class of what the nodes saw (data-shot subs-clients, and subs-clients-classic with the chip "Old-style" chosen: the OK device there is the table's mistake), with no device that gets REALITY servers, as a helper sees it (the table of minimum versions only
// read), and with that table unfolded for the owner (one row changed from the built-in one, one of their own).

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
function ClientApiShot({ enabled, owner, label, empty }: { enabled: boolean; owner: boolean; label: string; empty?: boolean }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: owner ? Role.OWNER : Role.HELPER, displayName: owner ? "Owner" : "Helper" }, version: "dev" } as never);
    c.setQueryData(clientApiQuery.queryKey, { enabled, stats: empty ? { people: 24, peopleOn: 0, devices: 0 } : { people: 24, peopleOn: 17, devices: 31 } });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <div data-shot={`subs-client-api-${owner ? (empty ? "empty" : enabled ? "on" : "off") : "readonly"}`} className="flex flex-col gap-3">
        <SectionLabel>{label}</SectionLabel>
        <ClientApiCard />
      </div>
    </QueryClientProvider>
  );
}

const ago = (s: number) => Math.floor(Date.now() / 1000) - s;
const dev = (o: Partial<CompatDevice> & Pick<CompatDevice, "userId" | "userName" | "state" | "reason">): CompatDevice => ({
  deviceId: `${o.userId}-1`,
  client: "",
  platform: "",
  model: "",
  osVersion: "",
  shared: false,
  lastSeenUnix: 0,
  needVersion: "",
  fingerprint: "",
  appTitle: "",
  actual: ClientCompatActual.NONE,
  helloPq: 0,
  helloClassic: 0,
  helloUnknown: 0,
  tableMismatch: "",
  ...o,
});
const B = ClientCompatState.BELOW_MINIMUM;
const U = ClientCompatState.UNSUPPORTED;
const K = ClientCompatState.UNKNOWN;
const PQ = ClientCompatActual.PQ;
const CL = ClientCompatActual.CLASSIC;
const MX = ClientCompatActual.MIXED;
const compatDevices: CompatDevice[] = [
  dev({ userId: "alice", userName: "alice", state: B, reason: "version", needVersion: "3.1.1", client: "Happ/3.0.2", appTitle: "Happ", platform: "android", model: "Pixel 8", osVersion: "15", lastSeenUnix: ago(420), actual: PQ, helloPq: 41, tableMismatch: "better" }),
  dev({ userId: "alice", userName: "alice", deviceId: "alice-2", state: U, reason: "fingerprint", fingerprint: "random", client: "Mihomo/1.19.12", appTitle: "Mihomo", platform: "windows", model: "ThinkPad T14", osVersion: "11", lastSeenUnix: ago(3 * 3600), actual: CL, helloClassic: 17 }),
  dev({ userId: "bob", userName: "bob", state: U, reason: "app", client: "SFI/1.14.3", appTitle: "sing-box (SFA, SFI, SFM)", platform: "ios", model: "iPhone 15", osVersion: "18.1", lastSeenUnix: ago(86400 * 2), actual: CL, helloClassic: 6, helloUnknown: 1 }),
  dev({ userId: "carol", userName: "carol", deviceId: "", shared: true, state: K, reason: "app_unknown", client: "NovaVPN/1.2", lastSeenUnix: ago(86400), actual: MX, helloPq: 8, helloClassic: 3 }),
  dev({ userId: "dave", userName: "dave", state: B, reason: "version", needVersion: "1.9.30", client: "v2rayNG/1.8.5", appTitle: "v2rayNG", platform: "android", model: "Galaxy S23", osVersion: "14", lastSeenUnix: ago(86400 * 6), actual: PQ, helloPq: 3, tableMismatch: "better" }),
  dev({ userId: "erin", userName: "erin", state: K, reason: "no_version", client: "FlClash", appTitle: "FlClash", platform: "macos", model: "MacBook Air", osVersion: "15.0", lastSeenUnix: ago(1800) }),
  dev({ userId: "frank", userName: "frank", state: K, reason: "no_threshold", client: "Happ/3.4.2", appTitle: "Happ", platform: "ios", model: "iPhone 13", osVersion: "17.5", lastSeenUnix: ago(86400 * 11) }),
  dev({ userId: "grace", userName: "grace", state: K, reason: "no_version", platform: "linux", model: "Laptop", lastSeenUnix: 0 }),
  dev({ userId: "heidi", userName: "heidi", state: U, reason: "app", client: "Karing/1.2.3", appTitle: "Karing", platform: "ios", model: "iPad Air", osVersion: "17.2", lastSeenUnix: ago(86400 * 20) }),
];
const count = (state: ClientCompatState, devices: number, people: number) => ({ state, devices, people });
const compatCounts = [count(ClientCompatState.OK, 14, 6), count(B, 2, 2), count(U, 3, 3), count(K, 4, 4)];
// What the nodes saw, over all devices: new-style 12, old-style 3, mixed 1, no data 9; two disagreements with the table (an OK device that sends
// old-style handshakes, a device below the minimum that sends new-style ones), and one node whose agent does not count yet.
const actualCount = (actual: ClientCompatActual, devices: number, people: number) => ({ actual, devices, people });
const compatActual = [actualCount(ClientCompatActual.NONE, 9, 5), actualCount(PQ, 12, 6), actualCount(CL, 3, 2), actualCount(MX, 1, 1)];
const compatPage = (devices: CompatDevice[], counts = compatCounts, actualCounts = compatActual, o: { worse?: number; better?: number; nodes?: string[] } = {}) => ({
  devices,
  total: devices.length,
  counts,
  fingerprints: ["chrome", "firefox"],
  actualCounts,
  helloWindowHours: 168,
  mismatchWorse: o.worse ?? 1,
  mismatchBetter: o.better ?? 2,
  helloKexNodesWithout: o.nodes ?? ["nl2"],
});
// the "In fact" view: every state, the devices that sent old-style or mixed handshakes (the table's OK device among them)
const classicDevices: CompatDevice[] = [
  dev({ userId: "ivan", userName: "ivan", deviceId: "ivan-1", state: ClientCompatState.OK, reason: "", client: "Happ/3.4.2", appTitle: "Happ", platform: "android", model: "Pixel 7", osVersion: "15", lastSeenUnix: ago(600), actual: CL, helloClassic: 9, tableMismatch: "worse" }),
  compatDevices[1]!,
  compatDevices[2]!,
];
const rule = (app: string, platform: string, core: string, minChrome: string, minOther: string, o: Partial<CompatRule> = {}): CompatRule => ({ app, platform, core, unsupported: false, minChrome, minOther, title: o.title ?? app, ...o });
const compatDefaults: CompatRule[] = [
  rule("happ", "", "xray", "3.1.1", "3.1.1", { title: "Happ" }),
  rule("mihomo", "", "mihomo", "1.19.31", "1.19.31", { title: "Mihomo" }),
  rule("v2rayng", "android", "xray", "1.9.30", "1.9.30", { title: "v2rayNG" }),
  rule("karing", "", "singbox", "", "", { title: "Karing", unsupported: true }),
  rule("flclash", "", "mihomo", "0.8.90", "", { title: "FlClash" }),
];
const compatRules: CompatRule[] = [compatDefaults[0]!, { ...compatDefaults[1]!, minOther: "1.19.40" }, ...compatDefaults.slice(2), rule("novavpn", "", "unknown", "1.4", "", { title: "novavpn" })];

/**
 * The "App versions" tab on its own cache: the devices of the first page (everything but OK, or the class `actual` asked for), the
 * table of minimum versions.
 */
function ClientsShot({ owner, empty, rulesOpen, actual = ClientCompatActual.UNSPECIFIED, shot, label }: { owner: boolean; empty?: boolean; rulesOpen?: boolean; actual?: ClientCompatActual; shot: string; label: string }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: owner ? Role.OWNER : Role.HELPER, displayName: owner ? "Owner" : "Helper" }, version: "dev" } as never);
    const zero = [count(ClientCompatState.OK, 0, 0), count(B, 0, 0), count(U, 0, 0), count(K, 0, 0)];
    const none = compatActual.map((a) => ({ ...a, devices: 0, people: 0 }));
    const page = empty ? compatPage([], zero, none, { worse: 0, better: 0, nodes: [] }) : compatPage(actual === ClientCompatActual.CLASSIC ? classicDevices : compatDevices);
    c.setQueryData(outdatedDevicesQuery({ state: ClientCompatState.UNSPECIFIED, actual, userId: "", page: 1, size: 50 }).queryKey, page as never);
    c.setQueryData(clientCompatQuery.queryKey, { rules: compatRules, defaults: compatDefaults });
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <div data-shot={shot} className="flex flex-col gap-3">
        <SectionLabel>{label}</SectionLabel>
        <ClientsTab rulesOpen={rulesOpen} initialActual={actual} />
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
            <ClientApiShot enabled owner label="Subscriptions → Who gets what: the Mistgate app, on, with the summary" />
            <ClientApiShot enabled={false} owner label="Subscriptions → Who gets what: the Mistgate app, off for the whole panel" />
            <ClientApiShot enabled owner={false} label="Subscriptions → Who gets what: the Mistgate app, read only (helper)" />
            <ClientApiShot enabled owner empty label="Subscriptions → Who gets what: the Mistgate app, on, nobody has connected yet" />
          </div>
        )}
        {state === "clients" && (
          <>
            <ClientsShot owner shot="subs-clients" label="Subscriptions → App versions: devices of every state with what the nodes saw (a device new-style in fact but below the minimum by the table, one that is old-style in fact, a mixed one, one nobody measured), the owner" />
            <ClientsShot owner actual={ClientCompatActual.CLASSIC} shot="subs-clients-classic" label="Subscriptions → App versions: the chip “Old-style” chosen; the OK device that sends old-style handshakes is the table’s mistake" />
          </>
        )}
        {state === "clients-empty" && <ClientsShot owner empty shot="subs-clients-empty" label="Subscriptions → App versions: no device gets REALITY servers yet" />}
        {state === "clients-readonly" && <ClientsShot owner={false} rulesOpen shot="subs-clients-readonly" label="Subscriptions → App versions, as a helper sees it (the table of minimum versions is read only)" />}
        {state === "clients-rules" && <ClientsShot owner rulesOpen shot="subs-clients-rules" label="Subscriptions → App versions: the table of minimum versions unfolded (one row changed, one of the owner’s own)" />}
        {state === "idle" && (
          <div data-shot="subs-device-idle" className="flex flex-col gap-3">
            <SectionLabel>Subscriptions → Who gets what: inactive devices, on (30 days), on (7 days) and off</SectionLabel>
            <DeviceIdleCard settings={{ ...shown, deviceIdleAfterDays: 30 } as Settings} />
            <DeviceIdleCard settings={{ ...shown, deviceIdleAfterDays: 7 } as Settings} />
            <DeviceIdleCard settings={{ ...shown, deviceIdleAfterDays: 0 } as Settings} />
          </div>
        )}
        {!awg && state !== "client-api" && state !== "idle" && !state.startsWith("clients") && (
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
