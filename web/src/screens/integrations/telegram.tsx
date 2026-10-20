import { useQuery } from "@tanstack/react-query";
import { useState, type FormEvent } from "react";
import { CopyButton } from "@/components/copy-button";
import { SectionLabel } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { useFmt } from "@/lib/format";
import { clockText, useTick } from "@/lib/integrations";
import {
  botErrorKey,
  linkPollMs,
  looksLikeBotToken,
  telegramPollMs,
  telegramQuery,
  useTelegramActions,
  type TelegramLink,
  type TelegramStatus,
} from "@/lib/telegram";
import { card } from "./parts";

type Code = { code: string; deepLink: string; expiresUnix: number };

const roleKey: Record<string, MessageKey> = { owner: "tg.role.owner", helper: "tg.role.helper", readonly: "tg.role.readonly" };

/** The bot (owner only): paste the token once; afterwards the card shows "set" and the bot's @username, never the token. */
function BotSection({ status }: { status: TelegramStatus }) {
  const t = useT();
  const { setBot, clearBot } = useTelegramActions();
  const bot = status.bot!;
  const [editing, setEditing] = useState(false);
  const [token, setToken] = useState("");
  const [tried, setTried] = useState(false);
  const [removing, setRemoving] = useState(false);
  const form = !bot.configured || editing;
  const errKey = botErrorKey(bot.error);

  function stopEditing() {
    setEditing(false);
    setToken("");
    setTried(false);
  }

  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (setBot.isPending || !looksLikeBotToken(token)) return;
    setBot.mutate(token.trim(), {
      onSuccess: () => {
        setToken("");
        setTried(false);
        setEditing(false);
      },
    });
  }

  return (
    <div className="flex flex-col gap-2.5">
      <SectionLabel>{t("tg.bot.title")}</SectionLabel>
      {errKey && bot.configured && <Notice tone="danger">{t(errKey)}</Notice>}
      {form ? (
        <form onSubmit={submit} noValidate className="flex flex-col gap-3">
          <p className="text-xs leading-normal text-pretty text-muted">{t("tg.bot.lead")}</p>
          <TextField
            label={t("tg.bot.token")}
            hint={bot.configured ? t("tg.bot.replace.hint") : t("tg.bot.token.hint")}
            error={tried && !looksLikeBotToken(token) ? t("tg.err.token") : undefined}
            value={token}
            onChange={(e) => setToken(e.target.value)}
            placeholder={t("tg.bot.token.ph")}
            type="password"
            autoComplete="off"
            spellCheck={false}
            mono
          />
          <div className="flex flex-wrap justify-end gap-2">
            {bot.configured && (
              <Button variant="ghost" size="md" disabled={setBot.isPending} onClick={stopEditing}>
                {t("tg.bot.cancel")}
              </Button>
            )}
            <Button type="submit" variant="primary" size="md" disabled={setBot.isPending}>
              {setBot.isPending ? t("tg.bot.saving") : t("tg.bot.save")}
            </Button>
          </div>
        </form>
      ) : (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 rounded-field border border-line bg-canvas px-3 py-2.5">
          <span data-testid="tg-bot" className="font-mono text-[13px] font-bold">
            @{bot.username}
          </span>
          <span className="text-xs text-muted">{t("tg.bot.set")}</span>
          <span className="ml-auto flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
              {t("tg.bot.replace")}
            </Button>
            <Button variant="ghostDanger" size="sm" onClick={() => setRemoving(true)}>
              {t("tg.bot.remove")}
            </Button>
          </span>
        </div>
      )}
      {removing && (
        <Modal
          open
          onOpenChange={(o) => !o && !clearBot.isPending && setRemoving(false)}
          title={t("tg.bot.remove.title")}
          description={t("tg.bot.remove.body")}
          footer={
            <>
              <Button variant="ghost" size="md" disabled={clearBot.isPending} onClick={() => setRemoving(false)}>
                {t("tg.bot.cancel")}
              </Button>
              <Button variant="danger" size="md" disabled={clearBot.isPending} onClick={() => clearBot.mutate(undefined, { onSuccess: () => setRemoving(false) })}>
                {t("tg.bot.remove.confirm")}
              </Button>
            </>
          }
        />
      )}
    </div>
  );
}

