// rt.js: the runtime of compiled design boards. The canvas runtime of the design (support.js) is not in the export; boards use
// a small React-like API (state, props, setState, renderVals, componentDidMount/WillUnmount/DidUpdate), which Preact provides.
//   DCLogic / defineBoard   a board class and its template
//   Lazy                    a board frame filled in when the screen gets near (a page's <dc-import>)
//   href                    design page links -> real addresses of the site (table of the page language)
//   boot                    mounts a page (Main, AppPage) over its pre-rendered markup
import { Component, h, Fragment, render } from 'preact';

export { h, Fragment };

// ---- text, lists ----
// null, undefined and booleans print nothing; sc-for over a missing list renders nothing
export const t = (x) => (x == null || x === false || x === true ? '' : x);
export const l = (x) => (x == null ? [] : x);

// ---- configuration of the page language (the lang bundle): links, css, page addresses ----
const CFG = { links: {}, css: {}, pages: {}, page: '' };
const unmapped = new Set();
if (typeof window !== 'undefined') window.__dcUnmapped = unmapped;

/** a design link -> an address of the site. page: data-page of a protocol button; text/ctx: the Russian text of the link and of its card */
export function href(target, page, text, ctx) {
  const L = CFG.links;
  if (page && L.page && L.page[page]) return L.page[page];
  if (typeof target !== 'string' || !/\.dc\.html/.test(target)) return target;
  const tx = (text || '').replace(/\s+/g, ' ').trim();
  const r = (L.byText && ((ctx && L.byText[tx + '|' + ctx]) || L.byText[tx])) || (L.byPrefix && (L.byPrefix.find((p) => tx.startsWith(p[0])) || [])[1]) || (L.map && L.map[target]);
  if (r) return r;
  unmapped.add(target);
  return target;
}

// ---- scope of a board: its lookups run over its own frame ----
const NONE = { querySelector: () => null, querySelectorAll: () => [], getElementById: () => null };
function hostOf(inst) {
  if (inst._h && inst._h.isConnected) return inst._h;
  let n = inst.base;
  if (n && n.nodeType !== 1) n = n.parentElement;
  const h2 = n && n.closest ? n.closest('[data-board]') : null;
  if (h2) inst._h = h2;
  return h2;
}
function scopeOf(root) {
  return root.__dcScope || (root.__dcScope = {
    querySelector: (s) => root.querySelector(s),
    querySelectorAll: (s) => root.querySelectorAll(s),
    getElementById: (id) => root.querySelector('#' + (window.CSS && CSS.escape ? CSS.escape(id) : id)),
  });
}

export class DCLogic extends Component {
  /** what `document` was in the board's lookups (the compiler rewrites document.querySelector*, getElementById to this) */
  get $d() { const r = hostOf(this); return r ? scopeOf(r) : NONE; }
}

export function defineBoard(Logic, tpl, imports, defaults, name) {
  class Board extends Logic {
    constructor(p) { super(p); (window.__dcBoards = window.__dcBoards || []).push(new WeakRef(this)); } // the stand asks every board for its scope
    render() { return tpl(this.renderVals(), imports); }
  }
  Board.defaultProps = defaults;
  Board.displayName = name;
  return Board;
}

// ---- lazy frames ----
export const registry = { eager: {}, load: {} };
const cssLoaded = new Map();
function addCss(url) {
  if (cssLoaded.has(url)) return cssLoaded.get(url);
  const p = new Promise((ok) => {
    const k = document.createElement('link');
    k.rel = 'stylesheet'; k.href = url; k.onload = k.onerror = () => ok();
    const before = CFG.css.before && document.querySelector('link[rel=stylesheet][href="' + CFG.css.before + '"]');
    if (before) before.parentNode.insertBefore(k, before); else document.head.appendChild(k);
  });
  cssLoaded.set(url, p);
  return p;
}
function cssFor(name) { return /^App/.test(name) ? CFG.css.app || [] : /^Sub/.test(name) ? CFG.css.sub || [] : []; }

// The page of the design lays out its frames (a tour box, a shot: where the board sits in its window) at the start, when every board is
// already there; here a board arrives later, so the page is asked to lay out again once it has arrived (a few times: a board that
// switches its own tab first needs a moment to show what the shot is cut to). The board is translated first: laying out Russian text that is
// about to be replaced would make the browser fetch the Cyrillic fonts for a page in another language.
let refitT = [];
function refit() { refitT.forEach(clearTimeout); refitT = [0, 150, 600, 1500].map((ms) => setTimeout(() => { if (window.MG_I18N && window.MG_I18N.now) window.MG_I18N.now(); const p = window.__dcPage; if (p && p._fit) p._fit(); }, ms)); }

