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
