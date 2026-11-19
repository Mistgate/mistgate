// The output of the pipeline against the contract (plans/site-contract.md): run `pnpm build` first; without build/ these tests skip.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync, readdirSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parseFragment } from 'parse5';

const BUILD = process.env.MG_BUILD || join(dirname(fileURLToPath(import.meta.url)), '..', '..', 'build');
const have = existsSync(join(BUILD, 'manifest.json')) && existsSync(join(BUILD, 'pages', 'main.en.html'));
const manifest = have ? JSON.parse(readFileSync(join(BUILD, 'manifest.json'), 'utf8')) : null;
const page = (p, l) => readFileSync(join(BUILD, manifest.pages[p][l].body), 'utf8');
const each = (f) => { for (const p of Object.keys(manifest.pages)) for (const l of manifest.langs) f(p, l, manifest.pages[p][l]); };
const HASHED = /^assets\/(?:[\w-]+\.[\w-]{6,}|c-[\w-]{6,})\.[a-z0-9]+$/;
/** Russian words left on screen: text and attributes outside the zones the design's translator skips (the mock-up state controls .demo, [data-app], [data-noi18n]) */
function russianLeft(html) {
  let n = 0;
  const skip = (e) => (e.attrs || []).some((a) => (a.name === 'class' && /(^|\s)(demo|an-demo)(\s|$)/.test(a.value)) || a.name === 'data-app' || a.name === 'data-noi18n');
  const words = (v) => (v.match(/[А-Яа-яЁё]{3,}/g) || []).length;
  const walk = (node) => {
    if (node.nodeName === '#text') n += words(node.value);
    if (node.tagName) { if (skip(node)) return; for (const a of node.attrs) if (['placeholder', 'aria-label', 'title', 'data-l'].includes(a.name)) n += words(a.value); }
    for (const c of node.childNodes || []) walk(c);
  };
  walk(parseFragment(html));
  return n;
}
const flag = (re, s) => (re.test(s) ? 'yes' : 'no'); // a failing assertion must not print a whole page

test('the manifest has what the generator needs, for every page and language', { skip: !have }, () => {
  assert.equal(manifest.v, 1); assert.ok(manifest.designVersion);
  assert.deepEqual(manifest.langs.slice().sort(), ['en', 'fa', 'ru', 'tr', 'zh']);
  assert.deepEqual(Object.keys(manifest.pages).sort(), ['app', 'main']);
  each((p, l, m) => {
    for (const k of ['body', 'title', 'description', 'css', 'modules', 'preload', 'dir']) assert.ok(k in m, `${p}/${l}: ${k}`);
    assert.equal(m.dir, l === 'fa' ? 'rtl' : 'ltr');
    assert.ok(m.title && m.description, `${p}/${l}: title and description (${m.todo ? 'marked todo' : 'missing'})`);
    assert.ok(m.modules.length >= 1);
    assert.equal(m.htmlAttrs['data-lg'], l.toUpperCase());
    assert.equal(m.fetch.length, 1); assert.ok(m.fetch[0].startsWith('assets/lang-'));
  });
});

test('every file the manifest names exists, carries a hash and is listed in assets', { skip: !have }, () => {
  const listed = new Set(manifest.assets);
  each((p, l, m) => {
    assert.ok(existsSync(join(BUILD, m.body)), m.body);
    for (const f of [...m.css, ...m.modules, ...m.preload, ...(m.fonts || [])]) {
      assert.ok(existsSync(join(BUILD, f)), f + ' exists'); assert.ok(listed.has(f), f + ' is in assets');
      assert.ok(HASHED.test(f), f + ' has a hash in the name');
    }
  });
  for (const f of readdirSync(join(BUILD, 'assets'))) assert.ok(listed.has('assets/' + f), f + ' is listed');
});

test('a fragment is the translated first screen: no inline script, no inline handler, no veil, real addresses', { skip: !have }, () => {
  each((p, l) => {
    const h = page(p, l), at = `${p}/${l}`;
    assert.ok(h.startsWith('<div data-board='), `${at}: starts with the host of the page`);
    assert.equal(flag(/<script|<html[\s>]|<body[\s>]|<head[\s>]/i, h), 'no', `${at}: a document tag or a script in the fragment`);
    assert.equal(flag(/\son[a-z]+="/i, h), 'no', `${at}: an inline handler`);
    assert.equal(flag(/lx-boot/, h), 'no', `${at}: the page is hidden until the fonts are in`);
    assert.equal(flag(/\.dc\.html/, h), 'no', `${at}: a link of the design is left`);
    for (const m of h.matchAll(/(?:src|href)="(\/assets\/[^"#]+)"/g)) assert.ok(existsSync(join(BUILD, m[1].slice(1))), `${at}: ${m[1]} exists`);
    if (l !== 'ru') {
      const left = russianLeft(h);
      assert.ok(left === 0, `${at}: Russian words left in the first render: ${left}`);
    }
  });
});

test('the footer link "План развития" leads to roadmap/status (site/test/docs.test.mjs wants it on the home pages)', { skip: !have }, () => {
  assert.ok(page('main', 'en').includes('href="/roadmap/status/"'));
  assert.ok(page('main', 'ru').includes('href="/ru/roadmap/status/"'));
});

test('fa is right to left like the design; the page language reaches the host', { skip: !have }, () => {
  const fa = page('main', 'fa');
  assert.equal(flag(/class="lp[ "][^>]*dir="rtl"/, fa), 'yes');
  assert.ok(fa.includes('data-lang="fa"'));
});
