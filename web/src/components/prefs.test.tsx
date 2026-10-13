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
    expect(rows.map((r) => r.textContent)).toEqual(["EnglishEN", "РусскийRU", "简体中文中文"]);
    expect(rows.map((r) => r.getAttribute("lang"))).toEqual(["en", "ru", "zh-CN"]);

    await act(async () => rows[2]!.click());
    await settle();
    expect(document.documentElement.lang).toBe("zh-CN");
    expect(trigger.getAttribute("aria-label")).toBe("Language: 简体中文");
    expect(trigger.textContent).toBe("中文");
  });
});
