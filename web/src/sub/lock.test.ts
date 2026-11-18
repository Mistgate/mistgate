import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, endpoint, unlock } from "./api";
import { cases } from "./dev-data";
import { foreignLetters, formatPassword, lockActions, lockView, type LockState } from "./lock";
import { normalize } from "./logic";

const data = () => normalize(structuredClone(cases.locked!));

function setup(over: Partial<Parameters<typeof lockActions>[0]> = {}) {
  const st: LockState = { lang: "ru", pw: "", busy: false, error: "", note: "" };
  const log: string[] = [];
  const root = document.createElement("div");
  document.body.append(root);
  const actions = lockActions({
    st,
    endpoint: "https://sub.example.com/p/t/unlock",
    unlock: async (_u, pw) => void log.push(`unlock ${pw}`),
    render: () => root.replaceChildren(lockView(data(), st, actions)),
    reload: () => log.push("reload"),
    save: (l) => log.push(`lang ${l}`),
    ...over,
  });
  root.replaceChildren(lockView(data(), st, actions));
  const field = () => root.querySelector<HTMLInputElement>("#pw")!;
  const go = () => root.querySelector<HTMLButtonElement>("[data-k=pw-go]")!;
  const type = (v: string) => {
    field().value = v;
    field().dispatchEvent(new Event("input", { bubbles: true }));
  };
  return { st, log, root, actions, field, go, type };
}
afterEach(() => document.body.replaceChildren());

