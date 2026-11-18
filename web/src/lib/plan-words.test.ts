import { describe, expect, it } from "vitest";
import { dictionaries, fill, pickForm, type Lang, type T } from "@/i18n";
import { makeFmt } from "./format";
import { dangerText, factLabel, factWords, planTitle, type Fact, type Piece } from "./integrations";

// The plans the owner saw half in English on the live panel: a decoy site put on the panel, a speed test between two nodes. In each
// language the title, the labels and the values are the panel's own words; a name from data stays on a plate of its own.

const tr = (lang: Lang) =>
  Object.assign((key: never, vars?: Record<string, string | number>) => fill((dictionaries[lang] as Record<string, string>)[key as string]!, vars), {
    n: (key: never, n: number, vars?: Record<string, string | number>) => fill(pickForm((dictionaries[lang] as Record<string, string>)[key as string]!, n, lang), { n, ...vars }),
  }) as unknown as T;

const fact = (key: string, value: string, over: Partial<Fact> = {}): Fact => ({ key, value, untrusted: false, code: "", params: {}, untrustedParams: [], ...over }) as Fact;
const text = (pieces: Piece[]) => pieces.map((p) => (typeof p === "string" ? p : `«${p.data}»`)).join("");
const line = (lang: Lang, f: Fact) => {
  const t = tr(lang);
  return `${factLabel(t, f.key)}: ${text(factWords(t, makeFmt(lang, t), f))}`;
};

describe("a decoy site put on the panel only", () => {
  const facts = [
    fact("site", "Studio", { untrusted: true }),
    fact("state", "draft"),
    fact("version", "2"),
    fact("marks_ready", "true"),
    fact("panel", "true"),
    fact("replaces", "the panel: dir", { untrusted: true, code: "replaces", params: { previous: "dir", site: "" }, untrustedParams: ["site"] }),
  ];
  it("is worded in Russian: the title, the labels, the yes and the state", () => {
    const t = tr("ru");
    expect(planTitle(t, "decoy_site_assign")).toBe("Поставить сайт-ширму на ноды или на панель");
    expect(facts.map((f) => line("ru", f))).toEqual([
      "Сайт: «Studio»",
      "Состояние: черновик",
      "version: 2".replace("version", factLabel(t, "version")),
      "Пометит готовым: да",
      "На адресе панели: да",
      "Заменяет: панель: каталог панели",
    ]);
  });
  it("shows no English, no raw key and no true/false in any language", () => {
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const f of facts) {
        const out = line(lang, f);
        expect(out, `${lang} ${f.key}`).not.toMatch(/true|false|marks_ready|draft|\bdir\b/);
      }
    }
  });
  it("names the panel's address in the warning, not the nodes", () => {
    expect(dangerText(tr("ru"), "panel_site")).toBe("Меняет сайт на адресе панели.");
    expect(dangerText(tr("ru"), "panel_site")).not.toContain("нод");
  });
});

describe("the replaced site of a node", () => {
  it("puts the node and the site's names on plates and the rest in the panel's words", () => {
    const f = fact("replaces", "de1: own Old site", { untrusted: true, code: "replaces", params: { previous: "own", site: "Old site", target: "de1" }, untrustedParams: ["site", "target"] });
    expect(line("ru", f)).toBe("Заменяет: «de1»: сайт «Old site»");
    expect(line("en", f)).toBe("Replaces: «de1»: the site «Old site»");
  });
});

describe("the speed test between two nodes", () => {
  it("words the effect and the direction in the interface language", () => {
    const effect = fact("effect", "The target opens a listener ...", { code: "path_speed_effect" });
    expect(line("ru", effect)).toContain("Целевая нода откроет на свободном порту приёмник");
    expect(line("ru", effect)).not.toContain("The target");
    expect(planTitle(tr("ru"), "path_speed")).toBe("Проверить скорость между двумя нодами");
  });
});

describe("retention and tokens", () => {
  it("words a retention change and a log level", () => {
    expect(line("ru", fact("journal_days", "7 -> 14 days"))).toBe("Журнал нод: 7 дней → 14 дней");
    expect(line("ru", fact("to", "debug"))).toContain("отладка");
  });
});
