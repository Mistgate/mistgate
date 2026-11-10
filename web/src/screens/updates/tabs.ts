export const updateTabs = ["panel", "agents", "os"] as const;
export type UpdateTab = (typeof updateTabs)[number];

/** Search-param validation of /updates: a known layer stays in the URL; without one the page opens the layer that needs a hand. */
export const validateUpdatesSearch = (s: Record<string, unknown>): { tab?: UpdateTab } => ({
  ...(updateTabs.includes(s.tab as UpdateTab) ? { tab: s.tab as UpdateTab } : {}),
});
