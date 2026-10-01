export const SITE = "https://mistgate.app";
export const GITHUB = "https://github.com/Mistgate/mistgate";
export const LANGS = ["en", "ru"];

/** docs/<lang>/guide/nodes.md (key "guide/nodes") -> /guide/nodes/ or /ru/guide/nodes/; key "index" is the language home. */
export function pageUrl(lang, key) {
  const base = lang === "en" ? "" : `/${lang}`;
  return key === "index" ? `${base}/` : `${base}/${key}/`;
}
export const editUrl = (lang, key) => `${GITHUB}/edit/main/docs/${lang}/${key}.md`;
