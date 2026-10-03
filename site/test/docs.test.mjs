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

test("M2 roadmap pages report shipped SSH provisioning and remaining backup work", () => {
  const en = page("en", "roadmap/m2-ssh-provisioning");
  const ru = page("ru", "roadmap/m2-ssh-provisioning");

  assert.ok(en.includes("M2: SSH provisioning and recovery"));
  assert.ok(en.includes("Go SSH installer"));
  assert.ok(en.includes("/nodes/install"));
  assert.ok(en.includes("M2 is not complete"));
  assert.ok(en.includes("password rotation"));
  assert.ok(ru.includes("M2: установка по SSH и восстановление"));
  assert.ok(ru.includes("Go-мастер"));
  assert.ok(ru.includes("/nodes/install"));
  assert.ok(ru.includes("M2 не завершён"));
  assert.ok(ru.includes("смена пароля"));
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
