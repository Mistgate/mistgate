export const healthTabs = ["alerts", "people", "checks", "doctor"] as const;
export type HealthTab = (typeof healthTabs)[number];

/** Search-param validation of /health: a known tab other than the first stays in the URL. */
export const validateHealthSearch = (s: Record<string, unknown>): { tab?: HealthTab } => ({
  ...(healthTabs.includes(s.tab as HealthTab) && s.tab !== "alerts" ? { tab: s.tab as HealthTab } : {}),
});
