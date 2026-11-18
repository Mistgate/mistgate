import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { dictionaries, langs } from "@/i18n";
import { planTitleVariants, planTitleWithoutNames, structuralCodes } from "./integrations";

// What every plan says to the owner by key, as the Go side finds it in internal/panel/mcp (plan_vocabulary_test.go pins the file): the plan
// tools, the keys of the facts, the codes the facts carry and the warnings. Each has to be worded in every language, so a plan card in the
// assistant and a row of the approval queue never show a raw key or an English line. A plan that says something new fails here until
// its words are added to src/i18n/planwords.ts (and, for a coded fact, to factWords in integrations.ts).
type Vocabulary = { tools: string[]; fact_keys: string[]; codes: string[]; dangers: string[]; outcomes: string[]; failures: string[] };
const vocabulary = JSON.parse(readFileSync(`${process.cwd()}/../internal/panel/mcp/testdata/plan_vocabulary.json`, "utf8")) as Vocabulary;

// codes the server builds at run time ("path_speed_" + direction): the Go test cannot list them
const builtCodes = ["path_speed_down", "path_speed_up", "ipv6_effect_enable", "ipv6_effect_disable"];

const has = (lang: (typeof langs)[number], key: string) => Boolean((dictionaries[lang] as Record<string, string>)[key]);
const missing = (keys: string[], prefix: string) => langs.flatMap((lang) => keys.filter((k) => !has(lang, prefix + k)).map((k) => `${lang}: ${prefix}${k}`));

describe("the words of every plan", () => {
  it("knows the scan found the plans", () => {
    expect(vocabulary.tools.length).toBeGreaterThan(50);
    expect(vocabulary.fact_keys.length).toBeGreaterThan(150);
  });
  it("titles every plan tool in every language", () => {
    expect(missing(vocabulary.tools, "approval.tool.")).toEqual([]);
  });
  it("labels every fact key in every language", () => {
    expect(missing(vocabulary.fact_keys, "approval.fact.")).toEqual([]);
  });
  it("words every warning a plan raises in every language", () => {
    expect(missing(vocabulary.dangers, "approval.danger.")).toEqual([]);
  });
  it("words what came of every plan that can be applied, and every reason it was refused, in every language", () => {
    // a speed test is worded by its direction; a refusal with no code ("") is a plain error
    const outcomes = vocabulary.outcomes.map((c) => (c === "path_speed_done" ? "path_speed_done.down" : c)).filter((c) => !["decoy_site_assigned"].includes(c));
    expect(missing(outcomes, "approval.outcome.")).toEqual([]);
    expect(missing(["decoy_site_assigned", "decoy_site_assigned.panel", "decoy_site_assigned.both", "path_speed_done.up"], "approval.outcome.")).toEqual([]);
    expect(missing(vocabulary.failures.filter(Boolean), "approval.fail.")).toEqual([]);
  });
  it("words every code a fact carries: in factWords itself, or by a message of its own", () => {
    const own = [...vocabulary.codes, ...builtCodes].filter((c) => !structuralCodes.includes(c));
    expect(missing(own, "approval.value.")).toEqual([]);
  });
  it("titles every plan with the names its facts carry, or says it has none", () => {
    const named = vocabulary.tools.filter((tool) => !planTitleWithoutNames.includes(tool));
    // a tool with no names stays out of the list of the titled, and the list names no tool that does not exist
    expect(planTitleWithoutNames.filter((tool) => !vocabulary.tools.includes(tool))).toEqual([]);
    expect(missing(named, "approval.title.")).toEqual([]);
    const variants = Object.entries(planTitleVariants).flatMap(([tool, v]) => v.names.map((n) => `${tool}.${n}`));
    expect(Object.keys(planTitleVariants).filter((tool) => !named.includes(tool))).toEqual([]);
    expect(missing(variants, "approval.title.")).toEqual([]);
  });
  it("puts into a title only the facts the plan has, the same ones in every language", () => {
    const keys = new Set(vocabulary.fact_keys);
    const names = (lang: (typeof langs)[number], key: string) => [...new Set(((dictionaries[lang] as Record<string, string>)[key] ?? "").match(/{w+}/g) ?? [])].sort();
    const titles = Object.keys(dictionaries.en).filter((k) => k.startsWith("approval.title."));
    expect(titles.length).toBeGreaterThan(50);
    for (const key of titles) {
      const want = names("en", key);
      for (const n of want) expect(keys.has(n.slice(1, -1)), `${key} names ${n}, which no plan has as a fact`).toBe(true);
      for (const lang of langs) expect(names(lang, key), `${lang} ${key}`).toEqual(want);
    }
  });
});
