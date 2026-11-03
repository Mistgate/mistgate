export const SITE = "https://mistgate.app";
export const GITHUB = "https://github.com/Mistgate/mistgate";
export const LANGS = ["en", "ru", "zh", "fa", "tr"];
/** Languages whose docs have every English page. A language that is not complete may be missing pages (even the whole
 *  docs/<lang> directory): the build skips what is absent, the language menu links only to what exists (else to that
 *  language's home), and the parity test checks only the pages that are there. Its sidebar still lists every English
 *  page: one not translated yet links to the English page with an "EN" mark, and so do the links to it from its pages.
 *  Flip a language to true when its last page lands: from then on the parity and diagram tests require every page and
 *  the panel-wording tests (brand.test) cover it. fa and tr have no wiki strings yet (wiki.mjs keeps en, ru and zh). */
export const COMPLETE = { en: true, ru: true, zh: true, fa: false, tr: false };

/** docs/<lang>/guide/nodes.md (key "guide/nodes") -> /guide/nodes/, /ru/guide/nodes/, /fa/guide/nodes/ ...; key "index" is the language home. */
export function pageUrl(lang, key) {
  const base = lang === "en" ? "" : `/${lang}`;
  return key === "index" ? `${base}/` : `${base}/${key}/`;
}
export const editUrl = (lang, key) => `${GITHUB}/edit/main/docs/${lang}/${key}.md`;
