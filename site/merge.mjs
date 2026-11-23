// One folder for mistgate.app: the documentation (build.mjs -> dist/) and the new landing (landing/ -> landing/dist/).
//   node merge.mjs              build both, merge, check, write public/        (pnpm build:all)
//   node merge.mjs --no-build   merge what dist/ and landing/dist/ already hold
// The docs write their start page at /docs/ (and /ru/docs/ ...), the landing the home pages /, /ru/ ...; a path may come from
// one side only. _headers and sitemap.xml are merged, not copied. Any problem: nothing is written, exit 1.
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync, copyFileSync, existsSync, statSync } from "node:fs";
import { join, dirname, relative, posix } from "node:path";
import { createHash } from "node:crypto";
import { spawnSync } from "node:child_process";
import { fileURLToPath, pathToFileURL } from "node:url";
import { SITE, GITHUB } from "./src/urls.mjs";
import { WEBSITE, ORG, ldScript, gitDates, newest } from "./src/jsonld.mjs";

/** Files the merged folder must have: the landing's <head> points at the icons and og.png, which only the docs write. */
const REQUIRED = ["favicon.svg", "favicon-32.png", "apple-touch-icon.png", "og.png", "mistgate-badge.svg", "robots.txt", "sitemap.xml", "404.html", "_headers", "_redirects", "install.sh"];
const SECURITY = ["x-content-type-options", "x-frame-options", "referrer-policy", "permissions-policy", "content-security-policy"];

// a file of the merged folder: {from: path on disk} or {data: string|Buffer}; {landing: true} once the landing wrote it
const text = (e) => (e.data ?? readFileSync(e.from)).toString("utf8");
const NONE = { data: "" };
const attr = (tag, name) => new RegExp(`\\s${name}="([^"]*)"`).exec(tag)?.[1];
const tagsOf = (html, name) => [...html.matchAll(new RegExp(`<${name}\\b[^>]*>`, "g"))].map((m) => m[0]);

// ---- _headers: Cloudflare Pages semantics (miniflare's asset worker, attachCustomHeaders): the rules that match a path run in
// file order; a rule first deletes the headers it unsets (`! Name`), then sets its own; a name set again is joined with ", ".
export const parseHeaders = (t) => {
  const rules = [];
  for (const l of t.split("\n")) if (l.trim()) l.startsWith(" ") ? rules.at(-1).lines.push(l.trim()) : rules.push({ path: l.trim(), lines: [] });
  return rules;
};
const showHeaders = (rules) => rules.map((r) => `${r.path}\n${r.lines.map((l) => "  " + l).join("\n")}`).join("\n\n") + "\n";
export function headersFor(rules, path) {
  const h = new Map();
  for (const r of rules) {
    if (!new RegExp("^" + r.path.split("*").map((s) => s.replace(/[.+?^${}()|[\]\\]/g, "\\$&")).join("(.*)") + "$").test(path)) continue;
    for (const l of r.lines) if (l.startsWith("!")) h.delete(l.slice(1).trim().toLowerCase());
    for (const l of r.lines) {
      if (l.startsWith("!")) continue;
      const i = l.indexOf(":"), k = l.slice(0, i).toLowerCase(), v = l.slice(i + 1).trim();
      h.set(k, h.has(k) ? `${h.get(k)}, ${v}` : v);
    }
  }
  return h;
}
const CSP = "Content-Security-Policy";
/** The docs' rules stay as they are (their CSP, with the hash of the theme script, is the default under /*); every page of the
 *  landing gets `! CSP` and its own policy in the rule of its address. Were the reset ignored, the two policies would be joined
 *  and both enforced, which still leaves the landing at its own, stricter, script-src. */
