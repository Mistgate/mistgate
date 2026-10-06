export interface KVBinding {
  get(key: string): Promise<string | null>;
  put(key: string, value: string): Promise<void>;
  delete(key: string): Promise<void>;
}

export interface WatchEnv {
  WATCH: KVBinding;
  HEALTH_URL?: string;
  HEALTH_TIMEOUT_MS?: string;
  ALERT_AFTER?: string;
  ALERT_NAME?: string;
  ALERT_LANG?: string;
  TELEGRAM_BOT_TOKEN?: string;
  TELEGRAM_CHAT_ID?: string;
}

export interface WatchConfig {
  healthUrl: string;
  healthTimeoutMs: number;
  alertAfter: number;
  alertName: string;
  alertLang: "en" | "ru";
  botToken: string;
  chatId: string;
}

function positiveInteger(value: string | undefined, fallback: number): number {
  if (!value) return fallback;
  const parsed = Number(value);
  return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : fallback;
}

export function readConfig(env: WatchEnv): WatchConfig {
  return {
    healthUrl: (env.HEALTH_URL ?? "").trim(),
    healthTimeoutMs: positiveInteger(env.HEALTH_TIMEOUT_MS, 8000),
    alertAfter: positiveInteger(env.ALERT_AFTER, 3),
    alertName: env.ALERT_NAME?.trim() || "Mistgate panel",
    alertLang: env.ALERT_LANG === "ru" ? "ru" : "en",
    botToken: (env.TELEGRAM_BOT_TOKEN ?? "").trim(),
    chatId: (env.TELEGRAM_CHAT_ID ?? "").trim(),
  };
}
