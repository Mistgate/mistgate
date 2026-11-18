import { describe, expect, it } from "vitest";
import { dictionaries, fill, pickForm, type Lang, type T } from "@/i18n";
import { makeFmt } from "./format";
import { ApprovalState } from "@/gen/mistgate/admin/v1/integrations_pb";
import { dangerText, factLabel, factWords, outcomeText, planTitle, planTitleWords, type Fact, type Piece } from "./integrations";

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

describe("the title of a plan with its names", () => {
  const title = (lang: Lang, tool: string, facts: Fact[]) => {
    const t = tr(lang);
    const pieces = planTitleWords(t, makeFmt(lang, t), tool, facts);
    return pieces && pieces.map((p) => (typeof p === "string" ? p : p.data)).join("");
  };
  const relay = (action: string) => [fact("action", action), fact("entry", "msk1", { untrusted: true }), fact("exit", "fi1", { untrusted: true })];
  it("says what the board's card says: the relay of fi1 through msk1, in every language", () => {
    expect(title("ru", "relay", relay("enable"))).toBe("Включить ретрансляцию fi1 через msk1");
    expect(title("en", "relay", relay("disable"))).toBe("Turn off the relay of fi1 through msk1");
    for (const lang of ["zh", "fa", "tr"] as const) {
      const out = title(lang, "relay", relay("enable"));
      expect(out, lang).toContain("fi1");
      expect(out, lang).toContain("msk1");
      expect(out, lang).not.toMatch(/\{|relay/i);
    }
  });
  it("keeps the names apart from the panel's words, and takes the variant the facts ask for", () => {
    const t = tr("ru");
    const pieces = planTitleWords(t, makeFmt("ru", t), "relay", relay("port"))!;
    expect(pieces.filter((p) => typeof p !== "string")).toEqual([{ data: "fi1" }, { data: "msk1" }]);
    expect(title("ru", "relay", relay("port"))).toContain("Сменить порт");
    expect(title("ru", "relay", relay("create"))).toContain("Создать ретрансляцию");
    // an action it has no words for: the general title of the relay, still with the names
    expect(title("ru", "relay", relay("other"))).toBe("Изменить ретрансляцию fi1 через msk1");
  });
  it("names the group, the site, the template, the person", () => {
    expect(title("ru", "group_create", [fact("name", "Семья", { untrusted: true })])).toBe("Создать группу «Семья»");
    expect(title("ru", "subscription_mihomo_template_set", [fact("template", "Для семьи", { untrusted: true }), fact("action", "update")])).toBe("Изменить шаблон Mihomo «Для семьи»");
    expect(title("ru", "subscription_mihomo_template_set", [fact("template", "Для семьи", { untrusted: true }), fact("action", "remove")])).toBe("Удалить шаблон Mihomo «Для семьи»");
    expect(title("en", "user_update", [fact("user", "alice", { untrusted: true })])).toBe("Change the user alice");
  });
  it("counts the people when there are several, and words a level and a time zone", () => {
    const people = (n: number) => [fact("count", String(n)), ...["alice", "bob", "carol"].slice(0, n).map((u) => fact("users", u, { untrusted: true }))];
    expect(title("ru", "user_disable", people(1))).toBe("Отключить alice");
    expect(title("ru", "user_disable", people(3))).toBe("Отключить 3 пользователей");
    expect(title("en", "user_reset_traffic", people(2))).toBe("Reset the traffic of 2 users");
    expect(title("ru", "log_level", [fact("to", "debug")])).toContain("отладка");
    expect(title("en", "update_timezone", [fact("to", "180 min", { code: "utc_offset", params: { minutes: "180" } })])).toBe("Set the time zone of update schedules to UTC+03:00");
  });
  it("falls back to the general title when a name is missing, and has none for a plan without names", () => {
    expect(title("ru", "relay", [fact("action", "enable")])).toBeNull();
    expect(title("ru", "group_update", [fact("group", "", { untrusted: true })])).toBeNull();
    expect(title("ru", "retention", [fact("journal_days", "7 -> 14 days")])).toBeNull();
    expect(planTitle(tr("ru"), "relay")).toBe("Изменить ретрансляцию");
  });
  it("puts a site on the nodes, the panel, or both", () => {
    const site = [fact("site", "Studio", { untrusted: true })];
    expect(title("en", "decoy_site_assign", [...site, fact("nodes", "de1, nl1", { untrusted: true }), fact("panel", "false")])).toBe("Put the decoy site “Studio” on de1, nl1");
    expect(title("en", "decoy_site_assign", [...site, fact("panel", "true")])).toBe("Put the decoy site “Studio” on the panel");
    expect(title("en", "decoy_site_assign", [...site, fact("nodes", "de1", { untrusted: true }), fact("panel", "true")])).toBe("Put the decoy site “Studio” on nodes and the panel");
  });
});

describe("the reverse bridge's check, with its time", () => {
  it("words a verdict and a stage with the time, not 'ok at 2026-…'", () => {
    const reach = fact("reverse_reach", "reach at 2026-11-18 14:20 UTC");
    const stage = fact("reverse_reach", "ok  at 2026-11-18 14:20 UTC");
    const coded = fact("reverse_reach", "fail other at 2026-11-18 14:20 UTC");
    for (const lang of ["en", "ru", "zh", "fa", "tr"] as const) {
      for (const f of [reach, stage, coded]) expect(line(lang, f), `${lang} ${f.value}`).not.toMatch(/\bat\b|UTC|\b(ok|fail|reach)\b/);
    }
    expect(line("ru", reach)).toMatch(/^Доступ в обратную сторону: да, проверено /);
    expect(line("ru", stage)).toMatch(/: проходит, проверено /);
    expect(line("ru", coded)).toMatch(/: не проходит \(other\), проверено /);
    expect(line("en", stage)).toMatch(/: passes, checked /);
  });
});
