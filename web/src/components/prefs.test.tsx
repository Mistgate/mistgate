import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it } from "vitest";
import { setLang } from "@/i18n";
import { LangToggle } from "./prefs";

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  act(() => setLang("en"));
  localStorage.clear();
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));

describe("LangToggle", () => {
  it("is a button naming the current language; its list has a row per language, each in its own script and `lang`", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<LangToggle />));
    const trigger = host.querySelector<HTMLButtonElement>("button")!;
    expect(trigger.getAttribute("aria-label")).toBe("Language: English");
    expect(trigger.textContent).toBe("EN");

    await act(async () => {
      trigger.focus();
      trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    await settle();
    const rows = [...document.querySelectorAll<HTMLElement>("[role=option]")];
    expect(rows.map((r) => r.textContent)).toEqual(["EnglishEN", "РусскийRU", "简体中文中文", "فارسیFA", "TürkçeTR"]);
    expect(rows.map((r) => r.getAttribute("lang"))).toEqual(["en", "ru", "zh-CN", "fa", "tr"]);

    await act(async () => rows[2]!.click());
    await settle();
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(trigger.getAttribute("aria-label")).toBe("语言：简体中文");
    expect(trigger.textContent).toBe("中文");
    expect(document.documentElement.dir).toBe("ltr");
  });

  it("Persian turns the page right to left; any other language turns it back", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    act(() => root!.render(<LangToggle />));
    const trigger = host.querySelector<HTMLButtonElement>("button")!;
    await act(async () => {
      trigger.focus();
      trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    });
    await settle();
    await act(async () => [...document.querySelectorAll<HTMLElement>("[role=option]")][3]!.click());
    await settle();
    expect(document.documentElement.lang).toBe("fa");
    expect(document.documentElement.dir).toBe("rtl");
    expect(trigger.textContent).toBe("FA");
    expect(localStorage.getItem("lang")).toBe("fa");
    act(() => setLang("tr"));
    expect(document.documentElement.lang).toBe("tr");
    expect(document.documentElement.dir).toBe("ltr");
  });
});
