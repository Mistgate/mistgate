// budgets.mjs: the size budgets of the plan of the new site, measured on the build, printed as a table (fact / target / red).
// Over the target: a warning with the name of the culprit; over the red line: RED. The build does not fail on it, it does not stay quiet either.
import { readFileSync, existsSync } from 'node:fs';
import { join, basename } from 'node:path';

const KB = 1024;
// name -> [target, red] in KB (br)
export const LIMITS = {
  'HTML of the first screen (page fragment)': [50, 70],
  'CSS blocking (land + pn / app)': [60, 80],
  'CSS lazy (app of the language + sub)': [45, 60],
  'Fonts preloaded for the first screen': [80, 120],
  'wasm + glue': [120, 150],
  'JS of the hero (runtime + Preact + page + first board)': [80, 110],
  'All boards behind the scroll': [450, 600],
  'Dictionary of the language': [45, 60],
};

export function budgets(c) {
  const { BUILD, manifest, out, outByEntry, closureOf, eagerOf, jsOf, cssStats, cssOut, bundles, pages, langs, brLen, boardsFor, names, isAppBoard } = c;
  const brOf = (f) => { const k = join(BUILD, f); return existsSync(k) ? brLen(readFileSync(k)) : 0; };
  const brCache = new Map();
  const jsBr = (f) => { if (!brCache.has(f)) brCache.set(f, brLen(readFileSync(join(BUILD, 'assets', basename(f))))); return brCache.get(f); };
  const inputsOf = (files) => { const m = new Map(); for (const f of files) for (const [i, v] of Object.entries(out[f].inputs || {})) m.set(i, (m.get(i) || 0) + v.bytesInOutput); return [...m].sort((a, b) => b[1] - a[1]).slice(0, 3).map(([i, b]) => `${i.replace(/^.*[\\/](?=[^\\/]+[\\/][^\\/]+$)/, '')} ${(b / KB).toFixed(0)} KB raw`).join(', '); };
  const rows = [];
  const add = (page, lang, name, bytes, culprit) => {
    const [t, r] = LIMITS[name], kb = bytes == null ? null : bytes / KB;
    rows.push({ page, lang, name, kb: kb == null ? null : +kb.toFixed(1), target: t, red: r, status: kb == null ? 'n/a' : kb > r ? 'RED' : kb > t ? 'over' : 'ok', culprit: kb != null && kb > t ? culprit : '' });
  };
  for (const page of pages) for (const lang of langs) {
    const m = manifest.pages[page][lang], o = cssOut[page];
    const frag = brOf(m.body);
    add(page, lang, 'HTML of the first screen (page fragment)', frag || null, `pages/${page}.${lang}.html: the markup of ${page === 'main' ? 'Main' : 'AppPage'} and the first board`);
    const blocking = m.css.filter((x) => /\/(land|pn|app)[.-]/.test('/' + x));
    add(page, lang, 'CSS blocking (land + pn / app)', blocking.reduce((s, x) => s + brOf(x), 0), blocking.map((x) => `${x} ${(brOf(x) / KB).toFixed(1)} KB`).join(', '));
    const app = o.app[lang === 'fa' ? 'app-fa-s.css' : lang === 'zh' ? 'app-zh-s.css' : 'app-s.css'];
    const lazy = [!blocking.includes(app) && app, o.sub].filter(Boolean);
    add(page, lang, 'CSS lazy (app of the language + sub)', lazy.reduce((s, x) => s + brOf(x), 0), lazy.map((x) => `${x} ${(brOf(x) / KB).toFixed(1)} KB`).join(', '));
    add(page, lang, 'Fonts preloaded for the first screen', (m.fonts || []).reduce((s, x) => s + statSize(join(BUILD, x)), 0), (m.fonts || []).join(', '));
    add(page, lang, 'wasm + glue', null, '');
    const js = jsOf(page, lang), hero = new Set([js.main, ...js.pre]);
    add(page, lang, 'JS of the hero (runtime + Preact + page + first board)', [...hero].reduce((s, f) => s + jsBr(f), 0), inputsOf(hero));
    const behind = new Set();
    for (const n of boardsFor(page, lang)) { const f = outByEntry['b-' + n]; if (f) for (const g of closureOf(f)) if (!hero.has(g)) behind.add(g); }
    add(page, lang, 'All boards behind the scroll', [...behind].reduce((s, f) => s + jsBr(f), 0), inputsOf(behind));
    if (lang !== 'ru') {
      const b = JSON.parse(readFileSync(join(BUILD, bundles[`${page}:${lang}`]), 'utf8'));
      add(page, lang, 'Dictionary of the language', brLen(Buffer.from(JSON.stringify(b.dict))), `${Object.keys(b.dict).length} strings of ${page}`);
    }
  }
  const over = rows.filter((r) => r.status === 'over' || r.status === 'RED');
  console.log('\nbudgets (br, KB): fact / target / red');
  const grouped = new Map();
  for (const r of rows) { const k = `${r.page}:${r.name}`; (grouped.get(k) || grouped.set(k, []).get(k)).push(r); }
  for (const [k, rs] of grouped) {
    const [page, ...nm] = k.split(':'), vals = rs.filter((r) => r.kb != null).map((r) => `${r.lang} ${r.kb}`).join('  ');
    const worst = rs.some((r) => r.status === 'RED') ? 'RED ' : rs.some((r) => r.status === 'over') ? 'over' : 'ok  ';
    console.log(`  ${worst} ${page.padEnd(4)} ${nm.join(':').padEnd(56)} ${rs[0].target}/${rs[0].red}   ${vals || 'n/a'}`);
  }
  for (const r of over) console.warn(`${r.status === 'RED' ? 'RED' : 'warning'}: budget "${r.name}" ${r.page}/${r.lang} is ${r.kb} KB (target ${r.target}, red ${r.red}); culprit: ${r.culprit}`);
  return { rows, over };
}

import { statSync } from 'node:fs';
function statSize(f) { try { return statSync(f).size; } catch { return 0; } }
