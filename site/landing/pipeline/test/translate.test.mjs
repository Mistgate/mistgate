import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync, existsSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { parseI18n, patchTranslator, makeTr, corpusOf, stringsOf, subsetKeys, keysUsedFor, langDict, LANGS } from '../translate.mjs';
import { closure } from '../sync.mjs';

const DESIGN = join(fileURLToPath(new URL('.', import.meta.url)), '..', '..', 'design');
const have = existsSync(join(DESIGN, 'land.i18n.js'));
const i18n = have ? readFileSync(join(DESIGN, 'land.i18n.js'), 'utf8') : '';
const read = (n) => readFileSync(join(DESIGN, n), 'utf8');
const isApp = (n) => /^App(Main|Routes|Diag|Set|Byp)(En|Zh|Fa|Tr)?$/.test(n);

test('the design translator patches apply (exactly once each)', { skip: !have }, () => {
  const { code } = parseI18n(i18n);
  assert.match(patchTranslator(code), /now: apply/);
  assert.throws(() => patchTranslator('nothing'), /exactly once/);
});

test('the design translator, over the full dictionary, translates the known strings', { skip: !have }, () => {
  const { dict } = parseI18n(i18n), tr = makeTr(i18n, dict);
  assert.equal(tr('Панель', 'EN'), 'Panel');
  assert.equal(tr('5 нод · 6 онлайн', 'EN'), '5 nodes · 6 online'); // the numbers are folded to # and put back
  assert.equal(tr('Привет всем', 'EN'), null, 'a string without a translation stays Russian');
});

// the subset of the dictionary a page gets must give the same text as the full dictionary: for the strings of the sources, and
// for the strings the live screens showed (harvest.json, written by `pnpm check --harvest`)
test('the subset of the dictionary gives the same text as the full one', { skip: !have }, () => {
  const { dict } = parseI18n(i18n);
  const names = closure((n) => read(n + '.dc.html'), ['Main']).filter((n) => !isApp(n) && existsSync(join(DESIGN, n + '.dc.html')));
  const sources = names.map((n) => read(n + '.dc.html'));
  const harvestFile = join(DESIGN, '..', 'pipeline', 'harvest.json');
  const harvest = existsSync(harvestFile) ? JSON.parse(readFileSync(harvestFile, 'utf8')).strings.main || [] : [];
  const keys = subsetKeys(dict, corpusOf(sources), [...keysUsedFor(i18n, dict, harvest)]);
  const strings = new Set([...harvest, ...stringsOf(sources).map((x) => x.trim())]);
  const cyr = [...strings].filter((s) => /[А-Яа-яЁё]/.test(s) && s.length < 400);
  assert.ok(cyr.length > 500, 'there are strings to compare: ' + cyr.length);
  const full = makeTr(i18n, dict);
  for (const L of LANGS) {
    const sub = makeTr(i18n, Object.fromEntries(Object.entries(langDict(dict, keys, L)).map(([k, v]) => [k, [v]])), { subset: true });
    const bad = cyr.filter((s) => full(s, L) !== sub(s, L));
    assert.deepEqual(bad.slice(0, 5), [], `${L}: ${bad.length} of ${cyr.length} strings differ between the subset and the full dictionary`);
  }
  assert.ok(keys.length < Object.keys(dict).length / 2, 'the subset is a part of the dictionary: ' + keys.length);
});

test('keysUsedFor reads the keys the full translator reads', { skip: !have }, () => {
  const { dict } = parseI18n(i18n);
  const k = keysUsedFor(i18n, dict, ['Панель'], ['EN']);
  assert.ok(k.has('Панель'));
});

test('langDict keeps one language', { skip: !have }, () => {
  const { dict } = parseI18n(i18n);
  assert.equal(langDict(dict, ['Панель'], 'EN')['Панель'], 'Panel');
  assert.throws(() => langDict(dict, ['Панель'], 'DE'), /no such language/);
});
