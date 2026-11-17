import { queryOptions, useQuery } from "@tanstack/react-query";
import type { ProfileSummary } from "@/gen/mistgate/admin/v1/profile_pb";
import { nodes } from "@/lib/api";
import type { InboundConn } from "@/lib/connections";
import { checksQuery, countryColumn } from "@/lib/health";
import { plain, type Plain } from "@/lib/plain";
import { pollMs } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { useIsOwner } from "@/lib/updates";
import { cellsByProfile, type PCell } from "./profiles-model";

/** The nodes' own counts of connections over the last day, every node at once (one call; the matrix and the drawer read it). */
export const fleetConnectionsQuery = queryOptions({
  queryKey: ["nodes", "fleet-connections"],
  queryFn: async ({ signal }) => plain(await nodes.getFleetConnections({ hours: 24 }, { signal })),
  refetchInterval: 60_000,
});

export type FleetConns = Plain<Awaited<ReturnType<typeof nodes.getFleetConnections>>>;

/**
 * Everything the list and the matrix draw beyond the profiles themselves: each profile's servers (from the checks), the people's
 * connections by server, the relays that go through a server (owner only: a relay is infrastructure) and the country check of the
 * nodes. `checks` says whether the cells can be trusted yet: while they load the rows show placeholders, when they fail the count of
 * nodes the profile list itself holds.
 */
export function useServers(profiles: readonly ProfileSummary[] | undefined) {
  const checks = useQuery({ ...checksQuery(""), refetchInterval: pollMs });
  const conns = useQuery(fleetConnectionsQuery);
  const owner = useIsOwner();
  const relays = useQuery({ ...relaysQuery(""), enabled: owner });
  const cells = cellsByProfile(checks.data, profiles ?? []);
  const connOf = new Map<string, InboundConn>();
  for (const n of conns.data?.nodes ?? []) for (const i of n.inbounds) connOf.set(i.inboundId, i);
  const relayList = relays.data ?? [];
  const via = new Map<string, string[]>();
  for (const r of relayList) if (r.enabled && r.inboundId) via.set(r.inboundId, [...(via.get(r.inboundId) ?? []), r.viaNodeName]);
  return {
    checks,
    cells: (profileId: string): PCell[] => cells.get(profileId) ?? [],
    connOf,
    via,
    /** The relays that reach this server (the drawer's last section). */
    relaysOf: (inboundId: string) => relayList.filter((r) => r.inboundId === inboundId),
    /** The nodes some relay enters through: the matrix tints their column. */
    entries: new Set(relayList.filter((r) => r.enabled).map((r) => r.viaNodeId)),
    country: countryColumn(checks.data?.countryChecks),
    ready: checks.isSuccess,
    nodes: checks.data?.rows ?? [],
  };
}

export type Servers = ReturnType<typeof useServers>;
