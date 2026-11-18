import { NodeShareMode } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { readPref, writePref } from "./storage";

// Who a node is handed out to (GroupService.SetNodeSharing, Node.sharing, the `sharing` of a new node). The API names three modes
// (ALL, LISTED, NONE); the pages say them as "everyone", "some groups" and "nobody yet". A group receives a node unless the node is on
// its disabled list (Group.disabled_node_ids), so what a node's page shows per group is read off the groups.

export type ShareMode = "all" | "listed" | "none";
/** A share as asked for: LISTED names the groups (at least one), ALL and NONE name none. */
export type ShareChoice = { mode: ShareMode; groupIds: readonly string[] };

export const shareModes: readonly ShareMode[] = ["all", "listed", "none"];

export const modeOf = (m: NodeShareMode): ShareMode => (m === NodeShareMode.ALL ? "all" : m === NodeShareMode.LISTED ? "listed" : "none");
export const protoMode = (m: ShareMode): NodeShareMode => (m === "all" ? NodeShareMode.ALL : m === "listed" ? NodeShareMode.LISTED : NodeShareMode.NONE);

/** Whether the group has the node: it is not on the group's disabled list. */
export const gets = (g: Pick<Group, "disabledNodeIds">, nodeId: string) => !g.disabledNodeIds.includes(nodeId);

/** The ids of the groups that receive the node now. */
export const receivingIds = (groups: readonly Pick<Group, "id" | "disabledNodeIds">[], nodeId: string): string[] => groups.filter((g) => gets(g, nodeId)).map((g) => g.id);

/** The share that gives the node to exactly these groups: none of them is "nobody yet". */
export const shareOfIds = (ids: readonly string[]): ShareChoice => (ids.length > 0 ? { mode: "listed", groupIds: [...ids] } : { mode: "none", groupIds: [] });

/** The share as the API takes it (a request that makes a node, or SetNodeSharing): the groups go only with LISTED. */
export const sharing = (c: ShareChoice) => ({ mode: protoMode(c.mode), groupIds: c.mode === "listed" ? [...c.groupIds] : [] });

/** A share the API would refuse (LISTED without a group) is not one to send. */
export const shareValid = (c: ShareChoice) => c.mode !== "listed" || c.groupIds.length > 0;

// ---- the last choice of the New node window, kept in this browser

const key = "nodes.share";

/** Remember what the window used (best-effort, like every preference: lib/storage.ts). */
export function rememberShare(c: ShareChoice) {
  writePref(key, JSON.stringify({ mode: c.mode, groupIds: c.mode === "listed" ? c.groupIds : [] }));
}

function remembered(): ShareChoice | null {
  try {
    const v = JSON.parse(readPref(key) ?? "null") as { mode?: unknown; groupIds?: unknown } | null;
    if (!v || !shareModes.includes(v.mode as ShareMode)) return null;
    const ids = Array.isArray(v.groupIds) ? v.groupIds.filter((x): x is string => typeof x === "string") : [];
    return { mode: v.mode as ShareMode, groupIds: v.mode === "listed" ? ids : [] };
  } catch {
    return null;
  }
}

/**
 * What the New node window starts with (from the second node on): the last choice, else "nobody yet". Groups that no longer exist
 * drop out of a remembered list; a list left empty is "nobody yet". `groups` undefined (not loaded yet) keeps the list as it was.
 */
export function defaultShare(groups: readonly Pick<Group, "id">[] | undefined): ShareChoice {
  const last = remembered();
  if (!last) return { mode: "none", groupIds: [] };
  if (last.mode !== "listed" || !groups) return last;
  return shareOfIds(last.groupIds.filter((id) => groups.some((g) => g.id === id)));
}
