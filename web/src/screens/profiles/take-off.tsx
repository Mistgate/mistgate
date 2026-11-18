import { useQueryClient } from "@tanstack/react-query";
import { profiles as profilesApi } from "@/lib/api";

// Taking a profile off nodes, several at once, is one RemoveProfileFromNodes call: the panel removes the servers in one transaction
// (a node that is not connected drops the profile when it connects again), and with the profile deleted too when the list covers
// every node it is on.

/** The call, and what the screens that show the profile must read again afterwards. */
export function useTakeOff(profileId: string) {
  const qc = useQueryClient();
  return async (nodeIds: string[], remove?: { name: string }) => {
    const res = await profilesApi.removeProfileFromNodes({ profileId, nodeIds, deleteProfile: !!remove, confirmName: remove?.name ?? "" });
    await Promise.all(["profiles", "nodes", "node", "groups", "users", "subs", "relays"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
    return res;
  };
}
