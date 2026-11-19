// build.mjs: the board pipeline. One command: design/ -> build/ (the input of the page generator, see plans/site-contract.md)
//   node build.mjs [--no-prerender] [--pages main,app] [--langs en,ru,...] [--keep-shell]
//   design/*.dc.html  --compile-->  ES modules  --esbuild-->  assets/*.js (hashed, split: the page, one chunk per board)
//   design/*.css      --prune by the sources of the page, minify-->  assets/*.css      (+ fonts of the repository)
//   land.i18n.js      --patched translator + per-language subset of the dictionary-->  assets/lang-<page>-<lang>.json
//   a headless Chrome mounts every page in every language and writes the first render:  pages/<page>.<lang>.html
//   manifest.json     what the generator needs
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync, copyFileSync, existsSync, statSync } from 'node:fs';
import { join, dirname, basename } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createHash } from 'node:crypto';
import { brotliCompressSync, constants } from 'node:zlib';
import * as esbuild from 'esbuild';
import { compileBoard } from './compile.mjs';
import { closure } from './sync.mjs';
import { PATCHES } from './patches.mjs';
import { scanBlobs, blobAsset } from './blobs.mjs';
import { parseI18n, translatorModule, makeTr, corpusOf, subsetKeys, keysUsedFor, langDict } from './translate.mjs';
import { usage, pruneCss } from './css.mjs';
import { resolveLinks, homeUrl, appUrl } from './links.mjs';
import { prerender } from './prerender.mjs';
import { budgets } from './budgets.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const p = (...a) => join(HERE, ...a);
const DESIGN = process.env.MG_DESIGN || p('..', 'design');
const BUILD = process.env.MG_BUILD || p('..', 'build');
const REPO = p('..', '..', '..');
const DOCS = process.env.MG_DOCS || join(REPO, 'docs');
const FONTS = join(REPO, 'web', 'src', 'fonts');
const args = process.argv.slice(2);
const opt = (k) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : null; };
const PAGES = (opt('--pages') || 'main,app').split(',');
const LANGS = (opt('--langs') || 'en,ru,zh,fa,tr').split(',');
const ALL_LANGS = ['en', 'ru', 'zh', 'fa', 'tr'];
const PAGE_BOARD = { main: 'Main', app: 'AppPage' };
const SUFFIX = { ru: '', en: 'En', zh: 'Zh', fa: 'Fa', tr: 'Tr' };
const CSS_ORDER = ['land', 'app', 'sub', 'pn']; // the order of the design
const CSS_FILE = { land: 'land.css', sub: 'sub-s.css', pn: 'pn.css' };
const appCss = (lang) => (lang === 'fa' ? 'app-fa-s.css' : lang === 'zh' ? 'app-zh-s.css' : 'app-s.css');
const brLen = (b) => brotliCompressSync(b, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } }).length;
const sha = (b, n = 8) => createHash('sha256').update(b).digest('hex').slice(0, n);
const warnings = [];
const warn = (m) => { warnings.push(m); console.warn('warning: ' + m); };
const t0 = performance.now();
const lap = (what) => console.log(`${((performance.now() - t0) / 1000).toFixed(1).padStart(6)}s  ${what}`);

// ---- 0. input ----
const read = (n) => readFileSync(join(DESIGN, n), 'utf8');
const names = closure((n) => read(n + '.dc.html'), PAGES.map((x) => PAGE_BOARD[x]));
const sources = Object.fromEntries(names.map((n) => [n, read(n + '.dc.html')]));
const designFiles = [...names.map((n) => n + '.dc.html'), 'land.css', 'pn.css', 'sub-s.css', 'app-s.css', 'app-zh-s.css', 'app-fa-s.css', 'land.i18n.js'];
const designVersion = sha(designFiles.map((f) => f + '\0' + read(f)).join('\0'), 12);
const isAppBoard = (n) => /^App(Main|Routes|Diag|Set|Byp)(En|Zh|Fa|Tr)?$/.test(n);
const langOfBoard = (n) => { const m = /(En|Zh|Fa|Tr)$/.exec(n); return m ? m[1].toLowerCase() : 'ru'; };
/** boards one page can mount in one language: the App boards of other languages never are */
const boardsFor = (page, lang) => closure((n) => sources[n], [PAGE_BOARD[page]]).filter((n) => !isAppBoard(n) || langOfBoard(n) === lang);

