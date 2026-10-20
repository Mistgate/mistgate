import { act, StrictMode, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { en } from "@/i18n/en";
import { setLang } from "@/i18n";
import { RecoverScreen } from "./recover";

// The page behind the link of `mistgate edge reset-login`, rendered as the app renders it (StrictMode doubles renders and
// effects): the fragment leaves the address bar at once, the token goes out only on a click, and the new details live
// only on screen.

const recoverLogin = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  recoverLogin: (...args: unknown[]) => recoverLogin(...args),
}));
vi.mock("@tanstack/react-router", () => ({ Link: ({ children, to }: { children: ReactNode; to: string }) => <a href={to}>{children}</a> }));

const token = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789-_abcde";
const details = {
  adminDisplayName: "Alice",
  login: "alice",
  password: "k7mp-4xq2-hh3n-wr8d-tz5c",
  totpUri: "otpauth://totp/Mistgate:alice?secret=JBSWY3DPEHPK3PXP&issuer=Mistgate",
  totpSecret: "JBSWY3DPEHPK3PXP",
  created: false,
  sessionsEnded: 2,
};
const refused = (code: string, message = "refused") => Object.assign(new Error(message), { code });

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const open = (fragment: string, path = "/secret/recover") => window.history.replaceState({ key: "k" }, "", `${path}#${fragment}`);
beforeEach(() => {
  setLang("en");
  open(`recoveryToken=${token}`);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  recoverLogin.mockReset();
  localStorage.clear();
  sessionStorage.clear();
  window.history.replaceState(null, "", "/");
});

async function renderScreen() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<StrictMode><RecoverScreen /></StrictMode>));
}

