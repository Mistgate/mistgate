import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { StepUpProvider } from "@/components/step-up";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { telegram } from "@/lib/api";
import { setEffectsEnabled } from "@/lib/effects";
import { awaitingQuery } from "@/lib/integrations";
import { userCountQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { telegramQuery } from "@/lib/telegram";
import { setTheme, useTheme } from "@/lib/theme";
import { Shot } from "@/screens/dev-kit-redesign";
import { SceneProvider } from "@/screens/ai/bits";
import { IntegrationsView, type IntegrationsScene } from "@/screens/integrations";
import { shellSeed } from "./dev-kit-ai-page";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=pn-integrations: the Integrations screen of the redesign (PnIntegrations) in every state of the board, each in a frame of its own
// width (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the side-by-side with the board:
//   /dev-kit?only=pn-integrations-page&state=<what>[.<what>…][&lang=en|ru|zh|fa|tr][&theme=light|dark][&role=owner|helper|readonly][&fleet=0|1|4|12][&issues=0|1|4|9][&fx=1]
// what: the board's switches — "Данные": load | err; "Бот": nobot; "Чат": unlinked | wait | mute | tfail (the test did not arrive); and what is open — rep (the token being
//   replaced), filled (a token typed), ferr (the form says it is not a token), fbusy (the bot is being checked), dlg-bot | dlg-un (the windows), tdone | tbusy (the test button),
//   fresh-bot | fresh-chat (the row that has just changed flashes), warn (Telegram refuses the bot's token), nourl (the panel does not know its address), alone (no other admin
//   linked).
// Made-up data of the board: the bot example_mistgate_bot, the chat 100200300, the admins owner and eve.

const NOW = Math.floor(Date.now() / 1000);
const D = 86_400;
const mods = (state: string) => new Set(state.split(".").filter(Boolean));

/** The status the panel answers with: `raw` keeps the 64-bit numbers as the wire has them (bigint), otherwise they are what the query cache holds (plain numbers). */
export function statusData(m: ReadonlySet<string>, role: Role, raw = false) {
  const n = (v: number) => (raw ? BigInt(v) : v);
  const nobot = m.has("nobot");
  const mineOn = !nobot && !m.has("unlinked") && !m.has("wait");
  const roleName = role === Role.HELPER ? "helper" : role === Role.READONLY ? "readonly" : "owner";
  const mine = { adminId: "adm_1", adminName: "owner", role: roleName, enabled: !m.has("mute"), linkedUnix: n(NOW - 14 * D), chatId: n(100200300) };
  const eve = { adminId: "adm_2", adminName: "eve", role: "helper", enabled: true, linkedUnix: n(NOW - 3 * D), chatId: n(100200301) };
  return {
    bot: { configured: !nobot, username: nobot ? "" : "example_mistgate_bot", error: m.has("warn") ? "unauthorized" : "" },
    mine: mineOn ? mine : undefined,
    links: mineOn ? (m.has("alone") ? [mine] : [mine, eve]) : m.has("alone") || nobot ? [] : [eve],
    adminUrlKnown: !m.has("nourl"),
  };
}

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
const never = () => new Promise<never>(() => {});

/** The panel's calls of the scene: what a page asks for is answered from the scene's data, what a button asks for is answered after a breath and changes nothing. */
export function fakeCalls(m: ReadonlySet<string>, role: Role) {
  const status = statusData(m, role, true);
  Object.assign(telegram as object, {
    getTelegram: () => (m.has("load") ? never() : m.has("err") ? Promise.reject(new Error("panel did not answer")) : Promise.resolve({ status })),
    setTelegramBot: async () => (await sleep(1300), { status }),
    beginTelegramLink: async () => (await sleep(600), { code: "k3n9p2xq", deepLink: "https://t.me/example_mistgate_bot?start=k3n9p2xq", expiresUnix: BigInt(NOW + 540) }),
    unlinkTelegram: async () => (await sleep(500), { status }),
    setTelegramAlerts: async () => (await sleep(400), { status }),
    sendTelegramTest: async () => (await sleep(1200), {}),
  });
}

/** The private query cache of a scene (exported for its smoke test). */
export function seeded(state: string, role: Role, fleet = 4, issues = 1) {
  const m = mods(state);
  fakeCalls(m, role);
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: NOW + 3600 } as never);
  shellSeed(c, fleet, issues);
  c.setQueryData(userCountQuery.queryKey, { counts: { all: 140, online: 31, devicesUsed: 0, devicesOnline: 0 } } as never);
  c.setQueryData(awaitingQuery.queryKey, { awaiting: 0 } as never);
  if (!m.has("load") && !m.has("err")) c.setQueryData(telegramQuery.queryKey, statusData(m, role) as never);
  return c;
}

