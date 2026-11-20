// check.mjs: the stand. Serves build/ with the CSP of the site, opens the pages in a headless Chrome and checks them.
//   node check.mjs [--pages main,app] [--langs ru,en,fa] [--widths 390,820,1440] [--tours] [--no-shots] [--harvest] [--only nodes,ov]
// Per page, language and width: no console errors and no CSP violations; every board frame mounts when scrolled to; every board
// looks things up in its own frame; the hotkeys of the browser (Ctrl+K, Ctrl+J) stay with the browser; every link is a real address.
// --tours (default on): every step of every tour of the page, played from the tour definitions of the page itself (compressed in time):
// the element of the step must be found. --harvest: also write harvest.json (the Russian strings the screens showed), the input of the
// dictionary subset. Shots of the key frames go to SHOTS (outside the repository).
import { readFileSync, writeFileSync, mkdirSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { tmpdir } from 'node:os';
import { serve } from './serve.mjs';
import { launch, watch, sleep } from './cdp.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const BUILD = process.env.MG_BUILD || join(HERE, '..', 'build');
const SHOTS = process.env.MG_SHOTS || join(tmpdir(), 'mistgate-landing-shots'); // outside the repository
const args = process.argv.slice(2);
const opt = (k, d) => { const i = args.indexOf(k); return i >= 0 ? args[i + 1] : d; };
const PAGES = opt('--pages', 'main,app').split(','), LANGS = opt('--langs', 'ru,en,fa').split(','), WIDTHS = opt('--widths', '390,820,1440').split(',').map(Number);
const HARVEST = args.includes('--harvest'), TOURS = !args.includes('--no-tours'), SHOT = !args.includes('--no-shots'), ONLY = opt('--only', '') ? opt('--only').split(',') : null;
const manifest = JSON.parse(readFileSync(join(BUILD, 'manifest.json'), 'utf8'));
const path = (page, lang) => (lang === 'en' ? '' : '/' + lang) + (page === 'app' ? '/app/' : '/');

// ---- page side: helpers installed into every document ----
const PAGE_JS = String.raw`(() => {
  window.__csp = []; window.__FULLWAIT = ${process.env.FULLWAIT ? 'true' : 'false'};
  // the page's own tour engine stays still while the smoke drives the tours: its kick is switched off the moment the page instance exists
  Object.defineProperty(window, "__dcPage", { configurable: true, set(v) { if (v) v.tourKick = () => {}; window.__dcPageV = v; }, get() { return window.__dcPageV; } }); document.addEventListener('securitypolicyviolation', (e) => window.__csp.push(e.violatedDirective + ' ' + e.blockedURI));
  const CYR = /[А-Яа-яЁё]/, SKIP = '.demo,.an-demo,[data-app],script,style,[data-noi18n]';
  const seen = new Set();
  // Russian text left on screen (outside the zones the translator skips): text nodes and attributes
  window.__cyr = (root, bag) => {
    const out = bag || new Set(), w = document.createTreeWalker(root || document.body, NodeFilter.SHOW_TEXT); let n;
    while ((n = w.nextNode())) { const v = n.nodeValue.trim(); if (v && CYR.test(v) && !n.parentElement.closest(SKIP)) out.add(v); }
    (root || document.body).querySelectorAll('[placeholder],[aria-label],[title],[data-l],[data-scr]').forEach((el) => { if (el.closest(SKIP)) return; ['placeholder', 'aria-label', 'title', 'data-l', 'data-scr'].forEach((a) => { const v = el.getAttribute(a); if (v && CYR.test(v)) out.add(v); }); });
    return out;
  };
  // --harvest: every state control of the page, one after the other (the screens put new Russian text on screen with each)
  window.__sweep = async () => {
    let n = 0;
    for (let pass = 0; pass < 3; pass++) {
      const list = [...document.querySelectorAll('[aria-selected],[aria-pressed],.stp,.lx-aib')].filter((e) => !e.closest('.lx-lg,.langs,.lx-lgm,.lx-mn'));
      for (const e of list) {
        try { e.click(); } catch (er) { /* a state the board cannot draw */ }
        await sleep(80); await window.__grab(); n++;
      }
    }
    const x = document.querySelector('.lx-aix'); if (x) x.click();
    return n;
  };
  // the nodes board (PnNodes) with 0 and with 1 node: every state control of the mock-up in turn, then a "paths" state of a bigger fleet carried
  // over to the small one. The exceptions are caught by the stand's watcher; here a board that went blank counts too.
  window.__fleet = async () => {
    const f = document.querySelector('[data-board="PnNodes"]');
    if (!f || !f.querySelector('.demo')) return JSON.stringify({ n: 0, bad: ['no PnNodes board with its state controls on the page'] });
    f.scrollIntoView({ block: 'center' }); await sleep(400);
    const groups = () => [...f.querySelector('.demo').querySelectorAll('.dl')].map((l) => ({ g: l.textContent.trim(), bs: [...l.nextElementSibling.querySelectorAll('button')] }));
    const press = async (g, t) => { const b = (groups().find((x) => x.g === g) || { bs: [] }).bs.find((x) => x.textContent.trim() === t); if (!b) return false; b.click(); await sleep(120); await window.__grab(); return true; };
    const names = groups().map((x) => [x.g, x.bs.map((b) => b.textContent.trim())]), bad = [];
    let n = 0;
    const blank = (what) => { n++; if (f.innerText.length < 300) bad.push(what + ': the board is empty'); };
    for (const fl of ['0', '1']) {
      if (!(await press('Ноды', fl))) { bad.push('no fleet button ' + fl); continue; }
      for (const [g, ts] of names) if (g !== 'Ноды') for (const t of ts) { await press(g, t); blank('fleet ' + fl + ', ' + g + ' = ' + t); }
    }
    for (const t of (names.find((x) => x[0] === 'Пути') || ['', []])[1]) for (const fl of ['0', '1']) { await press('Ноды', '4'); await press('Пути', t); await press('Ноды', fl); blank('state ' + t + ' carried to fleet ' + fl); }
    await press('Ноды', '4');
    return JSON.stringify({ n, bad: bad.slice(0, 5) });
  };
  window.__bag = new Set();
  window.__classes = new Set();
  // the translator runs a frame after the DOM changes: a Russian string counts as left over only if it is still there 150 ms later (one in transit does not)
  window.__grab = async () => {
    const now = new Set(); window.__cyr(document.body, now); let keep = now;
    if (window.MG_LANG !== 'RU' && now.size) { await sleep(150); const again = new Set(); window.__cyr(document.body, again); keep = new Set([...now].filter((x) => again.has(x))); } // a string the translator has not reached yet is not left over
    keep.forEach((x) => window.__bag.add(x));
    document.querySelectorAll('[class]').forEach((e) => { if (e.classList) e.classList.forEach((c) => window.__classes.add(c)); });
  };
  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
  window.__sleep = sleep;
  const src = (e) => ((window.MG_I18N && window.MG_I18N.ru ? window.MG_I18N.ru(e) : e.textContent) || '').trim();
  const desc = (sp) => (sp.c || sp.until) + (sp.t ? ' ' + sp.t : '') + (sp.n ? ' #' + sp.n : '') + (sp.type ? ' type=' + sp.type : '');
  // one tour, step by step, the way tourStep() of the page looks for its element (waits are cut short)
  window.__smoke = async (box, steps, harvest) => {
    const rows = []; let ok = true;
    for (let i = 0; i < steps.length; i++) {
      const sp = steps[i], find = () => { const list = Array.prototype.slice.call(box.querySelectorAll(sp.c || sp.until)).filter((e) => sp.silent || e.getClientRects().length); return sp.t ? list.filter((e) => sp.t.test(src(e)))[sp.n || 0] : list[sp.n || 0]; };
      if (sp.pc && box.classList.contains('ph')) { rows.push({ i, d: 'skipped in the phone view', ok: true }); continue; }
      if (sp.g != null) continue;
      if (sp.w) { await sleep(window.__FULLWAIT ? sp.w : Math.min(Math.round(sp.w * 0.3), 1500)); continue; }
      if (sp.until) { const t0 = Date.now(); let e; while (!(e = find()) && Date.now() - t0 < (sp.max || 8000)) await sleep(100); rows.push({ i, d: 'until ' + desc(sp), ok: !!e, note: e ? '' : 'did not appear in ' + (sp.max || 8000) + ' ms; the board shows: ' + box.textContent.replace(/\s+/g, ' ').trim().slice(0, 160) }); if (!e) { ok = false; break; } continue; }
      const el = find();
      if (!el) { if (sp.opt) { rows.push({ i, d: 'optional ' + desc(sp), ok: true, note: 'absent' }); continue; } rows.push({ i, d: desc(sp), ok: false, note: 'element not found' }); ok = false; break; }
      // a person's cursor takes a second to arrive: a button that is still aria-disabled (a check in progress) is waited for, up to 4 s
      for (let k = 0; k < 40 && el.getAttribute('aria-disabled') === 'true'; k++) await sleep(100);
      if (sp.type) { try { el.focus({ preventScroll: true }); const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, 'value').set; for (let k = 1; k <= sp.type.length; k++) { set.call(el, sp.type.slice(0, k)); el.dispatchEvent(new Event('input', { bubbles: true })); } el.blur(); } catch (e) { rows.push({ i, d: desc(sp), ok: false, note: 'typing failed: ' + e.message }); ok = false; break; } }
      else el.click();
      rows.push({ i, d: desc(sp), ok: true, aw: (box.querySelector('.an-w') || {}).textContent ? box.querySelector('.an-w').textContent.replace(/\s+/g, ' ').slice(40, 100) : '-' });
      await sleep(140);
      if (harvest) await window.__grab();
    }
    return { ok, rows };
  };
})();`;

const sections = ['top', 'tour', 'features', 'link', 'app', 'protocols', 'compare', 'install'];
const report = { runs: [], tours: [], boards: [], errors: [], cyr: {}, summary: {} };
const harvestOut = { designVersion: manifest.designVersion, strings: {} };
const web = await serve(BUILD);
const chrome = await launch({ profile: join(HERE, '.profile') });
const w = watch(chrome);
let failed = 0;
const fail = (m) => { failed++; console.log('  FAIL ' + m); };
try {
  for (const d of ['Page', 'Runtime', 'Log', 'Network', 'Audits']) await chrome.send(d + '.enable').catch(() => {});
  await chrome.send('Page.addScriptToEvaluateOnNewDocument', { source: PAGE_JS });
  mkdirSync(SHOTS, { recursive: true });
  for (const page of PAGES) for (const lang of LANGS) for (const width of WIDTHS) {
    const key = `${page}-${lang}-${width}`, run = { key, errors: [], boards: {}, tours: [] };
    console.log('== ' + key);
    w.errors.length = 0; w.reqs.length = 0;
    const height = width < 500 ? 844 : width < 1000 ? 1024 : 900;
    await chrome.send('Emulation.setDeviceMetricsOverride', { width, height, deviceScaleFactor: 1, mobile: width < 500 });
    await chrome.send('Page.navigate', { url: 'about:blank' }); await sleep(150);
    await chrome.send('Page.navigate', { url: `http://127.0.0.1:${web.port}${path(page, lang)}` });
    let up = false;
    for (let i = 0; i < 100 && !up; i++) { await sleep(150); up = await chrome.ev('!!(window.__dcPage && document.querySelector("[data-board][data-ready]"))').catch(() => false); }
    if (!up) { fail(`${key}: the page did not come up`); run.errors.push(...w.errors); report.runs.push(run); continue; }
    await sleep(900);
    // 1. every frame mounts when it is scrolled to
    const total = await chrome.ev(`(() => { const h = document.documentElement.scrollHeight; return h; })()`);
    for (let y = 0; y < total + 800; y += Math.round(height * 0.7)) { await chrome.ev(`window.scrollTo(0, ${y})`); await sleep(260); }
    await sleep(900);
    const frames = await chrome.ev(`[...document.querySelectorAll('[data-board]')].filter((e) => e.parentElement.closest('[data-board]')).map((e) => ({ n: e.getAttribute('data-board'), ready: e.hasAttribute('data-ready') && e.getAttribute('data-ready') === '1' }))`);
    run.frames = frames.length; run.notReady = frames.filter((f) => !f.ready).map((f) => f.n);
    if (run.notReady.length) fail(`${key}: boards that did not mount: ${run.notReady.join(', ')} (${frames.length - run.notReady.length}/${frames.length})`);
    else { const cnt = {}; frames.forEach((f) => { cnt[f.n] = (cnt[f.n] || 0) + 1; }); run.twice = Object.entries(cnt).filter(([, n]) => n > 1).map(([n, c]) => n + "x" + c); console.log(`  boards mounted: ${frames.length}/${frames.length}` + (run.twice.length ? `; on the page more than once: ${run.twice.join(", ")}` : "")); }
    // 2. scope: every board looks things up inside its own frame
    const scope = await chrome.ev(`(() => { const bad = []; let n = 0; for (const r of window.__dcBoards || []) { const b = r.deref(); if (!b || !b.base || !b.base.isConnected) continue; n++; const host = (b.base.nodeType === 1 ? b.base : b.base.parentElement).closest('[data-board]'); const probe = b.$d.querySelector('[class]'); if (probe && probe.closest('[data-board]') !== host) bad.push(b.constructor.displayName); } return { n, bad }; })()`);
    run.scope = scope;
    if (scope.bad.length) fail(`${key}: boards that look outside their frame: ${scope.bad.join(', ')}`); else console.log(`  scope: ${scope.n} board instances look only inside their own frame`);
    // 3. the hotkeys of the browser
    const keys = await chrome.ev(`(() => ['k', 'j'].map((k) => { const e = new KeyboardEvent('keydown', { key: k, ctrlKey: true, cancelable: true, bubbles: true }); window.dispatchEvent(e); return e.defaultPrevented; }))()`);
    if (keys.some(Boolean)) fail(`${key}: Ctrl+K / Ctrl+J are taken from the browser`);
    // 4. links
    const links = await chrome.ev(`({ unmapped: [...window.__dcUnmapped], dc: [...document.querySelectorAll('a[href]')].map((a) => a.getAttribute('href')).filter((h) => /\\.dc\\.html/.test(h)) })`);
    if (links.unmapped.length || links.dc.length) fail(`${key}: links of the design left: ${[...links.unmapped, ...links.dc].join(', ')}`);
    // 5. shots of the key frames
    if (SHOT) for (const s of sections) {
      const sel = s === 'top' ? null : '#' + s;
      await chrome.ev(sel ? `(() => { const e = document.querySelector(${JSON.stringify(sel)}); if (e) window.scrollTo(0, window.scrollY + e.getBoundingClientRect().top - 8); })()` : 'window.scrollTo(0, 0)');
      await sleep(1100);
      await chrome.shot(join(SHOTS, `${key}-${s}.png`));
    }
    // 6. tours
    if (TOURS) await smokeTours(page, lang, width, key, run);
    // 6b. the nodes board with no fleet and with one node (every state control of the mock-up in each), and with a state left over from a bigger fleet
    if (page === 'main' && width === 1440 && (lang === 'ru' || lang === 'en')) {
      const e0 = w.errors.length, r = JSON.parse(await chrome.ev('window.__fleet()').catch((e) => JSON.stringify({ n: 0, bad: ['eval: ' + e.message.slice(0, 120)] })));
      const thrown = w.errors.splice(e0).filter((x) => /^exception/.test(x));
      if (thrown.length || r.bad.length) fail(`${key}: the nodes board breaks with 0 or 1 nodes: ${[...r.bad, ...thrown.slice(0, 2).map((x) => x.replace(/\s+/g, ' ').slice(0, 140))].join('; ')}`);
      else console.log(`  nodes board with 0 and 1 nodes: ${r.n} states drawn, no errors`);
    }
    if (HARVEST && lang === 'ru') { const n = await chrome.ev('window.__sweep()').catch((e) => { console.log('  sweep: ' + e.message.slice(0, 120)); return 0; }); console.log(`  swept ${n} state controls`); }
    // 7. Russian left on screen (non-ru), the strings for the harvest (ru)
    await chrome.ev('window.__grab()');
    const bag = await chrome.ev('[...window.__bag]');
    if (lang === 'ru') { (harvestOut.strings[page] ||= new Set()); bag.forEach((s) => harvestOut.strings[page].add(s)); }
    else { run.cyr = bag; (report.cyr[`${page}:${lang}`] ||= new Set()); bag.forEach((s) => report.cyr[`${page}:${lang}`].add(s)); if (bag.length) console.log(`  untranslated: ${bag.length} strings`); }
    // classes on screen whose rules the css pruning cut away
    const cut = JSON.parse(readFileSync(join(BUILD, 'report.json'), 'utf8')).prunedClasses[page]?.dropped || [];
    const onScreen = new Set(await chrome.ev('[...window.__classes]'));
    run.cutButUsed = cut.filter((x) => onScreen.has(x));
    if (run.cutButUsed.length) fail(`${key}: classes on screen whose css was cut: ${run.cutButUsed.slice(0, 8).join(', ')}`);
    const csp = await chrome.ev('window.__csp');
    run.errors.push(...w.errors, ...csp.map((x) => 'csp: ' + x));
    const errs = run.errors.filter((e) => !/favicon/.test(e));
    if (errs.length) fail(`${key}: ${errs.length} console errors / CSP violations, first: ${errs[0]}`); else console.log('  console clean, CSP clean');
    // 8. the language menu opens the page in that language
    if (lang === "en" && width === 1440) {
      await chrome.ev("window.scrollTo(0, 0)");
      await chrome.ev("document.querySelector('.lx-lg > button').click()"); await sleep(250);
      await chrome.ev("[...document.querySelectorAll('.lx-lgm button')].find((b) => /Русский/.test(b.textContent)).click()").catch(() => {});
      await sleep(1500);
      const at = await chrome.ev("location.pathname").catch(() => "?"), want = page === "app" ? "/ru/app/" : "/ru/";
      if (at !== want) fail(`${key}: the language menu led to ${at}, not to ${want}`); else console.log("  language menu: en -> " + at);
    }
    report.runs.push(run);
  }
} finally { chrome.close(); web.close(); }

async function waitBoard(sel, ms = 20000) {
  for (let t = 0; t < ms; t += 200) { if (await chrome.ev(`(() => { const b = document.querySelector(${JSON.stringify(sel)}); return !!(b && b.querySelector('[data-board][data-ready="1"] > *')); })()`)) return true; await sleep(200); }
  return false;
}
async function playTour(page, lang, width, run, name, sel, prep) {
  const label = `${page}/${lang}/${width} ${name}`;
  if (ONLY && !ONLY.includes(name)) return;
  const t0 = Date.now();
  try {
    if (prep) await chrome.ev(prep);
    await sleep(500);
    // the page's own engine must not run beside the smoke: a tab switch re-arms it
    await chrome.ev(`(() => { const lg = window.__dcPage; Object.values(lg._tr || {}).forEach((s) => { s.hold = true; clearTimeout(s.tm); clearTimeout(s.back); }); })()`);
    await chrome.ev(`document.querySelector(${JSON.stringify(sel)}).scrollIntoView({ block: 'center' })`);
    if (!(await waitBoard(sel))) { fail(`${label}: the board did not mount`); run.tours.push({ name, ok: false, note: 'board not mounted' }); report.tours.push({ page, lang, width, name, ok: false, failedAt: 'board not mounted' }); return; }
    await sleep(400);
    const r = await chrome.ev(`(async () => { const lg = window.__dcPage, box = document.querySelector(${JSON.stringify(sel)}), k = box.getAttribute('data-tour'); const steps = lg.tourDefs()[k] || []; const res = await window.__smoke(box, steps, true); res.n = steps.filter((s) => !s.w && s.g == null).length; return res; })()`);
    const bad = r.rows.find((x) => !x.ok); if (process.env.DEBUG) console.log(JSON.stringify(r.rows.map((x) => [x.i, x.d.slice(0, 40), x.aw || x.note || '']), null, 0));
    run.tours.push({ name, ok: r.ok, steps: r.rows.length });
    report.tours.push({ page, lang, width, name, ok: r.ok, steps: r.rows.length, failedAt: bad ? `step ${bad.i}: ${bad.d}${bad.note ? ' (' + bad.note + ')' : ''}` : null, ms: Date.now() - t0 });
    if (!r.ok) fail(`${label}: step ${bad.i} ${bad.d}: ${bad.note}`); else console.log(`  tour ${name}: ${r.rows.length} steps ok (${((Date.now() - t0) / 1000).toFixed(1)} s)`);
  } catch (e) { fail(`${label}: ${e.message.slice(0, 200)}`); report.tours.push({ page, lang, width, name, ok: false, failedAt: 'error: ' + e.message.slice(0, 200) }); }
}
async function smokeTours(page, lang, width, key, run) {
  const tab = (group, i) => `(() => { const g = document.querySelectorAll(${JSON.stringify(group)})[0]; g.querySelectorAll('.lx-tab')[${i}].click(); })()`;
  if (page === 'main') {
    await playTour(page, lang, width, run, 'nodes', '[data-tour="nodes"]');
    for (const [i, k] of ['ov', 'nd', 'us', 'hl', 'rc', 'up'].entries()) await playTour(page, lang, width, run, k, '.emb.tr-box', `document.querySelector('.lx-tabs[aria-label]').querySelectorAll('.lx-tab')[${i}].click()`);
    await playTour(page, lang, width, run, 'sub', '[data-tour="sub"]');
    for (const [i, k] of ['Main', 'Routes', 'Diag', 'Set', 'Byp'].entries()) await playTour(page, lang, width, run, 'app:' + k, '.lx-app[data-tour="app"]', `document.querySelector('.lx-atabs').querySelectorAll('.lx-tab')[${i}].click()`);
  } else {
    for (const [i, k] of ['Main', 'Routes', 'Diag', 'Set', 'Byp'].entries()) await playTour(page, lang, width, run, 'app:' + k, '.lx-app[data-tour="app"]', `(() => { const lg = window.__dcPage; lg.setAs ? lg.setAs(${JSON.stringify(k)}) : 0; })()`);
    for (let i = 0; i < 5; i++) await playTour(page, lang, width, run, 'ga:' + i, '.lx-app[data-tour="ga"]', `window.__dcPage.setG('A', ${i})`);
    for (let i = 0; i < 4; i++) await playTour(page, lang, width, run, 'gb:' + i, '.lx-app[data-tour="gb"]', `window.__dcPage.setG('B', ${i})`);
  }
}

const names = [...new Set(report.tours.map((t) => t.page + ':' + t.name))];
const cols = [...new Set(report.tours.map((t) => t.lang + '/' + t.width))];
if (names.length) {
  console.log('\ntours (steps checked; "-" = not run, FAIL = a step did not find its element):');
  console.log('  ' + 'tour'.padEnd(16) + cols.map((c) => c.padEnd(9)).join(''));
  for (const nm of names) console.log('  ' + nm.padEnd(16) + cols.map((c) => { const t = report.tours.find((x) => x.page + ':' + x.name === nm && x.lang + '/' + x.width === c); return (t ? (t.ok ? t.steps + ' ok' : 'FAIL') : '-').padEnd(9); }).join(''));
  for (const t of report.tours.filter((x) => !x.ok)) console.log('  FAIL ' + t.page + ':' + t.name + ' ' + t.lang + '/' + t.width + ': ' + t.failedAt);
}
report.summary = { failed, runs: report.runs.length };
const ser = (o) => JSON.parse(JSON.stringify(o, (k, v) => (v instanceof Set ? [...v].sort() : v)));
writeFileSync(join(BUILD, 'check.json'), JSON.stringify(ser(report), null, 1));
if (HARVEST) { writeFileSync(join(HERE, 'harvest.json'), JSON.stringify(ser(harvestOut), null, 1)); console.log('harvest.json written'); }
console.log(`\n${failed ? failed + ' FAILED' : 'all checks passed'} in ${report.runs.length} runs; report: build/check.json`);
process.exit(failed ? 1 : 0);
