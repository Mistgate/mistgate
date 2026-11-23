// schema.org JSON-LD and the dates (sitemap lastmod, dateModified). Docs pages are read from dist/ (pnpm build first).
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { execFileSync } from "node:child_process";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { gitDates, ldScript } from "../src/jsonld.mjs";
import { landingLd, mergeSitemap } from "../merge.mjs";
import { LANGS } from "../src/urls.mjs";

const root = dirname(dirname(fileURLToPath(import.meta.url)));
const dist = join(root, "dist");
const W3C = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2})$/;
const hasHistory = (() => { try { return execFileSync("git", ["-C", root, "rev-parse", "--is-shallow-repository"], { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim() === "false"; } catch { return false; } })();

/** the one JSON-LD script of a page, parsed: its @graph by @type */
function ld(html) {
  const blocks = [...html.matchAll(/<script type="application\/ld\+json">([\s\S]*?)<\/script>/g)];
  assert.equal(blocks.length, 1, "exactly one JSON-LD script");
  const doc = JSON.parse(blocks[0][1]);
  assert.equal(doc["@context"], "https://schema.org");
  return Object.fromEntries(doc["@graph"].map((n) => [n["@type"], n]));
}

test("a docs page: TechArticle and BreadcrumbList, in its own language", () => {
  for (const lang of LANGS) {
    const base = lang === "en" ? "" : `/${lang}`;
    const home = ld(readFileSync(join(dist, base.slice(1), "docs/index.html"), "utf8"));
    assert.equal(home.TechArticle.inLanguage.slice(0, 2), lang);
    assert.equal(home.BreadcrumbList, undefined, `${lang}: the home has no trail`);
    const html = readFileSync(join(dist, base.slice(1), "getting-started/overview/index.html"), "utf8");
    const g = ld(html), a = g.TechArticle;
    assert.equal(a.url, `https://mistgate.app${base}/getting-started/overview/`);
    assert.equal(a.headline, /<h1 class="doc-h1">([^<]*)<\/h1>/.exec(html)[1].replace(/&amp;/g, "&"));
    assert.ok(a.description && a.inLanguage && a.isPartOf.url === "https://mistgate.app/");
    const items = g.BreadcrumbList.itemListElement;
    assert.deepEqual(items.map((i) => i.position), items.map((_, i) => i + 1));
    assert.equal(items[0].item, `https://mistgate.app${base}/docs/`);
    assert.equal(items.at(-1).item, a.url);
    assert.ok(items.every((i) => i.name && i.item.startsWith("https://mistgate.app/")));
    if (a.dateModified) assert.match(a.dateModified, W3C);
  }
});

test("sitemap.xml has a W3C lastmod on every docs address when git history is there", { skip: !hasHistory && "no full git history" }, () => {
  const urls = readFileSync(join(dist, "sitemap.xml"), "utf8").match(/<url>[\s\S]*?<\/url>/g);
  assert.ok(urls.length > 20);
  for (const u of urls) assert.match(/<lastmod>([^<]+)<\/lastmod>/.exec(u)?.[1] ?? "", W3C, u.slice(0, 80));
});

const head = (url, lang, desc) => `<!doctype html><html lang="${lang}"><head><meta name="description" content="${desc}"><link rel="canonical" href="${url}"></head><body></body></html>`;

test("the landing: the home of every language and /app/ get a JSON-LD built from their own head", () => {
  for (const lang of LANGS) {
    const url = lang === "en" ? "https://mistgate.app/" : `https://mistgate.app/${lang}/`;
    const g = Object.fromEntries(landingLd(head(url, lang, `A &amp; B &quot;${lang}&quot; &lt;/script&gt;`)).map((n) => [n["@type"], n]));
    const s = g.SoftwareApplication;
    assert.deepEqual([s.name, s.operatingSystem, s.url, s.inLanguage, s.description], ["Mistgate", "Linux", url, lang, `A & B "${lang}" </script>`]);
    assert.equal(s.offers.price, "0");
    assert.match(s.license, /agpl-3\.0/);
    assert.equal(s.codeRepository, "https://github.com/Mistgate/mistgate");
    assert.ok(!("softwareVersion" in s) && !("aggregateRating" in s), "no invented fields");
    assert.equal(g.WebSite.url, "https://mistgate.app/");
    assert.equal(g.Organization.logo, "https://mistgate.app/mistgate-badge.svg");
    // the script cannot be closed from inside, and it parses back to the same text
    const script = ldScript(landingLd(head(url, lang, "x &lt;/script&gt;&lt;b&gt;")));
    assert.equal(script.match(/<\/script>/g).length, 1);
    assert.equal(ld(`<head>${script}</head>`).SoftwareApplication.description, "x </script><b>");
  }
  const [app] = landingLd(head("https://mistgate.app/ru/app/", "ru", "Клиент"));
  assert.deepEqual([app["@type"], app.operatingSystem, app.description], ["SoftwareApplication", "Windows", "Клиент"]);
  assert.ok(!("softwareVersion" in app) && !("downloadUrl" in app) && !("offers" in app));
});

test("the landing's lastmod is written when there is a date, and left out when there is not", () => {
  const pages = [head("https://mistgate.app/", "en", "d")];
  const sm = "<urlset>\n<url><loc>x</loc></url>\n</urlset>\n";
  assert.match(mergeSitemap(sm, pages, "2026-11-23T10:00:00+03:00"), /<loc>https:\/\/mistgate\.app\/<\/loc><lastmod>2026-11-23T10:00:00\+03:00<\/lastmod>/);
  assert.doesNotMatch(mergeSitemap(sm, pages), /lastmod/);
});

test("dates: a folder outside any repository gives none, and nothing throws", () => {
  const d = mkdtempSync(join(tmpdir(), "seo-nogit-"));
  try { assert.equal(gitDates(d).size, 0); } finally { rmSync(d, { recursive: true, force: true }); }
  if (hasHistory) {
    const m = gitDates(join(root, "..", "docs"));
    assert.ok(m.size > 20 && m.get("en/index.md") && [...m.values()].every((v) => W3C.test(v)));
  }
});
