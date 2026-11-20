// merge.mjs on small in-memory trees: the merged folder is accepted, and each kind of mistake is refused.
import test from "node:test";
import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { merge, headersFor, parseHeaders } from "../merge.mjs";

const SITE = "https://mistgate.app";
const INIT = "document.documentElement.dataset.theme='dark'";
const HASH = createHash("sha256").update(INIT).digest("base64");
const csp = (script) => `default-src 'self'; script-src ${script}; style-src 'self' 'unsafe-inline'; object-src 'none'`;
const common = "/*\n  X-Content-Type-Options: nosniff\n  Referrer-Policy: no-referrer\n  X-Frame-Options: DENY\n  Permissions-Policy: camera=()\n";
const DOCS_HEADERS = `${common}  Content-Security-Policy: ${csp(`'self' 'sha256-${HASH}'`)}\n\n/assets/*\n  Cache-Control: public, max-age=31536000, immutable\n`;
const LAND_HEADERS = `${common}  Content-Security-Policy: ${csp("'self'")}\n\n/assets/*\n  Cache-Control: public, max-age=31536000, immutable\n\n/\n  Cache-Control: public, max-age=0, must-revalidate\n\n/app/\n  Cache-Control: public, max-age=0, must-revalidate\n`;

const page = (url, body, { inline = "", ids = [] } = {}) => ({
  data: `<!doctype html><head><link rel="canonical" href="${SITE}${url}"><link rel="alternate" hreflang="en" href="${SITE}${url}"></head><body>${inline ? `<script>${inline}</script>` : ""}${ids.map((i) => `<h2 id="${i}"></h2>`).join("")}${body}</body>`,
});
const url = (p) => "/" + p.replace(/index\.html$/, "");
const tree = (pages, extra = {}) => new Map(Object.entries({ ...Object.fromEntries(Object.entries(pages).map(([p, v]) => [p, page(url(p), v.body ?? v, v)])), ...extra }));

const docs = () => tree({
  "index.html": { body: '<a href="/guide/x/">x</a>', inline: INIT },
  "guide/x/index.html": { body: '<a href="/">home</a><a href="/app/">app</a><a href="#sec">s</a><img src="/og.png">', inline: INIT, ids: ["sec"] },
}, {
  "404.html": { data: `<a href="/">h</a><script>${INIT}</script>` },
  "favicon.svg": { data: "" }, "favicon-32.png": { data: "" }, "apple-touch-icon.png": { data: "" }, "og.png": { data: "" }, "mistgate-badge.svg": { data: "" }, "install.sh": { data: "" },
  "robots.txt": { data: `Sitemap: ${SITE}/sitemap.xml\n` }, "_redirects": { data: "/old/ /guide/x/ 301\n" }, "_headers": { data: DOCS_HEADERS },
  "sitemap.xml": { data: `<urlset>\n<url><loc>${SITE}/</loc></url>\n<url><loc>${SITE}/guide/x/</loc></url>\n</urlset>\n` },
});
const landing = () => tree({
  "index.html": { body: '<a href="/guide/x/#sec">docs</a><a href="/app/#made-by-script">app</a><link href="/assets/a.css"><script type="application/json">{"a":1}</script>' },
  "app/index.html": '<a href="/">main</a>',
}, { "_headers": { data: LAND_HEADERS }, "assets/a.css": { data: "" } });
const run = (d = docs(), l = landing()) => merge(d, l);

test("a clean pair merges: the landing replaces the docs home, nothing else of the docs moves", () => {
  const { files, errors } = run();
  assert.deepEqual(errors, []);
  assert.ok(files.get("index.html").landing && !files.get("guide/x/index.html").landing);
  assert.ok(files.get("favicon.svg") && files.get("404.html") && files.get("_redirects"));
});

