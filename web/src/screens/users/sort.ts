// The order of the people list. It lives in the URL (?sort=traffic&desc=1; no sort = by name, no desc = ascending), so a
// reload, the back button and a copied link keep it. Pure parts only: the screen maps an id to the server's enum.

export const sortIds = ["name", "status", "group", "devices", "traffic", "expiry", "seen"] as const;
export type SortId = (typeof sortIds)[number];
export type SortState = { sort: SortId; desc: boolean };
export type SortSearch = { sort?: SortId; desc?: 1 };

/** The direction a first click sorts in: the biggest, the latest and the most recent first; the soonest term and A→Z. */
export const firstDesc: Record<SortId, boolean> = { name: false, status: false, group: false, devices: true, traffic: true, expiry: false, seen: true };

/** The column header's text key of each sort (the term column is sorted by the expiry time). */
export const sortLabel = {
  name: "users.col.name",
  status: "users.col.status",
  group: "users.col.group",
  devices: "users.col.devices",
  traffic: "users.col.traffic",
  expiry: "users.col.term",
  seen: "users.col.seen",
} as const satisfies Record<SortId, string>;

/** Search-param validation: only a known sort and `desc` survive; the default (name, ascending) is the bare URL. */
export function validateSort(s: Record<string, unknown>): SortSearch {
  const sort = sortIds.find((id) => id === s.sort);
  const desc = s.desc === true || s.desc === "true" || s.desc === "1" || s.desc === 1;
  return { ...(sort && sort !== "name" ? { sort } : {}), ...(desc ? { desc: 1 as const } : {}) };
}

export function sortOf(s: Record<string, unknown>): SortState {
  const v = validateSort(s);
  return { sort: v.sort ?? "name", desc: v.desc === 1 };
}

/** The search params a header click sets: another column starts in its first direction, the same one reverses. */
export function clickSort(cur: SortState, id: SortId): SortSearch {
  const desc = cur.sort === id ? !cur.desc : firstDesc[id];
  return { ...(id !== "name" ? { sort: id } : {}), ...(desc ? { desc: 1 as const } : {}) };
}
