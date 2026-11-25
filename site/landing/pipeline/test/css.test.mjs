import { test } from 'node:test';
import assert from 'node:assert/strict';
import { usage, pruneCss } from '../css.mjs';

const CSS = `
.a{color:red}
.b{color:blue}
.a .b,.c .b{margin:0}
.lx-sw0{x:1}.lx-sw1{x:2}.zz-0{x:3}
.g-camera{x:4}.g-mic{x:6}.h-other{x:5}
.d:not(.e){y:1}
.f[data-x="1"]{y:2}
html[data-lg=FA] .a{z:1}
@media (max-width:600px){.a{w:1}.c{w:2}}
@keyframes spin{to{opacity:0}}
@keyframes unused{to{opacity:1}}
@keyframes byScript{to{opacity:1}}
.a{animation:spin 1s}
@font-face{font-family:X;src:url(x.woff2)}
:root{--k:1}
`;

test('selectors stay when every class of them is used; keyframes by name', () => {
  const u = usage(['<div class="a lx-sw{{i}} d f">x</div><span class="gl g-{{n.g}}"></span><script>icons = [{ g: "camera" }];cls = "lx-sw" + n; el.style.animation = "byScript 1s"</script>']);
  const r = pruneCss(CSS, u).css;
  assert.match(r, /\.a\{color:red\}/);
  assert.doesNotMatch(r, /\.b\{color:blue\}/, 'an unused class goes');
  assert.doesNotMatch(r, /\.c \.b/, 'a rule whose selectors are all unused goes');
  assert.match(r, /\.lx-sw0/); assert.match(r, /\.lx-sw1/); assert.doesNotMatch(r, /\.zz-0/, 'a class assembled by a script is kept by its prefix only');
  assert.match(r, /\.g-camera/, 'a two-letter prefix that ends in a dash counts (class="gl g-{{icon}}")'); assert.doesNotMatch(r, /\.h-other/); assert.doesNotMatch(r, /\.g-mic/, 'the rest of a class assembled from data must occur in the sources');
  assert.match(r, /\.d:not\(\.e\)/, 'a class inside :not() is not demanded');
  assert.match(r, /\.f\[data-x="1"\]/); assert.match(r, /html\[data-lg=FA\] \.a/);
  assert.match(r, /@media[^{]*\{[^}]*\.a\{w:1\}/); assert.doesNotMatch(r, /\.c\{w:2\}/);
  assert.match(r, /@keyframes spin/, 'named by a declaration that stays');
  assert.match(r, /@keyframes byScript/, 'named by a script');
  assert.doesNotMatch(r, /@keyframes unused/);
  assert.doesNotMatch(r, /@font-face/);
  assert.match(r, /:root\{--k:1\}/);
});

test('the names cut away are reported', () => {
  const r = pruneCss('.a{x:1}.b{x:2}', usage(['a']));
  assert.deepEqual([...r.dropped], ['b']);
});

test("a class a script assembles from a one-letter prefix and a number ('s' + step) is kept, a longer rest is not", () => {
  const u = usage(["<div class=\"box\"></div><script>el.className = 'box ' + 's' + Math.min(step, 3)</script>"]);
  const r = pruneCss('.box.s1{a:1}.box.s2{a:2}.box.sx{a:3}.box.t1{a:4}', u).css;
  assert.match(r, /\.s1/); assert.match(r, /\.s2/); assert.doesNotMatch(r, /\.sx/); assert.doesNotMatch(r, /\.t1/);
});

import { renameH1 } from '../css.mjs';
test('renameH1 renames the h1 tag in selectors only and gives the new tag the look of the browser h1', () => {
  const r = renameH1('.pn h1,.pn h2{margin:0}.pn .nd-h1 h1{x:1}.u-h1{y:1}.cS :where(.mg) :where(p,h1,h2){margin:0}.a[data-h1]{z:1}');
  assert.match(r, /^mg-h1\{display:block/);
  assert.match(r, /\.pn mg-h1,\.pn h2\{margin:0\}/);
  assert.match(r, /\.pn \.nd-h1 mg-h1\{x:1\}/);
  assert.match(r, /\.u-h1\{y:1\}/);
  assert.match(r, /:where\(p,mg-h1,h2\)/);
  assert.match(r, /\.a\[data-h1\]/);
});
