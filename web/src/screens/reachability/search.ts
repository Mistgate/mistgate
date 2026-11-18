/** The sources of the Reachability page, in the order of their list; the id is what `?source=` carries. */
export const sourceIds = ["globalping", "lists", "tspu", "cheburcheck", "bsbord", "klick", "probes"] as const;
export type SourceId = (typeof sourceIds)[number];

export const isSourceId = (v: unknown): v is SourceId => sourceIds.includes(v as SourceId);

/** The three tabs of the page: the nodes, the week's events, the sources. `?tab=`; Nodes is the page's own and has no key. */
export const reachTabs = ["nodes", "events", "sources"] as const;
export type ReachTab = (typeof reachTabs)[number];

/** The tab a link leads to by the old anchors of the one-page version (`#reach-nodes`, `#reach-events`, `#reach-sources`, `#sources`). */
export const tabOfHash = (hash: string): ReachTab | undefined => ({ "reach-nodes": "nodes", "reach-events": "events", "reach-sources": "sources", sources: "sources" } as Record<string, ReachTab>)[hash.replace(/^#/, "")];

export type ReachSearch = { tab?: Exclude<ReachTab, "nodes">; source?: SourceId };

/**
 * Search-param validation of /reachability: the tab (Nodes is the default and stays out of the URL) and the source picked in the Sources
 * tab, when it is a known one. A `source` without a `tab` opens the Sources tab, as a link from another screen expects.
 */
export const validateReachSearch = (s: Record<string, unknown>): ReachSearch => ({
  ...(s.tab === "events" || s.tab === "sources" ? { tab: s.tab } : {}),
  ...(isSourceId(s.source) ? { source: s.source } : {}),
});

/** The tab the page shows: the one asked for, else the Sources tab for a `?source=` link, else Nodes. */
export const tabOf = (s: ReachSearch): ReachTab => s.tab ?? (s.source ? "sources" : "nodes");
