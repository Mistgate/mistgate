import { test } from 'node:test';
import assert from 'node:assert/strict';
import { compileBoard, scopeLookups } from '../compile.mjs';
import { applyPatches, PATCHES } from '../patches.mjs';

const board = (tpl, logic, props = '{}') => `<x-dc>${tpl}</x-dc><script type="text/x-dc" data-dc-script data-props='${props}'>${logic}</script>`;
const LOGIC = 'class Component extends DCLogic { renderVals() { return { a: 1 }; } }';

test('holes, sc-if and sc-for become a template function', () => {
  const r = compileBoard(board('<div class="x {{c}}"><sc-if value="{{on}}"><b>{{t}}</b></sc-if><sc-for list="{{rows}}" as="r"><i>{{r.n}}</i></sc-for></div>', LOGIC), 'T', { patches: false });
  assert.match(r.code, /\$l\(\$v\.rows\)\.map\(\(r\)=>/);
  assert.match(r.code, /\(\$v\.on\?/);
  assert.equal(r.stats.ifs, 1); assert.equal(r.stats.fors, 1);
});

test('onChange of a text field is onInput, as in React', () => {
  const r = compileBoard(board('<input value="{{v}}" onChange="{{set}}">', LOGIC), 'T', { patches: false });
  assert.match(r.code, /onInput:\$v\.set/);
});

test('a page turns <dc-import> into lazy frames and the first one is the eager one', () => {
  const r = compileBoard(board('<div><dc-import name="A" accent="mint"></dc-import><sc-if value="{{x}}"><dc-import name="B"></dc-import></sc-if></div>', LOGIC), 'P', { page: true, patches: false });
  assert.deepEqual(r.eager, ['A']); assert.deepEqual(r.lazy, ['A', 'B']);
  assert.match(r.code, /\$Z,\{n:"A",e:1,p:\{accent:"mint"\}\}/);
  assert.match(r.code, /\$Z,\{n:"B",e:0/);
  assert.doesNotMatch(r.code, /^import A /m);
});

test('a board imports its nested boards statically', () => {
  const r = compileBoard(board('<div><dc-import name="A" on-close="{{close}}"></dc-import></div>', LOGIC), 'N', { patches: false });
  assert.match(r.code, /import A from '\.\/A\.js'/);
  assert.match(r.code, /\$C\.A,\{onClose:\$v\.close\}/);
});

test('links to design pages go through the link table, with the text and the card heading', () => {
  const r = compileBoard(board('<article><h3>Card</h3><a href="Docs.dc.html">More</a><a href="https://x.example/">out</a><a href="{{d}}">dyn</a><a href="#top" data-page="{{k}}">pg</a></article>', LOGIC), 'L', { page: true, patches: false });
  assert.match(r.code, /\$H\("Docs\.dc\.html",null,"More","Card"\)/);
  assert.match(r.code, /href:"https:\/\/x\.example\/"/);
  assert.match(r.code, /\$H\(\$v\.d,null,"dyn","Card"\)/);
  assert.match(r.code, /\$H\("#top",\$v\.k,"pg","Card"\)/);
});

test('frames of boards are inert on a page', () => {
  const r = compileBoard(board('<div class="emb pwin" aria-hidden="true"></div><div class="lx-shot"></div><div class="card"></div>', LOGIC), 'F', { page: true, patches: false });
  assert.equal((r.code.match(/inert:true/g) || []).length, 2);
});

test('document lookups of a board are scoped to its frame; plain functions are refused', () => {
  const ok = scopeLookups('class Component { a() { const f = () => document.querySelector(".x"); return document.getElementById("i"); } }', 'B');
  assert.equal(ok.count, 2); assert.match(ok.code, /this\.\$d\.querySelector\("\.x"\)/); assert.match(ok.code, /this\.\$d\.getElementById/);
  assert.throws(() => scopeLookups('class Component { a() { return [1].map(function () { return document.querySelector(".x"); }); } }', 'B'), /not supported/);
  assert.throws(() => scopeLookups('const q = document.querySelector(".x");', 'B'), /not supported/);
  assert.equal(scopeLookups('class Component { a() { return document.createElement("i"); } }', 'B').count, 0, 'only the lookups are rewritten');
});

test('a patch must apply exactly once', () => {
  const p = PATCHES.find((x) => x.id === 'no-veil'), all = PATCHES.filter((x) => x.files.test('Main')).map((x) => x.from).join(' ; ');
  assert.ok(applyPatches('Main', all).applied.includes('no-veil'));
  assert.throws(() => applyPatches('Main', all + ' ' + p.from), /exactly once/);
  assert.throws(() => applyPatches('Main', 'nothing here'), /exactly once/, 'a required board without the code fails the build');
  assert.deepEqual(applyPatches('PnSearch', 'no hotkeys here').applied, [], 'a board that is not required may lack it');
});

test('/_blob/ images go through the resolver', () => {
  const h = 'a'.repeat(32);
  const r = compileBoard(board(`<img src="/_blob/${h}" alt="">`, `class Component extends DCLogic { renderVals() { return { f: '/_blob/${h}' }; } }`), 'I', { patches: false, blob: (x) => '/assets/' + x.slice(0, 4) + '.svg' });
  assert.match(r.code, /src:"\/assets\/aaaa\.svg"/); assert.match(r.code, /f: '\/assets\/aaaa\.svg'/);
});
