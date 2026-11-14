import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useEffect, useState, type ReactNode } from "react";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { DeviceConfig } from "@/gen/mistgate/admin/v1/device_pb";
import { CountryVerdict } from "@/gen/mistgate/admin/v1/health_pb";
import { DeviceKind } from "@/gen/mistgate/admin/v1/user_pb";
import { userConnectionQuery } from "@/lib/connections";
import { countryQuery } from "@/lib/globalping";
import { alertsQuery, checksQuery, doctorQuery } from "@/lib/health";
import { nodesQuery } from "@/lib/queries";
import { candidatesQuery, relaysQuery } from "@/lib/relay";
import { meQuery } from "@/lib/session";
import { AddRelayDialog, ChangeRelayPort } from "@/screens/node/relays";
import { useWays, WaysTab } from "@/screens/node/ways";
import { settingsQuery } from "@/screens/subscriptions/queries";
import { DeviceConfigsModal, type DeviceRef } from "@/screens/users/awg-devices";
import { ConnectionPanel } from "@/screens/users/user-detail";

// Development only, /dev-kit?only=awg-relay&state=…: AmneziaWG through a relay (awg-relay-plan §2.3, §9), on made-up data and a
// private query cache (no server). Names: exits de1, nl1, de2 (also in Germany), the entry msk1 (RU), the exit ru2 in Russia as
// the entry's own country, the person alice.
//   dialog         "Relay through msk1": an AmneziaWG server picked, with the note about the keys
//   same-country   the same dialog: ru2 sits in the entry's country, so it is under "Other nodes" with its reason
//   pick           de1's "New way in" → "Through another node": de2 is in the exit's country, so it is no good fit
//   port           "Change port" of an AmneziaWG relay with its warning
//   device         the key of a device in AmneziaVPN: the direct config, with the path through RU in its list
//   device-via     the same, with the path through RU picked (its note and QR code)
//   history        "Connection" of alice's card: a direct row and a row "via RU" of the same key
// Open the page at 1440 and at 390 px, in both themes.

const now = Math.floor(Date.now() / 1000);
const min = 60;
const params = new URLSearchParams(location.search);

const nodes = [
  { id: "nod_msk1", name: "msk1", countryCode: "RU", status: NodeStatus.ONLINE },
  { id: "nod_de1", name: "de1", countryCode: "DE", status: NodeStatus.ONLINE },
  { id: "nod_de2", name: "de2", countryCode: "DE", status: NodeStatus.ONLINE },
  { id: "nod_nl1", name: "nl1", countryCode: "NL", status: NodeStatus.ONLINE },
  { id: "nod_fi1", name: "fi1", countryCode: "FI", status: NodeStatus.ONLINE },
  { id: "nod_ru2", name: "ru2", countryCode: "RU", status: NodeStatus.ONLINE },
];
const entry = { id: "nod_msk1", name: "msk1", cc: "RU" };

const cut = { verdict: "partial", dark: ["MTS"], atUnix: now };
const cand = (over: Record<string, unknown>) => ({
  inboundId: "inb_x", nodeId: "nod_de1", nodeName: "de1", profileId: "prf_x", profileName: "VLESS · TCP", protocol: "vless", network: "tcp", port: 443, unsupported: "", hop: false, relayId: "",
  country: cut, reach: { verdict: "reach", source: "tspu_row", atUnix: now }, ...over,
});
const awg = (over: Record<string, unknown>) => cand({ profileId: "prf_awg", profileName: "AWG 3.1", protocol: "awg", network: "udp", port: 51820, ...over });
const seen = { verdict: "ok", dark: [], atUnix: now };
const candidates = {
  agentSupported: true,
  entryCountry: seen,
  candidates: [
    cand({ inboundId: "inb_v", profileName: "VLESS · Reality" }),
    awg({ inboundId: "inb_awg_de1" }),
    awg({ inboundId: "inb_awg_nl1", nodeId: "nod_nl1", nodeName: "nl1", country: seen, reach: { verdict: "no_data", source: "", atUnix: 0 } }),
    awg({ inboundId: "inb_awg_ru2", nodeId: "nod_ru2", nodeName: "ru2" }),
  ],
};

