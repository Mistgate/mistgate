import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { TurnstileRow } from "@/components/turnstile";
import { en } from "@/i18n/en";
import { LoginPage, type LoginPageProps } from "./login";

// The sign-in page as the board draws it (PnLogin), in each of its states: the passkey button idle, waiting, refused; the
// password form; the sign-in that went through. The page is a view: what it shows follows its props.

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
});

const base: LoginPageProps = {
  view: "key",
  host: "panel.example.com",
  supported: true,
  passwordLogin: true,
  captcha: <TurnstileRow status="ok" label={en["win.cf.passed"]} />,
  captchaOn: true,
  captchaReady: true,
  busy: false,
  login: "",
  password: "",
  code: "",
  onLogin: () => {},
  onPassword: () => {},
  onCode: () => {},
  onPasskey: () => {},
  onSubmit: (e) => e.preventDefault(),
  onToggle: () => {},
};
async function show(over: Partial<LoginPageProps> = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<LoginPage {...base} {...over} />));
}
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const text = () => document.body.textContent ?? "";

describe("the sign-in page", () => {
  it("is the title with the address in mono under it, the check, the passkey button and its line, another way, the decoy line", async () => {
    await show();
    expect(document.querySelector("h1")?.textContent).toBe(en["auth.login.title"]);
    const address = document.querySelector("h1 + p")!;
    expect(address.textContent).toBe("admin · panel.example.com");
    expect(address.className).toContain("font-mono");
    expect(text()).toContain(en["win.cf.passed"]);
    expect(button(en["auth.login.passkey"])?.disabled).toBe(false);
    expect(text()).toContain(en["auth.login.passkeyHint"]);
    expect(button(en["auth.login.otherWay"])).toBeDefined();
    expect(text()).toContain(en["win.login.decoy"]);
  });

  it("waits for the passkey: a ring in the button, the browser to confirm in the line, nothing to press", async () => {
    await show({ busy: true });
    const b = button(en["win.login.waiting"])!;
    expect(b.className).toContain("pointer-events-none");
    expect(b.querySelector("svg")).toBeNull();
    expect(b.disabled).toBe(false); // not dimmed
    expect(text()).toContain(en["win.login.confirmInBrowser"]);
  });

  it("says what went wrong in the line, in the failure colour, and offers to try again", async () => {
    await show({ error: en["err.cancelled"] });
    expect(button(en["win.retry"])).toBeDefined();
    const line = [...document.querySelectorAll("p")].find((p) => p.textContent === en["err.cancelled"])!;
    expect(line.className).toContain("text-danger-text");
  });

  it("is the password, the six boxes and a button on the other way, and it says how to get back", async () => {
    await show({ view: "password" });
    expect(document.querySelector('input[type="password"]')).not.toBeNull();
    expect(document.querySelectorAll(".wn-otp input")).toHaveLength(6);
    expect(button(en["auth.login.passkey"])).toBeUndefined();
    expect(button(en["win.login.byPasskey"])).toBeDefined();
    expect(text()).toContain(en["win.login.codeHint"]);
    expect(button(en["auth.login.submit"])?.disabled).toBe(true); // login, password and the code are all needed
  });

  it("opens the password form the browser without passkeys needs, with the notice that says why", async () => {
    await show({ view: "password", supported: false });
    expect(text()).toContain(en["auth.noPasskeyTitle"]);
    expect(button(en["win.login.byPasskey"])).toBeUndefined(); // there is no other way to go back to
  });

  it("says the panel did not answer for the login info, with a way to ask again", async () => {
    const again = vi.fn();
    await show({ onRetryInfo: again });
    expect(text()).toContain(en["err.network"]);
    await act(async () => void button(en["common.retry"])!.click());
    expect(again).toHaveBeenCalledTimes(1);
  });

  it("when the sign-in went through the button is a confirmation and a bar fills while the panel loads", async () => {
    await show({ view: "done" });
    expect(button(en["auth.login.passkey"])).toBeUndefined();
    const done = [...document.querySelectorAll("[role=status]")].find((r) => r.textContent?.includes(en["win.login.done"]))!;
    expect(done.className).toContain("wn-confirm");
    expect(done.querySelector(".wn-ts")?.className).toContain("done");
    expect(document.querySelector(".wn-fill")).not.toBeNull();
    expect(text()).toContain(en["win.login.opening"]);
    expect(text()).toContain(en["win.cf.passed"]);
  });

  it("without a check on the panel shows no check row", async () => {
    await show({ captcha: null, captchaOn: false });
    expect(text()).not.toContain("Cloudflare Turnstile");
    await act(async () => root!.render(<LoginPage {...base} captcha={null} captchaOn={false} view="done" />));
    expect(text()).not.toContain("Cloudflare Turnstile");
  });
});
