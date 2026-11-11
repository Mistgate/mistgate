export const subsTabs = ["formats", "rules", "clients", "texts", "page", "dns"] as const;
export type SubsTab = (typeof subsTabs)[number];

/**
 * Search-param validation of /subscriptions: only a known tab other than the first is kept in the URL, and `user` (the id of
 * the one person whose devices the "App versions" tab lists) when it is a plain id.
 */
export const validateSubsSearch = (s: Record<string, unknown>): { tab?: SubsTab; user?: string } => ({
  ...(subsTabs.includes(s.tab as SubsTab) && s.tab !== "formats" ? { tab: s.tab as SubsTab } : {}),
  ...(typeof s.user === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(s.user) ? { user: s.user } : {}),
});