export function mergeHeaders(docs, landing, errors) {
  const d = parseHeaders(docs), l = parseHeaders(landing);
  const dAll = d.find((r) => r.path === "/*"), lAll = l.find((r) => r.path === "/*");
  const landingCsp = lAll?.lines.find((x) => x.startsWith(CSP + ":"));
  if (!dAll?.lines.some((x) => x.startsWith(CSP + ":")) || !landingCsp) { errors.push("_headers: both sides must set a Content-Security-Policy under /*"); return docs; }
  for (const x of lAll.lines) if (x !== landingCsp && !dAll.lines.includes(x)) errors.push(`_headers: the landing's "${x}" under /* is not in the docs' /* rule`);
  const out = [...d];
  for (const r of l) {
    if (r === lAll) continue;
    const same = d.find((x) => x.path === r.path);
    if (same) { if (same.lines.join("\n") !== r.lines.join("\n")) errors.push(`_headers: ${r.path} differs between the docs and the landing`); continue; }
    out.push({ path: r.path, lines: ["! " + CSP, landingCsp, ...r.lines] });
  }
  if (out.length > 100) errors.push(`_headers: ${out.length} rules, Cloudflare Pages reads 100`);
  return showHeaders(out);
}

// ---- schema.org of a landing page, read from its own <head>: the facts are the page's (description, language, address)
const unesc = (s) => s.replace(/&(#x[0-9a-f]+|#\d+|amp|lt|gt|quot|apos);/gi, (m, e) => ({ amp: "&", lt: "<", gt: ">", quot: '"', apos: "'" })[e.toLowerCase()] ?? String.fromCodePoint(e[1] === "x" ? parseInt(e.slice(2), 16) : +e.slice(1)));
/** The home page: the program, the site, the project. /app/: the Windows client (no version or download: the page has neither). */
export function landingLd(html) {
  const description = unesc(attr(tagsOf(html, "meta").find((t) => attr(t, "name") === "description"), "content") ?? "");
  const url = attr(tagsOf(html, "link").find((t) => attr(t, "rel") === "canonical"), "href");
  const inLanguage = /<html\b[^>]*\slang="([^"]+)"/.exec(html)?.[1];
  const app = { "@type": "SoftwareApplication", name: "Mistgate", applicationCategory: "SecurityApplication", url, description, inLanguage, publisher: { "@id": ORG["@id"] } };
  if (new URL(url).pathname.endsWith("/app/")) return [{ ...app, operatingSystem: "Windows" }];
  return [
    { ...app, operatingSystem: "Linux", offers: { "@type": "Offer", price: "0", priceCurrency: "USD" }, license: "https://www.gnu.org/licenses/agpl-3.0.html", codeRepository: GITHUB, sameAs: [GITHUB] },
    { ...WEBSITE, inLanguage, publisher: { "@id": ORG["@id"] } }, ORG,
  ];
}
const withLd = (html) => html.replace("</head>", () => ldScript(landingLd(html)) + "\n</head>");

// ---- sitemap: the docs', plus every landing page with its hreflang set (lastmod: the last change of the design, when git knows it)
export function mergeSitemap(docs, landingPages, lastmod) {
  const kept = docs.match(/<url>[\s\S]*?<\/url>/g);
  const added = landingPages.map((html) => {
    const alts = tagsOf(html, "link").filter((t) => attr(t, "rel") === "alternate" && attr(t, "hreflang"));
    const loc = attr(tagsOf(html, "link").find((t) => attr(t, "rel") === "canonical"), "href");
    return `<url><loc>${loc}</loc>${lastmod ? `<lastmod>${lastmod}</lastmod>` : ""}\n${alts.map((a) => `  <xhtml:link rel="alternate" hreflang="${attr(a, "hreflang")}" href="${attr(a, "href")}"/>`).join("\n")}\n</url>`;
  });
  return docs.slice(0, docs.indexOf("<url>")) + [...added, ...kept].join("\n") + "\n</urlset>\n";
}