/** The one-time code: a deep link into the bot and the same thing as text, with the time it has left. */
function CodeBox({ c, bot, onCancel, onAgain, busy }: { c: Code; bot: string; onCancel: () => void; onAgain: () => void; busy: boolean }) {
  const t = useT();
  const now = useTick(true);
  const left = Math.max(0, Math.ceil(c.expiresUnix - now / 1000));
  const start = `/start ${c.code}`;
  return (
    <div className="screen-enter flex flex-col gap-3 rounded-card border border-accent-line bg-canvas p-3 md:p-3.5">
      <b className="text-[13px]">{t("tg.link.code.title")}</b>
      {left > 0 ? (
        <>
          <a href={c.deepLink} target="_blank" rel="noopener noreferrer" className={buttonClass("primary", "md")} data-testid="tg-deeplink">
            <Icon name="externalLink" size={14} />
            {t("tg.link.open")}
          </a>
          <div className="flex flex-col gap-1.5">
            <span className="text-xs text-muted">{t("tg.link.or", { bot })}</span>
            <div className="flex items-center gap-2 rounded-field border border-line bg-surface py-1.5 pr-1.5 pl-3">
              <code data-testid="tg-start" className="min-w-0 flex-1 font-mono text-[13px] font-semibold break-all select-all">
                {start}
              </code>
              <CopyButton value={start} />
            </div>
          </div>
          <span className="text-xs leading-snug text-muted">
            {t("tg.link.expires", { time: clockText(left) })} {t("tg.link.waiting")}
          </span>
        </>
      ) : (
        <span className="text-xs leading-snug text-danger-text">{t("tg.link.expired")}</span>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button variant="ghost" size="md" onClick={onCancel}>
          {t("tg.link.cancel")}
        </Button>
        {left === 0 && (
          <Button variant="primary" size="md" disabled={busy} onClick={onAgain}>
            {t("tg.link.again")}
          </Button>
        )}
      </div>
    </div>
  );
}

/** This admin's own chat: link it with a code, switch alerts, test, unlink. */
function LinkSection({ status, code, onCode }: { status: TelegramStatus; code: Code | null; onCode: (c: Code | null) => void }) {
  const t = useT();
  const fmt = useFmt();
  const { beginLink, unlink, setAlerts, test } = useTelegramActions();
  const mine: TelegramLink | undefined = status.mine;

  const begin = () => beginLink.mutate(undefined, { onSuccess: (r) => onCode({ code: r.code, deepLink: r.deepLink, expiresUnix: r.expiresUnix }) });

  return (
    <div className="flex flex-col gap-2.5">
      <SectionLabel>{t("tg.link.title")}</SectionLabel>
      {mine ? (
        <div className="flex flex-col gap-2.5 rounded-field border border-line bg-canvas px-3 py-2.5">
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5">
            <span className="flex items-center gap-1.5 text-[13px] font-bold">
              <Icon name="check" size={14} />
              {t("tg.link.linked")}
            </span>
            <span className="text-xs text-muted">{t("tg.link.since", { date: fmt.date(mine.linkedUnix) })}</span>
          </div>
          <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted">
            <span>{t("tg.link.chatId")}:</span>
            <code data-testid="tg-chat-id" className="font-mono font-semibold text-fg select-all">
              {mine.chatId}
            </code>
            <span>{t("tg.link.chatId.hint")}</span>
          </div>
          <label className="flex items-center gap-3 text-[13px] font-semibold">
            <Switch checked={mine.enabled} disabled={setAlerts.isPending} onCheckedChange={(v) => setAlerts.mutate(v)} aria-label={t("tg.link.alerts")} />
            {t("tg.link.alerts")}
          </label>
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" disabled={test.isPending} onClick={() => test.mutate()}>
              {t("tg.link.test")}
            </Button>
            <Button variant="ghostDanger" size="sm" disabled={unlink.isPending} onClick={() => unlink.mutate()}>
              {t("tg.link.unlink")}
            </Button>
          </div>
        </div>
      ) : code ? (
        <CodeBox c={code} bot={status.bot!.username} busy={beginLink.isPending} onCancel={() => onCode(null)} onAgain={begin} />
      ) : (
        <div className="flex flex-col items-start gap-2.5">
          <p className="text-xs leading-normal text-pretty text-muted">{t("tg.link.none")}</p>
          <Button variant="primary" size="md" disabled={beginLink.isPending} onClick={begin}>
            {beginLink.isPending ? t("tg.link.preparing") : t("tg.link.begin")}
          </Button>
        </div>
      )}
    </div>
  );
}

/** Who linked a chat (owner only): a read-only line per admin. */
function Linked({ links }: { links: TelegramLink[] }) {
  const t = useT();
  if (links.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <SectionLabel>{t("tg.others.title")}</SectionLabel>
      <ul className="flex flex-col">
        {links.map((l) => (
          <li key={l.adminId} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 border-t border-line py-2 text-[13px]">
            <b className="truncate">{l.adminName}</b>
            <span className="text-xs text-muted">{t(roleKey[l.role] ?? "tg.role.readonly")}</span>
            <span className="ml-auto text-xs text-muted">{t(l.enabled ? "tg.others.on" : "tg.others.off")}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * Telegram alerts: the owner sets the bot up (token in, @username out), and every admin links their own chat with a
 * one-time code. A non-owner sees only their own part, and only once the owner has set a bot.
 */
export function TelegramCard({ owner }: { owner: boolean }) {
  const t = useT();
  const [code, setCode] = useState<Code | null>(null);
  const q = useQuery({ ...telegramQuery, refetchInterval: code ? linkPollMs : telegramPollMs });
  const status = q.data;
  const bot = status?.bot;
  // the code is spent the moment the chat is linked
  const waiting = status?.mine ? null : code;

  return (
    <section className={card}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
        <SectionLabel as="h2" icon="bell" tone="sky">
          {t("tg.title")}
        </SectionLabel>
        <span className="text-xs text-muted">{t("tg.sub")}</span>
      </div>
      {q.isPending && <Pending compact />}
      {q.isError && !status && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {status && bot && (
        <>
          {owner && <BotSection status={status} />}
          {!bot.configured && !owner && <p className="text-xs leading-normal text-pretty text-muted">{t("tg.link.notReady")}</p>}
          {bot.configured && <LinkSection status={status} code={waiting} onCode={setCode} />}
          {bot.configured && <p className="text-xs leading-normal text-pretty text-muted">{t(owner ? "tg.what.owner" : "tg.what.other")}</p>}
          {bot.configured && !status.adminUrlKnown && <p className="text-xs leading-normal text-pretty text-muted">{t("tg.noUrl")}</p>}
          {owner && <Linked links={status.links} />}
          {owner && <p className="text-xs leading-normal text-pretty text-muted">{t("tg.worker")}</p>}
        </>
      )}
    </section>
  );
}
