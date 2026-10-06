import { validatePaging, type PagingSearch } from "@/lib/paging";

export const healthTabs = ["alerts", "checks", "doctor"] as const;
export type HealthTab = (typeof healthTabs)[number];

/** Search-param validation of /health: a known tab other than the first, and the page of the alert history, stay in the URL. */
export const validateHealthSearch = (s: Record<string, unknown>): { tab?: HealthTab } & PagingSearch => ({
  ...(healthTabs.includes(s.tab as HealthTab) && s.tab !== "alerts" ? { tab: s.tab as HealthTab } : {}),
  ...(s.tab === undefined || s.tab === "alerts" ? validatePaging(s) : {}),
});