rmSync(BUILD, { recursive: true, force: true });
for (const d of ['assets', 'pages', '_gen/boards', '_gen/runtime', '_gen/entries']) mkdirSync(join(BUILD, d), { recursive: true });
const asset = (name, body) => { writeFileSync(join(BUILD, 'assets', name), body); return 'assets/' + name; };
const hashedAsset = (stem, ext, body) => asset(`${stem}.${sha(body)}.${ext}`, body);

// ---- 1. compile the boards ----
const info = scanBlobs(sources);
const compiled = {}, images = new Map(), standIns = [];
// the brand mark: the svg the panel boards embed as a data: URI (the /_blob/ of the brand links is the same picture)
const logoSvg = names.map((n) => { const m = /src="data:image\/svg\+xml;base64,([^"]+)"/.exec(sources[n]); return m && Buffer.from(m[1], 'base64').toString(); }).find((x) => x && /aria-label="Mistgate"/.test(x)) || '';
if (!logoSvg) warn('the brand mark (the data: svg of the panel boards) was not found; the logo stand-in is empty');
const blobUrl = (h) => { const a = blobAsset(h, info, logoSvg); images.set(a.file, a.body); if (a.standIn && !standIns.some((x) => x.hash === h)) standIns.push({ hash: h, kind: a.kind, cc: a.cc, file: a.file }); return '/assets/' + a.file; };
let patchCount = 0;
for (const n of names) {
  const page = n === 'Main' || n === 'AppPage';
  const c = compileBoard(sources[n], n, { page, blob: blobUrl });
  compiled[n] = c; patchCount += c.patches.length;
  for (const [f, body] of c.assets) images.set(f, body);
  writeFileSync(join(BUILD, '_gen/boards', n + '.js'), c.code);
}
for (const pt of PATCHES) for (const r of pt.required) if (names.includes(r) && !compiled[r].patches.includes(pt.id)) throw new Error(`patch ${pt.id} did not apply to ${r}`);
for (const [f, body] of images) asset(f, body);
copyFileSync(p('runtime', 'rt.js'), join(BUILD, '_gen/runtime/rt.js'));
lap(`compiled ${names.length} boards, ${patchCount} patches applied, ${images.size} images`);

// ---- 2. entries of the pages, bundles ----
const i18nSrc = read('land.i18n.js');
writeFileSync(join(BUILD, '_gen/i18n.js'), translatorModule(i18nSrc));
const eagerOf = (page) => {
  const e = compiled[PAGE_BOARD[page]].eager[0];
  return Object.fromEntries(LANGS.map((l) => [l, names.includes(e + SUFFIX[l]) ? [e + SUFFIX[l]] : [e]])); // AppMain -> AppMainEn for English
};
const loadable = (page) => [...new Set(compiled[PAGE_BOARD[page]].lazy)];
for (const page of PAGES) {
  const board = PAGE_BOARD[page];
  writeFileSync(join(BUILD, '_gen/entries', page + '.js'), [
    `import { boot, registry } from '../runtime/rt.js';`,
    `import { startI18n } from '../i18n.js';`,
    `import Page from '../boards/${board}.js';`,
    `registry.load = {${loadable(page).map((n) => ` ${n}: () => import('../boards/${n}.js')`).join(',')} };`,
    `registry.i18n = startI18n;`,
    `boot(${JSON.stringify(board)}, Page, ${JSON.stringify(eagerOf(page))});`,
  ].join('\n'));
}
const entry = Object.fromEntries(PAGES.map((pg) => [pg, join(BUILD, '_gen/entries', pg + '.js')]));
const boardEntries = [...new Set(PAGES.flatMap(loadable))];
for (const n of boardEntries) entry['b-' + n] = join(BUILD, '_gen/boards', n + '.js');
const res = await esbuild.build({
  entryPoints: entry, bundle: true, splitting: true, format: 'esm', target: 'chrome120', minify: true, legalComments: 'none', metafile: true, logLevel: 'warning',
  outdir: join(BUILD, 'assets'), entryNames: '[name].[hash]', chunkNames: 'c-[hash]', nodePaths: [p('node_modules')],
});
const out = res.metafile.outputs; // 'build/assets/x.hash.js' -> {bytes, imports, entryPoint}
const rel = (f) => 'assets/' + basename(f);
const outByEntry = {};
for (const f of Object.keys(out)) { const b = basename(f); for (const k of Object.keys(entry)) if (b.startsWith(k + '.') && b.endsWith('.js') && out[f].entryPoint) outByEntry[k] = f; }
const closureOf = (f, dyn = false, seen = new Set()) => { seen.add(f); for (const i of out[f]?.imports || []) if ((dyn || i.kind === 'import-statement') && out[i.path] && !seen.has(i.path)) closureOf(i.path, dyn, seen); return seen; };
lap(`bundled: ${Object.keys(out).length} files`);