describe("the password form", () => {
  it("the locked page data is the brand and the unlock address, nothing of the user", () => {
    const d = data();
    expect(d.locked).toBe(true);
    expect(d.unlock_url).toBe("dev:");
    expect(d.apps).toEqual([]);
    expect(d.subscription_url).toBe("");
    expect(normalize({ locked: "yes" }).locked).toBe(false); // only a real true locks
  });

  it("formats what is typed: lower case, letters and digits, a dash after the fourth", () => {
    expect(formatPassword("ABCD23")).toBe("abcd-23");
    expect(formatPassword("abcd-2345")).toBe("abcd-2345");
    expect(formatPassword(" ab cd 23 45 99")).toBe("abcd-2345");
    expect(formatPassword("ab")).toBe("ab");
    expect(formatPassword("")).toBe("");
  });

  it("a Russian keyboard: the look-alike letters are taken for Latin ones, upper case too", () => {
    expect(formatPassword("к3м9-х7рq")).toBe("k3m9-x7pq");
    expect(formatPassword("АЕКМ РСУХ")).toBe("aekm-pcyx");
    expect(foreignLetters("к3м9")).toBe(false);
    expect(foreignLetters("k3m9б")).toBe(true);
    expect(foreignLetters("abcd-2345")).toBe(false);
  });

  it("another letter is not dropped silently: the line under the field says to switch the keyboard", () => {
    const { type, field, root } = setup();
    type("к3м9б");
    expect(field().value).toBe("k3m9");
    const note = root.querySelector<HTMLElement>("#pw-note")!;
    expect([note.textContent, note.hidden, note.className]).toEqual(["Пароль набирается латиницей — переключите клавиатуру на English (кнопка 🌐).", false, "u-hint"]);
    type("k3m9x");
    expect([note.textContent, note.hidden]).toEqual(["", true]);
  });

  it("the button is never dead: with too few characters it says how many there are, and nothing is sent", async () => {
    const { go, type, field, actions, log, root } = setup();
    expect(go().disabled).toBe(false);
    type("ABCD2");
    await actions.submit();
    expect(root.querySelector("[role=alert]")?.textContent).toBe("В пароле 8 знаков, вы ввели 5.");
    expect(log).toEqual([]);
    type("abcd2345");
    expect(field().value).toBe("abcd-2345");
    expect(root.querySelector("[role=alert]")).toBeNull(); // typing again clears it
    expect(field().getAttribute("autocapitalize")).toBe("none");
    expect(field().getAttribute("autocomplete")).toBe("current-password");
  });

  it("is the board's window on the page: no head, the logo, who set the password, the field, Open; and what to do without a password", () => {
    const { root, field, go } = setup();
    const win = root.querySelector(".a-desk.a-plain > section.a-dlg.sm")!;
    expect(win.querySelector(".a-h")).toBeNull();
    expect(win.getAttribute("aria-labelledby")).toBe("pw-t");
    const form = win.querySelector("form.a-b")!;
    expect([...form.children].map((x) => x.className)).toEqual(["a-lock", "a-fld", "u-btn pri", "u-hint lock-none"]);
    expect(form.querySelector(".a-lock .mg-motion")).not.toBeNull();
    expect(form.querySelector(".a-lock h2")?.textContent).toBe("Страница закрыта паролем");
    expect(form.querySelector(".a-lock .a-lead")?.textContent).toBe("Его задал владелец подписки");
    expect(form.querySelector("label.a-lbl")?.textContent).toBe("Пароль");
    expect(form.querySelector("label")?.getAttribute("for")).toBe("pw");
    expect([field().type, field().className, field().getAttribute("aria-invalid"), field().hasAttribute("data-autofocus")]).toEqual(["password", "a-inp", null, true]);
    expect([go().textContent, go().type, go().disabled]).toEqual(["Открыть", "submit", false]);
    expect(root.querySelector(".lock-none")?.textContent).toBe("Нет пароля? Спросите у того, кто прислал ссылку.");
    expect(root.querySelector(".u-hd")).not.toBeNull(); // the page's language menu stays
  });

  it("the board's error: the field marked, the line with the warning under it, the field described by it", async () => {
    const { actions, type, root, field } = setup({ unlock: async () => Promise.reject(new ApiError(401, "wrong_password", 0, 0)) });
    type("abcd2345");
    await actions.submit();
    expect([field().className, field().getAttribute("aria-invalid"), field().getAttribute("aria-describedby")]).toEqual(["a-inp err", "true", "pw-err pw-note"]);
    const err = root.querySelector("p.a-err")!;
    expect([err.id, err.getAttribute("role"), err.textContent, err.querySelector("svg.i") !== null]).toEqual(["pw-err", "alert", "Пароль не подошёл, проверьте раскладку и попробуйте ещё раз", true]);
    expect(err.parentElement).toBe(field().parentElement);
    type("abcd234");
    expect([field().className, field().hasAttribute("aria-invalid"), root.querySelector(".a-err")]).toEqual(["a-inp", false, null]);
  });

  it("a request in flight: Open says so with a spinner and is not sent twice; the field is read-only, not disabled", async () => {
    const { st, root, actions, field, type } = setup({ unlock: () => new Promise<void>(() => {}) });
    type("abcd2345");
    void actions.submit();
    expect(st.busy).toBe(true);
    const go = root.querySelector<HTMLButtonElement>("[data-k=pw-go]")!;
    expect([go.disabled, go.textContent, go.querySelector(".spin") !== null, go.getAttribute("aria-busy")]).toEqual([true, "Проверяем…", true, "true"]);
    expect([field().readOnly, field().disabled]).toEqual([true, false]);
  });

  it("a right password reloads the page (the server has set the cookie)", async () => {
    const { actions, log, type } = setup();
    type("abcd2345");
    await actions.submit();
    expect(log).toEqual(["unlock abcd-2345", "reload"]);
  });

  it("a wrong password says how many tries are left and keeps the field", async () => {
    const { actions, log, type, root, field, st } = setup({ unlock: async () => Promise.reject(new ApiError(401, "wrong_password", 0, 3)) });
    type("abcd2345");
    await actions.submit();
    expect(root.querySelector("[role=alert]")?.textContent).toBe("Пароль не подошёл, проверьте раскладку и попробуйте ещё раз. Осталось попыток: 3");
    expect(field().value).toBe("abcd-2345");
    expect(st.busy).toBe(false);
    expect(log).not.toContain("reload");
    expect(document.activeElement).toBe(field());
    type("abcd234"); // typing again clears the message without a redraw (the cursor stays)
    expect(root.querySelector("[role=alert]")).toBeNull();
  });

  it("too many tries says to wait, in minutes", async () => {
    const { actions, type, root } = setup({ unlock: async () => Promise.reject(new ApiError(429, "locked", 540)) });
    type("abcd2345");
    await actions.submit();
    expect(root.querySelector("[role=alert]")?.textContent).toBe("Слишком много попыток. Попробуйте через 9 мин.");
  });

  it("no connection and other failures have their own words, and the English page speaks English", async () => {
    const net = setup({ unlock: async () => Promise.reject(new ApiError(0, "network")) });
    net.type("abcd2345");
    await net.actions.submit();
    expect(net.root.querySelector("[role=alert]")?.textContent).toBe("Нет связи. Проверьте интернет и повторите.");
    const other = setup({ unlock: async () => Promise.reject(new Error("boom")) });
    other.st.lang = "en";
    other.type("abcd2345");
    await other.actions.submit();
    expect(other.root.querySelector("[role=alert]")?.textContent).toBe("That didn’t work. Try again.");
    const wrong = setup({ unlock: async () => Promise.reject(new ApiError(401, "wrong_password", 0, 2)) });
    wrong.st.lang = "en";
    wrong.type("abcd2345");
    await wrong.actions.submit();
    expect(wrong.root.querySelector("[role=alert]")?.textContent).toBe("That password didn’t match, check the keyboard layout and try again. Tries left: 2");
  });

  it("every language says the same things: the title, the line under it, the error, the hint", async () => {
    for (const lang of ["ru", "en", "zh", "fa", "tr"] as const) {
      const f = setup({ unlock: async () => Promise.reject(new ApiError(401, "wrong_password", 0, 1)) });
      f.st.lang = lang;
      f.type("abcd2345");
      await f.actions.submit();
      expect(f.root.querySelector(".a-lock h2")?.textContent, lang).toBeTruthy();
      expect(f.root.querySelector(".a-lock .a-lead")?.textContent, lang).toBeTruthy();
      expect(f.root.querySelector(".a-err")?.textContent, lang).toMatch(/1/);
      expect(f.root.querySelector(".lock-none")?.textContent, lang).toBeTruthy();
      f.type("к3м9б");
      expect(f.root.querySelector("#pw-note")?.textContent, lang).toBeTruthy();
    }
  });

  it("does not send twice while a call is in flight, and the form is not a form post", async () => {
    let n = 0;
    const { actions, type, root } = setup({ unlock: () => new Promise<void>((r) => (n++, setTimeout(r, 5))) });
    type("abcd2345");
    const first = actions.submit();
    await actions.submit();
    await first;
    expect(n).toBe(1);
    const ev = new Event("submit", { cancelable: true });
    root.querySelector("form")!.dispatchEvent(ev);
    expect(ev.defaultPrevented).toBe(true); // the CSP has form-action 'none': a real submit would be blocked
  });

  it("the language switch works on the form too", () => {
    const { root, log } = setup();
    root.querySelector<HTMLButtonElement>("[data-k=lang-en]")!.click();
    expect(log).toEqual(["lang en"]);
    expect(root.querySelector(".a-lock h2")?.textContent).toBe("This page is password-protected");
  });
});

