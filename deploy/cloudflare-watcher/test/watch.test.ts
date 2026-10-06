import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { check } from "../src/watch";
import type { WatchEnv } from "../src/env";
import { MemoryKV } from "./helpers";

const BOT_TOKEN = "test-token";
const CHAT_ID = "test-chat";
const START = new Date("2026-01-01T00:00:00.000Z");

function makeEnv(overrides: Partial<WatchEnv> = {}): WatchEnv {
  return {
    WATCH: new MemoryKV(),
    HEALTH_URL: "https://panel.example.com/",
    TELEGRAM_BOT_TOKEN: BOT_TOKEN,
    TELEGRAM_CHAT_ID: CHAT_ID,
    ...overrides,
  };
}

function health(status: number): Response {
  return new Response(null, { status });
}

function telegram(status = 200): Response {
  return new Response(null, { status });
}

function fetchMock(env: WatchEnv, healthResponse: () => Promise<Response> | Response, telegramResponse = telegram()) {
  const mock = vi.fn<typeof fetch>(async (input) => {
    if (String(input).startsWith("https://api.telegram.org/")) return telegramResponse;
    return healthResponse();
  });
  vi.stubGlobal("fetch", mock);
  return mock;
}

async function failTicks(env: WatchEnv, count: number): Promise<void> {
  for (let i = 0; i < count; i += 1) {
    await check(env);
    if (i + 1 < count) vi.advanceTimersByTime(60_000);
  }
}

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(START);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("panel watcher", () => {
  it("does not fetch or read KV without both Telegram secrets", async () => {
    const kv = new MemoryKV();
    const env = makeEnv({ WATCH: kv, TELEGRAM_CHAT_ID: undefined });
    const mock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", mock);

    await check(env);

    expect(mock).not.toHaveBeenCalled();
    expect(kv.reads).toBe(0);
  });

  it("does nothing without a HEALTH_URL instead of reporting the panel down", async () => {
    const kv = new MemoryKV();
    const env = makeEnv({ WATCH: kv, HEALTH_URL: "  " });
    const mock = vi.fn<typeof fetch>();
    vi.stubGlobal("fetch", mock);

    await failTicks(env, 5);

    expect(mock).not.toHaveBeenCalled();
    expect(kv.reads).toBe(0);
  });

  it("treats the decoy 404 as healthy and uses manual redirects", async () => {
    const env = makeEnv();
    const mock = fetchMock(env, () => health(404));

    await check(env);

    expect(mock).toHaveBeenCalledTimes(1);
    expect(mock.mock.calls[0][1]).toMatchObject({ method: "GET", redirect: "manual" });
    expect((env.WATCH as MemoryKV).writes).toHaveLength(0);
  });

  it("counts 5xx responses as failures", async () => {
    const env = makeEnv({ ALERT_AFTER: "2" });
    const mock = fetchMock(env, () => health(502));

    await check(env);
    expect((env.WATCH as MemoryKV).writes).toHaveLength(1);
    await check(env);

    expect(mock).toHaveBeenCalledTimes(3);
    expect((env.WATCH as MemoryKV).writes).toHaveLength(2);
    expect(String(mock.mock.calls[2][0])).toContain("api.telegram.org");
  });

  it("counts a timeout as a failure", async () => {
    const env = makeEnv();
    fetchMock(env, async () => {
      const error = new Error("timed out");
      error.name = "TimeoutError";
      throw error;
    });

    await check(env);

    expect((env.WATCH as MemoryKV).writes).toHaveLength(1);
  });

  it.each(["certificate verification failed", "connection failed"])("counts TLS and connection errors as failures: %s", async (message) => {
    const env = makeEnv();
    fetchMock(env, async () => {
      throw new TypeError(message);
    });

    await check(env);

    expect((env.WATCH as MemoryKV).writes).toHaveLength(1);
  });

  it("sends one down alert after consecutive failures and writes nothing during a long outage", async () => {
    const env = makeEnv();
    const kv = env.WATCH as MemoryKV;
    const mock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("https://api.telegram.org/")) return telegram();
      return health(503);
    });
    vi.stubGlobal("fetch", mock);

    await check(env);
    vi.advanceTimersByTime(60_000);
    await check(env);
    vi.advanceTimersByTime(60_000);
    await check(env);
    for (let i = 0; i < 10; i += 1) await check(env);

    const sends = mock.mock.calls.filter(([input]) => String(input).startsWith("https://api.telegram.org/"));
    expect(sends).toHaveLength(1);
    expect(JSON.parse(String(sends[0][1]?.body))).toMatchObject({
      chat_id: CHAT_ID,
      text: "Mistgate panel is not answering: down for 2 min.",
    });
    expect(kv.writes).toHaveLength(3);
  });

  it("retries a down alert when Telegram rejects the first send", async () => {
    const env = makeEnv();
    const kv = env.WATCH as MemoryKV;
    let sends = 0;
    const mock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("https://api.telegram.org/")) {
        sends += 1;
        return telegram(sends === 1 ? 500 : 200);
      }
      return health(503);
    });
    vi.stubGlobal("fetch", mock);

    await check(env);
    await check(env);
    await check(env);
    expect(kv.writes).toHaveLength(2);
    await check(env);

    expect(sends).toBe(2);
    expect(kv.writes).toHaveLength(3);
  });

  it("clears a short failure streak after a healthy response", async () => {
    const env = makeEnv({ ALERT_AFTER: "3" });
    const kv = env.WATCH as MemoryKV;
    let status = 503;
    fetchMock(env, () => health(status));

    await check(env);
    await check(env);
    status = 404;
    await check(env);
    status = 503;
    await check(env);
    await check(env);

    expect(kv.writes).toHaveLength(4);
    expect(kv.deletes).toHaveLength(1);
  });

  it("sends one recovery message with the outage duration", async () => {
    const env = makeEnv();
    const kv = env.WATCH as MemoryKV;
    let status = 503;
    const mock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("https://api.telegram.org/")) return telegram();
      return health(status);
    });
    vi.stubGlobal("fetch", mock);

    await failTicks(env, 3);
    status = 404;
    vi.setSystemTime(new Date(START.getTime() + 7 * 60_000));
    await check(env);
    await check(env);

    const sends = mock.mock.calls.filter(([input]) => String(input).startsWith("https://api.telegram.org/"));
    expect(sends).toHaveLength(2);
    expect(JSON.parse(String(sends[1][1]?.body))).toMatchObject({
      text: "Mistgate panel is back. It was down for 7 min.",
    });
    expect(kv.deletes).toHaveLength(1);
  });

  it("retries a recovery alert when Telegram rejects the first send", async () => {
    const env = makeEnv();
    const kv = env.WATCH as MemoryKV;
    let status = 503;
    let sends = 0;
    const mock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("https://api.telegram.org/")) {
        sends += 1;
        return telegram(sends === 2 ? 500 : 200);
      }
      return health(status);
    });
    vi.stubGlobal("fetch", mock);

    await failTicks(env, 3);
    status = 200;
    await check(env);
    expect(kv.deletes).toHaveLength(0);
    await check(env);

    expect(sends).toBe(3);
    expect(kv.deletes).toHaveLength(1);
  });

  it("does not write KV on a healthy minute", async () => {
    const env = makeEnv();
    fetchMock(env, () => health(200));

    await check(env);

    expect((env.WATCH as MemoryKV).writes).toHaveLength(0);
    expect((env.WATCH as MemoryKV).deletes).toHaveLength(0);
  });

  it("formats outage messages in Russian", async () => {
    const env = makeEnv({ ALERT_LANG: "ru", ALERT_NAME: "Панель" });
    let status = 503;
    const mock = vi.fn<typeof fetch>(async (input) => {
      if (String(input).startsWith("https://api.telegram.org/")) return telegram();
      return health(status);
    });
    vi.stubGlobal("fetch", mock);

    await failTicks(env, 3);
    status = 200;
    vi.setSystemTime(new Date(START.getTime() + 4 * 60_000));
    await check(env);

    const sends = mock.mock.calls.filter(([input]) => String(input).startsWith("https://api.telegram.org/"));
    expect(JSON.parse(String(sends[0][1]?.body))).toMatchObject({ text: "Панель не отвечает уже 2 мин." });
    expect(JSON.parse(String(sends[1][1]?.body))).toMatchObject({ text: "Панель снова отвечает. Не работала 4 мин." });
  });
});
