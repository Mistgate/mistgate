import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { GetLoginInfoResponseSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { en } from "@/i18n/en";
import { loginInfoQuery } from "@/lib/instance";
import { LoginScreen } from "./login";
import { SetupScreen } from "./setup";

// The sign-in and create-admin screens with Turnstile: what gates the buttons, which calls carry the token,
// and that a token is never used twice. Cloudflare's script is replaced by a fake that we drive by hand.

const api = vi.hoisted(() => ({
  beginLogin: vi.fn(),
  finishLogin: vi.fn(),
  passwordLogin: vi.fn(),
  beginSetup: vi.fn(),
  finishSetup: vi.fn(),
}));
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  auth: api,
  webauthnSupported: () => true,
}));
vi.mock("@/lib/passkey", () => ({ getPasskey: async () => "{}", createPasskey: async () => "{}" }));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useRouter: () => ({ navigate }),
  Link: ({ children }: { children: React.ReactNode }) => <a>{children}</a>,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

type Options = Record<string, (...a: unknown[]) => unknown>;
let widgets: { options: Options; id: string }[] = [];
const cloudflareScripts = () => document.querySelectorAll("script[src*='challenges.cloudflare.com']");
const reset = vi.fn();

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  widgets = [];
  window.turnstile = {
    render: (_el, options) => {
      const id = `w${widgets.length + 1}`;
      widgets.push({ options: options as Options, id });
      return id;
    },
    reset: (id) => reset(id),
    remove: () => {},
  };
  window.location.hash = "";
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  delete window.turnstile;
  Object.values(api).forEach((m) => m.mockReset());
  reset.mockReset();
  navigate.mockReset();
  vi.restoreAllMocks();
});

const info = (turnstile: boolean) =>
  create(GetLoginInfoResponseSchema, {
  brandHead: "",
  brandTail: "",
  accent: "",
  language: "en",
  hasLogo: false,
  setupOpen: false,
  passwordLogin: true,
  turnstileEnabled: turnstile,
  turnstileSiteKey: turnstile ? "1x00000000000000000000AA" : "",
  });

async function mount(ui: React.ReactElement, turnstile: boolean) {
  const qc = new QueryClient();
  qc.setQueryData(loginInfoQuery.queryKey, info(turnstile));
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>));
}

