// Chinese on the site, whatever pages exist: the search matcher finds Chinese text without word breaks, and a build with
// only two zh pages (a fixture in a temp dir; the real docs/zh is written by the translators) neither fails nor links to
// the pages that are missing. The language menu, hreflang, sitemap, llms.txt, 404 and the search index are checked there.
import assert from "node:assert/strict";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { spawnSync } from "node:child_process";
import test, { after, before } from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import { formatStars } from "../src/layout.mjs";

const site = dirname(dirname(fileURLToPath(import.meta.url)));
const find = vm.runInNewContext(`${readFileSync(join(site, "src/assets/find.js"), "utf8")}\nfind`);

test("search finds Chinese by any part of a word, with no spaces and in full-width form", () => {
  const data = [
    { t: "添加节点", d: "通过 SSH 安装代理，把服务器加入面板。", g: "入门", u: "/zh/getting-started/add-node/", h: [["节点的状态", "status"]], k: [["mistgate-node", "", 1]] },
    { t: "备份", d: "加密的面板备份。", g: "运维", u: "/zh/operations/backups/", h: [], k: [] },
  ];
  assert.equal(find(data, "节点")[0].url, "/zh/getting-started/add-node/");
  assert.equal(find(data, "添加")[0].label, "添加节点");
  assert.equal(find(data, "服务器")[0].url, "/zh/getting-started/add-node/", "a word inside the description");
  assert.ok(find(data, "节点").some((h) => h.url.endsWith("#status")), "a heading");
  assert.equal(find(data, "备份")[0].url, "/zh/operations/backups/");
  assert.equal(find(data, "ＳＳＨ 代理")[0].label, "添加节点", "full-width letters fold to ASCII, a space separates two words");
  assert.equal(find(data, "数据库").length, 0);
  assert.equal(find(data, "mistgate-node")[0].code, true, "names in code still work");
});

const root = mkdtempSync(join(tmpdir(), "mg-zh-"));
const out = join(root, "dist");
let build;
const read = (p) => readFileSync(join(out, p), "utf8");
const menu = (html) => /<details class="lang">[\s\S]*?<\/details>/.exec(html)?.[0] ?? "";

before(() => {
  // the fixture owns docs/zh: whatever real zh pages and screenshots exist are left out (the diagrams may stay)
  const realZh = join(site, "..", "docs", "zh");
  cpSync(join(site, "..", "docs"), join(root, "docs"), { recursive: true, filter: (src) => !(src.startsWith(realZh) && src.endsWith(".md")) && !src.endsWith("-zh.webp") });
  const zh = (p, text) => { const f = join(root, "docs/zh", p); mkdirSync(dirname(f), { recursive: true }); writeFileSync(f, text); };
  zh("index.md", "---\ntitle: Mistgate 文档\ndescription: 自托管 VPN 面板的文档。\n---\n\n欢迎。\n\n## 所有页面\n\n### 入门\n\n- [概览](getting-started/overview.md): 简介\n- [添加节点](getting-started/add-node.md): 尚未翻译\n");
  zh("getting-started/overview.md", "---\ntitle: 概览\ndescription: Mistgate 是什么，适合谁用，各部分如何配合。\n---\n\n## 两个程序\n\n节点是运行在每台 VPN 服务器上的代理。详见[添加节点](add-node.md)。\n\n> **注意：** 这是占位文字。\n");
  build = spawnSync(process.execPath, ["build.mjs"], { cwd: site, env: { ...process.env, MG_DOCS: join(root, "docs"), MG_DIST: out, MG_STARS: "1234" }, encoding: "utf8" });
});
after(() => rmSync(root, { recursive: true, force: true }));

test("a build with two zh pages succeeds and writes only those", () => {
  assert.equal(build.status, 0, build.stdout + build.stderr);
  assert.match(build.stdout, /no broken internal links/);
  assert.match(build.stdout, /zh: 2/);
  assert.ok(existsSync(join(out, "zh/index.html")) && existsSync(join(out, "zh/getting-started/overview/index.html")));
  assert.ok(!existsSync(join(out, "zh/guide/nodes/index.html")));
  assert.ok(existsSync(join(out, "zh/search-index.json")));
});

test("a zh page is lang zh-CN, has the zh interface, a callout and the English page behind a missing link", () => {
  const html = read("zh/getting-started/overview/index.html");
  assert.match(html, /<html lang="zh-CN">/);
  assert.match(html, /<meta property="og:locale" content="zh_CN">/);
  assert.match(html, /callout--note/);
  assert.match(html, /<span class="pending"/, "the link to the zh page that does not exist is not a link");
  assert.doesNotMatch(html, /href="\/zh\/getting-started\/add-node\/"/);
  assert.match(html, /<link rel="alternate" hreflang="zh-Hans" href="https:\/\/mistgate\.app\/zh\/getting-started\/overview\/">/);
  assert.match(html, /<link rel="alternate" hreflang="x-default" href="https:\/\/mistgate\.app\/getting-started\/overview\/">/);
});

