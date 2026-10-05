// Mistgate docs -> GitHub Wiki: docs/<lang>/**/*.md -> flat directory of wiki pages (+ Home, _Sidebar, _Footer).
// Usage: node wiki.mjs --out <dir> [--clean] [--docs <dir>]     (pnpm --dir site wiki -- --out <dir>)
// Works from whatever pages exist: titles, order and sections come from the pages and from <lang>/index.md only.
// Broken page links fail the run (nothing is written); broken #anchors are warnings.
import { readFileSync, writeFileSync, mkdirSync, readdirSync, rmSync, statSync, existsSync } from "node:fs";
import { join, dirname, relative, resolve, posix } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import YAML from "yaml";
import { parseNav } from "./src/markdown.mjs";
import { SITE, GITHUB, LANGS, pageUrl, editUrl } from "./src/urls.mjs";

const RAW = GITHUB.replace("github.com", "raw.githubusercontent.com");
const RESERVED = new Set(["home", "_sidebar", "_footer"]);

const T = {
  en: {
    name: "English", other: "Other pages", edit: "Edit this page", site: "View on mistgate.app", start: "Start page",
    intro: "**Mistgate** is a self-hosted panel for your own VPN fleet: one binary for the panel, one for the node agent, Hysteria2 and AmneziaWG in one subscription.",
    footer: `Generated from [docs/](${GITHUB}/tree/main/docs) in [Mistgate/mistgate](${GITHUB}) — edit the docs, not the wiki.`,
  },
  ru: {
    name: "Русский", other: "Другие страницы", edit: "Редактировать страницу", site: "Страница на mistgate.app", start: "Начальная страница",
    intro: "**Mistgate** — панель для собственного VPN-флота: один бинарь для панели, один для агента ноды, Hysteria2 и AmneziaWG в одной подписке.",
    footer: `Сгенерировано из [docs/](${GITHUB}/tree/main/docs) в [Mistgate/mistgate](${GITHUB}) — правьте документацию, а не вики.`,
  },
};

const ALERTS = { note: "NOTE", warning: "WARNING", "важно": "NOTE", "внимание": "WARNING" };

// ---- naming ----------------------------------------------------------------------------------

