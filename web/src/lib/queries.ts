import { keepPreviousData, queryOptions, useQuery } from "@tanstack/react-query";
import { OverviewRange } from "@/gen/mistgate/admin/v1/fleet_pb";
import { UserFilter } from "@/gen/mistgate/admin/v1/user_pb";
import { fleet, nodes, users } from "./api";
import { plain } from "./plain";

// Everything the screens read. The fleet changes under you, so every list polls every 10 s (agents report
// every 10 s; there is nothing fresher to show). react-query pauses polling in a hidden tab.
// Responses pass through plain(): 64-bit fields arrive as bigint and the screens want numbers (lib/plain.ts).
export const pollMs = 10_000;

export const overviewQuery = (range: OverviewRange = OverviewRange.OVERVIEW_RANGE_24H) =>
  queryOptions({
    queryKey: ["overview", range],
    queryFn: async ({ signal }) => plain(await fleet.overview({ range, eventLimit: 6 }, { signal })),
    refetchInterval: pollMs,
    // switching 24 h / 7 d keeps the old chart on screen until the new one is there
    placeholderData: keepPreviousData,
  });

export const nodesQuery = queryOptions({
  queryKey: ["nodes"],
  queryFn: async ({ signal }) => plain(await nodes.listNodes({}, { signal })),
  refetchInterval: pollMs,
});

export const nodeQuery = (id: string) =>
  queryOptions({
    queryKey: ["node", id],
    queryFn: async ({ signal }) => plain(await nodes.getNode({ nodeId: id }, { signal })),
    refetchInterval: pollMs,
  });

/** Number of users for the sidebar: one row asked for, only `counts` is read. */
export const userCountQuery = queryOptions({
  queryKey: ["users", "count"],
  queryFn: async ({ signal }) => plain(await users.listUsers({ pageSize: 1, filter: UserFilter.UNSPECIFIED }, { signal })),
  refetchInterval: pollMs,
});

/** The palette's user search: a small page, matched by the server on name or group. */
export const userSearchQuery = (q: string) =>
  queryOptions({
    queryKey: ["users", "search", q],
    queryFn: async ({ signal }) => plain(await users.listUsers({ pageSize: 6, query: q }, { signal })),
    staleTime: 10_000,
  });

export function useOverview(range: OverviewRange = OverviewRange.OVERVIEW_RANGE_24H) {
  return useQuery(overviewQuery(range));
}

export function useNodes() {
  return useQuery(nodesQuery);
}
