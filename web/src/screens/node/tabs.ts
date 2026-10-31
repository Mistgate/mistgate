import { validatePaging } from "@/lib/paging";
import { isWayId, type WayId } from "@/lib/ways";

export const nodeTabs = ["overview", "ways", "profiles", "exit", "history", "doctor", "server"] as const;
export type NodeTab = (typeof nodeTabs)[number];

/** The parts of the History tab; the events are its first, the bare `?tab=history`. */
export const historyViews = ["events", "journal", "logs"] as const;
export type HistoryView = (typeof historyViews)[number];

export type NodeSearch = { tab?: NodeTab; way?: WayId; add?: string; view?: Exclude<HistoryView, "events">; before?: number; size?: number };

/**
 * Search-param validation of /nodes/$id: only a known tab other than the first is kept in the URL. `add` (a profile id,
 * on the profiles tab) opens the add-profile dialog with that profile chosen: the profile page links here. On the ways
 * tab `way` is the open lane (direct, ipv6, via:<entry>, out:<exit>) and `add` the "New way in" dialog: "1", or
 * "via:<entry>" straight at that entry's servers, or "ipv6" the IPv6 entry's dialog (Reachability and the alerts link there). The history
 * tab keeps its part (`view`, the events without one) and, on the events, where their log is read from (`before`) and
 * the rows per page (`size`).
 */
export const validateNodeSearch = (s: Record<string, unknown>): NodeSearch => {
  if (!nodeTabs.includes(s.tab as NodeTab) || s.tab === "overview") return {};
  const tab = s.tab as NodeTab;
  if (tab === "history") {
    if (s.view === "journal" || s.view === "logs") return { tab, view: s.view };
    const { before, size } = validatePaging(s);
    return { tab, ...(before ? { before } : {}), ...(size ? { size } : {}) };
  }
  if (tab === "ways") {
    const add = s.add === "1" || s.add === 1 ? "1" : typeof s.add === "string" && (s.add === "ipv6" || /^via:[\w-]+$/.test(s.add)) ? s.add : undefined;
    return { tab, ...(isWayId(s.way) ? { way: s.way } : {}), ...(add ? { add } : {}) };
  }
  return tab === "profiles" && typeof s.add === "string" && s.add !== "" ? { tab, add: s.add } : { tab };
};

/**
 * Where a link made for the old tabs lands now (bookmarks, the docs of older versions): the Ports and Relays of Profiles
 * went into Ways in, Settings split into Exit (WARP and its `#warp`) and Server, Users went into the Overview (`#people`), Logs, Journal and Events into History. Undefined
 * for a link that needs nothing. The route answers it with a `replace` redirect.
 */
export function upgradeNodeLink(s: Record<string, unknown>, hash: string): { search: NodeSearch; hash: string } | undefined {
  switch (s.tab) {
    case "profiles":
      // the ports and the relays moved to the ways in
      return hash === "ports" ? { search: { tab: "ways", way: "direct" }, hash: "" } : hash === "relays" ? { search: { tab: "ways" }, hash: "" } : undefined;
    case "settings":
      return hash === "warp" ? { search: { tab: "exit" }, hash } : { search: { tab: "server" }, hash };
    case "users":
      return { search: {}, hash: "people" };
    case "logs":
    case "journal":
      return { search: { tab: "history", view: s.tab }, hash };
    case "events":
      return { search: validateNodeSearch({ ...s, tab: "history" }), hash };
  }
  return undefined;
}
