import { describe, expect, it } from "vitest";
import { dictionaries, fill, pickForm, type Lang, type T } from "@/i18n";
import { makeFmt } from "./format";
import { ApprovalState } from "@/gen/mistgate/admin/v1/integrations_pb";
import { dangerText, factLabel, factWords, outcomeText, planTitle, type Fact, type Piece } from "./integrations";

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

describe("what came of an applied plan", () => {
  const speed = { from: "nl1", to: "de1", direction: "down", mb: "32", mbit_s: "308.3", duration_ms: "870", connect_ms: "37", first_byte_ms: "37" };
  const said = (lang: Lang, outcomeCode: string, outcomeParams: Record<string, string>, state = ApprovalState.APPLIED) => {
    const t = tr(lang);
    return outcomeText(t, makeFmt(lang, t), { state, outcomeCode, outcomeParams });
  };
  it("words a speed test with its figures and names, not the panel's English line", () => {
    expect(said("ru", "path_speed_done", speed)).toBe("Замер скорости nl1 → de1, 32 МБ от целевой ноды к измеряющей: 308.3 Мбит/с за 870 мс, соединение 37 мс, первый байт через 37 мс.");
    expect(said("ru", "path_speed_done", { ...speed, direction: "up" })).not.toContain("первый байт");
    expect(said("en", "path_speed_done", speed)).toContain("308.3 Mbit/s over 870 ms");
    for (const lang of ["zh", "fa", "tr"] as const) expect(said(lang, "path_speed_done", speed), lang).toContain("308.3");
  });
  it("words a channel measure with its figures, partners and the caveat of an estimate, in every language", () => {
    const params = { node: "de1", down_mbps: "812.4", up_mbps: "455.1", down_partner: "nl1", up_partner: "se1" };
    expect(said("ru", "channel_measure_done", params)).toBe("Канал de1 измерен: приём 812.4 Мбит/с с nl1, отдача 455.1 Мбит/с с se1. Оценка на тот момент, а не гарантия тарифа.");
    expect(said("en", "channel_measure_done", params)).toBe("Channel of de1 measured: receiving 812.4 Mbit/s with nl1, sending 455.1 Mbit/s with se1. An estimate at that moment, not a guarantee of the tariff.");
    expect(said("ru", "channel_measure_done", { ...params, direction: "down" })).toContain("только на приём");
    expect(said("en", "channel_measure_done", { ...params, direction: "up" })).toContain("sending only");
    expect(said("en", "channel_measure_done", { node: "de1", direction: "running" })).toContain("still running");
    for (const lang of ["zh", "fa", "tr"] as const) {
      expect(said(lang, "channel_measure_done", params), lang).toContain("812.4");
      expect(said(lang, "channel_measure_done", params), lang).toContain("se1");
    }
    expect(said("ru", "channel_measure_not_run", { code: "no_partner" }, ApprovalState.FAILED)).toContain("замер не запустился");
  });
  it("words the plan card of a channel measure: its title, the partners, the warning and what happens", () => {
    expect(planTitle(tr("ru"), "channel_measure")).toBe("Измерить канал ноды");
    expect(line("ru", fact("partners", "nl1 (2000 Mbit/s known)"))).toContain("nl1");
    const effect = fact("effect", "The node and each partner take turns ...", { code: "channel_measure_effect" });
    expect(line("ru", effect)).toContain("2 ГиБ и 15 секундах");
    expect(line("ru", effect)).not.toContain("take turns");
    expect(line("en", effect)).toContain("paid traffic pays for it");
  });
  it("words where a decoy site went: nodes, the panel, or both", () => {
    expect(said("ru", "decoy_site_assigned", { n: "0", panel: "true" })).toBe("Сайт-ширма стоит на адресе панели");
    expect(said("ru", "decoy_site_assigned", { n: "2", panel: "false" })).toBe("Сайт-ширма стоит на 2 нодах");
    expect(said("ru", "decoy_site_assigned", { n: "2", panel: "true" })).toBe("Сайт-ширма стоит на 2 нодах и на адресе панели");
  });
  it("words a refusal, and gives nothing for a code it has no words for", () => {
    expect(said("ru", "decoy_site_changed", {}, ApprovalState.FAILED)).toContain("сайт изменился после создания плана: создай план заново");
    expect(said("ru", "no_such_code", {})).toBeNull();
  });
});
