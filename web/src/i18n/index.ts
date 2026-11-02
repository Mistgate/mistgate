import { useMemo, useSyncExternalStore } from "react";
import { en, type MessageKey, type Messages } from "./en";
import { ru } from "./ru";
import { zh } from "./zh";
import { fa } from "./fa";
import { tr } from "./tr";
import { readPref, writePref } from "@/lib/storage";

export type Lang = "en" | "ru" | "zh" | "fa" | "tr";
/** The order of every language list and menu. */
export const langs: Lang[] = ["en", "ru", "zh", "fa", "tr"];

/** Each language in its own words, for the pickers (Settings, first-run setup). */
export const langNames: Record<Lang, string> = { ru: "Русский", en: "English", zh: "简体中文", fa: "فارسی", tr: "Türkçe" };
/** The short code on the language button of the top bar. */
export const langShort: Record<Lang, string> = { ru: "RU", en: "EN", zh: "中文", fa: "FA", tr: "TR" };

/** The <html lang> locale of a language: "zh" is written zh-CN (simplified, mainland), the others as they are. */
export const localeOf = (lang: Lang): string => (lang === "zh" ? "zh-CN" : lang);
/**
 * The locale Intl formats with. Persian keeps Latin digits (the counts, IPs, ports and versions the copy carries are
 * Latin text, so a number formatted next to them is too) and the Persian calendar, as the person's page shows dates.
 */
export const intlLocaleOf = (lang: Lang): string => (lang === "fa" ? "fa-u-ca-persian-nu-latn" : localeOf(lang));
/** Persian is written right to left; the page mirrors through logical (start/end) utilities. */
export const dirOf = (lang: Lang): "rtl" | "ltr" => (lang === "fa" ? "rtl" : "ltr");

export const isLang = (v: unknown): v is Lang => langs.includes(v as Lang);

export const dictionaries: Record<Lang, Messages> = { en, ru, zh, fa, tr };
const listeners = new Set<() => void>();

function detect(): Lang {
  const saved = readPref("lang");
  if (isLang(saved)) return saved;
  const base = navigator.language.toLowerCase().split("-")[0];
  return isLang(base) ? base : "en";
}

/** <html lang> and <html dir> follow the language (public/splash.js sets them first, before the bundle runs). */
function apply(lang: Lang) {
  current = lang;
  document.documentElement.lang = localeOf(lang);
  document.documentElement.dir = dirOf(lang);
}

let current: Lang = detect();
apply(current);

export function setLang(lang: Lang) {
  apply(lang);
  writePref("lang", lang);
  listeners.forEach((l) => l());
}

/** The instance owner's default language; a device that picked one (or saved it) keeps its own. */
export function applyInstanceLang(lang: string) {
  if (!isLang(lang) || readPref("lang")) return;
  apply(lang);
  listeners.forEach((l) => l());
}

function subscribe(cb: () => void) {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

const rtlLetter = /[\u0590-\u08FF\uFB1D-\uFDFF\uFE70-\uFEFF]/;

/**
 * Fills the {name} placeholders. With `isolate`, a right-to-left sentence gets each value wrapped in FSI…PDI: an IP, a
 * path, a version or a name with brackets keeps its own direction instead of being reordered by the Persian around it.
 * A template with no right-to-left letter (a command line, "{label}: {value}" in Latin) is filled as it is.
 */
export function fill(text: string, vars?: Record<string, string | number>, isolate = false) {
  if (!vars) return text;
  const wrap = isolate && rtlLetter.test(text);
  return text.replace(/\{(\w+)\}/g, (_, name: string) => {
    const value = String(vars[name] ?? "");
    return wrap && value ? `\u2068${value}\u2069` : value;
  });
}

/** The text without bidi marks (fill()'s isolates, the Persian copy's LRMs): what goes to the clipboard or a share sheet. */
export const plainText = (text: string) => text.replace(/[\u200E\u200F\u2066-\u2069]/g, "");

/**
 * Picks a form of a "one|few|many" message. Two forms mean one|other (English, Turkish); three mean
 * one|few|many (Russian). A single form is returned as it is (Persian always has one: a noun stays singular after a
 * number, and Intl counts 0 as "one" there); Chinese has one, so for "zh" the rule is always "other" and a leftover "a|b"
 * gives the last form.
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
    const rtl = dirOf(lang) === "rtl";
    const t = (key: MessageKey, vars?: Record<string, string | number>) => fill(dictionaries[lang][key], vars, rtl);
    const n = (key: MessageKey, count: number, vars?: Record<string, string | number>) =>
      fill(pickForm(dictionaries[lang][key], count, lang), { n: count, ...vars }, rtl);
    return Object.assign(t, { n });
  }, [lang]);
}
