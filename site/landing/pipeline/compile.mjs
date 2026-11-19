// compile.mjs: a design board (.dc.html) -> an ES module on top of runtime/rt.js (Preact).
//   template (<x-dc>)      -> a function over renderVals(): {{path}}, <sc-if>, <sc-for>, <dc-import>, handlers, controlled inputs
//   class Component        -> kept as it is, after the compatibility patches (patches.mjs) and the scope rewrite below
// Scope rewrite: boards look themselves up with the global document.querySelector*, getElementById (155 places). With two
// copies of one board on a page only the first would work, so inside a board class `document.<lookup>(` becomes
// `this.$d.<lookup>(`: the same lookup over the root of the board's own frame (see rt.js). A lookup that sits in a plain
// `function` (where `this` is not the board) is not rewritten: the compiler fails, it does not guess.
import { parse } from 'parse5';
import { parse as acornParse } from 'acorn';
import { createHash } from 'node:crypto';
import { applyPatches } from './patches.mjs';

const walk = (n, f) => { f(n); for (const c of n.childNodes || []) walk(c, f); if (n.content) walk(n.content, f); };
const q = JSON.stringify;
const BLOCK = new Set('div section p ul ol li header footer aside nav main article h1 h2 h3 h4 h5 h6 dl dt dd details summary pre svg canvas'.split(' '));
const BOOL = new Set('disabled checked readonly hidden autofocus open selected multiple required'.split(' '));
const RAW = new Set(['pre', 'textarea']);
const HOLE = /\{\{([^}]*)\}\}/g;
const wholeHole = (s) => { const m = s.trim().match(/^\{\{([^}]*)\}\}$/); return m ? m[1] : null; };
/** frames of boards: users do not touch them (CSS: pointer-events none), keyboard focus must not enter them either */
const FRAME = new Set(['emb', 'lx-shot', 'lx-app', 'lx-gw']);
const LOOKUPS = new Set(['querySelector', 'querySelectorAll', 'getElementById']);

