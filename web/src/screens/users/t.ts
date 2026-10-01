import { useMemo } from "react";
import { en, type MessageKey } from "@/i18n/en";
import { pickForm, useLang, type Lang } from "@/i18n";
import { ru } from "@/i18n/ru";

// `useT` plus two things the people screens need: `opt` (text for a key that may not exist: a protocol's schema
// copy, see src/i18n/profiles.ts) and `lang` (date formats). Same call shape as `useT`, so it passes where a T is asked.
const dictionaries: Record<Lang, Record<string, string>> = { en, ru };

export type Key = MessageKey;
type Vars = Record<string, string | number>;

export type Tx = {
  (key: Key, vars?: Vars): string;
  /** Plural message: t.n("profiles.nodes", 2) fills {n} itself. */
  n: (key: Key, count: number, vars?: Vars) => string;
  /** Text for a key that may not exist: undefined when there is no entry. */
  opt: (key: string, vars?: Vars) => string | undefined;
  lang: Lang;
};

const fill = (text: string, vars?: Vars) =>
  vars ? text.replace(/\{(\w+)\}/g, (_, name: string) => String(vars[name] ?? "")) : text;

export function useTx(): Tx {
  const lang = useLang();
  return useMemo(() => {
    const dict = dictionaries[lang];
    const t = (key: Key, vars?: Vars) => fill(dict[key] ?? key, vars);
    const n = (key: Key, count: number, vars?: Vars) => fill(pickForm(dict[key] ?? key, count, lang), { n: count, ...vars });
    const opt = (key: string, vars?: Vars) => (key in dict ? fill(dict[key]!, vars) : undefined);
    return Object.assign(t, { n, opt, lang });
  }, [lang]);
}
