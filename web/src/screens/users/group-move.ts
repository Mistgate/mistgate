import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { UserSort } from "@/gen/mistgate/admin/v1/user_pb";
import { users as usersApi } from "@/lib/api";

// Moving people between groups for the group page. The panel has no batch call for it: a person's group is one UpdateUser,
// so the people go a few at a time, and a failure says how far it got.

/** At most this many calls run at once: the server writes each in its own transaction, and four keep a long list moving. */
const PARALLEL = 4;

/** What a move does to access, from the two sets of profiles alone (the server's dry run says the same): the profiles that go. */
export function lostProfileIds(from: readonly Pick<Group, "profileIds">[], to: Pick<Group, "profileIds">): string[] {
  const keep = new Set(to.profileIds);
  return [...new Set(from.flatMap((g) => g.profileIds))].filter((id) => !keep.has(id));
}

/** Moves each person to `to`; stops after the first failure (the ones in flight finish) and returns how many are over and why it stopped. */
export async function moveUsers(ids: readonly string[], to: string, onStep?: (done: number) => void): Promise<{ done: number; error?: unknown }> {
  let next = 0;
  let done = 0;
  let error: unknown;
  let failed = false;
  const worker = async () => {
    while (!failed && next < ids.length) {
      const userId = ids[next++]!;
      try {
        await usersApi.updateUser({ userId, groupId: to });
        done++;
        onStep?.(done);
      } catch (e) {
        failed = true;
        error = e;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(PARALLEL, ids.length) }, worker));
  return failed ? { done, error } : { done };
}

/** The ids of everybody in a group, page by page (a group may hold more than one page of people). */
export async function groupMemberIds(groupId: string): Promise<string[]> {
  const ids: string[] = [];
  let token = "";
  do {
    const r = await usersApi.listUsers({ groupId, pageSize: 200, pageToken: token, sort: UserSort.NAME });
    ids.push(...r.users.map((u) => u.id));
    token = r.nextPageToken;
  } while (token);
  return ids;
}
