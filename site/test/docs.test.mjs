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

test("M2 roadmap pages describe SSH provisioning and panel recovery", () => {
  const en = page("en", "roadmap/m2-ssh-provisioning");
  const ru = page("ru", "roadmap/m2-ssh-provisioning");

  assert.ok(en.includes("M2: SSH installation and recovery"));
  assert.ok(en.includes("four-step panel modal"));
  assert.ok(en.includes("Go-rendered"));
  assert.ok(en.includes("page remains the job and server-access manager"));
  assert.ok(en.includes("/nodes/install"));
  assert.ok(en.includes("M2 is complete when"));
  assert.ok(en.includes("Password rotation"));
  assert.ok(ru.includes("M2: установка по SSH и восстановление"));
  assert.ok(ru.includes("Четырёхшаговый мастер работает внутри модалки панели"));
  assert.ok(ru.includes("Go-страница"));
  assert.ok(ru.includes("остаётся менеджером заданий"));
  assert.ok(ru.includes("/nodes/install"));
  assert.ok(ru.includes("M2 завершён, когда"));
  assert.ok(ru.includes("сменой пароля"));
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

test("AI agent install guide and llms.txt are published in both languages", () => {
  const en = page("en", "getting-started/ai-agents");
  const ru = page("ru", "getting-started/ai-agents");
  const llms = readFileSync(join(dist, "llms.txt"), "utf8");
  assert.ok(en.includes("node_install_plan"));
  assert.ok(en.includes("confirmed_fingerprint"));
  assert.ok(ru.includes("node_server_password_rotate_apply"));
  assert.ok(llms.includes("AI agent guide"));
  assert.ok(llms.includes("/ru/getting-started/ai-agents/"));
});

test("M2 roadmap pages appear in both documentation sidebars", () => {
  assert.ok(page("en", "index").includes("/roadmap/m2-ssh-provisioning/"));
  assert.ok(page("ru", "index").includes("/ru/roadmap/m2-ssh-provisioning/"));
  assert.ok(page("en", "index").includes("/getting-started/ai-agents/"));
  assert.ok(page("ru", "index").includes("/ru/getting-started/ai-agents/"));
});

test("M2 pages link to the matching add-node guide and GitHub source", () => {
  const en = page("en", "roadmap/m2-ssh-provisioning");
  const ru = page("ru", "roadmap/m2-ssh-provisioning");

  assert.ok(en.includes('href="/getting-started/add-node/"'));
  assert.ok(ru.includes('href="/ru/getting-started/add-node/"'));
  assert.ok(en.includes("github.com/Mistgate/mistgate/edit/main/docs/en/roadmap/m2-ssh-provisioning.md"));
  assert.ok(ru.includes("github.com/Mistgate/mistgate/edit/main/docs/ru/roadmap/m2-ssh-provisioning.md"));
});

test("node guides show the SSH install choice and the published panel update path", () => {
  const addEn = page("en", "getting-started/add-node");
  const addRu = page("ru", "getting-started/add-node");
  const updatesEn = page("en", "operations/updates");
  const updatesRu = page("ru", "operations/updates");

  assert.ok(addEn.includes("Install automatically over SSH"));
  assert.ok(addEn.includes("Settings → System"));
  assert.ok(addRu.includes("Запустить автоустановку по SSH"));
  assert.ok(addRu.includes("Настройки → Система"));
  assert.ok(updatesEn.includes("current stable release"));
  assert.ok(updatesEn.includes("predate the panel updater"));
  assert.ok(updatesEn.includes("Check GitHub"));
  assert.ok(updatesRu.includes("текущего стабильного релиза"));
  assert.ok(updatesRu.includes("появились до самообновления панели"));
  assert.ok(updatesRu.includes("Проверить GitHub"));
});
