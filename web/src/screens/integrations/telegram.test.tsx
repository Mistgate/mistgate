import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { TelegramCard } from "./telegram";

const getTelegram = vi.fn();
const setTelegramBot = vi.fn();
const beginTelegramLink = vi.fn();
const unlinkTelegram = vi.fn();
const setTelegramAlerts = vi.fn();
const sendTelegramTest = vi.fn();
vi.mock("@/lib/api", () => ({
  telegram: {
    getTelegram: (...a: unknown[]) => getTelegram(...a),
    setTelegramBot: (...a: unknown[]) => setTelegramBot(...a),
    beginTelegramLink: (...a: unknown[]) => beginTelegramLink(...a),
    unlinkTelegram: (...a: unknown[]) => unlinkTelegram(...a),
    setTelegramAlerts: (...a: unknown[]) => setTelegramAlerts(...a),
    sendTelegramTest: (...a: unknown[]) => sendTelegramTest(...a),
  },
  apiTokens: {},
  approvals: {},
  updates: {},
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [getTelegram, setTelegramBot, beginTelegramLink, unlinkTelegram, setTelegramAlerts, sendTelegramTest]) m.mockReset();
});

const NOW = Math.floor(Date.now() / 1000);
const link = (over: Record<string, unknown> = {}) => ({ adminId: "adm_1", adminName: "Owner", role: "owner", enabled: true, linkedUnix: NOW - 86400, chatId: 424242, ...over });
const status = (over: { bot?: Record<string, unknown>; mine?: unknown; links?: unknown[]; adminUrlKnown?: boolean } = {}) => ({
  bot: { configured: false, username: "", error: "", ...over.bot },
  mine: over.mine,
  links: over.links ?? [],
  adminUrlKnown: over.adminUrlKnown ?? true,
});
const withBot = (over: Parameters<typeof status>[0] = {}) => status({ ...over, bot: { configured: true, username: "mist_alert_bot", ...over.bot } });

