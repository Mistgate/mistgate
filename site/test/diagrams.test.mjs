// Diagrams: every hand-drawn SVG exists in both languages with the same canvas, keeps its styles to itself (one
// <style>, every rule under .mgd), names itself, and loads nothing from outside. Mermaid output is scoped the same way.
import assert from "node:assert/strict";
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { mermaidSvg } from "../src/diagrams.mjs";

const docs = join(dirname(dirname(fileURLToPath(import.meta.url))), "..", "docs");
const list = (lang) => { const d = join(docs, lang, "diagrams"); return existsSync(d) ? readdirSync(d).filter((f) => f.endsWith(".svg")).sort() : []; };
const selectors = (css) => [...css.replace(/@media[^{]*\{/g, "").matchAll(/(?:^|\})\s*([^{}]+?)\s*\{/g)].flatMap((m) => m[1].split(",").map((s) => s.trim()));

test("every diagram has an English and a Russian file", () => {
  assert.deepEqual(list("ru"), list("en"));
});

for (const name of list("en")) {
  test(`diagram ${name}: one scoped style, a name, no outside resources, the same canvas in both languages`, () => {
    const [en, ru] = ["en", "ru"].map((l) => readFileSync(join(docs, l, "diagrams", name), "utf8"));
    for (const [lang, s] of [["en", en], ["ru", ru]]) {
      assert.match(s, /^<svg\b[^>]*\bclass="mgd"/, `${lang}: the root is <svg class="mgd">`);
      assert.equal(s.match(/<style\b/g)?.length, 1, `${lang}: exactly one <style>`);
      const css = /<style>([\s\S]*?)<\/style>/.exec(s)[1];
      assert.deepEqual(selectors(css).filter((x) => !x.startsWith(".mgd")), [], `${lang}: every rule is under .mgd`);
      assert.match(s, /<title\b[^>]*>[^<]+<\/title>/, `${lang}: has a title`);
      assert.match(s, /<desc\b[^>]*>[^<]+<\/desc>/, `${lang}: has a description`);
      assert.doesNotMatch(s, /<script|@import|(?:href|src)="(?!#)|url\((?!#)/i, `${lang}: loads nothing`);
    }
    assert.equal(/viewBox="([^"]+)"/.exec(ru)[1], /viewBox="([^"]+)"/.exec(en)[1], "same viewBox");
    assert.match(ru.replace(/<style>[\s\S]*?<\/style>/, ""), /<text[^>]*>[^<]*[а-яё]/i, "the Russian file is translated");
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
