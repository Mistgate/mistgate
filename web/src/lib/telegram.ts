import { Code, ConnectError } from "@connectrpc/connect";
import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import type { GetTelegramResponse } from "@/gen/mistgate/admin/v1/telegram_pb";
import { useT, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { telegram } from "./api";
import { errorText } from "./errors";
import { plain, type Plain } from "./plain";

// Everything the Telegram card reads and does (telegram.proto). The server answers with short codes ("telegram_*"); the
// words are in i18n/telegram.ts. The bot token goes up in one request and never comes back: the card keeps no copy.

export type TelegramStatus = NonNullable<Plain<GetTelegramResponse>["status"]>;
export type TelegramLink = NonNullable<TelegramStatus["mine"]>;

export const telegramPollMs = 30_000;
/** While a link code waits for the bot to hear it, the card looks every few seconds. */
export const linkPollMs = 3_000;

export const telegramQuery = queryOptions({
  queryKey: ["telegram"],
  queryFn: async ({ signal }) => plain((await telegram.getTelegram({}, { signal })).status!),
  refetchInterval: telegramPollMs,
});

export const useTelegram = () => useQuery(telegramQuery);

/** The shape of a bot token ("123456789:AA..."): the server checks it again, this only saves a round trip. */
export const looksLikeBotToken = (s: string) => /^\d{5,16}:[A-Za-z0-9_-]{30,100}$/.test(s.trim());

const errorKeys: Record<string, MessageKey> = {
  token_invalid: "tg.err.token",
  telegram_unauthorized: "tg.err.unauthorized",
  telegram_unreachable: "tg.err.unreachable",
  telegram_blocked: "tg.err.blocked",
  telegram_not_configured: "tg.err.notConfigured",
  telegram_not_linked: "tg.err.notLinked",
  telegram_test_too_soon: "tg.err.tooSoon",
};

/** A failed call as one sentence: the panel's own codes are worded here, the rest goes through errorText. */
export function telegramErrorText(e: unknown, t: T): string {
  const c = ConnectError.from(e);
  if (c.code !== Code.PermissionDenied) {
    const key = errorKeys[c.rawMessage];
    if (key) return t(key);
  }
  return errorText(e, t);
}

/** How the bot's last conversation with Telegram went, in words; null when it went fine. */
export function botErrorKey(error: string): MessageKey | null {
  switch (error) {
    case "unauthorized":
      return "tg.bot.error.unauthorized";
    case "conflict":
      return "tg.bot.error.conflict";
    case "":
      return null;
    default:
      return "tg.bot.error.unreachable";
  }
}

export function useTelegramActions() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  const refresh = () => qc.invalidateQueries({ queryKey: telegramQuery.queryKey });
  const fail = (e: unknown) => {
    if (!isStepUpCancelled(e)) toast.error(telegramErrorText(e, t));
  };

  // Setting the bot and starting a link each ask for a fresh step-up. The token is in the call and nowhere else.
  const setBot = useMutation({
    mutationFn: async (token: string) => plain((await guard(() => telegram.setTelegramBot({ token }))).status!),
    onSuccess: (s) => toast(t("tg.bot.saved", { name: s.bot?.username ?? "" })),
    onError: fail,
    onSettled: refresh,
  });
  const clearBot = useMutation({
    mutationFn: async () => {
      await guard(() => telegram.setTelegramBot({ clear: true }));
    },
    onSuccess: () => toast(t("tg.bot.removed")),
    onError: fail,
    onSettled: refresh,
  });
  const beginLink = useMutation({
    mutationFn: async () => plain(await guard(() => telegram.beginTelegramLink({}))),
    onError: fail,
  });
  const unlink = useMutation({
    mutationFn: async () => {
      await telegram.unlinkTelegram({});
    },
    onSuccess: () => toast(t("tg.link.unlinked")),
    onError: fail,
    onSettled: refresh,
  });
  const setAlerts = useMutation({
    mutationFn: async (enabled: boolean) => {
      await telegram.setTelegramAlerts({ enabled });
    },
    onError: fail,
    onSettled: refresh,
  });
  const test = useMutation({
    mutationFn: async () => {
      await telegram.sendTelegramTest({});
    },
    onSuccess: () => toast(t("tg.link.test.sent")),
    onError: fail,
  });
  return { setBot, clearBot, beginLink, unlink, setAlerts, test };
}
