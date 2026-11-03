// Persian (right to left) and Turkish on the site while they are being translated: a fixture in a temp dir owns docs/fa and
// docs/tr (two pages each; the real ones are left out), so the checks do not move with the translators. The menu lists five
// languages, a Persian page is rtl in Vazirmatn, the sidebar lists every English page with an EN mark on the untranslated
// ones, links to them go to English (with a valid anchor), callouts read the fa/tr words, and missing drawings and posters
// fall back to English.
import assert from "node:assert/strict";
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { spawnSync } from "node:child_process";
import test, { after, before } from "node:test";
import { fileURLToPath } from "node:url";
import vm from "node:vm";
import { T } from "../src/layout.mjs";
import { fold } from "../src/markdown.mjs";

const site = dirname(dirname(fileURLToPath(import.meta.url)));
const docs = join(site, "..", "docs");
const find = vm.runInNewContext(`${readFileSync(join(site, "src/assets/find.js"), "utf8")}\nfind`);
const ZWNJ = String.fromCharCode(0x200c);

const root = mkdtempSync(join(tmpdir(), "mg-fa-tr-"));
const out = join(root, "dist");
let build;
const read = (p) => readFileSync(join(out, p), "utf8");
const menu = (html) => /<details class="lang">[\s\S]*?<\/details>/.exec(html)?.[0] ?? "";
const side = (html) => /<nav class="side"[\s\S]*?<\/nav>/.exec(html)?.[0] ?? "";
const enPages = () => (read("search-index.json").match(/"u":"/g) ?? []).length;

before(() => {
  const own = [join(docs, "fa"), join(docs, "tr")];
  cpSync(docs, join(root, "docs"), { recursive: true, filter: (src) => !own.some((d) => src === d || src.startsWith(d + "\\") || src.startsWith(d + "/")) });
  const put = (p, text) => { const f = join(root, "docs", p); mkdirSync(dirname(f), { recursive: true }); writeFileSync(f, text); };
  put("fa/index.md", "---\ntitle: مستندات Mistgate\ndescription: مستندات پنل.\n---\n\n## همهٔ صفحه‌ها\n\n### شروع کار\n\n- [نمای کلی](getting-started/overview.md): معرفی\n- [افزودن نود](getting-started/add-node.md): هنوز ترجمه نشده\n");
  put("fa/getting-started/overview.md", [
    "---\ntitle: نمای کلی\ndescription: Mistgate چیست و اجزایش چطور کنار هم کار می‌کنند.\n---\n",
    "## دو برنامه\n",
    "نود را با [افزودن نود](add-node.md) اضافه کنید، [بررسی از دید کلاینت](../operations/health.md#client-eye-checks) را ببینید و [این پیوند](../operations/health.md#نیست) را.\n",
    "> **نکته:** یادداشت.\n", "> **توصیه:** پیشنهاد.\n", "> **هشدار:** اخطار.\n", "> **خطر:** خطرناک.\n",
    "![اجزای Mistgate](../diagrams/how-it-fits.svg)\n",
    "![نمایش سلامت](../../images/video/health-poster-fa.webp)\n",
    "## نصب ۱۲\n\nمتن.\n",
  ].join("\n"));
  put("tr/index.md", "---\ntitle: Mistgate belgeleri\ndescription: Panelin belgeleri.\n---\n\n## Tüm sayfalar\n\n### Başlarken\n\n- [Genel bakış](getting-started/overview.md): giriş\n");
  put("tr/getting-started/overview.md", [
    "---\ntitle: Genel bakış\ndescription: Mistgate nedir ve parçaları nasıl birleşir.\n---\n",
    "## İki program\n", "> **Not:** not.\n", "> **İPUCU:** ipucu.\n", "> **uyarı:** uyarı.\n", "> **Tehlike:** tehlike.\n",
  ].join("\n"));
  build = spawnSync(process.execPath, ["build.mjs"], { cwd: site, env: { ...process.env, MG_DOCS: join(root, "docs"), MG_DIST: out, MG_STARS: "" }, encoding: "utf8" });
});
after(() => rmSync(root, { recursive: true, force: true }));

test("a build with two fa and two tr pages succeeds and links nothing that is missing", () => {
  assert.equal(build.status, 0, build.stdout + build.stderr);
  assert.match(build.stdout, /no broken internal links/);
  assert.match(build.stdout, /fa: 2, tr: 2/);
  assert.ok(existsSync(join(out, "fa/getting-started/overview/index.html")) && !existsSync(join(out, "fa/getting-started/add-node/index.html")));
});

test("the language menu lists five languages in order, the same page where it exists, else that language's home", () => {
  const names = ["English", "Русский", "简体中文", "فارسی", "Türkçe"];
  for (const page of ["getting-started/overview/index.html", "fa/getting-started/overview/index.html", "tr/index.html"]) {
    const m = menu(read(page));
    const at = names.map((n) => m.indexOf(`>${n}<`));
    assert.ok(at.every((x, i) => x > 0 && (i === 0 || x > at[i - 1])), `${page}: ${names.join(", ")}`);
  }
  const en = menu(read("getting-started/overview/index.html"));
  assert.match(en, /href="\/fa\/getting-started\/overview\/" lang="fa" hreflang="fa"/);
  assert.match(en, /href="\/tr\/getting-started\/overview\/" lang="tr" hreflang="tr"/);
  assert.match(menu(read("guide/nodes/index.html")), /href="\/fa\/" lang="fa"/, "a page fa lacks: the fa home");
  assert.match(menu(read("fa/index.html")), /<span class="lang__cur" lang="fa">FA<\/span>/);
});

test("a Persian page is right to left in Vazirmatn; a Turkish one preloads the letters it needs", () => {
  const fa = read("fa/getting-started/overview/index.html");
  assert.match(fa, /<html lang="fa" dir="rtl">/);
  assert.match(fa, /<meta property="og:locale" content="fa_IR">/);
  assert.match(fa, /<link rel="preload" href="\/fonts\/vazirmatn-arabic\.woff2" as="font"/);
  assert.ok(existsSync(join(out, "fonts/vazirmatn-arabic.woff2")) && existsSync(join(out, "fonts/OFL-Vazirmatn.txt")), "the font and its licence are published");
  const css = read(/href="(\/assets\/style\.[0-9a-f]+\.css)"/.exec(fa)[1].slice(1));
  assert.match(css, /@font-face \{ font-family: "Vazirmatn";[^}]*url\("\/fonts\/vazirmatn-arabic\.woff2"\)[^}]*unicode-range: U\+0600-06FF/);
  assert.match(css, /:root:lang\(fa\)[^{]*\{\s*--font-sans: "Onest", "Vazirmatn",/, "Vazirmatn right after Onest, for Persian only");
  assert.doesNotMatch(css.slice(0, css.indexOf("[dir=\"rtl\"]")), /--font-sans:[^;]*Vazirmatn/, "the other languages' stack has no Vazirmatn");
  assert.match(css, /code, kbd, samp \{[^}]*direction: ltr; unicode-bidi: isolate;/, "code stays left to right");
  assert.match(css, /\[dir="rtl"\] \.ico--flip \{ scale: -1 1; \}/);
  assert.match(fa, /<a class="pn pn--next" href="[^"]+" rel="next"><span class="label">بعدی<svg class="ico ico--flip"/, "next points along the line");
  const tr = read("tr/getting-started/overview/index.html");
  assert.match(tr, /<html lang="tr">/);
  assert.match(tr, /<link rel="preload" href="\/fonts\/onest-latin-ext\.woff2"/);
});

test("the fa sidebar lists every English page; an untranslated one opens the English page with an EN mark", () => {
  const nav = side(read("fa/getting-started/overview/index.html"));
  assert.equal((nav.match(/class="nav-link"/g) ?? []).length, enPages(), "home + every English page");
  assert.equal((nav.match(/class="en-mark"/g) ?? []).length, enPages() - 2, "all but the two fa pages are marked");
  assert.match(nav, /href="\/fa\/getting-started\/overview\/" aria-current="page">/);
  // listed in fa/index.md: the name it gives there
  assert.match(nav, /href="\/getting-started\/add-node\/" hreflang="en"><span class="nav-ico"[^>]*>[\s\S]*?<\/span><span>افزودن نود<\/span><span class="en-mark" title="[^"]+"><span aria-hidden="true">EN<\/span>/);
  // not listed there: the English title, marked as English, in the group English puts it in
  assert.match(nav, /href="\/guide\/nodes\/" hreflang="en">[\s\S]*?<span lang="en" dir="ltr">Nodes<\/span><span class="en-mark"/);
  assert.match(nav, /<div class="grp__label">شروع کار<\/div>[\s\S]*?href="\/getting-started\/ssh-install\/"/, "a getting-started page joins the fa group");
  assert.doesNotMatch(side(read("tr/index.html")), /href="\/tr\/getting-started\/add-node\/"/);
});

test("links from a fa page to an untranslated page go to English, with the anchor only when the English page has it", () => {
  const fa = read("fa/getting-started/overview/index.html");
  assert.match(fa, /<a href="\/getting-started\/add-node\/" hreflang="en">افزودن نود<\/a>/);
  assert.match(fa, /<a href="\/operations\/health\/#client-eye-checks" hreflang="en">/);
  assert.match(fa, /<a href="\/operations\/health\/" hreflang="en">این پیوند<\/a>/);
  assert.match(build.stdout, /the English page has no #نیست, linked without it/);
  assert.doesNotMatch(fa, /class="pending"/);
});

test("callouts read the Persian and Turkish words, any case, and show the translated labels", () => {
  const fa = read("fa/getting-started/overview/index.html"), tr = read("tr/getting-started/overview/index.html");
  for (const [html, t] of [[fa, T.fa], [tr, T.tr]]) {
    for (const kind of ["note", "tip", "warning", "danger"]) {
      assert.match(html, new RegExp(`<div class="callout callout--${kind}" role="note">\\s*<div class="callout__label"><span class="id-chip"[^>]*>[\\s\\S]*?</span><span>${t.callouts[kind]}</span></div>`), `${t.htmlLang} ${kind}`);
    }
  }
  assert.deepEqual(["İPUCU", "İpucu", "ipucu", "UYARI", "Uyarı"].map(fold), ["ipucu", "ipucu", "ipucu", "uyari", "uyari"]);
});

test("a drawing or a video a language does not have yet is the English one", () => {
  const fa = read("fa/getting-started/overview/index.html");
  assert.match(fa, /<figure class="diagram diagram--svg" tabindex="0"><svg[^>]*class="mgd[ "][^>]*aria-label="اجزای Mistgate"/, "the English drawing, named by the fa alt text");
  assert.match(build.stdout, /fa\/getting-started\/overview\.md: diagram \.\.\/diagrams\/how-it-fits\.svg is not translated yet/);
  assert.match(fa, /<video src="\/video\/health-en\.mp4" poster="\/video\/health-poster-en\.webp"/);
  assert.match(build.stdout, /health-poster-fa\.webp does not exist, showing health-poster-en\.webp/);
});

test("the fa and tr homes are translated, play their own demo and send untranslated pages to English with the mark", () => {
  for (const [lang, h1] of [["fa", "پنلی برای VPN کوچک خودتان"], ["tr", "Kendi küçük VPN'iniz için bir panel"]]) {
    const html = read(`${lang}/index.html`);
    assert.ok(html.includes(`<h1 class="hero__h1">${h1}</h1>`), `${lang} h1`);
    assert.match(html, new RegExp(`video/hero-${lang}\\.mp4`));
    assert.match(html, /English · Русский · 简体中文 · فارسی · Türkçe/);
    assert.match(html, new RegExp(`href="/${lang}/getting-started/overview/"`));
    assert.match(html, /<li><a href="\/guide\/nodes\/"><span[^>]*>[^<]+<\/span><span class="en-mark"/, `${lang}: section cards mark English pages`);
  }
  assert.match(read("fa/index.html"), /<a class="btn btn--primary" href="\/getting-started\/install-panel\/">نصب پنل<svg class="ico ico--flip"/);
  assert.match(read("404.html"), /<p class="doc-lead" lang="fa" dir="rtl">/);
  assert.ok(read("tr/index.html").includes('<th lang="en">Mistgate</th><th lang="en">3x-ui</th>'), "product names keep English capitals on a Turkish page");
});

test("the fa search index holds its pages and finds the English ones by the sidebar's name; the matcher folds fa and tr", () => {
  const idx = JSON.parse(read("fa/search-index.json"));
  assert.equal(idx.length, enPages(), "two fa pages and the English rest");
  assert.equal(find(idx, "نمای کلی")[0].url, "/fa/getting-started/overview/");
  assert.equal(find(idx, "افزودن")[0].url, "/getting-started/add-node/", "an English page under its fa name");
  assert.equal(idx.find((p) => p.u === "/getting-started/ssh-install/").g, "شروع کار");
  assert.ok(find(idx, "12").some((h) => h.url === "/fa/getting-started/overview/#نصب-۱۲"), "Persian digits are found by Latin ones");
  const data = [{ t: "İpucu ve ışık", d: "", g: "", u: "/tr/a/", h: [], k: [] }, { t: "نصب پنل", d: `پنل را نصب می${ZWNJ}کنید 12`, g: "", u: "/fa/b/", h: [], k: [] }];
  for (const q of ["ipucu", "IPUCU", "IŞIK", "میکنید", `می${ZWNJ}کنید`, "۱۲", "نصب پنل"]) assert.ok(find(data, q).length, q);
});
