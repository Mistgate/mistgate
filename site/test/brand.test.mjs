// The docs wear the admin panel's look: its colour tokens, its accent, its section names and icons. These read the
// panel's own source (web/src) so a change there that the docs did not follow fails here.
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { T, PAGE_SECTION } from "../src/layout.mjs";
import { NAV } from "../src/icons.mjs";
import { LANDING } from "../src/landing.mjs";
import { LANGS, COMPLETE } from "../src/urls.mjs";

const root = join(dirname(dirname(fileURLToPath(import.meta.url))), "..");
const panelCss = readFileSync(join(root, "web/src/index.css"), "utf8");
const docsCss = readFileSync(join(root, "site/src/assets/style.css"), "utf8");
const block = (css, selector) => { const at = css.indexOf(`${selector} {`); assert.ok(at >= 0, `no ${selector} in index.css`); return css.slice(at, css.indexOf("\n}", at)); };
const value = (css, name) => new RegExp(`--${name}:\\s*([^;]+);`).exec(css)?.[1].trim().toLowerCase();

test("the docs use the panel's colour tokens in both themes", () => {
  const dark = block(panelCss, ":root"), light = block(panelCss, ':root[data-theme="light"]');
  for (const name of ["bg", "surface", "surface-2", "border", "text", "muted", "faint"]) {
    assert.equal(value(docsCss, name), `light-dark(${value(light, name)}, ${value(dark, name)})`, `--${name}`);
  }
});

test("the accent is the panel's lavender", () => {
  assert.equal(value(block(panelCss, ":root"), "accent-lavender"), "#b8acf2");
  assert.equal(value(docsCss, "accent"), "#b8acf2");
});

test("section names match the panel's sidebar in both languages", () => {
  for (const lang of LANGS.filter((l) => COMPLETE[l])) { // zh joins when COMPLETE.zh is true: until then its wording is a placeholder
    const i18n = readFileSync(join(root, `web/src/i18n/${lang}.ts`), "utf8");
    for (const [id, label] of Object.entries(T[lang].sections)) assert.match(i18n, new RegExp(`"nav\\.${id}": "${label}"`), `${lang} nav.${id}`);
  }
});

test("the home page describes each admin section in the panel's own words", () => {
  for (const lang of LANGS.filter((l) => COMPLETE[l])) { // zh joins when COMPLETE.zh is true: until then its wording is a placeholder
    const i18n = readFileSync(join(root, `web/src/i18n/${lang}.ts`), "utf8");
    for (const [id, desc] of Object.entries(LANDING[lang].desc)) {
      assert.equal(new RegExp(`"section\\.${id}\\.desc": "([^"]+)"`).exec(i18n)?.[1], desc, `${lang} section.${id}.desc`);
    }
  }
});

test("every page section is a section of the panel", () => {
  for (const [key, id] of Object.entries(PAGE_SECTION)) {
    assert.ok(NAV[id], `${key} -> ${id}`);
    assert.ok(existsSync(join(root, "docs/en", `${key}.md`)), `PAGE_SECTION names a page that does not exist: ${key}`);
  }
});
