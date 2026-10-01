import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { en } from "@/i18n/en";
import { LoginScreen } from "./login";

// The sign-in page when GetLoginInfo does not answer: it says so, offers another try, and keeps the password form
// reachable while nobody knows whether some admin signs in with one.

const getLoginInfo = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  auth: { getLoginInfo: (...a: unknown[]) => getLoginInfo(...a) },
  webauthnSupported: () => true,
}));
vi.mock("@tanstack/react-router", () => ({ useRouter: () => ({ navigate: vi.fn() }) }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  getLoginInfo.mockReset();
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const text = () => document.body.textContent ?? "";

describe("sign-in without the login info", () => {
  it("tries twice more, then says the panel is out of reach with a Retry, and keeps “another way”", async () => {
    getLoginInfo.mockRejectedValue(new ConnectError("down", Code.Unavailable));
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const qc = new QueryClient({ defaultOptions: { queries: { retryDelay: 0 } } });
    await act(async () => root!.render(<QueryClientProvider client={qc}><LoginScreen /></QueryClientProvider>));
    for (let i = 0; i < 8; i++) await settle();
    expect(getLoginInfo).toHaveBeenCalledTimes(3);
    expect(text()).toContain(en["err.network"]);
    expect(button(en["auth.login.otherWay"])).toBeDefined();

    getLoginInfo.mockResolvedValue({ brandHead: "", brandTail: "", accent: "", language: "en", hasLogo: false, setupOpen: false, passwordLogin: false, turnstileEnabled: false, turnstileSiteKey: "" });
    await act(async () => void button(en["common.retry"])!.click());
    for (let i = 0; i < 4; i++) await settle();
    expect(text()).not.toContain(en["err.network"]);
    // the panel answered: nobody signs in with a password, so there is no other way to offer
    expect(button(en["auth.login.otherWay"])).toBeUndefined();
  });
});