/** Page title -> wiki page name (spaces kept; GitHub shows the file name with dashes as spaces). */
export const sanitize = (t) => t.replace(/[\\/]/g, "-").replace(/[:*?"<>|#\[\]%]/g, " ").replace(/\s+/g, " ").trim().replace(/[. ]+$/, "");
export const fileBase = (name) => name.replace(/ /g, "-");
/** Link target for a page name: parentheses and friends percent-encoded so Markdown cannot trip over "(RU)". */
export const wikiHref = (name) => fileBase(name).replace(/[()%<>]/g, (c) => "%" + c.charCodeAt(0).toString(16).toUpperCase());
const cap = (s) => s.charAt(0).toUpperCase() + s.slice(1);

/** GitHub's heading anchor: lower case, drop punctuation, spaces -> dashes (duplicates get -1, -2 ... from the caller). */
export const ghSlug = (text) => text.toLowerCase().replace(/[^\p{L}\p{M}\p{N}_ -]/gu, "").replace(/ /g, "-");

/** Deterministic, order-independent unique names. EN keeps its title; other languages get " (RU)" on a cross-language clash;
 *  a clash inside one language adds the section dir to all members; the key is the last resort. */
export function assignNames(pages) {
  for (const p of pages) {
    p.name = sanitize(p.title) || p.key.split("/").pop();
    if (RESERVED.has(fileBase(p.name).toLowerCase())) p.name += " (docs)";
  }
  const groups = () => {
    const g = new Map();
    for (const p of pages) { const k = fileBase(p.name).toLowerCase(); g.set(k, [...(g.get(k) ?? []), p]); }
    return [...g.values()].filter((x) => x.length > 1);
  };
  for (const g of groups()) if (g.some((p) => p.lang === LANGS[0])) for (const p of g) if (p.lang !== LANGS[0]) p.name += ` (${p.lang.toUpperCase()})`;
  for (const g of groups()) for (const p of g) p.name += ` (${cap(p.key.includes("/") ? p.key.split("/")[0] : p.key)})`;
  for (const g of groups()) for (const p of g) p.name += " " + p.key.replace(/\//g, " ");
  const seen = new Set();
  for (const p of pages) { // cannot happen with unique keys; keeps the run safe if it ever does
    let n = 2, base = p.name;
    while (seen.has(fileBase(p.name).toLowerCase())) p.name = `${base} ${n++}`;
    seen.add(fileBase(p.name).toLowerCase());
  }
}

// ---- page parsing ----------------------------------------------------------------------------

export function parsePage(lang, key, rawText, warn = () => {}) {
  const raw = rawText.replace(/^﻿/, "").replace(/\r\n?/g, "\n");
  const m = /^---\n([\s\S]*?)\n---\n?/.exec(raw);
  let fm = {};
  try { fm = m ? YAML.parse(m[1]) ?? {} : {}; } catch {
    warn(`${lang}/${key}.md: front matter is not valid YAML (quote the value); read line by line`);
    fm = Object.fromEntries(m[1].split("\n").map((l) => /^(\w+):\s*(.*)$/.exec(l)).filter(Boolean).map((x) => [x[1], x[2].replace(/^(["'])(.*)\1$/, "$2")]));
  }
  if (!fm.title) warn(`${lang}/${key}.md: no title in front matter (using the file name)`);
  return {
    lang, key,
    title: String(fm.title ?? key.split("/").pop()).replace(/\s+/g, " ").trim(),
    description: String(fm.description ?? "").replace(/\s+/g, " ").trim(),
    body: (m ? raw.slice(m[0].length) : raw).replace(/^\n+/, ""),
  };
}

// ---- Markdown rewriting ----------------------------------------------------------------------

const FENCE = /^\s*(`{3,}|~{3,})/;
/** Call fn(line, inFence) for every line; fn returns the replacement line. */
function mapLines(body, fn) {
  let fence = null;
  return body.split("\n").map((line) => {
    const f = FENCE.exec(line);
    if (fence) {
      if (f && f[1][0] === fence[0] && f[1].length >= fence.length && /^\s*[`~]+\s*$/.test(line)) fence = null;
      return line;
    }
    if (f) { fence = f[1]; return line; }
    return fn(line);
  });
}

/** Headings of a body with their GitHub anchors (duplicates numbered like GitHub does). */
export function headingAnchors(body) {
  const seen = new Map(), out = new Set();
  mapLines(body, (line) => {
    const m = /^ {0,3}#{1,6}\s+(.*?)(?:\s+#+)?\s*$/.exec(line);
    if (m) {
      const text = m[1].replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1").replace(/[`*]/g, "");
      const s = ghSlug(text), n = seen.get(s) ?? 0;
      seen.set(s, n + 1);
      out.add(n ? `${s}-${n}` : s);
    }
    return line;
  });
  return out;
}

const LINK = /(!?)\[((?:[^\[\]\\]|\\.|\[[^\]]*\])*)\]\(\s*<?([^)\s>]*)>?((?:\s+(?:"[^"]*"|'[^']*'))?)\s*\)/g;
const REFDEF = /^(\s{0,3}\[[^\]]+\]:\s*)<?(\S+?)>?(\s.*)?$/;

function mapInline(line, fn) {
  const spans = [];
  const masked = line.replace(/(`+)(?!`)[\s\S]*?[^`]\1(?!`)/g, (s) => `\u0000${spans.push(s) - 1}\u0000`);
  return fn(masked).replace(/\u0000(\d+)\u0000/g, (_, i) => spans[i]);
}

/**
 * Rewrite one page body: .md links -> wiki links, repo-file links -> absolute GitHub URLs, images -> raw URLs, callouts -> alerts.
 * ctx: {lang, key, byPath: Map("docs/en/x.md" -> page), links: [], problems: []}
 */
export function rewriteBody(body, ctx) {
  const dir = posix.dirname(ctx.key) === "." ? "" : posix.dirname(ctx.key);
  const here = (suffix = "") => `${ctx.lang}/${ctx.key}.md${suffix}`;

  function target(href) {
    if (href === "" || /^[a-z][a-z0-9+.-]*:/i.test(href)) return href; // empty, http(s):, mailto:, ...
    const [pathAndQuery, ...fragParts] = href.split("#");
    const frag = fragParts.length ? fragParts.join("#") : null;
    let path = pathAndQuery.split("?")[0];
    try { path = decodeURI(path); } catch { /* keep as written */ }
    if (path === "") { // #anchor on the same page
      const self = ctx.byPath.get(`docs/${ctx.lang}/${ctx.key}.md`);
      ctx.links.push({ from: here(), page: self, anchor: frag });
      return `${wikiHref(self.name)}#${frag}`;
    }
    if (path.startsWith("/")) { ctx.problems.push(`${here()}: root-relative link ${href} (use a relative .md link)`); return href; }
    const repoPath = posix.normalize(posix.join("docs", ctx.lang, dir, path));
    if (repoPath.startsWith("..")) { ctx.problems.push(`${here()}: ${href} points outside the repository`); return href; }
    const page = ctx.byPath.get(repoPath);
    if (page) { ctx.links.push({ from: here(), page, anchor: frag }); return wikiHref(page.name) + (frag ? `#${frag}` : ""); }
    if (/^docs\/(en|ru)\/.*\.md$/.test(repoPath)) { ctx.problems.push(`${here()}: link to ${href} -> ${repoPath}, no such documentation page`); return href; }
    const kind = path.endsWith("/") ? "tree" : "blob";
    return `${GITHUB}/${kind}/main/${repoPath.replace(/\/$/, "")}${frag ? `#${frag}` : ""}`;
  }

  const image = (href) => {
    if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return href;
    const repoPath = posix.normalize(posix.join("docs", ctx.lang, dir, href.split(/[?#]/)[0]));
    if (repoPath.startsWith("..")) { ctx.problems.push(`${here()}: image ${href} points outside the repository`); return href; }
    return `${RAW}/main/${repoPath}`;
  };

  let prevQuote = false;
  const lines = mapLines(body, (line) => {
    const quote = /^\s*>/.test(line);
    let out = line;
    const alert = !prevQuote && /^(\s*)>\s?\*\*([^*:]+):\*\*\s*(.*)$/.exec(line);
    const kind = alert && ALERTS[alert[2].trim().toLowerCase()];
    if (kind) out = `${alert[1]}> [!${kind}]` + (alert[3] ? `\n${alert[1]}> ${alert[3]}` : "");
    prevQuote = quote;
    const ref = !kind && REFDEF.exec(out);
    if (ref) return ref[1] + target(ref[2]) + (ref[3] ?? "");
    return mapInline(out, (s) => s.replace(LINK, (_m, bang, text, href, title) => `${bang}[${text}](${bang ? image(href) : target(href)}${title})`));
  });
  return lines.join("\n");
}

// ---- site model ------------------------------------------------------------------------------

/** Sidebar/Home order of one language: the H3 groups and bullet links of index.md, then pages nobody lists. */
function buildNav(lang, pages, warn) {
  const index = pages.get("index");
  const rest = [...pages.keys()].filter((k) => k !== "index").sort();
  if (!index) { warn(`${lang}/index.md is missing: no section order, pages are listed alphabetically`); return [{ title: T[lang].other, items: rest.map((k) => pages.get(k)) }]; }
  const listed = new Set(["index"]);
  const nav = [];
  for (const g of parseNav(index.body)) {
    const items = [];
    for (const l of g.links) {
      if (!/\.md(#.*)?$/.test(l.href) || /^[a-z]+:/i.test(l.href)) continue;
      const key = posix.normalize(l.href.replace(/\.md(#.*)?$/, ""));
      if (!pages.has(key)) { warn(`${lang}/index.md lists ${key}.md, which does not exist`); continue; }
      if (listed.has(key)) continue;
      listed.add(key);
      items.push(pages.get(key));
    }
    if (items.length) nav.push({ title: g.title, items });
  }
  const orphans = rest.filter((k) => !listed.has(k));
  if (orphans.length) {
    warn(`${orphans.map((k) => `${lang}/${k}.md`).join(", ")} not listed in ${lang}/index.md (put under "${T[lang].other}")`);
    nav.push({ title: T[lang].other, items: orphans.map((k) => pages.get(k)) });
  }
  return nav;
}

const esc = (s) => s.replace(/([\[\]])/g, "\\$1");
const pageLink = (p) => `[${esc(p.title)}](${wikiHref(p.name)})`;

/**
 * sources: {en: {"guide/nodes": "<raw md>", ...}, ru: {...}}  ->  {files: Map("Page-Name.md" -> text), errors, warnings, pages}
 */
export function buildWiki(sources) {
  const warnings = [], errors = [];
  const warn = (m) => warnings.push(m);
  const byLang = {};
  for (const lang of LANGS) {
    byLang[lang] = new Map(Object.keys(sources[lang] ?? {}).sort().map((k) => [k, parsePage(lang, k, sources[lang][k], warn)]));
    if (!byLang[lang].size) warn(`no documentation pages for "${lang}"`);
  }
  const all = LANGS.flatMap((l) => [...byLang[l].values()]);
  assignNames(all);
  const byPath = new Map(all.map((p) => [`docs/${p.lang}/${p.key}.md`, p]));
  const nav = Object.fromEntries(LANGS.map((l) => [l, byLang[l].size ? buildNav(l, byLang[l], warn) : []]));

  const files = new Map();
  const links = [], problems = [];
  for (const p of all) {
    const t = T[p.lang];
    const body = rewriteBody(p.body, { lang: p.lang, key: p.key, byPath, links, problems });
    const lead = p.description ? rewriteBody(p.description, { lang: p.lang, key: p.key, byPath, links, problems }) : "";
    const foot = `<sub>[${t.edit}](${editUrl(p.lang, p.key)}) · [${t.site}](${SITE}${pageUrl(p.lang, p.key)})</sub>`;
    files.set(`${fileBase(p.name)}.md`, `${lead ? `*${lead}*\n\n` : ""}${body.trim()}\n\n---\n\n${foot}\n`);
  }

  // Home, sidebar, footer
  const start = Object.fromEntries(LANGS.map((l) => [l, byLang[l].get("index")]));
  const section = (lang, level) => {
    const out = [];
    if (start[lang]) out.push(`- ${pageLink(start[lang])}${start[lang].description ? `: ${start[lang].description}` : ""}`, "");
    for (const g of nav[lang]) out.push(`${"#".repeat(level)} ${g.title}`, "", ...g.items.map((p) => `- ${pageLink(p)}${p.description ? `: ${p.description}` : ""}`), "");
    return out;
  };
  const startLinks = LANGS.filter((l) => start[l]).map((l) => `**${T[l].name}:** [${T[l].start}](${wikiHref(start[l].name)})`).join(" · ");
  files.set("Home.md", [
    "# Mistgate documentation · Документация Mistgate", "",
    T.en.intro, "", T.ru.intro, "",
    startLinks, "",
    `Website: [mistgate.app](${SITE}) · Repository: [Mistgate/mistgate](${GITHUB}) · Сайт: [mistgate.app/ru/](${SITE}/ru/)`, "",
    ...LANGS.filter((l) => byLang[l].size).flatMap((l) => [`## ${T[l].name}`, "", ...section(l, 3)]),
  ].join("\n").replace(/\n{3,}/g, "\n\n").trim() + "\n");

  const side = ["**[Mistgate](Home)**", ""];
  for (const l of LANGS.filter((x) => byLang[x].size)) {
    side.push(`<details${l === LANGS[0] ? " open" : ""}>`, `<summary><b>${T[l].name}</b></summary>`, "");
    if (start[l]) side.push(`- ${pageLink(start[l])}`, "");
    for (const g of nav[l]) side.push(`**${g.title}**`, "", ...g.items.map((p) => `- ${pageLink(p)}`), "");
    side.push("</details>", "");
  }
  files.set("_Sidebar.md", side.join("\n").trim() + "\n");
  files.set("_Footer.md", LANGS.map((l) => T[l].footer).join("<br>\n") + "\n");

  // validation: every generated link must hit a generated page; anchors are checked too (warnings)
  errors.push(...new Set(problems));
  const anchorsOf = new Map(all.map((p) => [p, headingAnchors(p.body)]));
  for (const l of links) {
    let a = l.anchor;
    if (a === null) continue;
    try { a = decodeURIComponent(a); } catch { /* keep as written */ }
    if (!anchorsOf.get(l.page).has(a.toLowerCase())) warn(`${l.from}: anchor #${l.anchor} not found in ${l.page.lang}/${l.page.key}.md`);
  }
  return { files, errors, warnings: [...new Set(warnings)], pages: all };
}

// ---- CLI -------------------------------------------------------------------------------------

export function loadDocs(docsDir) {
  const walk = (d) => readdirSync(d).sort().flatMap((n) => { const p = join(d, n); return statSync(p).isDirectory() ? walk(p) : p.endsWith(".md") ? [p] : []; });
  const out = {};
  for (const lang of LANGS) {
    const dir = join(docsDir, lang);
    out[lang] = {};
    if (existsSync(dir)) for (const f of walk(dir)) out[lang][relative(dir, f).replace(/\\/g, "/").replace(/\.md$/, "")] = readFileSync(f, "utf8");
  }
  return out;
}

function main() {
  const { values } = parseArgs({ args: process.argv.slice(2).filter((a) => a !== "--"), options: { out: { type: "string" }, docs: { type: "string" }, clean: { type: "boolean" } } });
  if (!values.out) { console.error("usage: node wiki.mjs --out <dir> [--clean] [--docs <dir>]"); process.exit(2); }
  const docs = resolve(values.docs ?? join(dirname(fileURLToPath(import.meta.url)), "..", "docs"));
  const out = resolve(values.out);
  const { files, errors, warnings, pages } = buildWiki(loadDocs(docs));
  if (warnings.length) console.log(`${warnings.length} warning(s):\n` + warnings.map((w) => "  - " + w).join("\n") + "\n");
  if (errors.length) { console.error(`${errors.length} BROKEN link(s), nothing written:\n` + errors.map((e) => "  ! " + e).join("\n")); process.exit(1); }
  mkdirSync(out, { recursive: true });
  if (values.clean) for (const n of readdirSync(out)) if (n.endsWith(".md")) rmSync(join(out, n));
  for (const [name, text] of files) writeFileSync(join(out, name), text);
  console.log(`wiki: ${pages.length} pages + Home, _Sidebar, _Footer (${files.size} files) -> ${out}`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) main();