// ---- 3. fonts and css ----
let fontsCss = readFileSync(join(FONTS, 'fonts.css'), 'utf8');
const fontFiles = {};
fontsCss = fontsCss.replace(/url\("\.\/([\w-]+\.woff2)"\)/g, (_, f) => { fontFiles[f] = fontFiles[f] || hashedAsset(f.replace('.woff2', ''), 'woff2', readFileSync(join(FONTS, f))); return `url("/${fontFiles[f]}")`; });
const fontsFile = hashedAsset('fonts', 'css', Buffer.from((await esbuild.transform(fontsCss, { loader: 'css', minify: true })).code));
const siteCss = hashedAsset('site', 'css', Buffer.from('body{margin:0;background:#0c0c0e}[data-board]{display:contents}'));
const designCss = Object.fromEntries(['land.css', 'pn.css', 'sub-s.css', 'app-s.css', 'app-zh-s.css', 'app-fa-s.css'].map((f) => [f, read(f)]));
const tokensOf = (bs) => usage(bs.map((n) => sources[n]));
const cssStats = {}, prunedClasses = {};
const cssOut = {}; // page -> {land, sub, pn, app: {file group -> path}}
async function emitCss(page, key, file, boardList) {
  const pr = pruneCss(designCss[file], tokensOf(boardList));
  const min = Buffer.from((await esbuild.transform(pr.css, { loader: 'css', minify: true, target: 'chrome120' })).code);
  const path = hashedAsset(file.replace(/\.css$/, ''), 'css', min);
  cssStats[`${page}:${key}`] = { file: path, from: designCss[file].length, pruned: pr.stats.bytesOut, raw: min.length, br: brLen(min) };
  const pc = (prunedClasses[page] ||= { dropped: new Set(), kept: new Set() }); pr.dropped.forEach((x) => pc.dropped.add(x)); pr.kept.forEach((x) => pc.kept.add(x)); // per page: what the stand checks the screens against
  return min.length > 40 ? path : null; // a stylesheet with (almost) nothing left is not shipped
}
for (const page of PAGES) {
  const all = closure((n) => sources[n], [PAGE_BOARD[page]]);
  const nonApp = all.filter((n) => !isAppBoard(n)), hasApp = all.some(isAppBoard), hasSub = all.some((n) => /^Sub/.test(n)), hasPn = all.some((n) => /^Pn/.test(n));
  const o = (cssOut[page] = { app: {} });
  o.land = await emitCss(page, 'land', 'land.css', all);
  if (hasPn) o.pn = await emitCss(page, 'pn', 'pn.css', all);
  if (hasSub) o.sub = await emitCss(page, 'sub', 'sub-s.css', all);
  if (hasApp) for (const f of ['app-s.css', 'app-zh-s.css', 'app-fa-s.css']) {
    const ls = ALL_LANGS.filter((l) => appCss(l) === f && LANGS.includes(l));
    if (ls.length) o.app[f] = await emitCss(page, f.replace('.css', ''), f, [...new Set([...nonApp, ...ls.flatMap((l) => boardsFor(page, l))])]);
  }
}
lap('css pruned');

