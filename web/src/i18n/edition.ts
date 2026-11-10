// The Cloudflare ("edge") edition does not run everything the VPS one does yet. MeResponse.unavailable lists the feature ids
// (internal/panel/edition); the admin says so in these words instead of showing a button that does nothing. English is the
// source of truth: `ru` is type-checked against it. The admin speaks «ты». Never rename an id: the server sends them.
/** The feature ids MeResponse.unavailable carries (internal/panel/edition). */
export const editionFeatures = ["backups", "mcp", "assistant", "health_checks", "agent_updates", "agent_logs", "panel_update", "decoy_own", "tspu_probe", "os_updates"] as const;
export type EditionFeature = (typeof editionFeatures)[number];

export const en = {
  // the sentence for the refusal "unavailable_in_edition: feature=<id>" (src/lib/errors.ts) and the lead of every notice; {feature} is one of the names below
  "err.unavailable_in_edition": "Not available in the Cloudflare edition yet: {feature}.",
  "edition.feature.backups": "Backups",
  "edition.feature.mcp": "MCP and API tokens",
  "edition.feature.health_checks": "Tunnel checks",
  "edition.feature.agent_updates": "Updating nodes",
  "edition.feature.agent_logs": "Node logs",
  "edition.feature.panel_update": "Updating the panel",
  "edition.feature.decoy_own": "Own decoy site",
  "edition.feature.tspu_probe": "TSPU probe of the country check",
  "edition.feature.os_updates": "Operating-system updates of the servers",
  "edition.feature.assistant": "The assistant",
  // what to do instead, where there is something
  "edition.hint.mcp": "Tokens you already have are listed and can be revoked; new ones cannot be created yet.",
};

export const ru: typeof en = {
  "err.unavailable_in_edition": "Пока недоступно в Cloudflare-версии: {feature}.",
  "edition.feature.backups": "Резервные копии",
  "edition.feature.mcp": "MCP и API-токены",
  "edition.feature.health_checks": "Проверки туннелей",
  "edition.feature.agent_updates": "Обновление нод",
  "edition.feature.agent_logs": "Логи ноды",
  "edition.feature.panel_update": "Обновление панели",
  "edition.feature.decoy_own": "Свой сайт-ширма",
  "edition.feature.tspu_probe": "ТСПУ-зонд проверки из страны",
  "edition.feature.os_updates": "Обновления операционной системы серверов",
  "edition.feature.assistant": "Ассистент",
  "edition.hint.mcp": "Уже выданные токены видны и отзываются; новые пока создать нельзя.",
};

// Simplified Chinese has one plural form: write a message without "|" (a leftover "a|b" shows the last form).
export const zh: typeof en = {
  "err.unavailable_in_edition": "Cloudflare 版暂不支持：{feature}。",
  "edition.feature.backups": "备份",
  "edition.feature.mcp": "MCP 和 API 令牌",
  "edition.feature.health_checks": "隧道检测",
  "edition.feature.agent_updates": "更新节点",
  "edition.feature.agent_logs": "节点日志",
  "edition.feature.panel_update": "更新面板",
  "edition.feature.decoy_own": "自有伪装站点",
  "edition.feature.tspu_probe": "国家可达性检测的 TSPU 探测",
  "edition.feature.os_updates": "服务器操作系统更新",
  "edition.feature.assistant": "助手",
  "edition.hint.mcp": "已有的令牌可以查看和吊销，暂时无法创建新令牌。",
};

export const fa: typeof en = {
  "err.unavailable_in_edition": "هنوز در نسخهٔ Cloudflare در دسترس نیست: {feature}.",
  "edition.feature.backups": "نسخه‌های پشتیبان",
  "edition.feature.mcp": "MCP و توکن‌های API",
  "edition.feature.health_checks": "بررسی تونل‌ها",
  "edition.feature.agent_updates": "به‌روزرسانی نودها",
  "edition.feature.agent_logs": "لاگ‌های نود",
  "edition.feature.panel_update": "به‌روزرسانی پنل",
  "edition.feature.decoy_own": "سایت پوششی اختصاصی",
  "edition.feature.tspu_probe": "نقطهٔ سنجش TSPU در بررسی از داخل کشور",
  "edition.feature.os_updates": "به‌روزرسانی سیستم‌عامل سرورها",
  "edition.feature.assistant": "دستیار",
  "edition.hint.mcp": "توکن‌هایی که قبلاً صادر شده‌اند دیده و ابطال می‌شوند؛ ساختن توکن جدید هنوز ممکن نیست.",
};

export const tr: typeof en = {
  "err.unavailable_in_edition": "Cloudflare sürümünde henüz yok: {feature}.",
  "edition.feature.backups": "Yedekler",
  "edition.feature.mcp": "MCP ve API token'ları",
  "edition.feature.health_checks": "Tünel kontrolleri",
  "edition.feature.agent_updates": "Düğümleri güncelleme",
  "edition.feature.agent_logs": "Düğüm günlükleri",
  "edition.feature.panel_update": "Paneli güncelleme",
  "edition.feature.decoy_own": "Kendi kamuflaj sitesi",
  "edition.feature.tspu_probe": "Ülke kontrolünün TSPU ölçüm noktası",
  "edition.feature.os_updates": "Sunucuların işletim sistemi güncellemeleri",
  "edition.feature.assistant": "Asistan",
  "edition.hint.mcp": "Daha önce verilmiş token'lar listelenir ve iptal edilebilir; yenileri henüz oluşturulamaz.",
};
