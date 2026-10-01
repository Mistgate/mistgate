export const nodeTabs = ["overview", "profiles", "users", "logs", "events", "doctor", "settings"] as const;
export type NodeTab = (typeof nodeTabs)[number];

/**
 * Search-param validation of /nodes/$id: only a known tab other than the first is kept in the URL. `add` (a profile id,
 * on the profiles tab) opens the add-profile dialog with that profile chosen: the profile page links here.
 */
export const validateNodeSearch = (s: Record<string, unknown>): { tab?: NodeTab; add?: string } => {
  if (!nodeTabs.includes(s.tab as NodeTab) || s.tab === "overview") return {};
  const tab = s.tab as NodeTab;
  return tab === "profiles" && typeof s.add === "string" && s.add !== "" ? { tab, add: s.add } : { tab };
};