async function mount(owner: boolean, first: unknown) {
  getTelegram.mockResolvedValue({ status: first });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <TelegramCard owner={owner} />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const buttons = () => [...document.querySelectorAll("button")];
const button = (label: string) => buttons().find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
function type(input: Element | null, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  return act(async () => {
    set.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const TOKEN = "123456789:AAFakeFakeFakeFakeFakeFakeFakeFake_x";

describe("the Telegram card: the bot (owner)", () => {
  it("asks for the token when no bot is set, checks its shape, and sends it once", async () => {
    setTelegramBot.mockResolvedValue({ status: withBot() });
    await mount(true, status());
    expect(text()).toContain("Make a bot with @BotFather");
    expect(document.querySelector("[data-testid=tg-bot]")).toBeNull();
    expect(text()).not.toContain("Your Telegram"); // nothing to link before there is a bot

    await click(button("Save bot"));
    expect(text()).toContain("does not look like a bot token");
    await type(document.querySelector("input[type=password]"), "not a token");
    await click(button("Save bot"));
    expect(setTelegramBot).not.toHaveBeenCalled();

    await type(document.querySelector("input[type=password]"), ` ${TOKEN} `);
    await click(button("Save bot"));
    await settle();
    expect(setTelegramBot).toHaveBeenCalledTimes(1);
    expect(setTelegramBot).toHaveBeenCalledWith({ token: TOKEN });
  });

  it("shows only “set” and the bot's name once it is saved, never the token", async () => {
    await mount(true, withBot());
    expect(document.querySelector("[data-testid=tg-bot]")?.textContent).toBe("@mist_alert_bot");
    expect(text()).toContain("Token is set");
    expect(document.querySelector("input[type=password]")).toBeNull();
    expect(document.body.innerHTML).not.toContain("AAFake");
    expect(text()).toContain("Cloudflare mirror Worker"); // the Worker may use the same bot
  });

  it("replaces the token on request, and cancel puts the row back", async () => {
    await mount(true, withBot());
    await click(button("Replace token"));
    expect(document.querySelector("input[type=password]")).not.toBeNull();
    expect(text()).toContain("a different bot unlinks all of them");
    await click(button("Cancel"));
    expect(document.querySelector("input[type=password]")).toBeNull();
    expect(document.querySelector("[data-testid=tg-bot]")).not.toBeNull();
  });

  it("asks before removing the bot, then clears it", async () => {
    setTelegramBot.mockResolvedValue({ status: status() });
    await mount(true, withBot());
    await click(button("Remove bot"));
    expect(dialog()!.textContent).toContain("every linked chat is unlinked");
    expect(setTelegramBot).not.toHaveBeenCalled();
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Remove"));
    await settle();
    expect(setTelegramBot).toHaveBeenCalledWith({ clear: true });
  });

  it("says in words when Telegram refuses the token or the bot is read elsewhere", async () => {
    await mount(true, withBot({ bot: { error: "unauthorized" } }));
    expect(text()).toContain("Telegram refused the token");
    act(() => root?.unmount());
    host?.remove();
    await mount(true, withBot({ bot: { error: "conflict" } }));
    expect(text()).toContain("Another program reads this bot");
  });

  it("keeps the form and says why when the panel cannot check the token", async () => {
    setTelegramBot.mockRejectedValue(new ConnectError("telegram_unauthorized", Code.FailedPrecondition));
    await mount(true, status());
    await type(document.querySelector("input[type=password]"), TOKEN);
    await click(button("Save bot"));
    await settle();
    expect(setTelegramBot).toHaveBeenCalledTimes(1);
    expect(document.querySelector("input[type=password]")).not.toBeNull();
    expect(button("Save bot")?.hasAttribute("disabled")).toBe(false);
  });

  it("lists who linked a chat, with their role and whether alerts are on", async () => {
    await mount(true, withBot({ mine: link(), links: [link(), link({ adminId: "adm_2", adminName: "Dana", role: "helper", enabled: false })] }));
    expect(text()).toContain("Linked admins");
    expect(text()).toContain("Dana");
    expect(text()).toContain("helper");
    expect(text()).toContain("alerts off");
    expect(text()).toContain("alerts on");
  });
});

describe("the Telegram card: your own chat (every admin)", () => {
  it("links a chat with a one-time code and a deep link, and notices when it is bound", async () => {
    beginTelegramLink.mockResolvedValue({ code: "abcd2345wxyz", deepLink: "https://t.me/mist_alert_bot?start=abcd2345wxyz", expiresUnix: NOW + 600 });
    await mount(false, withBot());
    expect(text()).toContain("No chat is linked yet");
    await click(button("Link my Telegram"));
    await settle();
    expect(beginTelegramLink).toHaveBeenCalledTimes(1);
    expect(document.querySelector<HTMLAnchorElement>("[data-testid=tg-deeplink]")?.getAttribute("href")).toBe("https://t.me/mist_alert_bot?start=abcd2345wxyz");
    expect(document.querySelector("[data-testid=tg-start]")?.textContent).toBe("/start abcd2345wxyz");
    expect(text()).toMatch(/expires in (10:00|9:5\d)/); // ten minutes, give or take the second the test started in
    expect(text()).toContain("Waiting for the bot to hear you");

    // the panel heard the bot: the next look at the status shows the chat
    getTelegram.mockResolvedValue({ status: withBot({ mine: link({ adminName: "Me" }) }) });
    await act(async () => void (await new Promise((r) => setTimeout(r, 3100))));
    expect(document.querySelector("[data-testid=tg-start]")).toBeNull();
    expect(text()).toContain("Linked");
  }, 10_000);

  it("offers a new code when the old one has run out, and lets the admin give up", async () => {
    beginTelegramLink.mockResolvedValue({ code: "oldcode00000", deepLink: "https://t.me/mist_alert_bot?start=oldcode00000", expiresUnix: NOW - 1 });
    await mount(false, withBot());
    await click(button("Link my Telegram"));
    await settle();
    expect(text()).toContain("The code has expired");
    expect(document.querySelector("[data-testid=tg-deeplink]")).toBeNull();
    await click(button("New code"));
    await settle();
    expect(beginTelegramLink).toHaveBeenCalledTimes(2);
    await click(button("Cancel"));
    expect(button("Link my Telegram")).toBeDefined();
  });

  it("switches alerts, sends a test and unlinks", async () => {
    setTelegramAlerts.mockResolvedValue({});
    sendTelegramTest.mockResolvedValue({});
    unlinkTelegram.mockResolvedValue({});
    await mount(false, withBot({ mine: link({ adminName: "Me", role: "helper" }) }));
    expect(text()).toContain("Linked");
    expect(text()).toContain("You get the health alerts"); // a helper's share
    expect(document.querySelector("[data-testid=tg-chat-id]")?.textContent).toBe("424242"); // for the Worker
    expect(text()).not.toContain("Linked admins");
    expect(document.querySelector("input[type=password]")).toBeNull(); // only the owner sets the bot

    await click(document.querySelector("[role=switch]"));
    expect(setTelegramAlerts).toHaveBeenCalledWith({ enabled: false });
    await click(button("Send test"));
    expect(sendTelegramTest).toHaveBeenCalledTimes(1);
    await click(button("Unlink"));
    expect(unlinkTelegram).toHaveBeenCalledTimes(1);
  });

  it("tells a non-owner to wait for the owner while no bot is set", async () => {
    await mount(false, status());
    expect(text()).toContain("The owner has not set the Telegram bot up yet");
    expect(button("Link my Telegram")).toBeUndefined();
    expect(document.querySelector("input[type=password]")).toBeNull();
  });

  it("warns that messages carry no links when the panel does not know its address", async () => {
    await mount(false, withBot({ adminUrlKnown: false }));
    expect(text()).toContain("does not know its public admin address");
    act(() => root?.unmount());
    host?.remove();
    await mount(false, withBot({ adminUrlKnown: true }));
    expect(text()).not.toContain("does not know its public admin address");
  });

  it("tells the owner what they get", async () => {
    await mount(true, withBot());
    expect(text()).toContain("As the owner you get everything");
  });
});
