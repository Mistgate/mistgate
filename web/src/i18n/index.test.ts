import { describe, expect, it } from "vitest";
import { en } from "./en";
import { ru } from "./ru";
import { pickForm } from "./index";

describe("pickForm", () => {
  const ruForms = ru["auth.attemptsLeft"];
  it("picks Russian one / few / many", () => {
    expect(pickForm(ruForms, 1, "ru")).toBe("Осталась {n} попытка");
    expect(pickForm(ruForms, 2, "ru")).toBe("Осталось {n} попытки");
    expect(pickForm(ruForms, 5, "ru")).toBe("Осталось {n} попыток");
    expect(pickForm(ruForms, 11, "ru")).toBe("Осталось {n} попыток");
    expect(pickForm(ruForms, 21, "ru")).toBe("Осталась {n} попытка");
  });
  it("picks English one / other", () => {
    expect(pickForm(en["auth.attemptsLeft"], 1, "en")).toBe("{n} attempt left");
    expect(pickForm(en["auth.attemptsLeft"], 2, "en")).toBe("{n} attempts left");
  });
  it("Overview health strip: Russian plurals, one node, nobody online", () => {
    const pick = (k: "ov.allOk" | "ov.onlineNow", n: number) => pickForm(ru[k], n, "ru").replace("{n}", String(n));
    expect(pick("ov.allOk", 2)).toBe("Все 2 ноды работают");
    expect(pick("ov.allOk", 5)).toBe("Все 5 нод работают");
    expect(pick("ov.onlineNow", 1)).toBe("1 человек онлайн");
    expect(pick("ov.onlineNow", 3)).toBe("3 человека онлайн");
    expect(pick("ov.onlineNow", 12)).toBe("12 человек онлайн");
    expect(pickForm(en["ov.allOk"], 4, "en")).toBe("All {n} nodes are up");
    expect(ru["ov.oneUp"]).toBe("Нода {name} работает");
    expect(ru["ov.onlineNone"]).toBe("Сейчас никого онлайн");
  });
  it("returns a single form as it is", () => {
    expect(pickForm("Hello", 3, "en")).toBe("Hello");
  });
});

describe("dictionaries", () => {
  it("keep the same {placeholders} in both languages", () => {
    const ph = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]);
    const set = (s: string) => [...new Set(ph(s))].sort().join();
    for (const key of Object.keys(en) as (keyof typeof en)[]) expect(set(ru[key]), key).toBe(set(en[key]));
  });

  // the glossary (one word per idea): words the owner reads as jargon or a calque never come back into the Russian copy
  it("keep the Russian copy free of rejected words", () => {
    const banned = [/инбаунд/i, /inbound/i, /парк/i, /руками/i, /путь warp/i, /вернуть звук/i, /синтетическ/i, /клиентск\S* проверк/i, /нода × профиль/i, /сетевая база/i, /применить базу/i, /tls-имя/i, /ждёт агента/i, /токен api/i];
    for (const [key, text] of Object.entries(ru)) for (const re of banned) expect(text, key).not.toMatch(re);
  });
});
