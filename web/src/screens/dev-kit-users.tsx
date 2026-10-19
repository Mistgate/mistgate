import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { QuotaReset, UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { useFmt } from "@/lib/format";
import { nodesQuery } from "@/lib/queries";
import { dnsPresetsQuery, settingsQuery } from "@/screens/subscriptions/queries";
import { CreateUserModal } from "@/screens/users/create-user";
import type { DetailN } from "@/screens/users/model";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { LimitsPanel, TrafficPanel } from "@/screens/users/user-detail";

// Development only (the dev kit): the quota period in the new-user form and on the user card, on a private query cache
// with made-up data. Each block carries data-shot for the screenshot script; the form opens from its button.

const NOW = Math.floor(Date.now() / 1000);
const DAY = 86_400;
const GB = 1e9;

function cache() {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
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

function UserCard() {
  const fmt = useFmt();
  return (
    <div className="grid items-start gap-3.5 md:grid-cols-[minmax(0,1.25fr)_minmax(0,1fr)]">
      <TrafficPanel data={card} fmt={fmt} />
      <LimitsPanel user={card.user} actions={actions as never} />
    </div>
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
      </div>
    </QueryClientProvider>
  );
}