// ---- 4. dictionaries and lang bundles ----
const { dict } = parseI18n(i18nSrc);
const harvest = existsSync(p('harvest.json')) ? JSON.parse(readFileSync(p('harvest.json'), 'utf8')) : null; // Russian strings the live screens showed (check.mjs --harvest)
const subsets = {}; // page -> {keys, perLang}
for (const page of PAGES) {
  const own = closure((n) => sources[n], [PAGE_BOARD[page]]).filter((n) => !isAppBoard(n)); // App boards are written in their language: the translator skips [data-app]
  const strings = harvest && harvest.designVersion === designVersion ? harvest.strings[page] || [] : null;
  if (!strings) warn(`harvest.json is ${harvest ? 'of another design version' : 'missing'}: the dictionary of ${page} is built the loose way (bigger); run "pnpm check --harvest"`);
  subsets[page] = subsetKeys(dict, corpusOf(own.map((n) => sources[n])), strings ? [...keysUsedFor(i18nSrc, dict, strings)] : [], { loose: !strings });
}
const urlsOf = (page) => Object.fromEntries(ALL_LANGS.map((l) => [l, page === 'app' ? appUrl(l) : homeUrl(l)]));
const linksTable = JSON.parse(readFileSync(p('links.json'), 'utf8'));
const bundles = {}; // page:lang -> path
const pagesUrls = Object.fromEntries(['main', 'app'].map((pg) => [pg, urlsOf(pg)]));
for (const page of PAGES) for (const lang of LANGS) {
  const o = cssOut[page];
  const css = { before: o.pn ? '/' + o.pn : '', app: o.app[appCss(lang)] ? ['/' + o.app[appCss(lang)]] : [], sub: o.sub ? ['/' + o.sub] : [] };
  const b = { lang, links: resolveLinks(linksTable, lang, DOCS), css, pages: pagesUrls };
  if (lang !== 'ru') b.dict = langDict(dict, subsets[page], lang.toUpperCase());
  bundles[`${page}:${lang}`] = hashedAsset(`lang-${page}-${lang}`, 'json', Buffer.from(JSON.stringify(b)));
}
lap('dictionaries: ' + PAGES.map((pg) => `${pg} ${subsets[pg].length} keys`).join(', '));

// ---- 5. page titles of the current site (site/src/landing.mjs) ----
function siteMeta() {
  const src = readFileSync(join(REPO, 'site', 'src', 'landing.mjs'), 'utf8'), meta = {};
  for (const l of ALL_LANGS) {
    const a = src.indexOf(`\n  ${l}: {`); if (a < 0) continue;
    const b = src.indexOf('\n  },', a), blk = src.slice(a, b < 0 ? undefined : b);
    const get = (k) => { const m = new RegExp(`\\n    ${k}: ("(?:[^"\\\\]|\\\\.)*")`).exec(blk); return m ? JSON.parse(m[1]) : null; };
    meta[l] = { description: get('meta'), h1: get('h1') };
  }
  return meta;
}
const sm = siteMeta();