/** <dc-import> of a page: a frame (display: contents) that gets its board when the screen comes near. n: board, e: first screen, p: props */
export class Lazy extends Component {
  constructor(p) { super(p); this.state = { B: (p.e && registry.eager[p.n]) || null }; } // only the frame of the first screen is filled at once, even if the module of another one is loaded
  componentDidMount() {
    if (this.state.B) return;
    const el = this.base, box = el && (el.closest('.pwin-s,.lx-shot,.emb,.lx-app') || el.parentElement);
    if (typeof IntersectionObserver === 'undefined' || !box) { this.fetch(); return; }
    this._io = new IntersectionObserver((es) => { if (es.some((e) => e.isIntersecting)) { this._io.disconnect(); this.fetch(); } }, { rootMargin: '100% 0px' });
    // not before the first frame is out: a board that arrives earlier (its script is run, then laid out) delays what the person is waiting for
    const watch = () => { if (!this._gone) this._io.observe(box); };
    if (typeof requestAnimationFrame === 'function') requestAnimationFrame(() => setTimeout(watch, 0)); else watch();
  }
  componentDidUpdate(_, prev) { if (this.state.B && !prev.B) refit(); }
  componentWillUnmount() { if (this._io) this._io.disconnect(); this._gone = true; }
  fetch() {
    const n = this.props.n, go = registry.load[n];
    if (!go) { console.error('board is not registered: ' + n); return; }
    Promise.all([go(), ...cssFor(n).map(addCss)]).then(([m]) => { if (!this._gone) this.setState({ B: m.default }); });
  }
  render() { const B = this.state.B; return h('div', { 'data-board': this.props.n, 'data-ready': B ? '1' : null }, B ? h(B, this.props.p) : null); }
}

// ---- a page ----
/** the page: the host [data-board=name] holds the pre-rendered markup; the board of the page is rendered over it */
export async function boot(name, Page, eager = {}) {
  const host = document.querySelector('[data-board="' + name + '"]');
  if (!host) throw new Error('no host for ' + name);
  const lang = (host.getAttribute('data-lang') || 'ru'), LG = lang.toUpperCase();
  CFG.page = host.getAttribute('data-page') || name;
  window.MG_LANG = LG; window.__dcLang = LG;
  try { localStorage.setItem('mg-lang', LG); } catch (e) { /* storage is blocked: the patch reads window.MG_LANG */ }
  document.documentElement.setAttribute('data-lg', LG);
  const bundle = host.getAttribute('data-bundle');
  // the language bundle and the boards of the first screen are fetched together
  const [b] = await Promise.all([bundle ? fetch(bundle).then((r) => r.json()) : {}, ...(eager[lang] || eager.all || []).map(async (n) => { registry.eager[n] = (await registry.load[n]()).default; })]);
  Object.assign(CFG, { links: b.links || {}, css: b.css || {}, pages: b.pages || {} });
  if (b.dict && registry.i18n) registry.i18n(b.dict); // the translator of the design (generated, see translate.mjs)
  window.__dcNav = (to) => {
    const u = CFG.pages[CFG.page] && CFG.pages[CFG.page][String(to).toLowerCase()];
    if (!u) return false;
    try { sessionStorage.setItem('mg-scroll', String(window.scrollY)); localStorage.setItem('mg-lang', String(to)); } catch (e) { /* nothing to carry over */ }
    location.assign(u);
    return true;
  };
  try { const y = +sessionStorage.getItem('mg-scroll'); if (y) { sessionStorage.removeItem('mg-scroll'); setTimeout(() => window.scrollTo(0, y), 0); } } catch (e) { /* no storage */ }
  // the boards of the first screen are there before the first render: the pre-rendered markup is adopted as it is
  render(h(Page, { ref: (i) => { window.__dcPage = i; } }), host); // the instance of the page: the stand drives its tours
  if (window.MG_I18N && window.MG_I18N.now) window.MG_I18N.now();
  if (/[?&]prerender\b/.test(location.search)) window.__PRE = host.innerHTML; // build time: the markup of the first render, before any timer
  host.dispatchEvent(new CustomEvent('dc:mounted', { bubbles: true }));
  return host;
}

export const DC = {
  /** plain mount of one board (the stand and the tests) */
  mount(el, Board, props) { if (!el.hasAttribute('data-board')) el.setAttribute('data-board', Board.displayName || 'board'); render(h(Board, props || {}), el); },
  unmount(el) { render(null, el); },
};
