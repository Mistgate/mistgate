// css.mjs: cut a stylesheet of the design down to what a page can show.
// A selector stays when every class and id in it (outside :not(...)) occurs among the tokens of the sources of the boards of
// the page: templates, scripts (classes that scripts set are string literals there) and the generated markup. Prefixes of
// classes that scripts assemble ('lx-sw' + n, class="x-{{i}}") are kept as prefixes. @keyframes stay when something that
// stays, or a script, names them. Attribute selectors, tags, pseudo-classes and the custom properties are not judged.
import postcss from 'postcss';
import selectorParser from 'postcss-selector-parser';

const TOKEN = /[A-Za-z_][\w-]*/g;

const memo = new Map(); // text -> {tokens: Set, prefixes: Set}: a source is read once, whatever the number of pages and stylesheets
function scanText(s) {
  let r = memo.get(s);
  if (r) return r;
  r = { tokens: new Set(), prefixes: new Set(), short: new Set() };
  s = s.replace(/base64,[A-Za-z0-9+/=]{200,}/g, ""); // an embedded picture is not a class
  for (const m of s.matchAll(TOKEN)) r.tokens.add(m[0]);
  const pre = (x) => { if (x.length >= 3 || /[-_]$/.test(x)) r.prefixes.add(x); else r.short.add(x); }; // 'g-' + icon is a prefix; 'fl' + x is too short to say anything, but 's' + step is the class s1 (digits only, see isUsed)
  for (const m of s.matchAll(/['"`]([A-Za-z][\w-]{0,40})['"`]\s*\+/g)) pre(m[1]); // 'lx-sw' + n
  for (const m of s.matchAll(/\bclass="([^"]*)"/g)) for (const x of m[1].matchAll(/([A-Za-z][\w-]{1,40})\{\{/g)) pre(x[1]); // class="g-{{icon}}"
  for (const m of s.matchAll(/([A-Za-z][\w-]{1,40})\$\{/g)) pre(m[1]); // `x-${i}`
  memo.set(s, r);
  return r;
}
/** tokens of some source text, and the prefixes of the class names it assembles */
export function usage(texts) {
  const tokens = new Set(), prefixes = new Set(), short = new Set();
  for (const s of texts) { const r = scanText(s); r.tokens.forEach((x) => tokens.add(x)); r.prefixes.forEach((x) => prefixes.add(x)); r.short.forEach((x) => short.add(x)); }
  return { tokens, prefixes: [...prefixes], short: [...short] };
}

// a class assembled from a prefix and data ('g-' + icon): kept when the rest is a number or a word that occurs in the sources (the data is there)
const isUsed = (name, u) => u.tokens.has(name) || (u.short || []).some((p) => name.startsWith(p) && /^\d+$/.test(name.slice(p.length))) || u.prefixes.some((p) => { if (!name.startsWith(p)) return false; const rest = name.slice(p.length); return !rest || /^\d+$/.test(rest) || u.tokens.has(rest); });

/** the class and id names a selector demands (those inside :not() and friends with arguments are not demands) */
function demands(sel) {
  const out = [];
  sel.walk((n) => {
    if (n.type !== 'class' && n.type !== 'id') return;
    for (let p = n.parent; p; p = p.parent) if (p.type === 'pseudo') return; // inside :not(), :is(), :where(), :has()
    out.push(n.value);
  });
  return out;
}
const parsed = new Map(); // selector text -> [{text, need}]: the same selectors come back in every stylesheet
function analyze(selector) {
  let a = parsed.get(selector);
  if (!a) { a = selectorParser().astSync(selector).nodes.map((sel) => ({ text: sel.toString().trim(), need: demands(sel) })); parsed.set(selector, a); }
  return a;
}

/** @returns {{css, stats, dropped: Set<string>, kept: Set<string>}} dropped: class names that were cut together with their rules */
export function pruneCss(css, u, { keepKeyframes = () => false } = {}) {
  const root = postcss.parse(css);
  const stats = { rules: 0, kept: 0, bytesIn: css.length };
  const seen = new Set(), kept = new Set();
  const used = new Map();
  const isUsedC = (x) => { let v = used.get(x); if (v === undefined) used.set(x, (v = isUsed(x, u))); return v; };
  const pruneRule = (rule) => {
    stats.rules++;
    const all = analyze(rule.selector);
    all.forEach((s) => s.need.forEach((x) => seen.add(x)));
    const keep = all.filter((s) => s.need.every(isUsedC));
    if (!keep.length) { rule.remove(); return; }
    if (keep.length !== all.length) rule.selector = keep.map((s) => s.text).join(',');
    stats.kept++;
    keep.forEach((s) => s.need.forEach((x) => kept.add(x)));
  };
  const visit = (container) => {
    container.each((n) => {
      if (n.type === 'rule') { if (n.parent.type === 'atrule' && /keyframes$/i.test(n.parent.name)) return; pruneRule(n); }
      else if (n.type === 'atrule') {
        if (/keyframes$/i.test(n.name)) return;
        if (/^(media|supports|container|layer|document)$/i.test(n.name) && n.nodes) { visit(n); if (!n.nodes.length) n.remove(); }
        else if (/^font-face$/i.test(n.name)) n.remove(); // fonts come from fonts.css of the site, not from the design
      }
    });
  };
  visit(root);
  // keyframes: named by a declaration that stayed, or by a script
  const named = new Set();
  root.walkDecls(/^(-webkit-)?animation(-name)?$/i, (d) => { for (const m of d.value.matchAll(TOKEN)) named.add(m[0]); });
  root.walkAtRules(/keyframes$/i, (a) => { if (!named.has(a.params.trim()) && !u.tokens.has(a.params.trim()) && !keepKeyframes(a.params.trim())) a.remove(); });
  const out = root.toString();
  stats.bytesOut = out.length;
  // dropped: names that no source mentions, cut with their rules. A class that is used but whose rule asks for another one nobody uses is not "dropped"
  return { css: out, stats, dropped: new Set([...seen].filter((x) => !kept.has(x) && !isUsedC(x))), kept };
}

/** The headings of the boards inside a page are `<mg-h1>` (compile.mjs): the tag selectors `h1` of the design's stylesheets follow, and the
 *  rule the browser's own stylesheet gave the h1 stays with the new tag. Classes (`.nd-h1`, `.u-h1`) are not tags and are left alone. */
export function renameH1(css, tag = 'mg-h1') {
  const root = postcss.parse(css);
  root.walkRules((r) => { if (!/keyframes$/i.test(r.parent?.name || '')) r.selector = r.selector.replace(/(?<![\w.#\-[:"'=])h1(?![\w-])/g, tag); });
  root.prepend(postcss.rule({ selector: tag, nodes: [postcss.decl({ prop: 'display', value: 'block' }), postcss.decl({ prop: 'font-size', value: '2em' }), postcss.decl({ prop: 'font-weight', value: '700' }), postcss.decl({ prop: 'margin', value: '.67em 0' })] }));
  return root.toString();
}
