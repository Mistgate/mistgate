import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { resolveLinks, slugify, pageUrl, homeUrl, appUrl } from '../links.mjs';

const HERE = dirname(fileURLToPath(import.meta.url));
const DOCS = join(HERE, '..', '..', '..', '..', 'docs');
const table = JSON.parse(readFileSync(join(HERE, '..', 'links.json'), 'utf8'));
const LANGS = ['en', 'ru', 'zh', 'fa', 'tr'];

test('addresses are the ones the docs generator writes', () => {
  assert.equal(pageUrl('en', 'guide/vless'), '/guide/vless/'); assert.equal(pageUrl('ru', 'guide/vless'), '/ru/guide/vless/');
  assert.equal(pageUrl('en', 'index'), '/docs/'); assert.equal(pageUrl('fa', 'index'), '/fa/docs/'); // the start page of the docs, not the landing
  assert.equal(homeUrl('en'), '/'); assert.equal(homeUrl('fa'), '/fa/'); assert.equal(appUrl('en'), '/app/'); assert.equal(appUrl('zh'), '/zh/app/');
  assert.equal(slugify('Установка Mistgate с AI-агентом'), 'установка-mistgate-с-ai-агентом');
});

test('every docs address of the table is a page the docs generator builds; the roadmap link of the footer goes to roadmap/status', { skip: !existsSync(join(DOCS, 'en')) }, () => {
  for (const lang of LANGS) {
    const t = resolveLinks(table, lang, DOCS);
    const inLang = existsSync(join(DOCS, lang, 'roadmap', 'status.md')) ? lang : 'en';
    assert.equal(t.byText['План развития'], (inLang === 'en' ? '' : '/' + inLang) + '/roadmap/status/');
    for (const u of [...Object.values(t.map), ...Object.values(t.byText), ...t.byPrefix.map((x) => x[1]), ...Object.values(t.page)]) {
      const [path] = u.split('#');
      if (/^\/(?:[a-z]{2}\/)?(?:app\/)?$/.test(path)) continue; // a page of the new site
      const m = /^(?:\/(ru|zh|fa|tr))?\/(.+)\/$/.exec(path);
      assert.ok(m, `${lang}: ${u} is not an address of a docs page`);
      assert.ok(existsSync(join(DOCS, m[1] || 'en', (m[2] === 'docs' ? 'index' : m[2]) + '.md')), `${lang}: ${u} is not a docs page`);
    }
  }
});

test('the anchors of the AI guide come from the real headings; a language without the page gets the English one', { skip: !existsSync(join(DOCS, 'en')) }, () => {
  const en = resolveLinks(table, 'en', DOCS), ru = resolveLinks(table, 'ru', DOCS), fa = resolveLinks(table, 'fa', DOCS);
  assert.equal(en.map['Docs.dc.html#установка-mistgate-с-ai-агентом'], '/getting-started/ai-agents/#install-mistgate-with-an-ai-agent');
  assert.equal(ru.map['Docs.dc.html#ноды-и-флот-через-mcp'], '/ru/getting-started/ai-agents/#ноды-и-флот-через-mcp');
  assert.equal(en.map['Docs.dc.html'], '/docs/', 'a bare docs link is the start page of the docs');
  assert.equal(ru.map['Docs.dc.html'], '/ru/docs/');
  assert.equal(fa.map['Docs.dc.html'], '/fa/docs/');
});

test('a key that is not a docs page is a build error', () => {
  assert.throws(() => resolveLinks({ map: { x: 'nope/nothing' }, byText: {}, page: {} }, 'en', DOCS), /not a docs page/);
});
