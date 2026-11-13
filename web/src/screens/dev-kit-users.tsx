import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { DeviceKind, QuotaReset, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { clientApiQuery } from "@/lib/clientapi";
import { useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { CreateUserModal } from "@/screens/users/create-user";
import type { DetailN } from "@/screens/users/model";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { AccessPanel, DevicesPanel, LimitsPanel, TrafficPanel } from "@/screens/users/user-detail";
import type { DeviceN, UserN } from "@/screens/users/model";

// Development only (the dev kit): the quota period in the new-user form and on the user card, on a private query cache
// with made-up data. Each block carries data-shot for the screenshot script; the form opens from its button.
// The apps of a person on the card (data-shot user-apps-on|off|panel-off|needs-link): the Mistgate app switch on, off for this
// person, off for the whole panel (the owner's switch in Subscriptions; a pill under the row leads to it), and locked because the
// person's subscription link is off.

const NOW = Math.floor(Date.now() / 1000);
const DAY = 86_400;
const GB = 1e9;

function cache(panelApp = true) {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
  c.setQueryData(clientApiQuery.queryKey, { enabled: panelApp, stats: { people: 24, peopleOn: 17, devices: 31 } });
  c.setQueryData(groupsQuery.queryKey, [{ id: "grp_f", name: "friends", profileIds: ["p_hy", "p_awg"], userCount: 3, dnsPresetId: "", happNodes: 2, amneziaNodes: 1 }] as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: [{ id: "nod_de1", name: "de1", countryCode: "DE" }, { id: "nod_nl1", name: "nl1", countryCode: "NL" }] } as never);
  c.setQueryData(profileListQuery.queryKey, [{ id: "p_hy", name: "hy2 · 443", protocol: "hysteria2" }, { id: "p_awg", name: "AWG 3.1", protocol: "awg" }] as never);
  c.setQueryData(protocolsQuery.queryKey, [{ id: "hysteria2", displayName: "Hysteria2", apps: [App.HAPP] }, { id: "awg", displayName: "AmneziaWG", apps: [App.AMNEZIA] }] as never);
  c.setQueryData(settingsQuery.queryKey, { settings: { apps: [{ kind: App.HAPP, name: "Happ" }] }, effectiveTitle: "", samples: [], sampleGroup: "", namesLanguage: "", sharedLinkUsers: 0 } as never);
  c.setQueryData(dnsPresetsQuery.queryKey, { presets: [], providers: [], clientSupport: [] } as never);
  return c;
}

function Shot({ id, title, children }: { id: string; title: string; children: ReactNode }) {
  return (
    <section data-shot={id} className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      {children}
    </section>
  );
}

// the next Monday, 00:00 UTC: when a weekly quota starts again
const monday = (() => {
  const d = new Date(NOW * 1000);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate() + (((8 - d.getUTCDay()) % 7) || 7)) / 1000;
})();
const alice = {
  id: "usr_alice", name: "alice", subscriptionName: "", groupId: "grp_f", groupName: "friends", status: UserStatus.ACTIVE, apps: { happ: true, amnezia: true }, via: [],
  devicesUsed: 2, deviceLimit: 5, usedBytes: 37.4 * GB, quotaBytes: 100 * GB, quotaReset: QuotaReset.WEEK, nextResetUnix: monday, expiresUnix: NOW + 24 * DAY,
  lastSeenUnix: NOW - 300, speedLimitBps: 0, deviceSpeedLimitBps: 0, createdUnix: NOW - 60 * DAY, nodes: { all: true, nodeIds: [] }, dnsPresetId: "",
};
const card = {
  user: alice,
  devices: [],
  blocked: [],
  profiles: [],
  nodeAccess: [],
  dailyTraffic: Array.from({ length: 14 }, (_, i) => ({ dayUnix: NOW - (13 - i) * DAY, bytes: [2.1, 3.4, 0.8, 5.2, 4.4, 1.9, 6.1, 2.7, 3.3, 4.8, 0, 5.6, 3.9, 2.2][i]! * GB })),
  nodeTraffic: [
    { nodeId: "nod_de1", nodeName: "de1", protocol: "hysteria2", bytes: 24.1 * GB },
    { nodeId: "nod_nl1", nodeName: "nl1", protocol: "awg", bytes: 13.3 * GB },
  ],
} as unknown as DetailN;
const actions = { update: () => new Promise<void>((r) => setTimeout(r, 300)) };

