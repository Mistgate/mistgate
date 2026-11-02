// The admin's own UI strings, read from web/src/i18n/*.ts, so the takes find buttons by the words the app shows in
// each language. A plain line parser: `"key": "value",` inside `const en|ru|zh|fa|tr|core = {` blocks.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const dir = fileURLToPath(new URL("../web/src/i18n/", import.meta.url));
const dict = { en: {}, ru: {}, zh: {}, fa: {}, tr: {} };
for (const file of readdirSync(dir).filter((f) => f.endsWith(".ts") && !f.includes(".test."))) {
  let lang = null;
  for (const line of readFileSync(join(dir, file), "utf8").split("\n")) {
    const head = line.match(/^(?:export )?const (en|ru|zh|fa|tr|core)\b/);
    if (head) lang = head[1] === "core" ? file.replace(".ts", "") : head[1];
    const kv = lang in dict && line.match(/^\s*"([^"]+)":\s*("(?:[^"\\]|\\.)*"),?\s*$/);
    if (kv) dict[lang][kv[1]] = JSON.parse(kv[2]);
  }
}

/** The text of a key in a language, placeholders filled; the plural forms are split on "|" and the first is taken. */
export function t(lang, key, vars = {}) {
  const s = dict[lang][key] ?? dict.en[key];
  if (s === undefined) throw new Error(`no string ${key}`);
  return s.split("|")[0].replace(/\{(\w+)\}/g, (m, k) => (k in vars ? vars[k] : m));
}

// the person's page (web/src/sub/i18n.ts): `key: "value",` inside `const ru = {`, `const en: Dict = {` and the like
const subDict = { en: {}, ru: {}, zh: {}, fa: {}, tr: {} };
{
  let lang = null;
  for (const line of readFileSync(fileURLToPath(new URL("../web/src/sub/i18n.ts", import.meta.url)), "utf8").split("\n")) {
    const head = line.match(/^const (en|ru|zh|fa|tr)\b[^=]*= \{/);
    if (head) lang = head[1];
    else if (/^\};/.test(line)) lang = null;
    const kv = lang && line.match(/^  (\w+):\s*("(?:[^"\\]|\\.)*"),?\s*$/);
    if (kv) subDict[lang][kv[1]] = JSON.parse(kv[2]);
  }
}
/** A string of the person's page. */
export function sub(lang, key) {
  const s = subDict[lang][key];
  if (s === undefined) throw new Error(`no sub string ${key}`);
  return s;
}
