export const healthTabs = ["alerts", "checks", "doctor"] as const;
export type HealthTab = (typeof healthTabs)[number];

/** Search-param validation of /health: only a known tab other than the first is kept in the URL. */
export const validateHealthSearch = (s: Record<string, unknown>): { tab?: HealthTab } =>
  healthTabs.includes(s.tab as HealthTab) && s.tab !== "alerts" ? { tab: s.tab as HealthTab } : {};
