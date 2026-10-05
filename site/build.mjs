// Mistgate docs site generator: docs/<lang>/**/*.md -> dist/ (static HTML, no runtime dependencies).
// Usage: node build.mjs        Pages missing from the index list or links to unwritten pages are warnings, not errors.
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync, copyFileSync, existsSync, statSync } from "node:fs";
import { join, dirname, relative, posix } from "node:path";
import { createHash } from "node:crypto";
import { fileURLToPath } from "node:url";
import YAML from "yaml";
import { createHl, createRenderer, parseNav } from "./src/markdown.mjs";
import { T, renderDoc, render404, groupStyle, THEME_INIT } from "./src/layout.mjs";
import { SITE, LANGS, pageUrl } from "./src/urls.mjs";

const ROOT = dirname(fileURLToPath(import.meta.url));
const DOCS = join(ROOT, "..", "docs");
const DIST = join(ROOT, "dist");
const SRC = join(ROOT, "src");
const BRAND = join(ROOT, "..", "web", "src");
const warnings = [];
const warn = (m) => warnings.push(m);

const out = (path, data) => { const f = join(DIST, path); mkdirSync(dirname(f), { recursive: true }); writeFileSync(f, data); };
const hashed = (name, ext, data) => `/assets/${name}.${createHash("sha256").update(data).digest("hex").slice(0, 8)}.${ext}`;

rmSync(DIST, { recursive: true, force: true });
mkdirSync(DIST, { recursive: true });

// ---- static assets ----
const css = readFileSync(join(SRC, "assets", "style.css"), "utf8");
const js = readFileSync(join(SRC, "assets", "app.js"), "utf8");
const assets = { css: hashed("style", "css", css), js: hashed("app", "js", js) };
out(assets.css, css);
out(assets.js, js);
for (const f of readdirSync(join(BRAND, "fonts"))) if (/\.(woff2|txt)$/.test(f)) { mkdirSync(join(DIST, "fonts"), { recursive: true }); copyFileSync(join(BRAND, "fonts", f), join(DIST, "fonts", f)); }
const badge = readFileSync(join(BRAND, "assets", "mistgate-badge.svg"));
out("mistgate-badge.svg", badge);
out("favicon.svg", badge);
for (const [file, dest, key] of [["og.png", "og.png", "og"], ["favicon-32.png", "favicon-32.png", "favicon32"], ["apple-touch-icon.png", "apple-touch-icon.png", "touch"]]) {
  const p = join(SRC, "assets", file);
  if (existsSync(p)) { out(dest, readFileSync(p)); assets[key] = "/" + dest; }
}