const button = (text: string) => [...document.querySelectorAll("button")].find((b) => b.textContent === text) as HTMLButtonElement;
const solve = (token: string) => act(() => void widgets.at(-1)!.options.callback!(token));
const click = (b: HTMLButtonElement) => act(async () => b.click());
async function typeInto(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const captchaDenied = (why: string) => new ConnectError(why, Code.PermissionDenied);

describe("sign-in", () => {
  it("with Turnstile off nothing from Cloudflare is touched and the buttons are live", async () => {
    delete window.turnstile;
    api.beginLogin.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    await mount(<LoginScreen />, false);
    expect(cloudflareScripts()).toHaveLength(0);
    expect(button(en["auth.login.passkey"]).disabled).toBe(false);
    await click(button(en["auth.login.passkey"]));
    expect(api.beginLogin).toHaveBeenCalledWith({ turnstileToken: "" });
  });

  it("keeps the passkey button off until the check passes, then sends the token with BeginLogin and spends it", async () => {
    api.beginLogin.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    api.finishLogin.mockResolvedValue({});
    await mount(<LoginScreen />, true);
    expect(widgets).toHaveLength(1);
    expect(widgets[0]!.options.sitekey).toBe("1x00000000000000000000AA");
    expect(button(en["auth.login.passkey"]).disabled).toBe(true);

    solve("tok-1");
    expect(button(en["auth.login.passkey"]).disabled).toBe(false);
    await click(button(en["auth.login.passkey"]));

    expect(api.beginLogin).toHaveBeenCalledWith({ turnstileToken: "tok-1" });
    // Finish* rides on the ceremony id, not on a second token
    expect(api.finishLogin).toHaveBeenCalledWith({ ceremonyId: "c1", credentialJson: "{}" });
    expect(reset).toHaveBeenCalledWith("w1"); // a token works once
  });

  it("starts over after a refusal: the message, a reset widget, the button off again", async () => {
    api.beginLogin.mockRejectedValue(captchaDenied("captcha failed"));
    await mount(<LoginScreen />, true);
    solve("stale");
    await click(button(en["auth.login.passkey"]));

    expect(document.body.textContent).toContain(en["err.captcha"]);
    expect(reset).toHaveBeenCalledWith("w1");
    expect(button(en["auth.login.passkey"]).disabled).toBe(true); // the old token is gone
    solve("fresh");
    expect(button(en["auth.login.passkey"]).disabled).toBe(false);
  });

  it("gates the password form too, sends the token with PasswordLogin and resets after a wrong code", async () => {
    api.passwordLogin.mockRejectedValue(
      new ConnectError("bad", Code.Unauthenticated), // a wrong code: not a captcha problem
    );
    await mount(<LoginScreen />, true);
    await click(button(en["auth.login.otherWay"]));
    await typeInto(document.querySelector<HTMLInputElement>("input[autocomplete=username]")!, "admin");
    await typeInto(document.querySelector<HTMLInputElement>("input[autocomplete=current-password]")!, "hunter2hunter2");
    await typeInto(document.querySelector<HTMLInputElement>("input[inputmode=numeric]")!, "123456");
    const submit = button(en["auth.login.submit"]);
    expect(submit.disabled).toBe(true); // everything filled in, the check has not passed

    solve("tok-pw");
    expect(submit.disabled).toBe(false);
    await click(submit);
    expect(api.passwordLogin).toHaveBeenCalledWith({ login: "admin", password: "hunter2hunter2", totpCode: "123456", turnstileToken: "tok-pw" });
    expect(reset).toHaveBeenCalledWith("w1");
    expect(button(en["auth.login.submit"]).disabled).toBe(true);
  });

  it("reloads when the panel wants a check this page was loaded without", async () => {
    const reload = vi.fn();
    vi.stubGlobal("location", { ...window.location, reload, host: "localhost" });
    api.beginLogin.mockRejectedValue(captchaDenied("captcha required"));
    await mount(<LoginScreen />, false);
    await click(button(en["auth.login.passkey"]));
    expect(reload).toHaveBeenCalledTimes(1);
    expect(document.body.textContent).not.toContain(en["err.captcha"]);
    vi.unstubAllGlobals();
  });
});

describe("create the admin", () => {
  async function toAdminStep(turnstile: boolean) {
    window.location.hash = "#setup-token";
    await mount(<SetupScreen />, turnstile);
    await click(button(en["auth.next"])); // language step
  }

  it("gates passkey creation on the check and sends the token with BeginSetup", async () => {
    api.beginSetup.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    api.finishSetup.mockResolvedValue({});
    await toAdminStep(true);
    expect(button(en["auth.createPasskey"]).disabled).toBe(true);

    solve("tok-s");
    await click(button(en["auth.createPasskey"]));
    expect(api.beginSetup).toHaveBeenCalledWith({ setupToken: "setup-token", displayName: "admin", turnstileToken: "tok-s" });
    expect(api.finishSetup).toHaveBeenCalledWith({ setupToken: "setup-token", ceremonyId: "c1", credentialJson: "{}" });
    expect(reset).toHaveBeenCalledWith("w1");
  });

  it("the password path sends the token with the first step and needs none for the code", async () => {
    api.beginSetup.mockResolvedValue({ ceremonyId: "c2", totpUri: "otpauth://totp/x?secret=ABCDEFGH", totpSecret: "ABCDEFGH" });
    api.finishSetup.mockResolvedValue({});
    await toAdminStep(true);
    await click(button(en["auth.setup.otherWay"]));
    await typeInto(document.querySelector<HTMLInputElement>("input[autocomplete=new-password]")!, "correct horse battery");
    expect(button(en["auth.next"]).disabled).toBe(true);

    solve("tok-p");
    await click(button(en["auth.next"]));
    expect(api.beginSetup).toHaveBeenCalledWith(expect.objectContaining({ turnstileToken: "tok-p", login: "admin" }));

    // on the code step the check is behind us: no widget, no second token
    expect(document.querySelector("input[inputmode=numeric]")).not.toBeNull();
    await typeInto(document.querySelector<HTMLInputElement>("input[inputmode=numeric]")!, "123456");
    await click(button(en["auth.setup.createAdmin"]));
    expect(api.finishSetup).toHaveBeenCalledWith({ setupToken: "setup-token", ceremonyId: "c2", totpCode: "123456" });
  });

  it("with Turnstile off there is no widget and the call carries an empty token", async () => {
    delete window.turnstile;
    api.beginSetup.mockResolvedValue({ optionsJson: "{}", ceremonyId: "c1" });
    api.finishSetup.mockResolvedValue({});
    await toAdminStep(false);
    expect(cloudflareScripts()).toHaveLength(0);
    await click(button(en["auth.createPasskey"]));
    expect(api.beginSetup).toHaveBeenCalledWith(expect.objectContaining({ turnstileToken: "" }));
  });
});