test("the language menu lists three languages, links to the same page or else the language home, and marks the current one", () => {
  const en = menu(read("getting-started/overview/index.html"));
  assert.match(en, /<summary class="ib lang__btn" aria-label="Language">/);
  assert.match(en, /href="\/zh\/getting-started\/overview\/" lang="zh-CN" hreflang="zh-Hans"/);
  assert.match(en, /href="\/ru\/getting-started\/overview\/"/);
  assert.match(en, /<span class="lang__item" lang="en" aria-current="true">/, "the current language is not a link");
  assert.ok(en.indexOf("English") < en.indexOf("Русский") && en.indexOf("Русский") < en.indexOf("简体中文"), "English, Русский, 简体中文");
  // a page zh does not have yet: the zh entry leads to the zh home, never to a missing page
  const nodes = menu(read("guide/nodes/index.html"));
  assert.match(nodes, /href="\/zh\/" lang="zh-CN"/);
  assert.match(nodes, /href="\/ru\/guide\/nodes\/"/);
  // from zh: back to the same English and Russian pages
  const zh = menu(read("zh/getting-started/overview/index.html"));
  assert.match(zh, /<span class="lang__cur" lang="zh-CN">中文<\/span>/);
  assert.match(zh, /href="\/getting-started\/overview\/" lang="en"/);
  assert.match(zh, /<span class="lang__item" lang="zh-CN" aria-current="true">/);
  // the home page too
  assert.match(menu(read("ru/index.html")), /href="\/zh\/" lang="zh-CN"/);
});

test("the zh home keeps the structure, links the pages that exist in zh and falls back to English for the rest", () => {
  const html = read("zh/index.html");
  assert.match(html, /<h1 class="hero__h1">/);
  assert.match(html, /href="\/zh\/getting-started\/overview\/"/);
  assert.match(html, /href="\/getting-started\/install-panel\/"/, "install-panel is not translated yet: the English page");
  assert.match(html, /img\/overview-light-en\.webp/, "no zh screenshot yet: the English one");
  assert.match(html, /English · Русский · 简体中文/);
  assert.doesNotMatch(read("index.html").slice(read("index.html").indexOf("<body")).replace(/<details class="lang">[\s\S]*?<\/details>/, ""), /\/zh\//, "the English home links zh only through the menu");
});

test("sitemap, llms.txt, 404 and the zh search index know about zh", () => {
  const sitemap = read("sitemap.xml");
  assert.match(sitemap, /<loc>https:\/\/mistgate\.app\/zh\/getting-started\/overview\/<\/loc>/);
  assert.match(sitemap, /hreflang="zh-Hans" href="https:\/\/mistgate\.app\/zh\/getting-started\/overview\/"/);
  assert.doesNotMatch(sitemap, /\/zh\/guide\//);
  assert.match(read("llms.txt"), /\(https:\/\/mistgate\.app\/zh\/getting-started\/overview\/\)/);
  assert.match(read("404.html"), /href="\/zh\/" lang="zh-CN"/);
  const idx = JSON.parse(read("zh/search-index.json"));
  assert.deepEqual(idx.map((p) => p.t).sort(), ["Mistgate 文档", "概览"]);
  assert.equal(find(idx, "两个程序")[0].url, "/zh/getting-started/overview/#两个程序");
  assert.equal(find(idx, "适合谁")[0].url, "/zh/getting-started/overview/");
  // the redirect for the old M2 address is for languages that have the target page: not zh here
  assert.doesNotMatch(read("_redirects"), /\/zh\//);
  assert.match(read("_redirects"), /^\/ru\/roadmap\/m2-ssh-provisioning\/ \/ru\/roadmap\/status\/ 301$/m);
});

test("the shipped script has the matcher inlined and closes the language menu", () => {
  const js = readFileSync(join(out, readFileSync(join(out, "zh/index.html"), "utf8").match(/src="(\/assets\/app\.[0-9a-f]+\.js)"/)[1]), "utf8");
  assert.doesNotMatch(js, /@find/);
  assert.match(js, /const find = /);
  assert.match(js, /normalize\("NFKC"\)/);
  assert.match(js, /details\.lang/);
  assert.match(js, /isComposing/);
  assert.match(js, /const formatStars = /, "the star count is formatted by the same function as at build time");
});

test("the GitHub button carries the star count baked in at build time (MG_STARS here: no network), in each language", () => {
  assert.deepEqual([999, 1000, 1234, 12345].map(formatStars), ["999", "1k", "1.2k", "12.3k"]);
  for (const [page, label] of [["index.html", "Mistgate on GitHub, 1234 stars"], ["ru/index.html", "Mistgate на GitHub, звёзд: 1234"], ["zh/getting-started/overview/index.html", "GitHub 上的 Mistgate，1234 个星标"]]) {
    const button = /<a class="ib gh"[\s\S]*?<\/a>/.exec(read(page))?.[0] ?? "";
    assert.ok(button.includes(`aria-label="${label}"`), `${page}: ${button.slice(0, 160)}`);
    assert.match(button, /<span class="gh__stars"><svg[^>]*>[\s\S]*?<span class="gh__n">1\.2k<\/span>/, page);
  }
  assert.match(read("_headers"), /connect-src 'self' https:\/\/api\.github\.com;/, "the browser may refresh the count");
});
