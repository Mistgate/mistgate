// The Cloudflare ("edge") edition does not run everything the VPS one does yet. MeResponse.unavailable lists the feature ids
// (internal/panel/edition); the admin says so in these words instead of showing a button that does nothing. English is the
// source of truth: `ru` is type-checked against it. The admin speaks «ты». Never rename an id: the server sends them.
/** The feature ids MeResponse.unavailable carries (internal/panel/edition). */
export const editionFeatures = ["backups", "telegram", "mcp", "health_checks", "agent_updates", "agent_logs", "panel_update", "decoy_own", "tspu_probe"] as const;
export type EditionFeature = (typeof editionFeatures)[number];

export const en = {
  // the sentence for the refusal "unavailable_in_edition: feature=<id>" (src/lib/errors.ts) and the lead of every notice; {feature} is one of the names below
  "err.unavailable_in_edition": "Not available in the Cloudflare edition yet: {feature}.",
  "edition.feature.backups": "Backups",
  "edition.feature.telegram": "Telegram alerts",
  "edition.feature.mcp": "MCP and API tokens",
  "edition.feature.health_checks": "Tunnel checks",
  "edition.feature.agent_updates": "Updating nodes",
  "edition.feature.agent_logs": "Node logs",
  "edition.feature.panel_update": "Updating the panel",
  "edition.feature.decoy_own": "Own decoy site",
  "edition.feature.tspu_probe": "TSPU probe of the country check",
  // what to do instead, where there is something
  "edition.hint.mcp": "Tokens you already have are listed and can be revoked; new ones cannot be created yet.",
};

export const ru: typeof en = {
  "err.unavailable_in_edition": "Пока недоступно в Cloudflare-версии: {feature}.",
  "edition.feature.backups": "Резервные копии",
  "edition.feature.telegram": "Уведомления в Telegram",
  "edition.feature.mcp": "MCP и API-токены",
  "edition.feature.health_checks": "Проверки туннелей",
  "edition.feature.agent_updates": "Обновление нод",
  "edition.feature.agent_logs": "Логи ноды",
  "edition.feature.panel_update": "Обновление панели",
  "edition.feature.decoy_own": "Свой сайт-ширма",
  "edition.feature.tspu_probe": "ТСПУ-зонд проверки из страны",
  "edition.hint.mcp": "Уже выданные токены видны и отзываются; новые пока создать нельзя.",
};

// Simplified Chinese has one plural form: write a message without "|" (a leftover "a|b" shows the last form).
export const zh: typeof en = {
  "err.unavailable_in_edition": "Cloudflare 版暂不支持：{feature}。",
  "edition.feature.backups": "备份",
  "edition.feature.telegram": "Telegram 告警",
  "edition.feature.mcp": "MCP 和 API 令牌",
  "edition.feature.health_checks": "隧道检测",
  "edition.feature.agent_updates": "更新节点",
  "edition.feature.agent_logs": "节点日志",
  "edition.feature.panel_update": "更新面板",
  "edition.feature.decoy_own": "自有伪装站点",
  "edition.feature.tspu_probe": "国家可达性检测的 TSPU 探测",
  "edition.hint.mcp": "已有的令牌可以查看和吊销，暂时无法创建新令牌。",
};
