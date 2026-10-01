import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import { FieldErrorSchema, type FieldError } from "@/gen/mistgate/admin/v1/profile_pb";
import { groups, profiles } from "@/lib/api";

// Query keys: "users" and "nodes" are shared with the shell (sidebar counts, palette search), so invalidating
// ["users"] after a change refreshes them too. Everything of ours sits under a second key to stay apart from them.
export const groupsQuery = queryOptions({
  queryKey: ["groups"],
  queryFn: async ({ signal }) => (await groups.listGroups({}, { signal })).groups,
  staleTime: 15_000,
});

export const profileListQuery = queryOptions({
  queryKey: ["profiles", "list"],
  queryFn: async ({ signal }) => (await profiles.listProfiles({}, { signal })).profiles,
  refetchInterval: 10_000,
});

// Plugins and their schema only change with a panel upgrade.
export const protocolsQuery = queryOptions({
  queryKey: ["profiles", "protocols"],
  queryFn: async ({ signal }) => (await profiles.listProtocols({}, { signal })).protocols,
  staleTime: Infinity,
});

export const isCode = (e: unknown, code: Code) => ConnectError.from(e).code === code;

/** Per-field problems the server attached to an INVALID_ARGUMENT error (create / update of a profile). */
export function fieldErrors(e: unknown): FieldError[] {
  return ConnectError.from(e).findDetails(FieldErrorSchema);
}
