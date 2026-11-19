// translate.mjs: the translator of the design (land.i18n.js) for the site.
//   The design translates the live DOM from Russian by a dictionary of ~7000 strings (1.8 MB, four languages). The pages
//   carry only what their boards can show: a per-language subset (one language, only the strings of the boards of the page).
//   The translator code is the design's own, patched (each patch must apply once), and runs at build time (pre-render) and in
//   the browser (strings the scripts assemble on the fly: tab captions, tour steps, counters).
import { parse } from 'parse5';
import vm from 'node:vm';

export const LANGS = ['EN', 'TR', 'ZH', 'FA']; // order of the translations in the dictionary
const walk = (n, f) => { f(n); for (const c of n.childNodes || []) walk(c, f); if (n.content) walk(n.content, f); };

/** the pieces of land.i18n.js: the dictionary object and the translator code after it */
export function parseI18n(src) {
  const lines = src.split('\n');
  const di = lines.findIndex((l) => /^\s*var D = \{/.test(l));
  if (di < 0) throw new Error('land.i18n.js: the dictionary line (var D = {…}) is not where it was');
  const dl = lines[di];
  const dict = JSON.parse(dl.slice(dl.indexOf('{'), dl.lastIndexOf('};') + 1));
  const head = lines.slice(0, di).join('\n'); // up to the dictionary: the IIFE opens in it
  const code = lines.slice(di + 1).join('\n');
  return { dict, head, code };
}

/** patched translator body (everything the IIFE does after `var D`). Every patch must apply exactly once. */
export function patchTranslator(code) {
  const patch = (from, to) => { if (code.split(from).length !== 2) throw new Error('translator patch must apply exactly once: ' + from.slice(0, 60)); code = code.replace(from, () => to); };
  patch("var h = document.documentElement; if (h.getAttribute('data-lg') !== cur) { h.setAttribute('data-lg', cur); h.setAttribute('lang', cur.toLowerCase()); }", "/* site: <html lang> and data-lg belong to the page */");
  patch("if (cur === 'FA' || cur === 'ZH') fonts();", '/* site: no fonts from Google */');
  patch("IDX = { EN: 0, TR: 1, ZH: 2, FA: 3 }", 'IDX = { EN: 0, TR: 0, ZH: 0, FA: 0 }'); // the subset holds one language
  patch('window.MG_I18N = { kick: kick,', 'window.MG_I18N = { now: apply, kick: kick,'); // apply at once (the pre-render, the first mount)
  return code;
}

/** ES module text: startI18n({ru string: translation }) starts the translator over the page */
export function translatorModule(i18nSrc) {
  const { code } = parseI18n(i18nSrc);
  return `// generated from the design's land.i18n.js (translate.mjs)\nexport function startI18n(raw) {\n  var D = {}; for (var k in raw) D[k] = [raw[k]];\n${patchTranslator(code).replace(/\n\}\)\(\);\s*$/, '\n')}\n}\n`;
}

/** a function (string, LANG) -> translation|null with the design's own tr(), over a dictionary (full: 4 langs; subset: one, as {k:[v]}). */
export function makeTr(i18nSrc, dict, { subset = false, onHit } = {}) {
  const { code } = parseI18n(i18nSrc);
  const body = subset ? patchTranslator(code) : code;
  const watched = onHit ? new Proxy(dict, { get: (t, k) => { if (typeof k === 'string' && Object.prototype.hasOwnProperty.call(t, k)) onHit(k); return t[k]; } }) : dict;
  const sandbox = {
    D0: watched,
    window: { requestAnimationFrame() {}, MG_LANG: null },
    document: { readyState: 'complete', documentElement: { getAttribute() {}, setAttribute() {} }, addEventListener() {}, createTreeWalker() { return { nextNode: () => null }; }, querySelectorAll: () => [], getElementById: () => null },
    MutationObserver: class { observe() {} },
    NodeFilter: { SHOW_TEXT: 4 },
    setTimeout() {},
  };
  vm.createContext(sandbox);
  vm.runInContext(`(function () { var D = D0;\n${body.replace(/\n\}\)\(\);\s*$/, '\n')}\n})();`, sandbox);
  return (s, lang) => sandbox.window.MG_I18N.tr(s, lang);
}

/** every string a board can put on screen: text nodes, attribute values and script text of the sources */
export function corpusOf(sources) {
  const parts = [];
  for (const src of sources) walk(parse(src), (n) => { if (n.nodeName === '#text') parts.push(n.value); for (const a of n.attrs || []) parts.push(a.value); });
  const NUM = /\d+(?:[.,]\d+)?/g;
  const flat = parts.join('\n').replace(/[ \t\r\f\v]*\n[ \t\r\f\v]*/g, '\n').replace(/[ \t]+/g, ' ');
  return { flat, flatN: flat.replace(NUM, '#'), words: new Set(flat.match(/[А-Яа-яЁё]+/g) || []) };
}

/** keys of the dictionary a page can need: the string occurs in the sources (also with numbers folded to #).
 *  `loose` (no harvest of the live screens yet): also every string whose Cyrillic words all occur in the sources, which brings in
 *  what the scripts assemble ("# нод · # онлайн") at about 1.6 times the size. */
export function subsetKeys(dict, corpus, extra = [], { loose = false } = {}) {
  const keep = new Set(extra);
  for (const k of Object.keys(dict)) {
    if (corpus.flat.includes(k) || corpus.flatN.includes(k) || (loose && (k.match(/[А-Яа-яЁё]+/g) || [0]).every((w) => corpus.words.has(w)))) keep.add(k);
  }
  return [...keep].sort();
}

/** the keys the full translator reads to translate these strings (the strings the screens showed while the states were swept) */
export function keysUsedFor(i18nSrc, dict, strings, langs = LANGS) {
  const hit = new Set();
  const tr = makeTr(i18nSrc, dict, { onHit: (k) => hit.add(k) });
  for (const s of strings) for (const l of langs) tr(s, l);
  return hit;
}

/** one language of a subset: {ru string: translation}, in a stable order */
export function langDict(dict, keys, lang) {
  const i = LANGS.indexOf(lang.toUpperCase());
  if (i < 0) throw new Error('no such language in the dictionary: ' + lang);
  return Object.fromEntries(keys.map((k) => [k, dict[k][i]]));
}