describe("the call", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("posts the password to the page's own origin with the cookie jar on", async () => {
    const calls: { url: string; init: RequestInit }[] = [];
    vi.stubGlobal("fetch", async (url: string, init: RequestInit) => {
      calls.push({ url, init });
      return new Response('{"ok":true}', { status: 200 });
    });
    await unlock("https://sub.example.com/p/t/unlock", "abcd-2345");
    expect(calls[0]!.url).toBe(endpoint("https://sub.example.com/p/t/unlock", ""));
    expect(new URL(calls[0]!.url).origin).toBe(location.origin);
    expect(calls[0]!.init.credentials).toBe("same-origin"); // 'omit' would drop the cookie of an unlocked page
    expect(JSON.parse(String(calls[0]!.init.body))).toEqual({ password: "abcd-2345" });
  });

  it("maps the server's answers to errors", async () => {
    vi.stubGlobal("fetch", async () => new Response('{"error":"wrong_password","left":2}', { status: 401 }));
    await expect(unlock("/p/t/unlock", "x")).rejects.toMatchObject({ status: 401, code: "wrong_password", left: 2 });
    vi.stubGlobal("fetch", async () => new Response('{"error":"locked","retry_after":600}', { status: 429, headers: { "Retry-After": "600" } }));
    await expect(unlock("/p/t/unlock", "x")).rejects.toMatchObject({ status: 429, code: "locked", retryAfter: 600 });
    vi.stubGlobal("fetch", async () => Promise.reject(new TypeError("offline")));
    await expect(unlock("/p/t/unlock", "x")).rejects.toMatchObject({ code: "network" });
  });
});
