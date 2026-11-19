// prerender.mjs: the first render of every page in every language, written down.
// A headless Chrome opens the shell of the page (the document the generator will write, with an empty host), the runtime mounts the
// board of the page over the host and runs the translator once; at that moment, in the same task and before any timer, the markup
// of the host is copied out (rt.js: ?prerender -> window.__PRE). That markup is the fragment: it has no scripts and no handlers (handlers
// are not attributes), the browser adopts it when the same page mounts again.
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { launch, watch, sleep } from './cdp.mjs';
import { serve, renderDocument } from './serve.mjs';

const esc = (s) => String(s).replace(/&/g, '&amp;').replace(/"/g, '&quot;');
const BOARD = { main: 'Main', app: 'AppPage' };
/** the host element of a page: the generator writes the fragment (which starts with it) into <body> as it is */
export const hostOpen = (page, lang, bundle) => `<div data-board="${BOARD[page]}" data-lang="${lang}" data-page="${page}" data-bundle="/${esc(bundle)}">`;

export async function prerender({ build, manifest, bundles, pages, langs, profile }) {
  mkdirSync(join(build, '_pre'), { recursive: true });
  for (const page of pages) for (const lang of langs) writeFileSync(join(build, '_pre', `${page}.${lang}.html`), renderDocument(manifest, page, lang, hostOpen(page, lang, bundles[`${page}:${lang}`]) + '</div>'));
  const web = await serve(build);
  const chrome = await launch({ profile });
  const fragments = {}, report = {};
  try {
    const w = watch(chrome);
    for (const d of ['Page', 'Runtime', 'Log', 'Network']) await chrome.send(d + '.enable');
    await chrome.send('Emulation.setDeviceMetricsOverride', { width: 1440, height: 900, deviceScaleFactor: 1, mobile: false });
    for (const page of pages) for (const lang of langs) {
      w.errors.length = 0;
      await chrome.send('Page.navigate', { url: 'about:blank' });
      await chrome.send('Page.navigate', { url: `http://127.0.0.1:${web.port}/_pre/${page}.${lang}.html?prerender` });
      let html = null;
      for (let i = 0; i < 200 && !html; i++) { await sleep(100); html = await chrome.ev('window.__PRE || null').catch(() => null); }
      const key = `${page}:${lang}`;
      if (!html) throw new Error(`${key}: the page did not mount (no first render in 20 s): ${w.errors.slice(0, 3).join(' | ')}`);
      const facts = await chrome.ev(`(() => { const t = (s) => { const e = document.querySelector(s); return e ? e.textContent.replace(/\\s+/g, ' ').trim() : ''; }; return { h1: t('.hero h1'), lead: t('.hero-s'), html: document.documentElement.getAttribute('data-lg') }; })()`);
      fragments[key] = hostOpen(page, lang, bundles[key]) + html + '</div>';
      report[key] = { ...facts, bytes: Buffer.byteLength(fragments[key]), errors: [...w.errors] };
      if (w.errors.length) console.warn(`prerender ${key}: ${w.errors.length} console errors, first: ${w.errors[0]}`);
    }
  } finally { chrome.close(); web.close(); }
  return { fragments, report };
}
