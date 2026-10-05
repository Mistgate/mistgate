import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const siteRoot = dirname(dirname(fileURLToPath(import.meta.url)));
const dist = join(siteRoot, "dist");

function page(lang, key) {
  const prefix = lang === "en" ? "" : `${lang}/`;
  const path = key === "index" ? `${prefix}index.html` : `${prefix}${key}/index.html`;
  return readFileSync(join(dist, path), "utf8");
}

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
    for (const key of ["getting-started/ai-agents", "getting-started/ssh-install", "guide/torrent-protection", "operations/releases", "roadmap/status"]) {
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
