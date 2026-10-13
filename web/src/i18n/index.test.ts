import { describe, expect, it } from "vitest";
import { en } from "./en";
import { ru } from "./ru";
import { dictionaries, isLang, langs, localeOf, pickForm } from "./index";

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
  it("Chinese has one form: a message without \"|\" as it is, a leftover \"a|b\" gives the last form for every count", () => {
    for (const lang of ["zh", "zh-CN"]) {
      for (const n of [0, 1, 2, 5, 11, 21]) {
        expect(pickForm("{n} 条记录", n, lang)).toBe("{n} 条记录");
        expect(pickForm("{n} entry|{n} entries", n, lang)).toBe("{n} entries");
        expect(pickForm("{n} one|{n} few|{n} many", n, lang)).toBe("{n} many");
      }
    }
  });
});

describe("languages", () => {
  it("are the three of the panel, each with its dictionary and an Intl locale", () => {
    expect([...langs].sort()).toEqual(["en", "ru", "zh"]);
    for (const lang of langs) {
      expect(isLang(lang)).toBe(true);
      expect(Object.keys(dictionaries[lang]).sort(), lang).toEqual(Object.keys(en).sort());
      expect(Intl.DateTimeFormat.supportedLocalesOf(localeOf(lang))).toHaveLength(1);
    }
    expect(localeOf("zh")).toBe("zh-CN");
    expect(isLang("fr")).toBe(false);
  });
});

describe("dictionaries", () => {
  it("have a text for every key and keep the same {placeholders} in every language", () => {
    const ph = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]);
    const set = (s: string) => [...new Set(ph(s))].sort().join();
    for (const lang of langs) {
      for (const key of Object.keys(en) as (keyof typeof en)[]) {
        expect(dictionaries[lang][key], `${lang} ${key}`).toBeTruthy();
        expect(set(dictionaries[lang][key]), `${lang} ${key}`).toBe(set(en[key]));
      }
    }
  });

  // the glossary (one word per idea): words the owner reads as jargon or a calque never come back into the Russian copy
  it("keep the Russian copy free of rejected words", () => {
    const banned = [/инбаунд/i, /inbound/i, /парк/i, /руками/i, /путь warp/i, /вернуть звук/i, /синтетическ/i, /клиентск\S* проверк/i, /нода × профиль/i, /сетевая база/i, /применить базу/i, /tls-имя/i, /ждёт агента/i, /токен api/i];
    for (const [key, text] of Object.entries(ru)) for (const re of banned) expect(text, key).not.toMatch(re);
  });

  // the quotes and the colon belong to the language: the code joins a name or a label through these, never by hand
  it("quotes names and joins a label with the language's own marks", () => {
    expect(en["common.quoted"]).toBe("“{name}”");
    expect(ru["common.quoted"]).toBe("«{name}»");
    expect(dictionaries.zh["common.quoted"]).toBe("「{name}」");
    expect(en["common.labeled"]).toBe("{label}: {value}");
    expect(dictionaries.zh["common.labeled"]).toBe("{label}：{value}");
    expect(dictionaries.zh["hl.strip.headline"]).toBe("{count}：{names}{more}");
  });
});
