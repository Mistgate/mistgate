// links.mjs: links.json (design link -> docs page key) -> the real addresses of the site for one language.
// Addresses are the ones site/build.mjs writes: en has no prefix, the rest has /<lang>/; docs/<lang>/<key>.md is /<lang>/<key>/.
// A docs page a language does not have (fa and tr are incomplete) is the English page. A key that is not an English docs page
// is a build error: the link table would point nowhere.
import { readFileSync, existsSync } from 'node:fs';
import { join } from 'node:path';

export const slugify = (s) => s.trim().toLowerCase().replace(/[^\p{L}\p{N}\s-]/gu, '').replace(/\s+/g, '-') || 'section'; // as site/src/markdown.mjs
export const pageUrl = (lang, key) => `${lang === 'en' ? '' : '/' + lang}/${key}/`;
export const homeUrl = (lang) => (lang === 'en' ? '/' : `/${lang}/`);
export const appUrl = (lang) => (lang === 'en' ? '/app/' : `/${lang}/app/`);

const headings = (file) => {
  const out = []; let fence = false;
  for (const l of readFileSync(file, 'utf8').replace(/^﻿/, '').replace(/\r\n?/g, '\n').split('\n')) {
    if (/^```/.test(l)) fence = !fence;
    const m = !fence && /^## (.+?)\s*$/.exec(l);
    if (m) out.push(m[1].replace(/[`*_]/g, ''));
  }
  return out;
};

export function resolveLinks(table, lang, docsDir) {
  const has = (l, key) => existsSync(join(docsDir, l, key + '.md'));
  const resolve = (v) => {
    if (v === '@main') return homeUrl(lang);
    if (v === '@app') return appUrl(lang);
    const key = typeof v === 'string' ? v : v.key;
    if (!has('en', key)) throw new Error(`links.json: "${key}" is not a docs page (docs/en/${key}.md)`);
    const l = has(lang, key) ? lang : 'en';
    let url = pageUrl(l, key);
    if (typeof v === 'object') {
      const h = headings(join(docsDir, l, key + '.md'))[v.heading];
      if (h) url += '#' + slugify(h); // a language whose page has fewer headings links to the page alone
    }
    return url;
  };
  const out = { map: {}, byText: {}, byPrefix: [], page: {} };
  for (const [k, v] of Object.entries(table.map)) out.map[k] = resolve(v);
  for (const [k, v] of Object.entries(table.byText)) { if (k.endsWith('*')) out.byPrefix.push([k.slice(0, -1), resolve(v)]); else out.byText[k] = resolve(v); }
  for (const [k, v] of Object.entries(table.page)) out.page[k] = resolve(v);
  return out;
}
