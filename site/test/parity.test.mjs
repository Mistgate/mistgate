// The English docs and each translation must have the same shape: the same pages, and on each page the same headings, code
// blocks, tables and table rows. A row whose first cell is only a name in code (a flag, a variable, a procedure) must
// name the same thing in both languages. Prose is free to differ; the structure is what drifts silently.
import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import MarkdownIt from "markdown-it";
import { loadDocs } from "../wiki.mjs";
import { roadmapBlocks, roadmapShape } from "../src/roadmap.mjs";
import { LANGS, COMPLETE } from "../src/urls.mjs";

const docs = loadDocs(join(dirname(dirname(fileURLToPath(import.meta.url))), "..", "docs"));
const md = new MarkdownIt();

export function shape(src) {
  const tk = md.parse(src.replace(/^﻿?---\r?\n[\s\S]*?\r?\n---\r?\n/, ""), {});
  const s = { h2: 0, h3: 0, h4: 0, fences: 0, tables: [], keys: [] };
  tk.forEach((t, i) => {
    if (t.type === "heading_open" && t.tag in s) s[t.tag]++;
    else if (t.type === "fence") s.fences++;
    else if (t.type === "table_open") s.tables.push(0);
    else if (t.type === "tr_open") s.tables[s.tables.length - 1]++;
    else if (t.type === "inline" && tk[i - 1].type === "td_open" && tk[i - 2].type === "tr_open") {
      const parts = t.children.filter((c) => !(c.type === "text" && /^[\s,/、，]*$/.test(c.content)));
      const key = parts.map((c) => c.content).join(" ");
      // names only (flags, variables, procedures, commands): a shown value such as `123 ms` is translated
      if (parts.length && parts.every((c) => c.type === "code_inline") && /^[A-Za-z_.-][\w.:=/ -]*$/.test(key)) s.keys.push(key);
    }
  });
  return s;
}

// Every translation (ru, zh) against English. A language in COMPLETE must have every English page; one that is not
// (zh while it is being translated) is checked on the pages it has, and may not have a page English lacks. Flip
// COMPLETE.zh in src/urls.mjs when the last page lands: from then on a missing page fails here.
const TRANSLATIONS = LANGS.filter((l) => l !== "en");

for (const lang of TRANSLATIONS) {
  test(`every page exists in English and ${lang}${COMPLETE[lang] ? "" : " (as far as translated)"}`, () => {
    const en = Object.keys(docs.en).sort(), tr = Object.keys(docs[lang]).sort();
    assert.deepEqual(tr.filter((k) => !docs.en[k]), [], `pages in docs/${lang} that English does not have`);
    if (COMPLETE[lang]) assert.deepEqual(en.filter((k) => !docs[lang][k]), [], `pages missing in docs/${lang}`);
  });

  test(`each page has the same structure in English and ${lang}`, () => {
    const bad = [];
    for (const key of Object.keys(docs.en)) {
      if (!docs[lang][key]) continue;
      const a = shape(docs.en[key]), b = shape(docs[lang][key]);
      for (const f of ["h2", "h3", "h4", "fences"]) if (a[f] !== b[f]) bad.push(`${key}: ${f} en ${a[f]}, ${lang} ${b[f]}`);
      if (a.tables.join() !== b.tables.join()) bad.push(`${key}: table rows en [${a.tables}], ${lang} [${b.tables}]`);
      const onlyEn = a.keys.filter((k) => !b.keys.includes(k)), onlyTr = b.keys.filter((k) => !a.keys.includes(k));
      if (onlyEn.length || onlyTr.length) bad.push(`${key}: table rows only in en ${JSON.stringify(onlyEn)}, only in ${lang} ${JSON.stringify(onlyTr)}`);
    }
    assert.deepEqual(bad, []);
  });

  test(`each roadmap has the same phases, cards, chips and links in English and ${lang}`, () => {
    for (const key of Object.keys(docs.en)) {
      if (!docs[lang][key]) continue;
      assert.deepEqual(roadmapBlocks(docs[lang][key]).map(roadmapShape), roadmapBlocks(docs.en[key]).map(roadmapShape), `${lang}/${key}`);
    }
  });
}

test("English and Russian are complete; zh is the only language allowed to lag", () => {
  assert.equal(COMPLETE.en && COMPLETE.ru, true);
  assert.deepEqual(LANGS.filter((l) => !(l in COMPLETE)), [], "every language says whether it is complete");
});

test("the roadmap check catches a dropped card and a different link", () => {
  const en = "```roadmap\n- status: next\n  title: Next\n  cards:\n    - title: A\n      text: See [x](../a.md#one).\n    - title: B\n```\n";
  const ru = "```roadmap\n- status: next\n  title: Дальше\n  cards:\n    - title: А\n      text: См. [x](../a.md#раз).\n```\n";
  assert.notDeepEqual(roadmapBlocks(ru).map(roadmapShape), roadmapBlocks(en).map(roadmapShape));
  assert.deepEqual(roadmapShape(roadmapBlocks(ru)[0])[0].cards[0].links, ["../a.md"]); // anchors are translated, pages are not
  assert.throws(() => roadmapBlocks(en.replace("next", "soon")), /status must be one of/);
});

test("the shape check catches a dropped table row", () => {
  const en = "## A\n\n| Procedure | Admin |\n|---|---|\n| `X.List` | yes |\n| `X.Check` | yes |\n";
  const ru = "## А\n\n| Процедура | Админ |\n|---|---|\n| `X.List` | да |\n";
  assert.notDeepEqual(shape(en).tables, shape(ru).tables);
  assert.deepEqual(shape(en).keys, ["X.List", "X.Check"]);
});
