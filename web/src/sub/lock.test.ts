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
    expect(root.querySelector("#pw-note")?.textContent).toBe("Пароль набирается латиницей — переключите клавиатуру на English (кнопка 🌐).");
    type("k3m9x");
    expect(root.querySelector("#pw-note")?.textContent).toBe("");
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
    expect(field().getAttribute("autocomplete")).toBe("off");
  });

  it("says where the password comes from, once per browser, and what to do without one", () => {
    const { root } = setup();
    expect(root.querySelector(".lock-head p.mut")?.textContent).toBe("Его прислали вместе со ссылкой. Спросим один раз — в этом браузере запомним.");
    expect(root.querySelector(".lock-none")?.textContent).toBe("Нет пароля? Спросите у того, кто прислал ссылку.");
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
    expect(root.querySelector("[role=alert]")?.textContent).toBe("Пароль не подошёл. Осталось попыток: 3.");
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
    expect(root.querySelector(".ct")?.textContent).toBe("Enter the password");
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
