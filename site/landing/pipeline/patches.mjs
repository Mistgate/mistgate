// patches.mjs: the compatibility patches over the scripts of the design boards. Short and explicit.
// Every patch must apply EXACTLY ONCE in every board it targets, and the boards in `required` must carry it: if a
// redesign moves the code, the build fails instead of silently losing the patch.
// The boards themselves are never edited by hand: they come from the design as they are.
export const PATCHES = [
  { id: 'hotkeys', note: 'Ctrl+K / Ctrl+J of the panel boards stay with the browser (the boards on the site are decoration)',
    files: /^Pn/, from: "window.addEventListener('keydown', this._k);", to: '/* site: no global hotkeys */',
    required: ['PnOverview', 'PnNodes', 'PnUsers', 'PnHealth', 'PnReach', 'PnUpdates', 'PnUser', 'PnIntegrations'] },
  { id: 'lang-source', note: 'the language of the page, not of localStorage, decides what a board starts in',
    files: /^(Main|AppPage|Pn)/, from: "localStorage.getItem('mg-lang')", to: "(window.MG_LANG || localStorage.getItem('mg-lang'))",
    required: ['Main', 'AppPage', 'PnOverview', 'PnNodes', 'PnUsers', 'PnHealth', 'PnReach', 'PnUpdates', 'PnUser', 'PnIntegrations'] },
  { id: 'lang-nav', note: 'the language menu opens the page in that language (a page per language) instead of translating in place',
    files: /^(Main|AppPage)$/, from: "setLang(l) { try { localStorage.setItem('mg-lang', l); } catch (e) {}",
    to: "setLang(l) { if (window.__dcNav && window.__dcNav(l)) return; try { localStorage.setItem('mg-lang', l); } catch (e) {}", required: ['Main', 'AppPage'] },
  { id: 'no-veil', note: 'the page is not hidden until the fonts are loaded (the one agreed departure from the design)',
    files: /^(Main|AppPage)$/, from: "bootCls: s.boot ? 'lx-go' : 'lx-boot'", to: "bootCls: 'lx-go'", required: ['Main', 'AppPage'] },
  { id: 'early-fit', note: 'the boxes of the page (tour windows, shots) are laid out in the task that mounts the page, not 400 ms later: the pre-rendered markup has no sizes, and a late layout is a visible jump on a slow device',
    files: /^(Main|AppPage)$/, from: 'this.later(() => this.tourInit(), 400);', to: "if (window.MG_I18N && window.MG_I18N.now) window.MG_I18N.now(); this.tourInit();", required: ['Main', 'AppPage'] }, // translated first: laying out the Russian text the boards render would fetch the Cyrillic fonts for a page in another language
  { id: 'early-fit-only', note: 'of the first layout only the sizes of the boxes are needed at once; the shots (they read the boards, which arrive later) are laid out by the timers and by the arrival of a board',
    files: /^(Main|AppPage)$/, from: 'const both = () => { fit(); shot(); }; both();', to: 'const both = () => { fit(); shot(); }; fit();', required: ['Main', 'AppPage'] },
  { id: 'tours-later', note: 'the tours of the windows start 400 ms after the page, as in the design (the layout of the boxes is early, the show is not)',
    files: /^(Main|AppPage)$/, from: 'boxes.forEach((b) => this._tio.observe(b));', to: 'this.later(() => window.mgLive(() => boxes.forEach((b) => this._tio.observe(b))), 400);', required: ['Main', 'AppPage'] },
  { id: 'live-later', note: 'the canvases of the hero (the field, the watermark) start when the page has loaded and the browser is idle (rt.js mgLive), not in the first tasks',
    files: /^(Main|AppPage)$/, from: 'this.later(() => this.mxInit(), 80);', to: 'this.later(() => window.mgLive(() => this.mxInit()), 80);', required: ['Main', 'AppPage'] },
  { id: 'live-later-wm', note: 'as live-later',
    files: /^(Main|AppPage)$/, from: 'this.later(() => this.wmInit(), 300);', to: 'this.later(() => window.mgLive(() => this.wmInit()), 300);', required: ['Main', 'AppPage'] },
  { id: 'scramble-later', note: 'the numbers of a panel mock-up start to scramble when the page has loaded and the browser is idle, not at the first render (the text is already there: it is the pre-rendered one)',
    files: /^Pn/, from: 'this._scq = setTimeout(this._scrRun, 0);', to: 'this._scq = window.mgLive(this._scrRun);',
    required: ['PnOverview', 'PnNodes', 'PnUsers', 'PnHealth', 'PnReach', 'PnUpdates', 'PnUser', 'PnIntegrations'] },
  { id: 'fit-read-first', note: 'forced reflow: the sizes of all boxes are read before any of them is written (a read after a write of the neighbour laid the page out again, once per box)',
    files: /^(Main|AppPage)$/, from: "boxes.forEach((b) => { const s = b.querySelector('.pwin-s'); if (!s || !b.clientWidth) return;", to: "const cw = boxes.map((b) => b.clientWidth); boxes.forEach((b, bi) => { const s = b.querySelector('.pwin-s'); if (!s || !cw[bi]) return;", required: ['Main', 'AppPage'] },
  { id: 'fit-read-first-k', note: 'the width read by fit-read-first',
    files: /^(Main|AppPage)$/, from: 'k = b.clientWidth / w;', to: 'k = cw[bi] / w;', required: ['Main', 'AppPage'] },
  { id: 'slider-read-first', note: 'forced reflow: the places of the selected tabs are read for every group first, then the variables are written',
    files: /^(Main|AppPage)$/, from: "Array.prototype.forEach.call(root.querySelectorAll(Q), (g) => { const b = g.querySelector(SEL); if (!b || !b.offsetWidth) return; const kids",
    to: "Array.prototype.map.call(root.querySelectorAll(Q), (g) => { const b = g.querySelector(SEL); if (!b || !b.offsetWidth) return null; return { g: g, b: b, L: b.offsetLeft, T: b.offsetTop, W: b.offsetWidth, H: b.offsetHeight }; }).filter(Boolean).forEach((q) => { const g = q.g, b = q.b; const kids", required: ['Main', 'AppPage'] },
  { id: 'slider-read-first-set', note: 'the values read by slider-read-first',
    files: /^(Main|AppPage)$/, from: "g.style.setProperty('--ix', b.offsetLeft + 'px'); g.style.setProperty('--iy', b.offsetTop + 'px'); g.style.setProperty('--iw', b.offsetWidth + 'px'); g.style.setProperty('--ih', b.offsetHeight + 'px');",
    to: "g.style.setProperty('--ix', q.L + 'px'); g.style.setProperty('--iy', q.T + 'px'); g.style.setProperty('--iw', q.W + 'px'); g.style.setProperty('--ih', q.H + 'px');", required: ['Main', 'AppPage'] },
  { id: 'doc-link-show', note: 'the link of a "Панель в деле" slide carries the docs page the design lists for it',
    files: /^Main$/, from: "trDoc: 'Docs.dc.html'", to: "trDoc: 'Docs.dc.html#' + SHOW[s.tr][4]", required: ['Main'] },
  { id: 'doc-link-ai', note: 'the link of an AI prompt carries the anchor the design lists for it',
    files: /^Main$/, from: "aiDoc: 'Docs.dc.html'", to: "aiDoc: 'Docs.dc.html' + AIT[s.ai || 0][4]", required: ['Main'] },
];

const count = (s, sub) => s.split(sub).length - 1;

export function applyPatches(name, logic) {
  const applied = [];
  for (const p of PATCHES) {
    if (!p.files.test(name)) continue;
    const n = count(logic, p.from);
    if (n === 0 && !p.required.includes(name)) continue;
    if (n !== 1) throw new Error(`patch "${p.id}" must apply exactly once in ${name}, found ${n} (the design changed: look at patches.mjs)`);
    logic = logic.replace(p.from, () => p.to);
    applied.push(p.id);
  }
  return { code: logic, applied };
}
