// The English and Russian docs must have the same shape: the same pages, and on each page the same headings, code
// blocks, tables and table rows. A row whose first cell is only a name in code (a flag, a variable, a procedure) must
// name the same thing in both languages. Prose is free to differ; the structure is what drifts silently.
import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import MarkdownIt from "markdown-it";
import { loadDocs } from "../wiki.mjs";

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
      const parts = t.children.filter((c) => !(c.type === "text" && /^[\s,/]*$/.test(c.content)));
      const key = parts.map((c) => c.content).join(" ");
      // names only (flags, variables, procedures, commands): a shown value such as `123 ms` is translated
      if (parts.length && parts.every((c) => c.type === "code_inline") && /^[A-Za-z_.-][\w.:=/ -]*$/.test(key)) s.keys.push(key);
    }
  });
  return s;
}

test("every page exists in both languages", () => {
  const en = Object.keys(docs.en).sort(), ru = Object.keys(docs.ru).sort();
  assert.deepEqual(en.filter((k) => !docs.ru[k]), [], "pages missing in docs/ru");
  assert.deepEqual(ru.filter((k) => !docs.en[k]), [], "pages missing in docs/en");
});

test("each page has the same structure in English and Russian", () => {
  const bad = [];
  for (const key of Object.keys(docs.en)) {
    if (!docs.ru[key]) continue;
    const a = shape(docs.en[key]), b = shape(docs.ru[key]);
    for (const f of ["h2", "h3", "h4", "fences"]) if (a[f] !== b[f]) bad.push(`${key}: ${f} en ${a[f]}, ru ${b[f]}`);
    if (a.tables.join() !== b.tables.join()) bad.push(`${key}: table rows en [${a.tables}], ru [${b.tables}]`);
    const onlyEn = a.keys.filter((k) => !b.keys.includes(k)), onlyRu = b.keys.filter((k) => !a.keys.includes(k));
    if (onlyEn.length || onlyRu.length) bad.push(`${key}: table rows only in en ${JSON.stringify(onlyEn)}, only in ru ${JSON.stringify(onlyRu)}`);
  }
  assert.deepEqual(bad, []);
});

test("the shape check catches a dropped table row", () => {
  const en = "## A\n\n| Procedure | Admin |\n|---|---|\n| `X.List` | yes |\n| `X.Check` | yes |\n";
  const ru = "## А\n\n| Процедура | Админ |\n|---|---|\n| `X.List` | да |\n";
  assert.notDeepEqual(shape(en).tables, shape(ru).tables);
  assert.deepEqual(shape(en).keys, ["X.List", "X.Check"]);
});