const vantage = (network: string, state: string) => ({ source: "globalping", key: network, network, region: "", asn: "", kind: "mobile", state, atUnix: now - 10 * min, dpiHop: 0, icmpOk: state === "reach", tcpOk: state === "reach" });
const country = (nodeId: string, verdict: CountryVerdict, dark: string[] = []) => ({
  nodeId, country: "RU", verdict, atUnix: now - 10 * min, probes: [], reason: "", eyeball: true, confidence: "eyeball", listedBy: [], cdnProvider: "", cdnRegion: "", sources: ["globalping"],
  globalpingVerdict: verdict, tspuVerdict: CountryVerdict.UNSPECIFIED, tspuPorts: [], dpiHop: 0, tspuVantageId: "", tspuVantageName: "", tspuAtUnix: 0, noProbe: [],
  vantages: [...dark.map((n) => vantage(n, "dark")), vantage("Rostelecom", "reach")], sourceVerdicts: [],
});

function Mock({ fill = [], children }: { fill?: [readonly unknown[], unknown][]; children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER } } as never);
    c.setQueryData(nodesQuery.queryKey, { nodes } as never);
    c.setQueryData(countryQuery.queryKey, { country: "RU" } as never);
    c.setQueryData(settingsQuery.queryKey, { settings: { updateIntervalHours: 12, apps: [] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0, linkAwgKeys: 0 } as never);
    c.setQueryData(candidatesQuery("nod_msk1").queryKey, candidates as never);
    c.setQueryData(relaysQuery("nod_de1").queryKey, [] as never);
    c.setQueryData(alertsQuery.queryKey, { nowUnix: now, active: [], history: [] } as never);
    for (const [key, value] of fill) c.setQueryData(key, value as never);
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  );
}

// ---- the relay dialog ----

function Dialog({ same = false }: { same?: boolean }) {
  // the AmneziaWG server of de1 is ticked from the start: its note is what the scene is about
  return <AddRelayDialog open onOpenChange={() => {}} entry={entry} preselect={same ? undefined : "inb_awg_de1"} />;
}

// ---- "New way in" of de1, the step with the entries ----

const de1Page = {
  node: { id: "nod_de1", name: "de1", countryCode: "DE", address: "de1.example.com", status: NodeStatus.ONLINE, bandwidthMbps: 0, ipv6: { entry: "", entrySupported: false, unavailableReason: "no_ipv6", addresses: [], src: "" } },
  inbounds: [{ id: "inb_awg_de1", port: 51820, state: InboundState.ACTIVE, protocol: "awg", profileName: "AWG 3.1" }],
  portChecks: [],
  tcpChecks: [],
  facts: { hasIpv6: false },
} as never;

function Pick() {
  const w = useWays(de1Page);
  // the dialog opens on "what kind of way": step on to the entries, as a click would
  useEffect(() => {
    const id = window.setInterval(() => {
      const b = document.querySelector<HTMLButtonElement>("[role=dialog] button.group");
      if (b) {
        b.click();
        window.clearInterval(id);
      }
    }, 150);
    return () => window.clearInterval(id);
  }, []);
  return <WaysTab data={de1Page} ways={w} />;
}
const pickChecks = [country("nod_de1", CountryVerdict.PARTIAL, ["MTS"]), country("nod_msk1", CountryVerdict.OK), country("nod_de2", CountryVerdict.OK), country("nod_fi1", CountryVerdict.PARTIAL, ["MTS"])];

// ---- the person's side ----

const direct = {
  inboundId: "inb_awg_de1", nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", profileName: "AWG 3.1", awgVersion: "3.1", stale: false, warnings: [], viaCountry: "", viaNodeId: "",
  conf: "[Interface]\nPrivateKey = AAAA\nAddress = 10.66.4.9/32\n\n[Peer]\nEndpoint = de1.example.com:51820\n", vpnKey: "vpn://AAAAAQAAAGh4nO", confFilename: "de1-31.conf",
  minClients: [{ app: "AmneziaVPN", minVersion: "5.0.1.5" }],
} as unknown as DeviceConfig;
const viaRu = {
  ...direct, viaCountry: "RU", viaNodeId: "nod_msk1", confFilename: "de1-31-via-ru.conf", vpnKey: "vpn://BBBBAQAAAGh4nO",
  conf: "[Interface]\nPrivateKey = BBBB\nAddress = 10.66.4.12/32\n\n[Peer]\nEndpoint = msk1.example.com:51820\n",
} as unknown as DeviceConfig;
const nl1 = { ...direct, inboundId: "inb_awg_nl1", nodeId: "nod_nl1", nodeName: "nl1", countryCode: "NL", confFilename: "nl1-31.conf" } as unknown as DeviceConfig;
const phone: DeviceRef = { id: "dev_k", label: "Pixel 7", platform: "android", profileName: "AWG 3.1", version: "3.1", address: "10.66.4.9", stale: false };