test("the policy of each page: the docs get the hash, the landing its own, never two joined", () => {
  const { files, policies } = run();
  const rules = parseHeaders(files.get("_headers").data);
  const at = (p) => headersFor(rules, p).get("content-security-policy");
  assert.equal(at("/"), csp("'self'"));
  assert.equal(at("/app/"), csp("'self'"));
  assert.equal(at("/guide/x/"), csp(`'self' 'sha256-${HASH}'`));
  assert.equal(at("/404.html"), csp(`'self' 'sha256-${HASH}'`));
  assert.equal(headersFor(rules, "/").get("cache-control"), "public, max-age=0, must-revalidate");
  assert.equal(headersFor(rules, "/assets/a.css").get("cache-control"), "public, max-age=31536000, immutable");
  assert.equal(policies.size, 2);
});

test("Pages semantics: a rule unsets before it sets; a name set twice is joined", () => {
  const rules = parseHeaders("/*\n  A: 1\n\n/x/\n  ! A\n  A: 2\n\n/x/\n  A: 3\n");
  assert.equal(headersFor(rules, "/x/").get("a"), "2, 3");
  assert.equal(headersFor(rules, "/y/").get("a"), "1");
});

test("the sitemap has the landing pages with hreflang and not the replaced docs home", () => {
  const sm = run().files.get("sitemap.xml").data;
  assert.match(sm, /<loc>https:\/\/mistgate\.app\/app\/<\/loc>/);
  assert.match(sm, /hreflang="en" href="https:\/\/mistgate\.app\/"/);
  assert.equal(sm.match(/<loc>https:\/\/mistgate\.app\/<\/loc>/g).length, 1);
  assert.match(sm, /guide\/x\//);
});

const refused = (d, l, re) => assert.match(run(d, l).errors.join("\n"), re);

test("a broken link is refused, from the docs, from the landing, and a missing #fragment of a docs page", () => {
  const d = docs(); d.set("guide/x/index.html", page("/guide/x/", '<a href="/nope/">x</a>', { inline: INIT, ids: ["sec"] }));
  refused(d, landing(), /guide\/x\/index\.html: \/nope\//);
  const l = landing(); l.set("app/index.html", page("/app/", '<a href="/ru/guide/x/">x</a>'));
  refused(docs(), l, /app\/index\.html: \/ru\/guide\/x\//);
  const f = landing(); f.set("app/index.html", page("/app/", '<a href="/guide/x/#gone">x</a>'));
  refused(docs(), f, /#gone/);
  const og = docs(); og.delete("og.png");
  refused(og, landing(), /og\.png is missing/);
});

test("two writers of one path are refused, and so is a case-only twin", () => {
  const l = landing(); l.set("guide/x/index.html", page("/guide/x/", ""));
  refused(docs(), l, /both the docs and the landing write guide\/x\/index\.html/);
  const c = landing(); c.set("Guide/x/index.html", page("/Guide/x/", ""));
  refused(docs(), c, /differ only in case/);
  const a = landing(); a.set("robots.txt", { data: "" });
  refused(docs(), a, /both the docs and the landing write robots\.txt/);
});

test("an inline script must be hashed in the policy of its page; the landing allows none", () => {
  const d = docs(); d.set("guide/x/index.html", page("/guide/x/", '<script>alert(1)</script>', { ids: ["sec"] }));
  refused(d, landing(), /guide\/x\/: an inline script/);
  const l = landing(); l.set("app/index.html", page("/app/", "", { inline: INIT }));
  refused(docs(), l, /\/app\/: an inline script/);
});

test("headers: a landing rule that differs from the docs' own, or a policy that is not reset, is refused", () => {
  const l = landing(); l.set("_headers", { data: LAND_HEADERS.replace("max-age=31536000", "max-age=1") });
  refused(docs(), l, /_headers: \/assets\/\* differs/);
  const n = landing(); n.set("_headers", { data: LAND_HEADERS.replace(/  Content-Security-Policy.*\n/, "") });
  refused(docs(), n, /both sides must set a Content-Security-Policy/);
});

test("the sitemap and the canonical links agree", () => {
  const d = docs(); d.set("guide/y/index.html", page("/guide/y/", "", { inline: INIT }));
  refused(d, landing(), /sitemap\.xml lacks https:\/\/mistgate\.app\/guide\/y\//);
});
