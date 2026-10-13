import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const siteRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const repositoryRoot = dirname(siteRoot);
const dist = join(siteRoot, "dist");

function page(lang, key) {
  const prefix = lang === "en" ? "" : `${lang}/`;
  const path = key === "index" ? `${prefix}index.html` : `${prefix}${key}/index.html`;
  return readFileSync(join(dist, path), "utf8");
}

test("the install script is published at the stable URL as plain text", () => {
  const published = readFileSync(join(dist, "install.sh"));
  const source = readFileSync(join(repositoryRoot, "scripts", "install.sh"));
  const headers = readFileSync(join(dist, "_headers"), "utf8");
  assert.deepEqual(published, source);
  assert.ok(headers.includes("/install.sh\n  Content-Type: text/plain; charset=utf-8"));
});

test("the status page replaces the M2 roadmap page in both languages", () => {
  const en = page("en", "roadmap/status");
  const ru = page("ru", "roadmap/status");
  assert.ok(en.includes("Status and roadmap"));
  assert.ok(en.includes("Known limits"));
  assert.ok(en.includes('href="/getting-started/ssh-install/"'));
  assert.ok(ru.includes("Статус и план развития"));
  assert.ok(ru.includes("Известные ограничения"));
  assert.ok(ru.includes('href="/ru/getting-started/ssh-install/"'));
  assert.ok(en.includes("github.com/Mistgate/mistgate/edit/main/docs/en/roadmap/status.md"));
  assert.ok(ru.includes("github.com/Mistgate/mistgate/edit/main/docs/ru/roadmap/status.md"));
});

test("the status page draws its roadmap block as phases with cards, listed and searchable", () => {
  for (const [lang, base, phases, card] of [["en", "", ["Released", "Now", "Next", "Later", "Road to 1.0"], "Subscription mirrors"], ["ru", "/ru", ["Выпущено", "Сейчас", "Дальше", "Позже", "Путь к 1.0"], "Зеркала подписки"]]) {
    const html = page(lang, "roadmap/status");
    assert.ok(!html.includes("language-roadmap") && !html.includes("status: released"), `${lang}: the block is drawn, not shown as code`);
    assert.deepEqual([...html.matchAll(/<li class="rm-phase" data-status="(\w+)">/g)].map((m) => m[1]), ["released", "now", "next", "later", "goal"]);
    const h2 = [...html.matchAll(/<h2 id="([^"]+)"[^>]*>([^<]+)/g)].map((m) => [m[1], m[2]]);
    assert.deepEqual(h2.slice(0, 5).map((x) => x[1]), phases);
    for (const [id] of h2.slice(0, 5)) assert.ok(html.includes(`<a href="#${id}">`), `${lang}: the phase strip links to #${id}`);
    const toc = /<aside class="toc"[\s\S]*?<\/aside>/.exec(html)[0];
    assert.ok(toc.includes(phases[2]) && !toc.includes(card), `${lang}: the table of contents lists phases, not cards`);
    assert.ok(html.includes(`<h3 id=`) && html.includes(card));
    assert.ok(html.includes(`href="${base}/getting-started/install-edge/"`), `${lang}: card links point to site pages`);
    assert.ok(html.includes('<span class="rm-ver">v0.1.36</span>'));
    const index = JSON.parse(readFileSync(join(dist, lang === "en" ? "search-index.json" : "ru/search-index.json"), "utf8")).find((p) => p.u === `${base}/roadmap/status/`);
    assert.ok(index.h.some(([t]) => t === card), `${lang}: cards are in the search index`);
    assert.ok(index.k.some(([c]) => c === "mistgate edge deploy"), `${lang}: names in cards are searchable`);
    assert.ok(page(lang, "index").includes(`href="${base}/roadmap/status/"`), `${lang}: the home page links to the status page`);
  }
});

test("the old M2 address redirects to the status page", () => {
  const redirects = readFileSync(join(dist, "_redirects"), "utf8");
  assert.ok(redirects.includes("/roadmap/m2-ssh-provisioning/ /roadmap/status/ 301"));
  assert.ok(redirects.includes("/ru/roadmap/m2-ssh-provisioning/ /ru/roadmap/status/ 301"));
});

test("encrypted panel backup guides are published in both languages", () => {
  const en = page("en", "operations/backups");
  const ru = page("ru", "operations/backups");
  assert.ok(en.includes("Cloudflare R2"));
  assert.ok(en.includes("age identity"));
  assert.ok(en.includes("Restore into a "));
  assert.ok(ru.includes("Cloudflare R2"));
  assert.ok(ru.includes("закрытый age-ключ"));
  assert.ok(ru.includes("каталог данных"));
});

test("the AI agent guide carries the three prompts in both languages, and llms.txt points to it", () => {
  const en = page("en", "getting-started/ai-agents");
  const ru = page("ru", "getting-started/ai-agents");
  const llms = readFileSync(join(dist, "llms.txt"), "utf8");
  assert.ok(en.includes("Install Mistgate with an AI agent"));
  assert.ok(en.includes("mistgate setup --public-url"));
  assert.ok(en.includes("node_install_plan"));
  assert.ok(!en.includes("confirmed_fingerprint")); // the owner confirms the host key on the approval screen
  assert.ok(en.includes("approval card"));
  assert.ok(en.includes("AGENTS.md"));
  assert.ok(ru.includes("Установка Mistgate с AI-агентом"));
  assert.ok(ru.includes("node_server_password_rotate_apply"));
  assert.ok(ru.includes("Ты устанавливаешь VPN-панель Mistgate"));
  assert.ok(llms.includes("AI agent guide"));
  assert.ok(llms.includes("/ru/getting-started/ai-agents/"));
});

test("the new pages appear in both documentation sidebars", () => {
  for (const [lang, base] of [["en", ""], ["ru", "/ru"]]) {
    const index = page(lang, "index");
    for (const key of ["getting-started/ai-agents", "getting-started/ssh-install", "guide/torrent-protection", "operations/releases", "operations/panel-watcher", "roadmap/status"]) {
      assert.ok(index.includes(`${base}/${key}/`), `${lang} sidebar lists ${key}`);
    }
    assert.ok(!index.includes("m2-ssh-provisioning"), `${lang} sidebar has no M2 page`);
  }
});

test("node guides show the SSH install choice, and updates show the published panel update path", () => {
  const addEn = page("en", "getting-started/add-node");
  const addRu = page("ru", "getting-started/add-node");
  const sshEn = page("en", "getting-started/ssh-install");
  const sshRu = page("ru", "getting-started/ssh-install");
  const updatesEn = page("en", "operations/updates");
  const updatesRu = page("ru", "operations/updates");

  assert.ok(addEn.includes("Install automatically over SSH"));
  assert.ok(addEn.includes('href="/getting-started/ssh-install/"'));
  assert.ok(addRu.includes("Запустить автоустановку по SSH"));
  assert.ok(addRu.includes('href="/ru/getting-started/ssh-install/"'));
  assert.ok(sshEn.includes("Saved SSH access"));
  assert.ok(sshEn.includes("--token-stdin"));
  assert.ok(sshRu.includes("Сохранённый SSH-доступ"));
  assert.ok(updatesEn.includes("current stable release"));
  assert.ok(updatesEn.includes("predate the panel updater"));
  assert.ok(updatesEn.includes("Check GitHub"));
  assert.ok(updatesRu.includes("текущего стабильного релиза"));
  assert.ok(updatesRu.includes("появились до самообновления панели"));
  assert.ok(updatesRu.includes("Проверить GitHub"));
});