// ---- the checks on the merged folder
export function check(files, errors) {
  const html = new Map([...files].filter(([p]) => p.endsWith(".html")).map(([p, e]) => [p, text(e)]));
  const ids = new Map();
  const idsOf = (p) => ids.get(p) ?? ids.set(p, new Set([...html.get(p).matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]))).get(p);
  const isLanding = (p) => files.get(p)?.landing;
  const broken = new Set();
  // links: every root-relative href/src (and an absolute one at SITE, as in <meta og:image> or the canonical) is a file; a #fragment is an id
  for (const [from, h] of html) {
    const refs = [...h.matchAll(/\s(?:href|src|data-index)="([^"]*)"/g), ...h.matchAll(new RegExp(`\\scontent="(${SITE}[^"]*)"`, "g"))].map((m) => m[1]);
    for (const raw of refs) {
      let r = raw.replaceAll("&amp;", "&");
      if (r.startsWith(SITE + "/")) r = r.slice(SITE.length);
      if (/^([a-z][a-z0-9+.-]*:|\/\/)/i.test(r)) continue; // another site, mailto:, data:
      const [pq, ...rest] = r.split("#"), frag = rest.length ? decodeURIComponent(rest.join("#")) : "";
      let path = pq.split("?")[0];
      path = path === "" ? "/" + from : decodeURIComponent(path.startsWith("/") ? path : posix.join("/" + posix.dirname(from) + "/", path));
      const file = path.endsWith("/") ? path.slice(1) + "index.html" : path.slice(1);
      // the landing's own anchors may be made by its scripts at run time: not checked, as in landing/gen
      const ok = files.has(file) && (!frag || ["top", "content", "nav"].includes(frag) || !html.has(file) || (isLanding(from) && isLanding(file)) || idsOf(file).has(frag));
      if (!ok) broken.add(`${from}: ${raw}`);
    }
  }
  if (broken.size) errors.push(`${broken.size} broken link(s):\n    ` + [...broken].slice(0, 40).join("\n    ") + (broken.size > 40 ? `\n    ... and ${broken.size - 40} more` : ""));
  // one of each shared file
  for (const f of REQUIRED) if (!files.has(f)) errors.push(`${f} is missing`);
  const robots = files.has("robots.txt") ? text(files.get("robots.txt")) : "";
  if (!robots.includes(`Sitemap: ${SITE}/sitemap.xml`)) errors.push("robots.txt does not point at the sitemap");
  for (const l of text(files.get("_redirects") ?? NONE).split("\n").filter(Boolean)) {
    const to = l.split(" ")[1];
    if (!files.has(to.endsWith("/") ? to.slice(1) + "index.html" : to.slice(1))) errors.push(`_redirects: ${l} leads nowhere`);
  }
  // sitemap: exactly the pages that name themselves canonical, every address a file
  const sm = text(files.get("sitemap.xml") ?? NONE), locs = [...sm.matchAll(/<loc>([^<]+)<\/loc>/g)].map((m) => m[1]);
  const canon = [...html].flatMap(([, h]) => tagsOf(h, "link").filter((t) => attr(t, "rel") === "canonical").map((t) => attr(t, "href")));
  const sSet = new Set(locs), cSet = new Set(canon);
  if (sSet.size !== locs.length) errors.push("sitemap.xml lists an address twice");
  for (const u of cSet) if (!sSet.has(u)) errors.push(`sitemap.xml lacks ${u}`);
  for (const u of sSet) if (!cSet.has(u)) errors.push(`sitemap.xml lists ${u}, which no page names canonical`);
  for (const m of sm.matchAll(/(?:<loc>|href=")([^<"]+)/g)) {
    const p = m[1].replace(SITE, "");
    if (!files.has(p.endsWith("/") ? p.slice(1) + "index.html" : p.slice(1))) errors.push(`sitemap.xml: ${m[1]} is not a file`);
  }
  // headers: the policy every page really gets (see headersFor), no two joined, every inline script hashed in it
  const rules = parseHeaders(text(files.get("_headers") ?? NONE));
  const policies = new Map();
  for (const [p, h] of html) {
    const url = "/" + p.replace(/index\.html$/, ""), hd = headersFor(rules, url), csp = hd.get("content-security-policy");
    for (const k of SECURITY) if (!hd.has(k)) errors.push(`${url}: no ${k} header`);
    if (!csp) continue;
    if (csp.includes(",")) { errors.push(`${url}: the policies were joined: ${csp}`); continue; }
    policies.set((isLanding(p) ? "landing" : "docs") + " " + csp, url);
    const script = (/(?:^|;)\s*script-src ([^;]*)/.exec(csp) ?? [])[1] ?? "";
    for (const m of h.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)) {
      if (/\ssrc=/.test(m[1]) || /type="(?!module|text\/javascript)/.test(m[1])) continue; // a file, or a data block
      if (!script.includes(`'sha256-${createHash("sha256").update(m[2]).digest("base64")}'`)) errors.push(`${url}: an inline script the policy does not allow`);
    }
    if (isLanding(p) && script !== "'self'") errors.push(`${url}: the landing's script-src is ${script}, expected 'self'`);
  }
  return policies;
}

/** docs, landing: Map<path without a leading slash, Buffer | string | {from: file}>. -> {files, errors, policies} */
export function merge(docs, landing, lastmod) {
  const errors = [], files = new Map(docs), lower = new Map([...docs.keys()].map((p) => [p.toLowerCase(), p]));
  for (const [p, e] of landing) {
    if (p === "_headers") continue;
    if (files.has(p)) errors.push(`both the docs and the landing write ${p}`);
    else if (lower.has(p.toLowerCase()) && lower.get(p.toLowerCase()) !== p) errors.push(`${p} and ${lower.get(p.toLowerCase())} differ only in case`);
    else files.set(p, { ...e, landing: true });
  }
  files.set("_headers", { data: mergeHeaders(text(docs.get("_headers") ?? NONE), text(landing.get("_headers") ?? NONE), errors) });
  const pages = [];
  for (const [p, e] of landing) if (p.endsWith("index.html")) { const html = withLd(text(e)); pages.push(html); files.set(p, { data: html, landing: true }); }
  if (docs.has("sitemap.xml") && pages.length) files.set("sitemap.xml", { data: mergeSitemap(text(docs.get("sitemap.xml")), pages, lastmod) });
  const policies = check(files, errors);
  return { files, errors, policies };
}

// ---- the command
const ROOT = dirname(fileURLToPath(import.meta.url));
const walk = (d, base = d) => readdirSync(d).flatMap((n) => { const p = join(d, n); return statSync(p).isDirectory() ? walk(p, base) : [[relative(base, p).replace(/\\/g, "/"), { from: p }]]; });

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const DOCS = join(ROOT, "dist"), LAND = join(ROOT, "landing"), OUT = join(ROOT, "public");
  const run = (cmd, args, cwd) => {
    console.log(`\n$ ${cmd} ${args.join(" ")}   (${relative(ROOT, cwd) || "."})`);
    const r = spawnSync(cmd, args, { cwd, stdio: "inherit", shell: process.platform === "win32" && cmd === "pnpm" });
    if (r.status !== 0) { console.error(`${cmd} failed`); process.exit(1); }
  };
  if (!process.argv.includes("--no-build")) {
    run("node", ["build.mjs"], ROOT);
    run("pnpm", ["build"], join(LAND, "pipeline"));
    // cwd gen/: rustup picks the toolchain of gen/rust-toolchain.toml from the working directory
    run("cargo", ["run", "--release", "--", "--build", join(LAND, "build"), "--out", join(LAND, "dist"), "--docs-dist", DOCS], join(LAND, "gen"));
  }
  const { files, errors, policies } = merge(new Map(walk(DOCS)), new Map(walk(join(LAND, "dist"))), newest(gitDates(join(LAND, "design")).values()));
  if (errors.length) { console.error(`\nmerge: ${errors.length} problem(s)\n` + errors.map((e) => "  - " + e).join("\n")); rmSync(OUT, { recursive: true, force: true }); process.exit(1); }
  rmSync(OUT, { recursive: true, force: true });
  for (const [p, e] of files) {
    mkdirSync(dirname(join(OUT, p)), { recursive: true });
    if (e.from) copyFileSync(e.from, join(OUT, p)); else writeFileSync(join(OUT, p), e.data);
  }
  console.log(`\nmerged ${files.size} files -> public/ (${[...files.values()].filter((e) => e.landing).length} from the landing); policies in force:`);
  for (const [k, url] of policies) console.log(`  ${k.split(" ")[0]} (e.g. ${url}): ${k.slice(k.indexOf(" ") + 1)}`);
}
