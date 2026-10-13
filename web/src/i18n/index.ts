import { useMemo, useSyncExternalStore } from "react";
import { en, type MessageKey, type Messages } from "./en";
import { ru } from "./ru";
import { zh } from "./zh";
import { readPref, writePref } from "@/lib/storage";

export type Lang = "en" | "ru" | "zh";
/** The order of every language list and menu. */
export const langs: Lang[] = ["en", "ru", "zh"];

/** Each language in its own words, for the pickers (Settings, first-run setup). */
export const langNames: Record<Lang, string> = { ru: "Русский", en: "English", zh: "简体中文" };
/** The short code on the language button of the top bar. */
export const langShort: Record<Lang, string> = { ru: "RU", en: "EN", zh: "中文" };

/** The Intl / <html lang> locale of a language: "zh" is written zh-CN (simplified, mainland), the others as they are. */
export const localeOf = (lang: Lang): string => (lang === "zh" ? "zh-CN" : lang);

export const isLang = (v: unknown): v is Lang => langs.includes(v as Lang);

export const dictionaries: Record<Lang, Messages> = { en, ru, zh };
const listeners = new Set<() => void>();

function detect(): Lang {
  const saved = readPref("lang");
  if (isLang(saved)) return saved;
  const nav = navigator.language.toLowerCase();
  return nav.startsWith("ru") ? "ru" : nav.startsWith("zh") ? "zh" : "en";
}

let current: Lang = detect();
document.documentElement.lang = localeOf(current);

export function setLang(lang: Lang) {
  current = lang;
  writePref("lang", lang);
  document.documentElement.lang = localeOf(lang);
  listeners.forEach((l) => l());
}

/** The instance owner's default language; a device that picked one (or saved it) keeps its own. */
export function applyInstanceLang(lang: string) {
  if (!isLang(lang) || readPref("lang")) return;
  current = lang;
  document.documentElement.lang = localeOf(lang);
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export function fill(text: string, vars?: Record<string, string | number>) {
  return vars ? text.replace(/\{(\w+)\}/g, (_, name: string) => String(vars[name] ?? "")) : text;
}

/**
 * Picks a form of a "one|few|many" message. Two forms mean one|other (English); three mean
 * one|few|many (Russian). A single form is returned as it is; Chinese has one, so for "zh" the rule is always "other" and a
 * leftover "a|b" gives the last form.
 */
export function pickForm(message: string, n: number, lang: string): string {
  const forms = message.split("|");
  if (forms.length === 1) return forms[0]!;
  const rule = new Intl.PluralRules(lang).select(n);
  const i = rule === "one" ? 0 : rule === "few" && forms.length > 2 ? 1 : forms.length - 1;
  return forms[i]!;
}

export type T = {
  (key: MessageKey, vars?: Record<string, string | number>): string;
  /** Plural message: t.n("health.problems", 2) fills {n} itself. */
  n: (key: MessageKey, count: number, vars?: Record<string, string | number>) => string;
};

export function useLang(): Lang {
  return useSyncExternalStore(subscribe, () => current);
}

export function useT(): T {
  const lang = useLang();
  return useMemo(() => {
    const t = (key: MessageKey, vars?: Record<string, string | number>) => fill(dictionaries[lang][key], vars);
    const n = (key: MessageKey, count: number, vars?: Record<string, string | number>) =>
      fill(pickForm(dictionaries[lang][key], count, lang), { n: count, ...vars });
    return Object.assign(t, { n });
  }, [lang]);
}
