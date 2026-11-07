import { readPref, writePref } from "./storage";

// The assistant's interface tools (ADR 0014): ui_open(page, id) and ui_secret_form(kind). The panel checks them against
// whitelists (internal/panel/assistant/ui_tools.go) before the chat shows a card; this file is the same whitelist on this
// side, so a card is only drawn for what both accept, and maps a page to the router's real route. A card navigates nothing by
// itself. Changes are plans (the MCP plan tools) approved on the plan's own card, not forms the assistant fills.

export type Target = { to: string; params?: Record<string, string>; search?: Record<string, unknown> };

const idPattern = /^[A-Za-z0-9_-]{1,64}$/;

type PageSpec = { needsId: boolean; target: (id: string) => Target };

const settingsPages = ["interface", "system", "security", "sessions", "audit", "admins", "domains", "backups"] as const;

const pages: Record<string, PageSpec> = {
  overview: { needsId: false, target: () => ({ to: "/" }) },
  nodes: { needsId: false, target: () => ({ to: "/nodes" }) },
  node: { needsId: true, target: (id) => ({ to: "/nodes/$id", params: { id } }) },
  node_ways: { needsId: true, target: (id) => ({ to: "/nodes/$id", params: { id }, search: { tab: "ways" } }) },
  node_profiles: { needsId: true, target: (id) => ({ to: "/nodes/$id", params: { id }, search: { tab: "profiles" } }) },
  node_history: { needsId: true, target: (id) => ({ to: "/nodes/$id", params: { id }, search: { tab: "history" } }) },
  node_server: { needsId: true, target: (id) => ({ to: "/nodes/$id", params: { id }, search: { tab: "server" } }) },
  users: { needsId: false, target: () => ({ to: "/users" }) },
  groups: { needsId: false, target: () => ({ to: "/users", search: { tab: "groups" } }) },
  user: { needsId: true, target: (id) => ({ to: "/users/$id", params: { id } }) },
  profiles: { needsId: false, target: () => ({ to: "/profiles" }) },
  profile: { needsId: true, target: (id) => ({ to: "/profiles/$id", params: { id } }) },
  subscriptions: { needsId: false, target: () => ({ to: "/subscriptions" }) },
  health: { needsId: false, target: () => ({ to: "/health" }) },
  health_people: { needsId: false, target: () => ({ to: "/health", search: { tab: "people" } }) },
  reachability: { needsId: false, target: () => ({ to: "/reachability" }) },
  updates: { needsId: false, target: () => ({ to: "/updates" }) },
  ai: { needsId: false, target: () => ({ to: "/ai" }) },
  ai_memory: { needsId: false, target: () => ({ to: "/ai", search: { tab: "memory" } }) },
  ai_mcp: { needsId: false, target: () => ({ to: "/ai", search: { tab: "mcp" } }) },
  ai_waiting: { needsId: false, target: () => ({ to: "/ai", search: { tab: "approvals" } }) },
  ...Object.fromEntries(settingsPages.map((p) => [`settings_${p}`, { needsId: false, target: () => ({ to: "/settings/$section", params: { section: p } }) } satisfies PageSpec])),
};

export const uiPageIds = Object.keys(pages);

export type OpenAction = { kind: "open"; page: string; id: string; target: Target };

/** A ui_open call as a card: the page and the id it needs, checked, with where it goes; null when it is not on the whitelist. */
export function parseOpen(args: unknown): OpenAction | null {
  if (!args || typeof args !== "object") return null;
  const a = args as Record<string, unknown>;
  const page = typeof a.page === "string" ? a.page : "";
  const spec = pages[page];
  if (!spec) return null;
  const id = typeof a.id === "string" ? a.id : "";
  if (spec.needsId ? !idPattern.test(id) : id !== "") return null;
  return { kind: "open", page, id, target: spec.target(id) };
}

export type SecretKind = "provider_key" | "telegram_token" | "warp_license";
export type SecretAction = { kind: "secret"; secret: SecretKind; provider: string; node: string };

const secretProviders = ["anthropic", "deepseek", "zai", "ollama_cloud"];

/** A ui_secret_form call as a card: the kind of secret and its context (which provider, which node), never a value. */
export function parseSecret(args: unknown): SecretAction | null {
  if (!args || typeof args !== "object") return null;
  const a = args as Record<string, unknown>;
  const provider = typeof a.provider === "string" ? a.provider : "";
  const node = typeof a.node === "string" ? a.node : "";
  switch (a.kind) {
    case "provider_key":
      return secretProviders.includes(provider) && !node ? { kind: "secret", secret: "provider_key", provider, node: "" } : null;
    case "telegram_token":
      return !provider && !node ? { kind: "secret", secret: "telegram_token", provider: "", node: "" } : null;
    case "warp_license":
      return idPattern.test(node) && !provider ? { kind: "secret", secret: "warp_license", provider: "", node } : null;
    default:
      return null;
  }
}

export const isNavigationTool = (name: string) => name === "ui_open" || name === "ui_secret_form";

/** The card for a persisted tool call, or null when it is not an interface call, failed, or does not pass the whitelist. */
export function actionOf(tool: { tool: string; argsJson: string; done: boolean; ok: boolean }): OpenAction | SecretAction | null {
  if (!tool.done || !tool.ok || !isNavigationTool(tool.tool)) return null;
  let args: unknown;
  try {
    args = JSON.parse(tool.argsJson);
  } catch {
    return null;
  }
  return tool.tool === "ui_open" ? parseOpen(args) : parseSecret(args);
}

/** The browser's setting "open pages without asking" ("1" = on): a ui_open page of the turn being written opens at once. */
export const AUTO_OPEN_KEY = "assistant-ui-auto";
const USED_KEY = "assistant-ui-used";
const keep = 300;

type Marks = Record<string, "used" | "dismissed">;
export const readMarks = (): Marks => {
  try {
    return JSON.parse(readPref(USED_KEY) ?? "{}") as Marks;
  } catch {
    return {};
  }
};
export function markAction(id: string, as: "used" | "dismissed") {
  const all = readMarks();
  all[id] = as;
  const ids = Object.keys(all);
  for (const old of ids.slice(0, Math.max(0, ids.length - keep))) delete all[old];
  writePref(USED_KEY, JSON.stringify(all));
}
