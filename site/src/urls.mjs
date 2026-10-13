export const SITE = "https://mistgate.app";
export const GITHUB = "https://github.com/Mistgate/mistgate";
export const LANGS = ["en", "ru", "zh"];
/** Languages whose docs have every English page. A language that is not complete may be missing pages (even the whole
 *  docs/<lang> directory): the build skips what is absent, the language menu links only to what exists (else to that
 *  language's home), and the parity test checks only the pages that are there. Flip zh to true when all pages are
 *  translated: from then on the parity and diagram tests require every page and the panel-wording tests (brand.test)
 *  cover zh too, so the placeholder English UI strings in layout.mjs, landing.mjs and wiki.mjs must be translated first. */
export const COMPLETE = { en: true, ru: true, zh: false };

/** docs/<lang>/guide/nodes.md (key "guide/nodes") -> /guide/nodes/, /ru/guide/nodes/ or /zh/guide/nodes/; key "index" is the language home. */
export function pageUrl(lang, key) {
  const base = lang === "en" ? "" : `/${lang}`;
  return key === "index" ? `${base}/` : `${base}/${key}/`;
}
export const editUrl = (lang, key) => `${GITHUB}/edit/main/docs/${lang}/${key}.md`;
