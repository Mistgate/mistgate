import { readConfig, type KVBinding, type WatchEnv } from "./env";

const STATE_KEY = "panel";

interface WatchState {
  failures: number;
  firstFailure?: number;
  downSince?: number;
}

function parseState(raw: string | null): WatchState {
  if (!raw) return { failures: 0 };
  try {
    const value: unknown = JSON.parse(raw);
    if (!value || typeof value !== "object") return { failures: 0 };
    const state = value as Partial<WatchState>;
    if (!Number.isSafeInteger(state.failures) || (state.failures ?? 0) < 0) return { failures: 0 };
    return {
      failures: state.failures ?? 0,
      ...(Number.isFinite(state.firstFailure) ? { firstFailure: state.firstFailure } : {}),
      ...(Number.isFinite(state.downSince) ? { downSince: state.downSince } : {}),
    };
  } catch {
    return { failures: 0 };
  }
}

async function probe(urlText: string, timeoutMs: number): Promise<boolean> {
  try {
    const url = new URL(urlText);
    if (url.protocol !== "http:" && url.protocol !== "https:") return false;
    const response = await fetch(url.toString(), {
      method: "GET",
      redirect: "manual",
      signal: AbortSignal.timeout(timeoutMs),
    });
    if (response.body) {
      void response.body.cancel().catch(() => undefined);
    }
    return response.status < 500;
  } catch {
    return false;
  }
}

async function sendMessage(token: string, chatId: string, text: string): Promise<boolean> {
  const url = "https://api.telegram.org/bot" + token + "/sendMessage";
  try {
    const response = await fetch(url, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ chat_id: chatId, text }),
      signal: AbortSignal.timeout(10_000),
    });
    return response.ok;
  } catch {
    // Do not log this request URL: it contains the bot token.
    return false;
  }
}

export function formatDuration(durationMs: number, lang: "en" | "ru"): string {
  const totalMinutes = Math.max(1, Math.floor(durationMs / 60_000));
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;
  const parts: string[] = [];
  if (days) parts.push(String(days) + (lang === "ru" ? " д" : " d"));
  if (hours) parts.push(String(hours) + (lang === "ru" ? " ч" : " h"));
  if (minutes || parts.length === 0) parts.push(String(minutes || totalMinutes) + (lang === "ru" ? " мин" : " min"));
  return parts.join(" ");
}

function downMessage(name: string, durationMs: number, lang: "en" | "ru"): string {
  const duration = formatDuration(durationMs, lang);
  if (lang === "ru") return name + " не отвечает уже " + duration + ".";
  return name + " is not answering: down for " + duration + ".";
}

function upMessage(name: string, durationMs: number, lang: "en" | "ru"): string {
  const duration = formatDuration(durationMs, lang);
  if (lang === "ru") return name + " снова отвечает. Не работала " + duration + ".";
  return name + " is back. It was down for " + duration + ".";
}

async function saveState(kv: KVBinding, state: WatchState): Promise<void> {
  await kv.put(STATE_KEY, JSON.stringify(state));
}

export async function check(env: WatchEnv): Promise<void> {
  const config = readConfig(env);
  // Not configured: do nothing rather than report a missing URL as a dead panel.
  if (!config.botToken || !config.chatId || !config.healthUrl) return;

  const [healthy, rawState] = await Promise.all([
    probe(config.healthUrl, config.healthTimeoutMs),
    env.WATCH.get(STATE_KEY),
  ]);
  const state = parseState(rawState);
  const now = Date.now();

  if (!healthy) {
    if (state.downSince !== undefined) return;
    const failures = state.failures + 1;
    const firstFailure = state.firstFailure ?? now;
    if (failures < config.alertAfter) {
      await saveState(env.WATCH, { failures, firstFailure });
      return;
    }
    const text = downMessage(config.alertName, now - firstFailure, config.alertLang);
    if (!(await sendMessage(config.botToken, config.chatId, text))) return;
    await saveState(env.WATCH, { failures, firstFailure, downSince: firstFailure });
    return;
  }

  if (state.downSince !== undefined) {
    const text = upMessage(config.alertName, now - state.downSince, config.alertLang);
    if (!(await sendMessage(config.botToken, config.chatId, text))) return;
    await env.WATCH.delete(STATE_KEY);
  } else if (state.failures > 0) {
    await env.WATCH.delete(STATE_KEY);
  }
}
