import { ConnectError } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from "react";
import { Logo } from "@/components/brand";
import { Button, buttonClass } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { clockText, useTick } from "@/lib/integrations";
import { meQuery } from "@/lib/session";
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
import { CopyBtn, Dlg, ErrCard, Gl, ICONS, Pill, Sec, Skeleton, useScene } from "../ai/bits";
import "./integrations.css";

export type LinkCode = { code: string; deepLink: string; expiresUnix: number };

// the board's glyphs (24 grid)
const G = {
  bot: "M9 3h6M12 3v4M5 7h14v11H5zM9 12v2M15 12v2M2 12v3M22 12v3",
  chat: "M4 5h16v11H9l-5 4z",
  admins: "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM3 20a6 6 0 0 1 12 0M16 4.5a3.5 3.5 0 0 1 0 6.5M17.5 14.5A6 6 0 0 1 21 20",
  bell: "M6 16V11a6 6 0 0 1 12 0v5l1.5 2h-15zM10 20a2 2 0 0 0 4 0",
  health: "M3 12h4l2-5 4 10 2-5h6",
  update: "M4 12a8 8 0 0 1 14-5.3L20 9M20 4v5h-5M20 12a8 8 0 0 1-14 5.3L4 15M4 20v-5h5",
  backup: "M4 7l8-4 8 4v10l-8 4-8-4zM4 7l8 4 8-4M12 11v10",
  plan: "M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z",
  lock: ICONS.lock,
  arrow: "M5 12h14M13 6l6 6-6 6",
} as const;

type Tone = "off" | "wait" | "bad" | "on" | "mute";
const roleKey: Record<string, MessageKey> = { owner: "tg.role.owner", helper: "tg.role.helper", readonly: "tg.role.readonly" };

/** The chain on top of the card: panel, bot, your chat, and the wires between them say in one look how far the alerts get. */
function Chain({ title, bot, chat, hasBot, w1, w2, n2, n3 }: { title: string; bot: string; chat: string; hasBot: boolean; w1: Tone; w2: Tone; n2: Tone; n3: Tone }) {
  const t = useT();
  const tone: Tone = !hasBot ? "off" : n3;
  return (
    <div className={cx("ig-chain", tone)} role="img" aria-label={title}>
      <div className="ig-track">
        <span className="ig-nt on">
          <Logo size={26} />
        </span>
        <i className={cx("ig-w", w1)} aria-hidden="true">
          <i />
        </i>
        <span className={cx("ig-nt", n2)}>
          <Gl d={G.bot} />
        </span>
        <i className={cx("ig-w", w2)} aria-hidden="true">
          <i />
        </i>
        <span className={cx("ig-nt", n3)}>
          <Gl d={G.chat} />
        </span>
      </div>
      <div className="ig-lbls">
        <span>
          <b>{t("igp.n.panel")}</b>
          <em className="mono">{location.host}</em>
        </span>
        <span>
          <b>{t("igp.n.bot")}</b>
          <em className="mono">{bot}</em>
        </span>
        <span>
          <b>{t("igp.n.chat")}</b>
          <em className="mono">{chat}</em>
        </span>
      </div>
    </div>
  );
}