// ---- content ----
const walk = (dir) => readdirSync(dir).flatMap((n) => { const p = join(dir, n); return statSync(p).isDirectory() ? walk(p) : p.endsWith(".md") ? [p] : []; });
function readPage(lang, file) {
  const raw = readFileSync(file, "utf8").replace(/^﻿/, "").replace(/\r\n?/g, "\n");
  const m = /^---\n([\s\S]*?)\n---\n?/.exec(raw);
  const key = relative(join(DOCS, lang), file).replace(/\\/g, "/").replace(/\.md$/, "");
  let fm = {};
  try { fm = m ? YAML.parse(m[1]) ?? {} : {}; } catch {
    // an unquoted ": " inside a value is not valid YAML (GitHub shows it as an error too); read "key: rest of line" instead
    warn(`${lang}/${key}.md: front matter is not valid YAML (quote the value); read line by line`);
    fm = Object.fromEntries(m[1].split("\n").map((l) => /^(\w+):\s*(.*)$/.exec(l)).filter(Boolean).map((x) => [x[1], x[2].replace(/^(["'])(.*)\1$/, "$2")]));
  }
  if (!fm.title) warn(`${lang}/${key}.md: no title in front matter`);
  if (!fm.description) warn(`${lang}/${key}.md: no description in front matter`);
  return { lang, key, url: pageUrl(lang, key), title: String(fm.title ?? key), description: String(fm.description ?? ""), body: m ? raw.slice(m[0].length) : raw };
}

const highlight = await createHl();
const site = {}; // lang -> {pages: Map, nav: [...], seq: [...]}

for (const lang of LANGS) {
  const dir = join(DOCS, lang);
  if (!existsSync(dir)) { warn(`docs/${lang} does not exist`); continue; }
  const pages = new Map(walk(dir).map((f) => readPage(lang, f)).map((p) => [p.key, p]));
  if (!pages.has("index")) { warn(`docs/${lang}/index.md is missing: no home page, no sidebar order`); continue; }

  // sidebar order: the H3 groups and bullet links of index.md, and nothing else
  const keyOf = (href) => posix.normalize(href.replace(/\.md(#.*)?$/, ""));
  const listed = new Set(["index"]);
  const nav = [];
  parseNav(pages.get("index").body).forEach((g, n) => {
    const items = [];
    for (const l of g.links) {
      if (!/\.md(#.*)?$/.test(l.href) || /^[a-z]+:/i.test(l.href)) continue;
      const key = keyOf(l.href);
      if (!pages.has(key)) { warn(`${lang}/index.md lists ${key}.md, which does not exist yet (left out of the sidebar)`); continue; }
      if (listed.has(key)) continue;
      listed.add(key);
      items.push({ key, title: pages.get(key).title, url: pageUrl(lang, key) });
    }
    if (items.length) { const [icon, tone] = groupStyle(items[0].key.split("/")[0], n); nav.push({ title: g.title, icon, tone, items }); }
  });
  const orphans = [...pages.keys()].filter((k) => !listed.has(k));
  if (orphans.length) {
    orphans.forEach((k) => warn(`${lang}/${k}.md exists but is not listed in ${lang}/index.md (put into "${T[lang].other}")`));
    nav.push({ title: T[lang].other, icon: "tag", tone: "sage", items: orphans.map((key) => ({ key, title: pages.get(key).title, url: pageUrl(lang, key) })) });
  }
  const seq = [{ key: "index", title: pages.get("index").title, url: pageUrl(lang, "index") }, ...nav.flatMap((g) => g.items)];
  site[lang] = { pages, nav, seq };
}

// ---- render ----
const written = new Map(); // url -> Set of ids, for the link check
const searchIdx = {};
for (const lang of Object.keys(site)) {
  const { pages, nav, seq } = site[lang];
  const t = T[lang];
  const render = createRenderer(lang, t, highlight);
  searchIdx[lang] = [];
  for (const page of pages.values()) {
    page.headings = [];
    const w = (m) => warn(`${lang}/${page.key}.md: ${m}`);
    page.html = render(page.body, { lang, key: page.key, pages, warn: w, headings: page.headings });
    if (page.key === "index") page.lead = /^<p>([\s\S]*?)<\/p>\n/.exec(page.html)?.[1];
    const i = seq.findIndex((s) => s.key === page.key);
    const others = LANGS.filter((l) => l !== lang && site[l]?.pages.has(page.key));
    const switchLang = LANGS.find((l) => l !== lang);
    const alternates = [lang, ...others].map((l) => ({ lang: l, url: pageUrl(l, page.key) }));
    if (site.en?.pages.has(page.key)) alternates.push({ lang: "x-default", url: pageUrl("en", page.key) });
    const crumbGroup = nav.find((g) => g.items.some((x) => x.key === page.key))?.title;
    const html = renderDoc({
      lang, t, page, nav, assets, prev: seq[i - 1], next: seq[i + 1], crumbGroup, alternates,
      switchUrl: others.length ? pageUrl(switchLang, page.key) : pageUrl(switchLang, "index"),
    });
    out(page.url.slice(1) + "index.html", html);
    written.set(page.url, new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1])));
    page.alternates = alternates;
    searchIdx[lang].push({ t: page.title, d: page.description, g: crumbGroup ?? "", u: page.url, h: page.headings.filter((h) => h.level <= 3).map((h) => [h.title, h.id]) });
  }
  out(pageUrl(lang, "search-index.json").slice(1).replace(/\/$/, ""), JSON.stringify(searchIdx[lang]));
}
out("404.html", render404({ assets }));

// ---- sitemap, robots, headers ----
const urls = Object.values(site).flatMap((s) => [...s.pages.values()]);
const llmsPages = [...urls].sort((a, b) => a.url.localeCompare(b.url)).map((p) => `- [${p.title}](${SITE}${p.url}): ${p.description}`).join("\n");
out("llms.txt", `# Mistgate documentation\n\n> Self-hosted VPN fleet panel and Go node agent. The panel and node agent are static Go binaries; no Docker runtime is required.\n\n## AI and installation\n\n- [AI agent guide](${SITE}/getting-started/ai-agents/): ready prompts to install the panel over SSH, run the fleet through owner-approved MCP tools, and contribute to the code.\n- [Install the panel](${SITE}/getting-started/install-panel/): the official release binaries, mistgate setup and the systemd unit.\n- [Install a node over SSH](${SITE}/getting-started/ssh-install/)\n- [Repository agent instructions](https://github.com/Mistgate/mistgate/blob/main/AGENTS.md)\n- [MCP tool reference](${SITE}/reference/mcp/)\n\n## All pages\n\n${llmsPages}\n`);
out("sitemap.xml", `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">
${urls.map((p) => `<url><loc>${SITE}${p.url}</loc>${p.alternates.length > 1 ? "\n" + p.alternates.map((a) => `  <xhtml:link rel="alternate" hreflang="${a.lang}" href="${SITE}${a.url}"/>`).join("\n") + "\n" : ""}</url>`).join("\n")}
</urlset>
`);
out("robots.txt", `User-agent: *\nAllow: /\n\nSitemap: ${SITE}/sitemap.xml\n`);
// pages that moved: old address -> new page key (Cloudflare Pages reads _redirects)
const MOVED = { "roadmap/m2-ssh-provisioning": "roadmap/status" };
out("_redirects", Object.entries(MOVED).flatMap(([from, to]) => LANGS.flatMap((l) => {
  const src = pageUrl(l, from), dst = pageUrl(l, to);
  return [`${src} ${dst} 301`, `${src.slice(0, -1)} ${dst} 301`];
})).join("\n") + "\n");
const initHash = createHash("sha256").update(THEME_INIT).digest("base64");
out("_headers", `/*
  X-Content-Type-Options: nosniff
  Referrer-Policy: strict-origin-when-cross-origin
  X-Frame-Options: DENY
  Permissions-Policy: camera=(), microphone=(), geolocation=(), interest-cohort=()
  Content-Security-Policy: default-src 'self'; script-src 'self' 'sha256-${initHash}'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'

/assets/*
  Cache-Control: public, max-age=31536000, immutable

/fonts/*
  Cache-Control: public, max-age=2592000
`);

// ---- link check: every internal href/src must land on a written page, asset or anchor ----
const files = new Set(walk2(DIST).map((f) => "/" + relative(DIST, f).replace(/\\/g, "/")));
function walk2(d) { return readdirSync(d).flatMap((n) => { const p = join(d, n); return statSync(p).isDirectory() ? walk2(p) : [p]; }); }
const broken = [];
for (const f of files) {
  if (!f.endsWith(".html")) continue;
  const html = readFileSync(join(DIST, f), "utf8");
  const self = f.replace(/index\.html$/, "");
  for (const m of html.matchAll(/(?:href|src)="([^"]+)"/g)) {
    const h = m[1];
    if (/^(https?:|mailto:|data:)/.test(h)) continue;
    const [path, rawFrag] = h.split("#");
    const frag = rawFrag && decodeURIComponent(rawFrag); // markdown-it percent-encodes a Cyrillic anchor; the id is raw
    const target = path === "" ? self : path;
    const ok = written.has(target) ? (!frag || written.get(target).has(frag) || (frag === "content" || frag === "nav")) : files.has(target);
    if (!ok) broken.push(`${f}: ${h}`);
  }
}

// ---- report ----
const nPages = urls.length;
console.log(`built ${nPages} pages (${Object.entries(site).map(([l, s]) => `${l}: ${s.pages.size}`).join(", ")}) -> dist/ , ${files.size} files`);
const uniq = [...new Set(warnings)];
if (uniq.length) console.log(`\n${uniq.length} warning(s):\n` + uniq.map((w) => "  - " + w).join("\n"));
if (broken.length) { console.log(`\n${broken.length} BROKEN internal link(s):\n` + [...new Set(broken)].map((b) => "  ! " + b).join("\n")); process.exitCode = 1; }
else console.log("\nno broken internal links");