const connRow = (over: object) => ({
  deviceId: "dev_k", nodeId: "nod_de1", nodeName: "de1", inboundId: "inb_awg_de1", profileName: "AWG 3.1", protocol: "awg", connects: 14, rejects: 0, drops: 0, stalls: 0, onlineS: 7200, lastHourUnix: now - 40 * min,
  platform: "android", client: "", kind: "", hours: [], clientCloses: 0, freezes: 0, quiet: 0, rttMs: 0, lossPermille: 0, rttSamples: 0, pktsSent: 0, tcpStalls: 0, tcpStalls16k: 0, initiations: 0, responses: 0, responsesUnmatched: 0,
  unsupported: ["tcp_stalls", "tcp_stalls_16k", "initiations", "responses", "responses_unmatched"], viaCountry: "", viaNodeId: "", ...over,
});
const connection = {
  sinceUnix: now - 86_400, nowUnix: now, nodesWithoutCounters: [],
  rows: [connRow({}), connRow({ connects: 31, onlineS: 14_400, lastHourUnix: now - 5 * min, viaCountry: "RU", viaNodeId: "nod_msk1" }), connRow({ deviceId: "dev_l", connects: 6, onlineS: 1800, lastHourUnix: now - 3 * 3600, viaCountry: "RU", viaNodeId: "nod_msk1", stalls: 2 })],
  helloKex: [],
};
const aliceDevices = [
  { id: "dev_k", kind: DeviceKind.KEY, model: "Pixel 7", platform: "android" },
  { id: "dev_l", kind: DeviceKind.KEY, model: "Laptop", platform: "windows" },
] as never;

function Shot({ id, children }: { id: string; children: ReactNode }) {
  return <section data-shot={id}>{children}</section>;
}

export function AwgRelayKit() {
  const state = params.get("state") ?? "dialog";
  switch (state) {
    case "same-country":
      return (
        <Mock>
          <Shot id="awg-relay-same-country">
            <Dialog same />
          </Shot>
        </Mock>
      );
    case "pick":
      return (
        <Mock fill={[[checksQuery("").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: pickChecks }], [checksQuery("nod_de1").queryKey, { nowUnix: now, intervalS: 300, columns: [], rows: [], countryChecks: pickChecks.slice(0, 1) }], [doctorQuery("nod_de1").queryKey, { nowUnix: now, nodes: [] }]]}>
          <Shot id="awg-relay-pick">
            <Pick />
          </Shot>
        </Mock>
      );
    case "port":
      return (
        <Mock>
          <ChangeRelayPort r={{ id: "rly_awg", profileName: "AWG 3.1", protocol: "awg", network: "udp", port: 51820, exitPort: 51820, viaNodeName: "msk1", exitNodeName: "de1" } as never} onClose={() => {}} />
        </Mock>
      );
    case "device":
    case "device-via":
      return (
        <Mock>
          <DeviceConfigsModal device={phone} initial={state === "device" ? [direct, viaRu, nl1] : [viaRu, direct, nl1]} onClose={() => {}} onChanged={() => {}} />
        </Mock>
      );
    case "history":
      return (
        <Mock fill={[[userConnectionQuery("usr_alice").queryKey, connection]]}>
          <Shot id="awg-relay-history">
            <div className="max-w-xl">
              <ConnectionPanel userId="usr_alice" devices={aliceDevices} nodeName={(id) => id.replace("nod_", "")} />
            </div>
          </Shot>
        </Mock>
      );
    default:
      return (
        <Mock>
          <Shot id="awg-relay-dialog">
            <Dialog />
          </Shot>
        </Mock>
      );
  }
}
