export const subsTabs = ["formats", "rules", "texts", "page", "dns"] as const;
export type SubsTab = (typeof subsTabs)[number];

/** Search-param validation of /subscriptions: only a known tab other than the first is kept in the URL. */
export const validateSubsSearch = (s: Record<string, unknown>): { tab?: SubsTab } =>
  subsTabs.includes(s.tab as SubsTab) && s.tab !== "formats" ? { tab: s.tab as SubsTab } : {};
