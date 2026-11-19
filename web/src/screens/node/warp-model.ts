import { queryOptions } from "@tanstack/react-query";
import type { GetWarpResponse } from "@/gen/mistgate/admin/v1/warp_pb";
import { warp as warpApi } from "@/lib/api";
import { codedError } from "@/lib/coded-error";
import { plain, type Plain } from "@/lib/plain";
import { pollMs } from "@/lib/queries";
import type { Tx } from "@/screens/users/t";

export const warpQuery = (nodeId: string) =>
  queryOptions({
    queryKey: ["warp", nodeId],
    queryFn: async ({ signal }) => plain(await warpApi.getWarp({ nodeId }, { signal })),
    // a check of the sites that the node is running (asked by "Check now" or by the panel's schedule) is read every 2 s until it is done
    refetchInterval: (q) => (q.state.data?.reachCheck?.running ? 2_000 : pollMs),
  });

export type WarpData = Plain<GetWarpResponse>;

/** A sentence for a failed WARP call: the panel's codes (cloudflare_rate_limited, account_exists, ...) in words. */
export const warpError = (e: unknown, t: Tx) => codedError(e, t, "warp.err");
