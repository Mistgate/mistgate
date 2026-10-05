import assert from "node:assert/strict";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { buildWiki, loadDocs, sanitize, ghSlug, wikiHref } from "./wiki.mjs";

const page = (title, body, description = "A page.") => `---\ntitle: ${title}\ndescription: ${description}\n---\n\n${body}\n`;
const idx = (groups) => page("Docs", `Intro.\n\n## All pages\n\n${Object.entries(groups).map(([g, ls]) => `### ${g}\n\n${ls.map((l) => `- [x](${l}.md): d`).join("\n")}\n`).join("\n")}`);
const get = (r, name) => { const t = r.files.get(name); assert.ok(t, `no file ${name}; have ${[...r.files.keys()].join(", ")}`); return t; };

const en = {
  index: idx({ "Getting started": ["start/one", "start/two"], Guide: ["guide/nodes"] }),
  "start/one": page("Install the panel", "See [two](two.md#second-part), [nodes](../guide/nodes.md), [self](#first-part), [`code` link](two.md).\n\n## First part\n\nText.\n\n```sh\n# [no](two.md) inside a fence\n```\n\nInline `[no](two.md)` code.\n"),
  "start/two": page("Add: a node?", "Body.\n\n## Second part\n\n## Second part\n\nSee [license](../../../LICENSE), [agents](../../../AGENTS.md#x), [dir](../../../web/), [site](https://example.com/a.md), ![shot](../img/a.png).\n"),
  "guide/nodes": page("Nodes", "> **Note:** one line.\n> second line.\n\n> **Warning:** careful.\n\n> plain quote\n\n```md\n> **Note:** in a fence\n```\n"),
  "guide/lonely": page("Lonely", "Not listed in the index."),
};
const ru = {
  index: idx({ "Начало": ["start/one"], Руководство: ["guide/nodes"] }),
  "start/one": page("Установка панели", "См. [ноды](../guide/nodes.md#раздел) и [en](../../en/start/one.md)."),
  "guide/nodes": page("Nodes", "> **Важно:** раз.\n\n> **Внимание:** два.\n\n## Раздел\n"),
};
const build = (e = en, r = ru) => buildWiki({ en: e, ru: r });

test("page names: title, spaces to dashes, unsafe characters dropped", () => {
  assert.equal(sanitize("M2: SSH installation and recovery"), "M2 SSH installation and recovery");
  assert.equal(sanitize("A/B #1?"), "A-B 1");
  const r = build();
  get(r, "Install-the-panel.md");
  get(r, "Add-a-node.md");
  get(r, "Установка-панели.md");
});

test("links between pages become wiki links, anchors kept, code untouched", () => {
  const t = get(build(), "Install-the-panel.md");
  assert.match(t, /\[two\]\(Add-a-node#second-part\)/);
  assert.match(t, /\[nodes\]\(Nodes\)/);
  assert.match(t, /\[self\]\(Install-the-panel#first-part\)/);
  assert.match(t, /\[`code` link\]\(Add-a-node\)/);
  assert.match(t, /# \[no\]\(two\.md\) inside a fence/);
  assert.match(t, /`\[no\]\(two\.md\)`/);
  assert.doesNotMatch(t.replace(/```sh[\s\S]*?```/, "").replace(/`[^`]*`/g, "").replace(/<sub>.*<\/sub>/, ""), /\.md[)#]/);
});

test("links to repo files outside docs/, directories, images and external links", () => {
  const t = get(build(), "Add-a-node.md");
  assert.match(t, /\[license\]\(https:\/\/github\.com\/Mistgate\/mistgate\/blob\/main\/LICENSE\)/);
  assert.match(t, /\[agents\]\(https:\/\/github\.com\/Mistgate\/mistgate\/blob\/main\/AGENTS\.md#x\)/);
  assert.match(t, /\[dir\]\(https:\/\/github\.com\/Mistgate\/mistgate\/tree\/main\/web\)/);
  assert.match(t, /\[site\]\(https:\/\/example\.com\/a\.md\)/);
  assert.match(t, /!\[shot\]\(https:\/\/raw\.githubusercontent\.com\/Mistgate\/mistgate\/main\/docs\/en\/img\/a\.png\)/);
});

test("page layout: italic lead, no front matter, edit and site links", () => {
  const t = get(build(), "Install-the-panel.md");
  assert.ok(t.startsWith("*A page.*\n\n"));
  assert.doesNotMatch(t, /^---\ntitle:/);
  assert.match(t, /\[Edit this page\]\(https:\/\/github\.com\/Mistgate\/mistgate\/edit\/main\/docs\/en\/start\/one\.md\)/);
  assert.match(t, /\[View on mistgate\.app\]\(https:\/\/mistgate\.app\/start\/one\/\)\<\/sub\>\n$/);
  const r = get(build(), "Установка-панели.md");
  assert.match(r, /docs\/ru\/start\/one\.md\)/);
  assert.match(r, /https:\/\/mistgate\.app\/ru\/start\/one\/\)/);
  assert.match(get(build(), "Docs.md"), /\(https:\/\/mistgate\.app\/\)<\/sub>/);
  assert.match(get(build(), "Docs-(RU).md"), /mistgate\.app\/ru\/\)<\/sub>/);
});

test("callouts become GitHub alerts in both languages; plain quotes and fences stay", () => {
  const e = get(build(), "Nodes.md");
  assert.match(e, /> \[!NOTE\]\n> one line\.\n> second line\./);
  assert.match(e, /> \[!WARNING\]\n> careful\./);
  assert.match(e, /\n> plain quote\n/);
  assert.match(e, /```md\n> \*\*Note:\*\* in a fence\n```/);
  const r = get(build(), "Nodes-(RU).md");
  assert.match(r, /> \[!NOTE\]\n> раз\./);
  assert.match(r, /> \[!WARNING\]\n> два\./);
});

test("collisions: RU gets (RU), same-language clash gets the section, reserved names are avoided", () => {
  const r = build(en, ru);
  get(r, "Nodes.md");
  get(r, "Nodes-(RU).md");
  assert.equal(wikiHref("Nodes (RU)"), "Nodes-%28RU%29");
  assert.match(get(r, "Установка-панели.md"), /\[en\]\(Install-the-panel\)/);
  assert.match(get(r, "Установка-панели.md"), /\[ноды\]\(Nodes-%28RU%29#раздел\)/);

  const dup = build({ ...en, "guide/dup": page("Nodes", "x"), "ref/home": page("Home", "x") }, ru);
  const names = [...dup.files.keys()];
  assert.ok(names.includes("Nodes-(Guide)-guide-nodes.md") && names.includes("Nodes-(Guide)-guide-dup.md"), names.join());
  assert.equal(new Set(names.map((n) => n.toLowerCase())).size, names.length);
  assert.ok(names.includes("Home-(docs).md"));
  assert.ok(names.includes("Home.md"));
});

test("index order drives Home and the sidebar; unlisted pages go to Other pages; both languages", () => {
  const r = build();
  const side = get(r, "_Sidebar.md");
  const order = ["**Getting started**", "[Install the panel]", "[Add: a node?](Add-a-node)", "**Guide**", "[Nodes](Nodes)", "**Other pages**", "[Lonely]", "**Начало**", "[Установка панели]", "**Руководство**"];
  let at = -1;
  for (const s of order) { const i = side.indexOf(s, at + 1); assert.ok(i > at, `${s} out of order in sidebar`); at = i; }
  assert.match(side, /<details open>[\s\S]*<\/details>[\s\S]*<details>/);
  const home = get(r, "Home.md");
  assert.match(home, /https:\/\/mistgate\.app/);
  assert.match(home, /github\.com\/Mistgate\/mistgate/);
  assert.match(home, /\[Start page\]\(Docs\)/);
  assert.match(home, /\[Начальная страница\]\(Docs-%28RU%29\)/);
  assert.match(home, /### Getting started\n\n- \[Install the panel\]\(Install-the-panel\): A page\./);
  assert.match(home, /### Руководство\n\n- \[Nodes\]\(Nodes-%28RU%29\)/);
  assert.ok(r.warnings.some((w) => /guide\/lonely\.md not listed/.test(w)));
  const foot = get(r, "_Footer.md");
  assert.match(foot, /edit the docs, not the wiki/);
  assert.match(foot, /правьте документацию, а не вики/);
});

test("broken page links are errors that list each link; broken anchors are warnings", () => {
  const bad = build({ ...en, "start/one": page("Install the panel", "[a](missing.md) [b](../../../../x.md) [c](two.md#nope) [d](/abs)") }, ru);
  assert.equal(bad.errors.length, 3);
  assert.ok(bad.errors.some((e) => /en\/start\/one\.md.*missing\.md/.test(e)));
  assert.ok(bad.errors.some((e) => /outside the repository/.test(e)));
  assert.ok(bad.errors.some((e) => /root-relative/.test(e)));
  assert.ok(bad.warnings.some((w) => /#nope not found/.test(w)));
});

test("ghSlug matches GitHub anchors, duplicate headings are numbered", () => {
  assert.equal(ghSlug("Step 1: `mistgate setup`!"), "step-1-mistgate-setup");
  assert.equal(ghSlug("Раздел один"), "раздел-один");
  const r = build();
  assert.ok(!r.warnings.some((w) => /Second part/i.test(w)));
});

test("the real docs/ tree generates cleanly: every link resolves, every page ends with edit links", () => {
  const docs = join(dirname(fileURLToPath(import.meta.url)), "..", "docs");
  const r = buildWiki(loadDocs(docs));
  assert.deepEqual(r.errors, []);
  assert.equal(r.files.size, r.pages.length + 3);
  for (const [name, text] of r.files) {
    if (name.startsWith("_")) continue;
    if (name !== "Home.md") assert.match(text, /\[(Edit this page|Редактировать страницу)\]\(https:\/\/github\.com\/Mistgate\/mistgate\/edit\/main\/docs\/(en|ru)\/.+\.md\)/, name);
    // a page-local link must name a generated file
    for (const m of text.replace(/```[\s\S]*?```/g, "").matchAll(/\]\(([^)\s:]+?)(?:#[^)]*)?\)/g)) {
      const target = decodeURIComponent(m[1]);
      assert.ok(r.files.has(`${target}.md`) || target === "Home", `${name}: link to ${target} does not exist`);
    }
  }
});