const roleOf = (r: string | null) => (r === "helper" ? Role.HELPER : r === "readonly" ? Role.READONLY : Role.OWNER);
const num = (v: string | null, otherwise: number) => (v !== null && !Number.isNaN(Number(v)) ? Number(v) : otherwise);

/** What the page opens with: the link code of a chat that waits for Start. */
export const sceneOf = (state: string): { scene: IntegrationsScene; flags: ReadonlySet<string> } => {
  const m = mods(state);
  return { flags: m, scene: m.has("wait") ? { code: { code: "k3n9p2xq", deepLink: "https://t.me/example_mistgate_bot?start=k3n9p2xq", expiresUnix: NOW + 540 } } : {} };
};

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function IntegrationsScenePage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "";
  const [qc] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    if (params.get("fx") === "1") setEffectsEnabled(true);
    return seeded(state, roleOf(params.get("role")), num(params.get("fleet"), 4), num(params.get("issues"), 1));
  });
  const { scene, flags } = sceneOf(state);
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>
        <StepUpProvider>
          <AddNodeProvider>
            <ShellLayout current="integrations" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
              <SceneProvider value={flags}>
                <IntegrationsView scene={scene} />
              </SceneProvider>
            </ShellLayout>
          </AddNodeProvider>
        </StepUpProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

type Frame = { id: string; state: string; title: string; w: number; h: number; params?: string };
const f = (id: string, state: string, title: string, w = 1440, h = 1000, params = ""): Frame => ({ id: `ig-${id}`, state, title, w, h, params });
export const frames: Frame[] = [
  f("on", "", "Linked: the bot, the chat, alerts on (the board's default)", 1440, 1100),
  f("on-light", "", "The same on the light theme", 1440, 1100, "&theme=light"),
  f("on-1000", "", "A 1000 window", 1000, 1200),
  f("on-phone", "", "A phone, 400 wide", 400, 1900),
  f("on-fa", "", "Right to left (fa)", 1440, 1100, "&lang=fa"),
  f("on-ru", "", "Russian", 1440, 1100, "&lang=ru"),
  f("on-fx", "fresh-chat", "Effects on: the chat row that has just changed flashes", 1440, 1100, "&fx=1"),
  f("unlinked", "unlinked", "Chat: not linked (the bot is set)", 1440, 900),
  f("wait", "wait", "Chat: waits for Start (the code is the panel's own need, under the line)", 1440, 1200),
  f("wait-phone", "wait", "The same on a phone", 400, 1800),
  f("mute", "mute", "Chat: linked, alerts off", 1440, 1100),
  f("fail", "tfail", "Chat: the test did not arrive", 1440, 1100),
  f("fail-light", "tfail", "The same on the light theme", 1440, 1100, "&theme=light"),
  f("tbusy", "tbusy", "The test is being sent", 1440, 1100),
  f("tdone", "tdone", "The test was sent (the button says so for a moment)", 1440, 1100),
  f("nobot", "nobot", "Bot: none (the owner gets the form)", 1440, 900),
  f("nobot-filled", "nobot.filled", "A token typed", 1440, 900),
  f("nobot-err", "nobot.filled.ferr", "The form says it is not a token", 1440, 900),
  f("nobot-busy", "nobot.filled.fbusy", "The bot is being checked", 1440, 900),
  f("nobot-phone", "nobot", "The form on a phone", 400, 1500),
  f("rep", "rep", "Replace the token: the form with Cancel", 1440, 1100),
  f("warn", "warn", "Telegram refuses the bot's token (a line under the bot)", 1440, 1100),
  f("dlg-bot", "dlg-bot", "Remove the bot: the window", 1440, 900),
  f("dlg-un", "dlg-un", "Unlink the chat: the window", 1440, 900),
  f("dlg-phone", "dlg-un", "The window on a phone (a sheet)", 400, 900),
  f("fresh-bot", "fresh-bot", "The bot has just been saved: its row flashes", 1440, 1100),
  f("alone", "alone", "No other admin has linked a chat", 1440, 1100),
  f("nourl", "nourl", "The panel does not know its own address", 1440, 1100),
  f("load", "load", "Loading", 1440, 700),
  f("err", "err", "Did not load", 1440, 700),
  f("helper", "", "A helper: only their own chat, only the health alerts", 1440, 1000, "&role=helper"),
  f("readonly", "", "An observer: only the health alerts", 1440, 1000, "&role=readonly"),
  f("readonly-nobot", "nobot", "An observer, no bot: the owner sets it up", 1440, 800, "&role=readonly"),
  f("readonly-unlinked", "unlinked", "An observer, not linked", 1440, 800, "&role=readonly"),
];

export function PnIntegrationsKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Integrations</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((fr) => (
        <Shot key={fr.id} id={fr.id} title={fr.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={fr.title} src={`${path}?only=pn-integrations-page&state=${fr.state}${fr.params ?? ""}`} width={fr.w} height={fr.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
