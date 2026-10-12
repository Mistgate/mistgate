// The Cloudflare ("edge") edition does not run everything the VPS one does yet. MeResponse.unavailable lists the feature ids
// (internal/panel/edition); the admin says so in these words instead of showing a button that does nothing. English is the
// source of truth: `ru` is type-checked against it. The admin speaks «ты». Never rename an id: the server sends them.
/** The feature ids MeResponse.unavailable carries (internal/panel/edition). */
export const editionFeatures = ["backups", "telegram", "mcp", "provision", "health_checks", "agent_updates", "agent_logs", "panel_update"] as const;
export type EditionFeature = (typeof editionFeatures)[number];

export const en = {
  // the sentence for the refusal "unavailable_in_edition: feature=<id>" (src/lib/errors.ts) and the lead of every notice; {feature} is one of the names below
  "err.unavailable_in_edition": "Not available in the Cloudflare edition yet: {feature}.",
  "edition.feature.backups": "Backups",
  "edition.feature.telegram": "Telegram alerts",
  "edition.feature.mcp": "MCP and API tokens",
  "edition.feature.provision": "Installing a node over SSH",
  "edition.feature.health_checks": "Tunnel checks",
  "edition.feature.agent_updates": "Updating nodes",
  "edition.feature.agent_logs": "Node logs",
  "edition.feature.panel_update": "Updating the panel",
  // what to do instead, where there is something
  "edition.hint.provision": "Install the node by hand: create it, then run the one-time command on the server.",
  "edition.hint.mcp": "Tokens you already have are listed and can be revoked; new ones cannot be created yet.",
};

export const ru: typeof en = {
  "err.unavailable_in_edition": "Пока недоступно в Cloudflare-версии: {feature}.",
  "edition.feature.backups": "Резервные копии",
  "edition.feature.telegram": "Уведомления в Telegram",
  "edition.feature.mcp": "MCP и API-токены",
  "edition.feature.provision": "Установка ноды по SSH",
  "edition.feature.health_checks": "Проверки туннелей",
  "edition.feature.agent_updates": "Обновление нод",
  "edition.feature.agent_logs": "Логи ноды",
  "edition.feature.panel_update": "Обновление панели",
  "edition.hint.provision": "Поставь ноду вручную: создай её и выполни одноразовую команду на сервере.",
  "edition.hint.mcp": "Уже выданные токены видны и отзываются; новые пока создать нельзя.",
};