const text = () => document.body.textContent ?? "";
const loginField = () => document.querySelector<HTMLInputElement>("input[autocomplete=username]");
const resetButton = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === en["auth.recover.submit"]);
const alert = () => document.querySelector('[role="alert"]')?.textContent;
async function submit() {
  await act(async () => {
    resetButton()!.click();
    await Promise.resolve();
  });
}
async function type(input: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

describe("edge sign-in recovery", () => {
  it("clears the fragment on the first paint, sends the token once on the click, and shows the new details", async () => {
    // a login in the query string is never read: only the fragment carries one
    open(`recoveryToken=${token}`, "/secret/recover?login=bob");
    let answer!: (v: typeof details) => void;
    recoverLogin.mockReturnValue(new Promise((r) => (answer = r)));
    await renderScreen();

    expect(window.location.hash).toBe("");
    expect(window.location.pathname).toBe("/secret/recover");
    expect(window.history.state).toEqual({ key: "k" }); // the router's own entry state is kept
    expect(recoverLogin).not.toHaveBeenCalled();
    expect(loginField()).toBeNull(); // one admin: nothing to ask

    // a double click while the answer is on its way, and re-renders, post nothing more
    await submit();
    await act(async () => void resetButton()?.click());
    await act(async () => setLang("ru"));
    await act(async () => setLang("en"));
    expect(recoverLogin).toHaveBeenCalledTimes(1);
    expect(recoverLogin).toHaveBeenCalledWith(token, "");

    await act(async () => answer(details));
    expect([...document.querySelectorAll("code")].map((c) => c.textContent)).toEqual(expect.arrayContaining([details.login, details.password]));
    expect(text()).toContain("Alice");
    expect(text()).toContain("JBSW Y3DP EHPK 3PXP");
    expect(document.querySelector('[role="img"][aria-label]')).not.toBeNull(); // the QR code
    expect(text()).toContain(en["auth.recover.onceTitle"]);
    expect(text()).toContain(en["auth.recover.nextPasskey"]);
    expect(text()).toContain("2 sessions ended");
    expect(document.activeElement?.tagName).toBe("H1");
    expect(document.activeElement?.textContent).toBe(en["auth.recover.doneTitle"]);
    expect([...document.querySelectorAll("button")].some((b) => b.textContent?.includes(en["common.copy"]))).toBe(true);
    expect(recoverLogin).toHaveBeenCalledTimes(1);
  });

  it("takes the login from the fragment and sends it in the request", async () => {
    open(`recoveryToken=${token}&login=alice`);
    recoverLogin.mockResolvedValue(details);
    await renderScreen();
    expect(window.location.hash).toBe("");
    expect(loginField()!.value).toBe("alice");
    await submit();
    expect(recoverLogin).toHaveBeenCalledWith(token, "alice");
    expect(text()).toContain(details.password);
  });

  it("a 400 asks for the login in the page's own words, and retries with the same token", async () => {
    const serverText = "no admin signs in as \"bob\"; to give one a password login, add --admin <id>";
    recoverLogin.mockRejectedValueOnce(refused("invalid_argument", serverText));
    recoverLogin.mockResolvedValueOnce(details);
    await renderScreen();
    await submit();

    expect(alert()).toBe(`${en["auth.recover.refused"]} ${en["auth.recover.whichAdmin"]}`);
    expect(text()).not.toContain(serverText); // English, and it speaks CLI flags
    expect(loginField()!.value).toBe("");
    await type(loginField()!, "alice");
    await submit();
    expect(recoverLogin).toHaveBeenNthCalledWith(2, token, "alice");
    expect(text()).toContain(details.password);
  });

  it("keeps no detail and no token in storage, history or the address, also while a retry waits", async () => {
    recoverLogin.mockRejectedValueOnce(refused("invalid_argument", "enter the admin login"));
    recoverLogin.mockResolvedValueOnce(details);
    await renderScreen();
    const kept = () => {
      const stored = [localStorage, sessionStorage].flatMap((s) => Object.keys(s).map((k) => `${k}=${s.getItem(k)}`)).join("\n");
      return `${stored}\n${JSON.stringify(window.history.state)}\n${window.location.href}`;
    };
    await submit();
    expect(kept()).not.toContain(token);
    await type(loginField()!, "alice");
    await submit();
    expect(text()).toContain(details.password);
    for (const secret of [token, details.password, details.totpSecret, details.totpUri]) expect(kept()).not.toContain(secret);
  });

  it("a wrong, expired or used link (403) gets the generic message and the command, and no second try", async () => {
    recoverLogin.mockRejectedValue(refused("permission_denied", "recovery link is invalid or expired"));
    await renderScreen();
    await submit();
    expect(alert()).toBe(en["auth.recover.linkTitle"] + en["auth.recover.linkBody"]);
    expect(document.activeElement?.textContent).toBe(en["auth.recover.linkTitle"]);
    expect(text()).toContain("mistgate edge reset-login --file KEYFILE");
    expect(resetButton()).toBeUndefined();
    expect(recoverLogin).toHaveBeenCalledTimes(1);
  });

  it("a reload finds no token: the same generic message, nothing posted", async () => {
    window.history.replaceState(null, "", "/secret/recover");
    await renderScreen();
    expect(recoverLogin).not.toHaveBeenCalled();
    expect(alert()).toBe(en["auth.recover.linkTitle"] + en["auth.recover.linkBody"]);
    expect(resetButton()).toBeUndefined();
  });

  it.each([
    ["resource_exhausted", "err.rateLimited"],
    ["internal", "err.generic"],
    ["unknown", "err.generic"], // an HTTP error without a JSON code
    [undefined, "err.network"], // fetch threw: no answer at all
  ] as const)("%s keeps the form and the token for another try", async (code, key) => {
    recoverLogin.mockRejectedValueOnce(code ? refused(code) : new TypeError("Failed to fetch"));
    recoverLogin.mockResolvedValueOnce(details);
    await renderScreen();
    await submit();
    expect(alert()).toBe(`${en["auth.recover.refused"]} ${en[key]}`);
    expect(loginField()).toBeNull();
    await submit();
    expect(recoverLogin).toHaveBeenNthCalledWith(2, token, "");
    expect(text()).toContain(details.password);
  });

  it("refuses to post a login the server would reject", async () => {
    open(`recoveryToken=${token}&login=alice`);
    await renderScreen();
    await type(loginField()!, "no spaces allowed");
    expect(resetButton()!.disabled).toBe(true);
    expect(text()).toContain(en["auth.err.login"]);
    expect(recoverLogin).not.toHaveBeenCalled();
  });
});
