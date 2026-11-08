export const aiTabs = ["assistant", "memory", "tasks", "mcp", "approvals"] as const;
export type AiTab = (typeof aiTabs)[number];

/** Search-param validation of /ai: only a known tab other than the first is kept in the URL, with the approvals history page, the
 * plan to focus in the approvals and the task to open among the scheduled tasks. */
export const validateAiSearch = (s: Record<string, unknown>): { tab?: AiTab; page?: number; new?: "token"; plan?: string; task?: string } => {
  const tab = aiTabs.includes(s.tab as AiTab) && s.tab !== "assistant" ? (s.tab as AiTab) : undefined;
  const page = Number(s.page);
  // ?new=token: the palette's "New API token" opens the create form of the tokens card
  return { ...(tab ? { tab } : {}), ...(Number.isInteger(page) && page > 1 ? { page } : {}), ...(s.new === "token" ? { new: "token" as const } : {}), ...(tab === "approvals" && typeof s.plan === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(s.plan) ? { plan: s.plan } : {}), ...(tab === "tasks" && typeof s.task === "string" && /^[A-Za-z0-9_-]{1,64}$/.test(s.task) ? { task: s.task } : {}) };
};