/** the literal text of an element (holes and svg are skipped): the Russian label a link is known by in links.json */
function staticText(n) {
  let s = '';
  walk(n, (c) => { if (c.nodeName === '#text' && !/\{\{/.test(c.value)) s += c.value; });
  return s.replace(/\s+/g, ' ').trim();
}
/** the nearest heading (h1-h4) among the children of the ancestors: tells apart links with one label ("Подробнее") */
function headingOf(n) {
  for (let p = n.parentNode; p && p.nodeName !== 'x-dc'; p = p.parentNode) {
    const h = (p.childNodes || []).find((c) => /^h[1-4]$/.test(c.tagName || ''));
    if (h) return staticText(h);
  }
  return '';
}

/** `document.<lookup>` -> `this.$d.<lookup>` inside the class; returns {code, count} */
export function scopeLookups(logic, name) {
  const ast = acornParse(logic, { ecmaVersion: 'latest', sourceType: 'script' });
  const cuts = [];
  const visit = (n, fns, inClass) => {
    if (!n || typeof n.type !== 'string') return;
    if (n.type === 'MemberExpression' && !n.computed && n.object.type === 'Identifier' && n.object.name === 'document' && n.property.type === 'Identifier' && LOOKUPS.has(n.property.name)) {
      const plain = fns.some((f) => f.type !== 'ArrowFunctionExpression' && !(f.type === 'FunctionExpression' && f.isMethod));
      if (!inClass || plain) throw new Error(`${name}: document.${n.property.name} at ${n.start} is outside a board method or inside a plain function: not supported`);
      cuts.push([n.object.start, n.object.end]);
    }
    const isFn = n.type === 'FunctionExpression' || n.type === 'FunctionDeclaration' || n.type === 'ArrowFunctionExpression';
    if (n.type === 'MethodDefinition') n.value.isMethod = true;
    const next = isFn ? [...fns, n] : fns;
    for (const k of Object.keys(n)) {
      if (k === 'type' || k === 'start' || k === 'end') continue;
      const v = n[k];
      if (Array.isArray(v)) v.forEach((c) => visit(c, next, inClass || n.type === 'ClassBody'));
      else if (v && typeof v.type === 'string') visit(v, next, inClass || n.type === 'ClassBody');
    }
  };
  visit(ast, [], false);
  let code = logic;
  for (const [s, e] of cuts.sort((a, b) => b[0] - a[0])) code = code.slice(0, s) + 'this.$d' + code.slice(e);
  return { code, count: cuts.length };
}

/**
 * @param src   the .dc.html text
 * @param name  board name
 * @param opts  { page: the board is a whole page (Main, AppPage): its dc-imports are lazy frames, its document.* is left alone,
 *                blob: (hash)->url for /_blob/ images, patches: false to skip the compatibility patches }
 */
export function compileBoard(src, name, opts = {}) {
  const doc = parse(src);
  let xdc = null, script = null;
  walk(doc, (n) => { if (n.nodeName === 'x-dc') xdc = n; if (n.tagName === 'script' && n.attrs.some((a) => a.name === 'data-dc-script')) script = n; });
  if (!xdc) throw new Error(name + ': no <x-dc>');
  if (!script) throw new Error(name + ': no <script data-dc-script>');
  let logic = script.childNodes.map((c) => c.value).join('');
  const propsJson = (script.attrs.find((a) => a.name === 'data-props') || { value: '{}' }).value;
  const defaults = {};
  for (const [k, v] of Object.entries(JSON.parse(propsJson))) if (v && typeof v === 'object' && 'default' in v) defaults[k] = v.default;

  const patched = opts.patches === false ? { code: logic, applied: [] } : applyPatches(name, logic);
  logic = patched.code;
  const blobUrl = opts.blob || ((h) => '/_blob/' + h);
  const assets = [], blobs = new Set();
  logic = logic.replace(/\/_blob\/([0-9a-f]{32})/g, (_, h) => { blobs.add(h); return blobUrl(h); });
  let scoped = 0;
  if (!opts.page) { const r = scopeLookups(logic, name); logic = r.code; scoped = r.count; }

  // original case of attribute names: parse5 lowercases them on HTML elements (onClick -> onclick, defaultValue -> defaultvalue)
  const raw = src.slice(src.indexOf('<x-dc>'), src.indexOf('</x-dc>'));
  const caseMap = {};
  for (const m of raw.matchAll(/[\s"'}]([a-z][A-Za-z]*[A-Z][A-Za-z]*)=/g)) caseMap[m[1].toLowerCase()] = m[1];

  const imports = new Set(), eager = [], stats = { holes: 0, ifs: 0, fors: 0, imports: 0, scoped, patches: patched.applied.length };
  const pathExpr = (p, scope) => {
    const parts = p.trim().split('.');
    let e = scope.has(parts[0]) ? parts[0] : '$v.' + parts[0];
    for (const s of parts.slice(1)) e += /^\d+$/.test(s) ? `[${s}]` : `?.${s}`;
    return e;
  };
  const interp = (s, scope) => { // string with holes -> JS concat expression
    const out = []; let last = 0;
    for (const m of s.matchAll(HOLE)) { stats.holes++; if (m.index > last) out.push(q(s.slice(last, m.index))); out.push(`$t(${pathExpr(m[1], scope)})`); last = m.index + m[0].length; }
    if (last < s.length) out.push(q(s.slice(last)));
    return ['""', ...out].join('+');
  };
  const strVal = (v, scope) => { const wh = wholeHole(v); return wh != null ? (stats.holes++, pathExpr(wh, scope)) : /\{\{/.test(v) ? interp(v, scope) : q(v); };

  const genAttrs = (n, scope) => {
    const props = [];
    const cls = n.attrs.find((a) => a.name === 'class');
    if (opts.page && cls && !/\{\{/.test(cls.value) && cls.value.split(/\s+/).some((c) => FRAME.has(c))) props.push('inert:true');
    for (const a of n.attrs) {
      if (a.name.startsWith('hint-')) continue;
      let key = caseMap[a.name] || a.name;
      if ((n.tagName === 'input' || n.tagName === 'textarea') && key === 'onChange') key = 'onInput'; // React semantics: onChange fires on every edit
      let val;
      const wh = wholeHole(a.value);
      if (n.tagName === 'a' && a.name === 'href' && (wh != null || /\{\{|\.dc\.html/.test(a.value) || n.attrs.some((x) => x.name === 'data-page'))) {
        // design page links: resolved against the link table of the page language at run time (links.json)
        const dp = n.attrs.find((x) => x.name === 'data-page');
        val = `$H(${strVal(a.value, scope)},${dp ? strVal(dp.value, scope) : 'null'},${q(staticText(n))},${q(headingOf(n))})`;
      } else if (wh != null) { stats.holes++; val = pathExpr(wh, scope); }
      else if (/\{\{/.test(a.value)) val = interp(a.value, scope);
      else if (a.value === '' && BOOL.has(a.name)) val = 'true';
      else if (a.name === 'src' && /^data:/.test(a.value) && a.value.length > 1000) {
        const ext = /^data:image\/svg/.test(a.value) ? 'svg' : (a.value.match(/^data:image\/(\w+)/) || [0, 'bin'])[1];
        const body = a.value.includes(';base64,') ? Buffer.from(a.value.split(';base64,')[1], 'base64') : Buffer.from(decodeURIComponent(a.value.split(',').slice(1).join(',')));
        const file = 'img-' + createHash('sha1').update(body).digest('hex').slice(0, 10) + '.' + ext;
        assets.push([file, body]); val = q('/assets/' + file);
      } else if (/\/_blob\/[0-9a-f]{32}/.test(a.value)) val = q(a.value.replace(/\/_blob\/([0-9a-f]{32})/g, (_, h) => (blobs.add(h), blobUrl(h))));
      else val = q(a.value);
      props.push(`${/^[A-Za-z_$][\w$]*$/.test(key) ? key : q(key)}:${val}`);
    }
    return props;
  };

  const kids = (list, scope, pre) => {
    const out = [];
    const arr = list.filter((c) => c.nodeName !== '#comment');
    arr.forEach((c, i) => {
      if (c.nodeName === '#text') {
        let s = c.value;
        if (!pre) {
          s = s.replace(/\s+/g, ' ');
          if (s === ' ') { // whitespace between nodes: keep only between two inline neighbours
            const blk = (x) => !x || (x.tagName && BLOCK.has(x.tagName));
            if (blk(arr[i - 1]) || blk(arr[i + 1])) return;
          }
        }
        if (!s) return;
        if (!/\{\{/.test(s)) out.push(q(s));
        else if (wholeHole(s) != null && s.trim() === s) { stats.holes++; out.push(pathExpr(wholeHole(s), scope)); }
        else out.push(interp(s, scope)); // one text node per string, like a DOM textContent
      } else if (c.tagName) { const e = elem(c, scope, pre); if (e != null) out.push(e); }
    });
    return out;
  };
  const group = (list) => (list.length === 1 ? list[0] : `[${list.join(',')}]`);

  function elem(n, scope, pre) {
    const t = n.tagName;
    if (t === 'helmet') return null;
    if (t === 'sc-if') {
      stats.ifs++;
      const p = wholeHole(n.attrs.find((a) => a.name === 'value').value);
      if (p == null) throw new Error(name + ': sc-if value is not a plain hole');
      const body = kids(n.childNodes, scope, pre);
      return `(${pathExpr(p, scope)}?${body.length ? group(body) : 'null'}:null)`;
    }
    if (t === 'sc-for') {
      stats.fors++;
      const lst = wholeHole(n.attrs.find((a) => a.name === 'list').value), as = n.attrs.find((a) => a.name === 'as').value;
      const sc = new Set(scope); sc.add(as);
      const body = kids(n.childNodes, sc, pre);
      return `$l(${pathExpr(lst, scope)}).map((${as})=>${body.length ? group(body) : 'null'})`;
    }
    if (t === 'dc-import') {
      stats.imports++;
      const nm = n.attrs.find((a) => a.name === 'name').value; imports.add(nm);
      const props = [];
      for (const a of n.attrs) {
        if (a.name === 'name' || a.name.startsWith('hint-')) continue;
        props.push(`${a.name.replace(/-([a-z])/g, (_, c) => c.toUpperCase())}:${strVal(a.value, scope)}`);
      }
      if (!opts.page) return `$h($C.${nm},{${props.join(',')}})`;
      // a page: the board is a frame filled in later (rt.js Lazy); the first one in document order is on the first screen
      const first = eager.length === 0 && stats.imports === 1;
      if (first) eager.push(nm);
      return `$h($Z,{n:${q(nm)},e:${first ? 1 : 0},p:{${props.join(',')}}})`;
    }
    if (t === 'style' || t === 'link' || t === 'script') throw new Error(name + ': unexpected <' + t + '> in template');
    const props = genAttrs(n, scope);
    const body = kids(n.childNodes, scope, pre || RAW.has(t));
    return `$h(${q(t)},{${props.join(',')}}${body.length ? ',' + body.join(',') : ''})`;
  }

  const roots = kids(xdc.childNodes, new Set(), false);
  const imp = [...imports];
  const staticImports = opts.page ? [] : imp;
  const code = [
    `import { h as $h, Fragment as $F, DCLogic, defineBoard, t as $t, l as $l, Lazy as $Z, href as $H } from '../runtime/rt.js';`,
    ...staticImports.map((i) => `import ${i} from './${i}.js';`),
    `const $C = { ${staticImports.join(', ')} };`,
    `const tpl = ($v) => ${roots.length === 1 ? roots[0] : `$h($F,null,${roots.join(',')})`};`,
    logic,
    `export const defaults = ${JSON.stringify(defaults)};`,
    `export default defineBoard(Component, tpl, $C, defaults, ${q(name)});`,
  ].join('\n');
  return { code, assets, imports: imp, lazy: opts.page ? imp : [], eager, blobs: [...blobs], stats, defaults, patches: patched.applied };
}
