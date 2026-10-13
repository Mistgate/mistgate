// Diagrams: every hand-drawn SVG exists in every complete language with the same canvas, keeps its styles to itself (one
// <style>, every rule under .mgd), names itself, and loads nothing from outside. Mermaid output is scoped the same way.
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { mermaidSvg } from "../src/diagrams.mjs";
import { COMPLETE } from "../src/urls.mjs";

const docs = join(dirname(dirname(fileURLToPath(import.meta.url))), "..", "docs");
const list = (lang) => { const d = join(docs, lang, "diagrams"); return existsSync(d) ? readdirSync(d).filter((f) => f.endsWith(".svg")).sort() : []; };
const selectors = (css) => [...css.replace(/@media[^{]*\{/g, "").matchAll(/(?:^|\})\s*([^{}]+?)\s*\{/g)].flatMap((m) => m[1].split(",").map((s) => s.trim()));

// the letters a translated drawing must contain
const SCRIPT = { ru: /[а-яё]/i, zh: /[一-鿿]/ };
const TRANSLATIONS = Object.keys(SCRIPT);

test("every diagram has a file in every complete language, and a language still being translated has no stray ones", () => {
  for (const lang of TRANSLATIONS) {
    if (COMPLETE[lang]) assert.deepEqual(list(lang), list("en"), lang);
    else assert.deepEqual(list(lang).filter((f) => !list("en").includes(f)), [], `${lang}: a diagram English does not have`);
  }
});

for (const name of list("en")) {
  const langs = ["en", ...TRANSLATIONS.filter((l) => list(l).includes(name))];
  test(`diagram ${name}: one scoped style, a name, no outside resources, the same canvas in every language`, () => {
    const src = Object.fromEntries(langs.map((l) => [l, readFileSync(join(docs, l, "diagrams", name), "utf8")]));
    for (const [lang, s] of Object.entries(src)) {
      assert.match(s, /^<svg\b[^>]*\bclass="mgd"/, `${lang}: the root is <svg class="mgd">`);
      assert.equal(s.match(/<style\b/g)?.length, 1, `${lang}: exactly one <style>`);
      const css = /<style>([\s\S]*?)<\/style>/.exec(s)[1];
      assert.deepEqual(selectors(css).filter((x) => !x.startsWith(".mgd")), [], `${lang}: every rule is under .mgd`);
      assert.match(s, /<title\b[^>]*>[^<]+<\/title>/, `${lang}: has a title`);
      assert.match(s, /<desc\b[^>]*>[^<]+<\/desc>/, `${lang}: has a description`);
      assert.doesNotMatch(s, /<script|@import|(?:href|src)="(?!#)|url\((?!#)/i, `${lang}: loads nothing`);
      if (lang !== "en") assert.match(s.replace(/<style>[\s\S]*?<\/style>/, ""), new RegExp(`<text[^>]*>[^<]*${SCRIPT[lang].source}`, SCRIPT[lang].flags), `the ${lang} file is translated`);
    }
    for (const lang of langs) assert.equal(/viewBox="([^"]+)"/.exec(src[lang])[1], /viewBox="([^"]+)"/.exec(src.en)[1], `${lang}: same viewBox`);
  });
}

test("a Mermaid diagram renders offline, scoped, with its own ids and its accTitle as the name", () => {
  const warns = [];
  const svg = mermaidSvg("sequenceDiagram\n  accTitle: Adding a node\n  participant P as Panel\n  participant N as Node\n  N->>P: enroll", "Diagram", (m) => warns.push(m));
  assert.deepEqual(warns, []);
  assert.doesNotMatch(svg, /@import|googleapis|display=swap/);
  assert.match(svg, /\.mgm text \{ font-family/, "the renderer's bare `text` rule is scoped");
  assert.match(svg, /^<svg\b[^>]*class="mgm"[^>]*role="img"[^>]*aria-label="Adding a node"/);
  const css = /<style>([\s\S]*?)<\/style>/.exec(svg)[1];
  assert.deepEqual(selectors(css).filter((x) => !x.startsWith(".mgm")), []);
  assert.ok([...svg.matchAll(/\sid="([^"]+)"/g)].every((m) => /^m\d+-/.test(m[1])), "every id is prefixed");
  assert.match(mermaidSvg("graph LR\n  A --> B", "Diagram", (m) => warns.push(m)), /aria-label="Diagram"/);
  assert.equal(warns.length, 1, "a diagram without accTitle is reported");
});