const accessNodes = [
  { nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", location: "Frankfurt", provider: "Hetzner", protocols: ["hysteria2", "awg"], nodeProtocols: ["hysteria2", "awg"], selected: true },
  { nodeId: "nod_nl1", nodeName: "nl1", countryCode: "NL", location: "Amsterdam", provider: "Northwind", protocols: ["hysteria2"], nodeProtocols: ["hysteria2", "awg"], selected: true },
];
const withApps = (apps: { happ: boolean; amnezia: boolean; telegram: boolean; mistgate: boolean }) =>
  ({
    ...card,
    user: { ...alice, apps },
    profiles: [{ id: "p_hy", name: "hy2 · 443", protocol: "hysteria2" }, { id: "p_awg", name: "AWG 3.1", protocol: "awg" }],
    nodeAccess: accessNodes,
  }) as unknown as DetailN;

/** The access block of the card with the apps of one person; `panel` is the owner's switch in Subscriptions. */
function AppsShot({ id, title, apps, panel = true }: { id: string; title: string; apps: { happ: boolean; amnezia: boolean; telegram: boolean; mistgate: boolean }; panel?: boolean }) {
  const [qc] = useState(() => cache(panel));
  return (
    <QueryClientProvider client={qc}>
      <Shot id={id} title={title}>
        <div className="max-w-md">
          <AccessPanel data={withApps(apps)} actions={actions as never} />
        </div>
      </Shot>
    </QueryClientProvider>
  );
}

function UserCard() {
  const fmt = useFmt();
  return (
    <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
      <TrafficPanel data={card} fmt={fmt} />
      <LimitsPanel user={card.user} actions={actions as never} />
    </div>
  );
}

// A person's devices with two inactive ones (an old tablet on the link, an AmneziaWG key): "3 of 5 +2 inactive", the grey-blue plaque,
// the line that says they take no place. The settings are Subscriptions' (inactive after 30 days).
const dev = (id: string, over: Partial<DeviceN>): DeviceN =>
  ({
    id, kind: DeviceKind.LINK, model: "", platform: "ios", client: "", osVersion: "", awgProfileId: "", awgProfileName: "", awgVersion: "", address: "", stale: false, online: false, onlineNodeIds: [],
    firstSeenUnix: NOW - 90 * DAY, lastSeenUnix: NOW - 60, lastHandshakeUnix: 0, usedBytes: 4 * GB, fingerprint: "", enrolled: false, enrolLinkStale: false, idle: false, idleSinceUnix: 0, ...over,
  }) as unknown as DeviceN;
const idleDevices = [
  dev("dev_1", { model: "iPhone 15", client: "Happ/3.4.2", osVersion: "18.1", online: true, onlineNodeIds: ["nod_de1"] }),
  dev("dev_2", { model: "ThinkPad T14", platform: "windows", client: "Mihomo/1.19.31", osVersion: "11", lastSeenUnix: NOW - 2 * DAY }),
  dev("dev_3", { model: "Old tablet", platform: "android", client: "Happ/3.1.1", osVersion: "12", lastSeenUnix: NOW - 43 * DAY, idle: true, idleSinceUnix: NOW - 13 * DAY }),
  dev("dev_4", { kind: DeviceKind.KEY, model: "Pixel 6", platform: "android", awgProfileId: "p_awg", awgProfileName: "AWG 3.1", lastHandshakeUnix: NOW - 61 * DAY, idle: true, idleSinceUnix: NOW - 31 * DAY }),
];

function IdleDevices() {
  const [qc] = useState(cache);
  const user = { ...alice, devicesUsed: 2, devicesIdle: 2, deviceLimit: 5, apps: { happ: true, amnezia: true, telegram: false } } as unknown as UserN;
  return (
    <QueryClientProvider client={qc}>
      <div className="@container max-w-md">
        <DevicesPanel
          user={user}
          devices={idleDevices}
          profiles={[{ id: "p_awg", name: "AWG 3.1", protocol: "awg" }] as never}
          nodeName={(id) => id.replace("nod_", "")}
          actions={{ refresh: () => Promise.resolve() } as never}
          onEditGroup={() => {}}
        />
      </div>
    </QueryClientProvider>
  );
}

/** The quota-period blocks of the dev kit. */
export function UsersKit() {
  const [qc] = useState(cache);
  const [open, setOpen] = useState(false);
  return (
    <QueryClientProvider client={qc}>
      <div className="flex flex-col gap-8">
        <Shot id="user-form" title="New user: the quota and how often it resets">
          <div>
            <Button onClick={() => setOpen(true)}>Open the new-user form</Button>
          </div>
          <CreateUserModal open={open} onOpenChange={setOpen} onCreated={() => setOpen(false)} />
        </Shot>
        <Shot id="user-card" title="User card: traffic with the next reset, and the period in Limits">
          <UserCard />
        </Shot>
        <Shot id="user-devices-idle" title="User card, devices: 2 of 5 count, +2 inactive (a tablet and an AmneziaWG key have not connected for a long time and take no place)">
          <IdleDevices />
        </Shot>
        <AppsShot id="user-apps-on" title="User card, access: the Mistgate app on for the person (all four apps)" apps={{ happ: true, amnezia: true, telegram: false, mistgate: true }} />
        <AppsShot id="user-apps-off" title="User card, access: the Mistgate app off for this person" apps={{ happ: true, amnezia: true, telegram: false, mistgate: false }} />
        <AppsShot id="user-apps-panel-off" title="User card, access: the person has it on, the owner has it off for the whole panel (pill leads to Subscriptions)" apps={{ happ: true, amnezia: true, telegram: false, mistgate: true }} panel={false} />
        <AppsShot id="user-apps-needs-link" title="User card, access: the subscription link is off, so the Mistgate app is locked" apps={{ happ: false, amnezia: true, telegram: false, mistgate: true }} />
      </div>
    </QueryClientProvider>
  );
}
