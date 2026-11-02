import { describe, expect, it } from "vitest";
import { en } from "./en";
import { ru } from "./ru";
import { dictionaries, dirOf, fill, intlLocaleOf, isLang, langs, localeOf, pickForm, plainText } from "./index";

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
  it("are the five of the panel, each with its dictionary and an Intl locale", () => {
    expect([...langs].sort()).toEqual(["en", "fa", "ru", "tr", "zh"]);
    for (const lang of langs) {
      expect(isLang(lang)).toBe(true);
      expect(Object.keys(dictionaries[lang]).sort(), lang).toEqual(Object.keys(en).sort());
      expect(Intl.DateTimeFormat.supportedLocalesOf(intlLocaleOf(lang))).toHaveLength(1);
    }
    expect(localeOf("zh")).toBe("zh-CN");
    expect(localeOf("fa")).toBe("fa");
    expect(isLang("fr")).toBe(false);
    expect(langs.map(dirOf)).toEqual(["ltr", "ltr", "ltr", "rtl", "ltr"]);
  });

  it("format Persian with Latin digits, like the IPs and counts around them, and the Persian calendar, like the person's page", () => {
    const fa = intlLocaleOf("fa");
    expect(new Intl.NumberFormat(fa).format(1234.5)).toBe("1,234.5");
    const date = new Date(Date.UTC(2026, 10, 2, 12));
    expect(date.toLocaleDateString(fa, { day: "numeric", month: "long", year: "numeric", timeZone: "UTC" })).toBe("11 آبان 1405");
    expect(new Intl.NumberFormat(intlLocaleOf("tr")).format(1234.5)).toBe("1.234,5");
  });
});

describe("Persian and Turkish plurals", () => {
  it("Persian has one form everywhere: a noun stays singular after a number, and Intl counts 0 as \"one\"", () => {
    for (const [key, text] of Object.entries(dictionaries.fa)) expect(text.includes("|"), key).toBe(false);
    expect(pickForm(dictionaries.fa["pager.count"], 0, "fa")).toBe(dictionaries.fa["pager.count"]);
  });
  it("Turkish picks one|other by Intl, and a single form for every count", () => {
    expect(pickForm("{n} portu|{n} portları", 1, "tr")).toBe("{n} portu");
    expect(pickForm("{n} portu|{n} portları", 0, "tr")).toBe("{n} portları");
    expect(pickForm("{n} portu|{n} portları", 5, "tr")).toBe("{n} portları");
    for (const n of [0, 1, 2, 21]) expect(fill(pickForm(dictionaries.tr["pager.count"], n, "tr"), { n })).toBe(`${n} kayıt`);
    for (const [key, text] of Object.entries(dictionaries.tr)) expect(text.split("|").length, key).toBeLessThanOrEqual(2);
  });
});

describe("right to left", () => {
  it("isolates every value filled into a Persian sentence, and leaves Latin templates and copied text bare", () => {
    const sentence = fill("{ip} از {country} در دسترس نیست", { ip: "203.0.113.7", country: "Iran" }, true);
    expect(sentence).toBe("\u2068203.0.113.7\u2069 از \u2068Iran\u2069 در دسترس نیست");
    expect(plainText(sentence)).toBe("203.0.113.7 از Iran در دسترس نیست");
    expect(fill("mistgate backup keygen --identity-file {path}", { path: "/root/key.txt" }, true)).toBe("mistgate backup keygen --identity-file /root/key.txt");
    expect(fill("{ip} از", { ip: "203.0.113.7" })).toBe("203.0.113.7 از");
  });
  it("points the change arrow the way the language reads", () => {
    expect(dictionaries.fa["common.fromTo"]).toBe("{from} ← {to}");
    expect(dictionaries.tr["common.fromTo"]).toBe("{from} → {to}");
    expect(dictionaries.fa["common.quoted"]).toBe("«{name}»");
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