/** The one-time code: a link into the bot and the same thing as text, with the time it has left (the board waits for Start; the panel needs this to hear it). */
function CodeBox({ c, bot, onAgain, busy }: { c: LinkCode; bot: string; onAgain: () => void; busy: boolean }) {
  const t = useT();
  const now = useTick(true);
  const left = Math.max(0, Math.ceil(c.expiresUnix - now / 1000));
  const start = `/start ${c.code}`;
  return (
    <div className="ig-form ig-code">
      {left > 0 ? (
        <>
          <p>{t("tg.link.code.title")}</p>
          <a href={c.deepLink} target="_blank" rel="noopener noreferrer" className={cx(buttonClass("primary", "md"), "ig-open")} data-testid="tg-deeplink">
            {t("tg.link.open")}
          </a>
          <div className="ig-start">
            <span>{t("tg.link.or", { bot })}</span>
            <div>
              <code data-testid="tg-start" className="mono">
                {start}
              </code>
              <CopyBtn value={start} />
            </div>
          </div>
          <p className="ig-mut">
            {t("tg.link.expires", { time: clockText(left) })} {t("tg.link.waiting")}
          </p>
        </>
      ) : (
        <>
          <p className="ig-bad">{t("tg.link.expired")}</p>
          <div className="ai-na">
            <Button size="md" variant="primary" disabled={busy} onClick={onAgain}>
              {t("tg.link.again")}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}

/** The bot row (the owner sets it, the others only see whether there is one). */
function BotRow({ status, owner, flash, replacing, setReplacing, onSaved }: { status: TelegramStatus; owner: boolean; flash: boolean; replacing: boolean; setReplacing: (v: boolean) => void; onSaved: () => void }) {
  const t = useT();
  const scene = useScene();
  const { setBot, clearBot } = useTelegramActions();
  const bot = status.bot!;
  const hasBot = bot.configured;
  const [token, setToken] = useState(scene.has("filled") ? "123456789:AAnot-a-real-token-FakeFakeFakeFake_x" : "");
  const [tried, setTried] = useState(scene.has("ferr"));
  const [removing, setRemoving] = useState(scene.has("dlg-bot"));
  const form = owner && (!hasBot || replacing);
  const busy = setBot.isPending || scene.has("fbusy");
  const bad = tried && !looksLikeBotToken(token);
  const errKey = botErrorKey(bot.error);

  function stop() {
    setReplacing(false);
    setToken("");
    setTried(false);
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (busy || !looksLikeBotToken(token)) return;
    setBot.mutate(token.trim(), {
      onSuccess: () => {
        setToken("");
        setTried(false);
        setReplacing(false);
        onSaved();
      },
    });
  }

  return (
    <div className={cx("pp-row ig-row ig-bot", flash && "flash")}>
      <div className="pp-lb">
        <b>{t("igp.row.bot")}</b>
      </div>
      <div className="pp-ct">
        {hasBot && !form && (
          <div className="ig-line">
            <span className="ig-id">
              <b className="mono" data-testid="tg-bot">
                @{bot.username}
              </b>
            </span>
            {owner && (
              <span className="ig-act">
                <Button size="md" onClick={() => setReplacing(true)}>
                  {t("igp.bot.replace")}
                </Button>
                <button type="button" className="ig-lk" onClick={() => setRemoving(true)}>
                  {t("igp.bot.remove")}
                </button>
              </span>
            )}
          </div>
        )}
        {hasBot && !form && errKey && <p className="ig-mut ig-bad">{t(errKey)}</p>}
        {!hasBot && !owner && <p className="ig-mut">{t("igp.bot.none")}</p>}
        {form && (
          <form className="ig-form" onSubmit={submit} noValidate>
            <p>{t(replacing ? "igp.bot.leadReplace" : "igp.bot.lead")}</p>
            <TextField
              label={t("igp.bot.token")}
              hint={replacing ? t("tg.bot.replace.hint") : undefined}
              error={bad ? t("igp.bot.tokenBad") : undefined}
              value={token}
              onChange={(e) => setToken(e.target.value)}
              placeholder={t("tg.bot.token.ph")}
              type="password"
              autoComplete="off"
              spellCheck={false}
              mono
            />
            <div className="ai-na">
              {replacing && (
                <Button size="md" disabled={busy} onClick={stop}>
                  {t("igp.cancel")}
                </Button>
              )}
              <Button type="submit" size="md" variant="primary" disabled={busy}>
                {busy && <span className="an-sp" aria-hidden="true" />}
                {t(busy ? "igp.bot.saving" : "igp.bot.save")}
              </Button>
            </div>
          </form>
        )}
      </div>
      {removing && (
        <Dlg
          title={t("igp.dBot.t")}
          onClose={() => setRemoving(false)}
          busy={clearBot.isPending}
          footer={
            <>
              <Button size="lg" disabled={clearBot.isPending} onClick={() => setRemoving(false)}>
                {t("igp.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={clearBot.isPending} onClick={() => clearBot.mutate(undefined, { onSuccess: () => setRemoving(false) })}>
                {t("igp.dBot.go")}
              </Button>
            </>
          }
        >
          <p>{t("igp.dBot.body")}</p>
        </Dlg>
      )}
    </div>
  );
}

/** This admin's own chat: link with a code, test, unlink; and, once linked, its id and the alerts switch. */
function ChatRows({ status, code, setCode, flash, onTest, tDone, linking }: { status: TelegramStatus; code: LinkCode | null; setCode: (c: LinkCode | null) => void; flash: boolean; onTest: () => void; tDone: boolean; linking: boolean }) {
  const t = useT();
  const fmt = useFmt();
  const scene = useScene();
  const { beginLink, unlink, setAlerts, test } = useTelegramActions();
  const toast = useToast();
  const mine: TelegramLink | undefined = status.mine;
  const hasBot = status.bot!.configured;
  const [unlinking, setUnlinking] = useState(scene.has("dlg-un"));
  const testing = test.isPending || scene.has("tbusy");
  const begin = () => beginLink.mutate(undefined, { onSuccess: (r) => setCode({ code: r.code, deepLink: r.deepLink, expiresUnix: r.expiresUnix }) });
  const alerts = mine?.enabled ?? false;

  return (
    <>
      <div className={cx("pp-row ig-row ig-me", flash && "flash")}>
        <div className="pp-lb">
          <b>{t("igp.row.chat")}</b>
        </div>
        <div className="pp-ct">
          {mine && (
            <div className="ig-line">
              <span className="ig-id">
                <b>{t("igp.chat.linked")}</b>
                <em>{t("tg.link.since", { date: fmt.date(mine.linkedUnix) })}</em>
              </span>
              <span className="ig-act">
                <Button size="md" className={cx(testing && "busy", tDone && "done")} disabled={testing} onClick={onTest}>
                  {testing && <span className="an-sp" aria-hidden="true" />}
                  {t(testing ? "igp.chat.testing" : tDone ? "igp.chat.tested" : "igp.chat.test")}
                </Button>
                <button type="button" className="ig-lk" disabled={unlink.isPending} onClick={() => setUnlinking(true)}>
                  {t("igp.chat.unlink")}
                </button>
              </span>
            </div>
          )}
          {!mine && !linking && (
            <div className="ig-line">
              <span className="ig-id">
                <b className="ig-off">{t("igp.chat.off")}</b>
                {!hasBot && <em>{t("igp.chat.needBot")}</em>}
              </span>
              {hasBot && (
                <span className="ig-act">
                  <Button size="md" variant="primary" disabled={beginLink.isPending} onClick={begin}>
                    {beginLink.isPending && <span className="an-sp" aria-hidden="true" />}
                    {t(beginLink.isPending ? "tg.link.preparing" : "igp.chat.link")}
                  </Button>
                </span>
              )}
            </div>
          )}
          {!mine && linking && (
            <>
              <div className="ig-line">
                <span className="ig-id">
                  <span className="an-sp" aria-hidden="true" />
                  <b>{t("igp.chat.waiting")}</b>
                </span>
                <span className="ig-act">
                  <Button size="md" onClick={() => setCode(null)}>
                    {t("igp.cancel")}
                  </Button>
                </span>
              </div>
              {code && <CodeBox c={code} bot={status.bot!.username} busy={beginLink.isPending} onAgain={begin} />}
            </>
          )}
        </div>
      </div>
      {mine && (
        <>
          <div className="pp-row ig-row">
            <div className="pp-lb">
              <b>{t("igp.row.chatId")}</b>
            </div>
            <div className="pp-ct">
              <div className="ig-line">
                <span className="ig-id">
                  <b className="mono" data-testid="tg-chat-id">
                    {String(mine.chatId)}
                  </b>
                </span>
                <span className="ig-act">
                  <CopyBtn value={String(mine.chatId)} />
                </span>
              </div>
              <p className="ig-mut">{t("igp.chat.idNote")}</p>
            </div>
          </div>
          <div className="pp-row ig-row">
            <div className="pp-lb">
              <b>{t("igp.row.alerts")}</b>
            </div>
            <div className="pp-ct">
              <div className="ig-line">
                <span className="ig-id">
                  <b>{t(alerts ? "igp.alerts.on" : "igp.alerts.off")}</b>
                </span>
                <Switch
                  checked={alerts}
                  disabled={setAlerts.isPending}
                  onCheckedChange={(v) => setAlerts.mutate(v, { onSuccess: () => toast(t(v ? "igp.t.unmute" : "igp.t.mute")) })}
                  aria-label={t("igp.alerts.aria")}
                />
              </div>
            </div>
          </div>
        </>
      )}
      {unlinking && (
        <Dlg
          title={t("igp.dUn.t")}
          onClose={() => setUnlinking(false)}
          busy={unlink.isPending}
          footer={
            <>
              <Button size="lg" disabled={unlink.isPending} onClick={() => setUnlinking(false)}>
                {t("igp.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={unlink.isPending} onClick={() => unlink.mutate(undefined, { onSuccess: () => setUnlinking(false) })}>
                {t("igp.dUn.go")}
              </Button>
            </>
          }
        >
          <p>{t("igp.dUn.body")}</p>
        </Dlg>
      )}
    </>
  );
}

/** Who linked a chat: the owner sees every admin, anyone else only their own line. */
function Admins({ status, owner, myId }: { status: TelegramStatus; owner: boolean; myId: string }) {
  const t = useT();
  const rows = owner ? status.links : status.mine ? [status.mine] : [];
  return (
    <Sec tone="lav" d={G.admins} title={t("igp.adm.title")} foot={t("igp.adm.foot")}>
      {rows.length === 0 && <p className="sb-emp ai-emp">{t("igp.adm.none")}</p>}
      <div className="ai-recs ig-adm">
        {rows.map((a) => (
          <div key={a.adminId} className="ai-rec">
            <div className="ig-ad">
              <span className="ai-mono" aria-hidden="true">
                {(a.adminName[0] ?? "?").toUpperCase()}
              </span>
              <span>
                <b>
                  {a.adminName}
                  {a.adminId === myId && <i className="uq-otg">{t("igp.adm.you")}</i>}
                </b>
                <em>{t(roleKey[a.role] ?? "tg.role.readonly")}</em>
              </span>
            </div>
            <Pill kind={a.enabled ? "ok" : ""}>{t(a.enabled ? "igp.adm.on" : "igp.adm.off")}</Pill>
          </div>
        ))}
      </div>
    </Sec>
  );
}

/** What comes to this admin: the first line for everyone, the rest for the owner; a dot where the chain is complete. */
function Gets({ owner, role, live }: { owner: boolean; role: string; live: boolean }) {
  const t = useT();
  const items: { key: MessageKey; d: string; tone: "sand" | "sky" | "sage" | "lav" | "mint"; show: boolean }[] = [
    { key: "igp.gets.health", d: G.health, tone: "sand", show: true },
    { key: "igp.gets.updates", d: G.update, tone: "sky", show: owner },
    { key: "igp.gets.backup", d: G.backup, tone: "sage", show: owner },
    { key: "igp.gets.plans", d: G.plan, tone: "lav", show: owner },
    { key: "igp.gets.login", d: G.lock, tone: "mint", show: owner },
  ];
  return (
    <Sec tone="sky" d={G.bell} title={t("igp.gets.title")} foot={t(owner ? "igp.role.owner" : role === "helper" ? "igp.role.helper" : "igp.role.readonly")}>
      <div className="ig-gets">
        {items.map((g) => {
          const on = g.show && live;
          return (
            <div key={g.key} className={cx("ig-get", g.show ? (on ? "on" : "idle") : "no")}>
              <span className={cx("pp-ic", g.tone === "sage" ? "mint" : g.tone)} aria-hidden="true">
                <Gl d={g.d} />
              </span>
              <b>{t(g.key)}</b>
              <i className={cx("rw-gl", on && "ok")} aria-hidden="true" />
            </div>
          );
        })}
      </div>
    </Sec>
  );
}

/** Wraps the page's own failure: what did not load, that nothing changed, and Retry. */
function LoadFailed({ onRetry }: { onRetry: () => void }) {
  const t = useT();
  return <ErrCard onRetry={onRetry} title={t("igp.errT")} body={t("igp.errS")} retry={t("igp.retry")} />;
}

/**
 * Telegram alerts as the board draws them (PnIntegrations): the chain of panel, bot and your chat with its verdict, the rows of the bot and
 * the chat, who linked a chat and what comes to this admin. The owner sets the bot up (the token goes in once and never comes back); every
 * admin links their own chat with a one-time code. A non-owner sees only their own part, and only once the owner has set a bot.
 * `extra` is the card under "what comes to you" (the way to MCP and the tokens, which live on the AI screen).
 */
export function TelegramCard({ owner, extra, initialCode }: { owner: boolean; extra?: ReactNode; initialCode?: LinkCode }) {
  const t = useT();
  const scene = useScene();
  const [code, setCode] = useState<LinkCode | null>(initialCode ?? null);
  const q = useQuery({ ...telegramQuery, refetchInterval: code ? linkPollMs : telegramPollMs });
  const me = useQuery(meQuery).data?.admin;
  const { test } = useTelegramActions();
  const toast = useToast();
  const [replacing, setReplacing] = useState(scene.has("rep"));
  const [tFail, setTFail] = useState(scene.has("tfail"));
  const [tDone, setTDone] = useState(scene.has("tdone"));
  const [flash, setFlash] = useState<"bot" | "chat" | "">(scene.has("fresh-bot") ? "bot" : scene.has("fresh-chat") ? "chat" : "");
  const doneTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const flashTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const meRow = useRef<HTMLDivElement>(null);
  const status = q.data;
  const hasBot = !!status?.bot?.configured;
  const linked = !!status?.mine;
  const linking = !linked && !!code;
  const alerts = !!status?.mine?.enabled;

  // the code is spent the moment the chat is linked: the card says so once, with a wave if the effects are on
  const wasLinking = useRef(false);
  useEffect(() => {
    if (linked && (wasLinking.current || code)) {
      setCode(null);
      toast(t("igp.t.linked"));
      setFlash("chat");
      clearTimeout(flashTimer.current);
      flashTimer.current = setTimeout(() => setFlash(""), 1600);
    }
    wasLinking.current = !linked && !!code;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- the moment the chat gets linked is what counts
  }, [linked]);
  // a bot that has just been saved (or its token replaced) flashes its row, once
  const botSaved = () => {
    setFlash("bot");
    clearTimeout(flashTimer.current);
    flashTimer.current = setTimeout(() => setFlash(""), 1600);
  };
  useEffect(
    () => () => {
      clearTimeout(doneTimer.current);
      clearTimeout(flashTimer.current);
    },
    [],
  );

  function sendTest() {
    if (test.isPending) return;
    test.mutate(undefined, {
      onSuccess: () => {
        setTFail(false);
        setTDone(true);
        clearTimeout(doneTimer.current);
        doneTimer.current = setTimeout(() => setTDone(false), 2200);
      },
      onError: (e) => {
        // a refusal for asking too soon says nothing about the chat
        if (ConnectError.from(e).rawMessage !== "telegram_test_too_soon") setTFail(true);
      },
    });
  }

  if (q.isPending && !status) return <Skeleton />;
  if (q.isError && !status) return <LoadFailed onRetry={() => void q.refetch()} />;
  if (!status?.bot) return null;

  const failed = tFail && linked;
  const w1: Tone = hasBot ? "on" : "off";
  const chat: Tone = !linked ? (linking ? "wait" : "off") : failed ? "bad" : alerts ? "on" : "mute";
  const head: Tone = !hasBot ? "off" : chat;
  const headKey = !hasBot ? "nobot" : linking ? "wait" : !linked ? "unlinked" : failed ? "fail" : alerts ? "on" : "mute";
  const botName = status.bot.username;
  const live = hasBot && linked && alerts;

  return (
    <div className="sb-tab">
      <div className="pf-g pp-grid sb-grid ig-grid">
        <div className="col">
          <section className="card ig-hero">
            <Chain
              title={t(`igp.h.${headKey}`)}
              bot={hasBot ? `@${botName}` : t("igp.n.botNone")}
              chat={linked ? `ID ${String(status.mine!.chatId)}` : linking ? t("igp.n.chatWait") : t("igp.n.chatNone")}
              hasBot={hasBot}
              w1={w1}
              w2={chat}
              n2={hasBot ? "on" : "off"}
              n3={chat}
            />
            <div className={cx("ig-head", head === "bad" && "bad")}>
              <b>{t(`igp.h.${headKey}`)}</b>
              <span>{headKey === "wait" ? t("igp.s.wait", { bot: botName }) : t(`igp.s.${headKey}`)}</span>
            </div>
            <div className="pp-rows" ref={meRow}>
              <BotRow status={status} owner={owner} flash={flash === "bot"} replacing={replacing} setReplacing={setReplacing} onSaved={botSaved} />
              <ChatRows status={status} code={code} setCode={setCode} flash={flash === "chat"} onTest={sendTest} tDone={tDone} linking={linking} />
              {hasBot && !status.adminUrlKnown && <p className="ig-mut ig-note">{t("tg.noUrl")}</p>}
            </div>
          </section>
          <Admins status={status} owner={owner} myId={me?.id ?? ""} />
        </div>
        <aside className="col pp-as">
          <Gets owner={owner} role={me?.role === Role.HELPER ? "helper" : me?.role === Role.READONLY ? "readonly" : "owner"} live={live} />
          {extra}
        </aside>
      </div>
    </div>
  );
}