// ---- 6. manifest (without the fragments), the shells for the pre-render ----
const manifest = { v: 1, designVersion, langs: LANGS, pages: {}, assets: [] };
const siteFonts = (lang) => ['onest-latin', ...(lang === 'ru' ? ['onest-cyrillic'] : []), ...(lang === 'fa' ? ['vazirmatn-arabic'] : [])].map((s) => fontFiles[s + '.woff2']);
const jsOf = (page, lang) => {
  const main = outByEntry[page], eager = eagerOf(page)[lang].map((n) => outByEntry['b-' + n]);
  const stat = new Set([...closureOf(main), ...eager.flatMap((f) => [...closureOf(f)])]);
  return { main, eager, pre: [...stat].filter((f) => f !== main) };
};
for (const page of PAGES) for (const lang of LANGS) {
  const o = cssOut[page], js = jsOf(page, lang);
  const blocking = [fontsFile, siteCss, o.land, o.app[appCss(lang)] && !o.pn ? o.app[appCss(lang)] : null, o.pn].filter(Boolean);
  const m = ((manifest.pages[page] ||= {})[lang] = {
    body: `pages/${page}.${lang}.html`, title: '', description: '', css: blocking, modules: [rel(js.main)], preload: js.pre.map(rel), dir: lang === 'fa' ? 'rtl' : 'ltr',
    htmlAttrs: { 'data-lg': lang.toUpperCase() }, fonts: siteFonts(lang),
  });
  if (page === 'main') { const s = sm[lang]; m.title = s?.h1 ? `Mistgate — ${s.h1}` : ''; m.description = s?.description || ''; if (!s?.description) { m.todo = true; warn(`main/${lang}: no title/description in site/src/landing.mjs`); } }
}
writeFileSync(join(BUILD, 'manifest.json'), JSON.stringify(manifest, null, 2));
lap('manifest written');

// ---- 7. pre-render ----
let pre = { fragments: {}, report: {} };
if (!args.includes('--no-prerender')) {
  pre = await prerender({ build: BUILD, manifest, bundles, pages: PAGES, langs: LANGS, profile: p('.profile') });
  for (const [k, html] of Object.entries(pre.fragments)) { const [page, lang] = k.split(':'); writeFileSync(join(BUILD, manifest.pages[page][lang].body), html); }
  for (const page of PAGES) for (const lang of LANGS) { // titles of the app page: from the design, translated
    const m = manifest.pages[page][lang], r = pre.report[`${page}:${lang}`] || {};
    if (page === 'app') { m.title = r.h1 ? `${r.h1} · Mistgate` : ''; m.description = r.lead || ''; if (!r.h1) { m.todo = true; warn(`app/${lang}: no title in the first render`); } }
  }
  lap(`pre-rendered ${Object.keys(pre.fragments).length} pages`);
}
// bundle attributes of the host of every fragment come from the build; the contract wants a fragment without inline scripts
for (const [k, html] of Object.entries(pre.fragments)) if (/<script|\son[a-z]+="/i.test(html)) throw new Error(`${k}: the fragment has an inline script or handler`);

// ---- 8. manifest (final), report, budgets ----
manifest.assets = readdirSync(join(BUILD, 'assets')).sort().map((f) => 'assets/' + f);
writeFileSync(join(BUILD, 'manifest.json'), JSON.stringify(manifest, null, 2));
if (!args.includes('--keep-shell')) rmSync(join(BUILD, '_pre'), { recursive: true, force: true });
rmSync(join(BUILD, '_gen'), { recursive: true, force: true });
const report = { designVersion, boards: names, compile: Object.fromEntries(names.map((n) => [n, { patches: compiled[n].patches, scoped: compiled[n].stats.scoped }])), standIns, css: cssStats, prunedClasses: Object.fromEntries(Object.entries(prunedClasses).map(([k, v]) => [k, { dropped: [...v.dropped].filter((x) => !v.kept.has(x)).sort() }])), dictKeys: Object.fromEntries(PAGES.map((pg) => [pg, subsets[pg].length])), warnings, prerender: pre.report };
report.budgets = budgets({ BUILD, manifest, out, outByEntry, closureOf, eagerOf, jsOf, entry, cssStats, cssOut, bundles, pages: PAGES, langs: LANGS, brLen, rel, boardsFor, names, isAppBoard, langOfBoard, fontFiles });
writeFileSync(join(BUILD, 'report.json'), JSON.stringify(report, null, 1));
lap('done');
if (standIns.length) console.log(`image stand-ins (the export has no /_blob/ files): ${standIns.length} (${[...new Set(standIns.map((s) => s.kind))].join(', ')}), see report.json`);
if (report.budgets.over.length) process.exitCode = 0; // warnings only: the table says what is over
